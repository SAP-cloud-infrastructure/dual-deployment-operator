## Context

The operator renders a per-operator Helm chart or kustomize source twice per reconcile (seed + shoot) and applies each half to its target cluster. The Helm side is handled by `helmLoader` in [`internal/source/helmloader.go`](../../../internal/source/helmloader.go): `Load()` pulls the chart archive (OCI or classic HTTP(S)) into a temp dir and calls `helm.sh/helm/v3/pkg/chart/loader.Load()` on it.

`loader.Load()` is a **pure unpack** — it reads whatever `charts/` the archive contains and does **not** fetch dependencies declared in `Chart.yaml`. A chart that declares `dependencies:` therefore renders with the subchart **silently absent** unless the subchart was vendored (`charts/<dep>-<ver>.tgz`) into the published `.tgz`. This is an undocumented "vendor all subcharts" contract on chart authors.

The motivating consumer is `metal-operator-remote-v2` ([`sapcc/helm-charts`](https://github.com/sapcc/helm-charts/tree/master/system/metal-operator-remote-v2)), the operator-native rewrite that wraps the upstream `metal-operator` chart as a **subchart dependency**. It works against the operator today only because it vendors that subchart. Removing the vendoring requirement is what this change enables — it blocks production adoption of subchart-wrapping wrapper charts.

**Constraints in play:**
- Helm v3.21.3 (`helm.sh/helm/v3`). `downloader.Manager.Build()` operates on an unpacked chart **directory**, and silently falls back to `Update()` (semver re-negotiation) when `Chart.lock` is absent — with no flag to disable that fallback.
- Helm's registry client supports **one credential set per OCI hostname**; no per-dependency credentials ([helm/helm#11286](https://github.com/helm/helm/issues/11286)). The CR carries a single `authSecretRef`.
- The operator runs per-shoot in a Gardener `shoot--cp--*` namespace behind a `deny-all` NetworkPolicy; egress is label-gated (already required for the parent chart pull).
- The Phase 7.5 render cache keys on the resolved parent OCI digest and caches final manifests.

Full rationale: [`docs/subchart-dependency-resolution.md`](../../../docs/subchart-dependency-resolution.md) and `docs/context.md` Revision 8. This change realizes **Phase 7.6** of `docs/implementation.md`.

## Goals / Non-Goals

**Goals:**
- The Helm loader resolves declared `dependencies:` at pull time, so a wrapper chart can declare a plain `dependencies:` entry (with a committed `Chart.lock`) instead of vendoring subcharts.
- Resolution is **deterministic**: only the lock-driven `Build()` path runs; a missing/out-of-sync `Chart.lock` on a dependency-declaring chart fails the render closed with a clear error, never silently re-resolves via semver.
- The existing vendored-subchart path keeps working unchanged (backward compatible).
- A dependency-less chart is unaffected (cheap no-op).
- Change is confined to `internal/source` — no CRD change, no new transformation, no CR schema field.

**Non-Goals:**
- Subchart dependencies from classic HTTP(S) Helm repositories — explicitly rejected fail-closed (OCI-only support; see Decisions). Charts needing HTTP(S)-sourced subcharts must vendor them or republish via OCI.
- Per-dependency / multi-registry credentials (Helm does not support it; the OCI single-registry contract applies instead).
- Any kustomize-side change (`krusty` already resolves remote bases; verified — see Decisions).
- A separate source-fetch/artifact controller (Flux model) — out of proportion; the render cache already gives "resolve once per version".
- Changing the render cache key or layer.
- Authoring the `metal-operator-remote-v2` chart-side migration (drops vendoring) — that is a `sapcc/helm-charts` change, tracked separately.

## Capabilities

**Modified Capabilities:**
- `helm-chart-loader` — the production Helm `ChartLoader` (`internal/source/helmloader.go`) gains a dependency-resolution step in `Load()`: expand the pulled `.tgz`, enforce `Chart.lock` when `dependencies:` are declared, run `downloader.Manager.Build()`, then `loader.Load()` the directory. Behavior for dependency-less and pre-vendored charts is unchanged.

*(No new capabilities; no other capability's requirements change. The kustomize source loader is explicitly unaffected.)*

## Decisions

**Decision: Resolve dependencies at pull time via `downloader.Manager.Build()` in `Load()`**
- Chosen: expand the pulled archive (`chartutil.Expand`) to a directory, run `downloader.Manager{Out, ChartPath, Getters, RepositoryConfig, RepositoryCache, RegistryClient}.Build()`, then `loader.Load(chartDir)`.
- Reason: community-standard for server-side rendering — **Argo CD** resolves chart dependencies at sync (reconcile) time exactly this way. `downloader.Manager` operates on a directory, so the archive must be expanded first. ~35–40 LOC; reuses inputs already present in the pull path.
- Alternatives considered: (a) **keep requiring vendored subcharts** — rejected, it is the current defect; (b) **pre-resolve in a separate source-controller/artifact step** (Flux model) — rejected, this operator has no such layer and adding one is disproportionate.

**Decision: Enforce `Build()`-only semantics with a `Chart.lock` pre-check**
- Chosen: after expanding, parse the chart's `Chart.yaml`; if it declares a non-empty `dependencies:` list, require a `Chart.lock` in the chart directory and return a clear render error if it is absent — **before** calling `Build()`. Dependency-less charts skip the check; `Build()` is then a cheap no-op.
- Reason: Helm's `Build()` has no flag to disable its internal fallback to `Update()` when the lock is missing, and `Update()` re-negotiates semver against the live index (non-deterministic, silent subchart upgrades, extra network). The pre-check guarantees `Build()` only ever runs the deterministic lock-driven path. Committing `Chart.lock` is the accepted industry practice (like `package-lock.json` / `Cargo.lock`).
- Alternatives considered: **call `Build()` unconditionally and accept the `Update()` fallback** — rejected, reintroduces the non-determinism the design exists to prevent.

**Decision: Support OCI subchart repositories only; reject HTTP(S)-repo subchart deps fail-closed**
- Chosen: resolve subchart dependencies only when the dependency `repository` is an OCI reference (`oci://…`). If a chart declares a dependency whose `repository` is a classic HTTP(S) Helm repo, `Load` fails closed with a clear error before attempting `Build()`. OCI resolution reuses the parent pull's registry-client option set (cache, optional basic-auth from the CR's `authSecretRef`, the test `httpClient` seam).
- Reason: Helm's `downloader.Manager.Build()` resolves OCI (and `file://`) deps directly from the reference (`hasAllRepos` exempts them), but requires classic HTTP(S) dependency repos to be pre-registered in a `repositories.yaml` **and** have their index cached in the repository cache (`hasAllRepos` → `ErrRepoNotFound`, then `findChartURL` needs the cached index). The operator performs no such per-repo registration/caching at reconcile time, so an HTTP(S) subchart dep would fail with `ErrRepoNotFound` rather than resolve. Rather than add reconcile-time repo-registration machinery (network, a new failure mode) for a case the fleet does not use — every managed operator's charts live in the keppel **OCI** registry — the loader rejects HTTP(S) subchart deps explicitly. Single-credential auth (one set per OCI host, no per-dependency creds — helm/helm#11286) applies to the OCI path: subcharts must live on the same (or a public) OCI registry as the parent, or stay vendored.
- Alternatives considered: (a) **auto-register + cache each HTTP dep repo before `Build()`** (as the online test does) — rejected, adds reconcile-time code + network + a failure mode beyond the OCI-focused goal; (b) **document HTTP as a known limitation but attempt it anyway** — rejected, it would fail at runtime with an opaque `ErrRepoNotFound`; fail-closed with a clear message is better; (c) **extend the CR with per-dependency `authSecretRef`s** — rejected, Helm cannot consume them.

**Decision: No render-cache change (with a documented soundness qualifier)**
- Chosen: leave the cache key (resolved parent id + `repoScope` + `mode` + `inputHash` + `namespace`) and the `cachingSource` layer as-is. `Build()` runs inside `Load()`, which the cache only invokes on a miss — so subchart resolution runs at most once per resolved parent id.
- Reason: for the OCI path, `ResolveID` returns the parent chart's **manifest digest**, which covers the whole archive **including `Chart.lock`**. With `Chart.lock` mandated (pinned subchart versions), the resolved subcharts are a pure function of the parent digest → same digest → same render. No subchart dimension needed in the key.
- **Soundness qualifier (honest edge):** the cache is sound for subchart *content* only under the same **transitive-tag-mutability assumption already documented for kustomize in Phase 7.5** (upstream release tags don't move). Because subchart deps are OCI-only (HTTP(S)-repo deps are rejected), the parent chart's OCI digest covers `Chart.lock`, and subchart versions are pinned there. The one edge: `Chart.lock` pins subchart *versions*, and `Build()` re-fetches those versions from the OCI registry — a force-moved subchart tag could change bytes under an unchanged parent digest and the cache would serve the first render. Narrow (keppel OCI subchart versions are conventionally immutable; the LRU churns) and matches the pre-existing Phase 7.5 caveat rather than introducing a new class. Not blocking. (Note: the parent chart's own classic-HTTP-repo `ResolveID` version-fallback is a separate, pre-existing cache edge unrelated to subchart resolution and unchanged by this change.)
- Alternatives considered: **key the cache on parent-digest + resolved-subchart-digests** — rejected as unnecessary complexity for the OCI/digest path.

**Decision: Scratch/expansion space reuses the Phase 7.5 cache `emptyDir`; no new volume**
- Chosen: the loader continues to use an on-disk temp dir (not an in-memory pull — `downloader.Manager.Build()` requires an unpacked chart **directory** and writes fetched subcharts to `charts/` on disk). Point the temp base at the already-mounted Phase 7.5 cache volume when present (`os.MkdirTemp(cacheDir, "ddo-helm-")`), falling back to `os.TempDir()` when no volume is mounted (matching the existing loader fallback). Do NOT add a second `emptyDir`.
- Reason: Phase 7.6 adds `chartutil.Expand` + subchart downloads, roughly doubling the transient on-disk footprint per uncached render (still single-digit MB, cleaned up by the existing `defer os.RemoveAll(tmp)`). Phase 7.5 already ships an `emptyDir` at `/cache/source` (sizeLimit 640Mi) with a documented `os.TempDir()` fallback; reusing it keeps the ephemeral-storage story consolidated and bounded rather than fragmenting it across two volumes. A dedicated volume is unnecessary for correctness — the pod can write to its container layer today.
- **Required-vs-optional hinges on `readOnlyRootFilesystem`**: if chart 1's manager container sets `securityContext.readOnlyRootFilesystem: true`, the container-layer `/tmp` is not writable and `os.MkdirTemp("")` fails — then a writable mount (the reused cache `emptyDir`, or an `emptyDir` at the temp path) becomes **required**, not merely nice. If the root FS is writable, option (a) "do nothing, temp dir on the container layer" is also correct. The plan step MUST verify chart 1's securityContext and wire the temp base accordingly.
- Alternatives considered: (a) **in-memory pull** — rejected, `downloader.Manager` cannot run in memory and would force disk anyway while adding heap/OOM pressure under concurrency; (b) **a new dedicated `emptyDir` for Phase 7.6 scratch** — rejected, fragments the ephemeral-storage accounting Phase 7.5 deliberately consolidated.

**Decision: Kustomize path untouched (verified)**
- Chosen: no change to `internal/source/kustomize.go` or `gitresolver.go`.
- Reason: `krusty.Run` with `LoadRestrictionsNone` fetches remote bases itself during the build, and the git `RootResolver` is fail-closed on an unresolvable `?ref=`. Only Helm's `loader.Load()` is a pure unpack. The one kustomize nuance (transitive-tag mutability) is an already-documented Phase 7.5 cache-soundness caveat, not a rendering gap.

## Risks / Trade-offs

- [Reconcile-time network dependency on the subchart repo (new failure mode on cache miss)] → Covered by the Gardener egress labels already required for the parent pull (Phase 9); the render cache makes `Build()` run once per parent chart version, so steady state is unaffected. A subchart-repo outage surfaces as a per-resource render error on CR status (continue-on-error), same class as any pull failure.
- [Subchart in a different private registry cannot be authenticated (Helm single-host-credential limit)] → Documented chart-author contract: same/public registry, or keep vendoring. Not silently broken — `Build()` returns an auth error surfaced to CR status.
- [Missing/out-of-sync `Chart.lock` on a dependency-declaring chart] → Fail closed with a clear render error via the pre-check, rather than the silent `Update()` fallback. Chart authors must commit `Chart.lock` (documented).
- [`chartutil.Expand` adds a disk write + a nested-directory load per uncached render] → Negligible (temp dir, cleaned up via existing `defer os.RemoveAll(tmp)`); reconcile is 10-minute-scale and the render cache elides it on hits.
- [Refactoring the registry-client construction shared between `pullOCI` and the new `Build()` step could regress the existing OCI pull] → Cover with the existing hermetic OCI test plus the new subchart-resolution tests; keep the option set identical (cache, basic-auth, `httpClient` seam).

## Migration Plan

Deployment steps:
1. Land this operator change (loader gains the resolution step). Backward compatible — vendored and dependency-less charts render exactly as before, so no coordinated rollout is needed.
2. Ship the operator image + chart (existing Phase 9 / 9.5 pipeline). No CRD change, so no CRD re-apply.
3. **Separately, in `sapcc/helm-charts`:** once this has shipped, `metal-operator-remote-v2` may drop its vendored subchart and declare a plain `dependencies:` entry with a committed `Chart.lock`. Until then it keeps vendoring and works unchanged.

Rollback:
- Revert the operator change; vendored charts (including `-v2` while it still vendors) continue to render. Because the chart-side vendoring drop is gated on this shipping, there is no window where a chart depends on unreleased operator behavior.

## Open Questions

- [ ] None blocking. The `Chart.lock` requirement and single-registry-auth constraint are accepted chart-author contracts to be surfaced in the `metal-operator-remote-v2` migration note when it drops vendoring — owner: resolved in specs/plan and the helm-charts-side change.
