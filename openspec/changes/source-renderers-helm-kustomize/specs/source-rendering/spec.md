# Spec: Source Rendering

## Purpose

Defines the `internal/source` package: the `Source` interface and `Mode` type, the `From()` discriminator factory, and the two concrete renderers (Helm via `helm.sh/helm/v3`, kustomize via `sigs.k8s.io/kustomize/api/krusty`). Each renderer turns a `DualDeploymentOperator` `spec.source` into an origin-tagged manifest stream for a given mode (`host` or `remote`), per the two-render pattern. Chart and kustomize-root acquisition are behind pluggable interfaces so rendering is unit-testable without network access.

## ADDED Requirements

### Requirement: Source interface and Mode

The `internal/source` package SHALL define a `Mode` string type with exactly two constants — `ModeHost` (value `"host"`) and `ModeRemote` (value `"remote"`) — and a `Source` interface with a single method `Render(ctx context.Context, mode Mode) ([]manifest.Manifest, error)`. A `Source` implementation MUST return the manifest stream for the requested mode only, tagged with `Origin` per the manifest-parsing capability.

#### Scenario: Mode constants have stable values

- **WHEN** the `Mode` constants are compared as strings
- **THEN** `ModeHost` equals `"host"`
- **AND** `ModeRemote` equals `"remote"`

#### Scenario: Render returns a manifest stream for a mode

- **WHEN** `Render` is called with a valid `Mode`
- **THEN** it returns a slice of `manifest.Manifest` for that mode
- **AND** each returned manifest carries an `Origin`

---

### Requirement: Source discriminator factory

The package SHALL provide a `From` factory that constructs a `Source` from a `v1alpha1.Source` spec plus its injectable dependencies (a `ChartLoader` for Helm, a `RootResolver` for kustomize). `From` MUST return a Helm renderer when exactly `spec.helm` is set, a kustomize renderer when exactly `spec.kustomize` is set, and MUST return an error when neither or both discriminator fields are set.

#### Scenario: Helm source selected

- **WHEN** `From` receives a spec with `helm` set and `kustomize` unset
- **THEN** it returns a Helm-backed `Source`
- **AND** returns no error

#### Scenario: Kustomize source selected

- **WHEN** `From` receives a spec with `kustomize` set and `helm` unset
- **THEN** it returns a kustomize-backed `Source`
- **AND** returns no error

#### Scenario: Neither discriminator set is rejected

- **WHEN** `From` receives a spec with both `helm` and `kustomize` unset
- **THEN** it returns an error indicating exactly one source type must be set

#### Scenario: Both discriminators set is rejected

- **WHEN** `From` receives a spec with both `helm` and `kustomize` set
- **THEN** it returns an error indicating exactly one source type must be set

---

### Requirement: Pluggable chart acquisition

The package SHALL define a `ChartLoader` interface with a method `Load(ctx context.Context, repo, name, version string) (*chart.Chart, error)`. The Helm renderer MUST obtain its chart exclusively through the injected `ChartLoader` and MUST NOT embed chart pull/registry logic directly. The package MUST provide a production implementation that pulls from OCI/HTTP Helm repositories, and MUST allow a test fake that loads a chart from a local directory.

#### Scenario: Renderer loads chart via injected loader

- **WHEN** the Helm renderer renders a source
- **THEN** it calls the injected `ChartLoader.Load` with the spec's `repo`, `name`, and `version`
- **AND** renders the chart returned by the loader

#### Scenario: Fake loader enables offline rendering

- **WHEN** a test injects a fake `ChartLoader` that returns a chart loaded from a local directory
- **THEN** the Helm renderer produces manifests without any network access

---

### Requirement: Helm values merge and mode injection

The Helm renderer SHALL merge values in this precedence order (lowest to highest): the chart's own `values.yaml` defaults, `spec.helm.values` (common), the mode-specific overrides (`spec.helm.hostValues` for host or `spec.helm.remoteValues` for remote), and finally an operator-injected `{mode: <mode>}` at the highest precedence. The renderer MUST inject `mode` itself and MUST reject rendering with an error if the merged user-supplied values already set a top-level `mode` key.

