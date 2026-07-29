// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func TestRenderCache_GetPut(t *testing.T) {
	c, err := newRenderCache(2)
	if err != nil {
		t.Fatalf("newRenderCache: %v", err)
	}
	if _, ok := c.get("k1"); ok {
		t.Fatal("expected miss on empty cache")
	}
	want := []manifest.Manifest{{}}
	c.put("k1", want)
	got, ok := c.get("k1")
	if !ok || len(got) != len(want) {
		t.Fatalf("expected hit with %d manifests, got ok=%v len=%d", len(want), ok, len(got))
	}
}

func TestRenderCache_EvictsLRU(t *testing.T) {
	c, err := newRenderCache(2)
	if err != nil {
		t.Fatalf("newRenderCache: %v", err)
	}
	c.put("k1", []manifest.Manifest{{}})
	c.put("k2", []manifest.Manifest{{}})
	_, _ = c.get("k1")                   // touch k1 so k2 is LRU
	c.put("k3", []manifest.Manifest{{}}) // evicts k2
	if _, ok := c.get("k2"); ok {
		t.Fatal("expected k2 to be evicted (LRU)")
	}
	if _, ok := c.get("k1"); !ok {
		t.Fatal("expected k1 to survive")
	}
	if _, ok := c.get("k3"); !ok {
		t.Fatal("expected k3 present")
	}
}

func TestRenderCache_ConcurrentRaceFree(t *testing.T) {
	c, err := newRenderCache(8)
	if err != nil {
		t.Fatalf("newRenderCache: %v", err)
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			c.put("k", []manifest.Manifest{{}})
			_, _ = c.get("k")
		})
	}
	wg.Wait()
}

func TestRenderCache_ReturnsIndependentCopy(t *testing.T) {
	c, err := newRenderCache(4)
	if err != nil {
		t.Fatalf("newRenderCache: %v", err)
	}
	m := manifest.Manifest{
		Unstructured: &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name": "cm1",
				"annotations": map[string]any{
					manifest.OriginAnnotation: "upstream",
				},
			},
		}},
		Origin: manifest.OriginUpstream,
	}
	c.put("k", []manifest.Manifest{m})

	got1, ok := c.get("k")
	if !ok {
		t.Fatal("expected hit")
	}
	got1[0].StripInternalAnnotations() // real mutation used in delivery

	got2, ok := c.get("k")
	if !ok {
		t.Fatal("expected second hit")
	}
	anns := got2[0].Unstructured.GetAnnotations()
	if _, present := anns[manifest.OriginAnnotation]; !present {
		t.Fatalf("cached manifest was mutated across gets: annotations=%v", anns)
	}

	m.Unstructured.SetName("changed-after-put")
	got3, _ := c.get("k")
	if got3[0].Unstructured.GetName() != "cm1" {
		t.Fatalf("cache aliased the caller's pointer: got name %q", got3[0].Unstructured.GetName())
	}
}

func TestNewRenderCache_DefaultSize(t *testing.T) {
	// size <= 0 must default to defaultRenderCacheSize, not error.
	if c, err := newRenderCache(0); err != nil || c == nil {
		t.Fatalf("newRenderCache(0): expected default-sized cache, got c=%v err=%v", c, err)
	}
	if c, err := newRenderCache(-1); err != nil || c == nil {
		t.Fatalf("newRenderCache(-1): expected default-sized cache, got c=%v err=%v", c, err)
	}
}

func TestCloneManifests_Nil(t *testing.T) {
	// The nil-input branch of cloneManifests must return nil, not panic.
	if got := cloneManifests(nil); got != nil {
		t.Fatalf("cloneManifests(nil) = %v, want nil", got)
	}
}
