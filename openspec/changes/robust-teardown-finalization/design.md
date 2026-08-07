<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Design: robust-teardown-finalization

## Problem

`reconcileDelete` (`internal/controller/dualdeploymentoperator_controller.go:262`) builds the
shoot applier **first** and returns early (`RequeueAfter=30s`, finalizer retained, no seed
teardown) whenever the shoot client cannot be built — e.g. the shoot API server is unreachable,
or the shootAccess Secret is absent. Because seed teardown never runs and the finalizer is
never removed, the CR is stuck in `Terminating` indefinitely. The only way out is a manual
`kubectl patch ... finalizers:[]`, which removes the finalizer with shoot cleanup still
outstanding — silently orphaning whatever shoot resources remained.

This is realistic: a CR is frequently deleted *because* its shoot is going away, so
"shoot unreachable at delete time" is a common case, not an edge case.

## Root cause

The delete path treats "can I reach the shoot?" as a precondition for doing *any* teardown.
It is not: seed cleanup is independent of shoot reachability, and blocking the finalizer
forever on an unreachable shoot (with no way to complete deletion short of a raw finalizer
wipe) is both a correctness bug (deadlock) and a safety bug (the manual wipe silently orphans).

## Scope stance

A behavioral bug fix in `reconcileDelete` plus reconcile-pipeline logging. Deliberately **not**
in scope:

- No `spec.applyOrder` change — default, doc-comment, and the reverse-order teardown it drives
  are all left exactly as today. Reverse-order deletion is a wanted generic capability.
- No CRD schema change — the override is an annotation, not a spec field.
- No `internal/deliver` change; no credential-preservation logic (the shootAccess Secret is
  install-chart-owned, so seed teardown never touches it).

`DualDeploymentOperator` is generic; `metal-operator-remote` is only the motivating case. All
behavior keys off generic CR fields (`spec.shootAccess.secretName`, `Status.SeedResources`,
`Status.ShootResources`), never operator name / chart labels / `metal-*` identifiers.

## Approach

### 1. `reconcileDelete` control flow (`internal/controller/…_controller.go`)

Stop hard-returning on shoot-applier build failure. Record `ShootUnreachable` and continue.
Keep the existing reverse-of-`applyOrder` teardown order. Decide the finalizer with a
three-way policy: complete, force-delete override, or block-and-retry.

```go
func (r *DualDeploymentOperatorReconciler) reconcileDelete(ctx context.Context, cr *ddov1alpha1.DualDeploymentOperator) (ctrl.Result, error) {
    ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

    // Build the shoot applier; on failure, record ShootUnreachable but DO NOT return.
    shootApplier, buildErr := r.buildShootApplierOrDefault(ctx, cr)
    if buildErr != nil {
        recordShootUnreachable(cr, buildErr)   // Warning event + Ready=False (non-fatal)
    }

    deleteRender := func(applier deliver.Applier, statuses []ddov1alpha1.ResourceStatus) []error {
        if applier == nil {
            return nil                          // shoot unreachable this pass: skip, retry later
        }
        var errs []error
        for _, rs := range deliver.SortStatusForDelete(statuses) {
            if rs.Kind == "CustomResourceDefinition" && cr.Spec.RetentionPolicy.CRDs != "Delete" {
                continue                        // existing CRD-retention rule, unchanged
            }
            if e := applier.Delete(ctx, deliver.ManifestFromStatus(rs), ownedBy); e != nil {
                errs = append(errs, e)
            }
        }
        return errs
    }

    // Tear down in the reverse of spec.applyOrder — UNCHANGED generic behavior.
    var shootErrs, seedErrs []error
    if cr.Spec.ApplyOrder == "SeedFirst" {
        shootErrs = deleteRender(shootApplier, cr.Status.ShootResources)
        seedErrs = deleteRender(r.SeedApplier, cr.Status.SeedResources)
    } else {
        seedErrs = deleteRender(r.SeedApplier, cr.Status.SeedResources)
        shootErrs = deleteRender(shootApplier, cr.Status.ShootResources)
    }

    // Real seed-delete errors retry via controller-runtime backoff.
    if agg := kerrors.NewAggregate(seedErrs); agg != nil {
        return ctrl.Result{}, agg
    }

    shootDone := buildErr == nil && len(shootErrs) == 0
    forceDelete := cr.Annotations[ForceDeleteAnnotation] == "true"

    switch {
    case shootDone:
        controllerutil.RemoveFinalizer(cr, FinalizerName)
        return ctrl.Result{}, r.Update(ctx, cr)
    case forceDelete:
        recordForceDeleted(cr, buildErr, kerrors.NewAggregate(shootErrs))
        controllerutil.RemoveFinalizer(cr, FinalizerName)
        return ctrl.Result{}, r.Update(ctx, cr)
    default:
        // Shoot cleanup outstanding, no override: RETAIN the finalizer, keep retrying,
        // and surface a loud, auditable ShootCleanupBlocked condition.
        recordShootCleanupBlocked(cr, buildErr, kerrors.NewAggregate(shootErrs))
        return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
    }
}
```

