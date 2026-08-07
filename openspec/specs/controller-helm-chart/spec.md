<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec: controller-helm-chart

## Purpose

Defines requirements for chart 1's generation and structure: the kubebuilder Helm plugin generating the chart at `chart/` from `config/*`, `PROJECT` file recording, chart linting, and the CRD shipping/toggling/keep-on-uninstall behaviour.

## Requirements

### Requirement: Controller Helm chart generated from config/*

The operator repository MUST provide a Helm chart (chart 1, the upstream controller chart) at the repo-root `chart/` directory, generated from the existing `config/*` kustomize scaffold via the kubebuilder Helm plugin (`helm.kubebuilder.io/v2-alpha`) with `--output-dir=.`. The chart MUST NOT be hand-authored; `config/*` remains the single source of truth and the chart is regenerated from it. The `PROJECT` file MUST record the plugin with its pinned output directory so regeneration is deterministic and confined to `chart/`.

#### Scenario: chart generated at repo root from config

- **WHEN** `kubebuilder edit --plugins=helm/v2-alpha --output-dir=.` is run in the repo
- **THEN** a `chart/` directory is created at the repo root containing `Chart.yaml`, `values.yaml`, and `templates/`
- **AND** the `PROJECT` file records the `helm.kubebuilder.io/v2-alpha` plugin with its output directory

#### Scenario: regeneration is confined to the chart directory

- **WHEN** the Helm plugin is re-run with `--force` after a change to `config/*`
- **THEN** only files under `chart/` are written or overwritten
- **AND** hand-written source under `cmd/`, `internal/`, and `api/` is left untouched

#### Scenario: chart lints and templates cleanly

- **WHEN** `helm lint chart/` and `helm template chart/` are executed
- **THEN** both succeed with no errors
- **AND** `make manifests generate` reports no drift in `config/*`

### Requirement: CRD shipped with the chart, toggled and kept

The chart MUST ship the `DualDeploymentOperator` CustomResourceDefinition so installing the chart registers the CRD. The kubebuilder `helm.kubebuilder.io/v2-alpha` plugin emits the CRD as a **templated** manifest under `chart/templates/crd/` gated by a `crd.enabled` value (default `true`) rather than under an untemplated `chart/crds/` directory; this is the plugin's native layout and MUST be accepted so the chart stays fully generated (not hand-authored). The CRD template MUST honor a `crd.keep` value (default `true`) that stamps `helm.sh/resource-policy: keep` so the CRD is retained on `helm uninstall`, preventing catastrophic loss of stored custom resources. `crd.enabled: false` MUST let a consumer suppress the CRD (e.g. when it is managed separately).

#### Scenario: CRD ships as a toggled template

- **WHEN** the chart is generated
- **THEN** the `DualDeploymentOperator` CRD exists at `chart/templates/crd/dualdeploymentoperators.dual-deployment-operator.cc.sap.yaml`
- **AND** it is wrapped in `{{- if .Values.crd.enabled }}` with `crd.enabled` defaulting to `true` in `values.yaml`

#### Scenario: installing the chart registers the CRD

- **WHEN** the chart is installed with Helm and default values
- **THEN** the rendered output includes the `DualDeploymentOperator` CustomResourceDefinition
- **AND** the CRD is registered on the cluster

#### Scenario: CRD is retained on uninstall by default

- **WHEN** the chart is rendered with the default `crd.keep: true`
- **THEN** the CRD manifest carries the `helm.sh/resource-policy: keep` annotation

#### Scenario: CRD can be suppressed by a consumer

- **WHEN** the chart is rendered with `crd.enabled=false`
- **THEN** no `CustomResourceDefinition` is rendered
