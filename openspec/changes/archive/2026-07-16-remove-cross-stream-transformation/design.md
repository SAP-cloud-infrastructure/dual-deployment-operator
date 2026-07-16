## Context

The `dual-deployment-operator` CRD (`api/v1alpha1`, archived scaffold + source-renderers changes) defines `spec.transformations[]` as a discriminated union. Under design revisions r5/r6 that union had **four** types across **two** scopes: three per-render (`patch`, `rewriteWebhookURL`, `filterKinds`) and one cross-stream (`packageWebhookConfigsForInjector`). The cross-stream type existed only because the webhook-injector could deliver WebhookConfigurations solely by reading a source ConfigMap and could not be scoped to shoot objects by label — so the operator packaged WebhookConfigurations into a host-side ConfigMap for the injector to consume.

Design **revision 7** (`docs/design.md` §2.2.4) removes that constraint: [webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14) adds a label-scoped **target patch mode** that keeps `caBundle` in sync on CRD/Validating/Mutating WebhookConfiguration objects applied **directly to the shoot** by the operator. The operator can therefore apply WebhookConfigurations itself (with `caBundle` unset) and let the injector top up `caBundle` in place, making the cross-stream packaging obsolete. r7 also closes a second, related gap: `rewriteWebhookURL` only ever rewrote `.webhooks[].clientConfig` on WebhookConfigurations, never a CRD conversion webhook's `.spec.conversion.webhook.clientConfig`, leaving conversion-webhook CRDs pointing at a seed-local Service the shoot cannot route to.

Current state of the code relevant to this change:
- `PackageWebhookConfigsForInjectorSpec` still exists in `api/v1alpha1/dualdeploymentoperator_types.go`, `zz_generated.deepcopy.go`, the `Transformation` `XValidation` CEL rule, and two test files (`dualdeploymentoperator_types_test.go`, `internal/controller/cel_validation_test.go`). It was scaffolded but never consumed — `internal/transform` does not exist yet (Phase 3 was never implemented).
- `RewriteWebhookURLSpec` in code already has only `URLPrefix` (no `TargetKinds`); the tracked live spec still described a `TargetKinds` field, so spec and code disagreed.

Constraints: this is an SDD-lane change; live specs change only via delta archive (never hand-edited). Generated artifacts (`config/crd/bases/*`, `config/rbac/role.yaml`, `zz_generated.*`) are produced by `make manifests generate`, never hand-edited (per `AGENTS.md`). No `DualDeploymentOperator` CR in the fleet sets `packageWebhookConfigsForInjector`, so removal is backward-safe.

Stakeholders: the downstream `transformations` change (Phase 3) builds the transform layer against the specs this change corrects; the metal-operator migration (the only candidate with a conversion-webhook CRD) depends on the CRD Service→URL rewrite and caBundle stamping working.

## Goals / Non-Goals

**Goals:**
- Remove `PackageWebhookConfigsForInjectorSpec` and its `Transformation` union field from the CRD types, collapsing the transformation menu to 3 per-render types in a single scope.
- Update the transformation-union CEL rule from a 4-term count to a 3-term count.
- Reconcile the `RewriteWebhookURLSpec` spec to the code by removing the already-absent `TargetKinds` field from the spec.
- Specify that `rewriteWebhookURL` rewrites conversion-webhook CRD `clientConfig` (`.spec.conversion.webhook.clientConfig`) in addition to Validating/Mutating WebhookConfigurations — closing the CRD Service→URL rewrite gap (§9.2).
- Keep the tracked specs, generated CRD/RBAC/deepcopy, and Go types mutually consistent after `make manifests generate`.

**Non-Goals:**
- Implementing the transform layer (`internal/transform`) or any transformation logic — that is the downstream `transformations` change (Phase 3). This change only reconciles CRD schema + CEL + the `rewriteWebhookURL` requirement text.
- Delivery-layer behavior: the operator applying WebhookConfigs/CRDs to the shoot with `caBundle` unset and the injector `--target-crd-label` stamped, and the disjoint-field SSA coexistence — Phase 3/5.
- Any change to `patch`, `filterKinds`, `Selector`, `DeletionPolicy`, status types, source discriminator, or `remoteNamespace`.
- Re-introducing per-kind narrowing for `rewriteWebhookURL` (no `targetKinds` returns).

## Capabilities

