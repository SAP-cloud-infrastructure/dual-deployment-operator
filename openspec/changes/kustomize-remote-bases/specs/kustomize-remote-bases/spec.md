<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec Delta: kustomize-remote-bases

## ADDED Requirements

### Requirement: Transitive discovery of kustomize remote bases

The operator MUST discover every **remote** reference reachable from the resolved
kustomize root by parsing each recognized kustomization filename with the typed
`sigs.k8s.io/kustomize/api/types.Kustomization` and inspecting its `resources`,
`components`, and (deprecated) `bases` list fields. Each entry MUST be classified as
local-vs-remote using a documented conservative subset of Kustomize Git URL syntax
(HTTPS repository URLs and GitHub shorthand), NOT a naive `"://"` substring test.
Unsupported remote forms MUST fail closed without importing internal Kustomize APIs.
Discovery MUST recognize `kustomization.yaml`, `kustomization.yml`, and `Kustomization`,
and MUST reject cycles. Discovery MUST be **transitive and
depth-first**: after a remote base is fetched, its own `kustomization.yaml` MUST be
parsed and its remote references discovered and processed the same way, to arbitrary
depth. Local directories that themselves contain a `kustomization.yaml` MUST be
recursed into.

#### Scenario: a direct remote base is discovered

- **WHEN** the resolved root's `kustomization.yaml` lists a `resources:` entry that parses as a Kustomize git URL (`repo//subdir?ref=`)
- **THEN** that entry is classified as a remote base
- **AND** a local `resources:` path entry alongside it is classified as local and left for krusty to load normally

#### Scenario: a transitively nested remote base is discovered

- **WHEN** a fetched remote base's own `kustomization.yaml` lists a further remote `components:` entry
- **THEN** that nested remote reference is also discovered and processed
- **AND** discovery continues depth-first until no remote references remain

#### Scenario: deprecated bases field is covered

- **WHEN** a `kustomization.yaml` uses the deprecated `bases:` field with a remote git URL
- **THEN** that entry is classified and processed identically to a `resources:` remote entry

### Requirement: Each remote base is pinned to an immutable commit SHA and fetched fail-closed

For every discovered remote base URL, the operator MUST resolve its `?ref=` to an
immutable commit SHA using the source-id-resolution machinery (ls-remote, preferring a
peeled annotated-tag commit) and MUST fetch exactly that commit fail-closed, reusing
the production git resolver's full-depth fetch so an arbitrary historical SHA resolves.
A `?ref=` that is already a 40-character commit SHA MUST be used as-is. Resolution and
fetch MUST fail closed: an unresolvable or unreachable remote base MUST fail the render,
and the operator MUST NOT fall back to a default branch or to krusty's own unpinned
fetch. Per-host credentials MUST be applied to each remote base's fetch using the same
`authSecretRef`, restricted to the root HTTPS origin (scheme, hostname, effective
port); other origins MUST be anonymous. Tags and branches are accepted refs and
resolved anew for each preparation; full SHAs provide cross-reconcile stability.

#### Scenario: remote base tag is resolved to a commit SHA before fetch

- **WHEN** a remote base URL pins `?ref=v1.1.1` (a tag)
- **THEN** the operator resolves that tag to its immutable commit SHA via remote listing
- **AND** fetches that exact commit, not whatever the tag currently points at during the render

#### Scenario: remote base bare SHA is fetched as-is

- **WHEN** a remote base URL pins `?ref=<40-char-sha>`
- **THEN** the operator fetches that specific commit full-depth so an arbitrary historical commit resolves

#### Scenario: unresolvable remote base fails closed

- **WHEN** a remote base's pinned `?ref=` cannot be resolved or fetched
- **THEN** the render fails with an error naming the offending base
- **AND** the operator does NOT fall back to a default branch or let krusty fetch it unpinned

#### Scenario: authenticated remote base fetch uses per-host credentials

- **WHEN** a remote base points at the root HTTPS origin and credentials resolve from the CR's `authSecretRef`
- **THEN** the operator applies those credentials to that base's fetch
- **AND** a remote base on another origin is fetched anonymously, without forwarding the root credentials

### Requirement: Fetched remote bases are cached keyed on their resolved SHA

The operator MUST cache each fetched remote base keyed on its full resolved commit SHA,
canonical repository identity and authorization context. Entries MUST be bounded,
concurrency-safe immutable snapshots independent of per-render scratch. Public bases
may share entries across operands; private entries MUST NOT cross authorization
contexts. Credentials MUST be re-resolved before cache use and credential rotation
MUST invalidate access to old private entries. Within a resident matching entry, a
shared base is fetched once; eviction may require a subsequent refetch. This
base-level cache MUST compose with — and MUST NOT replace — the existing render cache
that keys the whole render on the root content id. A cache miss MUST fetch; a cache hit
MUST avoid content-fetch egress for that base (mutable-ref resolution can still use
the network). The kustomize render cache MUST enforce the same authorization boundary.

