// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
var krustyRunHook error

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

	// Compute the preflight isolation root: the clone root from which krusty
	// will read. The resolver returns filepath.Join(cloneDir, subPath) so we
	// strip the subPath suffix to recover cloneDir. This allows kustomize
	// overlays that reference sibling directories (../base) to pass the
	// boundary check — those references stay within the clone root, which is
	// the correct isolation boundary.
	// When subPath is empty, preflightRoot == root (no stripping needed).
	preflightRoot := preflightIsolationRoot(root, subPath)

	// SHA-pin preflight: classify → tree-walk → SHA-gate, strictly before any
	// network/git fetch. Errors here are already sanitized (no raw URLs/creds).
	if err := preflightSHAGate(preflightRoot, k.requireSHAPinnedRemoteBases); err != nil {
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
		// repository URLs, credentials, or git command output) to a stable
		// sanitized message. The raw error is dropped — never forwarded.
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

// preflightIsolationRoot computes the tree-walk isolation boundary from the
// resolved root and the subPath that was appended to reach it.
//
// The RootResolver returns filepath.Join(cloneDir, subPath) for a given subPath.
// When walking for classification/preflight, we want to use cloneDir as the
// boundary — not the overlay subPath — because kustomize overlays legitimately
// reference sibling directories (../base) that are within the clone but outside
// the overlay subPath directory.
//
// If subPath is empty, root itself is the isolation root (no stripping needed).
// If the root doesn't end with the expected subPath suffix (e.g. because the
// resolver is a fake or uses a different layout), we fall back to root itself
// so the preflight still runs safely (potentially rejecting valid ../refs, but
// that only affects misconfigured test resolvers, not production).
func preflightIsolationRoot(root, subPath string) string {
	if subPath == "" {
		return root
	}
	// filepath.Clean normalizes the subPath (removes trailing separators, etc.)
	cleanSub := filepath.Clean(subPath)
	cleanRoot := filepath.Clean(root)
	// Check if root ends with the subPath segment.
	sep := string(filepath.Separator)
	suffix := sep + cleanSub
	if strings.HasSuffix(cleanRoot, suffix) {
		return cleanRoot[:len(cleanRoot)-len(suffix)]
	}
	// Fallback: subPath layout doesn't match — use root as-is.
	return root
}

// sanitizeKrustyError maps a raw error from krusty.Run to a stable, sanitized
// render error. krusty errors can contain repository URLs, embedded credentials
// (if the caller mis-configured an authSecretRef), full git command strings, or
// HTTP status lines — none of which should be surfaced to the operator's
// reconciliation log or status conditions.
//
// Design: we apply uniform sanitization — never forward the raw error message,
// always substitute a fixed descriptive string. This is strictly safer than
// trying to classify and partially sanitize: any partial approach risks missing
// a new krusty error format that leaks a URL.
//
// The returned error:
//   - Is nil iff err == nil (success path is completely unaffected).
//   - Uses errors.New (not fmt.Errorf with %w) so the raw error is never
//     accessible via errors.Unwrap or errors.As — preventing callers from
//     accidentally logging the sanitized-away detail.
func sanitizeKrustyError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New("remote fetch or build failed: krusty encountered an error building the kustomization (details omitted to prevent credential/URL leakage; check git access and remote base availability)")
}
