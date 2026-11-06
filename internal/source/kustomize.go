// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// kustomizeFetchHook is a test-only hook called immediately before krusty.Run
// (the actual remote-fetch boundary). In production it is always nil. Tests that
// need to assert zero-fetch semantics install a non-nil closure here and verify
// it was never called after the function under test returns. Tests that verify
// the positive-control path (fetch IS expected) assert the hook was called.
// The hook is package-level so remotepolicy_test.go can install it without
// exported plumbing.
var kustomizeFetchHook func()

// krustyRunHook is a test-only hook that, when non-nil, makes runKrusty return
// this error instead of executing the real krust.Run. This lets tests inject
// synthetic krusty errors (including errors containing raw URLs and credentials)
// to verify that the sanitizing wrapper behaves correctly without requiring any
// network access. In production this is always nil.
var krustyRunHook error //nolint:errname // this is a hook variable, not a sentinel error; the linter incorrectly treats any var of type error as a sentinel

type kustomizeSource struct {
	spec                        *v1alpha1.KustomizeSource
	resolver                    RootResolver
	requireSHAPinnedRemoteBases bool
}

func (k *kustomizeSource) Render(ctx context.Context, mode Mode, namespace string) ([]manifest.Manifest, error) {
	subPath := k.spec.SeedPath
	if mode == ModeShoot {
		subPath = k.spec.ShootPath
	}

	root, cleanup, err := k.resolver.Resolve(ctx, k.spec.URL, subPath)
	if err != nil {
		return nil, fmt.Errorf("source: resolve kustomize root (mode=%s): %w", mode, err)
	}
	defer cleanup()

	// Compute the preflight isolation root (boundary for local path/symlink
	// checks) separately from the entry point (where the walk starts).
	//
	// The resolver returns filepath.Join(cloneDir, rootSubPath, subPath), where
	// rootSubPath is the in-repo root taken from the URL's `//root` separator
	// (splitRef) and subPath is the per-mode seed/shoot path. krusty receives
	// `root` (= cloneDir/rootSubPath/subPath) as its entry point. The correct
	// isolation boundary is cloneDir — NOT the overlay subdir and NOT
	// cloneDir/rootSubPath — because a kustomize overlay legitimately references
	// sibling directories (../base, ../../common) that live anywhere within the
	// clone but outside the overlay subdir.
	//
	// We therefore strip the FULL filepath.Join(rootSubPath, subPath) suffix
	// from root to recover cloneDir. rootSubPath is parsed from k.spec.URL with
	// the same splitRef the resolver uses; if it cannot be parsed we fall back to
	// an empty rootSubPath (stripping subPath only), matching prior behavior.
	//
	// The walk STARTS at root (so the overlay's refs are actually inspected),
	// but local path boundary checks use isolationRoot (so ../base is allowed
	// as long as it stays inside the clone).
	var rootSubPath string
	if _, sub, _, splitErr := splitRef(k.spec.URL); splitErr == nil {
		rootSubPath = sub
	}
	isolationRoot := preflightIsolationRoot(root, filepath.Join(rootSubPath, subPath))

	// SHA-pin preflight: classify → tree-walk → SHA-gate, strictly before any
	// network/git fetch. Errors here are already sanitized (no raw URLs/creds).
	// Pass isolationRoot as boundary and root as the walk entry point.
	if err := preflightSHAGate(isolationRoot, root, k.requireSHAPinnedRemoteBases); err != nil {
		return nil, err
	}

	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = types.LoadRestrictionsNone
	kust := krusty.MakeKustomizer(opts)

	// kustomizeFetchHook fires at the actual fetch boundary (non-nil in tests
	// only). This is the lowest-level call site in this package that causes
	// krusty to fetch remote bases — placing the hook here lets tests assert
	// whether a remote fetch was attempted.
	if kustomizeFetchHook != nil {
		kustomizeFetchHook()
	}

	resMap, err := runKrusty(kust, root)
	if err != nil {
		// sanitizeKrustyError maps krusty's raw error (which can contain
		// repository URLs, credentials, or git command output) to a
		// credential-redacted message. The error TEXT is preserved (host, repo,
		// path, exit code, git subcommand) with only credentials redacted; it is
		// forwarded as a flat %s (no %w), so the raw error is not Unwrap-able.
		return nil, fmt.Errorf("source: kustomize build (mode=%s): %w", mode, sanitizeKrustyError(err))
	}
	yamlBytes, err := resMap.AsYaml()
	if err != nil {
		return nil, fmt.Errorf("source: kustomize serialize: %w", err)
	}
	manifests, err := manifest.Parse(yamlBytes, manifest.OriginUpstream)
	if err != nil {
		return nil, fmt.Errorf("source: parse rendered manifests: %w", err)
	}
	manifest.ApplyNamespace(manifests, namespace)
	return manifests, nil
}

