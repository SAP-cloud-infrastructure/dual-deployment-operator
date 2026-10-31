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

The only safe way to do this (verified: krusty exposes **no** network/pin/cache
knob — `LoadRestrictionsNone` governs local path traversal only, never remote
fetching) is to move acquisition **out** of krusty: DDO discovers every remote
reference, resolves each pinned `?ref=` to an immutable commit **SHA**, fetches it
fail-closed, caches it keyed on the SHA, rewrites the tree to local paths, then runs
krusty **fully offline**. This is issue #58 Option B / "Option A hardened for real";
the cheaper render-time-fetch-with-guardrails variant was explicitly rejected
because it delivers no reproducibility.

## What Changes

- **New prefetch engine** in `internal/source/` that, before krusty runs, walks the
  resolved kustomize root **transitively**, discovers every remote reference in
  `resources:` / `components:` / `bases:` (classified with Kustomize's own git-URL
  grammar, not a naive `://` check), and materializes a self-contained local tree.
- **Per-base pin-to-SHA + fail-closed fetch**, reusing the in-tree `gitResolver`
  (`ResolveID` → immutable commit SHA via `ls-remote`; `fetchSHA` → full-depth
  fail-closed clone; `authFor` → per-host credentials with anonymous fallback;
  `splitRef` → `repo//subdir?ref=` parsing). Each remote base is pinned by DDO, not
  by krusty.
- **SHA-keyed caching of fetched bases** so egress is **once per SHA**, not once per
  reconcile, reusing the existing SHA-keyed cache machinery (`render-cache`).
- **Rewrite to local relative paths** into a staging tree (remote URL string →
  local path for the exact selected subdir, relative to the rewritten
  kustomization's own directory), leaving a fully-vendored-looking tree.
- **Offline render + hard offline guarantee**: krusty builds the localized tree with
  plugins disabled (the default — exec plugins are never enabled for fetched
  content), and a **preflight rejects** any `resources:`/`components:`/`bases:` entry
  anywhere in the staged tree that still parses as a remote git target, so
  `krusty.Run` provably makes no network call.
- **BREAKING (admission)**: every remote-base reference MUST carry a pinned
  `?ref=<immutable>` — enforced fail-closed in the resolver and surfaced as a CEL
  admission rejection for the operand author (mutable/missing refs rejected). A bare
  branch name or missing ref on a remote base is rejected rather than silently built.
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
- `render-cache`: Cache keys extend to per-remote-base resolved SHAs so each base is
  fetched at most once per SHA across reconciles.
- `cel-admission-validation`: Add a rule requiring any remote-base git reference to
  carry an immutable pinned `?ref=` (reject mutable/missing refs at admission).
- `source-credentials`: Per-host credential resolution (`authFor`) is applied to
  each remote-base host reached during transitive prefetch, not only the root host.

## Impact

- **Code**: new prefetch module under `internal/source/` (transitive kustomization
  parsing via in-tree `sigs.k8s.io/kustomize/kyaml`, remote/local classification,
  depth-first fetch-and-recurse, staging-tree copy, URL→local-path rewrite, offline
  preflight rejector); `kustomize.go` renders the staged tree; wiring of the SHA
  cache through per-base fetches; `gitResolver` reused for each base.
- **CRD / admission**: new CEL `XValidation` on the kustomize source for remote-base
  pin enforcement; `make manifests` regenerates `config/crd/bases/*`.
- **Dependencies**: none new — `kustomize/api` + `kyaml` (v0.21.x), `go-git`, and the
  SHA cache are all already in-tree; the blocker was invocation/policy, not a missing
  capability.
- **Deployment (cross-ref, not edited here)**: the offline guarantee is enforced in
  code; the seed NetworkPolicy/egress posture for the operator Pod remains a Phase 9
  chart concern and is unchanged by this change.
- **Out of scope**: live-vs-upstream divergence stays an **operand-authoring gate**
  (only point a remote base at upstream when upstream == deployed) — documented, not
  mechanized. The `ipam-capi-remote` core-CAPI CRDs that come from a *different*
  upstream repo still need their own source entries.