Changes vs. today:
- The early `return` on shoot-applier build failure is removed; the error becomes a non-fatal
  `ShootUnreachable` record.
- `deleteRender` gains a `nil`-applier guard so the shoot side is skipped (not panicked) when
  the applier could not be built. No new parameter.
- Seed and shoot delete errors are tracked separately so the finalizer policy can distinguish
  "seed failed → retry with backoff" from "shoot outstanding → block".
- A three-way finalizer switch replaces the unconditional `RemoveFinalizer`.
- Teardown order still honors `spec.applyOrder` in reverse — unchanged.

### 2. Override annotation & signals

- `const ForceDeleteAnnotation = "dual-deployment-operator.cc.sap/force-delete"`. Set to
  `"true"` on the CR, it removes the finalizer even though shoot cleanup is outstanding — an
  explicit, auditable human consent to orphaning the remaining shoot resources, replacing the
  raw `kubectl patch ... finalizers:[]`. No blind timeout: the finalizer is retained
  indefinitely until shoot cleanup succeeds or the operator opts in.
- `recordShootUnreachable(cr, err)`: existing Warning event (`ShootUnreachable`) + `Ready=False`;
  now **non-fatal** — the delete proceeds to seed cleanup instead of returning.
- `recordShootCleanupBlocked(cr, buildErr, deleteErrs)`: **new** Warning event
  (`ShootCleanupBlocked`) + condition `{Type: "ShootCleanup", Status: False, Reason: "Blocked"}`,
  stating the deletion is waiting on shoot cleanup and naming the override annotation. Emitted
  each retry while blocked. `Ready` unchanged.
- `recordForceDeleted(cr, buildErr, deleteErrs)`: **new** Warning event
  (`ShootCleanupForceDeleted`) + condition
  `{Type: "ShootCleanup", Status: False, Reason: "ForceDeleted"}`, noting shoot resources may be
  orphaned by explicit operator override.

RBAC already grants `events` + `dualdeploymentoperators/status`; no marker change needed.

### 3. Reconcile-pipeline observability logging

Add structured `logr` logs (via `log.FromContext(ctx)`) at the pipeline stage boundaries,
following the K8s logging message-style guidelines (capitalized, no trailing period, past
tense, object type named, balanced key/value pairs). Levels: `Info` (V(0)) for
once-per-reconcile milestones; `V(1)` for per-resource detail. No secret material or token
bytes are ever logged.

| Stage | Location | Keys |
|---|---|---|
| Source pull (OCI chart / git kustomization) — start + result | `internal/source` | `sourceKind`, `ref`, `resolvedID` |
| Render + validation, per mode | `internal/controller` / `internal/source` | `mode`, `manifests`, `transform` |
| Render-cache hit / miss | `internal/source` (`Deps.RenderCache`) | `resolvedID`, `cache` |
| Apply / prune summary, per target | `internal/controller` | `cluster`, `applied`, `degraded`, `pruned` |
| Delete progress + finalizer decision | `internal/controller` (`reconcileDelete`) | `cluster`, `deleted`, `shootDone`, `forceDelete` |