// runKrusty executes krust.Run against the given root on the real filesystem,
// returning the resource map or an error. In tests, krustyRunHook (when non-nil)
// short-circuits the real call and returns the synthetic error directly —
// this allows injection of raw-URL-bearing errors to verify sanitization.
func runKrusty(kust *krusty.Kustomizer, root string) (resmap.ResMap, error) {
	if krustyRunHook != nil {
		return nil, krustyRunHook
	}
	return kust.Run(filesys.MakeFsOnDisk(), root)
}

// preflightIsolationRoot computes the tree-walk isolation boundary by stripping
// a trailing path suffix from the resolved root.
//
// The RootResolver returns filepath.Join(cloneDir, rootSubPath, subPath). The
// isolation root for the preflight boundary check is cloneDir — not the overlay
// directory and not cloneDir/rootSubPath — because kustomize overlays
// legitimately reference sibling directories (../base, ../../common) that live
// anywhere within the clone. Render() therefore passes the FULL
// filepath.Join(rootSubPath, subPath) as suffix so the whole in-clone prefix is
// stripped back to cloneDir.
//
// This function is used by Render() to compute the `isolationRoot` argument for
// preflightSHAGate. The `entryPoint` passed to preflightSHAGate remains the
// original `root` so the walk actually inspects the overlay.
//
// If suffix is empty (or cleans to "."), root itself is the isolation root (no
// stripping needed). If root doesn't end with the expected suffix (e.g. a fake
// resolver or a different layout), we fall back to root itself so the preflight
// still runs correctly (entryPoint == isolationRoot in that case).
func preflightIsolationRoot(root, suffix string) string {
	if suffix == "" {
		return root
	}
	// filepath.Clean normalizes the suffix (removes trailing separators, "."
	// segments, etc.). An all-empty rootSubPath+subPath join cleans to ".".
	cleanSuffix := filepath.Clean(suffix)
	if cleanSuffix == "." {
		return root
	}
	cleanRoot := filepath.Clean(root)
	sep := string(filepath.Separator)
	wantSuffix := sep + cleanSuffix
	if strings.HasSuffix(cleanRoot, wantSuffix) {
		return cleanRoot[:len(cleanRoot)-len(wantSuffix)]
	}
	// Fallback: layout doesn't match — use root as-is.
	return root
}

// redactCredentials removes credential substrings from a diagnostic string
// while leaving the rest of the message intact. It targets two forms in which
// a secret can appear in a krusty / git / HTTP error:
//
//  1. URL userinfo: everything between "scheme://" and the "@" that terminates
//     the authority (before the first "/", "?" or "#"):
//     scheme://user:password@host/... → scheme://[REDACTED]@host/...
//     scheme://user:p@ss@host/...     → scheme://[REDACTED]@host/...
//     The match is bounded to the authority so a benign "@" that appears LATER
//     in the path or query (e.g. ?ref=pkg@1.2.3) is NOT swallowed.
//  2. Query parameters: the value of every query parameter is redacted UNLESS
//     its key is on a small allowlist of known-safe kustomize remote-base
//     params (ref, version, timeout, submodules). This fail-closed allowlist
//     catches credential params that a denylist missed — X-Amz-Signature,
//     X-Amz-Credential, X-Goog-Signature, X-Goog-Credential, key, auth, token,
//     access_token, … — without having to enumerate them. The key is preserved;
//     only the value (after "=") is replaced with [REDACTED].
//
// Everything else (hostnames, repo paths, exit codes, git subcommands, HTTP
// status lines) is preserved so the operator keeps an actionable diagnostic.
// This is the OWASP-aligned "redact the secret, keep the context" approach
// rather than discarding the entire message.

