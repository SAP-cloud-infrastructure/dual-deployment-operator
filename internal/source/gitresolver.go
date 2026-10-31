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
	"path/filepath"
	"regexp"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	httpauth "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// gitResolver is the production RootResolver. It fetches a ?ref=-pinned remote git
// root into a temp dir (fail-closed) and returns <tmp>/<subPath> + cleanup.
type gitResolver struct {
	scratchDir string                                                     // empty => os.TempDir()
	resolve    func(ctx context.Context, origin gitOrigin) (creds, error) // nil => anonymous
	bases      *gitBaseCache                                              // shared by credential-bound copies; nil disables snapshots
}

func (r *gitResolver) Resolve(ctx context.Context, rawURL, subPath string) (fsPath string, cleanup func(), err error) {
	noop := func() {}
	base, rootSubPath, ref, err := splitRef(rawURL)
	if err != nil {
		return "", noop, err
	}
	checkout, err := r.acquire(ctx, base, ref)
	if err != nil {
		return "", noop, err
	}
	return filepath.Join(checkout.Root, rootSubPath, subPath), checkout.Cleanup, nil
}

func (r *gitResolver) authFor(ctx context.Context, base string) (transport.AuthMethod, error) {
	if r.resolve == nil {
		return nil, nil // no resolver => anonymous
	}
	origin, err := gitOriginOf(base)
	if err != nil {
		return nil, err
	}
	c, err := r.resolve(ctx, origin)
	if err != nil {
		return nil, errors.New("source: resolve credentials failed")
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
		return "", "", "", errors.New("source: parse kustomize url: invalid URL")
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

func fetchSHA(ctx context.Context, dir, base, sha string, auth transport.AuthMethod) error {
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		return err
	}
	remote, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{base}})
	if err != nil {
		return err
	}
	// Fetch all branch + tag refs to full depth. Bare-SHA refspecs are not
	// universally supported (e.g. git's file:// dumb transport rejects them), so we
	// fetch the advertised refs and resolve the SHA from history. A shallow
	// (Depth:1) fetch would only download each ref's tip commit, so an arbitrary
	// historical SHA (not a branch/tag tip) would be absent — hence full depth.
	// Still fail-closed: Checkout errors if the hash is genuinely absent.
	if err := remote.FetchContext(ctx, &git.FetchOptions{
		RefSpecs: []config.RefSpec{
			"+refs/heads/*:refs/heads/*",
			"+refs/tags/*:refs/tags/*",
		},
		Auth: auth,
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

// repoScope returns the transport+host scope for the cache key: "git:<host>".
func (r *gitResolver) repoScope(rawURL string) (string, error) {
	base, _, _, err := splitRef(rawURL)
	if err != nil {
		return "", err
	}
	return "git:" + hostOf(base), nil
}

// ResolveID resolves the pinned ?ref= to its immutable commit SHA without cloning.
// A bare SHA is returned as-is. A tag or branch ref is resolved via ls-remote and
// MUST map to a commit SHA — never the ref name — or an error is returned.
func (r *gitResolver) ResolveID(ctx context.Context, rawURL string, _ Mode) (string, error) {
	base, _, ref, err := splitRef(rawURL)
	if err != nil {
		return "", err
	}
	auth, err := r.authFor(ctx, base)
	if err != nil {
		return "", err
	}
	return resolveGitID(ctx, base, ref, auth)
}

// resolveGitID shares ref precedence with acquisitions without reading credentials twice.
func resolveGitID(ctx context.Context, base, ref string, auth transport.AuthMethod) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if shaRe.MatchString(ref) {
		return ref, nil
	}
	rem := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{base}})
	refs, err := rem.ListContext(ctx, &git.ListOptions{Auth: auth, PeelingOption: git.AppendPeeled})
	if err != nil {
		return "", gitAcquisitionError(ctx, "ls-remote", base, err)
	}
	byName := make(map[string]*plumbing.Reference, len(refs))
	for _, rr := range refs {
		byName[rr.Name().String()] = rr
	}
	// Prefer peeled annotated-tag commit, then lightweight tag, then branch.
	for _, cand := range []string{
		"refs/tags/" + ref + "^{}",
		"refs/tags/" + ref,
		"refs/heads/" + ref,
	} {
		if rr, ok := byName[cand]; ok {
			return rr.Hash().String(), nil
		}
	}
	return "", fmt.Errorf("source: could not resolve ref %q on %s to a commit SHA", ref, base)
}
