// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

func TestGitBaseOriginCredentials(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"}, Data: map[string][]byte{"token": []byte("private-token")}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	cr := &CredentialResolver{Client: cl, Namespace: "ns"}
	for _, root := range []string{"https://EXAMPLE.com/root?ref=v1", "http://example.com/root?ref=v1"} {
		r := withGitCreds(NewGitResolver(t.TempDir()), cr, &v1alpha1.SecretReference{Name: "s"}, root).(*gitResolver)
		for _, target := range []string{"https://example.com/base", "https://example.com:443/base", "https://example.com:0443/base", "https://example.com:444/base", "http://example.com/base", "https://other.example.com/base", "file:///tmp/base"} {
			auth, err := r.authFor(context.Background(), target)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.HasPrefix(root, "https:") && (target == "https://example.com/base" || target == "https://example.com:443/base" || target == "https://example.com:0443/base")
			if (auth != nil) != want {
				t.Fatalf("root %s target %s: auth present=%v want=%v", root, target, auth != nil, want)
			}
		}
	}
}

func TestGitBaseAcquisitionSnapshot(t *testing.T) {
	base, sha := makeLocalRepoTwoCommits(t)
	scratch := t.TempDir()
	r := NewGitResolver(scratch).(*gitResolver)
	first, err := r.acquire(context.Background(), base, sha)
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA != sha || first.Identity == "" || filepath.Dir(first.Root) != scratch {
		t.Fatal("invalid checkout identity/root")
	}
	if _, err := os.Stat(filepath.Join(first.Root, "later.txt")); !os.IsNotExist(err) {
		t.Fatalf("did not fetch historical SHA: %v", err)
	}
	if err := os.WriteFile(filepath.Join(first.Root, "seed", "kustomization.yaml"), []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	first.Cleanup()
	if err := os.RemoveAll(strings.TrimPrefix(base, "file://")); err != nil {
		t.Fatal(err)
	}
	second, err := r.acquire(context.Background(), base, sha)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Cleanup()
	if second.Identity != first.Identity {
		t.Fatal("identity changed")
	}
	data, err := os.ReadFile(filepath.Join(second.Root, "seed", "kustomization.yaml"))
	if err != nil || string(data) != "resources: []\n" {
		t.Fatalf("snapshot mutated: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(second.Root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("cache retained .git: %v", err)
	}
	second.Cleanup()
	assertScratchEmpty(t, scratch)
}

func TestGitBaseAuthorizationBeforeHits(t *testing.T) {
	base, sha := makeLocalRepo(t)
	scratch := t.TempDir()
	r := NewGitResolver(scratch).(*gitResolver)
	var calls int
	r.resolve = func(context.Context, gitOrigin) (creds, error) { calls++; return creds{}, nil }
	first, err := r.acquire(context.Background(), base, sha)
	if err != nil {
		t.Fatal(err)
	}
	first.Cleanup()
	if calls != 1 {
		t.Fatalf("credential reads=%d want 1", calls)
	}
	r.resolve = func(context.Context, gitOrigin) (creds, error) {
		return creds{}, errors.New("private-secret-do-not-echo")
	}
	if _, err := r.acquire(context.Background(), base, sha); err == nil || strings.Contains(err.Error(), "private-secret-do-not-echo") {
		t.Fatalf("credential failure bypassed/leaked: %v", err)
	}
	if _, err := r.ResolveID(context.Background(), base+"?ref="+sha, ModeSeed); err == nil {
		t.Fatal("bare SHA bypassed credentials")
	}
	assertScratchEmpty(t, scratch)
}

func TestGitBaseRefPreferenceAndMovement(t *testing.T) {
	base, old := makeLocalRepoTwoCommits(t)
	repo, err := git.PlainOpen(strings.TrimPrefix(base, "file://"))
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("pin"), head.Hash())); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateTag("pin", plumbing.NewHash(old), &git.CreateTagOptions{Tagger: &object.Signature{Name: "t", Email: "t@t"}, Message: "annotated"}); err != nil {
		t.Fatal(err)
	}
	r := NewGitResolver(t.TempDir()).(*gitResolver)
	first, err := r.acquire(context.Background(), base, "pin")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Cleanup()
	if first.SHA != old {
		t.Fatalf("peeled tag did not win: %s", first.SHA)
	}
	if err := repo.DeleteTag("pin"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateTag("pin", head.Hash(), nil); err != nil {
		t.Fatal(err)
	}
	second, err := r.acquire(context.Background(), base, "pin")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Cleanup()
	if second.SHA != head.Hash().String() || first.Identity == second.Identity {
		t.Fatal("moved tag served old identity")
	}
	if err := repo.DeleteTag("pin"); err != nil {
		t.Fatal(err)
	}
	third, err := r.acquire(context.Background(), base, "pin")
	if err != nil {
		t.Fatal(err)
	}
	defer third.Cleanup()
	if third.SHA != second.SHA {
		t.Fatal("branch fallback did not resolve")
	}
}

func TestGitBaseFailureCleanup(t *testing.T) {
	base, sha := makeLocalRepo(t)
	scratch := t.TempDir()
	r := NewGitResolver(scratch).(*gitResolver)
	for _, ref := range []string{"missing", strings.Repeat("f", 40), sha[:7]} {
		got, err := r.acquire(context.Background(), base, ref)
		if err == nil {
			got.Cleanup()
			t.Fatalf("unexpected success for %s", ref)
		}
		if !strings.Contains(err.Error(), base) {
			t.Fatalf("error omitted repository: %v", err)
		}
		assertScratchEmpty(t, scratch)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.acquire(ctx, base, sha); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition: %v", err)
	}
	assertScratchEmpty(t, scratch)
}

func TestGitBaseCacheBoundsAndConcurrentHits(t *testing.T) {
	base, sha := makeLocalRepo(t)
	r := NewGitResolver(t.TempDir()).(*gitResolver)
	r.bases = newGitBaseCache(8 << 10)
	first, err := r.acquire(context.Background(), base, sha)
	if err != nil {
		t.Fatal(err)
	}
	first.Cleanup()
	if err := os.RemoveAll(strings.TrimPrefix(base, "file://")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			got, err := r.acquire(context.Background(), base, sha)
			if err != nil {
				t.Error(err)
				return
			}
			defer got.Cleanup()
			if _, err := os.Stat(filepath.Join(got.Root, "seed", "kustomization.yaml")); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	assertScratchEmpty(t, r.scratchDir)
	base, sha = makeLocalRepo(t)
	r.bases = newGitBaseCache(1)
	got, err := r.acquire(context.Background(), base, sha)
	if err != nil {
		t.Fatal(err)
	}
	got.Cleanup()
	if err := os.RemoveAll(strings.TrimPrefix(base, "file://")); err != nil {
		t.Fatal(err)
	}
	if got, err := r.acquire(context.Background(), base, sha); err == nil {
		got.Cleanup()
		t.Fatal("oversized entry was retained")
	}
	assertScratchEmpty(t, r.scratchDir)
}

func TestGitBaseRedirectDoesNotForwardCredentials(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("Authorization") != "")
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound) // #nosec G710 -- Deliberate cross-origin redirect fixture verifies credentials are not forwarded.
	}))
	defer origin.Close()
	r := &gitResolver{resolve: func(context.Context, gitOrigin) (creds, error) {
		return creds{user: "git", pass: "secret", ok: true}, nil
	}}
	_, err := r.ResolveID(context.Background(), origin.URL+"?ref=v1", ModeSeed)
	if err == nil {
		t.Fatal("expected resolution error")
	}
	if leaked.Load() {
		t.Fatal("redirect forwarded credentials across effective ports")
	}
}

