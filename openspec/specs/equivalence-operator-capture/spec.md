<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec: Equivalence Operator Capture

## Purpose

Defines how the equivalence harness in `internal/equivalence` produces the operator-side manifest streams by driving the operator's own render and transform pipeline directly, without exercising the delivery layer.

## Requirements

### Requirement: Operator render+transform capture

The equivalence harness SHALL produce the operator-side manifest streams by driving
the operator's own render and transform pipeline directly — `source.From` →
`Render(ModeSeed, seedNamespace)` and `Render(ModeShoot, shootNamespace)` →
`transform.Build(cr.Spec.Transformations)` → applying each built transformation to
both renders in declaration order — and capturing the resulting seed and shoot
`[]manifest.Manifest` before the delivery layer. The harness MUST NOT run the
reconciler, envtest, or a real/fake delivery applier to obtain these streams.

#### Scenario: Two streams captured from the pipeline

- **WHEN** the harness captures the operator output for a fixture CR
- **THEN** it invokes the render+transform pipeline twice (seed and shoot modes) and
  returns two manifest streams captured before any delivery-layer mutation

#### Scenario: Delivery layer is not exercised

- **WHEN** the harness captures the operator output
- **THEN** it does not invoke the reconciler, envtest, or an SSA/fake applier — only
  `source.From`, `Render`, `transform.Build`, and the transformations' `Apply`

#### Scenario: Transformation errors surface as capture failures

- **WHEN** building or applying a transformation returns an error during capture
- **THEN** the harness returns that error and the test FAILS, surfacing the
  operator-side error

---

### Requirement: Fixture-driven capture inputs

The harness SHALL take the operator-side inputs from a per-operator fixture
comprising the CR and its source values, derived from a single pinned per-operator
**value baseline** that also drives the golden render (see the golden-render
capability). The value baseline is the wrapper chart's own `values.yaml` plus one
named, representative per-cluster overlay (the `cc/kube-secrets`-shaped values for a
single chosen shoot, e.g. `m-qa-de-1`), recorded in the fixture. The fixture MUST
express the operator's configuration (`spec.source.helm` `values`/`seedValues`/
`shootValues`, or `spec.source.kustomize` with `patch` transformations) so it
reproduces, on the operator side, the same effective input the value baseline feeds
the golden render.

The fixture MUST account for the fact that the operator renders the **upstream**
chart directly (e.g. `metal-operator-core` from `oci://ghcr.io/ironcore-dev/charts`),
whereas the golden side renders the **wrapper** chart (`<operator>-remote`) which
disables the upstream subchart and injects pre-rendered equivalents. The CR values
MUST therefore bridge the deliberate configuration differences between the two
renders — for example enabling upstream `rbac`/`crd`/`webhook` on the operator side
(which the wrapper disables and pre-renders), and carrying the same per-cluster
values (e.g. the apiserver host) into both renders. Fields produced by this bridging
are compared, not allowlisted.

#### Scenario: Both sides derive from one pinned value baseline

- **WHEN** an operator's equivalence test runs
- **THEN** the operator render (CR values) and the golden render (`helm template` of
  the wrapper) are both derived from the same pinned per-operator value baseline
  (wrapper `values.yaml` + the named representative per-cluster overlay), so
  differences reflect operator-vs-chart behavior rather than divergent configuration

#### Scenario: CR bridges upstream-enabled vs wrapper-disabled configuration

- **WHEN** the wrapper chart disables an upstream subchart capability (e.g.
  `metal-operator-core.rbac.enable: false`) and injects a pre-rendered equivalent
- **THEN** the fixture CR enables that capability on the operator's upstream render
  so the operator emits the same resource directly, and that resource is compared
  (not allowlisted) against the wrapper's pre-rendered equivalent

#### Scenario: Operator injects mode value not present in the golden render

- **WHEN** the operator renders the upstream chart in seed or shoot mode
- **THEN** the operator-injected `mode` value drives the per-mode render, and any
  resource selection it causes is reflected in the captured stream that is compared
  against the correspondingly-classified golden set

#### Scenario: Real source fetch occurs during capture

- **WHEN** the harness renders the operator side for a fixture whose source is a Helm
  chart or a pinned kustomize root
- **THEN** the operator pipeline fetches the real source (OCI/HTTP chart pull, or
  pinned git root) as in production, rather than a stubbed source
