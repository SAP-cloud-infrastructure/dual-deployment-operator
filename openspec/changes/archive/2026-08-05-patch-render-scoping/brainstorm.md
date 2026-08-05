<!-- SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

## Design Summary

`internal/transform` `patch.Apply` is fail-loud on zero matches, and the controller
applies every transform to **both** renders (seed + shoot) independently. A `patch`
whose target kind exists in only one render — e.g. a shoot-only
`ValidatingWebhookConfiguration` — matches in that render but errors on the other,
failing the whole reconcile with `SeedTransformFailed`. This blocks the design's own
r7 pattern (design §3.4.4): stamping the webhook-injector `--target-label` onto webhook
objects via a CR `patch`. The motivating consumer is `sapcc/helm-charts`
`system/metal-operator-remote-v2`, whose VWC comes from the upstream `metal-operator`
subchart (no values hook for a custom label, and a Helm wrapper can't edit a subchart's
rendered output) — so the label must come from a CR `patch`, which today is impossible.

The fix makes a zero-match `patch` a clean, silent **no-op** inside `patch.Apply` — exactly
the behavior `rewriteWebhookURL` and `filterKinds` already have when they match nothing.
The fail-loud guard is simply removed; there is no controller change, no cross-render
backstop, and no `TransformNoMatch` signal. A zero-match render passes its manifests through
unchanged. No CRD change, no new transformation type, no `api/` change — a ~3-line deletion
in `internal/transform/patch.go`, plus tests and one equivalence fixture.

## Alternatives Considered

### Option A: Zero-match is a silent no-op (no CRD change) — CHOSEN
- **Approach**: Remove the `matched == 0 → error` guard from `patch.Apply` so a zero-match
  render returns its input unchanged, no error — identical to how `rewriteWebhookURL` and
  `filterKinds` already behave on zero match. No controller change, no cross-render backstop,
  no match-count plumbing, no new status/event reason. The shared `Transformation.Apply`
  signature is untouched.
- **Pros**: Smallest possible diff (a ~3-line deletion in `patch.go`); `rewriteWebhookURL`,
  `filterKinds`, the controller, and all their existing tests are untouched. **Consistent**
  no-op-on-zero-match semantics across all three transformation types. No CR-authoring surface.
- **Cons**: A genuinely typo'd selector (matches nothing anywhere) silently does nothing rather
  than surfacing an error/warning. Mitigated in practice: the intended object simply won't carry
  the change, which is observable downstream (e.g. an unlabeled webhook object is never adopted
  by the injector) and is caught by unit/equivalence tests at authoring time.
- **Why not chosen**: It IS chosen (see Agreed Approach).

### Option A′: Per-render no-op + cross-render backstop/warning (rejected)
- **Approach**: Same per-render no-op, but the controller detects a `patch` that matched
  nothing across **both** renders and surfaces it — either as a fatal `TransformNoMatch` status
  (`Ready=False`, stops the reconcile) or as a non-fatal `TransformNoMatch` Warning event.
  Requires a `matchCount(manifests)` helper on `*patch`, a controller type-assertion, pre-Apply
  slice capture, and (for the warning) `r.Recorder` plumbing + an event reason.
- **Pros**: Preserves the original typo-protection signal.
- **Cons**: Adds a bespoke code path for `patch` alone, making it **inconsistent** with the
  other two transforms (which no-op silently). More surface: helper + type-assertion + tests +
  (warning variant) recorder plumbing. The fatal variant also *stops the apply*, which the user
  explicitly does not want.
- **Why not chosen**: The typo-protection is low-value under the two-render model (zero-match in
  one render is now expected/normal), and the extra machinery breaks the symmetry with
  `rewriteWebhookURL`/`filterKinds`. Simplicity and consistency win.

### Option C: CRD `scope: seed|shoot|both` field on the patch (Form B)
- **Approach**: Add an optional `scope` enum to `PatchSpec`; the controller applies a
  patch only to the render(s) named by `scope`, so a shoot-only patch is never applied to
  the seed render and never errors there.
- **Pros**: Explicit intent in the CR; a reader sees which render a patch targets.
- **Cons**: CRD change + CR-authoring surface + CEL validation + migration of the CRD
  schema. Pushes render-topology knowledge (which kind lives in which render) into every
  CR, duplicating what the chart/overlay already decides. Heavier than the problem warrants.
- **Why not chosen**: The handoff (§3) explicitly rejects Form B unless the reviewer/user
  asks for it. Zero-match-is-a-no-op solves the same problem with no schema surface.
  Complexity was evaluated explicitly during brainstorming: Option C is **A-plus-a-routing-field**,
  not a replacement — even with `scope: shoot`, a typo'd selector inside the scoped render
  still matches nothing and must still error, so A's match-count backstop is still required
  underneath C. Cost is ~3–5× A's diff (new `PatchSpec.Scope` enum field + `default=both`,
  regenerated deepcopy/CRD, controller routing branch, per-scope tests, CEL/admission test,
  doc/sample updates) plus a permanent CRD-schema field carried through v1alpha1→v1beta1→v1.
  It also cuts against the established "operator makes no routing decisions; the chart/overlay
  decides what each render emits" principle (CONTEXT.md Revisions 3–4) by pushing per-patch
  render-topology into the CR. Crucially, **A is a strict, non-breaking subset of C**: `scope`
  would default to `both`, so A's implicit no-op semantics ARE C's `both` branch verbatim.
  A can therefore be upgraded into C later with zero migration and zero behavior change for
  existing CRs — so C is deferred, not foreclosed. No current consumer needs to *force* a
  patch onto a render where its target is absent (the only capability C adds over A's implicit
  no-op); across the 5-operator transformation profile the only single-render patch case is the
  injector-label on webhook objects, which A handles transparently.

