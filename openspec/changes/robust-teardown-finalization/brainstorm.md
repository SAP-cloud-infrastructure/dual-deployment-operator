<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Brainstorm: robust-teardown-finalization

## Design Summary

`kubectl delete dualdeploymentoperator <name>` can deadlock: `reconcileDelete` builds the
shoot applier **first** and **hard-returns** when it fails (shoot unreachable / shootAccess
Secret absent), so seed teardown never runs and the finalizer is never removed — the CR is
stuck `Terminating` forever.

The issue was originally filed against a *self-deadlock*: the shootAccess Secret used to be
part of the workload's **seed render** (a Gardener token-requestor Secret shell), so DDO's own
seed teardown deleted the very credential its shoot teardown needed — a guaranteed deadlock on
every delete. **That is now structurally fixed** by feature-branch commits
`7bb8fb9`/`9c09682`/`906d936`, which moved the token-requestor Secret + shoot-applier RBAC into
the **operator install chart** (`chart/templates/shoot-rbac/`, `shootRbac.enabled`). The
credential no longer lives in any CR's seed render, so seed teardown can never delete it. What
remains is the **generic** case: a genuinely unreachable/gone shoot at CR-delete time still
hard-returns and deadlocks. This change fixes that.

Investigation surfaced a second, deeper defect: the CR's `spec.applyOrder` field
(`SeedFirst`/`ShootFirst`, **default `ShootFirst`**) conflates two unrelated concerns —
*apply-convergence sequencing* and *teardown-dependency ordering* — and reverse-order
deletion is the mechanism that triggers the deadlock. The verified-correct value for any
operator whose shoot credential is produced by its own seed render is `SeedFirst`; the only
real consumer (`metal-operator-remote-v2`) hardcodes `applyOrder: SeedFirst`.

The agreed change fixes both, coherently and minimally, while the CRD is still pre-live (no
live CRs, so a schema tweak is cheap): (1) flip the `applyOrder` default to `SeedFirst` and
narrow its contract to apply-time only; (2) decouple the delete path from `applyOrder` —
always attempt shoot cleanup first, then run seed cleanup unconditionally (no
credential-preservation predicate needed — the credential is no longer in the seed render);
don't hard-return on shoot-client build failure. When shoot cleanup is incomplete, **retain
the finalizer indefinitely** (no blind timeout) and surface a loud `ShootCleanupBlocked`
condition; orphaning happens **only** when an operator sets an explicit
`dual-deployment-operator.cc.sap/force-delete: "true"` annotation — a supported, signalled
replacement for the unsafe bare `kubectl patch finalizers:[]`. No credential-lifecycle
redesign, no second finalizer, no skip-predicate.

Key constraints that shaped the approach:
- The real invariant is simple: **shoot access must exist before any shoot operation**, on
  both apply and delete. This is a credential dependency, not a symmetric create/reverse-
  destroy ownership dependency.
- Unlike the GRM precedent it replaces (gardener-resource-manager kept retrying shoot
  cleanup asynchronously after `helm uninstall`), this operator has **no post-finalizer
  retry agent** — once the finalizer is gone, nothing retries. A blind timeout would
  therefore turn a transient shoot outage into *permanent, unretried* orphaning. So the
  operator **blocks** (retains the finalizer) until cleanup succeeds or a human explicitly
  consents to orphaning — the conservative, never-silently-orphan stance.
- The credential now lives in the operator install chart, not the workload seed render, so
  the elaborate credential-preservation logic of earlier revisions is unnecessary.

## Alternatives Considered

The load-bearing axis is **what to do when shoot cleanup cannot complete**: block
(retain the finalizer until clean or a human consents) vs. bounded timeout (auto-remove the
finalizer after a delay, orphaning shoot resources). Options A/B are timeout variants; the
chosen approach blocks.

### Option A: Bounded timeout then orphan (issue-doc / earlier Oracle design)
- **Approach**: Don't hard-return on shoot-client build failure; classify shoot-cleanup need;
  run seed cleanup with a skip-predicate preserving the shootAccess Secret until shoot cleanup
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

