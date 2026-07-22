<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## MODIFIED Requirements

### Requirement: Source interface and Mode

The `internal/source` package SHALL define a `Mode` string type with exactly two constants — `ModeSeed` (value `"seed"`) and `ModeShoot` (value `"shoot"`) — and a `Source` interface with a single method `Render(ctx context.Context, mode Mode, namespace string) ([]manifest.Manifest, error)`. A `Source` implementation MUST return the manifest stream for the requested mode only, tagged with `Origin` per the manifest-parsing capability, with namespaced resources placed in the given `namespace` per the target-namespace requirement.

#### Scenario: Mode constants have stable values

- **WHEN** the `Mode` constants are compared as strings
- **THEN** `ModeSeed` equals `"seed"`
- **AND** `ModeShoot` equals `"shoot"`

#### Scenario: Render returns a manifest stream for a mode

- **WHEN** `Render` is called with a valid `Mode` and a target namespace
- **THEN** it returns a slice of `manifest.Manifest` for that mode
- **AND** each returned manifest carries an `Origin`

### Requirement: Helm values merge and mode injection

The Helm renderer SHALL merge values in this precedence order (lowest to highest): the chart's own `values.yaml` defaults, `spec.helm.values` (common), the mode-specific overrides (`spec.helm.seedValues` for seed or `spec.helm.shootValues` for shoot), and finally an operator-injected `{mode: <mode>}` at the highest precedence. The renderer MUST inject `mode` itself and MUST reject rendering with an error if the merged user-supplied values already set a top-level `mode` key.

#### Scenario: Mode injected at highest precedence for seed

- **WHEN** the Helm renderer renders in `ModeSeed`
- **THEN** the effective values contain `mode: seed`
- **AND** `spec.helm.seedValues` are merged above `spec.helm.values`

#### Scenario: Mode injected at highest precedence for shoot

- **WHEN** the Helm renderer renders in `ModeShoot`
- **THEN** the effective values contain `mode: shoot`
- **AND** `spec.helm.shootValues` are merged above `spec.helm.values`

#### Scenario: User-supplied mode rejected

- **WHEN** the Helm renderer is asked to render a spec whose merged values already set a top-level `mode` key
- **THEN** the renderer returns an error indicating `mode` is operator-controlled and must not be set by the user

### Requirement: Helm rendering includes CRDs

The Helm renderer SHALL render chart templates in client-only dry-run mode (no cluster interaction) and MUST include CRDs in the rendered manifest stream. Rendered output MUST be parsed into `manifest.Manifest` values via the manifest-parsing capability with a fallback origin of `OriginUpstream`.

#### Scenario: CRDs present in rendered stream

- **WHEN** the Helm renderer renders a chart that ships CRDs
- **THEN** the returned manifest stream includes the chart's CustomResourceDefinition objects

#### Scenario: Upstream resources default to upstream origin

- **WHEN** the Helm renderer renders a chart whose upstream subchart resources carry no origin annotation
- **THEN** those manifests have `Origin` of `OriginUpstream`

#### Scenario: Chart-authored additions carry additions origin

- **WHEN** the Helm renderer renders a chart whose own templates set the `dual-deployment-operator.cc.sap/origin: additions` annotation
- **THEN** those manifests have `Origin` of `OriginAdditions`

#### Scenario: Seed and shoot renders differ by mode

- **WHEN** the Helm renderer renders the same source once in `ModeSeed` and once in `ModeShoot`
- **THEN** each render contains only the resources its mode guards enable
- **AND** the two manifest sets are the disjoint seed/shoot sets the chart defines for each mode

### Requirement: Kustomize overlay selection by mode

The kustomize renderer SHALL select the overlay subpath by mode: `spec.kustomize.seedPath` for `ModeSeed` and `spec.kustomize.shootPath` for `ModeShoot`. It MUST build the resolved root with `krusty` and MUST parse the build output into `manifest.Manifest` values via the manifest-parsing capability with a fallback origin of `OriginUpstream`.

