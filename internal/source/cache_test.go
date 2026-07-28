// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"
	"testing"

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

func TestCachingSource_HitSkipsInner(t *testing.T) {
	cache, _ := newRenderCache(8)
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
	cache, _ := newRenderCache(8)
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
	cache, _ := newRenderCache(8)
	inner := &fakeInner{out: []manifest.Manifest{{}}}
	cs := &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "helm",
		resolve:    (&fakeResolver{err: errUnkeyable}).resolve,
		inputHash:  "h1",
	}
	_, _ = cs.Render(context.Background(), ModeSeed, "ns")
	_, _ = cs.Render(context.Background(), ModeSeed, "ns")
	if inner.calls != 2 {
		t.Fatalf("unkeyable must skip cache; expected 2 inner calls, got %d", inner.calls)
	}
}

func TestCachingSource_RenderErrorNotCached(t *testing.T) {
	cache, _ := newRenderCache(8)
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
