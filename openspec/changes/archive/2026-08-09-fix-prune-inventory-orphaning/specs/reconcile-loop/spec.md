<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## MODIFIED Requirements

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
