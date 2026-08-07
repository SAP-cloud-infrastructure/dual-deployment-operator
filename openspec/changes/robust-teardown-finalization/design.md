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
the self-deadlock by construction. **What remains** is the generic case: if the shoot is genuinely
unreachable/gone at CR-delete time, the hard-return still deadlocks the delete. This change fixes
that generic case.

A deeper defect underlies this: `spec.applyOrder` (enum `{SeedFirst, ShootFirst}`, default
**`ShootFirst`**) conflates apply-convergence sequencing with teardown-dependency ordering,
and `reconcileDelete` tears down in the **reverse** of `applyOrder`. The verified-correct
value for any seed-rendered-credential operator is `SeedFirst`; the only real consumer
(`metal-operator-remote-v2`) hardcodes `applyOrder: SeedFirst` because the `ShootFirst`
default deadlocks first deploy (shoot read before seed applied → credentials not ready → seed
deferred).

## Root invariant

**Shoot access must exist before any shoot operation — on both apply and delete.** This is a
credential dependency (unidirectional), not a symmetric create/reverse-destroy ownership
dependency. Encoding delete order as "reverse of apply order" is the root conceptual bug.

## Genericity (DDO stays general-purpose)

`DualDeploymentOperator` is a generic operator serving all fleet operators; the
`metal-operator-remote` deployment is only the *motivating worst case*, never a hardcoded
assumption in the CRD or reconciler. This change preserves that boundary:

- **All reconciler behavior keys off generic CR fields only** — the delete path keys off
  `spec.shootAccess.secretName` / `Status.SeedResources` / `Status.ShootResources`; nothing is
  keyed on operator name, chart labels, or `metal-*` identifiers. Any DDO consumer benefits from
  the same teardown robustness.
