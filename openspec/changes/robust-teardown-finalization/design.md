<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Design: robust-teardown-finalization

> Pre-filled from the brainstorm session. Refine during the `design` artifact step.

## Problem

`reconcileDelete` (`internal/controller/dualdeploymentoperator_controller.go:274`) builds
the shoot applier **first** and **hard-returns** (`RequeueAfter=30s`, finalizer retained,
no seed teardown) whenever the shoot client cannot be built — e.g. when the shoot API is
unreachable or the shootAccess Secret is absent. Seed teardown never runs, the finalizer is
never removed, and the CR is stuck `Terminating` forever (requiring a manual finalizer wipe
that silently orphans shoot resources).

**Historical note (self-deadlock, now structurally fixed).** The issue was originally filed
because the shootAccess Secret used to be part of the workload's **seed render**, so DDO's own
seed teardown deleted the very credential its shoot teardown needed — a guaranteed deadlock on
every delete. Feature-branch commits `7bb8fb9`/`9c09682`/`906d936` moved the token-requestor
Secret + shoot-applier RBAC into the **operator install chart** (`chart/templates/shoot-rbac/`,
`shootRbac.enabled`), so the credential no longer lives in any CR's seed render. That eliminates
the self-deadlock by construction.

**What remains** — and what this change fixes — is the generic case: if the shoot is genuinely
unreachable/gone at CR-delete time (shoot API down, network partition, shoot being torn down,
or the operator install chart uninstalled first, removing the credential Secret), the
shoot-applier build fails and `reconcileDelete` hard-returns, deadlocking the delete. This is
realistic (a CR is often deleted *because* its shoot is going away) and reachable.

## Scope stance (narrow, generic)

This is a **behavioral bug fix in `reconcileDelete` plus reconcile-pipeline logging** — nothing
more. It deliberately does **not**:

- touch `spec.applyOrder` (no default flip, no doc-comment reframing);
- change the teardown order — **reverse-order deletion driven by `applyOrder` is kept as-is**,
  a wanted generic capability for consumers with a real teardown ordering dependency;
- add any CRD schema change (the force-delete override is an annotation);
- add a credential-preservation predicate (the self-deadlock is gone by construction — the
  credential now lives in the operator install chart, so seed teardown never touches it).

`DualDeploymentOperator` is a generic operator; `metal-operator-remote` is only the motivating
case. All behavior keys off generic CR fields (`spec.shootAccess.secretName`,
`Status.SeedResources`/`ShootResources`), never operator name / chart labels / `metal-*`
identifiers. Metal-specific deployment facts (hardcoded `applyOrder: SeedFirst`, GRM shoot-RBAC
bootstrap, token-requestor Secret in the install chart) stay in the chart; this change adds no
chart-aware or GRM-aware logic.

## Approach

### 1. No hard-return on shoot-client build failure (`internal/controller/…_controller.go`)

`reconcileDelete` keeps its existing structure — including **reverse-of-`applyOrder`
teardown order** — but stops hard-returning when the shoot applier cannot be built. Instead it
records `ShootUnreachable` and proceeds to seed cleanup. Seed cleanup no longer touches the
shoot credential (install-chart-owned), so it is safe to run whether or not the shoot is
reachable.

```go
ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

// Build the shoot applier; on failure, record but DO NOT return.
shootApplier, shootBuildErr := r.buildShootApplierOrDefault(ctx, cr)
if shootBuildErr != nil {
    recordShootUnreachable(cr, shootBuildErr)   // Warning event + Ready=False (non-fatal)
}

// Tear down in the reverse of spec.applyOrder — UNCHANGED generic behavior.
// deleteRender is a no-op for the shoot side when shootApplier is nil (build failed).
var shootErrs, seedErrs []error
deleteShoot := func() {
    if shootApplier != nil {
        shootErrs = deleteRender(shootApplier, cr.Status.ShootResources)
    }
}
deleteSeed := func() { seedErrs = deleteRender(r.SeedApplier, cr.Status.SeedResources) }

if cr.Spec.ApplyOrder == "SeedFirst" {
    deleteShoot(); deleteSeed()   // reverse of SeedFirst
} else {
    deleteSeed(); deleteShoot()   // reverse of ShootFirst
}

// Real seed-delete errors retry via controller-runtime backoff.
if agg := kerrors.NewAggregate(seedErrs); agg != nil {
    return ctrl.Result{}, agg
}

shootDone := shootApplier != nil && len(shootErrs) == 0
forceDelete := cr.Annotations[ForceDeleteAnnotation] == "true"

// Finalizer policy — block until clean, or explicit human override.
switch {
case shootDone:
    controllerutil.RemoveFinalizer(cr, FinalizerName)
    return ctrl.Result{}, r.Update(ctx, cr)
case forceDelete:
    recordForceDeleted(cr, kerrors.NewAggregate(shootErrs))  // distinct Warning event + condition
    controllerutil.RemoveFinalizer(cr, FinalizerName)
    return ctrl.Result{}, r.Update(ctx, cr)
default:
    // Shoot cleanup incomplete and no override: RETAIN the finalizer indefinitely,
    // keep retrying, and surface a loud, auditable ShootCleanupBlocked condition.
    recordShootCleanupBlocked(cr, shootBuildErr, kerrors.NewAggregate(shootErrs))
    return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}
```

Notes:
- The existing CRD-retention skip rule (`RetentionPolicy.CRDs != "Delete"`) inside
  `deleteRender` is preserved.
- `deleteRender` is unchanged (no new `skip`/predicate parameter). The only new inputs are the
  `nil`-applier guard and the finalizer-policy switch.
- Teardown order still honors `spec.applyOrder` in reverse — unchanged, generic.

### 2. Override annotation & signals

