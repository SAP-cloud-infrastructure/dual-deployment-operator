// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

// Package source — remotepolicy.go
//
// classifyKustomizeReference classifies a single entry from the resources/
// components/bases fields of a kustomization.yaml, aligned with the reference
// forms that krusty v0.21.1 + git-exec accepts. The classifier is a strict
// DDO-side guardrail (Design decision D2): it rejects unsafe/unsupported
// forms BEFORE any fetch occurs and without echoing raw credentials or full
// URLs in error messages.
//
// treeWalkKustomize walks the local kustomization tree rooted at a resolved
// on-disk directory, visiting every locally reachable kustomization, and
// returns the ordered set of remote references discovered.

package source

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/kustomize/api/konfig"
	"sigs.k8s.io/yaml"
)

// refKind is the category returned by classifyKustomizeReference.
type refKind int

const (
	// refKindLocal is a local filesystem path (relative, no scheme).
	refKindLocal refKind = iota
	// refKindRemoteGitDir is a remote git directory reference (HTTPS or GitHub
	// shorthand) with a mandatory ?ref= query parameter.
	refKindRemoteGitDir
	// refKindRemoteFile is a remote single-file reference (HTTPS URL ending in
	// .yaml / .yml / .json, no ?ref= required).
	refKindRemoteFile
)

func (k refKind) String() string {
	switch k {
	case refKindLocal:
		return "local"
	case refKindRemoteGitDir:
		return "remote-git-dir"
	case refKindRemoteFile:
		return "remote-file"
	default:
		return fmt.Sprintf("refKind(%d)", int(k))
	}
}

// RemoteRef is a classified remote reference discovered during tree walk.
type RemoteRef struct {
	// Raw is the original unmodified string from the kustomization field.
	Raw string
	// Kind is the classified category (always refKindRemoteGitDir or
	// refKindRemoteFile when returned from treeWalkKustomize).
	Kind refKind
	// Ref is the extracted ?ref= value for refKindRemoteGitDir references;
	// empty for refKindRemoteFile.
	Ref string
}

// classifyKustomizeReference classifies a single kustomize resource/component/
// bases entry string. It returns the kind and the extracted ref (non-empty for
// refKindRemoteGitDir), or an error for forms that DDO must reject.
//
// Rejected forms (fail-closed, sanitized errors):
//   - SSH URLs (git@ SCP-style, ssh:// scheme)
//   - URLs with embedded userinfo (user:token@host) — use authSecretRef instead
//   - URLs with a fragment (#)
//   - Multi-segment HTTPS paths with no // separator and no ?ref= that are
//     ambiguous (looks like a remote but has extra path segments without a
//     clear repo/path split marker)
func classifyKustomizeReference(raw string) (refKind, string, error) {
	// --- SSH rejection (before any URL parse so we never include the raw value) ---
	// git@host:org/repo SCP style
	if isSCPStyle(raw) {
		return 0, "", errors.New("remote reference: SCP-style SSH references are not supported; use HTTPS with authSecretRef for private repositories")
	}
	// ssh:// scheme
	if startsWithIgnoreCase(raw, "ssh://") {
		return 0, "", errors.New("remote reference: SSH (ssh://) scheme is not supported; use HTTPS with authSecretRef for private repositories")
	}

	// --- Fragment rejection ---
	if strings.Contains(raw, "#") {
		return 0, "", errors.New("remote reference: URL fragments (#) are not supported in kustomize references")
	}

	// --- Local path: no scheme, starts with ./ ../ a bare name, or absolute ---
	// A URL has a scheme (https:// / http://) or is a GitHub shorthand
	// (github.com/…). Everything else without one of those is local.
	if isLocalPath(raw) {
		return refKindLocal, "", nil
	}

	// --- HTTPS / HTTP URL: parse carefully ---
	if isHTTPURL(raw) {
		return classifyHTTPURL(raw)
	}

	// --- GitHub shorthand: github.com/org/repo[//path][?ref=] (no scheme) ---
	if isGitHubShorthand(raw) {
		return classifyGitHubShorthand(raw)
	}

	// Anything else is unrecognised — treated as local by krusty but DDO
	// cannot confidently classify it; fall through to local.
	return refKindLocal, "", nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// isSCPStyle detects git-style SCP URLs: user@host:path (no scheme).
// We match the pattern: optional chars, then a '@', then a ':', no '://' present.
func isSCPStyle(raw string) bool {
	// Must not have a scheme (those are handled separately)
	if strings.Contains(raw, "://") {
		return false
	}
	atIdx := strings.Index(raw, "@")
	if atIdx < 0 {
		return false
	}
	// After @, must have host:path (colon before any slash)
	after := raw[atIdx+1:]
	colonIdx := strings.Index(after, ":")
	slashIdx := strings.Index(after, "/")
	return colonIdx >= 0 && (slashIdx < 0 || colonIdx < slashIdx)
}

func startsWithIgnoreCase(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return strings.EqualFold(s[:len(prefix)], prefix)
}

// isLocalPath returns true if raw is a local filesystem path: starts with
// ./, ../, is just ".", or contains no "://" and does not start with
// "github.com/".
func isLocalPath(raw string) bool {
	if strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") || raw == "." || raw == ".." {
		return true
	}
	// No scheme, not github.com shorthand → local
	if !strings.Contains(raw, "://") && !startsWithIgnoreCase(raw, "github.com/") {
		return true
	}
	return false
}