func TestGitBaseAuthorizationIsolation(t *testing.T) {
	base, sha := makeLocalRepo(t)
	r := NewGitResolver(t.TempDir()).(*gitResolver)
	var reads int
	credential := creds{user: "git", pass: "first-private-token", ok: true}
	r.resolve = func(context.Context, gitOrigin) (creds, error) { reads++; return credential, nil }
	first, err := r.acquire(context.Background(), base, sha)
	if err != nil {
		t.Fatal(err)
	}
	first.Cleanup()
	if reads != 1 {
		t.Fatalf("credentials resolved %d times", reads)
	}
	credential.pass = "rotated-private-token"
	second, err := r.acquire(context.Background(), base, sha)
	if err != nil {
		t.Fatal(err)
	}
	second.Cleanup()
	if second.Identity == first.Identity {
		t.Fatal("rotation did not change identity")
	}
	if strings.Contains(second.Identity, credential.pass) {
		t.Fatal("identity exposed token")
	}
	credential = creds{}
	third, err := r.acquire(context.Background(), base, sha)
	if err != nil {
		t.Fatal(err)
	}
	third.Cleanup()
	if third.Identity == first.Identity || third.Identity == second.Identity {
		t.Fatal("anonymous context reused private entry")
	}
	if len(r.bases.entries) != 3 {
		t.Fatalf("authorization contexts not isolated: %d entries", len(r.bases.entries))
	}
	if err := os.RemoveAll(strings.TrimPrefix(base, "file://")); err != nil {
		t.Fatal(err)
	}
	credential = creds{user: "other-user", pass: "first-private-token", ok: true}
	if got, err := r.acquire(context.Background(), base, sha); err == nil {
		got.Cleanup()
		t.Fatal("different user received private cached content")
	}
	credential = creds{user: "git", pass: "first-private-token", ok: true}
	got, err := r.acquire(context.Background(), base, sha)
	if err != nil {
		t.Fatalf("matching context missed resident entry: %v", err)
	}
	got.Cleanup()
	assertScratchEmpty(t, r.scratchDir)
}

