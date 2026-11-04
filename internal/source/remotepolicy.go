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
	"regexp"
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
// Rejected forms (fail-closed, sanitized errors — raw value never echoed):
//   - The legacy go-getter git:: prefix is stripped; the underlying form is
//     then classified and may itself be rejected.
//   - SSH URLs (git@ SCP-style, ssh:// scheme)
//   - URLs with embedded userinfo (user:token@host) — use authSecretRef instead
//   - URLs with a fragment (#)
//   - HTTPS URLs with extra path segments after the repo root (>2 segments)
//     with no // separator, regardless of whether ?ref= is present —
//     the repo/path boundary is ambiguous.
//   - Remote git directory references without a mandatory ?ref=
func classifyKustomizeReference(raw string) (refKind, string, error) {
	// Finding 3: strip the legacy go-getter git:: prefix before any other check.
	// krusty silently strips it too; we strip it so we can classify (and reject)
	// the underlying form correctly.
	stripped := raw
	if startsWithIgnoreCase(raw, "git::") {
		stripped = raw[len("git::"):]
	}

	// --- SSH rejection (before any URL parse so we never include the raw value) ---
	// git@host:org/repo SCP style
	if isSCPStyle(stripped) {
		return 0, "", errors.New("remote reference: SCP-style SSH references are not supported; use HTTPS with authSecretRef for private repositories")
	}
	// ssh:// scheme
	if startsWithIgnoreCase(stripped, "ssh://") {
		return 0, "", errors.New("remote reference: SSH (ssh://) scheme is not supported; use HTTPS with authSecretRef for private repositories")
	}

	// --- Fragment rejection ---
	if strings.Contains(stripped, "#") {
		return 0, "", errors.New("remote reference: URL fragments (#) are not supported in kustomize references")
	}

	// --- Local path: no scheme, starts with ./ ../ a bare name, or absolute ---
	// A URL has a scheme (https:// / http://) or is a GitHub shorthand
	// (github.com/…). Everything else without one of those is local.
	if isLocalPath(stripped) {
		return refKindLocal, "", nil
	}

	// --- HTTPS / HTTP URL: parse carefully ---
	if isHTTPURL(stripped) {
		return classifyHTTPURL(stripped)
	}

	// --- GitHub shorthand: github.com/org/repo[//path][?ref=] (no scheme) ---
	if isGitHubShorthand(stripped) {
		return classifyGitHubShorthand(stripped)
	}

	// P1-3: Anything that reaches here is a URL-like or remote-looking string
	// that the classifier does not fully understand. Reject explicitly rather than
	// silently treating as local (which would let krusty fetch it unchecked).
	//
	// This catches forms such as:
	//   - file:///path/to/repo?ref=main  (file:// scheme — not a relative local path)
	//   - github.com:org/repo?ref=main   (SCP-without-@ — superficially looks local
	//     but contains a colon before the first slash, resembling a host:path form)
	//   - any other unrecognized scheme (e.g. ftp://, s3://)
	//
	// A genuine local path (relative or absolute) was already caught above by
	// isLocalPath; anything reaching here has scheme-like characteristics that
	// indicate it is not a plain relative filesystem path.
	if strings.Contains(stripped, "://") {
		// Unknown scheme (not https, http, or ssh which are handled/rejected above)
		return 0, "", errors.New("remote reference: unsupported URL scheme; only HTTPS remote bases are allowed")
	}
	// SCP-without-@: hostname:path where colon precedes any slash (looks like a
	// remote address but no git@ prefix). These are not valid local paths.
	if isSCPWithoutAt(stripped) {
		return 0, "", errors.New("remote reference: SCP-style references without 'user@' prefix are not supported; use HTTPS with authSecretRef for private repositories")
	}
	// Any remaining unrecognized form: fail closed.
	return 0, "", errors.New("remote reference: unrecognized reference form; only HTTPS remote bases and relative local paths are supported")
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
	_, after, found := strings.Cut(raw, "@")
	if !found {
		return false
	}
	// After @, must have host:path (colon before any slash)
	colonIdx := strings.Index(after, ":")
	slashIdx := strings.Index(after, "/")
	return colonIdx >= 0 && (slashIdx < 0 || colonIdx < slashIdx)
}