func isHTTPURL(raw string) bool {
	return startsWithIgnoreCase(raw, "https://") || startsWithIgnoreCase(raw, "http://")
}

func isGitHubShorthand(raw string) bool {
	return startsWithIgnoreCase(raw, "github.com/")
}

// classifyHTTPURL classifies an https:// or http:// URL.
func classifyHTTPURL(raw string) (refKind, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, "", errors.New("remote reference: malformed URL (parse error)")
	}

	// Reject embedded userinfo (credentials in URL)
	if u.User != nil {
		return 0, "", errors.New("remote reference: URL must not embed credentials; use authSecretRef instead")
	}

	// Fragment already rejected above, but double-check via parsed form
	if u.Fragment != "" {
		return 0, "", errors.New("remote reference: URL fragments (#) are not supported")
	}

	// Remote single file: ends with .yaml / .yml / .json
	ext := strings.ToLower(filepath.Ext(u.Path))
	if ext == ".yaml" || ext == ".yml" || ext == ".json" {
		// Remote files don't require ?ref=
		return refKindRemoteFile, "", nil
	}

	// Remote git directory: requires ?ref=
	ref := u.Query().Get("ref")
	if ref == "" {
		// Check for multi-segment path that might be ambiguous.
		// krusty's default for github.com is exactly 2 path segments (org/repo);
		// for other hosts it requires explicit // or .git marker.
		// Without ?ref= AND without a // separator, we reject as ambiguous.
		if !strings.Contains(raw, "//"+u.Host) && pathSegmentsAfterHost(u.Path) > 2 {
			return 0, "", errors.New("remote reference: ambiguous remote path (multi-segment, no // separator, no ?ref=); add ?ref=<sha-or-tag> to pin")
		}
		return 0, "", errors.New("remote reference: remote git directory references require a pinned ?ref= query parameter")
	}

	return refKindRemoteGitDir, ref, nil
}

// classifyGitHubShorthand classifies a github.com/org/repo[//path][?ref=] reference.
func classifyGitHubShorthand(raw string) (refKind, string, error) {
	// Split off query
	base, query, _ := strings.Cut(raw, "?")
	_ = base

	ref := ""
	if query != "" {
		vals, err := url.ParseQuery(query)
		if err == nil {
			ref = vals.Get("ref")
			if ref == "" {
				ref = vals.Get("version")
			}
		}
	}

	if ref == "" {
		return 0, "", errors.New("remote reference: GitHub shorthand references require a pinned ?ref= query parameter")
	}

	return refKindRemoteGitDir, ref, nil
}

// pathSegmentsAfterHost counts non-empty path segments in a URL path.
func pathSegmentsAfterHost(path string) int {
	// path includes leading /; trim it
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return 0
	}
	// Also ignore the // double-slash separator (already handled above)
	parts := strings.Split(path, "/")
	count := 0
	for _, p := range parts {
		if p != "" {
			count++
		}
	}
	return count
}

// ---------------------------------------------------------------------------
// Tree walk
// ---------------------------------------------------------------------------

// kustomizationYAML is a minimal typed struct for parsing kustomization.yaml.
// We only need the fields that can contain references to other resources.
type kustomizationYAML struct {
	Resources  []string `json:"resources,omitempty"`
	Components []string `json:"components,omitempty"`
	Bases      []string `json:"bases,omitempty"`
}

