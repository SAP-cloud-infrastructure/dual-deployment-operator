// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type fakeInner struct {
	calls int
	out   []manifest.Manifest
	err   error
}

func (f *fakeInner) Render(_ context.Context, _ Mode, _ string) ([]manifest.Manifest, error) {
	f.calls++
	return f.out, f.err
}

type fakeResolver struct {
	id    string
	scope string
	err   error
}

func (f *fakeResolver) resolve(_ context.Context, _ Mode) (id, scope string, err error) {
	return f.id, f.scope, f.err
}

func mustCache(t *testing.T, size int) *renderCache {
	t.Helper()
	c, err := newRenderCache(size)
	if err != nil {
		t.Fatalf("newRenderCache(%d): %v", size, err)
	}
	return c
}

func TestCachingSource_HitSkipsInner(t *testing.T) {
	cache := mustCache(t, 8)
	inner := &fakeInner{out: []manifest.Manifest{{}}}
	cs := &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "helm",
		resolve:    (&fakeResolver{id: "sha256:aaa", scope: "oci:h"}).resolve,
		inputHash:  "h1",
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 1 {
		t.Fatalf("expected inner called once (2nd is a hit), got %d", inner.calls)
	}
}

func TestCachingSource_ResolveErrorFallsThroughUncached(t *testing.T) {
	cache := mustCache(t, 8)
	inner := &fakeInner{out: []manifest.Manifest{{}}}
	cs := &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "helm",
		resolve:    (&fakeResolver{err: errors.New("boom")}).resolve,
		inputHash:  "h1",
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatalf("resolve failure must fall through, not error: %v", err)
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 2 {
		t.Fatalf("resolve error must not cache; expected 2 inner calls, got %d", inner.calls)
	}
}

func TestCachingSource_UnkeyableSkipsCache(t *testing.T) {
	cache := mustCache(t, 8)
	inner := &fakeInner{out: []manifest.Manifest{{}}}
	cs := &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "helm",
		resolve:    (&fakeResolver{err: errUnkeyable}).resolve,
		inputHash:  "h1",
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatalf("unexpected error on first render: %v", err)
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatalf("unexpected error on second render: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("unkeyable must skip cache; expected 2 inner calls, got %d", inner.calls)
	}
}

