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

func (c *renderCache) get(key string) ([]manifest.Manifest, bool) { return c.lru.Get(key) }

func (c *renderCache) put(key string, m []manifest.Manifest) { _ = c.lru.Add(key, m) }
