## Design Summary

Add Helm subchart dependency resolution to the operator's Helm chart loader (`internal/source/helmloader.go`, Phase 7.6). Today `Load()` pulls the chart `.tgz` and calls `loader.Load()` directly — a pure unpack that renders declared-but-unvendored `dependencies:` **silently absent**. This blocks production usage of subchart-wrapping charts such as `metal-operator-remote-v2`, which wraps the upstream `metal-operator` chart as a subchart. The change expands the pulled archive and runs Helm's `downloader.Manager.Build()` on the chart directory before loading, so a wrapper chart can declare a plain `dependencies:` entry (with a committed `Chart.lock`) instead of vendoring `charts/*.tgz`. Scope is `internal/source` only — no CRD change, no new transformation. Two constraints are documented chart-author contracts, not operator features: subcharts must live on the same (or a public) registry as the parent (Helm allows one credential set per OCI host), and dependency-declaring charts must commit `Chart.lock`.

## Alternatives Considered

### Option A: Reconcile-time `downloader.Manager.Build()` in the loader (CHOSEN)
- **Approach**: Expand the pulled `.tgz` (`chartutil.Expand`), run `downloader.Manager{...}.Build()` on the unpacked directory, then `loader.Load(dir)`. Runs on every uncached render; the Phase 7.5 render cache (keyed on resolved OCI digest) makes it run once per chart version.
- **Pros**: Community-standard for server-side rendering (Argo CD resolves deps at sync time identically); ~35–40 LOC; reuses the existing pull-path inputs (`l.settings`, `getter.All`, registry client); no CRD/transformation change; lets wrapper charts drop vendoring.
- **Cons**: Adds a reconcile-time network dependency to the subchart repo on cache miss; inherits Helm's single-credential-per-host limit; requires chart authors to commit `Chart.lock`.
- **Why chosen**: It is the aligned, minimal change that closes the silent-under-render gap. See Agreed Approach.

### Option B: Keep requiring vendored subcharts (status quo)
- **Approach**: Do nothing in the operator; chart authors must run `helm dependency build` and publish a fat `.tgz` with `charts/*.tgz` committed.
- **Pros**: Zero operator code; no reconcile-time network for deps; fully deterministic (subcharts are physically present).
- **Cons**: Imposes an undocumented, easy-to-violate contract on every publish; a chart that renders fine under local `helm template` under-renders through the operator if `charts/` was not published; bloats artifacts.
- **Why not chosen**: It is the current defect. `metal-operator-remote-v2` works only because it vendors; the goal is to let it stop.

### Option C: Pre-resolve dependencies in a separate fetch/artifact step (Flux model)
- **Approach**: Split source fetching from reconcile — a separate step resolves + packages the chart-with-deps into an artifact the reconcile consumes (as Flux's `source-controller` does).
- **Pros**: Removes dependency resolution from the hot reconcile path; strong caching story.
- **Cons**: This operator has **no** separate source-controller/artifact layer; introducing one is a large architectural change far beyond a loader tweak.
- **Why not chosen**: Disproportionate; the render cache already gives the "resolve once per version" benefit without a new subsystem.

## Agreed Approach

**Option A** — insert a `downloader.Manager.Build()` step into `helmLoader.Load()` before `loader.Load()`. This is the community-standard reconcile-time resolution model (verified: Argo CD resolves chart dependencies at sync time the same way; Flux's pre-resolve model has no analogue here). Grounded in a community-practice check against Helm v3.21.3 — no divergence from best practice. The Phase 7.5 render cache (keyed on the resolved parent OCI digest) already covers this correctly: `Build()` runs inside `Load()`, before rendering, so a given parent digest yields the same subchart resolution (subcharts pinned by `Chart.lock`) and the same cached render — resolution cost is paid once per parent chart version. The kustomize path is unaffected and needs no equivalent step.

## Key Decisions

- **Pre-check `Chart.lock`, then `Build()` (Build()-only semantics)**: Helm's `Manager.Build()` has no flag to disable its internal fallback to `Update()` when `Chart.lock` is absent — and that fallback re-negotiates semver against the live index (non-deterministic, silent subchart upgrades). So the loader parses the expanded chart's `Chart.yaml`; if it declares a non-empty `dependencies:` list, the loader requires a `Chart.lock` in the chart dir and returns a clear render error if it is missing, **before** calling `Build()`. This guarantees `Build()` only ever runs the deterministic lock-driven path. Dependency-less charts skip the check and `Build()` is a cheap no-op.
- **Single-registry auth is a documented contract, not worked around**: Helm's registry client supports one credential set per OCI hostname and has no per-dependency credentials ([helm/helm#11286](https://github.com/helm/helm/issues/11286)). Subcharts must reside on the same (or a public) registry as the parent (reusing the CR's one `authSecretRef`), or stay vendored. The loader reuses the parent pull's registry-client option set (cache, optional basic-auth, the test `httpClient` seam) for the `downloader.Manager`.
- **Unconditional `Build()` call for dependency-less charts**: no `Chart.yaml`-presence guard around the `Build()` invocation itself; the lock pre-check is the only gate, and a chart with no `dependencies:` is a cheap no-op through `Build()`.
- **Reuse the existing pull-path inputs**: `l.settings`, `getter.All(l.settings)`, `RepositoryConfig`/`RepositoryCache`, and the OCI registry client already exist in the Phase 7 pull path — the change refactors them into a shared helper rather than duplicating.
- **Cache interaction needs no change**: the render cache keys on the resolved parent digest and caches final manifests; because `Build()` runs before render inside `Load()`, resolution is transparently covered — no new cache key, no cache-layer edit.
- **Kustomize path untouched (verified)**: `krusty.Run` with `LoadRestrictionsNone` fetches remote bases during the build and the git `RootResolver` is fail-closed on an unresolvable `?ref=`; only Helm's `loader.Load()` is a pure unpack. No kustomize code change.

## Open Questions

- [ ] None blocking. The single-registry-auth constraint and `Chart.lock` requirement are accepted chart-author contracts (to be surfaced in the chart-side docs / `metal-operator-remote-v2` migration note when it drops vendoring) rather than operator behavior to decide. — owner: resolved during specs/plan