#### Scenario: a base shared by two operands is fetched once

- **WHEN** two kustomize operands reference the same remote base at the same resolved SHA
- **THEN** the base is fetched on the first miss and served from cache on the second
- **AND** no second network fetch is performed for that SHA

#### Scenario: a moved tag on an unchanged root still refetches the new SHA

- **WHEN** the root ref is unchanged but a transitive remote base's tag now resolves to a different commit SHA
- **THEN** the new SHA is a distinct cache key and is fetched
- **AND** the stale SHA's cached content is not served for the new ref

### Requirement: Remote bases are rewritten to local paths in a staging tree

The operator MUST materialize a self-contained **staging tree**: it copies each
`kustomization.yaml` and its referenced local files, drops each fetched remote base into
a deterministic subdirectory keyed by that base's resolved SHA, and rewrites each remote
reference string to a **local path relative to the directory containing the rewritten
kustomization file** (NOT the process working directory), pointing at the exact subdir
the git URL selected (not the repo root). The operator MUST NOT rewrite references
in-place in the resolver's temporary checkout. An operand that contains **no** remote
bases MUST produce a byte-identical render to today (nothing to rewrite).

#### Scenario: a remote reference becomes a relative local path

- **WHEN** a `kustomization.yaml` references `repo//config/crd/bases?ref=<sha>`
- **THEN** the staged copy of that kustomization references a local relative path to the fetched `config/crd/bases` subdirectory of that commit
- **AND** the path is relative to the staged kustomization's own directory

#### Scenario: operand without remote bases is unchanged

- **WHEN** a kustomize operand references only local paths
- **THEN** the staging step produces an equivalent tree and the render output is byte-identical to the pre-change behavior

### Requirement: Accepted inputs render offline under the pinned Kustomize version

Before invoking `krusty`, even if no Git bases were found, the operator MUST run a
preflight over all reachable recognized kustomizations and supported loading paths.
It MUST reject residual remote Git targets and unsupported URL loads in resources,
components, bases, patches, OpenAPI, configurations, CRDs, generator file/env inputs,
and generator/transformer/validator configuration files. Unsupported loading constructs
MUST fail closed; unknown fields MUST NOT silently disappear through typed rewriting.
External plugins and Helm execution MUST stay disabled. Accepted inputs MUST render
without network access under the pinned Kustomize version, established by regression
tests with observable endpoints. This preflight is NOT an OS-enforced or future-proof
network sandbox; dependency upgrades require reviewing its coverage.

#### Scenario: HTTP file inputs are rejected without a request

- **WHEN** a reachable resource, patch, OpenAPI, configuration or generator input names an unsupported HTTP URL
- **THEN** preflight fails before rendering and no request reaches that URL

#### Scenario: alternate filenames and supplemental configurations are checked

- **WHEN** nested `kustomization.yml`, `Kustomization`, or a local built-in generator configuration contains a remote loading path
- **THEN** preflight rejects it rather than allowing an unvalidated load

#### Scenario: residual remote reference fails preflight

- **WHEN** the staged tree still contains a reference that parses as a remote git target
- **THEN** preflight returns an error and the render does not proceed
- **AND** no krusty-driven network fetch occurs

#### Scenario: fully localized tree passes preflight and renders offline

- **WHEN** the staged tree contains only local references and passes preflight
- **THEN** krusty builds the tree with no network egress
- **AND** plugins remain disabled throughout

### Requirement: Prefetch scratch I/O is rooted under the configured scratch directory

All writable scratch the prefetch engine produces — per-base git checkouts and the
materialized staging tree — MUST be created under the operator's configured source
scratch directory (`--source-scratch-dir`), the same single writable volume the Helm
loader uses, so the kustomize render path works under
`securityContext.readOnlyRootFilesystem: true`. When the scratch directory is empty
(unset), the operator MUST fall back to the OS temp directory (`os.TempDir()`) for
local runs and tests. Every scratch subtree MUST be removed by a `cleanup` after the
render so the bounded volume does not accumulate across reconciles. The operator MUST
NOT write prefetch scratch to any path outside the configured scratch directory when
one is set.

#### Scenario: staging tree and base checkouts honor the scratch directory

- **WHEN** `--source-scratch-dir` is set to the writable `source-scratch` mount and a kustomize operand with remote bases is rendered
- **THEN** every per-base checkout and the staging tree are created under that directory
- **AND** no prefetch scratch is written to `os.TempDir()` or any read-only path

#### Scenario: empty scratch directory falls back to the OS temp dir

- **WHEN** `--source-scratch-dir` is empty
- **THEN** prefetch scratch is created under `os.TempDir()` (local/test behavior)

#### Scenario: scratch is cleaned up after the render

- **WHEN** a render that materialized a staging tree completes (success or failure)
- **THEN** the `cleanup` removes the staging tree and all per-base checkouts it created
