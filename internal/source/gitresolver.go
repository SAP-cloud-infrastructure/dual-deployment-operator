// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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

func (r *gitResolver) Resolve(ctx context.Context, rawURL, subPath string) (fsPath string, cleanup func(), err error) {
	noop := func() {}
	base, rootSubPath, ref, err := splitRef(rawURL)
	if err != nil {
		return "", noop, err
	}
	dir, err := os.MkdirTemp("", "ddo-kustomize-")
	if err != nil {
		return "", noop, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	auth, err := r.authFor(ctx, base)
	if err != nil {
		cleanup()
		return "", noop, fmt.Errorf("source: resolve credentials: %w", err)
	}
	if err := fetchPinned(ctx, dir, base, ref, auth); err != nil {
		cleanup()
		return "", noop, err
	}
	return filepath.Join(dir, rootSubPath, subPath), cleanup, nil
}

func (r *gitResolver) authFor(ctx context.Context, base string) (transport.AuthMethod, error) {
	if r.resolve == nil {
		return nil, nil // no resolver => anonymous
	}
	c, err := r.resolve(ctx, hostOf(base))
	if err != nil {
		return nil, err
	}
	if !c.ok {
		return nil, nil // recognized-keys-absent / nil ref => anonymous
	}
	return &httpauth.BasicAuth{Username: c.user, Password: c.pass}, nil
}

// splitRef parses a kustomize URL with optional git // in-repo root path and mandatory ?ref=.
//
// Supported forms:
//   - https://github.com/org/repo?ref=v1              → cloneURL=https://…/repo, rootSubPath="", ref="v1"
//   - https://github.com/org/repo//path/to/root?ref=v1 → cloneURL=https://…/repo, rootSubPath="path/to/root", ref="v1"
//   - file:///tmp/x?ref=v1                             → cloneURL=file:///tmp/x, rootSubPath="", ref="v1"
//
// The // separator is detected in the URL path (not the scheme ://). URLs with
// embedded userinfo (user:token@host) are rejected to prevent credential leakage
// into error messages; use authSecretRef instead.
func splitRef(rawURL string) (cloneURL, rootSubPath, ref string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", "", fmt.Errorf("source: parse kustomize url: %w", err)
	}
	if u.User != nil {
		return "", "", "", errors.New("source: kustomize url must not embed credentials in the URL; use authSecretRef")
	}
	ref = u.Query().Get("ref")
	if ref == "" {
		return "", "", "", errors.New("source: kustomize url missing pinned ?ref=")
	}
	u.RawQuery = ""

	path := u.Path
	if before, after, found := strings.Cut(path, "//"); found {
		rootSubPath = strings.Trim(after, "/")
		u.Path = before
	}

	return u.String(), rootSubPath, ref, nil
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
