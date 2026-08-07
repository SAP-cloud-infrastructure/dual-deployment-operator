<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Brainstorm: robust-teardown-finalization

## Design Summary

`kubectl delete dualdeploymentoperator <name>` can deadlock: `reconcileDelete` builds the
shoot applier **first** and **hard-returns** when it fails (shoot unreachable / shootAccess
Secret absent), so seed teardown never runs and the finalizer is never removed — the CR is
stuck `Terminating` forever, requiring a manual `kubectl patch ... finalizers:[]` wipe that
silently orphans shoot resources.

The issue was originally filed against a *self-deadlock* (the shootAccess Secret used to be
part of the workload's seed render, so DDO's own seed teardown deleted the credential its
shoot teardown needed). **That self-deadlock is now gone by construction**: feature-branch
commits `7bb8fb9`/`9c09682`/`906d936` moved the token-requestor Secret + shoot-applier RBAC
into the **operator install chart** (`chart/templates/shoot-rbac/`, `shootRbac.enabled`), so
the credential no longer lives in any CR's seed render.

What remains is the **generic** bug: if the shoot is genuinely unreachable/gone at CR-delete
time — shoot API down, network partition, shoot being torn down, or the operator install chart
uninstalled first (removing the credential Secret) — the shoot-applier build fails and
`reconcileDelete` hard-returns, deadlocking the delete. This is realistic (a CR is often
deleted *because* its shoot is going away) and reachable, so it is worth fixing.

The change is deliberately **narrow and generic**: it is purely a `reconcileDelete` behavior
fix plus reconcile-pipeline logging. It does **not** touch `spec.applyOrder` (no default flip,
no reframing, no delete-order change), makes **no** CRD schema change, and adds **no**
credential-preservation logic. Concretely:

1. **Don't hard-return on shoot-client build failure.** Record `ShootUnreachable`
   (event + `Ready=False`), then **proceed to seed cleanup** (which no longer touches the
   shoot credential — it lives in the install chart).
2. **Block until clean, with an explicit human override.** While shoot cleanup is incomplete,
   **retain the finalizer indefinitely** (no blind timeout) and surface a loud
   `ShootCleanupBlocked` condition + event. Remove the finalizer only when shoot cleanup
   succeeds/was unneeded, OR when an operator sets
   `dual-deployment-operator.cc.sap/force-delete: "true"` — an explicit, auditable,
   supported replacement for the bare `kubectl patch finalizers:[]`, signalled distinctly as
   `ShootCleanupForceDeleted`.
3. **Reconcile-pipeline observability logging.** Structured logs at the key stages (source
   pull of the upstream chart/kustomization, render/validation, render-cache hit/miss,
   per-target apply/prune/delete), following K8s logging message-style guidelines.

## Alternatives Considered

The load-bearing axis is **what to do when shoot cleanup cannot complete**: block (retain the
finalizer until clean or a human consents) vs. bounded timeout (auto-remove the finalizer
after a delay, orphaning shoot resources). Options A/B are timeout variants; the chosen
approach blocks.

### Option A: Bounded timeout then orphan (issue-doc / earlier Oracle design)
- **Approach**: Don't hard-return; run seed cleanup; retain the finalizer until shoot cleanup
  completes **or a 30 min timeout (from `deletionTimestamp`) expires**, then remove the
  finalizer with a distinct `ShootCleanupAbandoned` signal.
- **Pros**: The CR always eventually disappears without human action; matches Flux
  `WaitForTermination` / Gardener grace-then-force precedent.
- **Cons**: This operator has **no post-finalizer retry agent** (unlike the GRM system it
  replaces, whose async seed-side reconciler kept retrying shoot cleanup after `helm
  uninstall`). A blind timeout therefore turns a *transient* shoot outage into *permanent,
  unretried* orphaning — strictly worse than the precedent. Orphaning is silent-ish (only an
  event/condition a human may not be watching).
- **Why not chosen**: The user chose the never-silently-orphan stance. Auto-removing a
  finalizer with cleanup outstanding is exactly what finalizers exist to prevent, and the
  missing retry agent makes the timeout's orphaning unrecoverable.

### Option B: Immediate best-effort — delete finalizer regardless of shoot outcome
- **Approach**: Don't hard-return; attempt shoot cleanup once, then remove the finalizer
  unconditionally (best-effort shoot delete).
- **Pros**: Simplest; delete never hangs.
- **Cons**: Silently orphans shoot resources on any transient outage, with no retry and only
  a fire-and-forget attempt. Worst of both worlds for reliability.
- **Why not chosen**: Silent orphaning is precisely the danger the original manual
  finalizer-wipe posed; automating it is not an improvement.

### Option C: Broaden scope — reframe `applyOrder`, flip its default, decouple deletion
- **Approach**: Also narrow `spec.applyOrder` to apply-time only, flip its default
  `ShootFirst`→`SeedFirst`, and decouple `reconcileDelete` from `applyOrder` (fixed teardown
  order).
- **Pros**: Would make the API "coherent" if `applyOrder`'s dual apply+delete role were
  considered a defect.
- **Cons**: Re-examination showed the premises don't hold. (a) With the credential in the
  install chart, `ShootFirst` no longer *deadlocks* first deploy — it merely defers the seed
  render for a cycle or two until Gardener fills the token; the v2 chart comment itself was
  softened to "avoids a first-deploy ordering surprise," not "prevents a deadlock." So the
  default flip is a preference, not a fix. (b) Reverse-order deletion driven by `applyOrder`
  is a **wanted generic capability** — a future consumer may have a real teardown ordering
  dependency — so it is intended contract, not conflation. (c) Flipping the generic default to
  suit the metal case (which already sets `SeedFirst` explicitly in its chart) embeds a
  preference and contradicts "keep DDO generic."
