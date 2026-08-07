<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Brainstorm: robust-teardown-finalization

## Design Summary

Deleting a `DualDeploymentOperator` CR can hang forever. `reconcileDelete` builds the shoot
applier first and returns early if that fails — so when the shoot API server is unreachable at
delete time, seed teardown never runs and the finalizer is never removed. The CR is stuck in
`Terminating` until someone manually wipes the finalizer, which silently orphans whatever shoot
resources were left behind.

This change makes teardown robust to an unreachable shoot: proceed with seed cleanup even when
the shoot client cannot be built, retain the finalizer (with a clear, auditable signal) while
shoot cleanup is outstanding, and let an operator complete the deletion deliberately via a
`force-delete` annotation instead of a raw finalizer wipe. It also adds structured logging
across the reconcile pipeline so the next such issue is diagnosable from logs alone.

The fix is intentionally narrow and generic: a behavioral change in `reconcileDelete` plus
logging. It does not change the CRD, does not alter `spec.applyOrder` or the reverse-order
teardown it drives, and adds no shoot-specific special-casing.

## Alternatives Considered

The decision that matters: **when shoot cleanup cannot complete, what happens to the
finalizer?**

### Option A: Bounded timeout, then remove the finalizer
- **Approach**: Proceed with seed cleanup; keep retrying shoot cleanup; after a fixed window
  (e.g. 30 min from `deletionTimestamp`), remove the finalizer anyway and emit an "abandoned"
  event.
- **Pros**: The CR always disappears on its own; matches Flux `WaitForTermination` and
  Gardener grace-then-force precedent.
- **Cons**: This operator has **no post-finalizer retry agent**. Once the finalizer is gone,
  nothing ever retries shoot cleanup — so a merely *transient* shoot outage that outlasts the
  window becomes *permanent* orphaning, with only an event a human may never see. That is
  strictly worse than the gardener-resource-manager model it replaces (which had an async
  seed-side reconciler that kept retrying after `helm uninstall`).
- **Why not chosen**: Auto-removing a finalizer with cleanup outstanding is exactly what
  finalizers exist to prevent; without a retry agent, the timeout silently and irrecoverably
  orphans resources.

### Option B: Best-effort — remove the finalizer regardless of shoot outcome
- **Approach**: Attempt shoot cleanup once, then remove the finalizer unconditionally.
- **Pros**: Simplest; deletion never hangs.
- **Cons**: Silently orphans on any transient outage, with a single fire-and-forget attempt
  and no retry.
- **Why not chosen**: Automating the exact silent-orphan behavior that made the manual
  finalizer wipe dangerous is not an improvement.

### Option C: Block until clean, with an explicit human override — CHOSEN
- **Approach**: Proceed with seed cleanup; retain the finalizer indefinitely while shoot
  cleanup is outstanding, surfacing a loud `ShootCleanupBlocked` condition + event; remove the
  finalizer only when shoot cleanup succeeds, or when an operator sets a `force-delete`
  annotation (a supported, signalled replacement for the raw finalizer wipe).
- **Pros**: Never silently orphans — the conservative Kubernetes finalizer stance. A stuck
  delete stays visible and keeps retrying (the only retry mechanism this operator has). The
  "stuck forever" pain is resolved by a documented, low-friction override, not a scary
  `kubectl patch finalizers:[]`.
- **Cons**: A genuinely-gone shoot leaves the CR in `Terminating` until an operator applies the
  override annotation (deliberate: orphaning requires explicit human consent).
- **Why chosen**: Given no post-finalizer retry agent, blocking + explicit override is the only
  option that neither deadlocks nor silently orphans.

## Agreed Approach

Fix `reconcileDelete` (Option C) and add reconcile-pipeline logging. Nothing else.

1. **Do not hard-return when the shoot applier cannot be built.** Record `ShootUnreachable`
   (event + `Ready=False`) and proceed to seed cleanup. Seed cleanup is safe to run whether or
   not the shoot is reachable — it does not depend on the shoot credential (see Key Decisions).
2. **Keep reverse-of-`applyOrder` teardown order unchanged.** `reconcileDelete` continues to
   tear down in the reverse of `spec.applyOrder` exactly as today; when the shoot applier is
   nil (build failed), the shoot side is simply skipped for this pass.
3. **Block until clean, with explicit override.** Remove the finalizer when shoot cleanup
   succeeded (or was unneeded); or when the operator set
   `dual-deployment-operator.cc.sap/force-delete: "true"`; otherwise retain the finalizer,
   requeue after 30s, and surface `ShootCleanupBlocked`. Force-delete emits a distinct
   `ShootCleanupForceDeleted` signal.
4. **Add reconcile-pipeline logging** at source pull, render/validation, render-cache hit/miss,
   and per-target apply/prune/delete.

## Key Decisions

- **Block until clean, never a timeout, never best-effort**: this operator has no
  post-finalizer retry agent, so any automatic finalizer removal with cleanup outstanding
  silently and irrecoverably orphans shoot resources on a transient outage. Retaining the
  finalizer (and retrying) is the only safe automatic behavior.
- **`force-delete` annotation is the sole orphaning path**: orphaning happens only by explicit,
  auditable human consent — a supported replacement for the raw `kubectl patch finalizers:[]`.
  Proposed contract: `dual-deployment-operator.cc.sap/force-delete: "true"`.
- **Distinct signals** — `ShootCleanupBlocked` (waiting on shoot cleanup) vs.
  `ShootCleanupForceDeleted` (operator overrode; shoot resources may be orphaned) — so both the
  blocked state and any orphaning are auditable via `kubectl get`. `RetentionPolicy{CRDs:Retain}`
  covers only intended CRD retention; the real orphan risk is leftover non-CRD shoot resources
  (Deployments, RBAC, webhooks), which the force-delete signal surfaces.
- **`spec.applyOrder` is left entirely untouched** — no default change, no doc-comment change,
  no delete-order change. Reverse-order deletion driven by `applyOrder` is a legitimate generic
  capability for consumers with a real teardown ordering dependency, and there is no
  correctness reason to change it: the shoot credential lives in the operator install chart, so
  the historical first-deploy ordering concern no longer applies.
- **Seed cleanup needs no credential-preservation logic**: the shootAccess Secret is provisioned
  by the operator install chart (`chart/templates/shoot-rbac/`, `shootRbac.enabled`), not by any
  CR's seed render, so DDO's seed teardown can never delete its own shoot access. Seed cleanup
  deletes `Status.SeedResources` unconditionally (minus retained CRDs).
- **No CRD schema change; purely `internal/controller` + `internal/source`**: the fix is
  control-flow plus an annotation (no schema field) plus logging. No `api/v1alpha1` change, no
  `make manifests generate`.
- **DDO stays generic**: all behavior keys off generic CR fields (`spec.shootAccess.secretName`,
  `Status.SeedResources`/`ShootResources`), never operator name / chart labels / `metal-*`
  identifiers. Metal-specific deployment facts (hardcoded `applyOrder: SeedFirst`, GRM
  shoot-RBAC bootstrap, install-chart-owned token-requestor Secret) live in the chart; the
  reconciler is chart- and GRM-agnostic.
- **Logging is observability-only**: structured `logr` logs following K8s message-style
  guidelines; no behavior change; no secret or token bytes ever logged.

## Open Questions

- [ ] Confirm the override contract `dual-deployment-operator.cc.sap/force-delete: "true"`
      (key name + string value) — owner: user (design/spec review).
