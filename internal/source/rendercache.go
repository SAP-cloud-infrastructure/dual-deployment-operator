// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// defaultRenderCacheSize bounds the in-memory render cache by entry count.
// Steady state is ~5 operators x 2 modes = 10 live keys; 32 leaves headroom.
const defaultRenderCacheSize = 32

// renderCache is a concurrency-safe, entry-count-bounded LRU of rendered
// manifests, keyed by the cache key string. No TTL: invalidation comes from the
// resolved-id keying (a changed id is a new key). It is a pure optimization.
//
// The cache DEEP-COPIES manifests at both boundaries. Consumers (notably the
// delivery applier) mutate manifests in place — StripInternalAnnotations,
// SetOwnedByLabel, prepareForApply — so returning cached pointers directly
// would let one reconcile's mutations bleed into the next, and let concurrent
// seed+shoot reconciles race on shared *unstructured.Unstructured. Deep-copying
// insulates callers from each other and preserves the cache's authoritative copy.
type renderCache struct {
	lru *lru.Cache[string, []manifest.Manifest]
}

func newRenderCache(size int) (*renderCache, error) {
	if size <= 0 {
		size = defaultRenderCacheSize
	}
	c, err := lru.New[string, []manifest.Manifest](size)
	if err != nil {
		return nil, err
	}
	return &renderCache{lru: c}, nil
}

func (c *renderCache) get(key string) ([]manifest.Manifest, bool) {
	m, ok := c.lru.Get(key)
	if !ok {
		return nil, false
	}
	return cloneManifests(m), true
}

func (c *renderCache) put(key string, m []manifest.Manifest) {
	_ = c.lru.Add(key, cloneManifests(m))
}

// cloneManifests returns a deep copy of the slice: a fresh slice header plus
// deep-copied *unstructured.Unstructured for each entry. Origin is a value type
// (string) so it copies by assignment.
func cloneManifests(in []manifest.Manifest) []manifest.Manifest {
	if in == nil {
		return nil
	}
	out := make([]manifest.Manifest, len(in))
	for i, m := range in {
		out[i] = manifest.Manifest{
			Unstructured: m.Unstructured.DeepCopy(),
			Origin:       m.Origin,
		}
	}
	return out
}
