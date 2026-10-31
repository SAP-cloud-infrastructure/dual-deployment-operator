<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec Delta: source-rendering

## MODIFIED Requirements

### Requirement: Kustomize overlay selection by mode

The kustomize renderer SHALL select the overlay subpath by mode: `spec.kustomize.seedPath` for `ModeSeed` and `spec.kustomize.shootPath` for `ModeShoot`. Before any render-cache lookup or build, the renderer MUST prepare a separate staging tree, transitively localize remote Git bases (discover → pin-to-SHA → fetch → rewrite-to-local), and preflight all supported loading paths. It MUST build only the prepared staging tree. Preparation and preflight also apply when no remote Git bases are present; supported local-only inputs produce byte-identical output to the pre-change behavior. It MUST build with `krusty` with plugins and Helm execution disabled and MUST parse the build output into `manifest.Manifest` values via the manifest-parsing capability with a fallback origin of `OriginUpstream`. The no-network build claim is bounded to accepted inputs under the pinned Kustomize version, not an OS-enforced sandbox.

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

#### Scenario: overlay with remote bases builds the localized staging tree

- **WHEN** the selected overlay transitively references one or more remote bases
- **THEN** the renderer builds the localized offline staging tree produced by the prefetch step rather than fetching bases at build time
- **AND** returns the manifests the overlay selects with their origins preserved
