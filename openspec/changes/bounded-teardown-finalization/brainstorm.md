<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Brainstorm: bounded-teardown-finalization

## Design Summary

`kubectl delete dualdeploymentoperator <name>` can deadlock forever: `reconcileDelete`
builds the shoot applier **first** and **hard-returns** when it fails (missing/unreachable
shootAccess Secret), so seed teardown never runs and the finalizer is never removed. The
self-deadlock is structural — the shootAccess Secret is itself part of the **seed render**
(a Gardener token-requestor Secret shell), so seed teardown is what deletes the very
credential the shoot applier needs.

Investigation surfaced a second, deeper defect: the CR's `spec.applyOrder` field
(`SeedFirst`/`ShootFirst`, **default `ShootFirst`**) conflates two unrelated concerns —
*apply-convergence sequencing* and *teardown-dependency ordering* — and reverse-order
deletion is the mechanism that triggers the deadlock. The verified-correct value for any
operator whose shoot credential is produced by its own seed render is `SeedFirst`; the only
real consumer (`metal-operator-remote-v2`) hardcodes `applyOrder: SeedFirst` with a chart
comment stating that the `ShootFirst` default **deadlocks first deploy**.

The agreed change fixes both, coherently and minimally, while the CRD is still pre-live (no
live CRs, so a schema tweak is cheap): (1) flip the `applyOrder` default to `SeedFirst` and
narrow its contract to apply-time only; (2) decouple the delete path from `applyOrder` —
always attempt shoot cleanup first while **preserving the shootAccess Secret**, then run
seed cleanup; remove the finalizer when shoot cleanup succeeds (or is unneeded) and seed
cleanup completes, or after a bounded retry window (`remoteDeleteTimeout`, 30 min, measured
from `deletionTimestamp`) with a distinct `ShootCleanupAbandoned` event + condition. No
credential-lifecycle redesign, no second finalizer.

Key constraints that shaped the approach:
- The real invariant is simple: **shoot access must exist before any shoot operation**, on
  both apply and delete. This is a credential dependency, not a symmetric create/reverse-
  destroy ownership dependency.
- Unlike the GRM precedent it replaces (gardener-resource-manager kept retrying shoot
  cleanup asynchronously after `helm uninstall`), this operator has **no post-finalizer
  retry agent** — once the finalizer is gone, nothing retries. A *bounded finalizer* (retry
  then explicit abandonment) is therefore the correct compromise, not fire-and-forget.
- Building the shoot client dereferences **only** the shootAccess Secret's `token` /
  `bundle.crt` keys plus `spec.shootAccess.server` (a CR field) — so preserving that single
  Secret is provably sufficient to keep the credential usable during retries.

## Alternatives Considered

### Option A: Narrow bounded finalization only (issue-doc / earlier Oracle design)
- **Approach**: Implement bounded finalization exactly as the issue doc prescribes —
  classify shoot-cleanup need, don't hard-return on shoot-client build failure, run seed
  cleanup with a skip-predicate preserving the shootAccess Secret until shoot cleanup
  completes or a 30 min timeout expires, then remove the finalizer with a distinct
  `ShootCleanupAbandoned` signal. **Keep `applyOrder` and reverse-order deletion as-is.**
- **Pros**: Smallest diff; no CRD schema change; directly follows the code's real
  dependency; matches Flux `WaitForTermination` / Gardener grace-then-force precedent.
- **Cons**: Leaves a pre-live CRD default (`ShootFirst`) that deadlocks first deploy for
  the only verified consumer, so every real CR must keep force-overriding it; preserves the
  misleading `applyOrder` = "apply order + reverse delete order" overloading.
- **Why not chosen**: It unblocks the bug but leaves the design *incoherent* — a default
  that is known-wrong and an overloaded field — while a schema fix is still free (pre-live).

### Option B: Timeout-only, no credential preservation
- **Approach**: Don't hard-return; bound teardown by timeout, but skip the preserve
  predicate — delete all seed resources every pass and let the shoot applier fail benignly
  until the timeout expires.
