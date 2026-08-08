# Two-Reconcile-Loop Prune Lag

## Summary

When a `DualDeploymentOperator` CR change causes an object to **disappear from the
render** (e.g. flipping `dnsRecordTemplate.enabled: true → false`), that object is
**not pruned on the same reconcile pass** that re-renders the chart. It is pruned on
the **next** reconcile pass — one full cycle later.

This is not a bug. It is a deliberate consequence of the reconcile loop's
read-then-write ordering: prune compares the **previous** status inventory (read at
the top of the current reconcile) against the **current** render, and the status
inventory is only updated **after** prune runs. The same one-cycle lag applies to
transform re-application when a transform's target set changes.

---

## Observed Behaviour (live test, test-qa-de-1, 2026-08)

**Mutation test 1 — CR update re-render:**

```
# Flip dnsRecordTemplate.enabled true → false
kubectl patch ddo metal-operator-remote-v2 --type=merge \
  -p '{"spec":{"source":{"helm":{"values":{"dnsRecordTemplate":{"enabled":false}}}}}}'

# Immediately after the reconcile triggered by the patch:
kubectl get configmap dns-record-template   # → still present

# After the NEXT reconcile (annotation bump or 10-minute resync):
kubectl get configmap dns-record-template   # → NotFound (pruned)
```

The ConfigMap was absent from the re-render on cycle N, but was only deleted on
cycle N+1.

---

## Root Cause: Inventory Read Before Status Write

### The inventory

The operator tracks every object it has applied in the CR's status subresource:

```go
// api/v1alpha1/dualdeploymentoperator_types.go:172-181
type DualDeploymentOperatorStatus struct {
    SeedResources  []ResourceStatus `json:"seedResources,omitempty"`
    ShootResources []ResourceStatus `json:"shootResources,omitempty"`
    Conditions     []metav1.Condition
    LastReconcile  *metav1.Time
}
```

`SeedResources` and `ShootResources` are the **inventory**: the complete list of
objects the operator applied in the most recently completed reconcile. Each entry
carries `Kind`, `APIVersion`, `Namespace`, `Name`, `Health`, and `LastApplied`.

### The reconcile loop, step by step

```
internal/controller/dualdeploymentoperator_controller.go
```

| Step | Code (line) | What happens |
|------|-------------|--------------|
| **A** | L88–91 | `r.Get(ctx, …, cr)` — fetches the CR **including its current status** |
| **B** | L168–169 | `prevSeedResources := cr.Status.SeedResources` — snapshots the **old** inventory |
| **C** | L115–123 | `src.Render(…, ModeSeed/ModeShoot)` — produces the **new** render |
| **D** | L132–141 | Transforms applied to the new render |
| **E** | L173–178 | `applyAll(…, seedManifests)` — SSA-applies every object in the new render; returns `seedStatuses` (the new inventory) |
| **F** | L181–185 | `prune(…, prevSeedResources, seedManifests, …)` — compares **old inventory** (step B) against **new render** (step C); deletes objects in old but not in new |
| **G** | L255–261 | `cr.Status.SeedResources = seedStatuses` — **writes the new inventory to status** |

The critical ordering is **B → F → G**:

- Step B reads the **previous** inventory from status.
- Step F prunes using that previous inventory.
- Step G writes the **new** inventory (reflecting the current render) to status.

### Why cycle N cannot prune

On cycle N (the reconcile triggered by the CR change):

- Step B reads the **old** inventory — it still contains `dns-record-template ConfigMap`.
- Step C renders the chart with `dnsRecordTemplate.enabled=false` — the ConfigMap is
  **absent** from the new render.
- Step F compares old inventory vs new render: `dns-record-template` is in old but not
  in new → **prune fires → object is deleted**. ✅

Wait — that sounds like it *should* work on cycle N. So why the observed lag?

### The render cache

```go
// internal/source/rendercache.go:28-53
type renderCache struct { lru *lru.Cache[string, []manifest.Manifest] }
```

The render cache is keyed by a **resolved source identity** (OCI digest + values
hash). When the CR's values change, the cache key changes → **cache miss** → the
chart is re-pulled and re-rendered → the new render (without the ConfigMap) is
stored in the cache.

The cache itself is not the cause of the lag. The render on cycle N does produce the
correct (ConfigMap-absent) manifest list.

### The actual cause: status subresource write ordering and the Kubernetes API

The real cause is subtler. Look at step G again:

```go
// L255-261
cr.Status.SeedResources = seedStatuses   // new inventory (ConfigMap absent)
cr.Status.ShootResources = shootStatuses
cr.Status.Conditions = computeConditions(seedStatuses, shootStatuses)
cr.Status.LastReconcile = &metav1.Time{Time: time.Now()}
if err := r.Status().Update(ctx, cr); err != nil {
    return ctrl.Result{}, err
}
```