#### Scenario: Mode injected at highest precedence for host

- **WHEN** the Helm renderer renders in `ModeHost`
- **THEN** the effective values contain `mode: host`
- **AND** `spec.helm.hostValues` are merged above `spec.helm.values`

#### Scenario: Mode injected at highest precedence for remote

- **WHEN** the Helm renderer renders in `ModeRemote`
- **THEN** the effective values contain `mode: remote`
- **AND** `spec.helm.remoteValues` are merged above `spec.helm.values`

#### Scenario: User-supplied mode rejected

- **WHEN** the Helm renderer is asked to render a spec whose merged values already set a top-level `mode` key
- **THEN** the renderer returns an error indicating `mode` is operator-controlled and must not be set by the user

---

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

#### Scenario: Host and remote renders differ by mode

- **WHEN** the Helm renderer renders the same source once in `ModeHost` and once in `ModeRemote`
- **THEN** each render contains only the resources its mode guards enable
- **AND** the two manifest sets are the disjoint host/remote sets the chart defines for each mode

---

### Requirement: Pluggable kustomize root acquisition

The package SHALL define a `RootResolver` interface that resolves a kustomize root for a given base URL and mode subpath into a filesystem path the kustomizer can build, returning a cleanup function. The kustomize renderer MUST obtain its build root exclusively through the injected `RootResolver` and MUST NOT embed remote git-fetch logic directly. The package MUST provide a production implementation that resolves the `?ref=`-pinned remote URL, and MUST allow a test fake that resolves to a local overlay directory.

#### Scenario: Renderer resolves root via injected resolver

- **WHEN** the kustomize renderer renders a source in a given mode
- **THEN** it calls the injected `RootResolver` with the spec's `url` and the mode's subpath
- **AND** builds the resolved filesystem root

#### Scenario: Fake resolver enables offline rendering

- **WHEN** a test injects a fake `RootResolver` that resolves to a local overlay directory
- **THEN** the kustomize renderer produces manifests without any network access

---

### Requirement: Kustomize overlay selection by mode

The kustomize renderer SHALL select the overlay subpath by mode: `spec.kustomize.hostPath` for `ModeHost` and `spec.kustomize.remotePath` for `ModeRemote`. It MUST build the resolved root with `krusty` and MUST parse the build output into `manifest.Manifest` values via the manifest-parsing capability with a fallback origin of `OriginUpstream`.

#### Scenario: Host mode builds the host overlay

- **WHEN** the kustomize renderer renders in `ModeHost`
- **THEN** it resolves and builds the root at the `hostPath` subpath
- **AND** returns the manifests that overlay selects

#### Scenario: Remote mode builds the remote overlay

- **WHEN** the kustomize renderer renders in `ModeRemote`
- **THEN** it resolves and builds the root at the `remotePath` subpath
- **AND** returns the manifests that overlay selects

#### Scenario: Kustomize additions carry additions origin

- **WHEN** the kustomize renderer builds an overlay whose `additions/` resources set `dual-deployment-operator.cc.sap/origin: additions` via commonAnnotations
- **THEN** those manifests have `Origin` of `OriginAdditions`
- **AND** upstream-referenced resources have `Origin` of `OriginUpstream`

---

### Requirement: Source rendering test coverage

The source-rendering and manifest-parsing capabilities SHALL be verified by table-driven unit tests that run offline using the fake `ChartLoader` and fake `RootResolver` against local testdata fixtures (a real small Helm chart and kustomize overlays). These tests MUST NOT require a Kubernetes cluster; cluster-level and equivalence testing are deferred to later phases (reconciler and equivalence phases).

#### Scenario: Renderers verified offline against fixtures

- **WHEN** the source-rendering unit tests run in CI
- **THEN** they exercise both the Helm and kustomize renderers using local fixtures and fake fetchers
- **AND** they assert host vs remote disjoint sets, correct origin tags, mode injection, user-`mode` rejection, and CRD inclusion
- **AND** they complete without contacting any network endpoint or Kubernetes cluster
