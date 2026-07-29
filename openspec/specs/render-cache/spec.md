<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec: Render Cache

## Purpose

Defines the in-memory rendered-manifest cache in `internal/source` (Phase 7.5). The cache sits as a `cachingSource` decorator around `Source` at the single point where the Helm and kustomize paths converge on a `[]manifest.Manifest`, so both loaders are cached symmetrically. It is a pure optimization: any resolve or render failure falls through to the existing uncached path, and failures are never cached.

## Requirements

### Requirement: Cache rendered manifests at the Source boundary

The system MUST cache the rendered `[]manifest.Manifest` output of a `Source.Render` call in an in-memory cache, so that a subsequent render for the same resolved source content, mode, inputs, and namespace returns the cached manifests without re-fetching or re-rendering. The cache MUST be implemented as a decorator (`cachingSource`) wrapping the inner `Source` returned by `source.From`, so that Helm and kustomize are cached symmetrically at the single point where both converge on a manifest slice. When no cache is configured, `source.From` MUST return the inner source unwrapped, preserving the uncached behavior.

#### Scenario: second identical render is served from cache

- **WHEN** `Render` is invoked twice for the same source with the same resolved id, mode, inputs, and namespace
- **THEN** the first call is a cache miss and delegates to the inner `Source.Render`
- **AND** the second call is a cache hit and returns the cached `[]manifest.Manifest` without calling the inner `Source.Render` (skipping Helm pull+template / kustomize clone+krusty+transitive fetches)

#### Scenario: no cache configured preserves uncached behavior

- **WHEN** `source.From` is called with no render cache in its dependencies
- **THEN** it returns the inner `helmSource`/`kustomizeSource` directly (not wrapped in `cachingSource`)
- **AND** every `Render` fetches and renders fresh

---

### Requirement: Cache key uniquely identifies a render

The cache key MUST be composed of `sourceKind | repoScope | resolvedID | mode | inputHash | namespace` so that any change to a dimension that affects the rendered output produces a distinct key. `repoScope` MUST be the transport-and-host scope (`oci:<host>`, `http:<host>`, or `git:<host>`) so that an OCI Helm source and an HTTP Helm source can never share a cache entry, and same-named artifacts on different hosts stay distinct. `inputHash` MUST be a hash over the mode-specific render inputs not already captured by `resolvedID` — for Helm the canonicalized (sorted-key) values plus chart name and version, for kustomize the mode subpath (and root subpath). Transformations MUST NOT be part of the key, because they run after `Source.Render` on the cached-or-fresh output.

#### Scenario: OCI and HTTP Helm sources do not collide

- **WHEN** an OCI Helm source and an HTTP Helm source resolve to the same chart name, version, and id string
- **THEN** their cache keys differ because `repoScope` is `oci:<host>` for one and `http:<host>` for the other
- **AND** each is cached under its own entry

#### Scenario: differing values produce distinct keys

- **WHEN** two renders use the same resolved chart id but different mode values
- **THEN** their `inputHash` differs
- **AND** they occupy distinct cache entries

#### Scenario: values map ordering does not affect the key

- **WHEN** the same values are supplied with different map key ordering
- **THEN** the canonicalized `inputHash` is identical
- **AND** both renders map to the same cache entry

#### Scenario: mode and namespace are part of the key

- **WHEN** the same source is rendered for `seed` versus `shoot`, or into different namespaces
- **THEN** each combination produces a distinct cache key and its own entry

---

### Requirement: Bounded in-memory LRU with no TTL

The render cache MUST be an in-memory LRU bounded by entry count with a package-default capacity, and MUST evict the least-recently-used entry when a new entry is inserted beyond capacity. The cache MUST NOT use a TTL; invalidation is achieved solely by the resolved-id keying (a changed resolved id yields a new key and the stale entry ages out via LRU). The cache MUST be safe for concurrent `Get`/`Put` from concurrent reconciles.

#### Scenario: eviction past capacity

- **WHEN** entries are inserted beyond the configured capacity
- **THEN** the least-recently-used entry is evicted
- **AND** the total number of retained entries does not exceed the capacity

#### Scenario: concurrent access is race-free

- **WHEN** multiple goroutines call `Get` and `Put` concurrently
- **THEN** the cache operates without data races (verified under the race detector)

---

### Requirement: Cache is a pure optimization

The cache MUST NOT be a source of truth: any failure in the resolve or render path MUST fall through to correct uncached behavior rather than failing the reconcile or serving wrong data. When resolving the id fails, the system MUST render directly via the inner `Source` (the uncached path) and MUST NOT cache the result for that call. When the inner `Source.Render` returns an error, the system MUST propagate the error and MUST NOT cache a failure. Only a successful, non-nil render MUST be stored.

#### Scenario: resolve failure falls through to uncached render

- **WHEN** the resolve step returns an error
- **THEN** the system calls the inner `Source.Render` directly and returns its result
- **AND** nothing is stored in the cache for that call

#### Scenario: render failure is not cached

- **WHEN** the inner `Source.Render` returns an error
- **THEN** the error is propagated to the caller
- **AND** no cache entry is created for that key
