<!-- SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

## Context

`internal/transform` `patch.Apply` is fail-loud on zero matches: after iterating the
manifest stream it returns `errors.New("no matching resource for patch target selector")`
when nothing matched ([`patch.go`](../../../internal/transform/patch.go) lines 53–55). The
reconciler applies every transform to **both** renders (seed + shoot) independently
([`dualdeploymentoperator_controller.go`](../../../internal/controller/dualdeploymentoperator_controller.go)
lines 130–140). Combined, these mean a `patch` whose target kind exists in only one render
matches there but errors on the other, failing the whole reconcile with
`SeedTransformFailed` / `ShootTransformFailed`.

This blocks the design's own r7 pattern (design §3.4.4): stamping the webhook-injector
`--target-label` onto webhook objects via a CR `patch`. A `ValidatingWebhookConfiguration`
exists only in the shoot render, so a label `patch` targeting it matches in the shoot render
but errors on the seed render. The motivating consumer is `sapcc/helm-charts`
`system/metal-operator-remote-v2`, whose VWC comes from the upstream `metal-operator`
subchart — the upstream chart has no values hook to add a custom label, and a Helm wrapper
cannot edit a subchart's rendered output, so the label MUST come from a CR `patch`, which the
current fail-loud behavior makes impossible.

**Current state**: the shared `Transformation` interface is
`Apply([]manifest.Manifest) ([]manifest.Manifest, error)`, implemented by `patch`,
`rewriteWebhookURL`, and `filterKinds`. The controller has an `errStatus(ctx, cr, reason, err)`
helper and an existing `InvalidTransformation` status reason (used for `transform.Build`
failures).

**Constraints**: no CRD change (Form A); operator-only scope (`internal/transform` +
controller loop + tests + one equivalence fixture); no `api/` change, no source/loader/delivery
change, no upstream bump. Repo AGENTS.md mandates test-first (TDD) and a `code-reviewer` pass
for multi-file changes.

**Stakeholders**: the operator itself; `metal-operator-remote-v2` and `ipam-capi` (the two
operators whose CRs will carry injector-label patches on single-render webhook objects); the
webhook-injector (target patch mode) that adopts the labeled objects.

## Goals / Non-Goals

**Goals:**
- A `patch` transform whose selector matches nothing in a given render is a **clean, silent
  no-op** for that render (returns the input manifests unchanged, no error) — identical to how
  `rewriteWebhookURL` and `filterKinds` already behave on zero match.
- The reconcile is **never stopped** by a zero-match patch, in any render or across both.
- Declaration order is preserved — a no-op render passes its unchanged manifests through so
  later transforms in the sequence still see the full stream.
- The r7 injector-label pattern (`patch` on a shoot-only VWC) succeeds without failing the
  seed render, proven end-to-end by an equivalence fixture.

**Non-Goals:**
- **No CRD change.** Form B (`scope: seed|shoot|both` field on `PatchSpec`) is explicitly out
  of scope for this change. It is documented below (Decisions → "Option C deferred") as a
  **non-breaking future upgrade** to be revisited at the v1beta1 schema step, not foreclosed.
- **No typo backstop or warning.** A `patch` that matches nothing anywhere is a silent no-op;
  there is no `TransformNoMatch` status condition or event and no controller change.
- No change to `rewriteWebhookURL` / `filterKinds` behavior.
- No change to the shared `Transformation.Apply` signature.
- No `api/`, source/loader, delivery, or upstream-version change.

## Capabilities

**Modified Capabilities:**
- `manifest-transformation` — the `patch` application semantics change: a zero-match `patch`
  is a clean no-op that never errors and never stops the reconcile, making it consistent with
  `rewriteWebhookURL` and `filterKinds`. No controller change; no new status/event reason.

<!-- No new capabilities; this modifies the existing transform behavior. -->

## Decisions

**Decision: Form A — zero-match is a silent no-op**
- Chosen: Remove the `matched == 0 → error` guard (and the now-unused `matched` counter) from
  `patch.Apply` so a zero-match render returns its input unchanged, no error — exactly like
  `rewriteWebhookURL`/`filterKinds`. Update the doc comment. The controller and the shared
  `Transformation.Apply` signature are untouched.
- Reason: Simplest change (a ~3-line deletion); consistent no-op-on-zero-match semantics across
  all three transformation types; solves the r7 label-patch case; the reconcile is never blocked
  by a zero-match patch.
- Alternatives considered: (1) per-render no-op + a cross-render backstop/warning
  (`TransformNoMatch`, either fatal `Ready=False` or a non-fatal Warning event, needing a
  `matchCount` helper + controller type-assertion) — rejected: adds a `patch`-only code path
  that breaks symmetry with the other transforms for a low-value typo signal, and the fatal
  variant stops the apply, which is not wanted. (2) Form B / Option C (CRD `scope:` field) —
  rejected for this change (see below). (3) Extending the shared `Apply` signature to return a
  match count — rejected: no count needs to escape `patch`, so nothing to plumb.

