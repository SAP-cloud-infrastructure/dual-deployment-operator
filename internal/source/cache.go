// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"

	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// resolveFunc resolves a source (for a mode) to its immutable id + repoScope.
// It returns errUnkeyable to signal "skip caching, render fresh".
type resolveFunc func(ctx context.Context, mode Mode) (id, scope string, err error)

// cachingSource decorates an inner Source with a render-result cache, keyed by
// the resolved immutable content id (resolve-then-key). The cache is a pure
// optimization: any resolve failure falls through to a direct inner render.
type cachingSource struct {
	inner      Source
	cache      *renderCache
	sourceKind string      // "helm" | "kustomize"
	resolve    resolveFunc // closes over the loader's ResolveID + repoScope + url
	inputHash  string      // precomputed static portion (values / paths); mode+namespace enter the key separately
}

func (c *cachingSource) Render(ctx context.Context, mode Mode, namespace string) ([]manifest.Manifest, error) {
	l := log.FromContext(ctx)

	id, scope, err := c.resolve(ctx, mode)
	switch {
	case errors.Is(err, errUnkeyable):
		l.V(1).Info("Source is unkeyable; rendering without cache", "kind", c.sourceKind)
		return c.inner.Render(ctx, mode, namespace)
	case err != nil:
		l.V(1).Info("Resolve failed; rendering without cache", "kind", c.sourceKind, "err", err.Error())
		return c.inner.Render(ctx, mode, namespace)
	}

	l.Info("Resolved source", "kind", c.sourceKind, "id", id) // E1 audit line

	key := keyParts{
		sourceKind: c.sourceKind,
		repoScope:  scope,
		resolvedID: id,
		mode:       string(mode),
		inputHash:  c.inputHash,
		namespace:  namespace,
	}.String()

	if m, ok := c.cache.get(key); ok {
		l.V(1).Info("Render cache hit", "kind", c.sourceKind, "mode", string(mode))
		return m, nil
	}
	m, err := c.inner.Render(ctx, mode, namespace)
	if err != nil {
		return nil, err // never cache failures
	}
	c.cache.put(key, m)
	l.V(1).Info("Render cache miss stored", "kind", c.sourceKind, "mode", string(mode))
	return m, nil
}

// wrapHelm returns a cachingSource wrapping a helm inner Source, or the inner
// unwrapped when cache is nil or the loader isn't the production *helmLoader
// (fakes/tests get the uncached path automatically).
func wrapHelm(inner Source, loader ChartLoader, spec *v1alpha1.HelmSource, cache *renderCache) Source {
	hl, ok := loader.(*helmLoader)
	if !ok || cache == nil {
		return inner
	}
	// inputHash covers static (name, version) + BOTH mode-specific merged values.
	// mode is a key dimension separately, but its 4-byte value ("seed"/"shoot")
	// cannot carry actual values content, so mode-specific values MUST also enter
	// inputHash — otherwise a CR that changes seedValues (all other dimensions
	// stable) would key identically and serve a stale render. Folding both modes
	// in at wrap time is intentional: a change to shootValues bumps the seed key
	// too (one wasted re-render on next seed reconcile), a harmless correctness
	// margin over per-mode hashing.
	base := map[string]any{"name": spec.Name, "version": spec.Version}
	if seed, err := mergeValues(spec.Values, spec.SeedValues); err == nil {
		base["seedValues"] = seed
	}
	if shoot, err := mergeValues(spec.Values, spec.ShootValues); err == nil {
		base["shootValues"] = shoot
	}
	return &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "helm",
		inputHash:  hashValues(base),
		resolve: func(ctx context.Context, _ Mode) (string, string, error) {
			scope, err := hl.repoScope(spec.Repo)
			if err != nil {
				return "", "", err
			}
			id, err := hl.ResolveID(ctx, spec.Repo, spec.Name, spec.Version)
			if err != nil {
				return "", "", err
			}
			return id, scope, nil
		},
	}
}

// wrapKustomize returns a cachingSource wrapping a kustomize inner Source, or
// the inner unwrapped when cache is nil or the resolver isn't the production
// *gitResolver.
func wrapKustomize(inner Source, resolver RootResolver, spec *v1alpha1.KustomizeSource, cache *renderCache) Source {
	gr, ok := resolver.(*gitResolver)
	if !ok || cache == nil {
		return inner
	}
	return &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "kustomize",
		inputHash:  hashValues(map[string]any{"seedPath": spec.SeedPath, "shootPath": spec.ShootPath}),
		resolve: func(ctx context.Context, _ Mode) (string, string, error) {
			scope, err := gr.repoScope(spec.URL)
			if err != nil {
				return "", "", err
			}
			id, err := gr.ResolveID(ctx, spec.URL, ModeSeed) // mode irrelevant for git id
			if err != nil {
				return "", "", err
			}
			return id, scope, nil
		},
	}
}