## Agreed Approach

**Option A** — a zero-match `patch` is a clean, silent no-op inside `patch.Apply`, consistent
with `rewriteWebhookURL` and `filterKinds`.

One changed unit:

1. **`patch.Apply`** ([`internal/transform/patch.go`](../../../internal/transform/patch.go)):
   remove the `if matched == 0 { return nil, error }` block (lines 53–55) and the now-unused
   `matched` counter (lines 40, 46). A render with zero matches returns its input manifests
   **unchanged, no error**. Update the doc comment (line 31) that currently says "Fails loud on
   zero matches". The shared `Transformation` interface signature is untouched.

The controller transform loop needs **no change**: per-render `Apply` no longer returns an
error on zero-match, so the existing
`if err != nil { return r.errStatus(...) }` branches simply don't fire for that case. No
`matchCount` helper, no type-assertion, no `TransformNoMatch` reason, no `r.Recorder` plumbing.

Declaration order is preserved: a no-op render returns its input slice unchanged, so later
transforms still see the full manifest set. The fix lives in the shared transform layer, so
both the Helm and kustomize source paths benefit without source-specific code.

Chosen over A′ (per-render no-op + cross-render backstop/warning) because A′ adds a bespoke
code path for `patch` alone — breaking symmetry with the other two transforms — for a
low-value typo signal; chosen over C (CRD `scope:` field) because A needs no schema surface
and the handoff rejects C by default.

## Key Decisions

- **Zero-match is a silent no-op, no CRD change**: delete the fail-loud guard so `patch`
  matches the existing no-op-on-zero-match behavior of `rewriteWebhookURL` and `filterKinds`.
  No controller change, no cross-render backstop, no `TransformNoMatch` signal. — *Rationale:
  simplest change; consistent semantics across all three transforms; solves the r7 label-patch
  case with the smallest surface.*
- **No typo backstop / warning retained**: a selector that matches nothing anywhere does
  nothing, silently. — *Rationale: under the two-render model, zero-match in a given render is
  now expected (the whole point of this change), so the original single-render typo guard is
  low-value; a real typo is observable downstream (the intended object lacks the change) and is
  caught by unit/equivalence tests. Adding a `patch`-only warning path would break symmetry with
  the other transforms.*
- **Shared `Transformation.Apply` signature untouched**: `([]manifest.Manifest, error)` stays
  as-is. — *Rationale: no match count needs to escape `patch`, so there is nothing to plumb.*
- **Option C (`scope: seed|shoot|both`) deferred as a non-breaking future upgrade, not
  foreclosed**: if explicit per-patch routing is ever needed, add `PatchSpec.Scope` with
  `+kubebuilder:default=both` — every existing CR keeps its exact behavior because the implicit
  no-op IS the `both` branch. Revisit at the v1beta1 schema step. — *Rationale: C is
  A-plus-routing, costs ~3–5× the diff plus a permanent CRD field, and pushes render-topology
  into the CR against the "operator makes no routing decisions" principle; no current consumer
  needs what C adds. Because the no-op semantics are a strict subset of C, deferring costs
  nothing later.*
- **Equivalence fixture in scope**: extend/add a fixture under
  `testdata/fixtures/metal-operator/` proving the injector `--target-label` lands on the
  shoot render's VWC only and the seed render is a byte-identical no-op. — *Rationale: proves
  the motivating `metal-operator-remote-v2` case end-to-end through
  `source.From → Render → transform.Build → Apply`, not just at unit level. Runs under the
  existing `RUN_EQUIVALENCE=1` gate (needs the internal keppel OCI registry), consistent with
  the other four Helm-operator equivalence subtests.*

### Test contract (TDD RED→GREEN, mandatory per repo AGENTS.md)

Table-driven cases in `internal/transform/patch_test.go`:

- **S1 (regression, happy path)**: patch matching an object in a render still patches it —
  GREEN unchanged.
- **S2 (the fix)**: patch whose selector matches nothing in a render → `Apply` returns the
  input manifests **byte-identical, no error** (silent no-op). This is the seed-render behavior
  for a shoot-only VWC label patch.
- **S3 (consistency / no backstop)**: patch whose selector matches **nothing** → still a no-op
  with no error; assert NO error is returned and NO status/event side effect exists (there is no
  `TransformNoMatch`). Confirms `patch` now behaves like `rewriteWebhookURL`/`filterKinds`.
- **S4 (ordering)**: two transforms in sequence where the first no-ops on a render; assert the
  second transform still sees the unchanged manifests.

All four exercise `patch.Apply` directly at the unit level — no controller-level test is needed
because the controller is unchanged. The equivalence fixture (above) is the real-surface
artifact for the VWC-label case: it proves the shoot render's VWC carries the injector label
and the seed render is a byte-identical no-op through the full pipeline.
Verification command: `go test ./internal/transform/...`; equivalence subtest under
`RUN_EQUIVALENCE=1`.

## Open Questions

<!-- None outstanding. Forks resolved during brainstorming:
  zero-match = silent no-op (Option A, no backstop/warning), shared signature untouched,
  Option C deferred as non-breaking future upgrade, equivalence fixture in scope. -->

- _None._ All design forks were resolved during brainstorming.
