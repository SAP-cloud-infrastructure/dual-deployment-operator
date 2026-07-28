// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"sync"
	"testing"

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
	c, _ := newRenderCache(2)
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
	c, _ := newRenderCache(8)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.put("k", []manifest.Manifest{{}})
			_, _ = c.get("k")
		}()
	}
	wg.Wait()
}
