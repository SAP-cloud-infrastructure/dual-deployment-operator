<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## ADDED Requirements

### Requirement: Chart 1 provisions the shoot-applier RBAC bootstrap

The operator applies its shoot render **as** a Gardener-minted token-requestor ServiceAccount, which cannot grant itself the RBAC it needs (Kubernetes privilege-escalation prevention): a privileged principal must seed the operator's apply permissions on the shoot first. Chart 1 therefore MUST be able to provision that bootstrap, gated `shootRbac.enabled` (default `false`). When enabled, the chart MUST emit a `resources.gardener.cloud/v1alpha1` `ManagedResource` (plus the backing `Secret` carrying its objects) in the shoot control-plane namespace on the seed, so gardener-resource-manager (which holds cluster-admin-equivalent on the shoot) delivers to the shoot, as one atomic unit: (1) the shoot `ServiceAccount` the token-requestor Secret mints a token for, and (2) a broad apply-scoped `ClusterRole` + `ClusterRoleBinding` bound to that ServiceAccount. The ServiceAccount MUST be created by the bootstrap (not by any per-workload render), because the token-requestor Secret mints a token for it, the ClusterRoleBinding binds to it, and the operator cannot self-bootstrap the identity it authenticates as — the SA, its token, and its RBAC must be seeded together by the privileged principal (design.md §3.6.7, line 942). The bootstrap ClusterRole MUST cover the shoot-render kinds the operator applies (CustomResourceDefinitions; ClusterRole/ClusterRoleBinding/Role/RoleBinding with `bind`+`escalate`; Validating/MutatingWebhookConfigurations; ServiceAccounts/ConfigMaps/Secrets/Services/Namespaces). The bootstrap is operator install-time plumbing — identical for every managed workload and parameterized by the shoot ServiceAccount name/namespace — so it lives in the operator install chart, NOT in a per-workload chart. Workload-specific shoot RBAC (e.g. a webhook-injector ServiceAccount subject) MUST stay in the workload chart.

The applier ServiceAccount MUST default to a dedicated, operator-owned identity (`shootRbac.serviceAccountName` defaults to `dual-deployment-operator-shoot-applier`) and MUST NOT default to any ServiceAccount that a workload render also creates or deletes. The operator authenticates to the shoot AS this ServiceAccount; if it were a workload-owned SA, deleting the CR would tear that SA down mid-finalizer, invalidating the operator's own token (401) and deadlocking the finalizer with shoot resources orphaned. A dedicated operator-owned SA is never in a workload teardown set, so the operator keeps valid credentials through cleanup, and the workload SA is not burdened with the broad `bind`/`escalate` applier grant (design.md §3.6.7, §3.7). The value remains overridable, but overriding it to a workload SA reintroduces the deadlock and is a misconfiguration. The `required` guard still fires when `serviceAccountName`/`serviceAccountNamespace` are explicitly set empty while `shootRbac.enabled=true`.

This capability was previously scoped to the downstream `sapcc/helm-charts` workload chart; ownership moved to chart 1 so the bootstrap tracks the operator's presence and is uniform across workloads.

#### Scenario: bootstrap is off by default

- **WHEN** the chart is rendered with no overrides
- **THEN** no `ManagedResource` and no shoot-applier `ClusterRole`/`ClusterRoleBinding` objects are produced

#### Scenario: applier ServiceAccount defaults to a dedicated operator-owned identity

- **WHEN** the chart is rendered with `shootRbac.enabled=true` and no `serviceAccountName` override
- **THEN** rendering succeeds and the bootstrap SA + ClusterRoleBinding subject are `dual-deployment-operator-shoot-applier` in `kube-system`
- **AND** that ServiceAccount is not created or deleted by any workload render (so a CR delete cannot self-deauthenticate the operator mid-finalizer)

#### Scenario: explicitly-empty ServiceAccount coordinates fail fast

- **WHEN** `shootRbac.enabled=true` is set with `shootRbac.serviceAccountName` explicitly empty
- **THEN** rendering fails fast with a required-value error (the same for an explicitly-empty `shootRbac.serviceAccountNamespace`)

#### Scenario: enabled bootstrap emits a GRM ManagedResource seeding the shoot applier RBAC

- **WHEN** the chart is rendered with `shootRbac.enabled=true`, `shootRbac.serviceAccountName=<sa>`, `shootRbac.serviceAccountNamespace=<ns>`
- **THEN** a `ManagedResource` (`resources.gardener.cloud/v1alpha1`) and its backing `Secret` are produced in the shoot-CP namespace (release namespace, overridable via `shootRbac.namespace`)
- **AND** the ManagedResource's objects contain the `<sa>`/`<ns>` `ServiceAccount`, a broad apply-scoped `ClusterRole`, and a `ClusterRoleBinding` whose subject is that ServiceAccount
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
