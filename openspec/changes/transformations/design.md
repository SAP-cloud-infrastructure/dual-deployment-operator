## Context

Phase 3 of the `dual-deployment-operator` build-out. Phase 2 (`internal/source`) already renders a source **twice per reconcile** — once for host, once for remote — producing two independent `[]manifest.Manifest` streams, each `Manifest` carrying an `Origin` (`upstream` | `additions`) tag ([`internal/manifest/manifest.go`](../../../internal/manifest/manifest.go)). This change adds `internal/transform`, the layer that turns a `DualDeploymentOperator` CR's `spec.transformations` into an ordered list of operations applied to each of those two streams.

Under design **revision 7** (`docs/design.md` §3.4, `docs/CONTEXT.md`), the transformation menu is **three per-render types in a single scope** — `patch` (strategic-merge XOR JSON Patch DSL), `rewriteWebhookURL` (typed), `filterKinds` (typed). r7 (enabled by [webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14)) removed the r5/r6 cross-stream scope and its sole type `packageWebhookConfigsForInjector`: the operator now applies WebhookConfigurations (and conversion-webhook CRDs) directly to the shoot and the injector's target patch mode keeps their `.caBundle` current in place, so packaging them into a host-side ConfigMap is obsolete. Injector integration is therefore no longer a transformation type — it is a plain `patch` that stamps the injector's `--target-label` onto the webhook objects (design §3.4.4).

The CRD type set was already committed to r7 shape: the `Transformation` union in [`api/v1alpha1/dualdeploymentoperator_types.go`](../../../api/v1alpha1/dualdeploymentoperator_types.go) has exactly `patch`/`rewriteWebhookURL`/`filterKinds`, and the archived change `2026-07-16-remove-cross-stream-transformation` already deleted `PackageWebhookConfigsForInjectorSpec` from the types, deepcopy, CEL rule, tests, and regenerated manifests. So the "CRD cleanup" the brainstorm anticipated is **already done** — this change realizes the type set in code rather than reshaping it.

Constraints:
- **Load-bearing downstream**: the output feeds Phase 5 delivery (dual-cluster SSA, caBundle-unset for webhook objects) and Phase 6 reconciler wiring. Semantics fixed here — missing-match policy, no in-place mutation, declaration ordering — are consumed there.
- **Generated artifacts** (`config/crd/bases/*`, `config/rbac/role.yaml`, `zz_generated.*`) are produced by `make manifests generate`, never hand-edited (per `AGENTS.md`).
- **TDD is mandatory** (RED→GREEN→REFACTOR per `AGENTS.md`); tests are table-driven, offline (no cluster), with fixtures under `internal/transform/testdata/`.
- **SDD lane**: live specs change only via delta archive.

Stakeholders: the operator reconciler (consumer of `transform.Build`), the five candidate operators whose CRs declare transformations (`metal-operator`, `ipam-capi` use all three; `boot-operator`, `argora-operator`, `khalkeon` use only `filterKinds`).

## Goals / Non-Goals

**Goals:**
- Implement `internal/transform` with **one** `Transformation` interface (`Type() string`, `Apply([]manifest.Manifest) ([]manifest.Manifest, error)`), a `Build(specs)` parse step that builds an ordered `[]Transformation` from the CR entries preserving declaration order, and a shared `Match(m, Selector)` selector.
- Implement the three transformations to design §3.4 semantics:
  - `patch` — strategic-merge XOR JSON Patch against a target selector; fail-loud on zero matches; re-validates the strategicMerge-XOR-jsonPatch invariant at runtime.
  - `rewriteWebhookURL` — Service→URL rewrite across `ValidatingWebhookConfiguration`, `MutatingWebhookConfiguration`, and conversion-webhook `CustomResourceDefinition`; preserve `.caBundle`; idempotent; no-op on zero matches.
  - `filterKinds` — drop listed kinds from the stream, optional `source` origin restriction; no-op on zero matches.
- Guarantee **no in-place mutation**: each transformation returns fresh `[]manifest.Manifest`, never mutating the caller's slice or the underlying `Unstructured`.
- Table-driven offline tests per transformation plus `Build`/parse and `Match` unit tests, including a conversion-webhook CRD fixture and an injector-label `patch` fixture.

