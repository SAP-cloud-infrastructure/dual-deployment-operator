<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec: Reconcile Loop

## Purpose

Defines the full reconciliation pipeline of the `DualDeploymentOperator` controller: two-render source rendering, per-render transformation, cross-render apply ordering (`spec.applyOrder`), continue-on-error aggregation, orphan pruning, status population, finalizer-driven deletion, periodic drift-correction requeue, and concurrency safety. Supersedes the noop-reconciler capability.

## Requirements

### Requirement: Two-render reconcile pipeline

The reconciler SHALL, on each reconcile of a `DualDeploymentOperator`, build the source renderer from `spec.source`, render it twice (seed mode using the CR's own namespace; shoot mode using `spec.shootNamespace`), apply the declared transformations to each render independently in declaration order, sort each render by the fixed intra-render kind priority, and then deliver each render to its target cluster. A failure in source construction, rendering, or transformation SHALL be fatal to the reconcile and set a `Ready=False` condition with a reason identifying the failing stage. A missing shoot-access Secret is likewise fatal (`ShootClientFailed`); however, a shoot-access Secret that exists but whose token/CA are not yet populated is NOT fatal — it is a benign progressing state (see the "Shoot credentials not yet ready" requirement).

#### Scenario: Seed and shoot rendered with mode-specific namespace

- **WHEN** a CR is reconciled
- **THEN** the source is rendered once in seed mode with the CR's `metadata.namespace`
- **AND** rendered once in shoot mode with `spec.shootNamespace`

#### Scenario: Transformations applied per render in declaration order

- **WHEN** `spec.transformations` is non-empty
- **THEN** each transformation is applied to the seed render and the shoot render independently, in declaration order

#### Scenario: Render or transform failure is fatal and surfaced

- **WHEN** source construction, a render, or a transformation fails, or the shoot-access Secret is missing
- **THEN** the reconcile stops before delivery of the affected render
- **AND** sets `Ready=False` with a reason identifying the stage (e.g. `InvalidSource`, `SeedRenderFailed`, `ShootRenderFailed`, `InvalidTransformation`, `ShootClientFailed`)
- **AND** requeues

### Requirement: Shoot failure severity respects spec.applyOrder

The reconciler SHALL distinguish three shoot-render outcomes and, under `ShootFirst`, use them to decide whether the seed render may proceed. This respects the `ShootFirst` contract — seed depends on the shoot (CRDs/RBAC/webhooks) being present first, so the seed render MUST NOT proceed when the shoot render has not fully converged.

**Why any shoot failure gates seed under `ShootFirst`.** The shoot cluster is a **workless** cluster: the shoot render contains only structural dependencies the seed controller consumes — CRDs, ClusterRoles/Roles and their bindings, ServiceAccounts, Validating/Mutating WebhookConfigurations, and additions. There are effectively no "seed doesn't care" resources in the shoot render. Therefore a *partial* shoot failure is not benign under `ShootFirst`: whatever failed is something the seed depends on, and starting the seed render against a missing CRD/RBAC/webhook would crash-loop or silently no-op the seed controller. So under `ShootFirst` **both** partial and complete shoot failures gate the seed render. Partial vs complete still differ in *diagnostics* (which resources failed, and the requeue path) but not in the seed-gating decision.

- **Partial failure** — the shoot client was built and at least one shoot resource applied, but some failed. Under `ShootFirst` the reconciler SHALL **stop before applying the seed render** (the failed structural resources are seed dependencies), record the failed shoot resources as `Health=Degraded` with a `Message`, set `Ready=False` reason `ResourcesDegraded`, aggregate the per-resource errors, and requeue. Under `SeedFirst` the seed render is applied first regardless and the failures are flagged (`ResourcesDegraded`) without blocking seed.
- **Complete shoot failure** — the shoot client cannot be built or reached (e.g. shoot NotFound/unreachable, forbidden), or zero of the shoot render's resources applied. Under `ShootFirst` the reconciler SHALL **stop before applying the seed render**, set `Ready=False` reason `ShootApplyFailed`, and requeue. Under `SeedFirst` the seed render is applied first regardless and the shoot failure is flagged (`ShootApplyFailed`) but does not block seed.
- **Credentials not yet ready** — the shoot-access Secret exists but the token or CA bundle is absent or empty (the normal window before Gardener's token-requestor fills it). This is a benign bootstrap wait, **not** a failure: the reconciler SHALL set `Ready=False` reason `WaitingForShootCredentials` (progressing, not degraded) and requeue with a short backoff. Under `ShootFirst` it also **defers the seed render** (seed must not start before the shoot it depends on); under `SeedFirst` the seed render is applied first regardless.

`SeedFirst` never blocks the seed render on a shoot outcome — the consumer has declared seed does not depend on shoot coming up first. Only `ShootFirst` gates the seed render on shoot convergence; under `ShootFirst` *any* incomplete shoot outcome (partial, complete, or credentials-not-ready) gates seed.

#### Scenario: ShootFirst — complete shoot failure stops before seed

- **WHEN** a CR with `spec.applyOrder: ShootFirst` (or unset) is reconciled and the shoot render cannot be applied at all (shoot NotFound/unreachable, forbidden, or zero resources applied)
- **THEN** the seed render is NOT applied this cycle
- **AND** the `Ready` condition is `False` with reason `ShootApplyFailed`
- **AND** the reconcile requeues

#### Scenario: ShootFirst — credentials not ready defers seed

- **WHEN** a CR with `spec.applyOrder: ShootFirst` (or unset) is reconciled and its shoot-access Secret exists but the token or CA is absent or empty
- **THEN** neither the shoot nor the seed render is applied this cycle
- **AND** the `Ready` condition is `False` with reason `WaitingForShootCredentials` (progressing, not degraded)
- **AND** the reconcile requeues with a short backoff

#### Scenario: ShootFirst — partial shoot failure also stops before seed

- **WHEN** a CR with `spec.applyOrder: ShootFirst` (or unset) is reconciled, the shoot client is built, and at least one shoot resource applies while some fail
- **THEN** the seed render is NOT applied this cycle (the failed shoot resources are structural dependencies the seed consumes)
- **AND** the failed shoot resources are recorded `Health=Degraded` with a `Message`
- **AND** the `Ready` condition is `False` with reason `ResourcesDegraded`
- **AND** the reconcile requeues

#### Scenario: SeedFirst — seed applies despite partial shoot failure

- **WHEN** a CR with `spec.applyOrder: SeedFirst` is reconciled and the shoot render has partial per-resource failures
- **THEN** the seed render is still applied first
- **AND** the failed shoot resources are flagged `ResourcesDegraded` without blocking the seed render

#### Scenario: SeedFirst — seed applies regardless of shoot outcome

- **WHEN** a CR with `spec.applyOrder: SeedFirst` is reconciled and the shoot render is a complete failure or credentials are not ready
- **THEN** the seed render is still applied first
- **AND** the shoot failure is flagged (`ShootApplyFailed`) or the wait is flagged (`WaitingForShootCredentials`) without blocking the seed render

#### Scenario: Credentials populated on a later reconcile proceed normally

- **WHEN** a subsequent reconcile finds the token and CA present and non-empty
- **THEN** the shoot client is built and the shoot render is applied normally

### Requirement: Cross-render apply order via spec.applyOrder

The reconciler SHALL apply the two renders to their target clusters in the order given by `spec.applyOrder` (`ShootFirst` default, or `SeedFirst`). Whether the second render proceeds after the first render has failures depends on the ordering: under `SeedFirst` the seed render (first) is always applied and the shoot render (second) follows regardless of seed per-resource failures; under `ShootFirst` the seed render (second) is **gated on the shoot render (first) fully converging** — *any* incomplete shoot outcome (partial per-resource failure, complete failure, or credentials-not-ready) stops or defers the seed render per the "Shoot failure severity respects spec.applyOrder" requirement, because the workless shoot's shoot render is entirely structural dependencies (CRDs/RBAC/webhooks) the seed consumes. `SeedFirst` never gates the seed render on the shoot outcome. Intra-render kind ordering remains the fixed built-in priority and is not affected by `applyOrder`.

`spec.applyOrder` governs cross-render sequencing for both directions: **all deletion — both per-reconcile prune and CR-deletion teardown — SHALL process the two renders in the reverse of `spec.applyOrder`.** Under the default `ShootFirst`, apply sequences shoot-then-seed, so deletion sequences seed-then-shoot (the seed controller stops before the shoot CRDs/RBAC it depends on are removed); under `SeedFirst` the reverse holds. This cross-render reversal is independent of, and composes with, the fixed intra-render kind-priority reversal already applied within each render.

#### Scenario: ShootFirst applies shoot render before seed render

- **WHEN** a CR with `spec.applyOrder: ShootFirst` (or unset) is reconciled
- **THEN** the shoot render is applied to the shoot cluster before the seed render is applied to the seed cluster

#### Scenario: SeedFirst applies seed render before shoot render

- **WHEN** a CR with `spec.applyOrder: SeedFirst` is reconciled
- **THEN** the seed render is applied to the seed cluster before the shoot render is applied to the shoot cluster

#### Scenario: Deletion reverses the cross-render apply order

- **WHEN** a CR with `spec.applyOrder: ShootFirst` (or unset) is deleted, or a reconcile prunes orphans from both renders
- **THEN** the seed render's resources are deleted before the shoot render's resources (the reverse of the apply order)

#### Scenario: SeedFirst deletion order is shoot-then-seed

- **WHEN** a CR with `spec.applyOrder: SeedFirst` is deleted, or a reconcile prunes orphans from both renders
- **THEN** the shoot render's resources are deleted before the seed render's resources (the reverse of the apply order)

#### Scenario: SeedFirst — shoot render still applied despite seed per-resource failures

- **WHEN** a CR with `spec.applyOrder: SeedFirst` is reconciled and the seed render (first) has one or more per-resource apply failures but is not a complete failure (at least one resource applied)
- **THEN** the shoot render (second) is still applied
- **AND** all failures are aggregated into status

### Requirement: Continue-on-error aggregation across a render

When applying a render, the reconciler SHALL attempt every resource, recording each resource's `ResourceStatus` (including `Health=Degraded` and a `Message` for failures), and SHALL aggregate all per-resource errors so the reconcile requeues while still applying the healthy resources. A single failing resource MUST NOT abort the remaining resources in that render. A cluster-scoped seed object that the delivery layer refuses to apply because it is owned by a *different* CR (see the resource-delivery "Cluster-scoped apply is guarded against cross-CR conflict" requirement) is one such per-resource failure: it is recorded `Health=Degraded` with a `Message` naming the conflicting owner and aggregated like any other, never aborting the batch.

#### Scenario: One failing resource does not block the batch

- **WHEN** applying a render where one resource fails (e.g. transient conflict or invalid object) and the others succeed
- **THEN** the successful resources are applied
- **AND** the failing resource is recorded with `Health=Degraded` and a `Message`
- **AND** the reconcile returns an aggregated error and requeues

#### Scenario: Cross-CR cluster-scoped conflict is a continue-on-error failure

- **WHEN** applying a seed render whose cluster-scoped object is already owned by a different CR, so the delivery layer refuses to apply it
- **THEN** that object is recorded with `Health=Degraded` and a `Message` identifying the conflicting owner
- **AND** the remaining seed resources are still applied
- **AND** the reconcile returns an aggregated error and requeues (it does not clobber the foreign-owned object)

### Requirement: Prune resources that leave a render

After applying each render, the reconciler SHALL prune orphans: it SHALL diff the previous applied-set (recorded in `status.seedResources` / `status.shootResources`) against the new render using a version-independent identity key `group/kind/namespace/name`, and delete resources present before but absent now, on that render's own target client. Within each render the orphans SHALL be deleted in reverse of the fixed intra-render kind priority; across the two renders the prune SHALL process them in the reverse of `spec.applyOrder` (the same cross-render reversal used by CR-deletion teardown). A `CustomResourceDefinition` orphan SHALL NOT be deleted while `spec.retentionPolicy.crds` is `Retain`.

**Prune runs before every status write, on every terminating path, for every render applied this cycle.** The reconciler SHALL NOT persist a render's inventory to `status.seedResources` / `status.shootResources` before that render's prune has run in the same reconcile. This invariant holds on **all** terminating paths, including early returns taken when the shoot render is degraded — not only the happy path. A render's status is always written post-prune, so the previous applied-set read on the next reconcile reflects post-prune reality and never silently drops an orphan.

**Apply-vs-skip determines prune-vs-preserve.** A render **applied this cycle** SHALL be pruned before its status is written. A render **not applied this cycle** (deferred on a benign wait such as `WaitingForShootCredentials`, or whose client could not be built, `ShootApplyFailed` with zero resources applied) SHALL NOT be pruned, and its prior inventory (`status.seedResources` / `status.shootResources` as read at the top of the reconcile) SHALL be preserved verbatim. Pruning a render that was never applied — against a cluster known to be unreachable or in a bad state — is forbidden because it would issue deletes for resources based on a render that was not applied.

Prune acts only on the set difference `previous − current` and its only action is deletion; it iterates the previous applied-set, never the current render. **Additions are delivered by the apply step and are invisible to prune**: a render that only adds resources produces an empty prune set, and prune ordering relative to the status write cannot drop, duplicate, or delay an addition. A mixed add-and-remove render is correct because additions are driven by the current render (apply) and removals by the previous applied-set (prune), with disjoint effects.

A failed prune SHALL leave the orphan tracked in status (appended to the render's applied-set as retained) and retried on the next reconcile. On a degraded-shoot early-return path, a prune error SHALL be aggregated into the reconcile's returned error (matching happy-path prune-error handling and triggering requeue), but SHALL NOT overwrite the `Ready` condition reason that reflects the degraded resource (`ResourcesDegraded` / `ShootApplyFailed`).

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

#### Scenario: ShootFirst degraded shoot render prunes its orphan the same cycle

- **WHEN** a CR with `spec.applyOrder: ShootFirst` (or unset) is reconciled, the shoot render both removes a previously-applied resource and contains a degraded resource (triggering the degraded-shoot early return)
- **THEN** the removed shoot resource is pruned (deleted from the shoot cluster) in that same reconcile, before status is written
- **AND** the removed resource is absent from `status.shootResources` because it was deleted, not because status was trimmed before prune
- **AND** the `Ready` condition reason reflects the degraded resource (`ResourcesDegraded` or `ShootApplyFailed`), not a prune error

#### Scenario: Skipped render is preserved verbatim, not pruned

- **WHEN** a render is not applied this cycle (shoot deferred on `WaitingForShootCredentials`, or shoot client unbuildable, or the seed render is deferred under `ShootFirst` on those paths)
- **THEN** that render's prune does NOT run
- **AND** its prior inventory (`status.seedResources` / `status.shootResources` as read at the top of the reconcile) is written back verbatim, never emptied or trimmed

#### Scenario: Addition is unaffected by prune ordering

- **WHEN** a render only adds a resource (no previously-applied resource leaves the render)
- **THEN** the prune set is empty and prune deletes nothing
- **AND** the added resource is applied and recorded in status regardless of prune-vs-status-write ordering

### Requirement: Status population and Ready condition

After apply and prune, the reconciler SHALL populate `status.seedResources`, `status.shootResources`, a `LastReconcile` timestamp, and a `Ready` condition aggregated from all per-resource health: `True` when all resources are `Healthy`, `False` with reason `ResourcesDegraded` when any is `Degraded`, and `False` with reason `Progressing` when any is `Progressing` and none is `Degraded`.

**Every status write reflects post-prune inventory for renders applied this cycle and preserved prior inventory for renders skipped this cycle.** The reconciler SHALL NOT write a render's inventory to status before that render's prune has run in the same reconcile (see the prune requirement). On any early-return path (degraded shoot, waiting for credentials, unbuildable client), the status write SHALL either reflect the post-prune inventory of a render applied this cycle, or preserve verbatim the prior inventory of a render not applied this cycle. In particular, an early-return path SHALL NOT persist an empty or current-render-only inventory for a render that was not applied — doing so would silently drop the render's tracked resources from the applied-set. This closes the seed-inventory asymmetry whereby, under `ShootFirst` on the `WaitingForShootCredentials` / `ShootApplyFailed` (unbuildable client) paths, the seed render is deferred and its prior `status.seedResources` MUST be preserved rather than emptied.

#### Scenario: Ready is True when all resources healthy

- **WHEN** every applied resource across both renders reports `Healthy`
- **THEN** the CR's `Ready` condition is `True`

#### Scenario: Ready is False when any resource degraded

- **WHEN** at least one applied resource reports `Degraded`
- **THEN** the CR's `Ready` condition is `False` with reason `ResourcesDegraded`

#### Scenario: Status records the applied set for the next prune

- **WHEN** a reconcile completes apply and prune
- **THEN** `status.seedResources` and `status.shootResources` reflect the resources of the render just applied
- **AND** `status.lastReconcile` is set to the reconcile time

#### Scenario: Deferred seed inventory is preserved on ShootFirst benign-wait paths

- **WHEN** a CR with `spec.applyOrder: ShootFirst` (or unset) is reconciled and the shoot render is deferred (`WaitingForShootCredentials`) or its client is unbuildable, so the seed render is not applied this cycle
- **THEN** `status.seedResources` is written back verbatim from the prior applied-set (as read at the top of the reconcile)
- **AND** it is NOT emptied or replaced with a current-render-only list

#### Scenario: Early-return status write never trims before prune

- **WHEN** any early-return path writes status for a render that was applied this cycle
- **THEN** that render's prune has already run before the status write
- **AND** `status` reflects the post-prune applied-set, so a resource removed from the render is deleted this cycle rather than dropped from the applied-set

### Requirement: Finalizer-driven deletion with reverse cross-render order

The reconciler SHALL add a finalizer on first reconcile and, on CR deletion, tear down resources in the reverse of `spec.applyOrder`. Building the shoot client SHALL NOT be a precondition for teardown: if the shoot client cannot be built or reached, the reconciler SHALL record a `ShootUnreachable` condition and Kubernetes Event and SHALL still proceed with seed cleanup (seed teardown does not depend on shoot reachability). Shoot resources SHALL be deleted explicitly via the shoot client when it is available (respecting `spec.retentionPolicy.crds` for CRDs); seed resources SHALL be deleted on every pass regardless of shoot reachability. The two renders SHALL continue to be torn down in the reverse of `spec.applyOrder`.

The reconciler SHALL remove the finalizer only when shoot cleanup is confirmed complete (every shoot resource deleted or observed NotFound), OR when the operator has explicitly consented to abandoning shoot cleanup by setting the annotation `dual-deployment-operator.cc.sap/force-delete: "true"` on the CR. While shoot cleanup is outstanding and no such annotation is present, the reconciler SHALL retain the finalizer, requeue, and surface a `ShootCleanup` condition with reason `Blocked` plus a `ShootCleanupBlocked` Kubernetes Event naming the override annotation — keeping the CR in `Terminating` rather than silently orphaning live shoot resources. The reconciler SHALL NOT auto-remove the finalizer after any timeout: this operator has no post-finalizer retry agent, so a timeout would turn a transient shoot outage into permanent, unretried orphaning.

When the `force-delete` annotation is set with shoot cleanup outstanding, the reconciler SHALL complete seed cleanup, remove the finalizer, and surface a `ShootCleanup` condition with reason `ForceDeleted` plus a distinct `ShootCleanupForceDeleted` Kubernetes Event recording that remaining shoot resources may be orphaned by explicit operator action. This annotation-driven path is the supported alternative to a raw `kubectl patch ... finalizers:[]`, which would delete the CR outright and skip seed cleanup.

#### Scenario: Finalizer added on first reconcile

- **WHEN** a CR without the operator finalizer is reconciled
- **THEN** the finalizer is added and the CR is updated before delivery proceeds

#### Scenario: Shoot resources deleted on CR deletion, CRDs retained

- **WHEN** a CR with `spec.retentionPolicy.crds: Retain` is deleted and the shoot is reachable
- **THEN** all non-CRD shoot resources are deleted via the shoot client in reverse-delete order
- **AND** CustomResourceDefinitions on the shoot are retained
- **AND** the finalizer is removed only after shoot deletion succeeds

#### Scenario: Reverse cross-render order honored on deletion

- **WHEN** a CR with `spec.applyOrder: ShootFirst` (or unset) is deleted and the shoot is reachable
- **THEN** the seed render's resources are deleted before the shoot render's resources
- **WHEN** a CR with `spec.applyOrder: SeedFirst` is deleted and the shoot is reachable
- **THEN** the shoot render's resources are deleted before the seed render's resources

#### Scenario: Unreachable shoot does not block seed cleanup

- **WHEN** the shoot client cannot be built or reached during CR deletion
- **THEN** seed resources are still deleted on that reconcile pass (no early return)
- **AND** a `ShootUnreachable` condition and Kubernetes Event are set on the CR
- **AND** the shoot resources are NOT assumed gone

#### Scenario: Finalizer blocks until shoot cleanup completes

- **WHEN** shoot cleanup is outstanding (shoot unreachable, or one or more shoot deletes failed) and the `force-delete` annotation is NOT set
- **THEN** the finalizer is NOT removed
- **AND** a `ShootCleanup` condition with reason `Blocked` and a `ShootCleanupBlocked` Event are set, naming the `dual-deployment-operator.cc.sap/force-delete` annotation
- **AND** the reconcile requeues so cleanup completes if the shoot returns
- **AND** the CR stays in `Terminating`

#### Scenario: No timeout auto-removes the finalizer

- **WHEN** shoot cleanup remains outstanding for an arbitrarily long time and no `force-delete` annotation is set
- **THEN** the finalizer is never auto-removed on the basis of elapsed time
- **AND** the CR remains in `Terminating`, still retrying, until the shoot is reachable or the operator sets the annotation

#### Scenario: force-delete annotation completes deletion and orphans shoot resources deliberately

- **WHEN** the shoot is unreachable and the operator sets `dual-deployment-operator.cc.sap/force-delete: "true"` on the CR
- **THEN** seed cleanup completes
- **AND** the finalizer is removed and the CR is deleted
- **AND** a `ShootCleanup` condition with reason `ForceDeleted` and a distinct `ShootCleanupForceDeleted` Event are set, recording that remaining shoot resources may be orphaned


### Requirement: Reconciler replaces the no-op behavior

The `DualDeploymentOperator` reconciler SHALL perform the full render → transform → deliver → prune → status pipeline described above, replacing the prior no-op reconcile that only fetched the CR and requeued.

#### Scenario: Reconcile delivers resources rather than no-op

- **WHEN** a valid CR is reconciled
- **THEN** the operator renders, transforms, and applies resources to the seed and shoot clusters and populates status
- **AND** it does not merely fetch the CR and return

---

### Requirement: Periodic drift-correction requeue

On a successful reconcile the reconciler SHALL requeue after a fixed interval (default 10 minutes) so the render is periodically re-applied for drift correction; the periodic re-apply MUST continue to omit `caBundle` so it never reverts the webhook-injector's value.

#### Scenario: Successful reconcile requeues for drift correction

- **WHEN** a reconcile completes successfully
- **THEN** it requeues after the fixed interval (default 10 minutes)

---

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
