<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Issue: CR deletion deadlocks when the shoot is unreachable / shootAccess Secret is gone

**Status:** open — proposed for a new OpenSpec change (not yet started).
**Found:** live testing on seed `rt-qa-de-1`, ns `shoot--cp--test-qa-de-1`, during the
CR-delete prune test (metal-operator-remote-v2). Operator image built from
`feature/deployment-chart-helm` @ `34253e3`.
**Severity:** high — a deleted CR can get stuck in `Terminating` forever, requiring a
manual finalizer wipe (`kubectl patch ... --type=merge -p '{"metadata":{"finalizers":[]}}'`),
which silently orphans shoot resources.

---

## Symptom

`kubectl delete dualdeploymentoperator <name>` never returns. The CR stays in
`Terminating` with `metadata.finalizers = ["dual-deployment-operator.cc.sap/finalizer"]`
indefinitely. The operator logs, every 30s:

```
Warning ShootUnreachable RetainFinalizer
  Shoot API server unreachable during deletion; retaining finalizer and retrying
```

Seed resources the teardown was supposed to prune are never cleaned; the finalizer is
never removed. Only a manual finalizer wipe clears the CR — which orphans whatever shoot
resources remained.

---

## Root cause

`reconcileDelete` (`internal/controller/dualdeploymentoperator_controller.go:274`) builds
the shoot applier **first** and **hard-returns** if it cannot:

```go
func (r *DualDeploymentOperatorReconciler) reconcileDelete(ctx, cr) (ctrl.Result, error) {
    shootApplier, err := r.buildShootApplierOrDefault(ctx, cr)
    if err != nil {
        // record ShootUnreachable event + Ready=False, requeue 30s, DO NOT remove finalizer,
        // DO NOT run any seed teardown.
        return ctrl.Result{RequeueAfter: 30 * time.Second}, nil   // <-- deadlock source
    }
    ...
    // reverse-of-applyOrder teardown: for SeedFirst -> shoot first, then seed
    // then controllerutil.RemoveFinalizer + Update
}
```

`buildShootApplier` (`:332`) returns an error when the shootAccess Secret
(`spec.shootAccess.secretName`, e.g. `metal-operator-remote-kubeconfig`) is **missing**.

The self-deadlock: **that Secret is part of the seed render the operator itself applied**
(the chart authors it as a Gardener token-requestor Secret shell). So the seed teardown is
what deletes it. Once it is gone (deleted by an earlier teardown pass, or by any other
cause), `buildShootApplier` fails permanently → `ShootUnreachable` → hard return → seed
teardown never runs → finalizer never removed → **loops forever**.

Even without the "Secret is part of seed render" wrinkle, the design is fragile: if the
shoot cluster is legitimately **deleted/unreachable** at CR-delete time, the operator can
never complete deletion of its own CR.

### Observed sequence (live)

1. `kubectl delete dualdeploymentoperator metal-operator-remote-v2` → sets
   `deletionTimestamp`, finalizer present.
2. `reconcileDelete` runs; `applyOrder: SeedFirst` → delete order is shoot-first, then seed.
3. At some point the shootAccess Secret `metal-operator-remote-kubeconfig` is deleted
   (NotFound confirmed live).
4. Next `reconcileDelete`: `buildShootApplierOrDefault` → `failed to get shoot access
   secret: ... not found` → `ShootUnreachable` → `RequeueAfter=30s`, finalizer retained.
5. Steps repeat forever. Seed leftover (`metal-operator-remote-cert-secret`) and shoot
   leftovers (`metal-servers` ns, 25 metal ClusterRoles, 17 retained CRDs) never pruned.
6. Manual `finalizers: []` patch required to unstick — orphaning the shoot leftovers.

---

## Relevant code

- `internal/controller/dualdeploymentoperator_controller.go:274` — `reconcileDelete`
  (hard return on shoot-client build failure at `:275-291`; reverse-order teardown at
  `:308-318`; `RemoveFinalizer` + `Update` at `:324-325`).
- `:295` — `deleteRender` (SortStatusForDelete, per-resource `applier.Delete`, skips CRDs
  when `RetentionPolicy.CRDs != "Delete"`).
- `:332` — `buildShootApplier` (errors on missing/NotFound shootAccess Secret; returns
  `errShootCredentialsNotReady` when the Secret exists but token/CA are empty).
- `internal/deliver/applier.go:86` — `SSAApplier.Delete` is owned-by-guarded (only deletes
  objects carrying the CR's owned-by label) and treats `NotFound` as success.
- The seed render **contains** the shootAccess Secret (chart-authored token-requestor
  Secret), so seed teardown deletes it.

---

## Required behavior

1. **Normal delete:** cleanly prune shoot **then** seed (reverse of applyOrder), then
   remove the finalizer.
2. **Shoot genuinely unreachable/gone:** must **not** deadlock forever. After a bounded
   effort, complete deletion (at least seed cleanup + finalizer removal) without silently
   leaking shoot resources with no signal.
3. **shootAccess Secret is part of the seed render:** deleting it must **not** strand shoot
   teardown. The credential needed for remote cleanup must be preserved until shoot pruning
   is confirmed done (or deliberately abandoned).

---

## Proposed fix (Oracle-designed; bounded finalization)

Precedent: Flux `Kustomization.spec.deletionPolicy=WaitForTermination` (times out after
`.spec.timeout`, then allows deletion) and Gardener's cleanup model (grace periods, then
forceful finalization of blocking resources). This differs from the conservative generic
Kubernetes stance (manual finalizer removal may orphan) — so **make the orphaning explicit**
via a distinct condition/event.

### Control-flow changes in `reconcileDelete`