#### Scenario: Seed mode builds the seed overlay

- **WHEN** the kustomize renderer renders in `ModeSeed`
- **THEN** it resolves and builds the root at the `seedPath` subpath
- **AND** returns the manifests that overlay selects

#### Scenario: Shoot mode builds the shoot overlay

- **WHEN** the kustomize renderer renders in `ModeShoot`
- **THEN** it resolves and builds the root at the `shootPath` subpath
- **AND** returns the manifests that overlay selects

#### Scenario: Kustomize additions carry additions origin

- **WHEN** the kustomize renderer builds an overlay whose `additions/` resources set `dual-deployment-operator.cc.sap/origin: additions` via commonAnnotations
- **THEN** those manifests have `Origin` of `OriginAdditions`
- **AND** upstream-referenced resources have `Origin` of `OriginUpstream`

### Requirement: Target namespace application

Both renderers SHALL place the render's output into the `namespace` argument passed to `Render`, using fallback semantics: a **namespaced** resource whose `metadata.namespace` is empty MUST be assigned the given `namespace`; a resource that already declares a `metadata.namespace` MUST be left unchanged; a **cluster-scoped** resource (for example `CustomResourceDefinition`, `ClusterRole`, `ClusterRoleBinding`, `Namespace`) MUST never be assigned a namespace. The Helm renderer MUST apply the namespace by setting the install action's `Namespace` (driving `.Release.Namespace` and Helm's fallback stamp). The kustomize renderer MUST apply the namespace as a post-build step over the rendered stream. Namespaced-vs-cluster-scoped classification MAY use a static list of well-known cluster-scoped kinds while rendering is offline. The operator MUST NOT hardcode a fixed namespace such as `"default"`.

#### Scenario: Namespaced resource without a namespace gets the target namespace

- **WHEN** a renderer renders a namespaced resource that omits `metadata.namespace`
- **AND** `Render` is called with target namespace `N`
- **THEN** the resulting manifest's `metadata.namespace` is `N`

#### Scenario: Explicit namespace is preserved

- **WHEN** a renderer renders a resource that already sets `metadata.namespace` to `X`
- **AND** `Render` is called with a different target namespace `N`
- **THEN** the resulting manifest's `metadata.namespace` remains `X`

#### Scenario: Cluster-scoped resource is never namespaced

- **WHEN** a renderer renders a cluster-scoped resource (e.g. a `CustomResourceDefinition`)
- **AND** `Render` is called with target namespace `N`
- **THEN** the resulting manifest has no `metadata.namespace`

#### Scenario: Seed uses CR namespace, shoot uses shootNamespace

- **WHEN** the reconciler renders seed mode with the CR's own namespace and shoot mode with `spec.shootNamespace`
- **THEN** seed-render namespaced resources lacking a namespace land in the CR's namespace
- **AND** shoot-render namespaced resources lacking a namespace land in `spec.shootNamespace`

### Requirement: Source rendering test coverage

The source-rendering and manifest-parsing capabilities SHALL be verified by table-driven unit tests that run offline using the fake `ChartLoader` and fake `RootResolver` against local testdata fixtures (a real small Helm chart and kustomize overlays). These tests MUST NOT require a Kubernetes cluster; cluster-level and equivalence testing are deferred to later phases (reconciler and equivalence phases).

#### Scenario: Renderers verified offline against fixtures

- **WHEN** the source-rendering unit tests run in CI
- **THEN** they exercise both the Helm and kustomize renderers using local fixtures and fake fetchers
- **AND** they assert seed vs shoot disjoint sets, correct origin tags, mode injection, user-`mode` rejection, CRD inclusion, and target-namespace application (namespaced resources stamped, explicit namespaces preserved, cluster-scoped resources untouched)
- **AND** they complete without contacting any network endpoint or Kubernetes cluster
