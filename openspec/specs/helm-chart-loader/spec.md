<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec: Helm Chart Loader

## Purpose

Defines the production `ChartLoader` implementation in `internal/source` that acquires a real Helm chart from either an OCI registry or a classic HTTP(S) repository. The loader satisfies the existing `ChartLoader.Load(ctx, repo, name, version) (*chart.Chart, error)` seam without changing the interface or the renderer logic.

## Requirements

### Requirement: Production Helm ChartLoader with scheme dispatch

The operator MUST provide a production `ChartLoader` implementation (`internal/source`) that satisfies the existing `ChartLoader.Load(ctx, repo, name, version) (*chart.Chart, error)` seam and acquires a real chart. `Load` MUST dispatch on the `repo` scheme:

- `oci://…` — pull via the Helm registry client + `action.Pull` (OCI ref `oci://<repo>/<name>`, pinned `Version`).
- `http://…` / `https://…` — pull a chart from a classic Helm repository via `action.Pull` with `RepoURL = repo`, resolving the chart by `Name` + `Version` against the repo index.

Both paths MUST produce a chart `.tgz` that is loaded via `loader.Load` into a `*chart.Chart`. The renderer logic and the `ChartLoader` interface signature MUST NOT change.

#### Scenario: OCI reference is pulled via the registry client

- **WHEN** `Load` is called with `repo = "oci://keppel.global.cloud.sap/ccloud-helm"`, `name = "metal-operator-remote"`, `version = "0.6.2"`
- **THEN** the loader pulls the chart via the Helm registry client at the OCI ref `oci://keppel.global.cloud.sap/ccloud-helm/metal-operator-remote` and pinned version `0.6.2`
- **AND** returns a parsed `*chart.Chart` for that chart

#### Scenario: classic HTTP(S) repo reference is pulled via RepoURL

- **WHEN** `Load` is called with `repo = "https://charts.example.com"`, a chart `name`, and a pinned `version`
- **THEN** the loader configures `action.Pull` with `RepoURL = "https://charts.example.com"` and resolves the chart by name + version against the repo index
- **AND** returns a parsed `*chart.Chart`

---

### Requirement: Non-nil registry client for anonymous OCI pulls

The loader MUST always supply a non-nil `*registry.Client` to the pull action for OCI pulls, even when no credentials are configured, because the Helm SDK panics on a nil `RegistryClient`. An anonymous OCI pull MUST succeed against a registry that serves charts anonymously, using a registry client constructed WITHOUT basic-auth.

#### Scenario: anonymous OCI pull against an anonymous registry

- **WHEN** `Load` pulls an `oci://` chart and no credentials resolve for the registry host
- **THEN** the loader constructs a `registry.Client` without basic-auth and assigns it to the pull action's registry client
- **AND** the pull succeeds without a login step and without writing credentials to disk

---

### Requirement: Pull to a temporary directory and clean up (loader-level, no chart caching)

The loader MUST pull each chart into a temporary directory, resolve its declared Helm dependencies (see "Subchart dependency resolution at pull time"), load it via `loader.Load` into a `*chart.Chart`, and remove the temporary directory after the chart is loaded. The loader itself MUST NOT cache charts — every `Load` fetches fresh into a fresh temporary directory. Render-result caching is a separate concern performed one layer above the loader by the `cachingSource` decorator (see the `render-cache` capability), keyed by the resolved OCI digest via the loader's `ResolveID` method (see the `source-id-resolution` capability); a cache hit skips the `Load` entirely, but any `Load` that does run still fetches fresh and cleans up. This is a layering statement, not a contradiction: the loader is stateless per call, while caching lives above it. Because dependency resolution runs inside `Load`, it is transparently covered by the render cache — resolution is paid at most once per resolved parent chart id and requires no change to the cache key.

#### Scenario: each Load fetches fresh and cleans up its temp dir

- **WHEN** `Load` is called for a chart
- **THEN** the loader pulls the chart `.tgz` into a temporary directory and loads it
- **AND** removes the temporary directory before returning
- **AND** does not retain the chart for a subsequent call (a second `Load` of the same `repo|name|version` pulls again)

#### Scenario: render-layer cache can skip Load without changing loader semantics

- **WHEN** the `cachingSource` decorator has a cached render for the resolved chart digest, mode, inputs, and namespace
- **THEN** `Load` is not invoked for that render
- **AND** WHEN the cache misses and `Load` is invoked, the loader still fetches fresh into a temporary directory and cleans up as specified

---

### Requirement: Subchart dependency resolution at pull time

