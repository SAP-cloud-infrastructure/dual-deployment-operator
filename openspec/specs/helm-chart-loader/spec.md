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

### Requirement: Pull to a temporary directory and clean up (no caching)

The loader MUST pull each chart into a temporary directory, load it via `loader.Load` into a `*chart.Chart`, and remove the temporary directory after the chart is loaded. The loader MUST NOT cache charts in this change — every `Load` fetches fresh. (Source caching, keyed by a resolved OCI digest, is scoped to Phase 7.5.)

#### Scenario: each Load fetches fresh and cleans up its temp dir

- **WHEN** `Load` is called for a chart
- **THEN** the loader pulls the chart `.tgz` into a temporary directory and loads it
- **AND** removes the temporary directory before returning
- **AND** does not retain the chart for a subsequent call (a second `Load` of the same `repo|name|version` pulls again)
