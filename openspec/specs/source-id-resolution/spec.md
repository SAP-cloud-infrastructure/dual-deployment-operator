<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec: Source ID Resolution

## Purpose

Defines the `ResolveID` capability that each source loader in `internal/source` exposes: a cheap resolve step (git `ls-remote` / OCI manifest digest / HTTP `index.yaml`) that reports the immutable content id a source currently points at without fetching the full artifact. This resolved id is the soundness anchor for the render cache (Phase 7.5) — a moved ref yields a different id and therefore a cache miss, with no TTL.

## Requirements

### Requirement: Resolve a source to its immutable content id

The system MUST provide a `Resolver` capability that reports the immutable content id a source currently points at, without fetching the full artifact, via a method `ResolveID(ctx context.Context, mode Mode) (id string, err error)`. This resolved id is the soundness anchor for the render cache: a moved ref MUST yield a different id (and therefore a cache miss) with no TTL. Resolution MUST reuse the same per-source credentials already wired for fetching (`withHelmCreds` / `withGitCreds`), so an authenticated source resolves authenticated. For Helm sources, `ResolveID` MUST support ONLY the OCI transport (manifest digest); a non-`oci://` Helm `repo` MUST NOT reach `ResolveID` (it is rejected earlier at admission and by the loader).

#### Scenario: moved ref changes the resolved id

- **WHEN** a source pins a mutable ref (e.g. `?ref=main`) and the ref advances to a new commit
- **THEN** `ResolveID` returns the new content id on the next reconcile
- **AND** the render cache treats it as a miss and re-renders the new content

---

### Requirement: Resolve git refs via remote listing without cloning

For a git-backed kustomize source, `ResolveID` MUST resolve a tag or branch ref to its full 40-character commit SHA by listing the remote's advertised references without cloning (an `ls-remote`-equivalent), honoring the pinned `?ref=`. It MUST prefer the peeled commit for an annotated tag when the advertisement provides it, and fall back to the bare tag ref's commit hash for a lightweight tag. A tag or branch ref MUST resolve to a commit SHA and MUST NOT fall back to using the ref *name* (e.g. the literal string `v1.0.0`) as the resolved id — the id for a given ref MUST be canonical (always the commit SHA) so that a stable tag produces the same id (and cache hit) on every reconcile. If a tag or branch ref cannot be resolved to a commit SHA (not advertised, peeling unavailable, or a transient listing error), `ResolveID` MUST return an error (treated as a resolve failure — the caller renders uncached and caches nothing) rather than degrading to a name-based id, which would create a second cache entry for identical content. A ref pinned as a bare commit SHA (short or full) MUST be used as the resolved id as-is, because remote listing advertises only reference tips and cannot resolve an arbitrary non-tip SHA without a fetch; the pinned SHA is already immutable.

#### Scenario: tag ref resolves to its commit SHA

- **WHEN** `ResolveID` is called for a source pinned to a tag
- **THEN** it lists the remote references without cloning
- **AND** returns the full 40-character commit SHA the tag points at (the peeled commit for an annotated tag, the tag ref's commit hash for a lightweight tag)
- **AND** never returns the tag name as the id

#### Scenario: a stable tag resolves to the same id across reconciles

- **WHEN** the same unmoved tag is resolved on two successive reconciles
- **THEN** both reconciles yield the identical commit SHA as the resolved id
- **AND** the second reconcile is a cache hit (no spurious miss from a name-vs-SHA id difference)

#### Scenario: unresolvable ref is a resolve failure, not a name-keyed id

- **WHEN** a pinned tag or branch ref cannot be resolved to a commit SHA
- **THEN** `ResolveID` returns an error
- **AND** the caller renders uncached and stores nothing (it does NOT cache under the ref name)

#### Scenario: bare SHA ref is used as-is

- **WHEN** a source is pinned to a bare commit SHA
- **THEN** `ResolveID` returns that pinned SHA as the resolved id without attempting remote resolution

---

### Requirement: Resolve OCI Helm charts to their manifest digest

For an `oci://` Helm source, `ResolveID` MUST resolve the chart reference to its immutable manifest digest (`sha256:…`) without pulling chart blob layers, reusing the existing registry client construction and credentials. This is the fully-sound Helm case because OCI manifest digests are immutable.

#### Scenario: OCI reference resolves to a manifest digest

- **WHEN** `ResolveID` is called for an `oci://` chart at a version tag
- **THEN** it returns the `sha256:…` manifest digest without downloading blob layers
- **AND** reuses the configured registry credentials and HTTP client

---

### Requirement: Document the transitive-tag caveat for kustomize

The kustomize resolved id (the root commit SHA) covers the committed kustomization files but MUST be documented as NOT covering upstream transitive bases that those files reference by a mutable tag. A force-moved upstream tag under an unchanged root SHA MAY therefore serve a stale render until the pod restarts. The system MUST rely on upstream release tags being immutable and MUST document this caveat; full transitive-ref resolution is out of scope for this change.

#### Scenario: root SHA change is detected, transitive tag move under same root is not

- **WHEN** the kustomize root commit changes
- **THEN** the resolved id changes and the cache re-renders
- **AND** WHEN only a transitive upstream tag is force-moved while the root SHA is unchanged, the cache may serve the prior render until pod restart (documented caveat)
