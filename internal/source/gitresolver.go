// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	httpauth "github.com/go-git/go-git/v5/plumbing/transport/http"
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// gitResolver is the production RootResolver. It fetches a ?ref=-pinned remote git
// root into a temp dir (fail-closed) and returns <tmp>/<subPath> + cleanup.
type gitResolver struct {
	resolve func(ctx context.Context, host string) (creds, error) // nil => anonymous
}

func (r *gitResolver) Resolve(ctx context.Context, rawURL, subPath string) (string, func(), error) {
	base, ref, err := splitRef(rawURL)
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "ddo-kustomize-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	auth := r.authFor(ctx, base)
	if err := fetchPinned(ctx, dir, base, ref, auth); err != nil {
		cleanup()
		return "", nil, err
	}
	return filepath.Join(dir, subPath), cleanup, nil
}

func (r *gitResolver) authFor(ctx context.Context, base string) transport.AuthMethod {
	if r.resolve == nil {
		return nil
	}
	c, err := r.resolve(ctx, hostOf(base))
	if err != nil || !c.ok {
		return nil
	}
	return &httpauth.BasicAuth{Username: c.user, Password: c.pass}
}

// splitRef extracts the base repo URL and the pinned ref from a ?ref= URL.
func splitRef(rawURL string) (base, ref string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("source: parse kustomize url: %w", err)
	}
	ref = u.Query().Get("ref")
	if ref == "" {
		return "", "", fmt.Errorf("source: kustomize url missing pinned ?ref=")
	}
	u.RawQuery = ""
	return u.String(), ref, nil
}

// fetchPinned fetches ref (tag/branch OR sha) at Depth 1, fail-closed.
func fetchPinned(ctx context.Context, dir, base, ref string, auth transport.AuthMethod) error {
	if shaRe.MatchString(ref) {
		return fetchSHA(ctx, dir, base, ref, auth)
	}
	for _, rn := range []plumbing.ReferenceName{
		plumbing.NewTagReferenceName(ref), plumbing.NewBranchReferenceName(ref),
	} {
		_, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
			URL: base, Auth: auth, ReferenceName: rn, SingleBranch: true, Depth: 1,
		})
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("source: could not resolve ref %q on %s (fail-closed)", ref, base)
}

func fetchSHA(ctx context.Context, dir, base, sha string, auth transport.AuthMethod) error {
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		return err
	}
	remote, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{base}})
	if err != nil {
		return err
	}
	// Fetch all branch + tag refs (shallow). Bare-SHA refspecs are not universally
	// supported (e.g. git's file:// dumb transport rejects them), so we fetch all
	// refs and resolve the SHA from what was advertised — still fail-closed because
	// Checkout will error if the hash is absent.
	if err := remote.FetchContext(ctx, &git.FetchOptions{
		RefSpecs: []config.RefSpec{
			"+refs/heads/*:refs/heads/*",
			"+refs/tags/*:refs/tags/*",
		},
		Depth: 1, Auth: auth,
	}); err != nil {
		return fmt.Errorf("source: fetch sha %s: %w (fail-closed)", sha, err)
	}
	w, err := repo.Worktree()
	if err != nil {
		return err
	}
	if err := w.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(sha)}); err != nil {
		return fmt.Errorf("source: checkout sha %s: %w (fail-closed)", sha, err)
	}
	return nil
}
