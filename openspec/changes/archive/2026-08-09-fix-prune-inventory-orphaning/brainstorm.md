## Design Summary

The live reconcile loop can **permanently orphan** a resource that leaves the render. On the `shootFirst` degraded path — `shootFirst == true` and `anyFailed(shootStatuses)` — reconcile returns early via `finishNotReady`, which persists a **current-render-only** inventory to `cr.Status.ShootResources` **before `prune` runs**. Because the next reconcile reads `prev` from that already-trimmed status, the removed object is never a prune candidate again: it stays live in the cluster with no status entry, and even `reconcileDelete` (which iterates `cr.Status.*`) cannot reclaim it. This is strictly worse than the documented one-cycle lag — it is unbounded orphaning.

The fix is to make prune part of every reconcile that applied a render this cycle, and to never write a trimmed inventory to status before that render's prune has run. Concretely: on the degraded early-return path, prune the render(s) that were actually applied this cycle **before** the status write, so status always reflects post-prune reality — the same invariant Flux upholds (old inventory from status, new inventory from the fresh build, diff and delete, *then* write status). Scope is **fix-forward only** (prevent new orphans); the operator is not live yet, so there are no production orphans to repair. Community norm (Flux kustomize-controller, Argo CD) is same-cycle prune; this change aligns the operator with it on the paths that regressed.

## Alternatives Considered

### Option A: Uncached `prev` read (`mgr.GetAPIReader()`)
- **Approach**: Read `prevSeedResources`/`prevShootResources` from an uncached `APIReader` so `prev` is always the authoritative persisted status, never an informer-cache copy that already reflects this cycle's own status write.
- **Pros**: ~5 lines; kills the informer-cache-lag class the design doc describes; matches how Flux reads its own inventory authoritatively.
- **Cons**: Does **not** fix the confirmed orphaning bug. The orphaning comes from *writing a trimmed inventory before prune*, not from a stale *read*. Once status is trimmed, an authoritative read of that trimmed status still yields a `prev` without the removed object. A is orthogonal to the real defect.
- **Why not chosen**: Solves the cosmetic lag, not the correctness bug. Oracle ranked it **second** for this bug precisely because it is insufficient alone.

### Option B: Live label-selector inventory (Argo-style)
- **Approach**: Derive `prev` from a live `List` of objects carrying the owned-by label on each target cluster, instead of from status. Diff the live owned-set against the current render.
- **Pros**: Immune to *any* status/cache anomaly; self-heals if status ever drifts from reality; the **only** option that also repairs *already-orphaned* objects.
- **Cons**: Largest change — new `applier.List`, cluster-scoped multi-kind listing (CRD, ClusterRole, ClusterRoleBinding, WebhookConfigurations), reconciling live objects back to `ResourceStatus` identity keys, extra API load per reconcile. Repair capability is unnecessary given no live CRs exist.
- **Why not chosen**: Over-scoped for a fix-forward correctness patch. Oracle ranked it **third / strategic** — valuable long-term robustness, but this change does not need live-discovery repair. Deferred as possible future hardening.

### Option C: Never persist a trimmed inventory before prune succeeds (chosen)
- **Approach**: Make prune run before *every* status write on any path that applied a render this cycle — including the degraded `shootFirst` early return. The degraded shoot render was still applied, so pruning it is correct and safe. Status is written only after prune, so `prev` on the next cycle reflects post-prune truth and never silently drops an entry. Also fix the `credsNotReady`/`clientFailed` seed-inventory asymmetry (seed statuses not preserved on those early returns while shoot statuses are).
- **Pros**: Directly closes the orphaning root cause; keeps the existing status-based inventory mechanism; aligns with Flux/Argo same-cycle-prune norm; bounded, reviewable change confined to the controller reconcile flow.
- **Cons**: Requires restructuring the early-return paths so prune is not skipped; must be careful that prune only runs against renders actually applied this cycle (never against a render that was skipped, e.g. shoot on `credsNotReady`).
- **Why chosen**: Smallest change that makes the defect impossible, per Oracle's #1 ranking and the confirmed control-flow evidence.

## Agreed Approach

**Option C, fix-forward only.** Restructure the reconcile loop so that the invariant *"status inventory is never written before that render's prune has run"* holds on **all** paths, not just the happy path.

Key mechanics:
- On the `shootFirst` degraded early-return (`anyFailed(shootStatuses)` → `finishNotReady`), run `pruneShoot()` (and `pruneSeed()` for any seed render applied this cycle) **before** the status write, so `ShootResources` reflects post-prune reality. The removed object is deleted this cycle instead of being dropped from `prev`.
- Guarantee prune only targets renders that were actually applied this cycle. A render skipped on a benign wait (e.g. shoot on `credsNotReady`) must not be pruned, and its prior inventory must be preserved verbatim (not overwritten with an empty/current-only list).
- Fix the seed-inventory asymmetry: on `credsNotReady`/`clientFailed` in the `shootFirst` case, seed is deferred and its prior `SeedResources` must be preserved, exactly as shoot statuses are preserved today.

The status write becomes the single, post-prune commit point on every terminating path.

## Key Decisions

- **Scope = C only, fix-forward.** No uncached read (A), no live-discovery/repair (B). Rationale: A doesn't fix the bug; B is over-scoped and its repair value is moot (no live CRs). Confirmed with user.
- **Prune before every status write.** The degraded shoot render *was* applied, so pruning it is correct; skipping prune is what caused the orphan. Chosen over "preserve prior inventory on early return" (reintroduces a one-cycle lag on the degraded path + risks stale entries) and "don't touch inventory on early return" (loses this cycle's apply results). Confirmed with user.
- **Preserve prior inventory only for renders NOT applied this cycle.** A skipped render (benign wait) keeps its exact prior status; an applied-but-degraded render is pruned and its post-prune status is written. This distinction is the crux of correctness.
- **Match community norm (same-cycle prune).** Flux kustomize-controller and Argo CD both prune on the same reconcile/sync; the design doc's "one-cycle lag is correct" claim is contradicted by the reference implementations on the paths this change touches.
- **RED test first.** Reproduce the `shootFirst` + degraded orphaning in an envtest (object removed from render + a degraded resource in the same render → assert the removed object is deleted this cycle and absent from status), before the fix — per TDD enforcement.

## Open Questions

- [x] Should the degraded-path prune failures be aggregated into the returned error (triggering backoff) or logged-and-continued like the happy-path `pruneErrs`? — **Resolved (design.md Decisions):** aggregate for requeue (matches happy-path semantics; the degraded path already returns with `backoff == 0`, so it backs off regardless, and failed-delete `retained` entries persist for retry), but preserve the `ResourcesDegraded`/`ShootApplyFailed` CR condition — a prune error must not overwrite the degraded-resource reason.
- [x] Does the `clientFailed` shootFirst branch (shoot render could not be applied at all) also need shoot prune, or only preserve? — **Resolved (design.md Decisions + per-path contract table):** shoot was NOT applied on `clientFailed`/`credsNotReady`, so its prior `ShootResources` is preserved verbatim and **not** pruned (pruning an un-applied render against an unreachable cluster would delete live resources during an outage). This generalized into the single invariant *a render not applied this cycle is preserved verbatim and never pruned*, which also surfaced the symmetric seed defect: on `credsNotReady`/`clientFailed` with `shootFirst`, seed is skipped and its prior `SeedResources` must likewise be preserved (currently emptied — in scope for this change).

_All brainstorm open questions resolved during the design phase._
