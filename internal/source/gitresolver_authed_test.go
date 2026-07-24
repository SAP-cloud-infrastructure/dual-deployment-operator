// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504 Httpoxy (CVE-2016-5386) requires a CGI process to read an attacker's Proxy header into HTTP_PROXY. Mitigated here: this is a localhost-only httptest server, and cgi.Handler runs with an explicit 2-var Env allowlist (GIT_HTTP_EXPORT_ALL, GIT_PROJECT_ROOT) — no Proxy/HTTP_PROXY is ever passed to the CGI. The CVE precondition is therefore absent.
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// makeBareBehindHTTP creates a bare git repo (clone of a working repo) in a temp
// dir and returns its path. The bare repo is what git-http-backend needs to serve.
func makeBareBehindHTTP(t *testing.T) string {
	t.Helper()

	// 1. Build a normal (non-bare) repo with one commit.
	work := t.TempDir()
	r, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "seed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "seed", "kustomization.yaml"), []byte("resources: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("."); err != nil {
		t.Fatal(err)
	}
	h, err := w.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateTag("v1", h, nil); err != nil {
		t.Fatal(err)
	}

	// 2. Clone it into a bare repo — git-http-backend works against bare repos.
	bare := t.TempDir()
	if _, err := git.PlainClone(bare, true, &git.CloneOptions{URL: "file://" + work}); err != nil {
		t.Fatalf("clone to bare: %v", err)
	}
	return bare
}

// authedGitServer wraps git-http-backend in an httptest.Server with Basic-auth enforcement.
// parentDir must be the directory CONTAINING the bare repo (git-http-backend uses GIT_PROJECT_ROOT
// to locate the repo by the request path). Only requests with Authorization: Basic
// base64(user:pass) are forwarded; others get 401.
func authedGitServer(t *testing.T, backendBin, parentDir, user, pass string) *httptest.Server {
	t.Helper()

	wantToken := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))

	cgiHandler := &cgi.Handler{
		Path: backendBin,
		Env: []string{
			"GIT_HTTP_EXPORT_ALL=1",
			"GIT_PROJECT_ROOT=" + parentDir,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != wantToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		// Rewrite path so git-http-backend sees /<repoName>/info/refs etc.
		// The server URL used in tests is srv.URL + "/" + repoName + ".git".
		cgiHandler.ServeHTTP(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestGitResolverHTTPSBasicAuth proves that a gitResolver with resolved credentials
// attaches them as HTTP Basic auth on every outbound request to the git remote.
// The server returns 403 to short-circuit the fetch; the test asserts the Authorization
// header value and that the password is not leaked in the error string.
func TestGitResolverHTTPSBasicAuth(t *testing.T) {
	const (
		wantUser = "u"
		wantPass = "pw"
	)
	wantToken := "Basic " + base64.StdEncoding.EncodeToString([]byte(wantUser+":"+wantPass))

	var gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		http.Error(w, "not a git server", http.StatusForbidden)
	}))
	defer srv.Close()

	authedResolver := &gitResolver{
		resolve: func(_ context.Context, _ string) (creds, error) {
			return creds{user: wantUser, pass: wantPass, ok: true}, nil
		},
	}
	_, cleanup, err := authedResolver.Resolve(context.Background(), srv.URL+"?ref=v1", "")
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatal("expected an error (server returns 403), got nil")
	}

	obs := ""
	if v := gotAuth.Load(); v != nil {
		obs = v.(string)
	}
	if obs == "" {
		t.Fatal("authed resolver: httptest server received NO Authorization header; expected Basic credentials")
	}
	if obs != wantToken {
		t.Fatal("authed resolver: Authorization header did not match the expected Basic credentials (values redacted)")
	}

	if strings.Contains(err.Error(), wantPass) {
		t.Fatal("credential leak: password appeared in the error string")
	}

	// Anonymous resolver must not send an Authorization header.
	var anonAuth atomic.Value
	srvAnon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anonAuth.Store(r.Header.Get("Authorization"))
		http.Error(w, "not a git server", http.StatusForbidden)
	}))
	defer srvAnon.Close()

	anonResolver := &gitResolver{}
	_, cleanupAnon, errAnon := anonResolver.Resolve(context.Background(), srvAnon.URL+"?ref=v1", "")
	_ = errAnon // anonymous path: we only assert no Authorization header was sent (checked below)
	if cleanupAnon != nil {
		cleanupAnon()
	}

	if v := anonAuth.Load(); v != nil && v.(string) != "" {
		t.Fatal("anonymous resolver: unexpected Authorization header (value redacted); want none")
	}
}

