<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## ADDED Requirements

### Requirement: Per-shoot deployment via the Helm release namespace

The chart MUST support per-shoot deployment — one operator instance per `shoot--cp--*` control-plane namespace on the seed — as one Helm release per namespace, targeted by the Helm release namespace (`--namespace`). The chart's namespaced resources (Deployment, ServiceAccount, namespaced Role/RoleBinding, and any operator-owned namespaced objects) MUST NOT hardcode `metadata.namespace`; they inherit the release namespace. Any cross-namespace reference (e.g. a `ClusterRoleBinding` subject) MUST use `{{ .Release.Namespace }}`. The target namespace MUST NOT be a values field. This matches the existing fleet `-remote` wrapper charts, which are deployed once per shoot-cp namespace via the Helm release namespace.

#### Scenario: namespaced resources inherit the release namespace

- **WHEN** the chart is installed with `--namespace shoot--cp--<x>`
- **THEN** the Deployment, ServiceAccount, and namespaced RBAC are created in `shoot--cp--<x>`
- **AND** none of those resources carry a hardcoded `metadata.namespace`

#### Scenario: two shoots get independent releases

- **WHEN** the chart is installed twice, once per shoot-cp namespace, as two Helm releases
- **THEN** each release's namespaced resources land in its own namespace
- **AND** the target namespace is not read from a values field

### Requirement: Gardener egress labels on the manager pod template

The operator runs per-shoot in a `shoot--cp--*` namespace on the seed, where Gardener enforces a deny-all NetworkPolicy with label-gated allow policies. Chart 1's `deployment.yaml` pod template MUST carry the Gardener networking egress labels `networking.gardener.cloud/to-dns: allowed`, `networking.gardener.cloud/to-public-networks: allowed`, and `networking.gardener.cloud/to-private-networks: allowed`, without which the operator's Helm OCI pulls (keppel) and kustomize root+transitive fetches (github) fail with DNS/connection errors.

#### Scenario: pod template stamps all three egress labels

- **WHEN** the chart's `deployment.yaml` is rendered
- **THEN** the manager pod template's `metadata.labels` includes `networking.gardener.cloud/to-dns: allowed`, `networking.gardener.cloud/to-public-networks: allowed`, and `networking.gardener.cloud/to-private-networks: allowed`

### Requirement: Leader election enabled

The manager MUST run with `--leader-elect=true` so at most one instance is active cluster-wide, required even at `replicas: 1` because a rolling update transiently runs two pods. `cmd/main.go` MUST set `LeaderElectionReleaseOnCancel: true` so the outgoing leader releases the lease on graceful shutdown. The chart's default `replicas` MUST be `1`.

#### Scenario: manager runs with leader election

- **WHEN** the chart's `deployment.yaml` is rendered
- **THEN** the manager container args include `--leader-elect=true`
- **AND** the Deployment's default `replicas` is `1`

#### Scenario: leader releases lease on shutdown

- **WHEN** `cmd/main.go` constructs the manager options
- **THEN** `LeaderElectionReleaseOnCancel` is set to `true`

### Requirement: Liveness and readiness probes and metrics wired

Chart 1's `deployment.yaml` MUST set the manager container's `livenessProbe` (HTTP `GET /healthz` on the health-probe port) and `readinessProbe` (HTTP `GET /readyz`), pass `--health-probe-bind-address` and `--metrics-bind-address`, and expose the metrics port. These MUST be surfaced from the `config/*` scaffold, not silently dropped by chart generation. Chart 1 MUST NOT mount a chart-cache `emptyDir` volume (that volume is a separate deliverable).

#### Scenario: deployment defines both probes

- **WHEN** the chart's `deployment.yaml` is rendered
- **THEN** the manager container has a `livenessProbe` doing HTTP `GET /healthz` on the probe port
- **AND** the manager container has a `readinessProbe` doing HTTP `GET /readyz` on the probe port

#### Scenario: probe and metrics bind flags are passed

- **WHEN** the manager container args are inspected
- **THEN** they include `--health-probe-bind-address` and `--metrics-bind-address`
- **AND** the metrics port is exposed on the container

#### Scenario: no chart-cache volume in this chart

- **WHEN** the chart's `deployment.yaml` is rendered
- **THEN** no `source-cache` (or equivalent chart-cache) `emptyDir` volume or mount is present
