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

// 3. Timeout + credential-preservation decision.
forceOrphan := !shootDone && time.Since(cr.DeletionTimestamp.Time) >= remoteDeleteTimeout
skip := preserveShootCredential(cr)
if shootDone || forceOrphan {
    skip = nil                                 // done or abandoning -> delete the credential too
}

// 4. Seed cleanup with preserve predicate. Real seed errors -> retry via backoff.
seedErrs := deleteRender(r.SeedApplier, cr.Status.SeedResources, skip)
if agg := kerrors.NewAggregate(seedErrs); agg != nil {
    return ctrl.Result{}, agg
}

// 5. Finalizer policy.
switch {
case shootDone:
    controllerutil.RemoveFinalizer(cr, FinalizerName)
    return ctrl.Result{}, r.Update(ctx, cr)
case forceOrphan:
    recordShootCleanupAbandoned(cr, shootErr)  // distinct Warning event + ShootCleanup condition
    controllerutil.RemoveFinalizer(cr, FinalizerName)
    return ctrl.Result{}, r.Update(ctx, cr)
default:
    return ctrl.Result{RequeueAfter: 30 * time.Second}, nil   // retain, keep retrying
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

### 4. Timeout & signals

- `const remoteDeleteTimeout = 30 * time.Minute` (package-level, mirrors `requeueInterval`).
  Measured from `cr.DeletionTimestamp` — no new status field, no CRD status-schema change.
- `recordShootUnreachable(cr, err)`: existing Warning event (`ShootUnreachable`) + `Ready=False`;
  now non-fatal (falls through to seed cleanup).
- `recordShootCleanupAbandoned(cr, shootErr)`: **new distinct** Warning event
  (`ShootCleanupAbandoned`) + dedicated status condition
  `{Type: "ShootCleanup", Status: False, Reason: "Abandoned"}` noting non-CRD shoot resources
  may be orphaned. `Ready` is left unchanged.

## Scope

- `api/v1alpha1/` — default flip + doc comment; regen CRD bases + `zz_generated`.
- `internal/controller/` — delete control flow, `deleteRender` skip param, predicate, signals.
- No `internal/deliver/` change. No new CRD status field. `remoteDeleteTimeout` stays a const.

## Testing (TDD, RED first)

Replace the existing test `retains finalizer and sets ShootUnreachable when Secret is missing`
(`…_controller_test.go:404`) — it encodes the buggy behavior. New coverage:

1. **Missing Secret, shoot pending, before timeout**: seed resources deleted **except** the
   shootAccess Secret; finalizer retained; `ShootUnreachable` set; `RequeueAfter=30s`.
2. **Missing Secret, after timeout** (old `deletionTimestamp`): full seed cleanup incl. the
   Secret; finalizer removed; `ShootCleanupAbandoned` event + `ShootCleanup` condition.
3. **No deletable shoot statuses** (only retained CRDs): shoot applier **not** built; seed
   cleanup runs; finalizer removed.
4. **Happy path**: shoot reachable → shoot then seed pruned → finalizer removed (keep).
5. **Default is `SeedFirst`** (types test update).

## Effort / risk

Medium (~1–2 days incl. tests + codegen). Confidence high: the current default is actively
wrong for the only verified consumer, and reverse-delete coupling is the root conceptual bug.
Follows Kubebuilder finalizer guidance (retry external cleanup, idempotent) and Flux/Gardener
bounded-deletion precedent, adapted for the absence of a post-finalizer retry agent.