**Decision: no typo backstop or warning retained**
- Chosen: A selector that matches nothing anywhere does nothing, silently.
- Reason: Under the two-render model, zero-match in a given render is now *expected* (the whole
  point of this change), so the original single-render typo guard is low-value. A real typo is
  observable downstream (the intended object lacks the change — e.g. an unlabeled webhook object
  is never adopted by the injector) and is caught by unit/equivalence tests at authoring time.
  A `patch`-only warning path would break the symmetry with `rewriteWebhookURL`/`filterKinds`.
- Alternatives considered: fatal `TransformNoMatch` status (stops reconcile) and non-fatal
  `TransformNoMatch` Warning event — both rejected in favor of simplicity/consistency.

**Decision: equivalence fixture in scope**
- Chosen: Extend/add a fixture under `testdata/fixtures/metal-operator/` proving the injector
  `--target-label` lands on the shoot render's VWC only and the seed render is a byte-identical
  no-op.
- Reason: Proves the motivating `metal-operator-remote-v2` case end-to-end through
  `source.From → Render → transform.Build → Apply`, not just at unit level. Runs under the
  existing `RUN_EQUIVALENCE=1` gate (needs the internal keppel OCI registry), consistent with
  the other four Helm-operator equivalence subtests.

**Decision: Option C (`scope: seed|shoot|both`) deferred as future work — non-breaking, not foreclosed**
- Chosen: Do NOT add a `scope` field now. Document it as a future upgrade to revisit at the
  v1beta1 schema step, if a real need for explicit per-patch render routing emerges (e.g.
  hard-scoping a patch whose selector would *accidentally* match in the wrong render).
- Reason: Option C is **A-plus-a-routing-field**, not a replacement. C costs ~3–5× A's diff (new
  `PatchSpec.Scope` enum + `+kubebuilder:default=both`, regenerated deepcopy/CRD, controller
  routing branch, per-scope tests, CEL/admission test, doc/sample updates) plus a permanent
  CRD-schema field carried through v1alpha1 → v1beta1 → v1, and it pushes per-patch
  render-topology into the CR against the established "operator makes no routing decisions; the
  chart/overlay decides what each render emits" principle (CONTEXT.md Revisions 3–4).
- Non-breaking upgrade path: because `scope` would default to `both`, A's silent no-op
  semantics **are** C's `both` branch verbatim. C can be added later with zero migration and
  zero behavior change for existing CRs. No current consumer needs what C adds over A's implicit
  no-op — across the 5-operator transformation profile the only single-render patch case is the
  injector label on webhook objects, which A handles transparently.

## Risks / Trade-offs

- [A zero-match patch anywhere is a silent no-op, so a typo'd selector does nothing without an
  explicit signal] → Accepted deliberately (consistency with the other two transforms). A real
  typo is observable downstream (the intended object lacks the change) and is caught by
  unit/equivalence tests; test S3 asserts the silent-no-op behavior explicitly so it is a
  conscious contract, not an accident.
- [A no-op render might not pass the unchanged manifests through, breaking a later transform's
  input] → `patch.Apply` returns the input slice unchanged on zero-match; test S4 asserts a
  second transform still sees the unchanged manifests.
- [Equivalence fixture needs the internal keppel OCI registry] → Gated behind `RUN_EQUIVALENCE=1`
  like the other four Helm-operator subtests; unit tests S1–S4 fully cover behavior offline.

## Migration Plan

Code-only behavior change; no CRD/schema migration, no data migration.

Deployment steps:
1. Land Form A in `internal/transform/patch.go` (~3-line deletion of the fail-loud guard +
   `matched` counter, plus the doc-comment update) + tests + equivalence fixture. The
   controller is unchanged.
2. `make manifests generate` is **not** required (no marker/type change), but run
   `go build ./...` (exit 0) and `go test ./internal/transform/...` (green) as the gate.
3. Flip `docs/patch-render-scoping.md` Status Proposed → Accepted/Implemented with the chosen
   shape noted; update CONTEXT.md Revision 10 if the implemented shape differs.
4. **Sequencing constraint**: this operator change must land before `metal-operator-remote-v2`
   is cut over live. A cluster running the un-enhanced operator must NOT enable that CR (the
   label patch would fail on the seed render). Independent of the already-shipped Phase 7.6
   subchart-dependency-resolution work.

Rollback:
- Revert the single-file change. Behavior returns to fail-loud-on-zero-match. Any CR relying on
  a single-render patch (i.e. `metal-operator-remote-v2`) must be disabled before rollback, per
  the sequencing constraint above.

## Open Questions

- [ ] _None._ All design forks (zero-match = silent no-op, no backstop/warning, shared signature
  untouched, equivalence fixture, Option C deferral) were resolved during brainstorming.