// isSCPWithoutAt detects SCP-like host:path references without a user@ prefix.
// These look like "hostname:org/repo" or "github.com:org/repo" — no scheme,
// but a colon appears before the first slash (the git SCP host:path separator).
//
// Critically, a '@' character may legitimately appear LATER in the path/query
// portion (e.g. "github.com:org/repo//base@variant?ref=main") without making
// the reference a git@ SCP-style URL. The structural test is: does a colon
// appear before the first '/', and is there no '@' BEFORE that colon (which
// would identify the git@host:path form handled by isSCPStyle)?
//
// This prevents the P1-3 bypass where "github.com:org/repo//base@variant?ref=main"
// was incorrectly classified as local because '@' appeared anywhere in the string.
func isSCPWithoutAt(raw string) bool {
	// Must not have a scheme (those are handled separately)
	if strings.Contains(raw, "://") {
		return false
	}
	// Find the first colon; must exist and not be at position 0
	colonIdx := strings.Index(raw, ":")
	if colonIdx <= 0 {
		return false
	}
	// Colon must appear before the first slash (host:path separator)
	slashIdx := strings.Index(raw, "/")
	if slashIdx >= 0 && colonIdx >= slashIdx {
		return false
	}
	// If a '@' appears BEFORE the colon, this is git@host:path — handled by
	// isSCPStyle, not our responsibility.
	atIdx := strings.Index(raw, "@")
	if atIdx >= 0 && atIdx < colonIdx {
		return false
	}
	return true
}

func startsWithIgnoreCase(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return strings.EqualFold(s[:len(prefix)], prefix)
}

// isLocalPath returns true if raw is a local filesystem path: starts with
// ./, ../, is just ".", or is a bare relative name without a scheme or
// colon-before-slash (which would indicate an SCP-style remote reference).
func isLocalPath(raw string) bool {
	if strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") || raw == "." || raw == ".." {
		return true
	}
	// Has a scheme → not a local path (handled by other classifiers or rejected)
	if strings.Contains(raw, "://") {
		return false
	}
	// github.com/ shorthand → not local
	if startsWithIgnoreCase(raw, "github.com/") {
		return false
	}
	// SCP-without-@ (e.g. "host:path") → not local; will be rejected downstream.
	// A colon before the first slash is the SCP host:path separator.
	if isSCPWithoutAt(raw) {
		return false
	}
	// No scheme, not github.com, not SCP-like → treat as local relative path.
	return true
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
		// Do NOT include raw or err.Error() — they may contain credential fragments.
		return 0, "", errors.New("remote reference: malformed URL")
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

	// Finding 4: validate repository/path boundary independent of ref presence.
	// krusty uses exactly 2 path segments (org/repo) as the default repo path
	// for non-file:// URLs. More segments without an explicit // separator or a
	// .git suffix means the repo/path boundary is ambiguous — reject always.
	if hasAmbiguousPathBoundary(u) {
		return 0, "", errors.New("remote reference: ambiguous remote path (multi-segment path with no // or .git boundary); use // to separate repo from subpath")
	}

	// Remote git directory: requires ?ref=
	// P1-2: use url.ParseQuery explicitly so a malformed query string (e.g.
	// "?ref=sha&bad=%zz") is rejected rather than silently returning partial
	// results. url.URL.Query() calls url.ParseQuery internally but discards
	// the error — a malformed query could extract a valid ?ref= and pass the
	// SHA gate even though krusty/git would see the WHOLE unparsable query
	// string and default to HEAD (bypassing the pinned SHA entirely).
	var ref string
	if u.RawQuery != "" {
		vals, qErr := url.ParseQuery(u.RawQuery)
		if qErr != nil {
			return 0, "", errors.New("remote reference: malformed query string in URL")
		}
		ref = vals.Get("ref")
	}
	if ref == "" {
		return 0, "", errors.New("remote reference: remote git directory references require a pinned ?ref= query parameter")
	}

	return refKindRemoteGitDir, ref, nil
}

