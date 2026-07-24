// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"os"
	"path/filepath"
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
