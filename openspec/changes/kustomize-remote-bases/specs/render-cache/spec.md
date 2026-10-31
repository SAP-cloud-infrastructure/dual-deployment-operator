<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec Delta: render-cache

## MODIFIED Requirements

### Requirement: Cache key uniquely identifies a render

The cache key MUST be composed of `sourceKind | repoScope | resolvedID | mode | inputHash | namespace` so that any change to a dimension that affects the rendered output produces a distinct key. `repoScope` MUST be the transport-and-host scope (`oci:<host>`, `http:<host>`, or `git:<host>`) so that an OCI Helm source and an HTTP Helm source can never share a cache entry, and same-named artifacts on different hosts stay distinct. `inputHash` MUST be a hash over the mode-specific render inputs not already captured by `resolvedID` — for Helm the canonicalized (sorted-key) values plus chart name and version, for kustomize the mode subpath (and root subpath) **plus the resolved commit SHA of every transitively referenced remote base**, so that a moved remote-base tag that resolves to a different SHA — even when the root `resolvedID` is unchanged — produces a distinct cache key and is not served stale. Transformations MUST NOT be part of the key, because they run after `Source.Render` on the cached-or-fresh output.

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

#### Scenario: a moved remote-base SHA invalidates the render cache key

- **WHEN** the kustomize root `resolvedID` is unchanged but a transitively referenced remote base now resolves to a different commit SHA
- **THEN** the `inputHash` differs because the remote-base SHA set changed
- **AND** the render is recomputed rather than served from the stale entry

For kustomize, cache identity MUST additionally include canonical repository and
authorization context, and preserve the association of each transitive remote identity
with its resolved SHA (not just an unordered SHA set). Preparation MUST run before
lookup to resolve mutable remote refs and current credentials. These additions MUST
NOT change Helm caching behavior.

#### Scenario: private cached content does not cross authorization contexts

- **WHEN** another operand requests the same private repository and SHA using different or rotated credentials
- **THEN** neither the base cache nor the kustomize render cache serves the previous authorization context's content