- **Pros**: Simplest possible diff (no predicate, no classify step).
- **Cons**: Reintroduces the exact self-deadlock one phase later — once seed cleanup deletes
  the shootAccess Secret, shoot cleanup can never succeed even if the shoot IS reachable, so
  it always burns the full 30 min and always orphans. The issue doc's own pitfalls section
  explicitly calls this out.
- **Why not chosen**: Turns a correctness bug into a "wait 30 min then orphan" path even in
  the happy, shoot-reachable case. Rejected.

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
  real but sequential; decoupling deletion from `applyOrder` + preserving the Secret gives
  most of the value with far less churn. Deferred as a possible future model if more
  consumers with externally-managed credentials appear.

## Agreed Approach

**Coherent minimal fix** (a refinement of Option A that also corrects the API framing).
Chosen over pure Option A because the CRD is still pre-live, so fixing the misleading
`applyOrder` default is cheap now and leaves the design coherent rather than merely
unblocked; chosen over Option C because the credential dependency is sequential and does not
justify a second finalizer or a credential-lifecycle redesign in a bug fix.

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
3. **Credential preservation** — seed cleanup runs with a predicate that preserves exactly
   the shootAccess Secret (matched by namespaced identity) while shoot cleanup is incomplete
   and the timeout has not expired. Verified sufficient: `ShootRESTConfig` reads only that
   Secret's `token`/`bundle.crt` plus `spec.shootAccess.server`.
4. **Bounded finalizer** — `const remoteDeleteTimeout = 30 * time.Minute`, measured from
   `cr.DeletionTimestamp` (no new status field). Remove the finalizer when shoot cleanup
   succeeded/unneeded AND full seed cleanup done; OR when the window is exceeded AND seed
   cleanup (incl. the credential) done — emitting a distinct `ShootCleanupAbandoned` Warning
   event + a dedicated `ShootCleanup=False` status condition. Otherwise `RequeueAfter=30s`
   (retain, keep retrying — the operator's only retry mechanism).

## Key Decisions

- **DDO stays generic; the metal case is only the motivating worst case**: rationale — all
  reconciler behavior keys off generic CR fields (`spec.shootAccess.secretName` in the CR's own
  namespace), never operator name / chart labels / `metal-*` identifiers. The teardown fix is
  generic for ALL DDO consumers; an externally-provisioned-credential operator hits the same
  path (its seed cleanup simply has no shootAccess Secret to preserve — the predicate no-ops).
  Metal-specific deployment facts (hardcoded `applyOrder: SeedFirst`, GRM shoot-RBAC bootstrap,
  token-requestor Secret) stay in the chart; this change adds no chart-aware or GRM-aware logic.
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
  credential dependency (shootAccess Secret in the seed render) dominates and is unidirectional
  on both apply and delete; encoding delete order as "reverse of apply order" is the root
  conceptual bug.
- **Preserve only the shootAccess Secret (verified sufficient)**: rationale — building the
  shoot client dereferences only that Secret's `token`/`bundle.crt` + a CR field; no
  namespace object or auxiliary resource is needed. Match by namespaced identity (it is a
  namespaced Secret in the CR's own namespace, not cluster-scoped).
- **Bounded finalizer, not fire-and-forget**: rationale — unlike the GRM precedent, nothing
  retries shoot cleanup after the finalizer is removed, so a bounded retry-then-abandon is
  the correct compromise between "deadlock forever" and "silently orphan immediately".
- **Distinct `ShootCleanupAbandoned` event + `ShootCleanup` condition on timeout**: rationale —
  make orphaning explicit and auditable via `kubectl get`; `RetentionPolicy{CRDs:Retain}`
  explains only intended CRD retention, not leftover non-CRD shoot resources (Deployments,
  RBAC, webhooks), which are the real orphan risk this signal surfaces.
- **`remoteDeleteTimeout` stays a package const (30 min)**: rationale — mirrors the existing
  `requeueInterval` const; a timeout is policy, not correctness. Make it a manager flag only
  later if a consumer needs a different risk tolerance.
- **No `internal/deliver` change; `deleteRender` gains a controller-local `skip` param**:
  rationale — one caller, YAGNI; don't extract a reusable predicate helper prematurely.

## Open Questions

- [ ] Confirm 30 min is the right bounded window for production shoot outages, or whether it
      should be shorter/longer — owner: user (deferred; const is trivially tunable later).
