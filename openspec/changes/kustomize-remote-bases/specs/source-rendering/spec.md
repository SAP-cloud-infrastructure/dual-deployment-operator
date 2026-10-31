<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec Delta: source-rendering

## MODIFIED Requirements

### Requirement: Kustomize overlay selection by mode

The kustomize renderer SHALL select the overlay subpath by mode: `spec.kustomize.seedPath` for `ModeSeed` and `spec.kustomize.shootPath` for `ModeShoot`. Before building, when the resolved root (transitively) references remote bases, the renderer MUST localize them into a self-contained offline staging tree via the kustomize-remote-bases prefetch step (discover → pin-to-SHA → fetch → rewrite-to-local → preflight) and build that staging tree; when no remote bases are present the renderer builds the resolved root directly and the output is byte-identical to the pre-change behavior. It MUST build with `krusty` with plugins disabled and MUST parse the build output into `manifest.Manifest` values via the manifest-parsing capability with a fallback origin of `OriginUpstream`.

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