// hasAmbiguousPathBoundary returns true when a URL's path contains more than 2
// non-empty segments (org/repo/extra…) with no explicit repo boundary marker
// (// separator or .git suffix). In such cases the repo-root/kust-root split
// is undefined and the reference must be rejected.
func hasAmbiguousPathBoundary(u *url.URL) bool {
	// If the raw URL contains //, the boundary is explicit — not ambiguous.
	// Note: the raw URL's "//" after stripping the scheme is the double-slash
	// marker, not the scheme "://". We check the raw URL's path portion.
	// url.Parse stores the path without the //-separator, so we check the
	// original string for the double-slash after the host.
	rawPath := u.RawPath
	if rawPath == "" {
		rawPath = u.Path
	}
	if strings.Contains(rawPath, "//") {
		return false
	}
	// Check for .git boundary marker in the path (e.g. repo.git/subdir)
	if strings.Contains(rawPath, ".git") {
		return false
	}
	// Count non-empty path segments; >2 is ambiguous
	return pathSegmentCount(u.Path) > 2
}

// classifyGitHubShorthand classifies a github.com/org/repo[//path][?ref=] reference.
func classifyGitHubShorthand(raw string) (refKind, string, error) {
	// Split off query to inspect the path portion separately.
	pathPart, query, _ := strings.Cut(raw, "?")

	// Finding 4 (round 2): apply the same repo/path boundary validation as the
	// HTTPS classifier. github.com/org/repo is exactly 3 path segments (host +
	// org + repo); any additional segment without a // separator is ambiguous.
	// Strip the "github.com/" prefix to get just the path, then check.
	if hasAmbiguousShorthandPath(pathPart) {
		return 0, "", errors.New("remote reference: ambiguous GitHub shorthand path (multi-segment path with no // boundary); use // to separate repo from subpath")
	}

	// Finding 5: only ?ref= is mandatory; do NOT fall back to ?version=.
	// Mirror classifyHTTPURL: reject a malformed query string explicitly rather
	// than silently swallowing the ParseQuery error. A malformed query could let
	// a valid-looking ?ref= pass the SHA gate while krusty/git sees the whole
	// unparsable query and defaults to HEAD (bypassing the pinned SHA).
	ref := ""
	if query != "" {
		vals, err := url.ParseQuery(query)
		if err != nil {
			return 0, "", errors.New("remote reference: malformed query string in GitHub shorthand reference")
		}
		ref = vals.Get("ref")
	}

	if ref == "" {
		return 0, "", errors.New("remote reference: GitHub shorthand references require a pinned ?ref= query parameter")
	}

	return refKindRemoteGitDir, ref, nil
}

// hasAmbiguousShorthandPath returns true when a GitHub shorthand path has more
// than 2 non-empty path segments (org + repo) without an explicit repo/path
// boundary marker (// separator or .git suffix). Mirrors the HTTPS check in
// hasAmbiguousPathBoundary so both forms treat the same markers as unambiguous.
//
// github.com/org/repo → 2 segments → unambiguous
// github.com/org/repo//subdir → has // → unambiguous
// github.com/org/repo.git/subdir → has .git → unambiguous
// github.com/org/repo/subdir → 3 segments, no marker → ambiguous → reject
func hasAmbiguousShorthandPath(pathPart string) bool {
	// If the path contains //, the boundary is explicit — not ambiguous.
	if strings.Contains(pathPart, "//") {
		return false
	}
	// .git suffix is an explicit boundary marker (e.g. repo.git/subdir).
	if strings.Contains(pathPart, ".git") {
		return false
	}
	// Strip the leading "github.com/" to count only the path segments after the host.
	afterHost := pathPart
	if _, rest, found := strings.Cut(pathPart, "/"); found {
		afterHost = rest // everything after "github.com/"
	}
	// >2 segments after the host means there's an extra subdir without a marker.
	return pathSegmentCount("/"+afterHost) > 2
}

