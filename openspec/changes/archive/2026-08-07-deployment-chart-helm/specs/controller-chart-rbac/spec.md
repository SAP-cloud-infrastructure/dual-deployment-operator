<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## ADDED Requirements

### Requirement: Broad seed applier ClusterRole and binding

Chart 1 MUST provision the operator's seed-side RBAC as a broad applier `ClusterRole` and `ClusterRoleBinding` (not a namespace-only `Role`), generated from `config/rbac` markers. The seed render is not namespace-local — candidate wrapper charts emit cluster-scoped seed resources, and the operator's transformations can produce `ClusterRole`s — so the applier MUST be able to create both namespaced and cluster-scoped seed-render kinds. The grant MUST cover: watching `DualDeploymentOperator` CRs cluster-wide; get/list on Secrets (the shoot token-requestor Secret referenced by `spec.shootAccess`); create/update/delete/get/list/watch of seed-render kinds, namespaced and cluster-scoped (Deployment, Service, ConfigMap, ServiceAccount, Role/RoleBinding, ClusterRole/ClusterRoleBinding, NetworkPolicy, and the operator's additions); and create/get/update of Leases in the operator's own namespace for leader election. Where the seed render creates RBAC, the applier grant MUST itself hold the powers it confers (Kubernetes privilege-escalation prevention).

#### Scenario: chart provisions a cluster-scoped applier role

- **WHEN** the chart is rendered
- **THEN** a `ClusterRole` and a `ClusterRoleBinding` for the operator's ServiceAccount are produced
- **AND** the `ClusterRole` grants create/update/delete/get/list/watch on both namespaced and cluster-scoped seed-render kinds

#### Scenario: applier can watch CRs and read the shoot Secret

- **WHEN** the applier `ClusterRole` is inspected
- **THEN** it grants get/list/watch on `DualDeploymentOperator` resources cluster-wide
- **AND** it grants get/list on Secrets so the operator can read the shoot token-requestor Secret

#### Scenario: leader-election lease permission is namespace-scoped

- **WHEN** the RBAC is rendered
- **THEN** the operator holds create/get/update on `coordination.k8s.io` Leases in its own namespace

#### Scenario: ClusterRoleBinding subject targets the release namespace

- **WHEN** the chart is rendered into a shoot control-plane namespace (the Helm release namespace)
- **THEN** the `ClusterRoleBinding` subject references the operator's ServiceAccount with `namespace: {{ .Release.Namespace }}` (not a hardcoded namespace)
- **AND** the `ClusterRole`/`ClusterRoleBinding` carry static, release-independent names (seed-global), consistent with the single-install-per-seed contract
