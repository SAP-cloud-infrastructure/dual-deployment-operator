// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

// Package source renders a DualDeploymentOperator spec.source into origin-tagged
// manifest streams, once per mode (seed or shoot), per the two-render pattern.
package source

import (
	"context"
	"errors"

	"helm.sh/helm/v3/pkg/chart"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// Mode selects which render to produce; each render's output targets one cluster.
type Mode string

const (
	// ModeSeed renders the seed-cluster resources.
	ModeSeed Mode = "seed"
	// ModeShoot renders the shoot-cluster resources.
	ModeShoot Mode = "shoot"
)

// Source renders the manifest stream for a specific mode.
type Source interface {
	Render(ctx context.Context, mode Mode, namespace string) ([]manifest.Manifest, error)
}

// ChartLoader acquires a Helm chart. Production pulls from OCI/HTTP; tests fake it.
type ChartLoader interface {
	Load(ctx context.Context, repo, name, version string) (*chart.Chart, error)
}

// RootResolver resolves a kustomize root (base URL + mode subpath) to a local
// filesystem path plus a cleanup func. Production fetches the pinned remote URL;
// tests resolve to a local overlay directory.
type RootResolver interface {
	Resolve(ctx context.Context, url, subPath string) (fsPath string, cleanup func(), err error)
}

// errUnkeyable signals a source cannot be soundly keyed for caching (e.g. an
// HTTP Helm repo whose index.yaml has neither a digest nor a matched version).
// The cachingSource decorator treats it like a resolve failure: render fresh,
// cache nothing.
var errUnkeyable = errors.New("source: unkeyable (skip caching)")

// Deps holds the injectable fetchers a Source needs.
type Deps struct {
	ChartLoader        ChartLoader
	RootResolver       RootResolver
	CredentialResolver *CredentialResolver // optional; nil => anonymous
	// RenderCache, when non-nil, enables the render-result cache. When nil the
	// source is returned unwrapped (uncached behavior preserved, matching
	// pre-Phase-7.5 semantics).
	RenderCache *renderCache
}

// NewHelmLoader returns a production OCI+HTTP ChartLoader (no cache). The
// per-source credential resolver is injected via Deps.CredentialResolver.
func NewHelmLoader() ChartLoader { return newHelmLoader(nil) }

// NewGitResolver returns a production git RootResolver.
func NewGitResolver() RootResolver { return &gitResolver{} }

// withHelmCreds returns a ChartLoader that resolves credentials for this source's
// authSecretRef. If cl is a production *helmLoader and a resolver is configured, it
// returns a FRESH loader (copy) with the per-source resolver bound — never mutating
// the shared instance (which would race across concurrent reconciles). Fakes and the
// no-resolver case pass through unchanged.
func withHelmCreds(cl ChartLoader, cr *CredentialResolver, ref *v1alpha1.SecretReference) ChartLoader {
	hl, ok := cl.(*helmLoader)
	if !ok || cr == nil {
		return cl
	}
	cp := *hl // shallow copy of the value; settings is shared read-only, resolve is replaced
	cp.resolve = func(ctx context.Context, _ string) (creds, error) { return cr.Resolve(ctx, ref) }
	return &cp
}

func withGitCreds(rr RootResolver, cr *CredentialResolver, ref *v1alpha1.SecretReference) RootResolver {
	gr, ok := rr.(*gitResolver)
	if !ok || cr == nil {
		return rr
	}
	cp := *gr
	cp.resolve = func(ctx context.Context, _ string) (creds, error) { return cr.Resolve(ctx, ref) }
	return &cp
}

// From constructs a Source from a spec discriminator plus its dependencies.
// Exactly one of spec.Helm / spec.Kustomize must be set.
func From(spec v1alpha1.Source, deps Deps) (Source, error) {
	switch {
	case spec.Helm != nil && spec.Kustomize == nil:
		if deps.ChartLoader == nil {
			return nil, errors.New("source: helm source requires a ChartLoader (none configured)")
		}
		loader := withHelmCreds(deps.ChartLoader, deps.CredentialResolver, spec.Helm.AuthSecretRef)
		inner := &helmSource{spec: spec.Helm, loader: loader}
		return wrapHelm(inner, loader, spec.Helm, deps.RenderCache), nil
	case spec.Kustomize != nil && spec.Helm == nil:
		if deps.RootResolver == nil {
			return nil, errors.New("source: kustomize source requires a RootResolver (none configured)")
		}
		resolver := withGitCreds(deps.RootResolver, deps.CredentialResolver, spec.Kustomize.AuthSecretRef)
		inner := &kustomizeSource{spec: spec.Kustomize, resolver: resolver}
		return wrapKustomize(inner, resolver, spec.Kustomize, deps.RenderCache), nil
	default:
		return nil, errors.New("source: exactly one of source.helm or source.kustomize must be set")
	}
}

// NewRenderCache returns a shared render cache with the default capacity, or nil
// (uncached) if construction fails — the operator MUST still run without a cache.
// Passed into source.Deps.RenderCache to enable render-result caching.
func NewRenderCache() *renderCache {
	c, err := newRenderCache(defaultRenderCacheSize)
	if err != nil {
		return nil
	}
	return c
}
