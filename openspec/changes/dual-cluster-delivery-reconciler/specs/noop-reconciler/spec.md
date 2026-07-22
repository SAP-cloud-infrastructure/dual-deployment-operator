<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## REMOVED Requirements

### Requirement: Reconcile logs one info line and requeues at 10 minutes

**Reason**: The no-op reconcile is replaced by the full render → transform → deliver → prune → status pipeline defined in the `reconcile-loop` capability. The reconciler no longer merely logs `"reconciling"` and returns; it now performs real work and populates status. The 10-minute drift-correction requeue is retained, but as part of the `reconcile-loop` capability's periodic-requeue requirement, not as a standalone log-and-requeue stub.

**Migration**: Behavior moves to the `reconcile-loop` capability (requirements "Two-render reconcile pipeline", "Status population and Ready condition", and "Periodic drift-correction requeue"). No CR-facing migration is required; the requeue cadence (10 minutes) is unchanged.

### Requirement: Reconcile does not mutate the CR

**Reason**: The no-op stub was defined as a pure wiring stub that made no writes. The `reconcile-loop` capability now writes `status` (host/remote resources, conditions, `lastReconcile`) and manages a finalizer for deletion, so this "must not mutate" requirement directly contradicts the new behavior and is removed.

**Migration**: Status writes and finalizer management are now specified by the `reconcile-loop` capability ("Status population and Ready condition", "Finalizer-driven deletion with reverse cross-render order"). Consumers that relied on the CR being unmodified must instead observe `status` and the operator finalizer as the source of truth.
