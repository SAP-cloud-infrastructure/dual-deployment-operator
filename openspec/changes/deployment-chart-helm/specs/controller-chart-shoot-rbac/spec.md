<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## ADDED Requirements

### Requirement: Chart 1 provisions the shoot-applier RBAC bootstrap

The operator applies its shoot render **as** a Gardener-minted token-requestor ServiceAccount, which cannot grant itself the RBAC it needs (Kubernetes privilege-escalation prevention): a privileged principal must seed the operator's apply permissions on the shoot first. Chart 1 therefore MUST be able to provision that bootstrap, gated `shootRbac.enabled` (default `false`). When enabled, the chart MUST emit a `resources.gardener.cloud/v1alpha1` `ManagedResource` (plus the backing `Secret` carrying its objects) in the shoot control-plane namespace on the seed, so gardener-resource-manager (which holds cluster-admin-equivalent on the shoot) delivers a broad apply-scoped `ClusterRole` + `ClusterRoleBinding` to the shoot for the operator's shoot ServiceAccount. The bootstrap ClusterRole MUST cover the shoot-render kinds the operator applies (CustomResourceDefinitions; ClusterRole/ClusterRoleBinding/Role/RoleBinding with `bind`+`escalate`; Validating/MutatingWebhookConfigurations; ServiceAccounts/ConfigMaps/Secrets/Services/Namespaces). The bootstrap is operator install-time plumbing — identical for every managed workload and parameterized only by the shoot ServiceAccount name/namespace — so it lives in the operator install chart, NOT in a per-workload chart. Workload-specific shoot RBAC (e.g. a webhook-injector ServiceAccount subject) MUST stay in the workload chart.

This capability was previously scoped to the downstream `sapcc/helm-charts` workload chart; ownership moved to chart 1 so the bootstrap tracks the operator's presence and is uniform across workloads.

#### Scenario: bootstrap is off by default

- **WHEN** the chart is rendered with no overrides
- **THEN** no `ManagedResource` and no shoot-applier `ClusterRole`/`ClusterRoleBinding` objects are produced

#### Scenario: enabling the bootstrap requires the shoot ServiceAccount coordinates

- **WHEN** `shootRbac.enabled=true` is set without `shootRbac.serviceAccountName`
- **THEN** rendering fails fast with a required-value error (the same for a missing `shootRbac.serviceAccountNamespace`)

#### Scenario: enabled bootstrap emits a GRM ManagedResource seeding the shoot applier RBAC

- **WHEN** the chart is rendered with `shootRbac.enabled=true`, `shootRbac.serviceAccountName=<sa>`, `shootRbac.serviceAccountNamespace=<ns>`
- **THEN** a `ManagedResource` (`resources.gardener.cloud/v1alpha1`) and its backing `Secret` are produced in the shoot-CP namespace (release namespace, overridable via `shootRbac.namespace`)
- **AND** the ManagedResource's objects contain a broad apply-scoped `ClusterRole` and a `ClusterRoleBinding` whose subject is the `<sa>`/`<ns>` ServiceAccount
- **AND** the `ClusterRole` grants the shoot-render apply verbs on CRDs, RBAC kinds (including `bind`/`escalate`), webhook configurations, and the core namespaced kinds

### Requirement: Chart 1 provisions the shoot token-requestor Secret

Chart 1 MUST provision the Gardener token-requestor `Secret` that mints the operator's shoot credentials, gated by the same `shootRbac.enabled` toggle, paired with the applier bootstrap (the Secret mints the token FOR the same ServiceAccount the bootstrap binds). The Secret MUST carry the Gardener token-requestor labels (`resources.gardener.cloud/purpose: token-requestor`, `resources.gardener.cloud/class: shoot`) and the ServiceAccount injection annotations (`serviceaccount.resources.gardener.cloud/{name,namespace,inject-ca-bundle}`), with empty `token`/`bundle.crt` fields that Gardener's token-requestor controller fills and rotates asynchronously. The operator reads this Secret each reconcile (`spec.shootAccess.secretName`) to build its shoot client — chart 1's seed-applier RBAC therefore already grants get/list on Secrets (see `controller-chart-rbac`). The Secret's name MUST default to a workload-agnostic `dual-deployment-operator-shoot-access` and MUST be overridable via `shootRbac.secretName`, so a downstream wrapper chart can reference a stable, non-workload-specific name without a hard link.

This capability was previously scoped to the downstream `sapcc/helm-charts` workload chart; ownership moved to chart 1 so the credential Secret is provisioned and named uniformly across workloads and the operator only ever *reads* it.

#### Scenario: token-requestor Secret is off by default

- **WHEN** the chart is rendered with no overrides
- **THEN** no token-requestor `Secret` is produced

#### Scenario: enabled render emits the token-requestor Secret with a generic default name

- **WHEN** the chart is rendered with `shootRbac.enabled=true` and the shoot ServiceAccount coordinates
- **THEN** an `Opaque` `Secret` named `dual-deployment-operator-shoot-access` is produced in the shoot-CP namespace
- **AND** it carries the `resources.gardener.cloud/purpose: token-requestor` and `class: shoot` labels and the `serviceaccount.resources.gardener.cloud/{name,namespace,inject-ca-bundle}` annotations pointing at the shoot ServiceAccount
- **AND** its `token` and `bundle.crt` fields are empty (filled by the Gardener token-requestor controller)

#### Scenario: Secret name is overridable

- **WHEN** the chart is rendered with `shootRbac.enabled=true` and `shootRbac.secretName=<custom>`
- **THEN** the token-requestor `Secret` is named `<custom>`, and a wrapper chart's `spec.shootAccess.secretName` can reference that same name
