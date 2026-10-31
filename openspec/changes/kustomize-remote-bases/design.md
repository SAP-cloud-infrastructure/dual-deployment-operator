<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Design: kustomize-remote-bases

## Context

DDO renders a kustomize operand by resolving the pinned `?ref=` **root** repo into a
local temp dir (`gitResolver.Resolve`, `internal/source/gitresolver.go`) and running
krusty over it (`kustomizeSource.Render`, `internal/source/kustomize.go`, with
`LoadRestrictions = LoadRestrictionsNone`). The render runs **inside the seed pod**.

Today a remote base — a `resources:` / `components:` / `bases:` entry that is a
Kustomize git URL (`repo//subdir?ref=`) pointing at *another* repo — is fetched by
**krusty itself** during `kust.Run`, via krusty's built-in git getter. Two
independent gates make that unsafe (issue #58):

- **Gate 1**: `gitResolver.Resolve` fetches **only the root** repo; it never touches
  any other repo, so remote bases are not DDO-pinned.
- **Gate 2**: krusty does the remote fetch itself, unpinned by DDO, at **every
  reconcile**, over seed egress to arbitrary hosts.

Verified constraint (research): **krusty exposes no network/pin/cache knob**.
`LoadRestrictions` governs only *local* filesystem path traversal, never remote
fetching. There is no krusty option meaning "allow remote bases but keep the getter
offline / pinned / cached". Therefore the only way to get reproducibility + pinning
+ caching is to move acquisition **out of** krusty and hand it a tree that already
has no remote references.

Decision made with the user (issue #58 investigation): build the real fix —
prefetch → resolve-to-SHA → SHA-keyed cache → rewrite-to-local → offline krusty
render — not the cheaper render-time-fetch-with-external-guardrails variant (that
variant was explicitly rejected because it delivers no reproducibility: mutable tags
still drift, egress still happens every reconcile). This is issue #58 "Option B" /
"Option A hardened for real".

The building blocks already exist in-tree: `sigs.k8s.io/kustomize/api` + `kyaml`
(v0.21.x), `go-git`, and `gitResolver`'s `ResolveID` (ls-remote → immutable commit
SHA), `fetchSHA` (full-depth fail-closed clone), `authFor` (per-host creds,
anonymous fallback), `splitRef` (`repo//subdir?ref=` parse, rejects embedded
userinfo, mandatory `?ref=`), `repoScope` (`git:<host>` key), plus a SHA-keyed
`Deps.RenderCache`. The blocker is invocation/policy, not a missing capability.

## Goals / Non-Goals

**Goals:**
- Let a kustomize operand reference a **pinned upstream remote base** instead of
  vendoring, with the same reproducibility/offline guarantees DDO gives the root.
- Discover remote references **transitively** (a fetched base may reference further
  remote bases) across `resources:`, `components:`, and deprecated `bases:`.
- Pin every remote base to an **immutable commit SHA** (DDO-resolved, not krusty's),
  fetch fail-closed, and **cache keyed on the SHA** so egress is once-per-SHA.
- Produce a **localized staging tree** with no remaining remote references and render
  it with krusty **fully offline**, provably (preflight rejects any residual remote
  reference; plugins stay disabled).
- Enforce the pin at admission (CEL) so mutable/missing remote-base refs are rejected
  before they ever reach a reconcile.

**Non-Goals:**
- **Live-vs-upstream divergence** is NOT mechanized. "Only point a remote base at
  upstream when upstream == the deployed shape" stays an operand-authoring gate,
  documented only (issue #58 `ipam-capi-remote` worked example).
- **No new krusty behavior / no plugins.** Exec plugins are never enabled for fetched
  content; built-in generators/transformers run exactly as krusty does today, over
  the localized tree.
- **No change to the seed NetworkPolicy/egress chart posture** — that is a Phase 9
  chart concern; the offline guarantee here is enforced in code.
- **No arbitrary non-git remote resolution** (e.g. `raw.githubusercontent.com` single
  files are out of initial scope unless they parse as a Kustomize git target); only
  Kustomize git-URL remote targets are prefetched.
- **No Helm-side change.** This is kustomize-only; the Helm loader is untouched.

## Decisions

### D1 — Prefetch replaces *acquisition only*; krusty still renders

We move the git fetch out of krusty into DDO, then run krusty unchanged over a
localized tree. krusty still loads the root + recursive bases/components/resources,
applies name prefix/suffix + namespace, runs generators, applies patches/transformers,
orders and validates. **Alternative rejected**: a krusty option to pin/cache/offline
the getter — it does not exist (`LoadRestrictions` is local-only). **Alternative
rejected**: render-time fetch + external guardrails (NetworkPolicy allowlist / RO
rootfs / CEL-SHA / timeout) — contains blast radius but gives no reproducibility;
rejected per the user decision.

### D2 — Resolve each remote base to an immutable commit SHA, reuse `gitResolver`

For every discovered remote URL, call `gitResolver.ResolveID` (ls-remote → peeled
annotated-tag commit, else lightweight tag, else branch head → a 40-char SHA), then
`fetchSHA` to clone that exact commit fail-closed. A bare-SHA `?ref=` is used as-is.
This reuses the root resolver's proven machinery verbatim; the **only** change is
applying it per-base rather than root-only. **Alternative rejected**: trusting the
tag string as the cache/identity key — a moved tag silently serves stale content
(issue #58). SHA is the only immutable identity.

### D3 — Transitive, depth-first discovery via typed Kustomization parsing

Parse each `kustomization.yaml` with `sigs.k8s.io/kustomize/api/types.Kustomization`
and inspect `.Resources`, `.Components`, `.Bases`. Classify each entry local-vs-remote
with **Kustomize's own git-URL grammar** (conservative; not a naive `://` test).
Recurse depth-first: read kustomization → classify → recurse local dirs that contain
a kustomization → fetch + recurse each remote → rewrite. **Alternative considered**:
kyaml `yaml.RNode` to preserve byte formatting — unnecessary because we write a
**new staging tree** (D4), so typed parse + typed re-emit is simpler and sufficient.
Caveat recorded: rely only on the stable boundary (typed `Kustomization` +
`Kustomizer.Run`); do **not** call unexported loader internals or git-URL
constructor internals (they churn across kustomize versions).

### D4 — Staging tree with relative local paths (not in-place rewrite)

Materialize everything into a fresh staging directory: copy each kustomization and
its local files, drop each fetched remote base into a deterministic subdir keyed by
its SHA, and rewrite the remote URL string to a **local path relative to the
directory of the rewritten kustomization** (not process cwd), pointing at the exact
subdir the URL selected (not repo root). **Alternative rejected**: in-place rewrite
of the resolved root — fragile (mutates the resolver's temp dir, poor cache reuse,
harder to assert offline). A staging tree is portable and cache-friendly.

### D5 — SHA-keyed base cache, layered under the existing render cache

Cache each **fetched base** keyed on its resolved SHA (+ `repoScope` host), so a base
shared by two operands, or re-seen across reconciles, is fetched once. This composes
with — does not replace — the existing `Deps.RenderCache` (which keys the whole
render on the root content id). **Alternative considered**: only the outer render
cache — insufficient, because the root ref can be unchanged while a transitive base
moved; per-base SHA keys are the correct granularity.

### D6 — Hard offline guarantee: preflight rejector + plugins off

After staging and before `kust.Run`, a **preflight** recursively parses every staged
kustomization and **errors** if any `resources:`/`components:`/`bases:` value still
parses as a remote git target. Combined with plugins staying disabled (krusty
default; never `--enable-alpha-plugins`), this makes the render provably offline:
`filesys.MakeFsOnDisk()` never fetches git itself, and a fully-localized tree makes
krusty's loader always take the local-path branch. **Alternative considered**: rely
on an OS/network sandbox alone — complementary (belongs to the Phase 9 chart), but
the in-code preflight is the portable, test-anchored guarantee and is the deliverable
here.

### D7 — Admission pin enforcement (CEL) in addition to resolver fail-closed

The resolver already fails closed on a missing/unresolvable `?ref=`. We add a CEL
`XValidation` so an operand author gets a **fast admission rejection** if a remote
base lacks an immutable pin, rather than a runtime `SeedRenderFailed`. CEL enforces
the *pin is present and shaped immutable*; the resolver enforces *it actually
resolves*. **Alternative considered**: resolver-only — worse DX (failure surfaces
late, per-reconcile) and weaker (admission is the hard gate).

### D8 — Phase placement

Lands as a **new phase after Phase 10** in `docs/implementation.md`. Per the
not-started-phase scoping rule, it does not edit any completed or ongoing phase;
cross-references to Phase 9 (egress labels) are additive notes only.

### D9 — All prefetch scratch I/O roots under `--source-scratch-dir` (chart emptyDir)

The prefetch engine writes a lot of scratch: per-base fetched checkouts plus the
materialized staging tree. The pod runs with `securityContext.readOnlyRootFilesystem:
true` and mounts exactly one writable volume — the `source-scratch` emptyDir at
`/var/run/ddo-source`, passed as `--source-scratch-dir` (chart `values.yaml`). The
Helm loader already roots its temp dirs there (`helmLoader.newScratchDir()` →
`os.MkdirTemp(scratchDir, …)`). **The kustomize `gitResolver` does NOT**: it calls
`os.MkdirTemp("", "ddo-kustomize-")`, which resolves to `os.TempDir()` (`/tmp`) — a
path that is **not** mounted writable, so the production kustomize path fails closed on
a read-only rootfs. This is a pre-existing latent gap that the prefetch work would
inherit and worsen.

Decision: thread `--source-scratch-dir` into the kustomize resolver exactly as the
Helm loader does — `NewGitResolver(scratchDir)` stores the base and every
`os.MkdirTemp` (root checkout, per-base checkouts, staging tree) uses it; empty string
preserves the `os.TempDir()` default for tests/local runs. The chart's existing
`source-scratch` emptyDir is **reused** (shared by Helm and kustomize); its
`sizeLimit` (currently `256Mi`) and the mount comment are revisited because the
staging tree + transitive base checkouts raise the kustomize-path footprint. **No new
volume** is introduced. **Alternative rejected**: a second dedicated kustomize scratch
volume — unnecessary duplication; one writable scratch mount is the established
pattern. **Alternative rejected**: mounting an emptyDir at `/tmp` — broader surface and
diverges from the explicit `--source-scratch-dir` contract the Helm loader already
honors.

Cleanup discipline is unchanged: each `Resolve`/prefetch returns a `cleanup` that
`RemoveAll`s its scratch subtree, so the bounded emptyDir does not accumulate across
reconciles.

## Risks / Trade-offs

- **Kustomize-internal API churn across versions** → Pin to the stable boundary
  (typed `types.Kustomization` + `krusty.Kustomizer.Run`); avoid unexported loader /
  git-URL internals. Add a focused test that fails loudly on a kustomize bump that
  breaks remote-target classification.
- **Incomplete remote-reference discovery** (a field other than
  resources/components/bases carrying a remote target, e.g. a generator/patch path)
  → Preflight (D6) is the backstop: anything still remote-parseable in the staged
  tree fails the build rather than silently fetching at render. Start with the three
  list fields; extend coverage if a real operand needs it.
- **Transitive fan-out / fetch cost on a cold cache** → SHA-keyed caching (D5) bounds
  egress to once-per-SHA; full-depth fetch (required for arbitrary historical SHAs)
  is the known cost, same as the root today.
- **Live-vs-upstream divergence** (upstream ≠ deployed shape) → NOT solved by this
  mechanism (explicit Non-Goal); mitigated by **documentation** of the
  "only-when-upstream==deployed" authoring gate and the `ipam-capi-remote` example.
- **Supply-chain (fetched kustomize could carry exec plugins/generators)** → plugins
  disabled always; generators/transformers run only as krusty's built-ins over an
  already-materialized, SHA-pinned tree; no code execution path is enabled by
  prefetch.
- **BREAKING for authors who relied on an unpinned remote base building today** →
  intended: such a build was never reproducible. Surface a clear CEL/resolver error
  naming the missing pin.
- **Read-only rootfs scratch exhaustion / write failure** → All prefetch scratch roots
  under the single writable `source-scratch` emptyDir (D9); the pre-existing
  `gitResolver` `os.TempDir()` write (which fails on the read-only rootfs) is fixed in
  the same change. The emptyDir `sizeLimit` is revisited for the larger kustomize
  footprint, and per-reconcile `cleanup` bounds accumulation.

## Migration Plan

1. Land the prefetch engine, renderer wiring, and preflight behind the existing
   kustomize path — operands with **no** remote bases are byte-unaffected (the tree
   has nothing to rewrite; render is identical).
2. Add the CEL pin-enforcement marker; `make manifests` regenerates CRDs.
3. Thread `--source-scratch-dir` into `NewGitResolver`; revisit the chart
   `source-scratch` emptyDir `sizeLimit` for the kustomize footprint (reuse the
   existing volume — no new mount). This also fixes the pre-existing read-only-rootfs
   write failure on the kustomize path.
4. Document the authoring guidance + divergence gate in the `source.kustomize` docs.
5. No data migration; no live-object churn for existing operands (same render → same
   SSA → same UIDs). Rollback = revert the change; vendored operands keep working
   throughout since they carry no remote bases.

## Open Questions

- Should discovery eventually cover remote targets in non-list fields (generator /
  transformer / patch file paths that are git URLs), or is the preflight-reject
  backstop sufficient for the foreseeable operands? (Start: three list fields +
  preflight; revisit on demand.)
- Do we want a per-base fetch **concurrency** bound, or is serial depth-first fetch
  acceptable given the small fan-out and the SHA cache? (Default: serial; optimize
  only if a real operand's base graph is wide.)
