## Design Summary

Remove the r5/r6 cross-stream transformation `packageWebhookConfigsForInjector` from the `dual-deployment-operator` CRD, realizing design revision 7 (`docs/design.md` §2.2.4), and — in the same change — extend `rewriteWebhookURL` to also rewrite conversion-webhook CRDs, closing the CRD Service→URL rewrite gap (§3.4.2, §9.2). The webhook-injector's new target patch mode ([webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14)) lets the operator apply WebhookConfigurations directly to the shoot with `caBundle` left unset while the injector keeps `caBundle` in sync in place, so packaging WebhookConfigurations into a host-side ConfigMap is obsolete. This change deletes the `PackageWebhookConfigsForInjectorSpec` type, its `Transformation` union field, its DeepCopy, its CEL union term, and its test references, then regenerates manifests/deepcopy — collapsing the transformation menu from 4 types across 2 scopes to 3 types in a single per-render scope. It also widens `rewriteWebhookURL`'s target set (no new field) to include `.spec.conversion.webhook.clientConfig` on CRDs. The load-bearing constraint: the removed type was scaffolded but never used (Phase 3 transformations were never built) and no `DualDeploymentOperator` CR sets it, so removal is a breaking CRD schema change with no data migration.

## Alternatives Considered

### Option A: Dedicated removal change (chosen)

- **Approach**: A standalone change carrying only the `crd-types` + `cel-admission-validation` delta specs and the corresponding Go cleanup (`_types.go`, `zz_generated.deepcopy.go`, the CEL rule, two test files, `make manifests generate`).
- **Pros**: Self-contained, already-shippable unit (the field is dead — no CR uses it); clean git history; unblocks the ongoing `transformations` change to start from correct specs; small, low-risk, easy to review.
- **Cons**: One extra change in the pipeline ahead of `transformations`.
- **Why not chosen**: This IS the chosen approach.

### Option B: Fold the removal into the `transformations` change

- **Approach**: Have the (not-yet-designed) `transformations` change carry both the removal deltas and the net-new 3-type transform-layer implementation.
- **Pros**: One change instead of two; removal and 3-type build land together.
- **Cons**: Mixes a subtractive CRD/CEL schema cleanup with a net-new feature build in one change, muddying both the history and the review; the `transformations` brainstorm would have to re-litigate the r7 scope before it can even define the interface.
- **Why not chosen**: The removal is a clean, self-contained, already-shippable unit; separating it lets `transformations` begin from clean specs rather than reasoning about a field it must simultaneously delete.

### Option C: Leave the type, filter at implementation time

- **Approach**: Keep `PackageWebhookConfigsForInjectorSpec` in the CRD and simply never implement the transformation.
- **Pros**: No CRD change; no regeneration.
- **Cons**: Leaves a validated-but-unbuildable field in the published CRD schema, directly contradicting the specs and design r7; a footgun for CR authors who could set a field that silently does nothing; the CEL rule still advertises a 4th variant.
- **Why not chosen**: The specs are the source of truth and currently mandate the removed transformation; leaving the type would keep the specs and the design permanently inconsistent.

## Agreed Approach

