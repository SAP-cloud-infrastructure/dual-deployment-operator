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
