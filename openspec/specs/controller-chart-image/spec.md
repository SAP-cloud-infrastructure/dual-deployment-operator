<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec: controller-chart-image

## Purpose

Defines requirements for chart 1's container image reference: registry-agnostic defaults, the kubebuilder `manager.image.repository`/`manager.image.tag` values shape, and subchart consumability for downstream wrapper charts that redirect the image to an internal mirror.

## Requirements

### Requirement: Registry-agnostic controller image reference

Chart 1 MUST be registry-agnostic and MUST NOT reference any keppel host. The chart's `values.yaml` MUST expose the container image using the kubebuilder shape `manager.image.repository` + `manager.image.tag`. The default `manager.image.repository` MUST be `ghcr.io/SAP-cloud-infrastructure/dual-deployment-operator` (the real GHCR publish target), and `manager.image.tag` MUST default to the chart's `.Chart.AppVersion` when left empty. The image reference MUST be fully overridable so a downstream consumer can point it at a mirror.

#### Scenario: default image is the keppel-free ghcr path

- **WHEN** the chart is rendered with default values
- **THEN** the manager container image resolves to `ghcr.io/SAP-cloud-infrastructure/dual-deployment-operator` at the tag `.Chart.AppVersion`
- **AND** no `keppel.global.cloud.sap` reference appears anywhere in the chart

#### Scenario: image is overridable by a consumer

- **WHEN** the chart is rendered with `manager.image.repository` and `manager.image.tag` overridden (e.g. to a keppel-mirror path and a pinned tag)
- **THEN** the manager container image resolves to the overridden repository and tag

### Requirement: Chart consumable as a subchart with image override

Chart 1 MUST be structured so a wrapper chart (chart 2, in `sapcc/helm-charts`) can declare it as a Helm dependency (subchart) and override `manager.image.repository`/`manager.image.tag` via the subchart's values key, redirecting the image to the keppel mirror (`keppel.global.cloud.sap/ccloud-ghcr-io-mirror/SAP-cloud-infrastructure/dual-deployment-operator`) without any change to chart 1.

#### Scenario: subchart image override redirects to the keppel mirror

- **WHEN** chart 1 is consumed as a subchart and the parent sets the subchart's `manager.image.repository` to the keppel-mirror path
- **THEN** the rendered manager container image resolves to the keppel-mirror path
- **AND** chart 1 itself contains no keppel reference
