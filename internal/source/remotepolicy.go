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

// kustField identifies which kustomization.yaml field a reference came from.
// The strict-mode validation pass (D2') uses this because loader semantics
// differ by field: upstream dispatches `resources` and `components` through
// different code paths, and most other fetch-capable fields are local-only in
// our strict contract.
type kustField int

const (
	// fieldResources is the resources: field (accepts remote git dirs + files).
	fieldResources kustField = iota
	// fieldComponents is the components: field (upstream calls Loader.New
	// directly; a validated HTTPS git ref here is not rejected for ambiguity).
	fieldComponents
	// fieldBases is the legacy bases: field (same acceptance as resources).
	fieldBases
	// fieldTreeEdge marks a reference drawn from generators/transformers/
	// validators entries (path form — inline docs are skipped before tagging).
	// krusty runs these through accumulateResources just like resource-like
	// fields, so a LOCAL DIRECTORY entry is a real kustomization tree edge that
	// krusty follows. Semantics:
	//   - walkDir recurses into a referenced local DIRECTORY (same as resource-
	//     like fields), so an unpinned remote base inside that directory is
	//     caught in strict mode.
	//   - A DIRECT remote value in this field is rejected in strict mode (same
	//     as fieldLocalOnly Rule 1).
	//   - A referenced local FILE (non-dir) is not recursed into; strict mode
	//     does NOT parse inside builtin config files (e.g. a PatchTransformer's
	//     embedded path:) — that is a documented limitation.
	fieldTreeEdge
	// fieldLocalOnly marks a reference drawn from a field that, under the strict
	// contract, must resolve to a LOCAL path only (patches[].path,
	// replacements[].path, crds, configurations, openapi.path,
	// configMap/secret generator files/envs). A remote value in such a field is
	// rejected in strict mode.
	fieldLocalOnly
)

