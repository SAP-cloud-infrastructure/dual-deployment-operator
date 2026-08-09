# Prune / Re-Render Timing — Same-Cycle

## Summary

When a `DualDeploymentOperator` CR change causes an object to **disappear from the
render** (e.g. flipping `dnsRecordTemplate.enabled: true → false`), that object is
**pruned in the same reconcile pass** that processes the CR change. There is no
one-cycle lag.

The reconcile that handles the spec change:
1. snapshots the prior applied inventory (`prevSeedResources` / `prevShootResources`)
   from the CR status at the **top** of the reconcile — this still contains the object,
   because it was written at the end of the *previous* cycle;
2. re-renders the chart with the new values — the object is now **absent** from the render;
3. SSA-applies the new render;
4. **prunes**: diffs the prior inventory against the current render and deletes anything
   in the inventory but absent from the render — **this same pass**;
5. writes the new (pruned) inventory to status.

The only precondition is that the CR edit triggers a reconcile at all — the spec-change
watch event does that immediately.

> **Historical note.** An earlier revision of this operator DID have a one-cycle prune
> lag on some early-return paths, plus an unbounded-orphaning bug on the `shootFirst`
> degraded path. Both were fixed by the change
> `openspec/changes/archive/2026-08-09-fix-prune-inventory-orphaning` (Option C:
> "prune runs for every render applied this cycle before any status write", explicitly
> aligning with the Flux / Argo CD same-cycle norm). This document describes the
> **current, fixed** behavior. Any older note claiming a "one reconcile cycle lag" is
> obsolete.

---

## The reconcile loop, step by step

`internal/controller/dualdeploymentoperator_controller.go`

| Step | Code | What happens |
|------|------|--------------|
| Snapshot prior inventory | `prevSeedResources := cr.Status.SeedResources` / `prevShootResources := cr.Status.ShootResources` | The applied set from the **previous** completed cycle. Still contains an object that is about to be removed. |
| Render | `src.Render(ModeSeed/ModeShoot, …)` | New render reflecting the changed CR values — the removed object is **absent**. |
| Transforms | `transform.Build` + `t.Apply(...)` | Applied to the fresh render each cycle. |
| Apply | `applyAll(…, seedManifests / shootManifests, …)` | SSA-applies every object in the new render; produces `seedStatuses` / `shootStatuses` (current-render-only). |
| **Prune** | `pruneSeed()` / `pruneShoot()` → `prune(prev, current, …)` | Compares **prior inventory** (`prev`) against the **current render** (`current`); any identity in `prev` but not in `currentKeys` gets a live `applier.Delete` — **this cycle**. `retained` (failed deletes) are appended back for retry. |
| Status write | `cr.Status.SeedResources = seedStatuses` … `r.Status().Update(ctx, cr)` | Persists the post-prune inventory. Prune ALWAYS runs before the status write for any render applied this cycle. |
| Requeue | `RequeueAfter: requeueInterval` (10m) | Periodic resync; not needed for prune correctness. |

### Why it is same-cycle (not lagged)

`prune` uses the **previous cycle's** inventory as `prev`, not the current CR's freshly
computed inventory. On the reconcile that processes the change:

- `prev` = end-of-previous-cycle inventory → **still has** the removed object.
- `current` = this cycle's render → **lacks** the removed object.
- diff → object is in `prev`, absent from `current` → deleted now.

The status write that trims the inventory happens **after** prune (see the per-path
contract below), so the object is deleted before it ever drops out of `prev`. This is
the invariant the fix established: *status inventory is never written for a render
before that render's prune has run this cycle.*

---

## Per-path apply / prune / preserve contract

A render **applied this cycle** is pruned before its status is written. A render **not
applied this cycle** (skipped on a benign wait or unbuildable shoot client) has its
prior inventory **preserved verbatim** and is **not** pruned (pruning an un-applied
render against an unreachable cluster would delete live resources during an outage).

| Path | Shoot applied? | Seed applied? | Shoot inventory | Seed inventory |
|---|---|---|---|---|
| `credsNotReady` + shootFirst | No | No | preserve prev | preserve prev |
| `credsNotReady` + SeedFirst | No | Yes | preserve prev | apply + prune |
| `clientFailed` + shootFirst | No | No | preserve prev | preserve prev |
| `clientFailed` + SeedFirst | No | Yes | preserve prev | apply + prune |
| `ready` + degraded (shootFirst) | Yes | No | prune before status write | preserve prev |
| happy path (either order) | Yes | Yes | apply + prune | apply + prune |

Prune acts only on `prev − current` and its only action is `Delete` (it iterates `prev`,
never `current`). **Additions flow through the apply step and are invisible to prune**, so
a render that only adds resources makes prune a no-op.

---

## Operational implications

### Assert after ONE reconcile

After a CR value change that removes an object from the render, force one reconcile and
the object is gone. There is no need to wait for a second cycle or the 10-minute resync.

```bash
# Force a reconcile (annotation value is arbitrary; just needs to change)
kubectl annotate ddo <name> -n <namespace> \
  dual-deployment-operator.cc.sap/force-reconcile=$(date +%s) --overwrite
```

The prune completes within that single reconcile. (The 10-minute periodic resync,
`requeueInterval`, is a safety net, not a requirement for prune to happen.)

### Transform re-application

Transform re-application on objects that **remain in the render** is immediate: the new
SSA apply overwrites the field this cycle. Prune of objects that **leave the render**
is also same-cycle, per the contract above.

### Deletion (CR delete) is separate

CR deletion uses `reconcileDelete`, which reads `cr.Status.SeedResources` /
`cr.Status.ShootResources` directly and deletes all tracked objects explicitly in one
pass (CRDs retained unless `retentionPolicy.crds: Delete`). It does not go through the
prune path.

---

## Code References

| File | Symbol | Relevance |
|------|--------|-----------|
| `internal/controller/dualdeploymentoperator_controller.go` | `Reconcile` | Full reconcile loop |
| `internal/controller/dualdeploymentoperator_controller.go` | `prevSeedResources` / `prevShootResources` | Prior-inventory snapshot at top of reconcile |
| `internal/controller/dualdeploymentoperator_controller.go` | `pruneSeed` / `pruneShoot` closures | Prune runs after apply, before status write |
| `internal/controller/dualdeploymentoperator_controller.go` | step 8 → step 9 | Prune → status write ordering (the invariant) |
| `internal/controller/dualdeploymentoperator_controller.go` | `prune(...)` | `prev − current` diff, live `Delete`, `owned-by` guard |
| `openspec/changes/archive/2026-08-09-fix-prune-inventory-orphaning/` | design.md | The change that made prune same-cycle on all applied-render paths |