Observability-only; no behavior change; no new dependency.

## User documentation

The `force-delete` annotation is an operator-facing operational contract and MUST be
documented, not left as an implementation detail. Add a "Deleting a DualDeploymentOperator"
section to `docs/design.md` (the operator's authoritative behavior doc) covering:

- Normal deletion: shoot + seed torn down in reverse of `spec.applyOrder`, then the finalizer
  is removed.
- Blocked deletion: when the shoot is unreachable, seed cleanup still completes but the
  finalizer is **retained** and the CR stays `Terminating`; the `ShootCleanup=Blocked`
  condition + `ShootCleanupBlocked` event explain why and name the escape hatch.
- Force-delete: set `dual-deployment-operator.cc.sap/force-delete: "true"` on the CR to complete
  deletion when the shoot is permanently gone. Documents that this **orphans the remaining
  shoot resources** (an explicit, deliberate action), that it is preferable to a raw
  `kubectl patch ... finalizers:[]` (which would also skip seed cleanup), and that it emits a
  `ShootCleanupForceDeleted` event/condition for audit.
- Example: `kubectl annotate dualdeploymentoperator <name> dual-deployment-operator.cc.sap/force-delete=true`.

## Scope

- `internal/controller/` — `reconcileDelete` control flow (no hard-return, `nil`-applier guard,
  separate seed/shoot error tracking, block/override finalizer switch), `ForceDeleteAnnotation`
  const, the three signal helpers, and apply/prune/delete logging.
- `internal/source/` — source-pull + render-cache hit/miss logging.
- `docs/design.md` — new "Deleting a DualDeploymentOperator" section documenting the deletion
  states and the `force-delete` annotation contract (see User documentation).
- No `api/v1alpha1/` change. No `internal/deliver/` change. No new CRD status field. No timeout
  const. No `deleteRender` skip parameter. No credential-preservation predicate.

## Testing (TDD, RED first)

The existing test `retains finalizer and sets ShootUnreachable when Secret is missing`
(`…_controller_test.go:404`) encodes the buggy behavior (hard-return, seed teardown skipped) and
is **replaced**.

1. **Shoot unreachable, no override**: shoot-applier build fails → `ShootUnreachable` recorded;
   seed resources are still deleted (no hard-return); finalizer **retained**;
   `ShootCleanupBlocked` condition set; `RequeueAfter=30s`.
2. **Shoot unreachable, `force-delete` annotation set**: seed cleanup completes; finalizer
   removed; `ShootCleanupForceDeleted` event + `ShootCleanup{Reason: ForceDeleted}` condition.
3. **Shoot reachable, happy path**: shoot + seed pruned; finalizer removed.
4. **Reverse-order honored** under both `applyOrder` values: `SeedFirst` → shoot-then-seed
   delete; `ShootFirst` → seed-then-shoot delete (guards the unchanged generic capability).
5. **Logging**: verified by inspection that each stage emits at the specified level and that no
   token/secret bytes appear in output (not asserted line-by-line).

## Effort / risk

Small–medium (~0.5–1.5 days incl. tests). The fix is a localized control-flow change in one
function plus additive logging — no CRD change, no `applyOrder` change, no `internal/deliver`
change. It follows generic Kubernetes finalizer guidance (retain until external cleanup
succeeds; never auto-remove a finalizer with cleanup outstanding). Orphaning is possible only
via an explicit human-set `force-delete` annotation. The blocking model deliberately diverges
from the Flux `WaitForTermination` timeout precedent because this operator has **no
post-finalizer retry agent** (unlike the gardener-resource-manager system it replaces): a blind
timeout here would turn a transient shoot outage into permanent, unretried orphaning.
