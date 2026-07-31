<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## ADDED Requirements

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

### Requirement: CRD shipped in the chart crds directory

The chart MUST place the `DualDeploymentOperator` CustomResourceDefinition under `chart/crds/` so Helm installs it once, before templates, and does not template or upgrade it. The CRD MUST NOT appear under `chart/templates/`.

#### Scenario: CRD is in crds, not templates

- **WHEN** the chart is generated
- **THEN** the `DualDeploymentOperator` CRD file exists under `chart/crds/`
- **AND** no CRD definition is rendered by `chart/templates/`

#### Scenario: installing the chart registers the CRD before workloads

- **WHEN** the chart is installed with Helm
- **THEN** Helm applies `chart/crds/` before rendering `templates/`
- **AND** the `DualDeploymentOperator` CRD is registered on the cluster