- **Why not chosen**: Out of scope for a bug fix, and its correctness justification collapsed
  under re-examination. `applyOrder` is left entirely untouched.

## Agreed Approach

**Narrow, generic deadlock fix + logging.** `reconcileDelete` stops hard-returning on
shoot-client build failure and instead proceeds to seed cleanup; the finalizer is retained
(blocking, never silently orphaning) with a loud signal until shoot cleanup succeeds or a
human sets the `force-delete` annotation. Reverse-order deletion and everything about
`spec.applyOrder` are left exactly as they are today (generic capability, no change). Add
reconcile-pipeline logging. No CRD schema change; purely `internal/controller` behavior +
logging.

Chosen over the timeout variants (A/B) because this operator has no post-finalizer retry
agent, so a blind timeout / best-effort delete would permanently and silently orphan shoot
resources on a merely transient outage. Chosen over the broadened scope (C) because the
`applyOrder` changes are not part of this bug and their justification collapsed under
re-examination.

Concretely:

1. **No hard-return on shoot-client build failure** — `reconcileDelete` records
   `ShootUnreachable` (event + `Ready=False`) and falls through to seed cleanup instead of
   returning early. Seed cleanup no longer touches the shoot credential (install-chart-owned),
   so it is safe to run whether or not the shoot is reachable.
2. **Reverse-order deletion unchanged** — `reconcileDelete` continues to tear down in the
   reverse of `spec.applyOrder` exactly as today. This is a generic capability, not modified.
3. **Block until clean, with explicit human override** — no blind timeout. Remove the
   finalizer when shoot cleanup succeeded/unneeded AND seed cleanup done; OR when the operator
   has set `dual-deployment-operator.cc.sap/force-delete: "true"` AND seed cleanup done —
   emitting a distinct `ShootCleanupForceDeleted` event + `ShootCleanup{Reason: ForceDeleted}`
   condition. Otherwise **retain the finalizer indefinitely**, `RequeueAfter=30s` (keep
   retrying — the operator's only retry mechanism), and surface a loud `ShootCleanupBlocked`
   condition + event naming the override annotation.
4. **Reconcile-pipeline observability logging** — structured logs at source pull,
   render/validation, render-cache hit/miss, and per-target apply/prune/delete stages.

## Key Decisions

- **DDO stays generic; the metal case is only the motivating worst case**: rationale — all
  reconciler behavior keys off generic CR fields (`spec.shootAccess.secretName`,
  `Status.SeedResources`/`ShootResources`), never operator name / chart labels / `metal-*`
  identifiers. Metal-specific deployment facts (hardcoded `applyOrder: SeedFirst`, GRM
  shoot-RBAC bootstrap, token-requestor Secret in the install chart) stay in the chart; this
  change adds no chart-aware or GRM-aware logic.
- **Leave `spec.applyOrder` entirely untouched** (user decision, post re-examination): no
  default flip, no doc-comment reframing, no delete-order decoupling. Reverse-order deletion
  driven by `applyOrder` is a **wanted generic capability** (a future consumer may have a real
  teardown ordering dependency). The earlier "flip the default / narrow to apply-time only"
  proposal was dropped because, with the credential now install-chart-owned, `ShootFirst` no
  longer deadlocks first deploy (it only briefly defers the seed render), so the flip was a
  preference — not a fix — and would embed a preference into a generic default.
- **The self-deadlock is gone by construction; no credential-preservation predicate**:
  rationale — the shootAccess Secret now lives in the operator install chart, not the workload
  seed render, so DDO's seed teardown can never delete its own shoot credential. Seed cleanup
  deletes `Status.SeedResources` unconditionally (minus retained CRDs). The remaining fix is
  purely about not hard-returning when the shoot itself is unreachable.
- **Block until clean, not a bounded timeout** (user decision): rationale — unlike the GRM
  precedent, nothing retries shoot cleanup after the finalizer is removed, so a blind timeout
  would turn a transient shoot outage into permanent, unretried orphaning. Retaining the
  finalizer indefinitely (the conservative, generic-Kubernetes stance) never silently orphans.
- **Explicit `force-delete` annotation as the only orphaning path**: rationale — the "stuck
  `Terminating` forever" complaint is addressed by a documented, low-friction override
  (`dual-deployment-operator.cc.sap/force-delete: "true"`) that replaces the unsafe bare
  `kubectl patch finalizers:[]`. Orphaning happens only by explicit human consent, signalled
  distinctly.
- **Distinct `ShootCleanupBlocked` (waiting) vs `ShootCleanupForceDeleted` (overridden)
  signals**: rationale — make both the blocked state and any orphaning explicit and auditable
  via `kubectl get`; `RetentionPolicy{CRDs:Retain}` explains only intended CRD retention, not
  leftover non-CRD shoot resources (Deployments, RBAC, webhooks), which are the real orphan
  risk the force-delete signal surfaces.
- **No CRD schema change; purely `internal/controller` behavior + logging**: rationale — the
  fix is behavioral (control flow in `reconcileDelete`) plus an annotation (no schema field)
  plus logging. No `api/v1alpha1` change, no `make manifests generate` churn.
- **Add reconcile-pipeline observability logging** (bundled scope): rationale — the deadlock
  was hard to diagnose live partly because the reconcile pipeline is quiet at stage
  boundaries. Add structured logs (source pull, render/validation, render-cache hit/miss,
  apply/prune/delete) following K8s logging message-style guidelines. Observability-only; no
  behavior change; no secret/token bytes logged.

## Open Questions

- [ ] Annotation key/value ergonomics — confirm `dual-deployment-operator.cc.sap/force-delete:
      "true"` is the desired override contract (key name + string value), or whether a
      different key/semantics is preferred — owner: user (design/spec review).