func (f kustField) String() string {
	switch f {
	case fieldResources:
		return "resources"
	case fieldComponents:
		return "components"
	case fieldBases:
		return "bases"
	case fieldTreeEdge:
		return "generator/transformer/validator"
	case fieldLocalOnly:
		return "local-only field"
	default:
		return fmt.Sprintf("kustField(%d)", int(f))
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
	// Field is the kustomization field this reference was drawn from. The
	// strict-mode validation pass branches on it (loader semantics differ by
	// field; most fields are local-only under the strict contract).
	Field kustField
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
	// Strip the legacy go-getter git:: prefix before any other check.
	// krusty silently strips it too; we strip it so we can classify (and reject)
	// the underlying form correctly.
	stripped := raw
	if startsWithIgnoreCase(raw, "git::") {
		stripped = raw[len("git::"):]
	}

	// --- SSH rejection (before any URL parse so we never include the raw value) ---
	// git@host:org/repo SCP style
	if isSCPStyle(stripped) {
		return 0, "", errors.New("remote reference: SCP-style SSH references are not supported; use anonymous public HTTPS, or vendor the base into the repository")
	}
	// ssh:// scheme
	if startsWithIgnoreCase(stripped, "ssh://") {
		return 0, "", errors.New("remote reference: SSH (ssh://) scheme is not supported; use anonymous public HTTPS, or vendor the base into the repository")
	}

	// --- Fragment rejection ---
	if strings.Contains(stripped, "#") {
		return 0, "", errors.New("remote reference: URL fragments (#) are not supported in kustomize references")
	}

	// --- Username-prefixed SCP/SSH form WITHOUT a host:path colon ---
	// krusty's repospec.go extractUsername matches a leading `user@` with the
	// anchored pattern ^[a-zA-Z][a-zA-Z0-9-]*@ and then treats the remainder as
	// a remote git target. A reference like "git@github.com/example/repo//base"
	// has NO colon before the first slash, so neither isSCPStyle (needs a colon
	// after the @) nor isSCPWithoutAt (needs a colon before the first slash)
	// fires — isLocalPath would then classify it as a local path, the tree
	// walker would skip it (the path does not exist on disk), and krusty would
	// still fetch it over SSH, bypassing BOTH our SSH rejection and the SHA-pin
	// gate. Reject any leading-username form here, before local classification.
	if hasLeadingGitUsername(stripped) {
		return 0, "", errors.New("remote reference: username-prefixed (user@…) git references are not supported; use anonymous public HTTPS, or vendor the base into the repository")
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

	// Anything that reaches here is a URL-like or remote-looking string
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
		return 0, "", errors.New("remote reference: SCP-style references without 'user@' prefix are not supported; use anonymous public HTTPS, or vendor the base into the repository")
	}
	// Any remaining unrecognized form: fail closed.
	return 0, "", errors.New("remote reference: unrecognized reference form; only HTTPS remote bases and relative local paths are supported")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// reGitUsername matches a leading git "username@" prefix using the same anchored
// pattern krusty uses (api/internal/git/repospec.go extractUsername):
// ^[A-Za-z][A-Za-z0-9-]*@ . A reference carrying such a prefix is a remote git
// target to krusty regardless of whether a host:path colon is present.
var reGitUsername = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*@`)

// hasLeadingGitUsername reports whether raw begins with a git "user@" prefix
// (no URL scheme). It deliberately matches krusty's extractUsername so DDO
// rejects exactly the forms krusty would otherwise fetch over SSH.
func hasLeadingGitUsername(raw string) bool {
	// A scheme-bearing URL (https://user@host) is handled by the URL parser,
	// which rejects embedded userinfo separately.
	if strings.Contains(raw, "://") {
		return false
	}
	return reGitUsername.MatchString(raw)
}

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
// This prevents the bypass where "github.com:org/repo//base@variant?ref=main"
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

	// Reject plaintext http:// — only HTTPS remote bases are allowed. isHTTPURL
	// matches both schemes so we gate here on the parsed scheme. krusty/git would
	// otherwise fetch over cleartext; a Pod-level GIT_ALLOW_PROTOCOL=https covers
	// git fetches for refs *inside* a remote base, but this check covers the
	// top-level reference DDO classifies.
	if strings.EqualFold(u.Scheme, "http") {
		return 0, "", errors.New("remote reference: plaintext http:// is not supported; use HTTPS")
	}

	// Reject embedded userinfo (credentials in URL)
	if u.User != nil {
		return 0, "", errors.New("remote reference: URL must not embed credentials; use anonymous public HTTPS, or vendor the base into the repository")
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

	// Validate repository/path boundary independent of ref presence.
	// krusty uses exactly 2 path segments (org/repo) as the default repo path
	// for non-file:// URLs. More segments without an explicit // separator or a
	// .git suffix means the repo/path boundary is ambiguous — reject always.
	if hasAmbiguousPathBoundary(u) {
		return 0, "", errors.New("remote reference: ambiguous remote path (multi-segment path with no // or .git boundary); use // to separate repo from subpath")
	}

	// Remote git directory: requires ?ref=
	// Use url.ParseQuery explicitly so a malformed query string (e.g.
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

	// Apply the same repo/path boundary validation as the
	// HTTPS classifier. github.com/org/repo is exactly 3 path segments (host +
	// org + repo); any additional segment without a // separator is ambiguous.
	// Strip the "github.com/" prefix to get just the path, then check.
	if hasAmbiguousShorthandPath(pathPart) {
		return 0, "", errors.New("remote reference: ambiguous GitHub shorthand path (multi-segment path with no // boundary); use // to separate repo from subpath")
	}

	// Only ?ref= is mandatory; do NOT fall back to ?version=.
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

// kustomizationYAML is a typed struct for parsing kustomization.yaml. We parse
// every top-level field that can carry a reference to another resource, patch,
// generator config, or data file — not only resources/components/bases. The
// strict-mode policy (D1') enforces its contract on the TOP-LEVEL references
// the operator parses here, before handing the root to krusty. Transitive refs
// that krusty resolves internally are NOT pre-enforced (same model as Flux /
// Argo CD): they are contained operationally (NetworkPolicy egress + least-
// privilege credentials) plus the always-on HTTPS-only and transport-timeout
// hardening.
//
// Field groups:
//   - Resource-like (may legitimately be remote): resources, components, bases.
//   - Tree-edge (generators/transformers/validators path form): a local directory
//     here is a real kustomization tree edge krusty follows; walkDir recurses.
//     A direct remote value is rejected in strict mode (same as local-only).
//     Inline multiline docs are skipped before tagging — they are not references.
//   - Local-only under the strict contract (a remote value is rejected in
//     strict mode): patches[].path, patchesJson6902[].path,
//     patchesStrategicMerge[] (file-path form), replacements[].path, crds,
//     configurations, openapi.path, and the configMapGenerator/secretGenerator
//     files/envs.
type kustomizationYAML struct {
	// Resource-like fields: remote values permitted (subject to SHA-pin gate).
	Resources  []string `json:"resources,omitempty"`
	Components []string `json:"components,omitempty"`
	Bases      []string `json:"bases,omitempty"`

	// Plugin config references. Each entry is either a path to a config file or
	// an inline config. Inline multiline docs (containing "\n") are skipped —
	// only single-line path entries are treated as references (fieldTreeEdge).
	// krusty runs path entries through accumulateResources, so a local directory
	// is a real kustomization tree edge and walkDir recurses into it in strict
	// mode; a direct remote value is rejected in strict mode. NOTE: strict mode
	// recurses into referenced local directories but does NOT parse inside
	// builtin config files (e.g. a PatchTransformer's embedded path: field) —
	// that is a documented limitation (see proposal.md §remote-base-policy).
	Generators   []string `json:"generators,omitempty"`
	Transformers []string `json:"transformers,omitempty"`
	Validators   []string `json:"validators,omitempty"`

	// Patch references. patches[].path and patchesJson6902[].path point to local
	// patch files; patchesStrategicMerge entries may be an inline patch document
	// OR a file path — we only treat the file-path form as a reference.
	Patches               []patchEntry       `json:"patches,omitempty"`
	PatchesJSON6902       []patchJSON6902    `json:"patchesJson6902,omitempty"`
	PatchesStrategicMerge []string           `json:"patchesStrategicMerge,omitempty"`
	Replacements          []replacementEntry `json:"replacements,omitempty"`

	// Data-generator sources. files[] and envs[] entries use the upstream
	// "key=path" / "path" syntax; the path portion is the reference.
	ConfigMapGenerator []generatorArgs `json:"configMapGenerator,omitempty"`
	SecretGenerator    []generatorArgs `json:"secretGenerator,omitempty"`

	// Schema / openapi references (local-only in strict mode).
	Crds           []string      `json:"crds,omitempty"`
	Configurations []string      `json:"configurations,omitempty"`
	OpenAPI        *openAPIField `json:"openapi,omitempty"`
}

// patchEntry mirrors the upstream patches[] element (we only need Path).
type patchEntry struct {
	Path string `json:"path,omitempty"`
	// Patch is the inline patch body (ignored for reference extraction).
	Patch string `json:"patch,omitempty"`
}

// patchJSON6902 mirrors the upstream patchesJson6902[] element (Path only).
type patchJSON6902 struct {
	Path string `json:"path,omitempty"`
}

// replacementEntry mirrors the upstream replacements[] element (Path only; the
// inline `source`/`targets` form carries no external reference).
type replacementEntry struct {
	Path string `json:"path,omitempty"`
}

// generatorArgs mirrors the upstream configMapGenerator/secretGenerator element
// insofar as we need the file/env source paths.
type generatorArgs struct {
	Files []string `json:"files,omitempty"`
	Envs  []string `json:"envs,omitempty"`
	// Env is the legacy singular form (older kustomization schema).
	Env string `json:"env,omitempty"`
}

// openAPIField mirrors the upstream openapi: field (we only need Path).
type openAPIField struct {
	Path string `json:"path,omitempty"`
}

// generatorArgSourcePath extracts the path portion from a configMapGenerator /
// secretGenerator files[]/envs[] entry, which uses the upstream "key=path"
// syntax (an "=" separates an optional key from the source path). When no "="
// is present the whole string is the path. We must NOT classify the entire
// "key=path" string, or a key containing "://" would be misread.
func generatorArgSourcePath(entry string) string {
	// Upstream (kustomize api/types) treats the FIRST "=" as the key/path
	// separator for files[]/envs[]. A path cannot contain "=" in this syntax.
	if _, path, found := strings.Cut(entry, "="); found {
		return path
	}
	return entry
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

	// Boundary-check the kustomization file itself before reading it,
	// to catch a symlinked kustomization.yaml that resolves outside the root.
	kust, err := readKustomizationBounded(absRoot, absDir)
	if err != nil {
		return err
	}
	if kust == nil {
		// No kustomization file — skip silently (the entry might be a raw file)
		return nil
	}

	// Collect every reference together with the field it came from. Field
	// context is required by the strict-mode validation pass (D1'/D2'): remote
	// values are only legitimate in resource-like fields, and loader semantics
	// differ between resources and components. Using a freshly-allocated slice
	// (never append-aliasing kust.Resources' backing array) is also required for
	// correctness — see the original #r1 aliasing fix.
	fieldRefs := collectFieldRefs(kust)

	for _, fr := range fieldRefs {
		raw := fr.raw
		if raw == "" {
			continue
		}
		field := fr.field
		kind, ref, err := classifyKustomizeReference(raw)
		if err != nil {
			// Do NOT include raw in the error message (it may contain a URL/credentials).
			return fmt.Errorf("tree walk: invalid reference in kustomization: %w", err)
		}

		switch kind {
		case refKindLocal:
			// Reject absolute local paths outright. There is no legitimate
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

			// Resolve symlinks and check boundary for every referenced
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

			// Only recurse into directories reached via resource-like fields
			// or fieldTreeEdge (generators/transformers/validators path form).
			// Pure local-only fields (patches, crds, configMap/secretGenerator
			// sources, ...) never form a kustomization tree edge — there is
			// nothing to recurse into; the directory case is unreachable for
			// them in practice, but guard anyway.
			// fieldTreeEdge entries DO recurse: krusty runs them through
			// accumulateResources, so an unpinned remote base inside a
			// transformer-referenced local dir would be fetched un-vetted
			// in strict mode without this recursion.
			if field == fieldLocalOnly {
				continue
			}
			if err := walkDir(absRoot, absChild, visited, seen, remotes); err != nil {
				return err
			}

		case refKindRemoteGitDir, refKindRemoteFile:
			// Deduplicate remote refs, preserving first-discovery order.
			// Carry the field context so the strict pass can apply field-specific
			// rules (remote values are only legitimate in resource-like fields, and
			// an HTTPS git-dir ref is admissible under components but not under
			// resources/bases).
			//
			// The dedup key is (raw + field), NOT raw alone: the same URL can
			// appear in both a components field (strict-accept) and a
			// resources/bases field (strict-reject). Keying on raw alone would keep
			// only the first-discovered occurrence and could silently drop the
			// stricter resources occurrence, letting an unsafe fetch through. Keying
			// on (raw, field) keeps both so validateStrictRemoteRefs sees — and can
			// reject — the resources occurrence.
			key := raw + "\x00" + field.String()
			if !seen[key] {
				seen[key] = true
				*remotes = append(*remotes, RemoteRef{Raw: raw, Kind: kind, Ref: ref, Field: field})
			}
		}
	}
	return nil
}

// fieldRef pairs a raw reference string with the kustomization field it came
// from, so the strict-mode validation pass can apply field-specific rules.
type fieldRef struct {
	raw   string
	field kustField
}

// collectFieldRefs gathers every reference string from a parsed kustomization,
// tagged with its originating field. Resource-like fields (resources/
// components/bases) may legitimately be remote; every other fetch-capable field
// is marked fieldLocalOnly so the strict pass rejects a remote value there.
//
// Order is deterministic: resource-like fields first (resources, components,
// bases) in their original slice order, then local-only fields. This preserves
// the pre-existing DFS/first-discovery ordering for the common case and keeps
// error messages stable.
//
// A freshly-allocated slice is returned; we never append into any input slice's
// backing array (the #r1 aliasing invariant).
func collectFieldRefs(kust *kustomizationYAML) []fieldRef {
	refs := make([]fieldRef, 0,
		len(kust.Resources)+len(kust.Components)+len(kust.Bases))

	for _, r := range kust.Resources {
		refs = append(refs, fieldRef{raw: r, field: fieldResources})
	}
	for _, c := range kust.Components {
		refs = append(refs, fieldRef{raw: c, field: fieldComponents})
	}
	for _, b := range kust.Bases {
		refs = append(refs, fieldRef{raw: b, field: fieldBases})
	}

	// Local-only fields. A remote value in any of these is rejected in strict
	// mode (fieldLocalOnly). In permissive mode they are classified for the
	// always-on security invariants (SSH/credential/fragment rejection) but a
	// remote git-dir/file value is simply collected like any other remote ref.
	addLocal := func(s string) {
		refs = append(refs, fieldRef{raw: s, field: fieldLocalOnly})
	}

	// Tree-edge fields (generators/transformers/validators, path form only).
	// krusty runs these through accumulateResources so a local directory is a
	// real kustomization tree edge that walkDir must recurse into in strict mode.
	// Inline multiline docs (containing "\n") are skipped here — they are not
	// references; classifyKustomizeReference would misread the first line of
	// "apiVersion: builtin\n..." as an SCP host:path, causing a spurious error.
	// A direct remote value in this field is rejected in strict mode (same as
	// fieldLocalOnly Rule 1, applied via fieldTreeEdge in validateStrictRemoteRefs).
	addTreeEdge := func(s string) {
		if s != "" && !strings.ContainsAny(s, "\n") {
			refs = append(refs, fieldRef{raw: s, field: fieldTreeEdge})
		}
	}
	for _, g := range kust.Generators {
		addTreeEdge(g)
	}
	for _, t := range kust.Transformers {
		addTreeEdge(t)
	}
	for _, v := range kust.Validators {
		addTreeEdge(v)
	}
	for _, p := range kust.Patches {
		if p.Path != "" {
			addLocal(p.Path)
		}
	}
	for _, p := range kust.PatchesJSON6902 {
		if p.Path != "" {
			addLocal(p.Path)
		}
	}
	for _, p := range kust.PatchesStrategicMerge {
		// A patchesStrategicMerge entry is EITHER an inline patch document OR a
		// file path. Inline patches contain YAML/newlines or a leading "apiVersion"
		// etc.; a bare path does not. We only treat it as a reference when it looks
		// like a single-line path (no newline). classifyKustomizeReference then
		// decides local vs remote; inline multiline docs are skipped here.
		if p != "" && !strings.ContainsAny(p, "\n") {
			addLocal(p)
		}
	}
	for _, r := range kust.Replacements {
		if r.Path != "" {
			addLocal(r.Path)
		}
	}
	for _, g := range kust.ConfigMapGenerator {
		for _, f := range g.Files {
			addLocal(generatorArgSourcePath(f))
		}
		for _, e := range g.Envs {
			addLocal(generatorArgSourcePath(e))
		}
		if g.Env != "" {
			addLocal(generatorArgSourcePath(g.Env))
		}
	}
	for _, g := range kust.SecretGenerator {
		for _, f := range g.Files {
			addLocal(generatorArgSourcePath(f))
		}
		for _, e := range g.Envs {
			addLocal(generatorArgSourcePath(e))
		}
		if g.Env != "" {
			addLocal(generatorArgSourcePath(g.Env))
		}
	}
	for _, c := range kust.Crds {
		addLocal(c)
	}
	for _, c := range kust.Configurations {
		addLocal(c)
	}
	if kust.OpenAPI != nil && kust.OpenAPI.Path != "" {
		addLocal(kust.OpenAPI.Path)
	}

	return refs
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
				// Do NOT include aErr.Error() — it may contain the raw path
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
		// Do NOT include err.Error() — it may contain the raw path (which
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

		// Resolve the kustomization file itself and check its boundary.
		absFile, err := filepath.EvalSymlinks(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			// Do NOT include err.Error() or the path — the path may contain
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
			// Do NOT include err.Error() or the absFile path — the OS error
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

	// Phase 2a (D2'): strict-mode acceptance rules. Classification stays flag-
	// agnostic (Phase 1), so these field/kind-specific rejections apply ONLY in
	// strict mode — flag-off behavior is unchanged. This rejects remote values
	// in local-only fields, closes the HTTP-first remote-file bypass (accepting
	// only canonical raw.githubusercontent.com SHA-pinned file URLs), and
	// rejects ambiguous HTTPS git-dir forms outside components.
	if err := validateStrictRemoteRefs(remotes); err != nil {
		return err
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

// validateStrictRemoteRefs applies the strict-mode (D2') acceptance rules to the
// remote references discovered by the tree walk. It is called ONLY when
// requireSHAPinnedRemoteBases is true, so permissive (flag-off) behavior is
// completely unaffected.
//
// Rules (per the Flux/Argo-CD-aligned top-level model + oracle guidance):
//
//  1. A remote value in a local-only field (patches/replacements/crds/
//     configurations/openapi/generator-transformer-validator configs/
//     configMap+secretGenerator files/envs) is rejected: those fields must
//     resolve to a local path under the strict contract.
//
//  2. A remote-FILE reference (refKindRemoteFile) is accepted only when it is a
//     canonical raw.githubusercontent.com URL whose ref path segment is a full
//     40-hex SHA. Every other remote-file URL is rejected. This closes the
//     bypass where krusty's loader does an HTTP GET FIRST for any https URL
//     ending .yaml/.yml/.json (or a git-looking https://host/org/repo?ref=<sha>),
//     so a server could return arbitrary unpinned content.
//
//  3. An ambiguous HTTPS git-dir reference is rejected EXCEPT in the components
//     field. Upstream dispatches components through Loader.New directly (not the
//     HTTP-first file branch), so a validated HTTPS git ref there is not
//     ambiguous; in resources/bases the same spelling can be served over HTTP.
//
// All messages are sanitized: the raw URL is never echoed.
func validateStrictRemoteRefs(remotes []RemoteRef) error {
	for _, r := range remotes {
		// Rule 1: remote values are only legitimate in resource-like fields.
		// fieldLocalOnly entries must resolve to local paths only; fieldTreeEdge
		// entries (generators/transformers/validators) may reference local
		// directories that krusty recurses into, but a DIRECT remote value in
		// such a field is also rejected — only the local directory path form is
		// allowed (the remote base inside it is validated by walkDir recursion).
		if r.Field == fieldLocalOnly || r.Field == fieldTreeEdge {
			return fmt.Errorf(
				"preflight: remote reference found in a local-only field (%s); "+
					"in strict mode patches, replacements, crds, configurations, openapi, "+
					"generator/transformer/validator configs, and configMap/secretGenerator "+
					"sources must reference local paths only — vendor the content into the repository",
				r.Field,
			)
		}

		switch r.Kind {
		case refKindRemoteFile:
			// Rule 2a: components must be directories (upstream dispatches them
			// through Loader.New, which expects a kustomization root, not a
			// single file). A raw.githubusercontent.com file URL — even a
			// canonical SHA-pinned one — is therefore rejected in components.
			if r.Field == fieldComponents {
				return errors.New(
					"preflight: remote file reference in a components field; " +
						"components must reference a kustomization directory, not a single file — " +
						"use a SHA-pinned git directory reference")
			}
			// Rule 2b: only canonical raw.githubusercontent.com SHA-pinned files.
			if !isCanonicalRawGitHubFile(r.Raw) {
				return errors.New(
					"preflight: remote file reference is not an immutable raw.githubusercontent.com " +
						"URL pinned to a 40-character commit SHA; in strict mode a remote file must be " +
						"https://raw.githubusercontent.com/<owner>/<repo>/<40-hex-sha>/<path> " +
						"(any other file URL is fetched over plain HTTP and cannot be pinned) — " +
						"use a SHA-pinned git directory reference or vendor the file")
			}
		case refKindRemoteGitDir:
			// Rule 3: reject ordinary HTTPS git-directory references outside
			// components, regardless of a `//` or `.git` boundary.
			//
			// krusty v0.21.1 (kusttarget.go accumulateResources) tries
			// accumulateFile FIRST for resources/bases/path-valued
			// generators|transformers|validators. fileloader.IsRemoteFile
			// returns true for ANY http/https scheme (no host/extension/.git/`//`
			// check), so an https:// git-dir URL is HTTP-GET'd before git is ever
			// consulted — a server can return arbitrary unpinned YAML and the
			// ?ref=<sha> pins nothing. The `//` / `.git` markers only split
			// repo/subpath AFTER the git route is already chosen; they do NOT
			// select the git route.
			//
			// components is exempt: upstream dispatches it through Loader.New
			// directly (skipping the file attempt), so an https:// git-dir ref
			// there reaches git and a full-SHA ?ref= is honored.
			//
			// Accepted git-dir forms in resource-like fields are therefore only
			// the scheme-less github.com/<owner>/<repo>[//<path>]?ref=<sha>
			// shorthand (classifyKustomizeReference routes it here without the
			// https HTTP-file branch) and the legacy git::https://… prefix
			// (both make isHTTPURL(r.Raw) false). Those flow through to the
			// SHA-pin loop below.
			if r.Field != fieldComponents && isHTTPURL(r.Raw) {
				return errors.New(
					"preflight: HTTPS git-directory reference in a resources/bases field; " +
						"in strict mode krusty fetches any https:// reference over plain HTTP before " +
						"consulting git (so ?ref= pins nothing and a server may return arbitrary " +
						"content) — use the scheme-less github.com/<owner>/<repo>//<path>?ref=<40-hex-sha> " +
						"shorthand (which reaches git), a raw.githubusercontent.com/<owner>/<repo>/<40-hex-sha>/<file> " +
						"URL for a single file, or place a git directory base under components")
			}
		case refKindLocal:
			// Unreachable: local refs are not collected into remotes.
		}
	}
	return nil
}

// isCanonicalRawGitHubFile reports whether raw is a canonical, SHA-pinned
// raw.githubusercontent.com file URL of the exact form
// https://raw.githubusercontent.com/<owner>/<repo>/<40-hex-sha>/<path...>.
//
// It fails closed on anything non-canonical: a non-HTTPS scheme, a wrong host,
// a non-default port, userinfo, a query/fragment, a path that does not have the
// owner/repo/<sha>/file shape, or a ref segment that is not a full 40-hex SHA.
// raw.githubusercontent.com serves a fixed blob at a commit SHA, so a pinned
// URL here is immutable even though the fetch is plain HTTP.
func isCanonicalRawGitHubFile(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	// Host must be exactly raw.githubusercontent.com with no explicit port.
	if !strings.EqualFold(u.Host, "raw.githubusercontent.com") {
		return false
	}
	// Path layout: /<owner>/<repo>/<ref>/<file...> → at least 4 non-empty
	// segments. Reject path-normalization tricks (., ..) and empty segments.
	segs := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(segs) < 4 {
		return false
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return false
		}
	}
	// The 3rd segment (index 2) is the ref; it must be a full 40-hex SHA.
	return isSHAPinned(segs[2])
}
