// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type kustomizeSource struct {
	spec     *v1alpha1.KustomizeSource
	resolver RootResolver
	cache    *renderCache // production Git identities only; injected resolvers are uncacheable
}

func (k *kustomizeSource) Render(ctx context.Context, mode Mode, namespace string) ([]manifest.Manifest, error) {
	subPath := k.spec.SeedPath
	if mode == ModeShoot {
		subPath = k.spec.ShootPath
	}

	root, scratch, acquire, err := k.acquireRoot(ctx, subPath)
	if root.Cleanup != nil {
		defer root.Cleanup()
	}
	if err != nil {
		return nil, fmt.Errorf("source: resolve kustomize root (mode=%s): %w", mode, err)
	}
	prepared, err := prepareKustomize(ctx, root, scratch, acquire)
	if err != nil {
		return nil, fmt.Errorf("source: prepare kustomize (mode=%s): %w", mode, err)
	}
	defer prepared.Cleanup()

	var key string
	if k.cache != nil {
		key, err = kustomizeCacheKey(k.spec, root.Identity, prepared.Identities, mode, namespace)
		if err != nil {
			return nil, fmt.Errorf("source: key prepared kustomize: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if m, ok := k.cache.get(key); ok {
			return m, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = types.LoadRestrictionsNone
	opts.PluginConfig = types.DisabledPluginConfig()
	kust := krusty.MakeKustomizer(opts)
	resMap, err := kust.Run(filesys.MakeFsOnDisk(), prepared.Root)
	if err != nil {
		return nil, fmt.Errorf("source: kustomize build (mode=%s): %w", mode, err)
	}
	// krusty has no context-aware Run; do not publish a result after cancellation.
	if err := ctx.Err(); err != nil {
		return nil, err
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
	if k.cache != nil {
		k.cache.put(key, manifests)
	}
	return manifests, nil
}

// acquireRoot retains the whole production checkout boundary. The public resolver
// interface exposes only a selected directory; injected resolvers therefore get
// uncached local preparation within that explicit boundary, never guessed parents
// or implicit production/network acquisition capability.
func (k *kustomizeSource) acquireRoot(ctx context.Context, modePath string) (acquiredBase, string, acquireBase, error) {
	r, production := k.resolver.(*gitResolver)
	if !production {
		root, cleanup, err := k.resolver.Resolve(ctx, k.spec.URL, modePath)
		return acquiredBase{RepositoryDir: root, SubPath: ".", Identity: "injected-root", Cleanup: cleanup}, "", nil, err
	}
	// Root CEL only requires ref and may admit additional query options. Runtime
	// deliberately enforces the same conservative grammar as bases: reject those
	// options rather than silently stripping timeout/submodules/etc via splitRef.
	ref, remote, err := classifyGitBase(k.spec.URL)
	if err != nil {
		return acquiredBase{}, "", nil, err
	}
	if !remote {
		return acquiredBase{}, "", nil, errors.New("production kustomize root requires an HTTPS Git reference")
	}
	if _, remote, err := classifyGitBase(modePath); remote || err != nil || path.IsAbs(modePath) || strings.ContainsAny(modePath, "\\\x00\r\n") {
		return acquiredBase{}, "", nil, errors.New("kustomize mode path must be repository-relative")
	}
	selected := path.Join(ref.SubPath, modePath)
	if selected == ".." || strings.HasPrefix(selected, "../") {
		return acquiredBase{}, "", nil, errors.New("kustomize mode path escapes repository")
	}
	acquire := func(ctx context.Context, ref gitBaseReference) (acquiredBase, error) {
		checkout, err := r.acquire(ctx, ref.Repository, ref.Ref)
		if err != nil {
			return acquiredBase{}, err
		}
		return acquiredBase{RepositoryDir: checkout.Root, SubPath: ref.SubPath, Identity: checkout.Identity, Cleanup: checkout.Cleanup}, nil
	}
	ref.SubPath = selected
	root, err := acquire(ctx, ref)
	return root, r.scratchDir, acquire, err
}
