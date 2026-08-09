## Context

The live reconcile loop in [`dualdeploymentoperator_controller.go`](../../../internal/controller/dualdeploymentoperator_controller.go) renders a source twice (seed + shoot), SSA-applies each render to its target cluster, then **prunes** orphans — objects present in the previous applied inventory (`cr.Status.SeedResources` / `cr.Status.ShootResources`) but absent from the current render. The status inventory is the sole source of truth for prune; there is no live-cluster discovery.

The reconcile has several early-return paths that call `finishNotReady`, which writes status and returns before the main prune section (step 8, L238–252). On the **`shootFirst` degraded path** (`anyFailed(shootStatuses)` at L218 → `finishNotReady` at L226), the shoot render *was* applied, producing a **current-render-only** `shootStatuses`, and that trimmed list is persisted to `cr.Status.ShootResources` **before `pruneShoot` ever runs**. On the next reconcile, `prevShootResources` (L169) is read from that already-trimmed status, so any object that left the render is no longer in `prev` → it is never a prune candidate again. The object stays live in the cluster with no status entry, and `reconcileDelete` (which iterates `cr.Status.*`) cannot reclaim it either. This is **unbounded orphaning**, strictly worse than the one-cycle prune lag documented in `docs/two-reconcile-loop-prune-lag.md`.

A symmetric defect exists on the seed side: on `credsNotReady`/`clientFailed` with `shootFirst == true`, the `if !shootFirst` guard (L189, L200) skips `applySeed()`, leaving `seedStatuses` empty, and `finishNotReady` then persists an **empty** `SeedResources` — silently dropping the entire seed inventory.

Root cause confirmed by control-flow analysis and independently verified by an Oracle consult. Constraints: the operator is **not yet deployed live** (no production CRs, no existing orphans to repair); the fix must stay within the controller reconcile flow; TDD is mandatory (RED test reproducing the orphaning before the fix).

## Goals / Non-Goals

**Goals:**
- Establish and enforce a single invariant: **status inventory is never written for a render before that render's prune has run this cycle.**
- Close the `shootFirst` degraded-path shoot orphaning (the primary bug).
- Close the `credsNotReady`/`clientFailed` `shootFirst` seed-inventory drop (the symmetric bug).
- Align prune timing with the community norm (Flux kustomize-controller, Argo CD both prune same-cycle) on the paths that regressed.
- Preserve prior inventory verbatim for any render **not applied** this cycle (benign waits), so nothing is silently dropped.
- Prove the fix with a failing envtest reproducing the orphaning, then make it pass.

**Non-Goals:**
- Uncached `prev` read via `mgr.GetAPIReader()` (Option A) — does not fix orphaning; the defect is writing a trimmed inventory before prune, not a stale read. Deferred.
- Live label-selector inventory / Argo-style discovery (Option B) — over-scoped; its only unique value is repairing already-orphaned objects, which cannot exist (no live CRs). Deferred as future hardening.
- Repairing already-orphaned objects (fix-forward only, per user decision).
- Changing the render-cache, transform, or delivery layers.
- Altering `reconcileDelete` teardown semantics (owned by the separate `robust-teardown-finalization` change).

## Capabilities

**Modified Capabilities:**
- `reconcile-loop` — the prune/status-write ordering contract across all terminating paths. This is the existing capability whose requirements change; the spec delta will encode the per-path apply/prune/preserve contract.

_No new capabilities._

## Decisions

**Decision: Prune before every status write (Option C)**
- Chosen: Restructure the reconcile so prune runs for every render **applied this cycle** before any status write, including the `shootFirst` degraded early-return. Status becomes a post-prune commit point on every terminating path.
- Reason: Directly makes the orphaning impossible — the removed object is deleted this cycle instead of dropped from `prev`. Keeps the existing status-based inventory mechanism. Matches Flux (old inventory from status, new inventory from fresh build, diff+delete, *then* write status).
- Alternatives considered: (A) uncached `prev` read — orthogonal to the bug, rejected; (B) live label-selector inventory — over-scoped, repair value moot, deferred; "preserve prior inventory on early return (union)" — reintroduces a one-cycle lag on the degraded path and risks stale entries; "don't touch inventory on early return" — loses this cycle's apply results.

**Decision: Apply-vs-skip determines prune-vs-preserve**
- Chosen: A render **applied this cycle** is pruned before its status is written. A render **not applied this cycle** (skipped on a benign wait or unbuildable client) has its prior inventory preserved verbatim and is **not** pruned.
- Reason: Pruning a render that was never applied would diff prior inventory against an un-applied render and issue deletes against a cluster in a known-bad/unreachable state — deleting real resources during an outage. Preservation is the only safe action for a skipped render.
- Alternatives considered: unconditionally pruning both renders every path — unsafe for `credsNotReady`/`clientFailed` where the shoot is unreachable.

