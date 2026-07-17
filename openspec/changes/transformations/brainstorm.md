## Design Summary

Phase 3 of the `dual-deployment-operator` build-out: implement the **transformation layer** (`internal/transform`) that turns a `DualDeploymentOperator` CR's `spec.transformations` into an ordered list of operations applied to the two origin-tagged manifest streams produced by Phase 2 (`internal/source` → `[]manifest.Manifest`). Under design **revision 7**, the menu is **three per-render types in a single scope** — `patch` (strategic-merge XOR JSON Patch DSL), `rewriteWebhookURL` (typed), `filterKinds` (typed) — realizing `docs/design.md` §3.4. The package exposes **one** `Transformation` interface, a `Group()`/parse step that builds the ordered list from the CR entries, and a shared `Match()` selector. Load-bearing constraints: the output feeds Phase 5 delivery and Phase 6 reconciler wiring, so semantics (missing-match policy, no in-place mutation, ordering) are fixed here; and the design/CRD have committed the type set, so this phase realizes them.

r7 (enabled by [webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14)) **removed the cross-stream scope** and its sole type `packageWebhookConfigsForInjector`: the operator now applies WebhookConfigurations (and conversion-webhook CRDs) **directly** to the shoot in the remote render, and the injector's target patch mode keeps their `.caBundle` current in place. Injector integration is therefore no longer a transformation — it is a plain `patch` that stamps the injector's `--target-label` onto the webhook objects (design §3.4.4).

**In scope**: `internal/transform` (one `Transformation` interface, `Group`/parse, `Match`/selector, three transformation implementations). Removing the now-obsolete `PackageWebhookConfigsForInjector` field/struct from the CRD types + regenerating (impl.md Phase 3 step 1). Table-driven tests with fixtures, offline (no cluster).

**Out of scope** (later phases): reconciler wiring (Phase 6), dual-cluster delivery/SSA + disjoint-field caBundle coexistence (Phase 5), status population, the v2 validating admission webhook for patch content.

## Alternatives Considered

### Option A: One `Transformation` interface + Group()/parse + shared Match() (chosen, r7)

- **Approach**: Define a single `Transformation` interface (`Type() string`, `Apply([]Manifest) ([]Manifest, error)`). `Group(specs)` (or an equivalently-named parse step) walks the CR's discriminated-union entries and returns an ordered `[]Transformation`, preserving declaration order; an entry with no field set → error. A shared `Match(m, Selector)` in `selector.go` (kind exact / name glob / origin) serves `patch` (and optionally `filterKinds` `source`). Each transformation returns fresh manifests, never mutating the caller's slice or the underlying `Unstructured`.
- **Pros**: Matches design §3.4/§5.2 and impl.md Phase 3 exactly under r7; the reconciler (§5.3) applies one ordered list to each render independently — no scope branching; smallest surface; no cross-render state; small independently-testable units.
- **Cons**: None material — a single scope is the natural fit once cross-stream is gone.
- **Why not chosen**: This IS the chosen approach.

### Option B: Keep the two-scope shape (PerRenderTransformation + CrossStreamTransformation) for forward-compatibility

- **Approach**: Retain both interfaces and `Group`-by-scope even though only per-render types exist in r7, anticipating a future cross-render operation.
- **Pros**: If a genuine cross-render operation reappears, no interface churn.
- **Cons**: Ships an interface and a reconciler phase with **zero implementations** — speculative complexity the design explicitly rejected in r7 ("if a genuine cross-render operation reappears, the scope can be reintroduced"). Dead abstraction, extra tests for an empty path, misleading to readers.
- **Why not chosen**: YAGNI. r7 deleted the scope deliberately; re-adding it "just in case" contradicts the design.

### Option C: Fold `rewriteWebhookURL`/`filterKinds` into `patch`

- **Approach**: Express the two typed transformations as `patch` operations, leaving `patch` as the only type.
- **Pros**: One transformation type.
- **Cons**: `rewriteWebhookURL` iterates `.webhooks[]` with a conditional (only when `.service` is set) and builds the new value from parts (`urlPrefix + service.path`) — not expressible as strategic-merge or JSON Patch. `filterKinds` removes resources from the stream — patches operate within a single resource, not across the stream. Forcing either into `patch` loses clarity and correctness.
- **Why not chosen**: The design keeps them typed for exactly these reasons (§3.4.5). Only the genuinely patch-shaped operations (old `injectInitContainer`/`addLabels`) became `patch`.

## Agreed Approach

**Option A.** A single `Transformation` interface with a `Group()`/parse step and a shared `Match()` selector, realizing design §3.4/§5.2 under r7. Transformations return fresh manifests (no in-place mutation), because the two render streams are independent and the reconciler threads each through the same ordered list separately. Missing-match behavior is per-type (see Key Decisions). There is **no cross-stream interface, no `ApplyCrossStream`, and no `manifest.SerializeMultiDoc`** — those were r5/r6 artifacts of the removed webhook-packaging transformation. Injector integration is achieved outside this package's type set, via a `patch` that stamps the injector's `--target-label` label onto WebhookConfigurations and conversion-webhook CRDs (design §3.4.4); it needs no new transformation code.

## Key Decisions

