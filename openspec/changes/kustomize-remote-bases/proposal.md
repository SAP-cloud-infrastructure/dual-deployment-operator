<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## Why

Today a kustomize overlay delivered by DDO must be fully self-contained / vendored
(issue #58). When a `kustomization.yaml` references an upstream **remote base** (a
`resources:` / `components:` / `bases:` entry pointing at another repo via
`repo//subdir?ref=`), krusty clones that URL **itself**, from inside the seed pod,
at **every reconcile**, with a ref DDO never pins and over an unbounded egress path.
That reintroduces exactly the failure classes DDO's `?ref=`-pinned, offline render
was built to prevent: non-reproducible renders (mutable tags drift between
reconciles → `SeedRenderFailed` or silent drift), per-reconcile egress dependence
on every transitive host, and render-time supply-chain exposure.

Vendoring avoids all of that but costs hand-maintenance, hides the upstream link
from reviewers, and is re-paid by every new kustomize operand. This change makes
remote bases **first-class and safe** so operands can reference pinned upstream
instead of vendoring — on par with how the Helm `-ddo` charts consume a pinned
`*-crds` subchart.

The selected approach (krusty exposes **no** network/pin/cache
knob — `LoadRestrictionsNone` governs local path traversal only, never remote
fetching) is to move acquisition **out** of krusty: DDO discovers every remote
reference, resolves each pinned `?ref=` to an immutable commit **SHA**, fetches it
fail-closed, caches it by repository/SHA/authorization context, rewrites the tree to
local paths, then runs krusty after version-specific offline preflight. This is
issue #58 Option B / "Option A hardened for real";
the cheaper render-time-fetch-with-guardrails variant was explicitly rejected
because it delivers no reproducibility.

## What Changes

- **New prefetch engine** in `internal/source/` that, before krusty runs, walks the
  resolved kustomize root **transitively**, discovers every remote reference in
  `resources:` / `components:` / `bases:` (a documented conservative Git-URL
  subset, not a naive `://` check), and materializes a self-contained local tree.
- **Per-base pin-to-SHA + fail-closed fetch**, reusing the in-tree `gitResolver`
  (`ResolveID` → immutable commit SHA via `ls-remote`; `fetchSHA` → full-depth
  fail-closed clone; `authFor` → root-origin-scoped credentials with anonymous fallback;
  `splitRef` → `repo//subdir?ref=` parsing). Each remote base is pinned by DDO, not
  by krusty.
- **Bounded caching of fetched bases** by canonical repository, full SHA and current
  authorization context. Resident hits avoid content fetch; mutable refs still resolve.
- **Rewrite to local relative paths** into a staging tree (remote URL string →
  local path for the exact selected subdir, relative to the rewritten
  kustomization's own directory), leaving a fully-vendored-looking tree.
- **Version-tested offline rendering**: krusty builds with plugins disabled after
  preflight rejects residual remote targets across all supported loading fields and
  recognized kustomization filenames. This is not an enforced network sandbox.
- **BREAKING (render time)**: every remote-base reference MUST carry a non-empty
  `?ref=`. Tags and branches resolve to immutable SHAs before fetch; missing or
  unresolvable refs fail closed. CEL checks only the root URL. Unsupported URL loads
  are rejected across all supported loading fields, not only Git-base lists.
- **Docs**: document in the `source.kustomize` operand-authoring guide when to
  reference a remote base vs. vendor, and make the **"only when upstream == the
  deployed shape"** divergence gate explicit (the one risk neither pinning nor
  caching removes; see issue #58 `ipam-capi-remote` worked example).
- **Phase placement**: lands as a **new phase after Phase 10** in
  `docs/implementation.md` (per the not-started-phase scoping rule); it does not edit
  any completed or ongoing phase.

## Capabilities

### New Capabilities
- `kustomize-remote-bases`: Transitive discovery, per-base pin-to-SHA fetch,
  SHA-keyed caching, rewrite-to-local staging, and the offline-render preflight that
  turns krusty's render-time remote fetch into a DDO-pinned, reproducible, offline
  build.

### Modified Capabilities
- `kustomize-root-resolver`: The resolver's pinned-ref, fail-closed, SHA-resolution
  machinery is extended from the **root only** to **every transitive remote base**
  (reusing `ResolveID`/`fetchSHA`/`authFor`/`splitRef`); the resolver remains the
  single owner of remote git-fetch logic.
- `source-rendering`: The kustomize renderer MUST build a **localized, offline**
  staging tree (no remaining remote references) rather than handing krusty a root
  that may fetch during `Run`; the `Source`/`RootResolver` seam and krusty invocation
  otherwise stay as specified.
- `render-cache`: Keys include associated remote identities/SHAs and current
  authorization context; bounded resident base snapshots avoid repeated content fetch.
- `cel-admission-validation`: Retain root-only ref validation and document the
  separate render-time transitive gate; CEL cannot inspect fetched repository content.
- `source-credentials`: Bind root credentials to the root HTTPS origin; other origins
  remain anonymous. Private caches are isolated by current authorization context.

## Impact

- **Code**: new prefetch module under `internal/source/` (transitive kustomization
  parsing via in-tree `sigs.k8s.io/kustomize/kyaml`, remote/local classification,
  depth-first fetch-and-recurse, staging-tree copy, URL→local-path rewrite, offline
  preflight rejector); `kustomize.go` renders the staged tree; wiring of the SHA
  cache through per-base fetches; `gitResolver` reused for each base.
- **CRD / admission**: no new marker; verify the existing root ref gate remains intact.
- **Dependencies**: none new — `kustomize/api` + `kyaml` (v0.21.x), `go-git`, and the
  SHA cache are all already in-tree; the blocker was invocation/policy, not a missing
  capability.
- **Deployment (cross-ref, not edited here)**: accepted inputs are checked in code
  against the pinned Kustomize version, not sandboxed; the seed NetworkPolicy/egress
  posture for the operator Pod remains a Phase 9
  chart concern and is unchanged by this change.
- **Out of scope**: live-vs-upstream divergence stays an **operand-authoring gate**
  (only point a remote base at upstream when upstream == deployed) — documented, not
  mechanized. The `ipam-capi-remote` core-CAPI CRDs that come from a *different*
  upstream repo still need their own source entries.
