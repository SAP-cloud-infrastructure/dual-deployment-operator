<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## MODIFIED Requirements

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
