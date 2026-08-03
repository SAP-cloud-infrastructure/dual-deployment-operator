# dual-deployment-operator — Subchart Dependency Resolution in the Helm Loader

**Status:** Accepted — scheduled as **Phase 7.6** (`internal/source` Helm loader). Blocks production usage of subchart-wrapping charts (`metal-operator-remote-v2`).
**Scope:** `internal/source` (Helm chart loader) — no CRD or transformation changes.
**Motivating consumer:** [`sapcc/helm-charts` `system/metal-operator-remote-v2`](https://github.com/sapcc/helm-charts/tree/master/system/metal-operator-remote-v2) — the operator-native rewrite of `metal-operator-remote`, which wraps the upstream `metal-operator` chart as a Helm **subchart** dependency.
**Related design:** see `design.md` §3.3 (source discriminator, Helm loader), `implementation.md` Phase 7.6, and `context.md` "Revision 8".
**Kustomize side:** unaffected — see §6. `krusty` already resolves remote bases during the build; only the Helm loader has the silent-under-render gap.

---

## TL;DR

The operator's Helm loader pulls a chart archive and calls `loader.Load()` on it directly. It does **not** run Helm's dependency resolution (`helm dependency build/update`). Any chart that declares a dependency in `Chart.yaml` therefore only works if its subcharts are **already vendored** inside the pulled `.tgz` (in `charts/`). Charts published without vendored subcharts render with the subchart silently absent.

This change adds a dependency-build step to the loader so charts can declare `dependencies:` in `Chart.yaml` and have the operator resolve them at pull time — removing the "publish a fat `.tgz` with `charts/*.tgz` vendored" requirement. It is **~35–40 lines of Go in `internal/source/helmloader.go`**, no CRD change, no new transformation.

This is the **community-standard approach** for server-side chart rendering: Argo CD resolves chart dependencies at sync time the same way (invoking the Helm SDK's `downloader.Manager` per render), whereas Flux pre-resolves in a separate `source-controller` artifact step it does not have an analogue for here. Running `downloader.Manager.Build()` at reconcile time is therefore the aligned choice; the render cache (Phase 7.5, keyed on resolved OCI digest) is what keeps its network/latency cost to once per chart version.

**Scheduled as Phase 7.6.** The first consumer (`metal-operator-remote-v2`) ships with its subchart **vendored** so it works against the operator as it exists today; this change lets that chart (and future ones) drop vendoring. It is a **prerequisite** for `-v2` to declare a plain `dependencies:` entry.

---

## 1. Background

### 1.1 How the loader works today

`internal/source/helmloader.go` `Load()` pulls the chart archive (OCI or HTTP(S)) into a temp dir and loads it:

```go
switch {
case strings.HasPrefix(repoURL, "oci://"):
    chartPath, err = l.pullOCI(ctx, repoURL, name, version, tmp)
case strings.HasPrefix(repoURL, "http://"), strings.HasPrefix(repoURL, "https://"):
    chartPath, err = l.pullHTTP(ctx, repoURL, name, version, tmp)
...
}
...
return loader.Load(chartPath)
```

`loader.Load()` reads whatever is in the archive. If the chart's `Chart.yaml` declares `dependencies:` but the archive does not contain the corresponding `charts/<dep>-<ver>.tgz`, Helm's loader does **not** fetch them — the subchart's templates simply do not render. There is no error; the objects the subchart would have produced are silently missing.

### 1.2 Why this matters for `metal-operator-remote-v2`

`metal-operator-remote-v2` is a thin wrapper: it declares the upstream `metal-operator` chart as a subchart dependency and drives its `*.enable` flags per render mode via `seedValues`/`shootValues`. For that to work through the operator, the upstream chart must be present at render time. Two ways to guarantee that:

1. **Vendor it** — commit/publish `charts/metal-operator-<ver>.tgz` inside the wrapper's own `.tgz`. Works with the operator as-is. This is what `-v2` does today.
2. **Resolve it** — let the operator run Helm dependency resolution at pull time so a plain `dependencies:` entry is enough. Requires this enhancement.

Vendoring works but couples every publish to a `helm dependency build` step and bloats the artifact. Resolution makes the wrapper chart a normal Helm chart with a declared dependency.

---

## 2. Problem

The loader has no dependency-resolution phase. Charts that declare dependencies but do not vendor them under-render silently. The operator thus imposes an implicit, undocumented contract on chart authors: **vendor all subcharts into the published archive**. This is easy to violate (a chart that renders fine under local `helm template` after a `helm dependency build` will under-render through the operator if the vendored `charts/` were not committed/published).

---

## 3. Proposed change

Add an opt-in dependency-build step to `internal/source/helmloader.go` `Load()`, inserted **before** `loader.Load()`. Because Helm's `downloader.Manager` operates on an unpacked chart **directory** (not an in-memory `chart.Chart`), the archive must be expanded first, dependencies resolved on the directory, then the directory loaded:

```go
// after chartPath is the pulled .tgz:
unpackedDir := filepath.Join(tmp, "unpacked")
if err := os.MkdirAll(unpackedDir, 0o755); err != nil {
    return nil, err
}
f, err := os.Open(chartPath)
if err != nil {
    return nil, err
}
defer f.Close()
if err := chartutil.Expand(unpackedDir, f); err != nil {
    return nil, err
}
chartDir := filepath.Join(unpackedDir, name)

rc, err := registry.NewClient(/* reuse the same options as pullOCI */)
if err != nil {
    return nil, err
}
m := &downloader.Manager{
    Out:              io.Discard,
    ChartPath:        chartDir,
    Getters:          getter.All(l.settings),
    RepositoryConfig: l.settings.RepositoryConfig,
    RepositoryCache:  l.settings.RepositoryCache,
    RegistryClient:   rc,
}
if err := m.Build(); err != nil {
    return nil, fmt.Errorf("source: build dependencies: %w", err)
}
return loader.Load(chartDir)
```

Estimated size: **~35–40 lines**. The inputs (`l.settings`, `getter.All`, a registry client) already exist in `helmloader.go`'s pull path and can be reused/refactored.

Gating: run `Manager.Build()` **unconditionally** — a chart with no `dependencies:` is a cheap no-op, so no `Chart.yaml`-presence guard is needed.

**`Build()` vs `Update()` — the determinism trap (verified against Helm v3.21.3 `pkg/downloader/manager.go`).** `Manager.Build()` reconstructs `charts/` from a committed `Chart.lock`, downloading the **exact** pinned versions with no semver re-negotiation. But if `Chart.lock` is **absent**, `Build()` does not fail — it silently falls back to `Update()`, which reads `Chart.yaml`, negotiates any semver ranges against the live repo index, downloads the resolved versions, and regenerates the lock. That fallback is a non-deterministic, network-heavy reconcile path: a `^1.0.0` range would silently pull a newer subchart the moment upstream publishes one. **This change calls `Build()` only** (never `Update()`) and treats a missing lock as a chart-author error surfaced to CR status, rather than letting the reconcile silently re-resolve. Charts using dependencies **MUST commit `Chart.lock`** — the accepted industry practice (analogous to `package-lock.json` / `Cargo.lock`). A lock that is out of sync with `Chart.yaml` makes `Build()` error rather than under-render, which is the desired fail-closed behavior.

---

## 4. Risks

1. **Single-registry auth (Helm limitation, not ours).** The CR carries one `authSecretRef` for pulling the chart. Helm's registry client supports **one credential set per OCI hostname** and there is **no per-dependency credential** mechanism (no `username`/`password` on a `dependencies:` entry) — [helm/helm#11286](https://github.com/helm/helm/issues/11286). So if a subchart lives in a **different** private registry than the parent, `Manager.Build()` cannot authenticate to it. Contract for chart authors: subcharts must reside on the **same** (or a public) registry as the parent — reusing the parent's `authSecretRef` — or be pre-vendored under `charts/` if they need distinct private credentials.
2. **Network at reconcile time.** Resolving dependencies requires egress from the operator Pod to the subchart repo on every **uncached** render. This needs the Gardener egress labels already required for the chart pull (Phase 9) and adds a failure mode (subchart repo unreachable → render fails) that vendoring does not have. This matches Argo CD's sync-time model; the render cache is what makes it acceptable.
3. **`Chart.lock` required.** See §3 — this change calls `Build()` only and never falls back to `Update()`; a missing/out-of-sync lock is a fail-closed error, so charts using dependencies MUST commit `Chart.lock` for deterministic, reproducible renders.

The render cache (Phase 7.5, keyed on the resolved content id / OCI digest) mitigates (2)/(3) for steady state — `Build()` runs once per unique chart version — but the first render per content id still pays the network cost.

---

## 5. Decision framing

- **Accepted and scheduled as Phase 7.6** — a self-contained `internal/source` change (no CRD, no transformation). It blocks production usage of subchart-wrapping charts, so it is not deferrable indefinitely.
- The approach (reconcile-time `downloader.Manager.Build()`) is the **community-standard** one for server-side rendering — Argo CD does exactly this at sync time. No divergence from best practice; the render cache bounds its cost.
- Sequencing vs. the helm-charts side: this operator change lands **first** (prerequisite); `metal-operator-remote-v2` then chooses vendored-`.tgz` (safe default, works today) or a plain `dependencies:` + committed `Chart.lock` entry once this has shipped.

Rationale and evolution are recorded in `context.md` **Revision 8**.

---

## 6. Kustomize side — not affected (verified)

The analogous "silently under-renders a declared dependency" gap does **not** exist on the kustomize path, and the difference is structural, not accidental:

- **`krusty` resolves remote bases itself during the build.** [`internal/source/kustomize.go`](../internal/source/kustomize.go) runs `krusty.MakeKustomizer(opts).Run(fSys, root)` with `LoadRestrictions = LoadRestrictionsNone`. When a `kustomization.yaml` references a remote base by URL (e.g. ipam-capi's `github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster//config/*?ref=v1.1.0`), krusty fetches it as part of the build. There is no separate "vendor or it vanishes" step — dependency resolution is built into the render engine.
- **The git `RootResolver` only supplies the root and is fail-closed.** [`internal/source/gitresolver.go`](../internal/source/gitresolver.go) `Resolve` fetches the pinned `?ref=` root; an unresolvable ref returns an error (`"could not resolve ref … (fail-closed)"`), never a silent empty build.
- **Contrast with Helm.** Helm's `loader.Load()` is a pure unpack — it reads whatever `charts/` contains and never fetches declared-but-absent `dependencies:`. That is exactly the gap this change closes with `downloader.Manager.Build()`. Kustomize's `krusty.Run` is fetch-and-build in one, so the equivalent step already runs.

The one kustomize nuance is **transitive-tag mutability** (a force-moved upstream tag like `?ref=v1.1.0` could change what renders under the same root SHA) — but that is a cache-soundness caveat already documented in Phase 7.5, not a silent-under-render defect. No new fetch step is needed on the kustomize side.