// pathSegmentCount counts non-empty path segments in a URL path string.
func pathSegmentCount(path string) int {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return 0
	}
	count := 0
	for p := range strings.SplitSeq(path, "/") {
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
// Returns an ordered set of RemoteRef values discovered (DFS order,
// first-occurrence deduplicated by Raw value).
func treeWalkKustomize(rootDir string) ([]RemoteRef, error) {
	return treeWalkKustomizeFrom(rootDir, rootDir)
}

// treeWalkKustomizeFrom is the core implementation: walk starting at entryPoint,
// using isolationRoot as the boundary for local path / symlink escape checks.
//
// This separation is necessary for kustomize overlay layouts where the krusty
// entry point is a subpath of the clone root (e.g. cloneDir/seed) and the
// overlay legitimately references sibling directories (../base) that are within
// the clone but outside the entryPoint directory. In that case:
//
//	isolationRoot = cloneDir        (the true boundary — all of the clone)
//	entryPoint    = cloneDir/seed   (where krusty starts — the overlay)
//
// All local path resolution stays within isolationRoot; the walk starts at
// entryPoint so refs inside the overlay are actually inspected.
func treeWalkKustomizeFrom(isolationRoot, entryPoint string) ([]RemoteRef, error) {
	// Canonicalize the isolation root so we can detect escapes reliably.
	absIsolationRoot, err := filepath.EvalSymlinks(isolationRoot)
	if err != nil {
		return nil, fmt.Errorf("tree walk: resolve isolation root: %w", err)
	}
	absIsolationRoot = filepath.Clean(absIsolationRoot)

	// Canonicalize the entry point. It must be within the isolation root.
	absEntry, err := filepath.EvalSymlinks(entryPoint)
	if err != nil {
		return nil, fmt.Errorf("tree walk: resolve entry point: %w", err)
	}
	absEntry = filepath.Clean(absEntry)
	if !isUnderRoot(absIsolationRoot, absEntry) {
		return nil, errors.New("tree walk: entry point is outside isolation root")
	}

	var remotes []RemoteRef
	seen := make(map[string]bool)    // deduplicate remote refs by Raw
	visited := make(map[string]bool) // DFS stack for cycle detection (canonical dirs)

	if err := walkDir(absIsolationRoot, absEntry, visited, seen, &remotes); err != nil {
		return nil, err
	}
	return remotes, nil
}

// walkDir recursively visits a single directory's kustomization.yaml,
// accumulating remote refs and detecting cycles/escapes.
func walkDir(absRoot, dir string, visited, seen map[string]bool, remotes *[]RemoteRef) error {
	// Resolve the dir canonically (follow symlinks)
	absDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Non-existent target: still check boundary before giving up silently.
			// Use filepath.Clean(Abs) as a best-effort canonical path for boundary check.
			absApprox := filepath.Clean(dir)
			if !isUnderRoot(absRoot, absApprox) {
				return errors.New("tree walk: path boundary violation: local reference escapes the root boundary")
			}
			return nil // non-existent but in-bounds — skip
		}
		return fmt.Errorf("tree walk: resolve directory: %w", err)
	}
	absDir = filepath.Clean(absDir)

	// Boundary check: absDir must be within absRoot
	if !isUnderRoot(absRoot, absDir) {
		return errors.New("tree walk: path boundary violation: local reference escapes the root boundary")
	}

	// Cycle detection
	if visited[absDir] {
		return fmt.Errorf("tree walk: cycle detected at %q", sanitizePath(absDir, absRoot))
	}
	visited[absDir] = true
	defer func() { delete(visited, absDir) }()

	// Finding 2: boundary-check the kustomization file itself before reading it,
	// to catch a symlinked kustomization.yaml that resolves outside the root.
	kust, err := readKustomizationBounded(absRoot, absDir)
	if err != nil {
		return err
	}
	if kust == nil {
		// No kustomization file — skip silently (the entry might be a raw file)
		return nil
	}

	// Collect all reference fields into a freshly-allocated slice. Do NOT use
	// append(append(kust.Resources, kust.Components...), ...): if kust.Resources
	// has spare capacity (len < cap — not guaranteed either way by encoding/json
	// under sigs.k8s.io/yaml), the first append would write into Resources'
	// backing array, silently corrupting it for any later read.
	allRefs := make([]string, 0, len(kust.Resources)+len(kust.Components)+len(kust.Bases))
	allRefs = append(allRefs, kust.Resources...)
	allRefs = append(allRefs, kust.Components...)
	allRefs = append(allRefs, kust.Bases...)

	for _, raw := range allRefs {
		if raw == "" {
			continue
		}
		kind, ref, err := classifyKustomizeReference(raw)
		if err != nil {
			// Finding 1: do NOT include raw in the error message.
			return fmt.Errorf("tree walk: invalid reference in kustomization: %w", err)
		}

		switch kind {
		case refKindLocal:
			// P1-4: Reject absolute local paths outright. There is no legitimate
			// reason for a kustomize overlay's resources/components/bases field to
			// reference an absolute filesystem path in the context of a remote-base
			// policy check. More importantly, filepath.Join(absDir, rawAbsPath)
			// returns absDir+rawAbsPath (a path inside the root that passes the
			// boundary check), while krusty's LoadRestrictionsNone loader interprets
			// the kustomization's original raw absolute path directly — completely
			// bypassing the DDO isolation boundary. Fail closed.
			if filepath.IsAbs(raw) {
				return errors.New("tree walk: absolute path references are not permitted in kustomize overlays")
			}
			// Local path: compute the child path and boundary-check before deciding
			// whether to recurse (directory) or skip (file).
			childPath := filepath.Join(absDir, raw)

			// Finding 2: resolve symlinks and check boundary for every referenced
			// path, including files, before any stat-based skip decision.
			absChild, boundErr := resolveAndCheckBoundary(absRoot, childPath)
			if boundErr != nil {
				return boundErr
			}
			if absChild == "" {
				// Non-existent but in-bounds — skip.
				continue
			}

			info, statErr := os.Lstat(absChild)
			if statErr != nil {
				continue // resolved but stat failed — skip
			}
			if !info.IsDir() {
				continue // file resource — boundary already checked above, skip recursion
			}

			if err := walkDir(absRoot, absChild, visited, seen, remotes); err != nil {
				return err
			}

		case refKindRemoteGitDir, refKindRemoteFile:
			// Finding 6: deduplicate by Raw value, preserving first-discovery order.
			if !seen[raw] {
				seen[raw] = true
				*remotes = append(*remotes, RemoteRef{Raw: raw, Kind: kind, Ref: ref})
			}
		}
	}
	return nil
}