// TestGitResolverAuthedCloneSucceeds proves that an authenticated clone against a
// REAL git-HTTP server succeeds with correct credentials and fails (401) without.
//
// Implementation: git-http-backend CGI via net/http/cgi (in-process, hermetic,
// no external network). The test is NOT build-tagged online; it skips gracefully
// when git-http-backend is absent (minimal CI runners without git).
func TestGitResolverAuthedCloneSucceeds(t *testing.T) {
	backendBin, err := exec.LookPath("git-http-backend")
	if err != nil {
		// Locate via `git --exec-path` (e.g. Homebrew installs it there).
		if out, e := exec.Command("git", "--exec-path").Output(); e == nil {
			candidate := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
			if _, e2 := os.Stat(candidate); e2 == nil {
				backendBin = candidate
			}
		}
	}
	if backendBin == "" {
		t.Skip("git-http-backend not found; skipping real authed-clone test")
	}

	const (
		testUser = "u"
		testPass = "secret"
		repoName = "repo.git"
	)

	// Build a bare repo and stand up an authed git-HTTP server.
	bare := makeBareBehindHTTP(t)

	// git-http-backend uses GIT_PROJECT_ROOT as the base; the request path
	// must match /<repoName>/…. We rename the bare dir to repo.git inside a
	// parent dir so paths align.
	parentDir := t.TempDir()
	namedBare := filepath.Join(parentDir, repoName)
	if err := os.Rename(bare, namedBare); err != nil {
		// Rename across tmp dirs may fail on some systems; copy instead.
		if err2 := exec.Command("cp", "-r", bare, namedBare).Run(); err2 != nil {
			t.Fatalf("could not place bare repo at %s: rename=%v cp=%v", namedBare, err, err2)
		}
	}

	srv := authedGitServer(t, backendBin, parentDir, testUser, testPass)

	repoURL := srv.URL + "/" + repoName

	// --- correct credentials: clone must succeed ---
	goodResolver := &gitResolver{
		resolve: func(_ context.Context, _ string) (creds, error) {
			return creds{user: testUser, pass: testPass, ok: true}, nil
		},
	}
	path, cleanup, err := goodResolver.Resolve(context.Background(), repoURL+"?ref=v1", "seed")
	if err != nil {
		t.Fatalf("authed clone with correct creds: unexpected error: %v", err)
	}
	defer cleanup()

	if _, statErr := os.Stat(filepath.Join(path, "kustomization.yaml")); statErr != nil {
		t.Fatalf("authed clone: expected seed/kustomization.yaml at %s: %v", path, statErr)
	}

	// --- wrong credentials: must return an error ---
	badResolver := &gitResolver{
		resolve: func(_ context.Context, _ string) (creds, error) {
			return creds{user: testUser, pass: "wrong-password", ok: true}, nil
		},
	}
	_, cleanupBad, errBad := badResolver.Resolve(context.Background(), repoURL+"?ref=v1", "seed")
	if cleanupBad != nil {
		cleanupBad()
	}
	if errBad == nil {
		t.Fatal("wrong-cred clone: expected an error (401), got nil")
	}
	if strings.Contains(errBad.Error(), testPass) {
		t.Fatal("credential leak: password appeared in wrong-cred error string")
	}
}

// TestFromThreadsHelmCredsToLoader proves that From() creates a *helmLoader whose
// resolve closure returns the credentials threaded from the CredentialResolver and
// the authSecretRef.
func TestFromThreadsHelmCredsToLoader(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mysecret", Namespace: "ns"},
		Data:       map[string][]byte{"token": []byte("tok")},
	}).Build()

	deps := Deps{
		ChartLoader:        NewHelmLoader(),
		CredentialResolver: &CredentialResolver{Client: cl, Namespace: "ns"},
	}
	spec := v1alpha1.Source{Helm: &v1alpha1.HelmSource{
		Repo:          "oci://r",
		Name:          "n",
		Version:       "1",
		AuthSecretRef: &v1alpha1.SecretReference{Name: "mysecret"},
	}}

	src, err := From(spec, deps)
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	hs, ok := src.(*helmSource)
	if !ok {
		t.Fatalf("From returned %T, want *helmSource", src)
	}
	hl, ok := hs.loader.(*helmLoader)
	if !ok {
		t.Fatalf("helmSource.loader is %T, want *helmLoader", hs.loader)
	}
	if hl.resolve == nil {
		t.Fatal("helmSource.loader.resolve is nil; credentials were not threaded")
	}

	got, err := hl.resolve(context.Background(), "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.user != "git" || got.pass != "tok" || !got.ok {
		t.Fatalf("resolve returned creds{user=%q pass=<redacted> ok=%v}, want {user=git pass=<tok> ok=true}",
			got.user, got.ok)
	}
}