**Modified Capabilities:**
- `crd-types` — remove the `PackageWebhookConfigsForInjectorSpec` requirement; modify the `Transformation` union requirement (4→3 types, single scope); modify the `RewriteWebhookURLSpec fields` requirement (drop `TargetKinds`, add CRD conversion-webhook targeting).
- `cel-admission-validation` — modify the `Transformation union enforced by CEL` requirement (3-term count; the removed variant's accepted-alone scenario flips to rejected).

No new capabilities. (Delta specs for both already exist under this change and pass strict validation.)

## Decisions

**Decision: Dedicated removal change, not folded into `transformations`**
- Chosen: a standalone change carrying only the `crd-types` + `cel-admission-validation` deltas and the corresponding Go/generated cleanup.
- Reason: the removal acts on a dead, unused type and is independently shippable; it unblocks the `transformations` change to build the 3-type layer against already-correct specs. Keeps a subtractive schema change out of a net-new feature build.
- Alternatives considered: fold into `transformations` (rejected — mixes subtraction with feature work, and the transformations brainstorm would have to re-litigate r7 scope); leave the type and never implement it (rejected — leaves a validated-but-unbuildable field in the published CRD, contradicting the specs).

**Decision: `rewriteWebhookURL` widened to CRDs in this change (no new field)**
- Chosen: the `rewriteWebhookURL` transformation targets three kinds — Validating/Mutating WebhookConfigurations (`.webhooks[].clientConfig`) and conversion-webhook CRDs (`.spec.conversion.webhook.clientConfig`, only when `.spec.conversion.strategy == "Webhook"`). Same `urlPrefix + service.path` construction, same idempotency (`.url` left alone), same `caBundle` preservation. No `targetKinds`/narrowing field.
- Reason: a CRD conversion webhook references a seed-local Service the shoot cannot route to — the identical problem `rewriteWebhookURL` already solves for WebhookConfigurations — and the injector deliberately does not rewrite Service→URL. This is a semantic widening of an existing, still-live type, so its spec update belongs with the other `RewriteWebhookURLSpec` change (the `TargetKinds` removal) in this CRD-focused change.
- Alternatives considered: a separate `patch` per CRD in each CR (rejected — verbose, per-CR, and not idempotent for the URL construction); a dedicated new transformation type (rejected — same operation, different path; no reason to split); defer to a later change (rejected — the requirement text belongs with the `RewriteWebhookURLSpec` requirement being modified here anyway).

**Decision: `TargetKinds` removal reconciles spec to code, owned by this change**
- Chosen: remove `TargetKinds` from the `RewriteWebhookURLSpec fields` spec requirement; the Go type/deepcopy/CRD YAML already omit it, so apply is spec-only for this field. A stray uncommitted hand-edit to the live spec that did the same was reverted so this change owns the removal via the delta/archive flow.
- Reason: keep the live spec authoritative and changed only through the SDD workflow; avoid two edits (a hand-edit + this delta) touching the same requirement.
- Alternatives considered: commit the live-spec hand-edit separately (rejected — edits a live spec outside the change workflow, and this change's delta would re-modify the same requirement on archive, briefly double-touching it).

**Decision: regeneration is authoritative**
- Chosen: after editing `api/v1alpha1/dualdeploymentoperator_types.go`, produce `config/crd/bases/*`, `config/rbac/role.yaml`, and `zz_generated.deepcopy.go` via `make manifests generate`.
- Reason: matches `AGENTS.md` "never edit auto-generated files"; guarantees the CRD YAML and deepcopy match the trimmed types.
- Alternatives considered: hand-editing generated files (rejected — drift, violates repo convention).

## Risks / Trade-offs

- [Breaking CRD schema change removes a union field] → No CR sets `packageWebhookConfigsForInjector` and the type is unimplemented, so no live object breaks. The CEL count drops from 4 to 3; structural pruning rejects the now-unknown field. No data migration.
- [`rewriteWebhookURL` CRD extension is new *implementation* work, unlike the pure removals] → This change only specifies the requirement; the actual Go logic lands in the Phase-3 `transformations` change, which must include a conversion-webhook CRD test fixture (service→url rewritten, caBundle preserved) and a non-webhook CRD fixture (untouched). Documented in `docs/implementation.md`.
- [Spec depends on webhook-injector#14 merging for the end-to-end r7 story] → The CRD/CEL removals are safe regardless (dead type). Only the delivery-layer behavior (Phase 5) truly depends on PR #14; this change does not. If PR #14 is abandoned, the fallback is reverting to r6 — but that is a separate decision that does not affect the correctness of removing an unused type.
- [Two MODIFY targets in the `crd-types` delta must match live headers verbatim] → Verified: `Transformation discriminated union with 4 types across 2 scopes` and `RewriteWebhookURLSpec fields` both match the live spec headers exactly; strict validation passes.

## Migration Plan

Deployment steps (executed during `/opsx-apply`):
1. Edit `api/v1alpha1/dualdeploymentoperator_types.go`: remove the `PackageWebhookConfigsForInjector` field from `Transformation`; remove the `PackageWebhookConfigsForInjectorSpec` struct; drop the `packageWebhookConfigsForInjector` term from the `Transformation` `XValidation` CEL rule (leaving the 3-way `patch`/`rewriteWebhookURL`/`filterKinds` count).
2. Remove `PackageWebhookConfigsForInjector` references from `api/v1alpha1/dualdeploymentoperator_types_test.go` and `internal/controller/cel_validation_test.go`.
3. Run `make manifests generate` to regenerate `config/crd/bases/*`, `config/rbac/role.yaml`, and `zz_generated.deepcopy.go` (drops the `PackageWebhookConfigsForInjectorSpec` DeepCopy methods and the union field).
4. Run `make build test lint-fix` — all green.
5. Archive the change so the `crd-types` and `cel-admission-validation` deltas fold into the live specs (this is also where the `TargetKinds` spec/code reconciliation lands).

Note: the `rewriteWebhookURL` conversion-webhook CRD *behavior* is specified here but implemented in the downstream `transformations` change; this migration touches only CRD types, CEL, generated artifacts, and tests.

Rollback:
- Revert the change branch. The scaffold and source-renderers changes are unaffected (nothing imports the removed type). Re-running `make manifests generate` on the reverted types restores the prior CRD YAML/deepcopy.

## Open Questions

- [x] ~~Should the `TargetKinds` removal be a separate change?~~ **Resolved**: folded here; the stray live-spec hand-edit was reverted so this change owns it via delta.
- [ ] Confirm webhook-injector#14 merges before the downstream `transformations`/delivery change relies on target patch mode for r7 delivery. Does NOT block this change (the removals are safe on a dead type). — owner: implementer, before Phase 5.
