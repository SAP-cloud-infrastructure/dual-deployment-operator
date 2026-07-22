<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec: Cluster Clients

## Purpose

Defines the `internal/clients` package: factory functions that construct Kubernetes `client.Client` instances for the host (seed) cluster and the remote (shoot) cluster. The host client is built once from the manager's in-cluster REST config; the shoot client is built per reconcile from the Gardener token-requestor Secret referenced by `spec.remoteAccess`. Also documents the RBAC prerequisites for both clusters.

## Requirements

### Requirement: Host client factory

The `internal/clients` package SHALL provide a factory that returns a `client.Client` for the host (seed) cluster using the manager's own in-cluster REST config, so the operator applies host-render resources via its own ServiceAccount. The host applier MUST be constructable once at manager startup and reused across reconciles.

#### Scenario: Host client built from manager rest config

- **WHEN** the host client factory is invoked with the manager's REST config
- **THEN** it returns a `client.Client` bound to the seed cluster
- **AND** the returned client requires no per-CR kubeconfig

#### Scenario: Host applier constructed once at startup

- **WHEN** the manager starts
- **THEN** a single host `SSAApplier` (field manager `dual-deployment-operator`, cluster label `"host"`) is constructed and held on the reconciler
- **AND** it is reused for every CR reconcile rather than rebuilt per reconcile

---

### Requirement: Shoot client factory from token-requestor Secret

The `internal/clients` package SHALL provide a factory that builds a `client.Client` for the remote (shoot) cluster from the Gardener token-requestor Secret referenced by `spec.remoteAccess`. The factory MUST read the bearer token from the Secret key given by `spec.remoteAccess.tokenKey` (default `token`) and the CA bundle from the key given by `spec.remoteAccess.caKey` (default `bundle.crt`), and construct a `rest.Config` directly as `{Host: spec.remoteAccess.server, BearerToken: <token>, CAData: <caBundle>}`. It MUST NOT parse a kubeconfig blob, because the token-requestor Secret holds a rotating token plus CA rather than a self-contained kubeconfig. Because the shoot token rotates, the shoot client MUST be built per reconcile.

The factory MUST distinguish a **missing Secret** (a misconfiguration) from **credentials not yet populated** (a benign bootstrap state). Gardener declares the token-requestor Secret with empty values (`token: ""`, `bundle.crt: ""`) and its token-requestor controller fills them in asynchronously, so there is a startup window in which the Secret exists but the token and/or CA key is absent or empty. The factory MUST treat the token and CA as ready only when both are **present and non-empty**; a present-but-empty value MUST be treated as not-ready, not as a valid credential.

#### Scenario: Shoot client built from token and CA

- **WHEN** the shoot client factory is invoked for a CR whose `spec.remoteAccess.secretName` points at an existing Secret whose resolved token and CA-bundle keys are present and non-empty, with `spec.remoteAccess.server` set
- **THEN** it reads the token and CA bundle from the Secret and constructs a `rest.Config` with `Host` = `spec.remoteAccess.server`, `BearerToken` = the token, and `CAData` = the CA bundle
- **AND** returns a `client.Client` bound to the shoot cluster

#### Scenario: Default token and CA keys

- **WHEN** the CR omits `spec.remoteAccess.tokenKey` and `spec.remoteAccess.caKey`
- **THEN** the factory reads the token from Secret key `token` and the CA bundle from Secret key `bundle.crt`

#### Scenario: Missing Secret is a fatal, surfaced error

- **WHEN** the shoot client factory is invoked and the referenced Secret does not exist
- **THEN** it returns an error identifying the missing Secret
- **AND** the reconcile treats this as fatal (reason `ShootClientFailed`) and requeues without applying the remote render

#### Scenario: Not-yet-populated credentials are a benign progressing state