### Option B: Timeout-only, no credential preservation
- **Approach**: Don't hard-return; bound teardown by timeout, but skip the preserve
  predicate — delete all seed resources every pass and let the shoot applier fail benignly
  until the timeout expires.
- **Pros**: Simplest possible diff (no predicate, no classify step).
- **Cons**: Reintroduces the exact self-deadlock one phase later — once seed cleanup deletes
  the shootAccess Secret, shoot cleanup can never succeed even if the shoot IS reachable, so
  it always burns the full window and always orphans. The issue doc's own pitfalls section
  explicitly calls this out.
- **Why not chosen**: Turns a correctness bug into a "wait then orphan" path even in the
  happy, shoot-reachable case. Rejected.

### Option C: Full applyOrder removal / rebuild, or a two-finalizer credential-lifecycle model
- **Approach**: Either remove `ShootFirst` from the enum entirely, or introduce a second
  finalizer (e.g. `shoot-cleanup`) so seed teardown and finalizer removal are decoupled from
  shoot teardown, and/or move the shootAccess Secret out of the seed render so the credential
  outlives teardown naturally.
- **Pros**: Cleanest separation of concerns long-term; each cluster's teardown owns its own
  finalizer; an explicit credential-lifecycle contract.
- **Cons**: Largest change; two-finalizer add/remove wiring on create + both delete paths;
  removing `ShootFirst` presumes no future externally-provisioned-credential use case ever
  needs it; blurs `applyOrder` semantics across finalizers; over-engineered for a single
  teardown flow that is fundamentally sequential.
- **Why not chosen**: More machinery than the bug warrants. The credential dependency is
  real but sequential, and (post credential-ownership move) the self-deadlock is gone by
  construction; decoupling deletion from `applyOrder` + block-until-clean gives most of the
  value with far less churn. Deferred as a possible future model if more consumers with
  externally-managed credentials appear.

## Agreed Approach

**Block until clean, with an explicit human override** (never silently orphan) + the
`applyOrder` API framing correction. Chosen over the timeout variants (A/B) because this
operator has no post-finalizer retry agent, so a blind timeout would permanently orphan shoot
resources on a merely transient outage; chosen over Option C because the credential dependency
is sequential and does not justify a second finalizer or a credential-lifecycle redesign in a
bug fix. The "stuck `Terminating` forever" complaint that motivated the issue is addressed not
by a timer but by a *documented, low-friction* override annotation that replaces the unsafe
bare `kubectl patch finalizers:[]`.

Concretely:

1. **`applyOrder` API reframing** — flip `+kubebuilder:default` from `ShootFirst` to
   `SeedFirst`; narrow the field's doc comment so it governs *apply-convergence sequencing
   only*, not deletion order. Keep `ShootFirst` in the enum, documented as valid only when
   shoot credentials exist independently of the seed render.
2. **Deletion decoupled from `applyOrder`** — `reconcileDelete` no longer branches on
   `applyOrder`. Teardown is always **shoot-first-then-seed**: classify shoot-cleanup need
   from `Status.ShootResources` (minus retained CRDs); build the shoot applier only when
   needed; on build failure record `ShootUnreachable` (event + `Ready=False`) but **do not
   return** — fall through to seed cleanup.
3. **No credential-preservation predicate (simplification)** — seed cleanup deletes
   `Status.SeedResources` unconditionally (minus retained CRDs). The shootAccess Secret now
   lives in the operator install chart (`shootRbac.enabled`), not the workload seed render, so
   DDO's seed teardown can never delete its own shoot credential — there is nothing to preserve.
   (Feature-branch commits `7bb8fb9`/`9c09682`/`906d936`.)