- **The self-deadlock is no longer a built-in trap, so no credential-preservation predicate is
  needed.** The credential now lives in the operator install chart (not any CR's seed render), so
  seed teardown can never delete DDO's own shoot access. A hypothetical consumer that puts the
  Secret in its own seed render reintroduces the foot-gun for itself; its escape hatch is the
  generic `force-delete` override — no metal-specific handling.
- **Metal-specific deployment facts stay in the chart, out of the operator.** The
  `metal-operator-remote-v2` chart hardcodes `applyOrder: SeedFirst`, and the operator install
  chart delivers the GRM shoot-RBAC bootstrap + token-requestor Secret as install-time plumbing.
  This change references those facts only as rationale/context; it adds no chart-aware or
  GRM-aware logic to the reconciler.
- **The `applyOrder` default flip to `SeedFirst` is a generic default, not a metal carve-out.**
  `SeedFirst` is the safe default for any consumer (seed converges first, shoot converges once
  its credential is live); `ShootFirst` remains available for externally-provisioned-credential
  operators. The flip removes a default that is *actively wrong* for the seed-rendered-credential
  case (the common fleet case) without foreclosing the generic `ShootFirst` option.

## Approach (coherent minimal fix)

### 1. `applyOrder` API reframing (`api/v1alpha1/dualdeploymentoperator_types.go`)

- Change `+kubebuilder:default=ShootFirst` → `+kubebuilder:default=SeedFirst`.
- Rewrite the field doc comment: `applyOrder` governs **apply-convergence sequencing only**,
  never deletion order. `ShootFirst` is valid only when shoot credentials exist independently
  of the seed render.
- Keep `ShootFirst` in the enum (no removal).
- Regenerate: `make manifests generate` (CRD bases under `config/crd/bases/*`,
  `zz_generated.deepcopy.go`). Update `api/v1alpha1/dualdeploymentoperator_types_test.go`
  default assertion.

### 2. Delete path decoupled from `applyOrder` (`internal/controller/…_controller.go`)

`reconcileDelete` no longer reads `cr.Spec.ApplyOrder`. Teardown is always
shoot-first-then-seed:

```go
// 1. Classify: is shoot cleanup actually needed?
shootPending := deletableShootStatuses(cr)   // Status.ShootResources minus retained CRDs
shootDone := len(shootPending) == 0
var shootErr error

// 2. Build shoot applier only when needed; on failure record but DO NOT return.
if !shootDone {
    shootApplier, err := r.buildShootApplierOrDefault(ctx, cr)
    if err != nil {
        shootErr = err
        recordShootUnreachable(cr, err)        // Warning event + Ready=False
    } else {
        errs := deleteRender(shootApplier, cr.Status.ShootResources)
        shootDone = len(errs) == 0
        shootErr = kerrors.NewAggregate(errs)
    }
}

// 3. Force-delete override. NO blind timer: the finalizer is retained indefinitely while
//    shoot cleanup is incomplete, UNLESS a human has explicitly consented via annotation.
forceDelete := cr.Annotations[ForceDeleteAnnotation] == "true"

// 4. Seed cleanup. DDO's seed render no longer contains the shoot credential (it lives in
//    the operator install chart), so seed teardown can never delete DDO's own shoot access —
//    no credential-preservation predicate is needed. Real seed errors -> retry via backoff.
seedErrs := deleteRender(r.SeedApplier, cr.Status.SeedResources)
if agg := kerrors.NewAggregate(seedErrs); agg != nil {
    return ctrl.Result{}, agg
}

// 5. Finalizer policy — block until clean, or explicit human override.
switch {
case shootDone:
    controllerutil.RemoveFinalizer(cr, FinalizerName)
    return ctrl.Result{}, r.Update(ctx, cr)
case forceDelete:
    recordForceDeleted(cr, shootErr)           // distinct Warning event + ShootCleanup condition
    controllerutil.RemoveFinalizer(cr, FinalizerName)
    return ctrl.Result{}, r.Update(ctx, cr)
default:
    // Shoot cleanup incomplete and no override: RETAIN the finalizer indefinitely,
    // keep retrying, and surface a loud, auditable ShootCleanupBlocked condition.
    recordShootCleanupBlocked(cr, shootErr)
    return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}
```

The existing CRD-retention skip rule (`RetentionPolicy.CRDs != "Delete"`) inside `deleteRender`
is preserved. No `skip`/predicate parameter is added — see below.

### 3. No credential-preservation predicate needed (simplification)

An earlier revision of this design preserved the shootAccess Secret during seed cleanup, because
the Secret used to be part of the workload's **seed render** — so DDO's own seed teardown would
delete the credential its shoot teardown still needed (the self-deadlock).

That is no longer the case. The operator install chart (`chart/templates/shoot-rbac/`,
feature-branch commits `7bb8fb9`/`9c09682`/`906d936`) now owns the token-requestor Secret and the
shoot-applier RBAC bootstrap, gated `shootRbac.enabled`. The credential lives in **chart 1
(operator install)**, not in the workload's seed render, so it never appears in
`Status.SeedResources` and DDO's seed teardown can never delete it. The v2 CR header confirms:
*"The shootAccess Secret … is provisioned by the dual-deployment-operator install chart
(shootRbac.enabled), NOT by this chart."*

Consequences:
- **The self-deadlock is structurally eliminated** for the standard deployment — the credential's
  lifecycle is tied to the operator install (uninstalled separately), not to any single CR delete.
- **No `preserveShootCredential` predicate, no `deleteRender` skip parameter.** Seed cleanup
  deletes `Status.SeedResources` unconditionally (minus retained CRDs) — there is nothing to
  preserve.
- A hypothetical future consumer that puts the shootAccess Secret in its *own* seed render would
  reintroduce the self-deadlock for itself; its escape hatch is the `force-delete` override
  (below). This is a documented foot-gun, not a supported pattern.

### 4. Override annotation & signals

- `const ForceDeleteAnnotation = "dual-deployment-operator.cc.sap/force-delete"`. When set to
  `"true"` on the CR, the operator removes the finalizer even though shoot cleanup is incomplete
  — an explicit, auditable human consent to orphaning the remaining shoot resources. This
  replaces the scary bare `kubectl patch ... finalizers:[]` with a supported, signalled action.
  **No blind timeout**: the finalizer is retained indefinitely until shoot cleanup succeeds or
  an operator opts in via this annotation.
- `recordShootUnreachable(cr, err)`: existing Warning event (`ShootUnreachable`) + `Ready=False`;
  now non-fatal (falls through to seed cleanup).
- `recordShootCleanupBlocked(cr, shootErr)`: **new** Warning event (`ShootCleanupBlocked`) +
  dedicated status condition `{Type: "ShootCleanup", Status: False, Reason: "Blocked"}` stating
  the CR deletion is waiting on shoot cleanup and naming the annotation escape hatch. Emitted
  each retry while blocked. `Ready` is left unchanged.
- `recordForceDeleted(cr, shootErr)`: **new distinct** Warning event (`ShootCleanupForceDeleted`)
  + condition `{Type: "ShootCleanup", Status: False, Reason: "ForceDeleted"}` noting non-CRD
  shoot resources may be orphaned by explicit operator override.

## Scope

- `api/v1alpha1/` — `applyOrder` default flip + doc comment; regen CRD bases + `zz_generated`.
  (The force-delete override is an annotation — no CRD schema field, no status-schema change.)
- `internal/controller/` — delete control flow (no-hard-return, block/override),
  `ForceDeleteAnnotation` const, block/override signals. No `deleteRender` skip parameter and
  no credential-preservation predicate (the credential now lives in the install chart).
- **Reconcile-pipeline observability logging** (see §5) — `internal/controller` (apply/prune/
  delete stages) and `internal/source` (source pull, render-cache hit/miss). Observability-only;
  no behavior change.
- No `internal/deliver/` change. No new CRD status field. No timeout const (blocking model).

## 5. Reconcile-pipeline observability logging

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

This is intentionally bundled with the teardown fix because both are about making the
delete/reconcile path observable; it adds no new dependency and no behavior change.

## Testing (TDD, RED first)

Replace the existing test `retains finalizer and sets ShootUnreachable when Secret is missing`
(`…_controller_test.go:404`) — it encodes the buggy behavior. New coverage:

1. **Shoot unreachable, shoot resources pending, no override**: seed resources deleted (the
   credential is not in the seed render, so nothing special to preserve); finalizer **retained**;
   `ShootUnreachable` + `ShootCleanupBlocked` set; `RequeueAfter=30s`. (Deletion does not
   complete — blocking model.)
2. **Shoot unreachable, force-delete annotation set** (`dual-deployment-operator.cc.sap/force-delete: "true"`):
   seed cleanup completes; finalizer removed; `ShootCleanupForceDeleted` event +
   `ShootCleanup{Reason: ForceDeleted}` condition (shoot resources may be orphaned).
3. **No deletable shoot statuses** (only retained CRDs): shoot applier **not** built; seed
   cleanup runs; finalizer removed.
4. **Happy path**: shoot reachable → shoot then seed pruned → finalizer removed (keep).
5. **Default is `SeedFirst`** (types test update).
6. **Logging**: not unit-asserted line-by-line; verified by inspection that each stage emits at
   the specified level and that no token/secret bytes appear in log output.

## Effort / risk

Medium (~1–2 days incl. tests + codegen). Confidence high: the current default is actively
wrong for the common fleet case, and reverse-delete coupling is the root conceptual bug.
Follows generic Kubernetes finalizer guidance (retain until external cleanup succeeds; never
auto-remove a finalizer with cleanup outstanding) — the blocking model is the conservative,
never-silently-orphan stance. Orphaning is possible **only** via an explicit human-set
`force-delete` annotation, which replaces the unsafe bare `kubectl patch finalizers:[]`. This
deliberately diverges from the Flux `WaitForTermination` timeout precedent because this
operator has **no post-finalizer retry agent** (unlike the GRM system it replaces): a blind
timeout here would turn a transient shoot outage into permanent, unretried orphaning.