// resolveAndCheckBoundary resolves path (which may contain symlinks) to its
// canonical form and checks it is within absRoot. Returns the canonical path, or
// ("", nil) if the path does not exist (but is in-bounds), or ("", err) for
// boundary violations or unexpected errors.
func resolveAndCheckBoundary(absRoot, path string) (string, error) {
	abs, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Path doesn't exist. Use Abs to get a non-symlinked approximation
			// and check the boundary.
			approx, aErr := filepath.Abs(path)
			if aErr != nil {
				// P1-5: do NOT include aErr.Error() — it may contain the raw path
				// (which can be a credential-bearing reference). Return a fixed message.
				_ = aErr // discarded intentionally to prevent credential leakage
				return "", errors.New("tree walk: resolve path: could not determine absolute path (fail-closed)")
			}
			approx = filepath.Clean(approx)
			if !isUnderRoot(absRoot, approx) {
				return "", errors.New("tree walk: path boundary violation: local reference escapes the root boundary")
			}
			return "", nil // non-existent, in-bounds
		}
		// P1-5: do NOT include err.Error() — it may contain the raw path (which
		// can be a credential-bearing reference). Return a fixed message.
		_ = err // discarded intentionally to prevent credential leakage
		return "", errors.New("tree walk: resolve path: could not resolve filesystem path (fail-closed)")
	}
	abs = filepath.Clean(abs)
	if !isUnderRoot(absRoot, abs) {
		return "", errors.New("tree walk: path boundary violation: local reference escapes the root boundary")
	}
	return abs, nil
}