- `const ForceDeleteAnnotation = "dual-deployment-operator.cc.sap/force-delete"`. When set to
  `"true"` on the CR, the operator removes the finalizer even though shoot cleanup is incomplete
  — an explicit, auditable human consent to orphaning the remaining shoot resources. This
  replaces the unsafe bare `kubectl patch ... finalizers:[]` with a supported, signalled action.
  **No blind timeout**: the finalizer is retained indefinitely until shoot cleanup succeeds or
  an operator opts in via this annotation.
- `recordShootUnreachable(cr, err)`: existing Warning event (`ShootUnreachable`) + `Ready=False`;
  now **non-fatal** (falls through to seed cleanup instead of hard-returning).
- `recordShootCleanupBlocked(cr, buildErr, deleteErrs)`: **new** Warning event
  (`ShootCleanupBlocked`) + dedicated status condition
  `{Type: "ShootCleanup", Status: False, Reason: "Blocked"}` stating the CR deletion is waiting
  on shoot cleanup and naming the annotation escape hatch. Emitted each retry while blocked.
  `Ready` is left unchanged.
- `recordForceDeleted(cr, deleteErrs)`: **new distinct** Warning event
  (`ShootCleanupForceDeleted`) + condition
  `{Type: "ShootCleanup", Status: False, Reason: "ForceDeleted"}` noting non-CRD shoot resources
  may be orphaned by explicit operator override.

### 3. Reconcile-pipeline observability logging

The teardown deadlock was hard to diagnose live because the reconcile pipeline is quiet at the
stage boundaries. Add structured `logr` logs (via `log.FromContext(ctx)`) at the key stages,
following the K8s logging message-style guidelines (capitalized, no trailing period, past
tense, object type named, balanced key/value pairs). Levels: `Info` (V(0)) for
once-per-reconcile milestones, `V(1)` for per-resource/verbose detail. No secret material or
token bytes are ever logged.

Stages to instrument:
- **Source pull** (`internal/source`): pulling the upstream chart (OCI ref + resolved digest)
  or kustomization (git URL + resolved SHA) — start + result. Keys: `sourceKind`, `ref`,
  `resolvedID`.
- **Render / validation**: render start + doc count per mode (seed/shoot); transformation
  applied; validation outcome. Keys: `mode`, `manifests`, `transform`.
- **Render-cache hit/miss** (`internal/source`, `Deps.RenderCache`): whether the resolved
  content id hit or missed the cache. Keys: `resolvedID`, `cache` (`hit`/`miss`).
- **Apply / prune / delete** (`internal/controller`): per-target (seed/shoot) apply summary
  (applied N, degraded M), prune of orphans, and — in `reconcileDelete` — per-target delete
  progress and the block/force-delete/complete decision. Keys: `cluster`, `applied`,
  `degraded`, `pruned`, `deleted`, `shootDone`, `forceDelete`.

Observability-only; no behavior change; no new dependency.

## Scope

- `internal/controller/` — `reconcileDelete` control flow (no-hard-return, `nil`-applier guard,
  block/override finalizer policy), `ForceDeleteAnnotation` const, the three signal helpers,
  and apply/prune/delete logging.
- `internal/source/` — source-pull + render-cache hit/miss logging.
- **No `api/v1alpha1/` change** — no `applyOrder` default flip, no doc-comment change; the
  force-delete override is an annotation (no CRD schema field, no status-schema change). No
  `make manifests generate` churn.
- **No `internal/deliver/` change.** No new CRD status field. No timeout const (blocking model).
  No `deleteRender` skip parameter, no credential-preservation predicate.

## Testing (TDD, RED first)

Replace the existing test `retains finalizer and sets ShootUnreachable when Secret is missing`
(`…_controller_test.go:404`) — it encodes the buggy behavior (hard-return, no seed teardown).
New coverage:

1. **Shoot unreachable, shoot resources pending, no override**: shoot applier build fails →
   `ShootUnreachable` recorded; seed resources are still deleted (no hard-return); finalizer
   **retained**; `ShootCleanupBlocked` set; `RequeueAfter=30s`. (Deletion does not complete —
   blocking model.)
2. **Shoot unreachable, force-delete annotation set** (`dual-deployment-operator.cc.sap/force-delete: "true"`):
   seed cleanup completes; finalizer removed; `ShootCleanupForceDeleted` event +
   `ShootCleanup{Reason: ForceDeleted}` condition (shoot resources may be orphaned).
3. **Shoot reachable, happy path**: shoot + seed pruned in reverse-of-`applyOrder` order;
   finalizer removed. (Keep existing coverage; assert order is honored.)
4. **Reverse-order honored under both `applyOrder` values**: assert `SeedFirst` → shoot-then-seed
   delete, `ShootFirst` → seed-then-shoot delete (guards that the generic capability is
   unchanged).
5. **Logging**: not unit-asserted line-by-line; verified by inspection that each stage emits at
   the specified level and that no token/secret bytes appear in log output.

## Effort / risk

Small–medium (~0.5–1.5 days incl. tests). Confidence high: the fix is a localized control-flow
change in `reconcileDelete` (stop hard-returning; add a finalizer-policy switch) plus additive
logging — no CRD change, no `applyOrder` change, no `internal/deliver` change. Follows generic
Kubernetes finalizer guidance (retain until external cleanup succeeds; never auto-remove a
finalizer with cleanup outstanding). Orphaning is possible **only** via an explicit human-set
`force-delete` annotation, which replaces the unsafe bare `kubectl patch finalizers:[]`. This
deliberately diverges from the Flux `WaitForTermination` timeout precedent because this operator
has **no post-finalizer retry agent** (unlike the GRM system it replaces): a blind timeout here
would turn a transient shoot outage into permanent, unretried orphaning.
