<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## MODIFIED Requirements

### Requirement: Production Helm ChartLoader with scheme dispatch

The operator MUST provide a production `ChartLoader` implementation (`internal/source`) that satisfies the existing `ChartLoader.Load(ctx, repo, name, version) (*chart.Chart, error)` seam and acquires a real chart. `Load` MUST support ONLY the OCI transport:

- `oci://…` — pull via the Helm registry client + `action.Pull` (OCI ref `oci://<repo>/<name>`, pinned `Version`).

A `repo` with any other scheme (including `http://` / `https://`) MUST be rejected with a clear error that names `oci://` as the only supported Helm chart transport, rather than being pulled or failing opaquely. The pull path MUST produce a chart `.tgz` that is loaded via `loader.Load` into a `*chart.Chart`. The renderer logic and the `ChartLoader` interface signature MUST NOT change.

#### Scenario: OCI reference is pulled via the registry client

- **WHEN** `Load` is called with `repo = "oci://keppel.global.cloud.sap/ccloud-helm"`, `name = "metal-operator-remote"`, `version = "0.6.2"`
- **THEN** the loader pulls the chart via the Helm registry client at the OCI ref `oci://keppel.global.cloud.sap/ccloud-helm/metal-operator-remote` and pinned version `0.6.2`
- **AND** returns a parsed `*chart.Chart` for that chart

#### Scenario: non-OCI Helm repo is rejected with a clear error

- **WHEN** `Load` is called with a `repo` whose scheme is `http://` or `https://` (or any non-`oci://` scheme)
- **THEN** `Load` returns a clear error identifying `oci://` as the only supported Helm chart transport
- **AND** the loader does NOT attempt a classic-repository pull and does NOT panic