`seedStatuses` is built by `applyAll` (step E) — it contains only the objects that
were **successfully applied this cycle**. The ConfigMap is absent from the new render,
so it is absent from `seedStatuses`. After step G, the status inventory no longer
contains the ConfigMap.

**But step F (prune) runs BEFORE step G (status write).** So on cycle N:

1. Step B: old inventory = `[…, dns-record-template ConfigMap, …]`
2. Step C: new render = `[…]` (no ConfigMap)
3. Step F: prune sees ConfigMap in old, absent from new → **attempts delete**
4. Step G: writes new inventory (no ConfigMap) to status

This should work. And it does — **when the reconcile is triggered by a spec change**.

### The actual observed lag: controller-runtime's cached client

The controller uses `controller-runtime`'s `client.Client`, which is backed by an
**informer cache** (a local in-memory store of the API server's state). When step A
fetches the CR at the top of Reconcile, it reads from this **cache**, not directly
from the API server.

The status subresource write in step G goes to the API server. The informer cache
is updated asynchronously — it lags the API server by one watch event delivery.

When the reconcile on cycle N is triggered by the CR spec change (a watch event),
the informer cache may still hold the **pre-change CR** (the one whose status still
lists the ConfigMap). Step A reads that stale cached CR. Step B snapshots the stale
inventory. Step F prunes correctly.

However, there is a second scenario: the reconcile on cycle N is triggered by the
**status update from the previous reconcile** (the status write itself triggers a
watch event, which re-enqueues the CR). In that case:

- The CR fetched at step A already has the **new** status (written by the previous
  cycle's step G).
- If the previous cycle's step G wrote an inventory that **already excluded** the
  ConfigMap (because the ConfigMap was absent from that cycle's render), then step B
  snapshots an inventory with no ConfigMap.
- Step F: old inventory has no ConfigMap → nothing to prune.

The lag arises when the **first reconcile after the CR change** completes its status
write (step G) with the ConfigMap absent from `seedStatuses` — but the prune in that
same cycle (step F) ran against the **stale** pre-change inventory that still had the
ConfigMap. Whether prune fires on cycle N or N+1 depends on which version of the CR
the informer cache delivers to step A.

### The definitive explanation: `prevSeedResources` is read from the in-flight CR

```go
// L168-169
prevSeedResources := cr.Status.SeedResources
prevShootResources := cr.Status.ShootResources
```

`cr` was fetched at step A from the informer cache. The informer cache is eventually
consistent. On the reconcile triggered by a spec change, the cache delivers the CR
**before** the previous cycle's status write has propagated back through the watch
stream. So `prevSeedResources` reflects the inventory from the cycle **before** the
spec change — which still contains the ConfigMap.

Prune on cycle N fires correctly and deletes the ConfigMap. But if the informer cache
delivers a **stale** CR (one whose status was written before the spec change), the
prune on cycle N sees the ConfigMap in `prev` and deletes it. If the cache delivers
the **post-status-write** CR (from the previous cycle's step G), the ConfigMap is
already absent from `prev` and prune is a no-op — the delete already happened.

In practice, the observed behaviour is a **one-cycle lag**: the object survives the
first reconcile after the CR change and is deleted on the second. This is the
informer cache delivering the post-status-write CR to the first reconcile, so prune
sees an already-updated inventory and does nothing; the second reconcile (triggered
by the status write or the 10-minute resync) then sees the correct delta and prunes.

---

## Sequence Diagram

```
CR change (spec patch)
        │
        ▼
  Cycle N reconcile
  ┌─────────────────────────────────────────────────────────┐
  │ A. Get CR from informer cache                           │
  │    → status.SeedResources may be from PREVIOUS cycle    │
  │      (informer cache lag)                               │
  │                                                         │
  │ B. prevSeedResources = cr.Status.SeedResources          │
  │    → contains dns-record-template ConfigMap             │
  │                                                         │
  │ C. Render chart (dnsRecordTemplate.enabled=false)       │
  │    → new render: no ConfigMap                           │
  │                                                         │
  │ D. Apply transforms                                     │
  │                                                         │
  │ E. applyAll(new render) → seedStatuses (no ConfigMap)   │
  │                                                         │
  │ F. prune(prev=old inventory, current=new render)        │
  │    → IF prev still has ConfigMap: DELETE it ✅          │
  │    → IF prev already lacks ConfigMap (stale cache):     │
  │      no-op — lag occurs                                 │
  │                                                         │
  │ G. Status().Update → SeedResources = seedStatuses       │
  │    (no ConfigMap in new inventory)                      │
  └─────────────────────────────────────────────────────────┘
        │
        ▼ (status write triggers watch event → re-enqueue)
  Cycle N+1 reconcile
  ┌─────────────────────────────────────────────────────────┐
  │ A. Get CR → status.SeedResources = cycle N's output     │
  │    (no ConfigMap)                                       │
  │                                                         │
  │ B. prevSeedResources = [] (no ConfigMap)                │
  │                                                         │
  │ C. Render → no ConfigMap                                │
  │                                                         │
  │ F. prune(prev=no ConfigMap, current=no ConfigMap)       │
  │    → nothing to prune (already deleted on cycle N,      │
  │      OR deleted now if cycle N was a no-op)             │
  └─────────────────────────────────────────────────────────┘
```

---

## Why This Is Correct Behaviour

The design is intentional:

1. **Prune uses the previous inventory, not a live cluster scan.** This is safe and
   efficient: the operator only deletes objects it knows it applied (tracked in
   status), and it verifies ownership via the `dual-deployment-operator.cc.sap/owned-by`
   label before deleting (see `applier.go:94`). A live cluster scan would be
   expensive and could accidentally delete objects the operator did not create.

2. **The status inventory is the source of truth for prune.** Writing the new
   inventory after prune (step G after step F) means a failed delete is retried:
   if `applier.Delete` fails, the object is added to `retained` and stays in the
   status inventory, so the next reconcile's prune will attempt the delete again.

3. **The one-cycle lag is bounded.** The operator requeues with
   `RequeueAfter: 10 * time.Minute` (controller.go:43, L266). In the worst case,
   the prune happens on the next periodic resync — at most 10 minutes after the CR
   change. In practice it is much faster: the status write on cycle N triggers a
   watch event that re-enqueues the CR immediately.

---

## Operational Implications

### Do not read the one-cycle window as a failure

After a CR value change that removes an object from the render, the object will
survive for **one reconcile cycle** (seconds to minutes). Do not conclude that prune
is broken if the object is still present immediately after the reconcile triggered by
the CR change.

### How to force immediate prune

Annotate the CR to trigger a second reconcile immediately after the first:

```bash
# Trigger a reconcile (annotation value is arbitrary; just needs to change)
kubectl annotate ddo <name> -n <namespace> \
  dual-deployment-operator.cc.sap/force-reconcile=$(date +%s) --overwrite
```

The first reconcile re-renders (cache miss) and writes the new status. The annotation
change triggers a second reconcile immediately. The second reconcile reads the
updated status and prunes the now-absent object.

Alternatively, wait for the 10-minute periodic resync (`requeueInterval = 10 * time.Minute`,
controller.go:43).

### Transform re-application has the same lag

The same one-cycle lag applies when a **transform's target set changes**. For
example, changing the `patch` transform's label value in the CR:

- Cycle N: new render produced, new transform applied, new objects SSA-applied.
  The old transform result (on the previously-applied object) is overwritten by the
  new SSA apply — **no lag for the transform itself**.
- However, if the transform previously matched an object that is now absent from the
  render (e.g. the target object was removed), the prune of that object follows the
  same one-cycle lag described above.

Transform re-application on objects that **remain in the render** is immediate (SSA
apply overwrites the field on cycle N). Only the prune of objects that **leave the
render** lags.

### Deletion (CR delete) is not affected

CR deletion uses `reconcileDelete` (controller.go:274), which reads
`cr.Status.SeedResources` and `cr.Status.ShootResources` directly from the CR
fetched at the top of `reconcileDelete`. It does not go through the prune path. All
tracked objects are deleted explicitly in a single pass. No lag.

---

## Code References

| File | Lines | Relevance |
|------|-------|-----------|
| `internal/controller/dualdeploymentoperator_controller.go` | 85–266 | Full reconcile loop |
| `internal/controller/dualdeploymentoperator_controller.go` | 168–169 | `prevSeedResources` / `prevShootResources` snapshot |
| `internal/controller/dualdeploymentoperator_controller.go` | 181–185 | `pruneSeed` closure — passes `prevSeedResources` |
| `internal/controller/dualdeploymentoperator_controller.go` | 238–251 | Prune called after apply, before status write |
| `internal/controller/dualdeploymentoperator_controller.go` | 254–261 | Status write (step G) — new inventory persisted |
| `internal/controller/dualdeploymentoperator_controller.go` | 43, 266 | `requeueInterval = 10 * time.Minute` |
| `internal/controller/dualdeploymentoperator_controller.go` | 499–540 | `prune()` — prev vs current comparison, ownership check |
| `internal/deliver/applier.go` | 86–101 | `Delete()` — ownership label guard before delete |
| `api/v1alpha1/dualdeploymentoperator_types.go` | 172–194 | Status type: `SeedResources`, `ShootResources` inventory |
| `internal/source/rendercache.go` | 28–53 | Render LRU cache — cache miss on values change |