1. **Classify** whether shoot cleanup is actually needed from `cr.Status.ShootResources`
   after applying the existing CRD-retention skip rule. If there are **no** deletable shoot
   statuses, skip shoot-client construction entirely.
2. Build the shoot applier **only when** shoot cleanup is needed. On success, run
   `deleteRender(shootApplier, cr.Status.ShootResources)` first (SeedFirst); if it returns
   errors, mark shoot cleanup **incomplete**.
3. If the shoot applier **cannot** be built, **do not hard-return** — record
   `ShootUnreachable` + `Ready=False`, then **continue to seed cleanup**.
4. **Seed cleanup with a skip predicate:** while shoot cleanup is incomplete **and** the
   timeout has not expired, delete seed resources **except** the exact shootAccess Secret
   (`apiVersion=v1`, `kind=Secret`, `namespace=cr.Namespace`,
   `name=cr.Spec.ShootAccess.SecretName`) — preserve the credential for remote-cleanup
   retries.
5. **Finalizer removal policy:**
   - Remove when: shoot cleanup succeeded (or none needed) **and** full seed cleanup
     (including the shootAccess Secret) succeeded.
   - Remove when: shoot cleanup has been unreachable/failing **longer than the bounded
     timeout** **and** seed cleanup (including the Secret) succeeded — emit a distinct
     `ShootCleanupAbandoned` warning event/condition noting shoot resources may be orphaned.
   - Otherwise: `RequeueAfter=30s` (retain).
6. **Retry clock:** use `cr.DeletionTimestamp` as the timeout reference (avoids a new status
   field). Default `remoteDeleteTimeout = 30 * time.Minute`; consider a future manager flag
   `--delete-shoot-timeout`.

### Control-flow sketch

```go
shootPending := deletableShootStatuses(cr) // ShootResources minus retained CRDs
shootDone := len(shootPending) == 0
var shootErr error

if !shootDone {
    shootApplier, err := r.buildShootApplierOrDefault(ctx, cr)
    if err != nil {
        shootErr = err
        recordShootUnreachable(cr, err) // event + Ready=False, but DO NOT return
    } else {
        errs := deleteRender(shootApplier, cr.Status.ShootResources, nil)
        shootDone = len(errs) == 0
        shootErr = kerrors.NewAggregate(errs)
    }
}

forceOrphanShoot := !shootDone && time.Since(cr.DeletionTimestamp.Time) >= remoteDeleteTimeout
skipShootAccessSecret := !shootDone && !forceOrphanShoot

seedErrs := deleteRender(r.SeedApplier, cr.Status.SeedResources, skipShootAccessSecretPredicate(cr))
if agg := kerrors.NewAggregate(seedErrs); agg != nil {
    return ctrl.Result{}, agg
}

switch {
case shootDone:
    removeFinalizer(cr); return ctrl.Result{}, r.Update(ctx, cr)
case forceOrphanShoot:
    recordWarning(cr, "ShootCleanupAbandoned",
        "Removing finalizer after timeout; shoot resources may remain orphaned")
    removeFinalizer(cr); return ctrl.Result{}, r.Update(ctx, cr)
default:
    return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}
```

`deleteRender` needs a new optional skip predicate parameter:
`deleteRender(applier, statuses, skip func(ResourceStatus) bool)`.

---

## Pitfalls to avoid (from Oracle)

- **Do not** treat a missing shootAccess Secret as immediate success when
  `Status.ShootResources` is non-empty — that silently leaks shoot resources on a local
  Secret accident. Only skip the shoot phase when there are genuinely no deletable shoot
  statuses.
- If shoot `Delete` errors after partially deleting, **preserve** the shootAccess Secret
  until the retry timeout — otherwise the same deadlock reappears one phase later.
- Emit a **distinct** final event/condition on timeout (`ShootCleanupAbandoned`), not just
  `ShootUnreachable`, so operators can audit possible shoot leaks.
- Cluster-scoped identity is namespace-normalized (see `34253e3`); ensure the
  skip-predicate matches the shootAccess Secret by its namespaced identity (it is a
  namespaced Secret in the CR's namespace, not cluster-scoped).

---

## Tests to add / change

Existing test `retains finalizer and sets ShootUnreachable when Secret is missing`
(`internal/controller/dualdeploymentoperator_controller_test.go:404`) encodes the CURRENT
(buggy) behavior and must be **replaced** by:

1. **Missing shootAccess Secret, shoot resources pending, before timeout:** seed resources
   **except** the shootAccess Secret are deleted; finalizer **retained**; `ShootUnreachable`
   set; `RequeueAfter=30s`.
2. **Missing shootAccess Secret, after timeout** (simulate old `deletionTimestamp`): full
   seed cleanup incl. the Secret; finalizer **removed**; `ShootCleanupAbandoned` warning
   emitted.
3. **No deletable shoot statuses** (e.g. only retained CRDs): shoot client is **not** built;
   seed cleanup runs; finalizer removed.
4. **Happy path:** shoot reachable → shoot then seed pruned → finalizer removed (existing
   coverage, keep).

---

## Effort / risk

Oracle estimate: **short, 1-4 hours** for controller + tests (no CRD/status schema change
required — `deletionTimestamp` is the retry clock). Confidence: high; the fix follows the
actual dependency in the code and matches Flux/Gardener bounded-deletion precedent.

## Phase placement

Per the project phase-scoping rule, this is **new/upcoming work** — scope it into a new
OpenSpec change (a NOT-STARTED phase), not into the ongoing `deployment-chart-helm` change
or any completed phase. It is a reconciler-teardown behavior change in `internal/controller`
(+ a small `internal/deliver` helper for the skip predicate, if extracted there).
