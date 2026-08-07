<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Design: bounded-teardown-finalization

> Pre-filled from the brainstorm session. Refine during the `design` artifact step.

## Problem

`reconcileDelete` (`internal/controller/dualdeploymentoperator_controller.go:274`) builds
the shoot applier **first** and **hard-returns** (`RequeueAfter=30s`, finalizer retained,
no seed teardown) whenever the shoot client cannot be built — including when the shootAccess
Secret is missing/unreachable. Because that Secret is part of the **seed render**, seed
teardown is what deletes it; once gone, the shoot applier can never be built, seed teardown
never runs, the finalizer is never removed, and the CR is stuck `Terminating` forever
(requiring a manual finalizer wipe that silently orphans shoot resources).

A deeper defect underlies this: `spec.applyOrder` (enum `{SeedFirst, ShootFirst}`, default
**`ShootFirst`**) conflates apply-convergence sequencing with teardown-dependency ordering,
and `reconcileDelete` tears down in the **reverse** of `applyOrder`. The verified-correct
value for any seed-rendered-credential operator is `SeedFirst`; the only real consumer
(`metal-operator-remote-v2`) hardcodes `applyOrder: SeedFirst` because the `ShootFirst`
default deadlocks first deploy (shoot read before seed applied → Secret missing → seed
skipped → never created).

## Root invariant

**Shoot access must exist before any shoot operation — on both apply and delete.** This is a
credential dependency (unidirectional), not a symmetric create/reverse-destroy ownership
dependency. Encoding delete order as "reverse of apply order" is the root conceptual bug.

## Genericity (DDO stays general-purpose)

`DualDeploymentOperator` is a generic operator serving all fleet operators; the
`metal-operator-remote` deployment is only the *motivating worst case*, never a hardcoded
assumption in the CRD or reconciler. This change preserves that boundary:

- **All reconciler behavior keys off generic CR fields only** — the credential-preservation
  predicate matches `spec.shootAccess.secretName` in the CR's own namespace; nothing is keyed
  on operator name, chart labels, or `metal-*` identifiers. Any DDO consumer benefits from the
  same teardown robustness.
- **The "credential is part of the seed render" scenario is the worst case the fix must
  survive, not a precondition it requires.** An operator whose shoot credential is provisioned
  externally (Secret not in the seed render) is handled by the same code path — its seed
  cleanup simply has no shootAccess Secret to preserve, and the predicate is a no-op for it.
- **Metal-specific deployment facts stay in the chart, out of the operator.** The
  `metal-operator-remote-v2` chart hardcodes `applyOrder: SeedFirst` (its credential is
  seed-rendered) and delivers the GRM shoot-RBAC bootstrap + token-requestor Secret as
  install-time plumbing. This change references those facts only as rationale/teardown-race
  context; it adds no chart-aware or GRM-aware logic to the reconciler.
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
        errs := deleteRender(shootApplier, cr.Status.ShootResources, nil)
        shootDone = len(errs) == 0
        shootErr = kerrors.NewAggregate(errs)
    }
}

// 3. Force-delete override + credential-preservation decision.
//    NO blind timer: the finalizer is retained indefinitely while shoot cleanup is
//    incomplete, UNLESS a human has explicitly consented to orphaning via annotation.
forceDelete := cr.Annotations[ForceDeleteAnnotation] == "true"
skip := preserveShootCredential(cr)
if shootDone || forceDelete {
    skip = nil                                 // done or force-deleting -> delete the credential too
}

// 4. Seed cleanup with preserve predicate. Real seed errors -> retry via backoff.
seedErrs := deleteRender(r.SeedApplier, cr.Status.SeedResources, skip)
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

`deleteRender` gains an optional `skip func(ddov1alpha1.ResourceStatus) bool` parameter
(controller-local closure — no `internal/deliver` extraction; one caller). Existing
CRD-retention skip rule (`RetentionPolicy.CRDs != "Delete"`) is preserved.

### 3. Credential-preservation predicate

```go
func preserveShootCredential(cr *ddov1alpha1.DualDeploymentOperator) func(ddov1alpha1.ResourceStatus) bool {
    name := cr.Spec.ShootAccess.SecretName
    return func(rs ddov1alpha1.ResourceStatus) bool {
        return rs.Kind == "Secret" &&
            (rs.APIVersion == "v1" || rs.APIVersion == "") &&
            rs.Namespace == cr.Namespace &&   // namespaced Secret in the CR's own ns
            rs.Name == name
    }
}
```

**Verified sufficient**: `clients.ShootRESTConfig` (`internal/clients/clients.go:31`) reads
only `secret.Data["token"]`, `secret.Data["bundle.crt"]`, and `spec.shootAccess.server`
(a CR field). No namespace object or auxiliary resource is dereferenced.

### 4. Override annotation & signals

- `const ForceDeleteAnnotation = "dual-deployment-operator.cc.sap/force-delete"`. When set to
  `"true"` on the CR, the operator deletes the shootAccess Secret too and removes the finalizer
  even though shoot cleanup is incomplete — an explicit, auditable human consent to orphaning.
  This replaces the scary bare `kubectl patch ... finalizers:[]` with a supported, signalled
  action. **No blind timeout**: the finalizer is retained indefinitely until shoot cleanup
  succeeds or an operator opts in via this annotation.
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
- `internal/controller/` — delete control flow, `deleteRender` skip param, credential predicate,
  `ForceDeleteAnnotation` const, block/override signals.
- No `internal/deliver/` change. No new CRD status field. No timeout const (blocking model).

## Testing (TDD, RED first)

Replace the existing test `retains finalizer and sets ShootUnreachable when Secret is missing`
(`…_controller_test.go:404`) — it encodes the buggy behavior. New coverage:

1. **Missing Secret, shoot pending, no override**: seed resources deleted **except** the
   shootAccess Secret; finalizer **retained**; `ShootUnreachable` + `ShootCleanupBlocked` set;
   `RequeueAfter=30s`. (Deletion does not complete — blocking model.)
2. **Missing Secret, force-delete annotation set** (`dual-deployment-operator.cc.sap/force-delete: "true"`):
   full seed cleanup incl. the Secret; finalizer removed; `ShootCleanupForceDeleted` event +
   `ShootCleanup{Reason: ForceDeleted}` condition.
3. **No deletable shoot statuses** (only retained CRDs): shoot applier **not** built; seed
   cleanup runs; finalizer removed.
4. **Happy path**: shoot reachable → shoot then seed pruned → finalizer removed (keep).
5. **Default is `SeedFirst`** (types test update).

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