**Decision: Per-path reconcile contract (authoritative table)**

| Path | Shoot applied? | Seed applied? | Shoot inventory action | Seed inventory action |
|---|---|---|---|---|
| `credsNotReady` + shootFirst | No | No | preserve prev | **preserve prev** (fix: currently emptied) |
| `credsNotReady` + SeedFirst | No | Yes | preserve prev | apply + prune |
| `clientFailed` + shootFirst | No | No | preserve prev | **preserve prev** (fix: currently emptied) |
| `clientFailed` + SeedFirst | No | Yes | preserve prev | apply + prune |
| `ready` + degraded (shootFirst) | Yes | No | **prune before status write** (fix) | preserve prev |
| happy path (either order) | Yes | Yes | apply + prune | apply + prune |

- Reason: Encodes the apply-vs-skip rule concretely for every terminating branch. The two **fix** cells are the defects; every other cell already behaves correctly and must be preserved.
- Scope note: prune acts only on `prev − current` and its only action is `Delete` (it iterates `prev`, never `current`). **Additions flow through the apply step and are invisible to prune**, so "prune before status write" changes nothing for a render that only adds resources (prune is a no-op) and cannot drop, duplicate, or delay an addition. The ordering fix is meaningful only for removals; a mixed add+remove cycle is correct because additions are driven by `current` (apply) and removals by `prev` (prune) — disjoint effects.

**Decision: Aggregate degraded-path prune errors for requeue, preserve the degraded condition**
- Chosen: On the `shootFirst` degraded path, fold `pruneShoot`'s error into the reconcile's requeue decision (aggregate, matching happy-path semantics), but keep the `ResourcesDegraded`/`ShootApplyFailed` **CR condition** as the surfaced reason — do not let a prune error overwrite it.
- Reason: The degraded path already returns with `backoff == 0` (exponential backoff), and failed-delete `retained` entries are persisted for retry next cycle, so aggregate-vs-log is nearly moot for requeue behavior. Aggregating keeps parity with the happy path; preserving the condition keeps the operator-facing diagnosis (a degraded resource) visible rather than masking it with a transient prune error.
- Alternatives considered: log-and-swallow (matches the `credsNotReady`/`clientFailed` seed-prune precedent) — rejected for the degraded path because it diverges from happy-path prune-error handling; letting `finishNotReady`'s synthetic error win — rejected because it drops the prune error entirely.

**Decision: RED test first**
- Chosen: Add a failing envtest that reproduces `shootFirst` + degraded orphaning (a resource removed from the render alongside a degraded resource in the same render → assert the removed object is deleted this cycle and absent from status), plus a seed-drop test for `credsNotReady`/`clientFailed` + shootFirst, before implementing the fix.
- Reason: TDD enforcement (AGENTS.md); the tests lock the invariant and prevent regression.

## Risks / Trade-offs

- [Pruning the degraded shoot render deletes an orphan while the shoot has a degraded (but not removed) resource] → Correct and intended: prune only deletes objects in `prev` but absent from the *current* render; a degraded-but-still-rendered resource stays in `currentKeys` and is never a prune target. The `owned-by` label guard (applier.go L94, controller.go L538) remains the safety net.
- [Restructuring early-return paths introduces a new regression on a benign-wait path] → Mitigated by the per-path contract table becoming explicit spec requirements + envtests for each row; the apply-vs-skip rule is the single invariant to verify.
- [Prune error on the degraded path masks the degraded-resource reason] → Mitigated by keeping the `ResourcesDegraded`/`ShootApplyFailed` condition and only using the prune error for the requeue/return value.
- [Extra prune call on the degraded path adds shoot API traffic during a degraded state] → Bounded: one prune pass (List-free; per-object Get/Delete only for `prev` entries absent from current), same cost profile as the happy-path prune already paid every reconcile.

## Migration Plan

Deployment steps:
1. Land the fix behind the normal release; no CRD schema change, no data migration (status shape unchanged).
2. Because the operator is not live, there is no in-field orphan backfill required.

Rollback:
- Pure controller-logic change; revert the commit to restore prior behavior. No persisted-state migration to unwind.

## Open Questions

_All prior open questions resolved during design:_
- Degraded-path prune error handling → **aggregate for requeue, preserve degraded condition** (see Decisions).
- Does `clientFailed`/`credsNotReady` shootFirst need shoot prune → **no; preserve shoot prev verbatim** (shoot not applied). Seed on those paths → **preserve seed prev verbatim** (seed not applied on the shootFirst variant); this is the seed-asymmetry fix.

_No outstanding questions._