// reURLUserinfo matches URL userinfo: everything between "scheme://" and the
// "@" that terminates the authority. The userinfo body reaches the LAST "@"
// before a query/fragment start, so it covers an embedded "@" or "/" in a
// password (user:p@ss@host, user:pa/ss@host) but stops at the first "?" or "#"
// — a "@" that appears later in the query (e.g. ?ref=pkg@1.2.3) is never
// consumed.
var reURLUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^\s?#]*@`)

// allowlistedQueryParams are the query-parameter keys whose values are known to
// be non-sensitive for kustomize remote bases; everything else is redacted.
var allowlistedQueryParams = map[string]bool{
	"ref":        true,
	"version":    true,
	"timeout":    true,
	"submodules": true,
}

// reQueryParam matches any single "?key=value" or "&key=value" pair. The value
// stops at the next query separator ("&"), fragment ("#"), whitespace, or a
// surrounding shell/quote delimiter ("'", `"`) so a trailing "': HTTP Error" in
// a wrapped error message is not eaten. Group 1 = separator, group 2 = key,
// group 3 = value.
var reQueryParam = regexp.MustCompile(`([?&])([A-Za-z0-9_.-]+)=([^&\s#'"]*)`)

func redactCredentials(s string) string {
	// 1. Redact URL userinfo (bounded to the authority).
	s = reURLUserinfo.ReplaceAllString(s, "${1}[REDACTED]@")
	// 2. Redact every query-parameter value whose key is not allowlisted.
	s = reQueryParam.ReplaceAllStringFunc(s, func(m string) string {
		sub := reQueryParam.FindStringSubmatch(m)
		sep, key := sub[1], sub[2]
		if allowlistedQueryParams[strings.ToLower(key)] {
			return m
		}
		return sep + key + "=[REDACTED]"
	})
	return s
}

// sanitizeKrustyError maps a raw error from krusty.Run to a sanitized render
// error that follows the REDACT-AND-PRESERVE policy (design decision: OWASP /
// Kubernetes norm — keep actionable diagnostics, strip only secrets).
//
// krusty errors can contain repository URLs, embedded credentials (if the
// caller mis-configured a URL), full git command strings, or HTTP status lines.
// Of these, only embedded credentials are sensitive. Rather than discard the
// whole message (which also hides purely-local overlay errors such as a bad
// patch target or invalid YAML, and would regress diagnostics vs. main), we
// forward the raw message with credential substrings redacted (redactCredentials)
// — hostnames, repo paths, exit codes and git subcommands are preserved.
//
// NOTE: an earlier reason-code prefix (krustyReason) was removed. It
// substring-matched the whole message and mislabelled purely-local errors
// (duplicate ClusterRole, patch-no-target, random temp-dir names) as
// "RemoteAuthenticationFailed" etc. Nothing outside tests read the label; the
// condition reason stays SeedRenderFailed/ShootRenderFailed. The label is gone.
//
// The returned error uses fmt.Errorf without %w so the ORIGINAL unredacted error
// is never reachable via errors.Unwrap / errors.As — a caller cannot accidentally
// re-log the pre-redaction text. The redacted text is still embedded in the
// returned error's message, which is intentional: it is safe by construction and
// is what the operator needs to debug.
//
// Returns nil iff err == nil (success path unaffected).
func sanitizeKrustyError(err error) error {
	if err == nil {
		return nil
	}
	redacted := redactCredentials(err.Error())
	return fmt.Errorf("%s", redacted) //nolint:err113 // dynamic sanitized message by design; no sentinel needed
}
