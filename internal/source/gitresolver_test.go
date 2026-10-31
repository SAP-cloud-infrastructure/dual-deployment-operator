// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// makeLocalRepo builds a repo on disk with one commit + tag "v1"; returns file:// URL and SHA.
func makeLocalRepo(t *testing.T) (fileURL, sha string) {
	t.Helper()
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
	return "file://" + work, h.String()
}

func TestGitResolverTagAndSHA(t *testing.T) {
	base, sha := makeLocalRepo(t)
	res := &gitResolver{} // anonymous
	for _, ref := range []string{"v1", sha} {
		path, cleanup, err := res.Resolve(context.Background(), base+"?ref="+ref, "seed")
		if err != nil {
			t.Fatalf("resolve ref %s: %v", ref, err)
		}
		if _, err := os.Stat(filepath.Join(path, "kustomization.yaml")); err != nil {
			cleanup()
			t.Fatalf("ref %s: expected seed/kustomization.yaml at %s: %v", ref, path, err)
		}
		cleanup()
	}
}

func TestGitResolverScratchAllocation(t *testing.T) {
	base, sha := makeLocalRepo(t)
	for _, configured := range []bool{true, false} {
		name := "configured"
		if !configured {
			name = "os-temp-fallback"
		}
		t.Run(name, func(t *testing.T) {
			// Isolate the fallback too, so leaked checkouts are observable.
			fallback := t.TempDir()
			t.Setenv("TMPDIR", fallback)
			t.Setenv("TMP", fallback)
			t.Setenv("TEMP", fallback)
			scratch, parent := "", os.TempDir() //nolint:usetesting // Verify the actual resolver fallback after isolating OS temp environment variables.
			if configured {
				scratch = t.TempDir()
				parent = scratch
			}
			for _, ref := range []string{"v1", sha} {
				t.Run(ref, func(t *testing.T) {
					res := NewGitResolver(scratch)
					path, cleanup, err := res.Resolve(context.Background(), base+"?ref="+ref, "seed")
					if cleanup == nil {
						t.Fatal("Resolve must return a non-nil cleanup")
					}
					t.Cleanup(cleanup)
					if err != nil {
						t.Fatal(err)
					}
					checkout := filepath.Dir(path)
					if filepath.Dir(checkout) != parent {
						t.Fatalf("checkout parent = %q, want %q", filepath.Dir(checkout), parent)
					}
					if _, err := os.Stat(filepath.Join(path, "kustomization.yaml")); err != nil {
						t.Fatalf("expected root fixture in checkout: %v", err)
					}
					cleanup()
					if _, err := os.Stat(checkout); !os.IsNotExist(err) {
						t.Fatalf("checkout still exists after cleanup: %v", err)
					}
					assertScratchEmpty(t, parent)
					assertScratchEmpty(t, fallback)
				})
			}
		})
	}
}

func TestGitResolverScratchCleanupOnError(t *testing.T) {
	base, sha := makeLocalRepo(t)
	for _, failure := range []string{"bad-ref", "credentials", "cancellation", "missing-scratch"} {
		t.Run(failure, func(t *testing.T) {
			scratch := t.TempDir()
			parent := scratch
			if failure == "missing-scratch" {
				scratch = filepath.Join(scratch, "missing")
			}
			res := NewGitResolver(scratch).(*gitResolver)
			ctx := context.Background()
			ref := sha
			switch failure {
			case "bad-ref":
				ref = "does-not-exist"
			case "credentials":
				res.resolve = func(context.Context, gitOrigin) (creds, error) {
					entries, err := os.ReadDir(scratch)
					if err != nil || len(entries) != 0 {
						t.Errorf("expected credentials before checkout allocation: entries=%v err=%v", entries, err)
					}
					return creds{}, errors.New("credentials unavailable")
				}
			case "cancellation":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			path, cleanup, err := res.Resolve(ctx, base+"?ref="+ref, "seed")
			if cleanup == nil {
				t.Fatal("Resolve must return a non-nil cleanup on error")
			}
			t.Cleanup(cleanup)
			if err == nil || path != "" {
				t.Fatalf("expected error and empty path, got path=%q err=%v", path, err)
			}
			// Errors must remove allocations even before the caller invokes cleanup.
			assertScratchEmpty(t, parent)
			cleanup()
			assertScratchEmpty(t, parent)
		})
	}
}

func assertScratchEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch directory %q contains leaked entries: %v", dir, entries)
	}
}

// makeLocalRepoTwoCommits builds a repo with two commits on the default branch
// and returns the file:// URL plus the FIRST (older, non-tip) commit's SHA. The
// first commit is not the tip of any branch or tag, so a shallow (Depth:1) fetch
// would not download it — exercising the arbitrary-historical-SHA path.
func makeLocalRepoTwoCommits(t *testing.T) (fileURL, oldSHA string) {
	t.Helper()
	work := t.TempDir()
	r, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatal(err)
	}
	w, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "seed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "seed", "kustomization.yaml"), []byte("resources: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("."); err != nil {
		t.Fatal(err)
	}
	first, err := w.Commit("first", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("."); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit("second", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@t"}}); err != nil {
		t.Fatal(err)
	}
	return "file://" + work, first.String()
}