// treeWalkKustomize walks the local kustomization tree rooted at rootDir,
// visiting every locally reachable kustomization (depth-first). For each
// kustomization it classifies every resources/components/bases entry and:
//   - If local: recurse into that directory (staying within rootDir boundary)
//   - If remote: collect the RemoteRef (do not recurse — krusty will fetch it)
//   - If error: return the error immediately (fail-closed)
//
// It detects reference cycles (same canonical path visited twice) and rejects
// path/symlink escapes (any local path that resolves outside rootDir).
//
// Returns the ordered slice of RemoteRef values discovered (one per remote entry,
// in DFS order).
func treeWalkKustomize(rootDir string) ([]RemoteRef, error) {
	// Canonicalize the root so we can detect escapes reliably.
	absRoot, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		return nil, fmt.Errorf("tree walk: resolve root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	var remotes []RemoteRef
	visited := make(map[string]bool) // canonical paths already on the stack (cycle detection)

	if err := walkDir(absRoot, absRoot, visited, &remotes); err != nil {
		return nil, err
	}
	return remotes, nil
}

// walkDir recursively visits a single directory's kustomization.yaml,
// accumulating remote refs and detecting cycles/escapes.
func walkDir(absRoot, dir string, visited map[string]bool, remotes *[]RemoteRef) error {
	// Resolve the dir canonically (follow symlinks)
	absDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		// Directory may not exist (e.g. escape target); treat as boundary violation
		if os.IsNotExist(err) {
			return fmt.Errorf("tree walk: path boundary violation: directory %q does not exist or escapes the root boundary", sanitizePath(dir, absRoot))
		}
		return fmt.Errorf("tree walk: resolve dir: %w", err)
	}
	absDir = filepath.Clean(absDir)

	// Boundary check: absDir must be within absRoot
	if !isUnderRoot(absRoot, absDir) {
		return fmt.Errorf("tree walk: path boundary violation: local reference escapes the root boundary")
	}

	// Cycle detection
	if visited[absDir] {
		return fmt.Errorf("tree walk: cycle detected at %q", sanitizePath(absDir, absRoot))
	}
	visited[absDir] = true
	defer func() { delete(visited, absDir) }()

	// Read and parse kustomization.yaml (try both names)
	kust, err := readKustomization(absDir)
	if err != nil {
		return err
	}
	if kust == nil {
		// No kustomization file — skip silently (the entry might be a raw file)
		return nil
	}

	// Collect all reference fields
	allRefs := append(append(kust.Resources, kust.Components...), kust.Bases...)

	for _, raw := range allRefs {
		if raw == "" {
			continue
		}
		kind, ref, err := classifyKustomizeReference(raw)
		if err != nil {
			return fmt.Errorf("tree walk: invalid reference %q: %w", sanitizeRef(raw), err)
		}

		switch kind {
		case refKindLocal:
			// Local path: compute the child directory and recurse.
			childDir := filepath.Join(absDir, raw)
			// Before recursing, check the child is a directory (not a file resource).
			// If it's a file, skip it.
			info, statErr := os.Stat(childDir)
			if statErr != nil {
				// Could be a non-existent path — could be a future escape, skip gracefully
				// or propagate the escape if clearly outside root.
				absChild, evalErr := filepath.Abs(childDir)
				if evalErr == nil && !isUnderRoot(absRoot, absChild) {
					return fmt.Errorf("tree walk: path boundary violation: local reference %q escapes the root boundary", sanitizeRef(raw))
				}
				continue
			}
			if !info.IsDir() {
				continue
			}
			// Recursion: abs child path already computed above
			absChild := filepath.Clean(childDir)
			// Check escape before recursing (symlink-safe check after EvalSymlinks inside walkDir)
			if !isUnderRoot(absRoot, absChild) {
				return fmt.Errorf("tree walk: path boundary violation: local reference %q escapes the root boundary", sanitizeRef(raw))
			}
			if err := walkDir(absRoot, childDir, visited, remotes); err != nil {
				return err
			}
		case refKindRemoteGitDir, refKindRemoteFile:
			*remotes = append(*remotes, RemoteRef{Raw: raw, Kind: kind, Ref: ref})
		}
	}
	return nil
}

// readKustomization reads and parses a kustomization.yaml (or kustomization.yml)
// from dir. Returns nil (no error) if no kustomization file exists in the dir.
func readKustomization(dir string) (*kustomizationYAML, error) {
	for _, name := range konfig.RecognizedKustomizationFileNames() {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("tree walk: read %s: %w", filepath.Base(path), err)
		}
		var k kustomizationYAML
		if err := yaml.Unmarshal(data, &k); err != nil {
			return nil, fmt.Errorf("tree walk: parse %s: %w", filepath.Base(path), err)
		}
		return &k, nil
	}
	return nil, nil
}

// isUnderRoot returns true if candidate is equal to root or is a subdirectory of root.
// Both paths must be cleaned (no trailing separators, no symlinks pending resolution).
func isUnderRoot(root, candidate string) bool {
	if root == candidate {
		return true
	}
	// candidate must start with root + separator
	return strings.HasPrefix(candidate, root+string(filepath.Separator))
}

// sanitizePath returns a relative representation of path for use in error messages,
// avoiding leakage of absolute filesystem details while still being useful.
func sanitizePath(path, root string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "<path>"
	}
	return rel
}

// sanitizeRef sanitizes a raw reference string for error messages by replacing
// any userinfo or credentials with a placeholder. For remote refs, only the host
// portion is preserved; for local refs the value is returned as-is (no creds).
func sanitizeRef(raw string) string {
	// If it looks like it has userinfo, redact it entirely
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		// No userinfo: return a truncated version (avoid leaking long remote URLs)
		if len(raw) > 60 {
			return raw[:57] + "..."
		}
		return raw
	}
	// Has userinfo — return only scheme+host
	return u.Scheme + "://" + u.Host + "/..."
}
