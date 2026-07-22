<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## ADDED Requirements

### Requirement: Two-render reconcile pipeline

The reconciler SHALL, on each reconcile of a `DualDeploymentOperator`, build the source renderer from `spec.source`, render it twice (host mode using the CR's own namespace; remote mode using `spec.remoteNamespace`), apply the declared transformations to each render independently in declaration order, sort each render by the fixed intra-render kind priority, and then deliver each render to its target cluster. A failure in source construction, rendering, or transformation SHALL be fatal to the reconcile and set a `Ready=False` condition with a reason identifying the failing stage. A missing shoot-access Secret is likewise fatal (`ShootClientFailed`); however, a shoot-access Secret that exists but whose token/CA are not yet populated is NOT fatal — it is a benign progressing state (see the "Remote credentials not yet ready" requirement).

#### Scenario: Host and remote rendered with mode-specific namespace

- **WHEN** a CR is reconciled
- **THEN** the source is rendered once in host mode with the CR's `metadata.namespace`
- **AND** rendered once in remote mode with `spec.remoteNamespace`

#### Scenario: Transformations applied per render in declaration order

- **WHEN** `spec.transformations` is non-empty
- **THEN** each transformation is applied to the host render and the remote render independently, in declaration order

#### Scenario: Render or transform failure is fatal and surfaced

- **WHEN** source construction, a render, or a transformation fails, or the shoot-access Secret is missing
- **THEN** the reconcile stops before delivery of the affected render
- **AND** sets `Ready=False` with a reason identifying the stage (e.g. `InvalidSource`, `HostRenderFailed`, `RemoteRenderFailed`, `InvalidTransformation`, `ShootClientFailed`)
- **AND** requeues

### Requirement: Remote failure severity respects spec.applyOrder

The reconciler SHALL distinguish three remote-render outcomes and, under `RemoteFirst`, use them to decide whether the host render may proceed. This respects the `RemoteFirst` contract — host depends on the remote (CRDs/RBAC/webhooks) being present first, so the host render MUST NOT proceed when the remote render has not fully converged.

**Why any remote failure gates host under `RemoteFirst`.** The remote (shoot) cluster is a **workless** cluster: the remote render contains only structural dependencies the host controller consumes — CRDs, ClusterRoles/Roles and their bindings, ServiceAccounts, Validating/Mutating WebhookConfigurations, and additions. There are effectively no "host doesn't care" resources in the remote render. Therefore a *partial* remote failure is not benign under `RemoteFirst`: whatever failed is something the host depends on, and starting the host render against a missing CRD/RBAC/webhook would crash-loop or silently no-op the host controller. So under `RemoteFirst` **both** partial and complete remote failures gate the host render. Partial vs complete still differ in *diagnostics* (which resources failed, and the requeue path) but not in the host-gating decision.

- **Partial failure** — the shoot client was built and at least one remote resource applied, but some failed. Under `RemoteFirst` the reconciler SHALL **stop before applying the host render** (the failed structural resources are host dependencies), record the failed remote resources as `Health=Degraded` with a `Message`, set `Ready=False` reason `ResourcesDegraded`, aggregate the per-resource errors, and requeue. Under `HostFirst` the host render is applied first regardless and the failures are flagged (`ResourcesDegraded`) without blocking host.
- **Complete remote failure** — the shoot client cannot be built or reached (e.g. shoot NotFound/unreachable, forbidden), or zero of the remote render's resources applied. Under `RemoteFirst` the reconciler SHALL **stop before applying the host render**, set `Ready=False` reason `RemoteApplyFailed`, and requeue. Under `HostFirst` the host render is applied first regardless and the remote failure is flagged (`RemoteApplyFailed`) but does not block host.
- **Credentials not yet ready** — the shoot-access Secret exists but the token or CA bundle is absent or empty (the normal window before Gardener's token-requestor fills it). This is a benign bootstrap wait, **not** a failure: the reconciler SHALL set `Ready=False` reason `WaitingForShootCredentials` (progressing, not degraded) and requeue with a short backoff. Under `RemoteFirst` it also **defers the host render** (host must not start before the remote it depends on); under `HostFirst` the host render is applied first regardless.

`HostFirst` never blocks the host render on a remote outcome — the consumer has declared host does not depend on remote coming up first. Only `RemoteFirst` gates the host render on remote convergence; under `RemoteFirst` *any* incomplete remote outcome (partial, complete, or credentials-not-ready) gates host.

#### Scenario: RemoteFirst — complete remote failure stops before host

- **WHEN** a CR with `spec.applyOrder: RemoteFirst` (or unset) is reconciled and the remote render cannot be applied at all (shoot NotFound/unreachable, forbidden, or zero resources applied)
- **THEN** the host render is NOT applied this cycle
- **AND** the `Ready` condition is `False` with reason `RemoteApplyFailed`
- **AND** the reconcile requeues

#### Scenario: RemoteFirst — credentials not ready defers host

- **WHEN** a CR with `spec.applyOrder: RemoteFirst` (or unset) is reconciled and its shoot-access Secret exists but the token or CA is absent or empty
- **THEN** neither the remote nor the host render is applied this cycle
- **AND** the `Ready` condition is `False` with reason `WaitingForShootCredentials` (progressing, not degraded)
- **AND** the reconcile requeues with a short backoff

#### Scenario: RemoteFirst — partial remote failure also stops before host

- **WHEN** a CR with `spec.applyOrder: RemoteFirst` (or unset) is reconciled, the shoot client is built, and at least one remote resource applies while some fail
- **THEN** the host render is NOT applied this cycle (the failed remote resources are structural dependencies the host consumes)
- **AND** the failed remote resources are recorded `Health=Degraded` with a `Message`
- **AND** the `Ready` condition is `False` with reason `ResourcesDegraded`
- **AND** the reconcile requeues

#### Scenario: HostFirst — host applies despite partial remote failure

- **WHEN** a CR with `spec.applyOrder: HostFirst` is reconciled and the remote render has partial per-resource failures
- **THEN** the host render is still applied first
- **AND** the failed remote resources are flagged `ResourcesDegraded` without blocking the host render

#### Scenario: HostFirst — host applies regardless of remote outcome

- **WHEN** a CR with `spec.applyOrder: HostFirst` is reconciled and the remote render is a complete failure or credentials are not ready
- **THEN** the host render is still applied first
- **AND** the remote failure is flagged (`RemoteApplyFailed`) or the wait is flagged (`WaitingForShootCredentials`) without blocking the host render

#### Scenario: Credentials populated on a later reconcile proceed normally

- **WHEN** a subsequent reconcile finds the token and CA present and non-empty
- **THEN** the shoot client is built and the remote render is applied normally

### Requirement: Cross-render apply order via spec.applyOrder

The reconciler SHALL apply the two renders to their target clusters in the order given by `spec.applyOrder` (`RemoteFirst` default, or `HostFirst`). Whether the second render proceeds after the first render has failures depends on the ordering: under `HostFirst` the host render (first) is always applied and the remote render (second) follows regardless of host per-resource failures; under `RemoteFirst` the host render (second) is **gated on the remote render (first) fully converging** — *any* incomplete remote outcome (partial per-resource failure, complete failure, or credentials-not-ready) stops or defers the host render per the "Remote failure severity respects spec.applyOrder" requirement, because the workless shoot's remote render is entirely structural dependencies (CRDs/RBAC/webhooks) the host consumes. `HostFirst` never gates the host render on the remote outcome. Intra-render kind ordering remains the fixed built-in priority and is not affected by `applyOrder`.

`spec.applyOrder` governs cross-render sequencing for both directions: **all deletion — both per-reconcile prune and CR-deletion teardown — SHALL process the two renders in the reverse of `spec.applyOrder`.** Under the default `RemoteFirst`, apply sequences remote-then-host, so deletion sequences host-then-remote (the host controller stops before the shoot CRDs/RBAC it depends on are removed); under `HostFirst` the reverse holds. This cross-render reversal is independent of, and composes with, the fixed intra-render kind-priority reversal already applied within each render.

#### Scenario: RemoteFirst applies remote render before host render

- **WHEN** a CR with `spec.applyOrder: RemoteFirst` (or unset) is reconciled
- **THEN** the remote render is applied to the shoot cluster before the host render is applied to the seed cluster

#### Scenario: HostFirst applies host render before remote render

- **WHEN** a CR with `spec.applyOrder: HostFirst` is reconciled
- **THEN** the host render is applied to the seed cluster before the remote render is applied to the shoot cluster

#### Scenario: Deletion reverses the cross-render apply order

- **WHEN** a CR with `spec.applyOrder: RemoteFirst` (or unset) is deleted, or a reconcile prunes orphans from both renders
- **THEN** the host render's resources are deleted before the remote render's resources (the reverse of the apply order)

#### Scenario: HostFirst deletion order is remote-then-host

- **WHEN** a CR with `spec.applyOrder: HostFirst` is deleted, or a reconcile prunes orphans from both renders
- **THEN** the remote render's resources are deleted before the host render's resources (the reverse of the apply order)

#### Scenario: HostFirst — remote render still applied despite host per-resource failures

- **WHEN** a CR with `spec.applyOrder: HostFirst` is reconciled and the host render (first) has one or more per-resource apply failures but is not a complete failure (at least one resource applied)
- **THEN** the remote render (second) is still applied
- **AND** all failures are aggregated into status

### Requirement: Continue-on-error aggregation across a render

When applying a render, the reconciler SHALL attempt every resource, recording each resource's `ResourceStatus` (including `Health=Degraded` and a `Message` for failures), and SHALL aggregate all per-resource errors so the reconcile requeues while still applying the healthy resources. A single failing resource MUST NOT abort the remaining resources in that render. A cluster-scoped host object that the delivery layer refuses to apply because it is owned by a *different* CR (see the resource-delivery "Cluster-scoped apply is guarded against cross-CR conflict" requirement) is one such per-resource failure: it is recorded `Health=Degraded` with a `Message` naming the conflicting owner and aggregated like any other, never aborting the batch.

#### Scenario: One failing resource does not block the batch

- **WHEN** applying a render where one resource fails (e.g. transient conflict or invalid object) and the others succeed
- **THEN** the successful resources are applied
- **AND** the failing resource is recorded with `Health=Degraded` and a `Message`
- **AND** the reconcile returns an aggregated error and requeues

#### Scenario: Cross-CR cluster-scoped conflict is a continue-on-error failure

- **WHEN** applying a host render whose cluster-scoped object is already owned by a different CR, so the delivery layer refuses to apply it
- **THEN** that object is recorded with `Health=Degraded` and a `Message` identifying the conflicting owner
- **AND** the remaining host resources are still applied
- **AND** the reconcile returns an aggregated error and requeues (it does not clobber the foreign-owned object)

### Requirement: Prune resources that leave a render

After applying each render, the reconciler SHALL prune orphans: it SHALL diff the previous applied-set (recorded in `status.hostResources` / `status.remoteResources`) against the new render using a version-independent identity key `group/kind/namespace/name`, and delete resources present before but absent now, on that render's own target client. Within each render the orphans SHALL be deleted in reverse of the fixed intra-render kind priority; across the two renders the prune SHALL process them in the reverse of `spec.applyOrder` (the same cross-render reversal used by CR-deletion teardown). A `CustomResourceDefinition` orphan SHALL NOT be deleted while `spec.retentionPolicy.crds` is `Retain`. Status SHALL be written after apply and prune so a failed prune leaves the orphan tracked and retried on the next reconcile.

#### Scenario: Non-CRD orphan is pruned

- **WHEN** a resource recorded in the previous applied-set is absent from the new render and is not a CustomResourceDefinition
- **THEN** it is deleted from its target cluster

#### Scenario: CRD orphan is retained under Retain policy

- **WHEN** a CustomResourceDefinition recorded in the previous applied-set is absent from the new render and `spec.retentionPolicy.crds` is `Retain`
- **THEN** it is not deleted

#### Scenario: CRD orphan is deleted under Delete policy

- **WHEN** the same CRD orphan occurs and `spec.retentionPolicy.crds` is `Delete`
- **THEN** it is deleted

#### Scenario: Identity match ignores API version

- **WHEN** a resource's API version changes between reconciles (e.g. `v1beta1` to `v1`) but its group, kind, namespace, and name are unchanged
- **THEN** it is treated as the same resource and not pruned as an orphan

#### Scenario: Failed prune self-heals

- **WHEN** a prune delete fails for an orphan
- **THEN** the orphan remains recorded in status
- **AND** deletion is retried on the next reconcile

### Requirement: Status population and Ready condition

After apply and prune, the reconciler SHALL populate `status.hostResources`, `status.remoteResources`, a `LastReconcile` timestamp, and a `Ready` condition aggregated from all per-resource health: `True` when all resources are `Healthy`, `False` with reason `ResourcesDegraded` when any is `Degraded`, and `False` with reason `Progressing` when any is `Progressing` and none is `Degraded`.

#### Scenario: Ready is True when all resources healthy

- **WHEN** every applied resource across both renders reports `Healthy`
- **THEN** the CR's `Ready` condition is `True`

#### Scenario: Ready is False when any resource degraded

- **WHEN** at least one applied resource reports `Degraded`
- **THEN** the CR's `Ready` condition is `False` with reason `ResourcesDegraded`

#### Scenario: Status records the applied set for the next prune

- **WHEN** a reconcile completes apply and prune
- **THEN** `status.hostResources` and `status.remoteResources` reflect the resources of the render just applied
- **AND** `status.lastReconcile` is set to the reconcile time

### Requirement: Finalizer-driven deletion with reverse cross-render order

The reconciler SHALL add a finalizer on first reconcile and, on CR deletion, tear down resources in the reverse of `spec.applyOrder`. Remote resources SHALL be deleted explicitly via the shoot client (respecting `spec.retentionPolicy.crds` for CRDs); host resources are removed by owner-reference garbage collection. The finalizer SHALL be removed only once remote deletion is confirmed complete (every remote resource deleted or observed NotFound). If the shoot is unreachable during CR deletion, the operator SHALL NOT remove the finalizer and SHALL NOT assume the resources are gone: "unreachable" is indistinguishable from "transiently down" and does not imply "deleted." Instead it SHALL surface a `ShootUnreachable` condition and Kubernetes Event on the CR and requeue, keeping the CR in `Terminating` until either the shoot becomes reachable and cleanup completes, or an operator manually removes the finalizer. This deliberately prevents silently orphaning live shoot resources; the trade-off (a CR can remain in `Terminating` if the shoot is genuinely gone and no one clears the finalizer) is accepted and surfaced rather than auto-resolved.

#### Scenario: Finalizer added on first reconcile

- **WHEN** a CR without the operator finalizer is reconciled
- **THEN** the finalizer is added and the CR is updated before delivery proceeds

#### Scenario: Remote resources deleted on CR deletion, CRDs retained

- **WHEN** a CR with `spec.retentionPolicy.crds: Retain` is deleted and the shoot is reachable
- **THEN** all non-CRD remote resources are deleted via the shoot client in reverse-delete order
- **AND** CustomResourceDefinitions on the shoot are retained
- **AND** the finalizer is removed only after remote deletion succeeds

#### Scenario: Finalizer retained when reachable shoot delete fails

- **WHEN** the shoot is reachable but one or more remote deletes fail during CR deletion
- **THEN** the finalizer is not removed
- **AND** the reconcile requeues to retry

#### Scenario: Unreachable shoot blocks finalizer removal and is surfaced

- **WHEN** the shoot client cannot be built or reached during CR deletion
- **THEN** the finalizer is NOT removed and the remote resources are NOT assumed gone
- **AND** a `ShootUnreachable` condition and a Kubernetes Event are set on the CR
- **AND** the reconcile requeues so cleanup completes if the shoot returns
- **AND** the CR stays in `Terminating` until the shoot is reachable or an operator manually removes the finalizer

### Requirement: Periodic drift-correction requeue

On a successful reconcile the reconciler SHALL requeue after a fixed interval (default 10 minutes) so the render is periodically re-applied for drift correction; the periodic re-apply MUST continue to omit `caBundle` so it never reverts the webhook-injector's value.

#### Scenario: Successful reconcile requeues for drift correction

- **WHEN** a reconcile completes successfully
- **THEN** it requeues after the fixed interval (default 10 minutes)

### Requirement: Concurrency safety without an application-level lock

The reconciler SHALL rely on controller-runtime's per-CR workqueue serialization (one reconcile per CR key at a time), optimistic concurrency on the CR via `resourceVersion` for status writes, and Server-Side Apply field ownership on target objects; it SHALL NOT implement a Helm-style status-as-lock. The operator MUST be run with leader election enabled so at most one instance is active cluster-wide, which is required even at a single replica because a rolling update transiently runs two pods.

#### Scenario: Same CR is never reconciled concurrently within a process

- **WHEN** multiple events arrive for the same CR
- **THEN** controller-runtime processes that CR key on at most one worker at a time

#### Scenario: Concurrent CR status change yields a conflict and requeue

- **WHEN** the CR is modified by another writer between read and `Status().Update`
- **THEN** the update fails with a 409 conflict
- **AND** the reconcile requeues and re-reads the CR rather than overwriting

#### Scenario: Leader election ensures a single active instance

- **WHEN** the operator runs with leader election enabled and more than one pod exists (including the transient overlap during a rolling update)
- **THEN** only the pod holding the lease reconciles CRs
- **AND** the other pods remain passive standbys

### Requirement: Reconciler replaces the no-op behavior

The `DualDeploymentOperator` reconciler SHALL perform the full render → transform → deliver → prune → status pipeline described above, replacing the prior no-op reconcile that only fetched the CR and requeued.

#### Scenario: Reconcile delivers resources rather than no-op

- **WHEN** a valid CR is reconciled
- **THEN** the operator renders, transforms, and applies resources to the host and shoot clusters and populates status
- **AND** it does not merely fetch the CR and return