func TestCachingSource_RenderErrorNotCached(t *testing.T) {
	cache := mustCache(t, 8)
	inner := &fakeInner{err: errors.New("render fail")}
	cs := &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "helm",
		resolve:    (&fakeResolver{id: "sha256:aaa", scope: "oci:h"}).resolve,
		inputHash:  "h1",
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err == nil {
		t.Fatal("expected error propagated")
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err == nil {
		t.Fatal("expected error again (not cached)")
	}
	if inner.calls != 2 {
		t.Fatalf("render error must not cache; expected 2 inner calls, got %d", inner.calls)
	}
}

func TestWrapKustomize_InputHashCoversRootSubPath(t *testing.T) {
	specA := &v1alpha1.KustomizeSource{
		URL:       "https://github.com/org/repo//path/A?ref=v1",
		SeedPath:  "seed",
		ShootPath: "shoot",
	}
	specB := &v1alpha1.KustomizeSource{
		URL:       "https://github.com/org/repo//path/B?ref=v1", // only // root differs
		SeedPath:  "seed",
		ShootPath: "shoot",
	}
	cache := mustCache(t, 4)
	resolver := &gitResolver{}
	a := wrapKustomize(&kustomizeSource{spec: specA, resolver: resolver}, cache).(*kustomizeSource)
	b := wrapKustomize(&kustomizeSource{spec: specB, resolver: resolver}, cache).(*kustomizeSource)
	keyA, err := kustomizeCacheKey(a.spec, "same-immutable-root", nil, ModeSeed, "ns")
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := kustomizeCacheKey(b.spec, "same-immutable-root", nil, ModeSeed, "ns")
	if err != nil {
		t.Fatal(err)
	}
	if keyA == keyB {
		t.Fatal("different //root subpath must change inputHash (otherwise CRs pointing to different sub-roots would share a cache key)")
	}
}

// helmSourceWithValues builds a v1alpha1.HelmSource with optional seed/shoot values encoded as JSON.
func helmSourceWithValues(t *testing.T, seed, shoot map[string]any) *v1alpha1.HelmSource {
	t.Helper()
	src := &v1alpha1.HelmSource{Repo: "oci://example/charts", Name: "demo", Version: "1.0.0"}
	if seed != nil {
		b, err := json.Marshal(seed)
		if err != nil {
			t.Fatal(err)
		}
		src.SeedValues = &apiextensionsv1.JSON{Raw: b}
	}
	if shoot != nil {
		b, err := json.Marshal(shoot)
		if err != nil {
			t.Fatal(err)
		}
		src.ShootValues = &apiextensionsv1.JSON{Raw: b}
	}
	return src
}

func wrapHelmInputHash(t *testing.T, spec *v1alpha1.HelmSource) string {
	t.Helper()
	cache := mustCache(t, 4)
	loader := newHelmLoader(nil)
	inner := &helmSource{spec: spec, loader: loader}
	s := wrapHelm(inner, loader, spec, cache)
	cs, ok := s.(*cachingSource)
	if !ok {
		t.Fatalf("wrapHelm returned %T, want *cachingSource", s)
	}
	return cs.inputHash
}

func TestWrapHelm_InputHashCoversSeedValues(t *testing.T) {
	a := helmSourceWithValues(t, map[string]any{"replicas": 1}, nil)
	b := helmSourceWithValues(t, map[string]any{"replicas": 2}, nil)
	if wrapHelmInputHash(t, a) == wrapHelmInputHash(t, b) {
		t.Fatal("changing SeedValues must change inputHash (otherwise stale renders are served)")
	}
}

func TestWrapHelm_InputHashCoversShootValues(t *testing.T) {
	a := helmSourceWithValues(t, nil, map[string]any{"image": "v1"})
	b := helmSourceWithValues(t, nil, map[string]any{"image": "v2"})
	if wrapHelmInputHash(t, a) == wrapHelmInputHash(t, b) {
		t.Fatal("changing ShootValues must change inputHash (otherwise stale renders are served)")
	}
}

func TestCachingSource_UnkeyableWrappedSkipsCache(t *testing.T) {
	cache := mustCache(t, 8)
	inner := &fakeInner{out: []manifest.Manifest{{}}}
	wrapped := fmt.Errorf("wrap: %w", errUnkeyable)
	cs := &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "helm",
		resolve:    (&fakeResolver{err: wrapped}).resolve,
		inputHash:  "h1",
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatalf("unexpected error on first render: %v", err)
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatalf("unexpected error on second render: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("wrapped errUnkeyable must be treated same as bare (via errors.Is); expected 2 inner calls, got %d", inner.calls)
	}
}

func TestWrapHelm_ResolveClosureRepoScopeError(t *testing.T) {
	// Bad Repo scheme -> helmLoader.repoScope errors, resolve closure returns that
	// error, cachingSource.Render falls through to inner (per pure-optimization semantics).
	cache := mustCache(t, 4)
	loader := newHelmLoader(nil)
	inner := &fakeInner{out: []manifest.Manifest{{}}}
	spec := &v1alpha1.HelmSource{Repo: "gopher://bad-scheme", Name: "demo", Version: "1.0.0"}
	cs, ok := wrapHelm(inner, loader, spec, cache).(*cachingSource)
	if !ok {
		t.Fatal("wrapHelm should return *cachingSource for real *helmLoader + non-nil cache")
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatalf("repoScope error must fall through (not fail): %v", err)
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 2 {
		t.Fatalf("repoScope error must skip cache; expected 2 inner calls, got %d", inner.calls)
	}
}

func TestWrapHelm_ResolveClosureResolveIDError(t *testing.T) {
	// URL-embedded credentials -> repoScope succeeds but ResolveID's
	// rejectURLCredentials guard fires -> closure returns that error -> cachingSource
	// falls through to inner. Covers the ResolveID-error branch of wrapHelm's closure.
	cache := mustCache(t, 4)
	loader := newHelmLoader(nil)
	inner := &fakeInner{out: []manifest.Manifest{{}}}
	// Build the URL at runtime so gosec doesn't flag a literal password.
	user, pass := "user", "hunter2"
	repoURL := fmt.Sprintf("oci://%s:%s@example.com/charts", user, pass)
	spec := &v1alpha1.HelmSource{Repo: repoURL, Name: "demo", Version: "1.0.0"}
	cs, ok := wrapHelm(inner, loader, spec, cache).(*cachingSource)
	if !ok {
		t.Fatal("wrapHelm should return *cachingSource")
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatalf("ResolveID error must fall through: %v", err)
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatalf("second render: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("ResolveID error must skip cache; expected 2 inner calls, got %d", inner.calls)
	}
}

func TestWrapKustomize_InvalidRootFailsClosed(t *testing.T) {
	cache := mustCache(t, 4)
	resolver := &gitResolver{}
	spec := &v1alpha1.KustomizeSource{URL: "https://example.com/repo", SeedPath: "seed", ShootPath: "shoot"}
	inner := &kustomizeSource{spec: spec, resolver: resolver}
	cs, ok := wrapKustomize(inner, cache).(*kustomizeSource)
	if !ok {
		t.Fatal("wrapKustomize must retain the preparation/render lifecycle")
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err == nil {
		t.Fatal("invalid root must fail closed")
	}
	if cache.lru.Len() != 0 {
		t.Fatal("invalid root cached")
	}
}

func TestWrapKustomize_ResolutionErrorFailsClosed(t *testing.T) {
	cache := mustCache(t, 4)
	resolver := &gitResolver{}
	spec := &v1alpha1.KustomizeSource{
		URL:       "https://127.0.0.1:1/nope.git?ref=v1", // unreachable port; ls-remote fails
		SeedPath:  "seed",
		ShootPath: "shoot",
	}
	inner := &kustomizeSource{spec: spec, resolver: resolver}
	cs, ok := wrapKustomize(inner, cache).(*kustomizeSource)
	if !ok {
		t.Fatal("wrapKustomize must retain the preparation/render lifecycle")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := cs.Render(ctx, ModeSeed, "ns"); err == nil {
		t.Fatal("resolution failure must not fall through to a second mutable-root fetch")
	}
	if cache.lru.Len() != 0 {
		t.Fatal("failed resolution cached")
	}
}

func TestWrapKustomize_KeyDimensions(t *testing.T) {
	spec := v1alpha1.KustomizeSource{URL: "https://example.com/root.git//overlay?ref=v1", SeedPath: "seed", ShootPath: "shoot"}
	bases := []preparedBaseIdentity{
		{Reference: gitBaseReference{Repository: "https://example.com/a.git", Ref: "moving", SubPath: "selected"}, Identity: "repo-a-auth-sha1"},
		{Reference: gitBaseReference{Repository: "https://example.com/b.git", Ref: "moving", SubPath: "."}, Identity: "repo-b-auth-sha2"},
	}
	want, err := kustomizeCacheKey(&spec, "root-repo-auth-sha", bases, ModeSeed, "ns")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"url", "seed", "shoot", "root-identity", "order", "association", "subdir", "mode", "namespace"} {
		t.Run(change, func(t *testing.T) {
			other, root, mode, ns := spec, "root-repo-auth-sha", ModeSeed, "ns"
			refs := append([]preparedBaseIdentity(nil), bases...)
			switch change {
			case "url":
				other.URL += "0"
			case "seed":
				other.SeedPath = "different"
			case "shoot":
				other.ShootPath = "different"
			case "root-identity":
				root = "other-root-auth-sha"
			case "order":
				refs[0], refs[1] = refs[1], refs[0]
			case "association":
				refs[0].Identity, refs[1].Identity = refs[1].Identity, refs[0].Identity
			case "subdir":
				refs[0].Reference.SubPath = "different"
			case "mode":
				mode = ModeShoot
			case "namespace":
				ns = "other"
			}
			got, err := kustomizeCacheKey(&other, root, refs, mode, ns)
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatal("cache key omitted " + change)
			}
		})
	}
}

func TestWrapKustomize_ShorthandScope(t *testing.T) {
	key, err := kustomizeCacheKey(&v1alpha1.KustomizeSource{URL: "github.com/org/repo/subdir?ref=v1"}, "immutable-root", nil, ModeSeed, "ns")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Split(key, "|")[1] != "git:github.com" {
		t.Fatal("shorthand cache scope omitted normalized host")
	}
}