The loader MUST resolve a chart's declared Helm dependencies at pull time so that a chart declaring `dependencies:` in `Chart.yaml` renders its subcharts without those subcharts being vendored under `charts/` in the pulled archive. After pulling the chart `.tgz` and before loading it, the loader MUST expand the archive to a chart directory and run Helm's `downloader.Manager.Build()` on that directory, then load the directory via `loader.Load`. A chart that declares no dependencies MUST be unaffected (the build step is a no-op), and a chart that already vendors its subcharts under `charts/` MUST continue to render those subcharts unchanged.

#### Scenario: chart declaring an unvendored dependency resolves its subchart

- **WHEN** `Load` is called for a chart whose `Chart.yaml` declares a `dependencies:` entry that is NOT vendored under `charts/`, and a committed `Chart.lock` pins that dependency
- **THEN** the loader expands the pulled archive, runs `downloader.Manager.Build()` to fetch the pinned subchart, and loads the resulting directory
- **AND** the returned `*chart.Chart` includes the resolved subchart so its templates render

#### Scenario: chart with vendored subcharts still renders unchanged

- **WHEN** `Load` is called for a chart that already contains its subchart vendored under `charts/`
- **THEN** the loader loads the chart with its vendored subchart present
- **AND** the returned `*chart.Chart` renders the subchart's objects exactly as before this capability existed

#### Scenario: chart with no dependencies is unaffected

- **WHEN** `Load` is called for a chart whose `Chart.yaml` declares no `dependencies:`
- **THEN** the dependency-build step is a no-op
- **AND** the returned `*chart.Chart` is identical to loading the pulled archive directly

---

### Requirement: Deterministic dependency resolution requires a committed Chart.lock

Dependency resolution MUST be deterministic: the loader MUST run only Helm's lock-driven build path and MUST NOT fall back to semver re-negotiation against a live repository index during a render. Because Helm's `downloader.Manager.Build()` silently falls back to `Update()` (semver re-negotiation) when no `Chart.lock` is present, the loader MUST, before invoking `Build()`, detect when the expanded chart's `Chart.yaml` declares a non-empty `dependencies:` list and require a `Chart.lock` in the chart directory. If a dependency-declaring chart has no `Chart.lock`, `Load` MUST fail closed with a clear error rather than resolve dependencies non-deterministically. Charts that declare dependencies MUST commit `Chart.lock`.

#### Scenario: missing Chart.lock on a dependency-declaring chart fails closed

- **WHEN** `Load` is called for a chart whose `Chart.yaml` declares a non-empty `dependencies:` list but the pulled archive contains no `Chart.lock`
- **THEN** `Load` returns a clear error identifying the missing lock
- **AND** the loader does NOT invoke `Build()`'s semver-re-negotiation fallback and does NOT silently under-render

#### Scenario: dependency-less chart does not require a lock

- **WHEN** `Load` is called for a chart whose `Chart.yaml` declares no `dependencies:` and no `Chart.lock` is present
- **THEN** the lock requirement does not apply
- **AND** `Load` proceeds and returns the parsed `*chart.Chart`

---

### Requirement: Subchart dependencies MUST use OCI repositories

Subchart dependency resolution is supported ONLY for dependencies whose `repository` is an OCI reference (`oci://…`). If a chart declares a dependency whose `repository` is a classic HTTP(S) Helm repository, `Load` MUST fail closed with a clear error rather than attempt resolution. Rationale: Helm's `downloader.Manager.Build()` resolves OCI (and `file://`) dependencies directly from the reference, but requires classic HTTP(S) dependency repositories to be pre-registered in a `repositories.yaml` and to have their index cached in the repository cache — setup the operator does not perform at reconcile time. Rather than resolve HTTP(S) subcharts non-deterministically or under-render silently, the loader rejects them. A chart whose subcharts must come from an HTTP(S) repository must either vendor those subcharts under `charts/` or republish them via an OCI registry.

The loader MUST authenticate OCI dependency resolution using the same single credential resolved for the parent chart pull (from the CR's one `authSecretRef`), reusing the parent pull's registry-client option set (registry cache, optional basic-auth, and the test HTTP-client seam). The loader MUST NOT attempt per-dependency credentials, because the Helm SDK supports only one credential set per registry host and provides no per-dependency credential mechanism.

#### Scenario: OCI subchart resolves with the parent credential

- **WHEN** `Load` resolves a dependency whose `repository` is an `oci://` reference
- **THEN** the loader uses the parent pull's credential/registry-client options for the dependency build
- **AND** the subchart resolves without any additional credential or repository configuration

#### Scenario: HTTP(S)-repository subchart dependency is rejected fail-closed

- **WHEN** `Load` processes a chart that declares a dependency whose `repository` is an `http://` or `https://` URL
- **THEN** `Load` returns a clear error identifying the unsupported HTTP(S) subchart repository
- **AND** the loader does NOT attempt resolution and does NOT silently under-render the parent chart
