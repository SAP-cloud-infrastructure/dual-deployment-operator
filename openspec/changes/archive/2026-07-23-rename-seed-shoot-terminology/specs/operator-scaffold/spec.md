<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## MODIFIED Requirements

### Requirement: RBAC scaffold with namespace-scoped roles

`config/rbac/` MUST contain kubebuilder-scaffolded `ServiceAccount`, `Role`, `RoleBinding`, and `ClusterRole`/`ClusterRoleBinding` resources appropriate for a namespace-scoped operator. In v1 the operator's RBAC grants MUST cover only:

- `get`, `list`, `watch`, `patch`, `update` on `DualDeploymentOperator` CRs in the operator's own namespace
- `patch`, `update` on `DualDeploymentOperator/status` and `DualDeploymentOperator/finalizers`
- `get`, `list`, `watch` on `Secrets` in the operator's own namespace (for future kubeconfig fetch, unused in v1 no-op reconciler)

RBAC scaffolds for applying arbitrary seed or shoot resources (Phases 4-6) MUST NOT be added in this change.

#### Scenario: RBAC covers only CRs and Secrets

- **WHEN** the maintainer runs `kustomize build config/rbac`
- **THEN** the output's `Role` grants verbs on `dualdeploymentoperators.dual-deployment-operator.cc.sap` and `secrets` (core)
- **AND** it does NOT grant permissions on `deployments`, `services`, `customresourcedefinitions`, `validatingwebhookconfigurations`, or any other cluster-scoped resources