- **Single `Transformation` interface (r7)**: `Type() string`, `Apply([]Manifest) ([]Manifest, error)`. The r5/r6 `CrossStreamTransformation` (`ApplyCrossStream(host, remote, hostNamespace)`) is **not implemented** — the cross-stream scope was removed in design r7 (§2.2.4, §3.4). Rationale: the only cross-render operation that ever existed (webhook packaging) is obsolete now that the operator applies WebhookConfigs directly and the injector patches caBundle in place.

- **`Group(specs []v1alpha1.Transformation) ([]Transformation, error)` builds one ordered list**, preserving declaration order; an entry with no field set → error. Rationale: ordering is user-controlled and load-bearing (design §3.4: structural patches → rewrites → filters); with one scope there is no per-scope partition.

- **No in-place mutation**: each transformation returns new/rebuilt `[]Manifest` and does not mutate the caller's input slice or the underlying `Unstructured`. Rationale: the reconciler renders host and remote independently and threads each stream through the same ordered list; avoiding shared-state mutation removes a class of cross-render aliasing bugs. (Supersedes the older impl.md sketch that wrote `manifests[i].Unstructured.Object = newObj` in place.)

- **Missing-match policy is per-type**: `patch` errors on zero selector matches (fail-loud — a mismatched selector is a misconfiguration, design §3.4.1). `rewriteWebhookURL` and `filterKinds` no-op on zero matches (stream operations, not assertions). Rationale: matches design intent and the old `make build-` behavior.

- **`patch` re-validates the strategicMerge-XOR-jsonPatch invariant at runtime**: CEL enforces it at admission, but the transformation re-checks as a safety net (design §3.4.1). strategicMerge via `k8s.io/apimachinery/pkg/util/strategicpatch`; jsonPatch via `github.com/evanphx/json-patch`.

- **`rewriteWebhookURL` targets both Validating and Mutating WebhookConfigurations unconditionally**: for each webhook whose `.clientConfig.service` is set, replace with `.clientConfig.url = urlPrefix + service.path`; preserve `.caBundle`; leave an existing `.url` alone (idempotent). Rationale: matches the exact old `yq` behavior; the previously-present `targetKinds` CRD field was removed earlier this session. (Under r7 `.caBundle` is owned by the injector, but rewriteWebhookURL never touches caBundle anyway, so nothing changes here.)

- **`filterKinds` drops listed kinds from the stream**, optional `source` origin restriction (`upstream`/`additions`). Rationale: stream-level filter; not a per-resource patch.

- **Injector integration is a `patch`, not a transformation type (r7)**: to make the injector's target patch mode adopt the WebhookConfigs/conversion-CRDs, a `patch` stamps `metadata.labels[<--target-label key>] = <value>` on them in the remote render (design §3.4.4). No new code in `internal/transform`; covered by the `patch` tests. Rationale: r7 replaced the cross-stream packaging transformation with disjoint-SSA-field coexistence; labeling is the only operator-side requirement, and it is patch-shaped.

- **CRD cleanup**: this change removes the now-obsolete `PackageWebhookConfigsForInjector` field + `PackageWebhookConfigsForInjectorSpec` struct from `api/v1alpha1` and drops it from the `Transformation` union CEL rule (leaving the 3-way count), then regenerates CRD/deepcopy (impl.md Phase 3 step 1). Rationale: r7 deleted the type; leaving a dead union member in the CRD would let a CR set a transformation the operator no longer implements. (Updating the archived `crd-types` main spec accordingly keeps specs coherent, same as the earlier `targetKinds` removal.)

- **No `manifest.SerializeMultiDoc`**: it was only needed to serialize WebhookConfigs into the packaged ConfigMap (r5/r6). r7 removes that need; do not add the helper speculatively (impl.md Phase 3).

- **Testing is table-driven per transformation, offline**, with fixtures under `testdata/fixtures/transform/<name>/`, plus unit tests for `Group`/parse and `Match`. TDD (RED→GREEN→REFACTOR) per AGENTS.md.

## Open Questions

- [x] The CRD `PackageWebhookConfigsForInjector` removal is a **breaking CRD change** to a field introduced in the archived Phase 1 (`crd-types`). No CR in the wild uses it yet (pre-release, v1alpha1), so removal is safe now — confirm no sample/manifest references it before regenerating. — owner: implementer, during apply — **RESOLVED**: the removal was already applied in commit `5759465` (archived change `2026-07-16-remove-cross-stream-transformation`); the field is absent from the Go type, CEL rule, deepcopy, and generated CRD/RBAC. A repo-wide grep found no reference in any sample (`config/samples/*` has an empty `spec:`) or manifest (`config/crd/bases/*`, `config/rbac/role.yaml`, `config/webhook/*`) — remaining hits are docs/specs/archived-change prose only. `make manifests generate` runs clean (exit 0) with a zero diff, confirming generated artifacts are already in sync.
- [ ] Strategic-merge patching of custom (CRD) types may fall back to JSON-merge behavior for list keys; for the v1 candidate patches (Deployment sidecar, label stamping) core-type behavior suffices, and users can switch to `jsonPatch` if needed (design §3.4.1). Confirm no candidate operator needs strategic-merge list-key semantics on a custom type. — owner: implementer, verify against metal-operator fixtures
- [ ] The injector-label key/value is a per-operator convention (`dual-deployment-operator.cc.sap/webhook-injector: <operator>` in the design example). It is a CR-authoring/chart concern, not `internal/transform` logic, but the `patch` tests should include a representative label-stamping case so the pattern is exercised. — owner: implementer