// TestGitResolverResolvesHistoricalSHA guards the fetchSHA full-depth fix: a SHA
// that is not the tip of any branch/tag must still resolve. The tip-SHA case in
// TestGitResolverTagAndSHA passed even under the old shallow (Depth:1) fetch, so
// this non-tip case is the actual regression guard.
func TestGitResolverResolvesHistoricalSHA(t *testing.T) {
	base, oldSHA := makeLocalRepoTwoCommits(t)
	res := &gitResolver{}
	path, cleanup, err := res.Resolve(context.Background(), base+"?ref="+oldSHA, "seed")
	if err != nil {
		t.Fatalf("resolve historical (non-tip) SHA %s: %v", oldSHA, err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(path, "kustomization.yaml")); err != nil {
		t.Fatalf("expected seed/kustomization.yaml from historical SHA at %s: %v", path, err)
	}
	// The historical checkout must NOT contain the later commit's file.
	if _, err := os.Stat(filepath.Join(path, "..", "later.txt")); err == nil {
		t.Error("checkout of the first commit must not include the second commit's file")
	}
}

func TestGitResolverFailsClosedOnBadRef(t *testing.T) {
	base, _ := makeLocalRepo(t)
	res := &gitResolver{}
	_, cleanup, err := res.Resolve(context.Background(), base+"?ref=does-not-exist", "seed")
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatal("expected fail-closed error for a nonexistent ref")
	}
}

// makeLocalRepoDeep builds a repo whose tree mirrors system/kustomize/app/ with
// a seed/kustomization.yaml inside it. Returns the file:// URL and tag "v1".
func makeLocalRepoDeep(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	r, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatal(err)
	}
	deepDir := filepath.Join(work, "system", "kustomize", "app", "seed")
	if err := os.MkdirAll(deepDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deepDir, "kustomization.yaml"), []byte("resources: []\n"), 0o600); err != nil {
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
	return "file://" + work
}

// TestGitResolverDoubleSlashRootSubPath verifies that URLs using the git // convention
// (e.g. file://<dir>//system/kustomize/app?ref=v1 with subPath="seed") correctly split
// the clone URL from the in-repo root path and return <tmp>/system/kustomize/app/seed.
func TestGitResolverDoubleSlashRootSubPath(t *testing.T) {
	base := makeLocalRepoDeep(t)
	res := &gitResolver{}

	rawURL := base + "//system/kustomize/app?ref=v1"
	path, cleanup, err := res.Resolve(context.Background(), rawURL, "seed")
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("resolve with // root subpath: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(path), "system/kustomize/app/seed") {
		t.Fatalf("expected path ending with system/kustomize/app/seed, got %s", path)
	}
	if _, err := os.Stat(filepath.Join(path, "kustomization.yaml")); err != nil {
		t.Fatalf("expected kustomization.yaml at %s: %v", path, err)
	}
}

// TestGitResolverNoDoubleSlashBackcompat verifies that a URL without // still returns
// <tmp>/<subPath> unchanged (backward compatibility).
func TestGitResolverNoDoubleSlashBackcompat(t *testing.T) {
	base, _ := makeLocalRepo(t)
	res := &gitResolver{}

	path, cleanup, err := res.Resolve(context.Background(), base+"?ref=v1", "seed")
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("resolve without // root subpath: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "kustomization.yaml")); err != nil {
		t.Fatalf("expected seed/kustomization.yaml at %s: %v", path, err)
	}
}

// TestSplitRefRejectsUserinfo verifies that URLs with embedded credentials are
// rejected and the error message does not echo back the password.
func TestSplitRefRejectsUserinfo(t *testing.T) {
	_, _, _, err := splitRef("https://user:supersecret@github.com/org/repo?ref=v1")
	if err == nil {
		t.Fatal("expected error for URL with embedded userinfo, got nil")
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatal("credential leak: password appeared in the userinfo rejection error")
	}
	if !strings.Contains(err.Error(), "authSecretRef") {
		t.Fatalf("error should mention authSecretRef, got: %v", err)
	}
}

func TestGitResolver_ResolveID_TagToSHA(t *testing.T) {
	fileURL, wantSHA := makeLocalRepo(t)
	r := &gitResolver{}
	got, err := r.ResolveID(context.Background(), fileURL+"?ref=v1", ModeSeed)
	if err != nil {
		t.Fatalf("ResolveID: %v", err)
	}
	if got != wantSHA {
		t.Fatalf("ResolveID = %q, want commit SHA %q (never the tag name)", got, wantSHA)
	}
}

func TestGitResolver_ResolveID_UnresolvableRefErrors(t *testing.T) {
	fileURL, _ := makeLocalRepo(t)
	r := &gitResolver{}
	if _, err := r.ResolveID(context.Background(), fileURL+"?ref=nope", ModeSeed); err == nil {
		t.Fatal("expected error for a ref that resolves to no commit SHA (must not return the ref name)")
	}
}

func TestGitResolver_ResolveID_BareSHAUsedAsIs(t *testing.T) {
	r := &gitResolver{}
	sha := "0123456789abcdef0123456789abcdef01234567"
	got, err := r.ResolveID(context.Background(), "https://example.com/x/y?ref="+sha, ModeSeed)
	if err != nil {
		t.Fatalf("ResolveID: %v", err)
	}
	if got != sha {
		t.Fatalf("bare SHA must be used as-is: got %q want %q", got, sha)
	}
}

func TestGitResolver_repoScope(t *testing.T) {
	r := &gitResolver{}
	scope, err := r.repoScope("https://github.com/org/repo//root?ref=v1")
	if err != nil {
		t.Fatalf("repoScope: %v", err)
	}
	if scope != "git:github.com" {
		t.Fatalf("repoScope = %q, want git:github.com", scope)
	}
}