// readKustomizationBounded reads and parses a kustomization.yaml from dir,
// checking that the file itself (after symlink resolution) is within absRoot.
// Returns nil (no error) if no kustomization file exists in the dir.
func readKustomizationBounded(absRoot, dir string) (*kustomizationYAML, error) {
	for _, name := range konfig.RecognizedKustomizationFileNames() {
		path := filepath.Join(dir, name)

		// Finding 2: resolve the kustomization file itself and check its boundary.
		absFile, err := filepath.EvalSymlinks(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			// P1-5: do NOT include err.Error() or the path — the path may contain
			// credential-bearing text (e.g. a resource entry like
			// "user:TOKEN@host/path" that becomes a directory name). Discard the
			// raw OS error and return a fixed-text sanitized message.
			_ = err // discarded intentionally to prevent credential leakage
			return nil, errors.New("tree walk: resolve kustomization file: could not resolve filesystem path (fail-closed)")
		}
		absFile = filepath.Clean(absFile)
		if !isUnderRoot(absRoot, absFile) {
			return nil, errors.New("tree walk: path boundary violation: kustomization file escapes the root boundary")
		}

		data, err := os.ReadFile(absFile)
		if err != nil {
			// P1-5: do NOT include err.Error() or the absFile path — the OS error
			// embeds the full path, which can contain a credential-bearing directory
			// name (e.g. ".../user:SUPERSECRET@host/kustomization.yaml: is a
			// directory"). Discard the raw OS error and return a sanitized message.
			_ = err // discarded intentionally to prevent credential leakage
			return nil, errors.New("tree walk: read kustomization file: could not read file (fail-closed)")
		}
		var k kustomizationYAML
		if err := yaml.Unmarshal(data, &k); err != nil {
			// Do NOT wrap the raw parse error: the YAML parser may echo back
			// fragments of the file content (including credential-bearing URLs
			// present as YAML keys/values), which would constitute a credential
			// leak.  Return a fixed-text error instead.
			return nil, errors.New("tree walk: parse kustomization file: invalid YAML")
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

// ---------------------------------------------------------------------------
// Ref extraction
// ---------------------------------------------------------------------------

// extractRef extracts the pinned ref (tag, branch, or SHA) from a classified
// remote reference. It handles three cases:
//
//  1. HTTPS / GitHub shorthand with a ?ref= query parameter — returns the
//     value of ?ref=.
//  2. raw.githubusercontent.com URLs — the ref is the 3rd path segment
//     (index 2, 0-based) immediately after org/repo: /org/repo/<ref>/...
//  3. Anything else (including local paths and unrecognised forms) — returns "".
//
// extractRef never echoes the raw value in an error; it simply returns "" on
// any form it cannot recognise.
func extractRef(raw string) string {
	// Strip go-getter prefix before any inspection.
	s := raw
	if startsWithIgnoreCase(raw, "git::") {
		s = raw[len("git::"):]
	}

	// raw.githubusercontent.com: ref is the 3rd path segment after the host
	// (path layout: /org/repo/<ref>/[rest...]).
	if isHTTPURL(s) {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		if strings.EqualFold(u.Hostname(), "raw.githubusercontent.com") {
			return rawGitHubRef(u.Path)
		}
		// Generic HTTPS URL: use ?ref= query param.
		return u.Query().Get("ref")
	}

	// GitHub shorthand (no scheme): extract ?ref= from the query portion.
	if isGitHubShorthand(s) {
		_, query, _ := strings.Cut(s, "?")
		if query == "" {
			return ""
		}
		vals, err := url.ParseQuery(query)
		if err != nil {
			return ""
		}
		return vals.Get("ref")
	}

	return ""
}

// rawGitHubRef extracts the ref (3rd path segment) from a raw.githubusercontent.com
// URL path of the form /org/repo/<ref>/... Segments are 1-based in the split
// (the first element after Split("/", ...) is empty for the leading slash).
func rawGitHubRef(path string) string {
	// path = /org/repo/ref/...
	// Split gives: ["", "org", "repo", "ref", ...]
	parts := strings.SplitN(path, "/", 5)
	if len(parts) >= 4 && parts[3] != "" {
		return parts[3]
	}
	return ""
}

// ---------------------------------------------------------------------------
// SHA predicate
// ---------------------------------------------------------------------------

// reSHA40 matches a full 40-character lowercase hexadecimal Git commit SHA.
var reSHA40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// isSHAPinned returns true if ref is a full 40-character lowercase hex Git
// commit SHA (i.e. not a tag, branch, or symbolic ref). Uppercase hex is
// intentionally rejected: Git SHA refs in kustomization.yaml must be
// lowercase to match krusty's normalisation.
func isSHAPinned(ref string) bool {
	return reSHA40.MatchString(ref)
}

// ---------------------------------------------------------------------------
// Preflight SHA-pin gate
// ---------------------------------------------------------------------------

// preflightSHAGate runs the classify → tree-walk → SHA-gate preflight sequence
// for a resolved kustomize root directory.
//
// Parameters:
//   - isolationRoot: the boundary for local path / symlink escape checks.
//     For a production git clone, this is the clone directory. For a subpath
//     overlay (e.g. cloneDir/seed), pass cloneDir so that ../base references
//     that stay within the clone are allowed.
//   - entryPoint: where the walk starts — the exact directory krusty receives.
//     For a subpath overlay, this is cloneDir/seed. The walk inspects the
//     overlay's kustomization.yaml and follows local refs within isolationRoot.
//   - requireSHAPinnedRemoteBases: enables the strict SHA-pin phase (Phase 2).
//
// The preflight has two phases:
//
//  1. Classification + tree-walk (ALWAYS runs, including permissive mode):
//     treeWalkKustomizeFrom visits every locally reachable kustomization
//     starting at entryPoint, classifying every resources/components/bases
//     entry. Invalid forms (SSH URLs, embedded credentials, URL fragments,
//     ambiguous paths, cycles, boundary escapes) cause an immediate error
//     before any network/git call. This is a security invariant: even in
//     permissive mode, DDO must not pass credential-bearing or SSH URLs
//     through to krusty.
//
//  2. SHA-pin gate (only when requireSHAPinnedRemoteBases = true):
//     Every discovered remote reference must be pinned to a full 40-hex SHA.
//     Any tag, branch, or undeterminable ref causes an immediate error. No
//     network or git call is made before this check succeeds.
//
// Error messages are sanitized: the raw URL and ref value are never included.
func preflightSHAGate(isolationRoot, entryPoint string, requireSHAPinnedRemoteBases bool) error {
	// Phase 1: classify + tree-walk — always runs to catch disallowed forms.
	// treeWalkKustomizeFrom classifies every reference starting at entryPoint
	// and fails immediately on SSH refs, embedded credentials, URL fragments,
	// ambiguous paths, cycles, and boundary escapes. No network or git calls.
	remotes, err := treeWalkKustomizeFrom(isolationRoot, entryPoint)
	if err != nil {
		// Do NOT wrap with %w: treeWalkKustomizeFrom errors are already sanitized
		// fixed-text messages, but using %w here would allow a future change to
		// accidentally propagate a raw underlying error (e.g. a YAML parser
		// message that echoes back a credential-bearing URL key). Use %v so
		// the message is included as text but no error wrapping chain is exposed.
		return fmt.Errorf("preflight: tree walk failed: %v", err) //nolint:errorlint // intentional: no %w to prevent future accidental credential leakage through wrapped error chain
	}

	// Phase 2: SHA-pin gate — only in strict mode.
	if !requireSHAPinnedRemoteBases {
		return nil
	}

	for _, r := range remotes {
		ref := r.Ref // already extracted by classifyKustomizeReference for git-dir refs
		if ref == "" {
			// For refKindRemoteFile, classifyKustomizeReference leaves Ref empty.
			// We must extract the ref ourselves from the raw URL.
			ref = extractRef(r.Raw)
		}
		if !isSHAPinned(ref) {
			// Sanitized: we do not echo r.Raw or ref.
			return fmt.Errorf(
				"preflight: remote base is not SHA-pinned (requireSHAPinnedRemoteBases=true); "+
					"pin the ?ref= (or equivalent) to a full 40-character lowercase hex SHA; "+
					"found %d remote reference(s), at least one uses a tag or branch ref",
				len(remotes),
			)
		}
	}
	return nil
}
