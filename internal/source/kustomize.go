// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"fmt"

	"sigs.k8s.io/kustomize/api/krusty"
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

	// SHA-pin preflight: classify → tree-walk → SHA-gate, strictly before any
	// network/git fetch. Errors here are already sanitized (no raw URLs/creds).
	if err := preflightSHAGate(root, k.requireSHAPinnedRemoteBases); err != nil {
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
	resMap, err := kust.Run(filesys.MakeFsOnDisk(), root)
	if err != nil {
		return nil, fmt.Errorf("source: kustomize build (mode=%s): %w", mode, err)
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