**Non-Goals:**
- Reconciler wiring (Phase 6) and dual-cluster delivery / SSA + disjoint-field caBundle coexistence (Phase 5).
- Status population.
- The v2 validating admission webhook that parses `patch` content against the target kind's OpenAPI schema.
- Any cross-stream / cross-render operation, `CrossStreamTransformation` interface, `Group()` scope-splitter, or `manifest.SerializeMultiDoc` helper — all r5/r6 artifacts removed in r7.
- CRD type changes: the r7 type set is already committed; this change consumes it, it does not reshape it.

## Decisions

**Decision: One `Transformation` interface + `Build()` parse + shared `Match()` selector**
- Chosen: A single interface `Transformation { Type() string; Apply([]manifest.Manifest) ([]manifest.Manifest, error) }`. `Build(specs []v1alpha1.Transformation) ([]Transformation, error)` walks the discriminated-union entries and returns an ordered list, preserving declaration order; an entry with no field set → error. A shared `Match(m manifest.Manifest, sel v1alpha1.Selector)` in `selector.go` (kind exact / name glob / origin) serves `patch` and optionally `filterKinds` `source`.
- Reason: Matches design §3.4/§5.2 and impl.md Phase 3 exactly under r7. The reconciler (§5.3) applies one ordered list to each render independently — no scope branching. Smallest surface, no cross-render state, small independently-testable units.
- Alternatives considered:
  - **Keep the two-scope shape** (`PerRenderTransformation` + `CrossStreamTransformation`, `Group`-by-scope) for forward-compatibility — rejected (YAGNI). r7 deleted the cross-stream scope deliberately; shipping an interface and reconciler phase with zero implementations is speculative dead abstraction the design explicitly rejected ("if a genuine cross-render operation reappears, the scope can be reintroduced").
  - **Fold `rewriteWebhookURL`/`filterKinds` into `patch`** — rejected. `rewriteWebhookURL` iterates `.webhooks[]` conditionally and builds the new value from parts (`urlPrefix + service.path`) — not expressible as strategic-merge or JSON Patch. `filterKinds` removes resources from the stream — patches operate within a single resource. The design keeps them typed for exactly these reasons (§3.4.5).

**Decision: No in-place mutation**
- Chosen: Each transformation returns new/rebuilt `[]manifest.Manifest` and does not mutate the caller's input slice or the underlying `Unstructured`.
- Reason: The reconciler renders host and remote independently and threads each stream through the *same* ordered list; avoiding shared-state mutation removes a class of cross-render aliasing bugs. Supersedes the older impl.md sketch that wrote `manifests[i].Unstructured.Object = newObj` in place.
- Alternatives considered: in-place mutation — rejected, aliasing risk across the two renders that reuse the same `[]Transformation`.

**Decision: Missing-match policy is per-type**
- Chosen: `patch` errors on zero selector matches (fail-loud). `rewriteWebhookURL` and `filterKinds` no-op on zero matches.
- Reason: A mismatched `patch` selector is a misconfiguration (design §3.4.1); the stream operations are not assertions. Matches design intent and the old `make build-` behavior.
- Alternatives considered: uniform fail-loud or uniform no-op — rejected; the design distinguishes assertion-like patches from stream filters.

**Decision: `patch` re-validates the strategicMerge-XOR-jsonPatch invariant at runtime**
- Chosen: CEL enforces exactly-one at admission, but `patch.Apply` re-checks as a safety net (design §3.4.1). strategicMerge via `k8s.io/apimachinery/pkg/util/strategicpatch`; jsonPatch via `github.com/evanphx/json-patch`.
- Reason: The transform package may run on inputs not gated by this cluster's admission (tests, future callers); a cheap re-check is defense in depth.
- Alternatives considered: trust CEL only — rejected; the package should be correct standalone.