- **WHEN** the referenced Secret exists but the resolved token key or CA key is absent or empty (Gardener's token-requestor has not populated it yet)
- **THEN** the factory reports credentials-not-ready rather than returning a fatal error
- **AND** the reconcile skips the remote render this cycle and sets `Ready=False` with reason `WaitingForShootCredentials` (not degraded), and requeues so the credentials are picked up once Gardener populates them

#### Scenario: Shoot client rebuilt each reconcile

- **WHEN** two successive reconciles of the same CR occur
- **THEN** the shoot client is rebuilt from the Secret on each reconcile
- **AND** a rotated token in the Secret is picked up on the next reconcile without operator restart

---

### Requirement: Shoot ServiceAccount must be pre-empowered to apply (cluster-scoped, GRM-seeded)

The ServiceAccount the operator authenticates as on the **shoot** MUST already hold a cluster-scoped grant permitting it to create, update, and delete the cluster-scoped kinds the operator delivers (CustomResourceDefinitions, ClusterRoles, ClusterRoleBindings, Roles, RoleBindings, ServiceAccounts, Validating/Mutating WebhookConfigurations, and the operator's additions). The operator SHALL NOT attempt to bootstrap its own apply permissions, because Kubernetes privilege-escalation prevention forbids an applier from creating a ClusterRole granting powers it does not already hold, and the shoot ServiceAccount is not otherwise permitted to create RBAC. This apply-scoped grant is a broad **ClusterRole + ClusterRoleBinding** and is an **install-time prerequisite** seeded by a minimal, static Gardener `ManagedResource` applied by the privileged gardener-resource-manager (out of scope for the operator's own code — see design.md "shoot RBAC bootstrap"). When the grant is absent, per-resource applies that require it fail and are surfaced as `Degraded` in status (continue-on-error), never silently.

#### Scenario: Applies succeed when the SA is pre-empowered

- **WHEN** the bootstrap `ManagedResource` has seeded the shoot ServiceAccount and its apply-scoped ClusterRole/Binding, and the operator applies the remote render
- **THEN** the CRD, RBAC, ServiceAccount, and WebhookConfiguration resources are created/updated successfully

#### Scenario: Applies fail visibly when the SA lacks apply RBAC

- **WHEN** the shoot ServiceAccount lacks the apply-scoped grant (bootstrap `ManagedResource` missing) and the operator attempts to apply RBAC or CRDs
- **THEN** those applies fail with a forbidden/privilege-escalation error
- **AND** each failing resource is recorded with `Health=Degraded` and the error `Message`, and the reconcile requeues
- **AND** the operator does not attempt to create its own apply permissions

---

### Requirement: Host install RBAC is a broad grant provisioned by the operator's own chart

The operator's own ServiceAccount on the **host (seed)** cluster MUST hold a grant broad enough to apply every kind the host render may contain, provisioned by the operator's **own install chart** (not GRM). The host render is NOT guaranteed to be namespace-local: the candidate wrapper charts emit cluster-scoped resources on the host side (e.g. metal-operator and ipam-capi both ship a host-side `ClusterRole` + `ClusterRoleBinding`), and the operator's own `patch`/rename transforms can produce `ClusterRole`s (design.md documents renaming upstream namespace-scoped `Role`→`ClusterRole` for shared-namespace deployment). Therefore the host grant MUST cover the host-render kinds including any cluster-scoped kinds, and — where the host render creates RBAC — MUST itself hold the powers it grants (Kubernetes privilege-escalation prevention forbids creating a `ClusterRole`/`Role` conferring verbs the applier lacks).

This mirrors the accepted best practice of the reference appliers rather than inventing a narrower model: gardener-resource-manager grants its target-cluster applier a broad `cluster-admin`-equivalent `ClusterRole` (wildcard `*/*/*`), and Flux ships its appliers as `cluster-admin` and scopes tenants via per-object ServiceAccount impersonation — neither narrows the applier SA itself, because a narrowed applier cannot manage the cluster-scoped kinds (CRDs, ClusterRoles, webhooks) it must deliver. The host and shoot grants therefore differ in **provisioning mechanism** (host = the operator's own deployment chart; shoot = a GRM-seeded bootstrap `ManagedResource`), **not** in breadth: both are broad, escalation-capable applier grants.

The host grant SHALL be no broader than needed to apply the host render and operate: create/update/delete/get/list/watch on the host-render kinds (namespaced and cluster-scoped), read access to the `spec.remoteAccess` Secret, and create/get/update on the leader-election Lease. Owner references SHALL be set on applied host resources so they garbage-collect on CR deletion.

#### Scenario: Host applier can create the host render's cluster-scoped resources

- **WHEN** the host render contains a cluster-scoped resource (e.g. a `ClusterRole` or `ClusterRoleBinding`, as the candidate charts emit)
- **THEN** the operator's host ServiceAccount is permitted to create/update/delete it
- **AND** the operator does not fail with a forbidden or privilege-escalation error for host-render RBAC it is authorized to apply

#### Scenario: Host grant is provisioned by the operator's own install chart

- **WHEN** the operator is installed on the seed
- **THEN** its host apply permissions are provisioned by the operator's own deployment chart (not by a GRM-seeded ManagedResource)
- **AND** the grant is broad enough to cover every kind the host render may contain, including cluster-scoped kinds

---

### Requirement: At most one operator-managed install per seed for seed-global host objects

Host-render cluster-scoped objects (e.g. `ClusterRole`, `ClusterRoleBinding`) retain their upstream **seed-global fixed names**; the operator SHALL NOT auto-qualify these names per shoot-cp namespace. Consequently, deploying two `DualDeploymentOperator` CRs on the same seed whose host renders emit the **same-named** cluster-scoped object is an **unsupported configuration**: the operator does not de-conflict them and the outcome is undefined (SSA field-ownership contention on the shared object, and ambiguous prune/garbage-collection because a cluster-scoped object cannot be owner-referenced by a namespaced CR). This matches how the current wrapper charts behave in production, where each seed runs a given `-remote` operator in only one workload shoot-cp namespace. This constraint is an **install-time contract**, documented rather than runtime-validated in v1; a per-name uniquifier or an admission guard is a possible future enhancement.

#### Scenario: Host cluster-scoped object names are not per-namespace qualified

- **WHEN** the host render contains a cluster-scoped object (e.g. a `ClusterRole`)
- **THEN** the operator applies it under its rendered (upstream, seed-global) name
- **AND** the operator does not append the shoot-cp namespace or any per-CR suffix to that name

#### Scenario: Two CRs emitting the same-named host cluster-scoped object is unsupported

- **WHEN** two `DualDeploymentOperator` CRs on the same seed each render a cluster-scoped host object with the same name
- **THEN** this is an unsupported configuration the operator does not de-conflict
- **AND** the single-install-per-seed contract is the documented mitigation (the operator neither auto-uniquifies the name nor arbitrates ownership between the two CRs)