**Option A.** A dedicated change carrying the two delta specs (`crd-types` MODIFY+REMOVE, `cel-admission-validation` MODIFY) plus the Go cleanup. This is preferred because the removal is a self-contained unit acting on a dead, unused type — no CR in the fleet sets `spec.transformations[].packageWebhookConfigsForInjector`, and no reconciler consumes it — so it is safe to ship independently and it unblocks the downstream `transformations` change to build the 3-type per-render layer against already-correct specs. `rewriteWebhookURL` is deliberately retained (the injector's target patch mode does not rewrite `clientConfig` Service→URL, so that responsibility stays operator-side).

## Key Decisions

- **Scope is removal + count-fix + a rewriteWebhookURL widening**: MODIFY the `Transformation` union requirement (4→3 types, drop the cross-stream scope), REMOVE the `PackageWebhookConfigsForInjectorSpec` requirement, MODIFY the transformation-union CEL requirement (drop the 4th term and flip its accepted-alone scenario to rejected), and MODIFY the `RewriteWebhookURLSpec fields` requirement to state the transformation also rewrites conversion-webhook CRDs. Rationale: these are the only live requirements that reference the removed type, plus the one that documents `rewriteWebhookURL`'s target set.

- **`rewriteWebhookURL` extended to CRDs (in scope here, no new field)**: the transformation now also rewrites `.spec.conversion.webhook.clientConfig.service → .url` on CRDs with `.spec.conversion.strategy == "Webhook"`, using the same `urlPrefix + service.path` construction and idempotency rule. Rationale: a CRD conversion webhook references a seed-local Service the shoot cannot route to — the same problem `rewriteWebhookURL` already solves for WebhookConfigurations — and the injector deliberately does not rewrite Service→URL. This is a semantic widening of an existing, still-live type (not the removed cross-stream type), so it belongs in the same CRD-focused change; it closes the §9.2 Service→URL half. No `targetKinds`/narrowing field is added — targeting stays unconditional across the three kinds.

- **Net-new r7 *delivery*-layer behavior stays out of scope here**: the operator applying WebhookConfigurations/CRDs to the shoot with `caBundle` unset and the injector's `--target-crd-label` stamped, plus the disjoint-field SSA coexistence, is delivery/reconcile behavior (Phase 3/5) — it belongs in the `transformations`/delivery change, not this CRD/CEL delta. Rationale: keep this change focused on CRD schema + the `rewriteWebhookURL` semantic; the label is applied via the existing `patch` type, already specced.

- **Breaking CRD change, no data migration**: `PackageWebhookConfigsForInjectorSpec` was scaffolded by the archived scaffold change but never wired into any reconciler or transformation, and no CR sets the field. Rationale: removal cannot break existing objects; after removal, `make manifests generate` regenerates the CRD YAML, RBAC, and deepcopy.

- **Regeneration is authoritative, not hand-editing**: after editing `api/v1alpha1/dualdeploymentoperator_types.go`, the generated artifacts (`config/crd/bases/*`, `config/rbac/role.yaml`, `zz_generated.deepcopy.go`) MUST be produced by `make manifests generate`, never hand-edited. Rationale: matches AGENTS.md "never edit auto-generated files".

- **`rewriteWebhookURL` stays typed, drops `TargetKinds`, and is widened to CRDs**: the unused `RewriteWebhookURLSpec.TargetKinds` field is removed (v1 targets its kinds unconditionally — no per-kind narrowing), and the transformation is extended to also rewrite conversion-webhook CRDs (see the dedicated decision above). The `TargetKinds` removal reconciles the tracked live spec to the code, which already dropped the field; a stray uncommitted hand-edit to the live spec that did the same was reverted so this change owns the removal through the proper delta/archive flow. Rationale: same Service→URL problem on a second clientConfig path; no new field; targeting stays unconditional.

## Open Questions

- [ ] PR #14 is not yet merged. This change removes the cross-stream type on the assumption that the injector's target patch mode ships. The removal itself is safe regardless (the type is dead), but if PR #14 is abandoned, the fallback would be reverting to r6 (re-introducing `packageWebhookConfigsForInjector`). — owner: confirm PR #14 merge before relying on r7 delivery in the downstream `transformations` change (does not block this removal).
- [x] ~~A worktree edit removing `RewriteWebhookURLSpec.TargetKinds` from the live `crd-types` spec appeared during this session and is not part of this change.~~ **Resolved**: the stray live-spec hand-edit was reverted (live spec restored to HEAD); the `TargetKinds` removal is now owned by this change's `crd-types` delta and will land via the normal archive flow. The Go type/deepcopy/CRD YAML already omit `TargetKinds`, so apply reconciles the spec to the code with no code change for this field.