func TestGitBaseCacheEvictionAndRepositoryIsolation(t *testing.T) {
	base, sha := makeLocalRepo(t)
	r := NewGitResolver(t.TempDir()).(*gitResolver)
	first, err := r.acquire(context.Background(), base, sha)
	if err != nil {
		t.Fatal(err)
	}
	first.Cleanup()
	// Exactly one snapshot fits. A different repository with the same SHA misses.
	r.bases.limit = r.bases.used
	other := t.TempDir()
	if _, err := git.PlainClone(other, false, &git.CloneOptions{URL: base}); err != nil {
		t.Fatal(err)
	}
	second, err := r.acquire(context.Background(), "file://"+other, sha)
	if err != nil {
		t.Fatal(err)
	}
	second.Cleanup()
	if first.Identity == second.Identity {
		t.Fatal("repository omitted from identity")
	}
	if r.bases.used > r.bases.limit || len(r.bases.entries) != 1 {
		t.Fatal("cache exceeded byte capacity")
	}
	if err := os.RemoveAll(strings.TrimPrefix(base, "file://")); err != nil {
		t.Fatal(err)
	}
	if got, err := r.acquire(context.Background(), base, sha); err == nil {
		got.Cleanup()
		t.Fatal("evicted repository still cached")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.bases.lock(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := r.bases.lock(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cache wait ignored cancellation: %v", err)
	}
	r.bases.unlock()
	assertScratchEmpty(t, r.scratchDir)
}

func TestGitBaseCanonicalRepository(t *testing.T) {
	a, err := canonicalGitRepository("https://EXAMPLE.com:443/org/repo")
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalGitRepository("https://example.com/org/repo")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("equivalent origin identities differ: %q != %q", a, b)
	}
	for _, bad := range []string{"https://user:private-token@example.com/repo", "https://user:private-token@%zz/repo", "ssh://example.com/repo"} {
		_, err := canonicalGitRepository(bad)
		if err == nil || strings.Contains(err.Error(), "private-token") {
			t.Fatalf("unsafe URL accepted/leaked: %v", err)
		}
	}
}

func TestGitBaseConcurrentMissFetchesOnce(t *testing.T) {
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git-http-backend unavailable")
	}
	backend := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend unavailable")
	}
	bare := makeBareBehindHTTP(t)
	repo, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	authed := authedGitServer(t, backend, filepath.Dir(bare), "git", "secret")
	var fetches, credentials atomic.Int32
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/git-upload-pack") {
			fetches.Add(1)
		}
		authed.Config.Handler.ServeHTTP(w, req)
	}))
	defer counting.Close()
	r := NewGitResolver(t.TempDir()).(*gitResolver)
	r.resolve = func(context.Context, gitOrigin) (creds, error) {
		credentials.Add(1)
		return creds{user: "git", pass: "secret", ok: true}, nil
	}
	const acquisitions = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range acquisitions {
		wg.Go(func() {
			<-start
			got, err := r.acquire(context.Background(), counting.URL+"/"+filepath.Base(bare), head.Hash().String())
			if err != nil {
				t.Error(err)
				return
			}
			got.Cleanup()
		})
	}
	close(start)
	wg.Wait()
	if fetches.Load() != 1 {
		t.Fatalf("content fetches=%d want 1", fetches.Load())
	}
	if credentials.Load() != acquisitions {
		t.Fatalf("credential reads=%d want %d", credentials.Load(), acquisitions)
	}
	assertScratchEmpty(t, r.scratchDir)
}
