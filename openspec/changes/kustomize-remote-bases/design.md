<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Design: kustomize-remote-bases

## Context and approved corrections

Issue #58 requires DDO, not krusty, to acquire transitive Git bases. The root resolver
currently fetches only the root. On 2026-10-31 the user approved correcting the
artifacts and implementing on the existing branch: root-only CEL, runtime resolution
of transitive refs, origin-scoped credentials, authorization-isolated caching and
broader fail-closed preflight. A network-denied execution sandbox was not selected.

## Goals / Non-goals

Discover → resolve SHA → fetch/cache → stage → preflight → krusty. Preserve supported
local operands' output, disable plugins, clean scratch on every exit. Do not change
Helm, the RootResolver interface, or deployment egress policy. Do not fetch arbitrary
HTTP files. Live-vs-upstream equivalence remains an authoring gate.

## Decisions

### D1 — Ref resolution and traversal

Require a non-empty ref on every Git base. Resolve tags/branches on each preparation
to full immutable SHAs, preferring peeled tags; use a full SHA as-is. Reuse ResolveID
and full-depth fetchSHA. Fail closed without default-branch or krusty-fetch fallback.
Mutable refs can change between reconciles; recommend full SHAs for stable output.
CEL checks only the root URL, never fetched repository contents; no new marker.

Use typed Kustomization parsing and all konfig-recognized filenames. Traverse local
directories and fetched resources/components/bases depth-first; detect cycles.
Kustomize's Git parser is internal, not a public API. Implement a documented subset:
HTTPS repositories with optional explicit //subdir and mandatory ref, plus GitHub
shorthand normalized to HTTPS. Reject other remote-looking forms, including SSH.
Local Git transport is a test seam, not production authoring. Distinguish real local
paths containing :// from URL targets. Test compatibility with pinned Kustomize.

### D2 — Credentials and caching

withGitCreds currently ignores host; do not reuse it unchanged for bases. Bind the
CR Secret to the root HTTPS origin (scheme, hostname, effective port); other origins
are anonymous. No new host-map CRD or SSH support. Never expose secret values.

Base cache keys include canonical repository, full SHA and authorization context,
not only git:<host> + SHA. Isolate private content across credential contexts and
Secret rotation; enforce the same boundary before kustomize render-cache hits.
Re-resolve credentials before cache use. A non-reversible in-process credential
fingerprint may enter identity, never raw credentials. Public bases share across
operands; private bases share only within the same authorization context. Cache
bounded immutable snapshots, not scratch paths; make it concurrency-safe. Eviction
permits refetch. Hits avoid content fetch, not mutable-ref resolution or Secret reads.

### D3 — Staging and cache invalidation

Never rewrite resolver checkouts in place. Stage repository content preserving local
sibling references, with deterministic identity/SHA base directories. Rewrite each
URL relative to its kustomization, selecting the exact URL subdirectory. Guard path
traversal and symlink escapes. Prepare before render-cache lookup: hash associated
remote identities and resolved SHAs, not an unordered SHA-only set. Compose with
Deps.RenderCache without changing Helm cache behavior.

### D4 — Preflight, not a network sandbox

Git-only scanning is insufficient: Kustomize also loads HTTP files. Check every
reachable recognized kustomization and all supported path-bearing fields, including
local generator/transformer/validator configurations. Reject residual Git references
and unsupported URL loads before krusty, even when no Git bases were found. Keep
external plugins and Helm execution disabled. Fail closed for unsupported loading
constructs; do not silently discard unknown fields through typed rewriting.

Claim no krusty network access for accepted inputs under the pinned dependency,
tested with observable HTTP/Git endpoints. This is not an OS-enforced or future-proof
sandbox. Dependency upgrades must revisit loading-path coverage.

### D5 — Scratch and phase placement

NewGitResolver(scratchDir string) roots root/base checkouts and staging under the
configured directory, falling back to os.TempDir only when empty. Reuse source-scratch
emptyDir; document sizing for full-depth clones plus staging copies. Clean scratch on
success, failure and cancellation; cache snapshots remain bounded and independent.
Add a new phase after Phase 10; leave completed/ongoing phases untouched. Put Phase 9
egress/scratch cross-references in the new section, not in Phase 9 itself.

## Alternatives / risks

Leaving acquisition to krusty with external guardrails was previously rejected: it
does not provide DDO-controlled identity or invalidation. A network-denied subprocess
would strengthen isolation but expands architecture/packaging; user selected broader
preflight with a bounded claim. SHA-only authoring was not selected: tags/branches
remain compatible with existing source semantics. Full-depth cloning and staging
cost disk/memory; bounded caching, cycle rejection and serial traversal limit cost.

## Sources and verification

- HTTP loading: https://github.com/kubernetes-sigs/kustomize/blob/kustomize/v5.8.1/api/internal/loader/fileloader.go
- Filenames: https://github.com/kubernetes-sigs/kustomize/blob/kustomize/v5.8.1/api/konfig/general.go
- Internal parser: https://github.com/kubernetes-sigs/kustomize/blob/kustomize/v5.8.1/api/internal/git/repospec.go
- CEL scope: https://kubernetes.io/docs/reference/using-api/cel/
- Credential isolation precedent: https://fluxcd.io/flux/components/source/helmrepositories/#pass-credentials

Focused TDD: classification, transitive staging, historical SHA fetch, origin/auth
isolation, cache movement, cleanup and HTTP-loading rejection. Use local fixtures
including an ipam-capi-shaped example, not internal registries or live clusters.
Final gate: build, envtest-backed full Go tests, project lint and strict OpenSpec.