4. **Block until clean, with explicit human override** — no blind timeout. Remove the
   finalizer when shoot cleanup succeeded/unneeded AND full seed cleanup done; OR when the
   operator has set `dual-deployment-operator.cc.sap/force-delete: "true"` AND seed cleanup
   (incl. the credential) done — emitting a distinct `ShootCleanupForceDeleted` event +
   `ShootCleanup{Reason: ForceDeleted}` condition. Otherwise **retain the finalizer
   indefinitely**, `RequeueAfter=30s` (keep retrying — the operator's only retry mechanism),
   and surface a loud `ShootCleanupBlocked` condition + event naming the override annotation.

## Key Decisions

- **DDO stays generic; the metal case is only the motivating worst case**: rationale — all
  reconciler behavior keys off generic CR fields (`spec.shootAccess.secretName`,
  `Status.SeedResources`/`ShootResources`), never operator name / chart labels / `metal-*`
  identifiers. The teardown fix is generic for ALL DDO consumers. A consumer that puts the
  shootAccess Secret in its own seed render would reintroduce the self-deadlock for itself; its
  escape hatch is the generic `force-delete` override — no metal-specific handling.
  Metal-specific deployment facts (hardcoded `applyOrder: SeedFirst`, GRM shoot-RBAC bootstrap,
  token-requestor Secret in the install chart) stay in the chart; this change adds no
  chart-aware or GRM-aware logic.
- **Flip `applyOrder` default to `SeedFirst` now** (user-confirmed, belongs in this change):
  rationale — the current default (`ShootFirst`) is actively wrong for the common fleet case
  (seed-rendered credential → deadlocks first deploy); the CRD is pre-live so the schema change
  is free; bundling it here keeps the `applyOrder` correction atomic (apply default + delete
  decoupling are two halves of the same fix); leaves the API coherent. Generic default, not a
  metal carve-out. Requires `make manifests generate` (CRD bases + zz_generated).
- **Keep `ShootFirst` in the enum, don't remove it**: rationale — no proven guarantee that a
  future externally-provisioned-credential operator won't want it; removal is larger churn
  for no present benefit. Decouple deletion from it instead.
- **Deletion is always shoot-first-then-seed, independent of `applyOrder`**: rationale — the
  credential dependency (shoot access must exist before touching the shoot) is unidirectional
  on both apply and delete; encoding delete order as "reverse of apply order" is the root
  conceptual bug.
- **No credential-preservation predicate; no `deleteRender` skip param** (simplification):
  rationale — the shootAccess Secret now lives in the operator install chart, not the workload
  seed render (feature-branch commits `7bb8fb9`/`9c09682`/`906d936`), so DDO's seed teardown can
  never delete its own shoot credential. The self-deadlock is eliminated by construction; the
  intricate preserve-the-Secret logic of earlier revisions is unnecessary. Seed cleanup deletes
  `Status.SeedResources` unconditionally (minus retained CRDs).
- **Block until clean, not a bounded timeout** (user decision): rationale — unlike the GRM
  precedent, nothing retries shoot cleanup after the finalizer is removed, so a blind timeout
  would turn a transient shoot outage into permanent, unretried orphaning. Retaining the
  finalizer indefinitely (the conservative, generic-Kubernetes stance) never silently orphans.
- **Explicit `force-delete` annotation as the only orphaning path**: rationale — the "stuck
  `Terminating` forever" complaint is addressed by a documented, low-friction override
  (`dual-deployment-operator.cc.sap/force-delete: "true"`) that replaces the unsafe bare
  `kubectl patch finalizers:[]`. Orphaning happens only by explicit human consent, and is
  signalled distinctly.
- **Distinct `ShootCleanupBlocked` (waiting) vs `ShootCleanupForceDeleted` (overridden)
  signals**: rationale — make both the blocked state and any orphaning explicit and auditable
  via `kubectl get`; `RetentionPolicy{CRDs:Retain}` explains only intended CRD retention, not
  leftover non-CRD shoot resources (Deployments, RBAC, webhooks), which are the real orphan
  risk the force-delete signal surfaces.
- **Add reconcile-pipeline observability logging** (bundled scope): rationale — the deadlock was
  hard to diagnose live partly because the reconcile pipeline is quiet. Add structured logs at
  the key stages (source pull of the upstream chart/kustomization, render/validation, render-cache
  hit/miss, per-target apply/prune/delete) following the K8s logging message-style guidelines.
  Observability-only; no behavior change.

## Open Questions

- [ ] Annotation key/value ergonomics — confirm `dual-deployment-operator.cc.sap/force-delete:
      "true"` is the desired override contract (key name + string value), or whether a
      different key/semantics is preferred — owner: user (design/spec review).