**Decision: `rewriteWebhookURL` walks three kinds, preserves caBundle, idempotent**
- Chosen: For `ValidatingWebhookConfiguration`/`MutatingWebhookConfiguration`, iterate `.webhooks[]`; for `CustomResourceDefinition`, only when `.spec.conversion.strategy == "Webhook"`, inspect `.spec.conversion.webhook.clientConfig`. For each `clientConfig` whose `.service` is set, replace with `.url = urlPrefix + service.path` (path `""` if absent); leave an existing `.url` alone; preserve `.caBundle`; other kinds and already-`.url` configs are no-ops. Targets both WebhookConfiguration kinds and CRD conversion webhooks unconditionally — no per-kind narrowing field.
- Reason: Matches the exact old `yq` behavior and closes the CRD conversion-webhook Service→URL gap (design §3.4.2, §9.2). Under r7 `.caBundle` is owned by the injector; `rewriteWebhookURL` never touches it, so nothing conflicts.
- Alternatives considered: a `targetKinds` narrowing field — already removed earlier this session; unconditional walk is simpler and every candidate operator wants all three.

**Decision: Injector integration is a `patch`, not a transformation type**
- Chosen: To make the injector's target patch mode adopt the WebhookConfigs / conversion-webhook CRDs, a `patch` stamps `metadata.labels[<--target-label key>] = <value>` on them in the remote render (design §3.4.4). No new code in `internal/transform`; covered by the `patch` tests.
- Reason: r7 replaced the cross-stream packaging transformation with disjoint-SSA-field coexistence; labeling is the only operator-side requirement, and it is patch-shaped. Order-insensitive since it only adds a label.
- Alternatives considered: a dedicated `labelForInjector` typed transformation — rejected; it is a plain label patch with no special logic.

## Capabilities

**Modified capabilities** (existing specs whose requirements this change realizes/reconciles):

- `crd-types` — the `Transformation` union is already the r7 3-type shape in code; this change confirms `internal/transform`'s `Build` consumes exactly `patch`/`rewriteWebhookURL`/`filterKinds` and errors on an empty entry. No CRD type change (the removal was archived).
- `cel-admission-validation` — no rule change; the 3-term union CEL rule is already live. `Build`'s empty-entry error is the runtime mirror of the admission "exactly one" rule.

**New capabilities:**

- `manifest-transformation` — the `internal/transform` package: the `Transformation` interface, `Build()` parse/ordering, shared `Match()` selector, and the three transformation implementations (`patch`, `rewriteWebhookURL`, `filterKinds`) with their per-type missing-match policy, no-in-place-mutation guarantee, and injector-label `patch` usage. Becomes `specs/manifest-transformation/spec.md`.

## Risks / Trade-offs

- [Strategic-merge patching of custom (CRD) types may fall back to JSON-merge behavior for list keys] → For the v1 candidate patches (Deployment sidecar, label stamping) core-type behavior suffices; document that users can switch to `jsonPatch` for list-key semantics on custom types (design §3.4.1). Verify against metal-operator fixtures during apply.
- [The same `[]Transformation` runs on both renders; a transformation that mutated shared state would corrupt the second render] → The no-in-place-mutation decision eliminates this by construction; enforce with a test that asserts the input slice/`Unstructured` is unchanged after `Apply`.
- [`patch` fail-loud on zero matches could break a CR whose selector legitimately matches nothing in one render but something in the other] → Accepted per design: a `patch` is applied per-render, and each candidate operator's patch selector matches in the render that emits the target. If a real case needs cross-render leniency it is a design change, not a silent no-op.
- [r7 depends on webhook-injector#14 shipping] → Out of scope for this package: `internal/transform` produces the same output regardless of injector mode (it only stamps a label and rewrites URLs). If PR #14 is abandoned, the fallback is a design-level revert to r6, not a change to this package.

## Migration Plan

No data migration. The r7 CRD type removal (`PackageWebhookConfigsForInjectorSpec`) was already applied and archived in `2026-07-16-remove-cross-stream-transformation`; a repo-wide scan confirms no sample or manifest references it, and `make manifests generate` is a clean no-op. This change adds a new package and tests only — no schema change, no regeneration required for the transformation type set.

Deployment steps:
1. Implement `internal/transform` (interface, `Build`, `Match`, three transformations) TDD.
2. `make build test lint-fix` green.

Rollback:
- Revert the `internal/transform` package addition. No CRD, RBAC, or generated-artifact changes to unwind.

## Open Questions

- [ ] Strategic-merge patching of custom (CRD) types may fall back to JSON-merge behavior for list keys; confirm no candidate operator needs strategic-merge list-key semantics on a custom type (design §3.4.1). — owner: implementer, verify against metal-operator fixtures during apply
- [ ] The injector-label key/value is a per-operator convention (`dual-deployment-operator.cc.sap/webhook-injector: <operator>`). It is a CR-authoring/chart concern, not `internal/transform` logic, but the `patch` tests should include a representative label-stamping case so the pattern is exercised. — owner: implementer, during apply
- [x] Does `filterKinds` need instance-level (name) granularity to drop one of several same-kind resources? **RESOLVED — no.** Researched the real `system/Makefile` `build-*-remote` targets in `sapcc/helm-charts` that this operator replaces. Every kind-filter across all five operators is a whole-kind drop by kind name only (metal: `Service`/`ValidatingWebhookConfiguration`/`MutatingWebhookConfiguration`; ipam-capi/khalkeon: `Service`; boot: `Service`/`ValidatingWebhookConfiguration`; argora: `Service`/`ValidatingWebhookConfiguration`/`ConfigMap`/`Secret`) — none narrows by `.metadata.name`, none keeps one instance while dropping another. The WebhookConfiguration drops are r5/r6 artifacts (r7 applies webhooks directly via `patch`-label, not `filterKinds`); the genuine r7 uses are whole-kind `Service`/`ConfigMap`/`Secret` drops. Where a chart emits multiple same-kind resources (e.g. metal's `metal-registry-service` + `webhook-service`), the two-render model's Helm values / kustomize overlays decide per-target rendering — so instance-level filtering is unneeded. Keeping `FilterKindsSpec` as `kinds` + optional `source`; a `name`/`Selector` field would be speculative surface (YAGNI). — owner: resolved during design
- [x] Does the injector-label `patch` need instance-level (name) selection to label one of several same-kind webhook objects? **RESOLVED — no.** Researched the actual rendered webhook/CRD artifacts for all five operators in `sapcc/helm-charts`. Each webhook kind is a singleton: metal-operator has 1 VWC + 0 MWC; ipam-capi has 1 MWC + 1 VWC; boot/argora/khalkeon have no webhooks at all. CRDs that need labeling are labeled uniformly by kind (the old pipeline uses an unqualified `select(.kind == "CustomResourceDefinition")`), and the injector's target patch mode self-filters to only `conversion.strategy: Webhook` CRDs, so labeling all CRDs by kind is harmless. A per-kind `patch target: {kind: ...}` therefore labels precisely the intended object(s) for every operator — no `name` selector needed. `patch`'s `Selector` already carries an optional `name` glob if a future operator ever needs it. — owner: resolved during design
- [ ] **Per-operator label guidance in `docs/design.md` / `docs/context.md` is factually inverted for metal-operator vs ipam-capi (verified against rendered charts).** The docs claim "metal-operator labels both its conversion-webhook CRDs and its WebhookConfigurations" and "ipam-capi labels only its WebhookConfigurations (its CRDs have no conversion webhook)". The rendered artifacts show the opposite: **metal-operator** has 17 CRDs with ZERO `conversion.strategy: Webhook` and only 1 VWC (no MWC) — so it should label only its single VWC; labeling its CRDs is pointless (injector skips non-conversion CRDs). **ipam-capi** has 4-of-7 conversion-webhook CRDs (`globalinclusterippools`, `globalinclusterprefixpools`, `inclusterippools`, `inclusterprefixpools`) plus 1 MWC + 1 VWC — so it SHOULD label its conversion CRDs in addition to its MWC+VWC. This is a CR-authoring / docs-accuracy concern (not `internal/transform` logic), but the per-operator CR profiles in plan/apply must use the corrected mapping, and the stale docs claims should be fixed. — owner: implementer, during apply (and a docs-sync pass)
