# Remove Cross-Stream Transformation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Trailing `- [ ]` checkboxes mark task-group completion — check them off AFTER all steps in the group complete.

**Goal:** Remove the dead cross-stream `packageWebhookConfigsForInjector` transformation type from the `v1alpha1` CRD (type, union field, DeepCopy, CEL term, tests) and reconcile the `crd-types` + `cel-admission-validation` specs to design revision 7, then regenerate manifests/deepcopy so specs, Go types, and generated artifacts agree.

**Architecture:** Edit the hand-written CRD types in `api/v1alpha1/dualdeploymentoperator_types.go` (remove one struct, one union field, and one CEL term on the `Transformation` marker), delete the corresponding test case, then run `make generate` + `make manifests` to regenerate `zz_generated.deepcopy.go` and the CRD YAML. Verify with envtest-backed CEL tests and unit tests. No transformation *logic* is implemented here (`internal/transform` does not exist yet — that is the downstream `transformations` change); this change is purely a schema/spec reconciliation. The `rewriteWebhookURL`-targets-CRDs behavior is *specified* by this change's delta but *implemented* in the Phase-3 `transformations` change, so this plan does not touch rewrite logic.

**Tech Stack:** Go 1.26, kubebuilder v4 markers, controller-gen (`make manifests`/`make generate`), Ginkgo/Gomega + envtest (`make test`), CEL admission validation, apiextensions `XValidation`.

---

## Task 1: Remove `PackageWebhookConfigsForInjector` from the CRD types + CEL marker

**Files:**
- Modify: `api/v1alpha1/dualdeploymentoperator_types.go` (Transformation union marker + field ~L81-L91; `PackageWebhookConfigsForInjectorSpec` struct ~L130-L137)

- [ ] **Step 1: Read the current Transformation block to confirm line targets**

Run: `sed -n '80,138p' api/v1alpha1/dualdeploymentoperator_types.go`
Expected: the `+kubebuilder:validation:XValidation` marker with four `has(self.*)` terms, the `Transformation` struct with a `PackageWebhookConfigsForInjector` field, and the `PackageWebhookConfigsForInjectorSpec` struct.

- [ ] **Step 2: Remove the 4th CEL term from the `Transformation` marker**

In `api/v1alpha1/dualdeploymentoperator_types.go`, change the marker line from:

```go
// +kubebuilder:validation:XValidation:rule="(has(self.patch) ? 1 : 0) + (has(self.rewriteWebhookURL) ? 1 : 0) + (has(self.filterKinds) ? 1 : 0) + (has(self.packageWebhookConfigsForInjector) ? 1 : 0) == 1",message="exactly one transformation type must be set per entry"
```

to:

```go
// +kubebuilder:validation:XValidation:rule="(has(self.patch) ? 1 : 0) + (has(self.rewriteWebhookURL) ? 1 : 0) + (has(self.filterKinds) ? 1 : 0) == 1",message="exactly one transformation type must be set per entry"
```

- [ ] **Step 3: Remove the `PackageWebhookConfigsForInjector` union field**

In the `Transformation` struct, delete these two lines:

```go
	// +optional
	PackageWebhookConfigsForInjector *PackageWebhookConfigsForInjectorSpec `json:"packageWebhookConfigsForInjector,omitempty"`
```

The struct now ends with the `FilterKinds` field.

- [ ] **Step 4: Delete the `PackageWebhookConfigsForInjectorSpec` struct**

Delete the entire struct declaration:

```go
type PackageWebhookConfigsForInjectorSpec struct {
	// +kubebuilder:validation:MinLength=1
	ConfigMapName string `json:"configMapName"`
	// +optional
	DataKey string `json:"dataKey,omitempty"`
}
```

- [ ] **Step 5: Verify no other references remain in hand-written code**

Run: `grep -rn "PackageWebhookConfigsForInjector" api/v1alpha1/dualdeploymentoperator_types.go`
Expected: no output (zero matches).

- [ ] **Step 6: Commit**

```bash
git add api/v1alpha1/dualdeploymentoperator_types.go
git commit -m "feat(crd)!: remove packageWebhookConfigsForInjector transformation type

Removes the r5/r6 cross-stream transformation type and its CEL union
term per design revision 7 (docs/design.md §2.2.4). The operator now
applies WebhookConfigurations directly; the webhook-injector target
patch mode keeps caBundle in sync."
```

- [ ] Task 1 complete

---

## Task 2: Remove the round-trip test case for the deleted type

**Files:**
- Modify: `api/v1alpha1/dualdeploymentoperator_types_test.go:57`

- [ ] **Step 1: Run the round-trip test to confirm it currently references the deleted type (build failure)**

Run: `go test ./api/v1alpha1/... -run TestTransformationVariantsRoundTrip 2>&1 | head -20`
Expected: FAIL — compile error `undefined: PackageWebhookConfigsForInjectorSpec` (and `unknown field PackageWebhookConfigsForInjector`), because Task 1 deleted the type but the test still constructs it.

- [ ] **Step 2: Delete the `PackageWebhookConfigsForInjector` case from the test table**

In `api/v1alpha1/dualdeploymentoperator_types_test.go`, remove this line from the `cases := []Transformation{...}` slice:

```go
		{PackageWebhookConfigsForInjector: &PackageWebhookConfigsForInjectorSpec{ConfigMapName: "webhooks"}},
```

The table now ends with the `FilterKinds` case.

- [ ] **Step 3: Run the round-trip test to verify it compiles and passes**

Run: `go test ./api/v1alpha1/... -run TestTransformationVariantsRoundTrip -v`
Expected: PASS — the three remaining variants (`Patch` ×2, `RewriteWebhookURL`, `FilterKinds`) round-trip.

- [ ] **Step 4: Verify no `PackageWebhookConfigsForInjector` references remain anywhere in the repo's Go sources (excluding generated)**

Run: `grep -rn "PackageWebhookConfigsForInjector" api/ internal/ cmd/ --include='*.go' | grep -v zz_generated`
Expected: no output (zero matches). The only remaining references are in `zz_generated.deepcopy.go`, removed by regeneration in Task 3.

- [ ] **Step 5: Commit**

```bash
git add api/v1alpha1/dualdeploymentoperator_types_test.go
git commit -m "test(crd): drop packageWebhookConfigsForInjector round-trip case"
```

- [ ] Task 2 complete

---

## Task 3: Regenerate deepcopy + CRD manifests and verify consistency

**Files:**
- Regenerate (do not hand-edit): `api/v1alpha1/zz_generated.deepcopy.go`
- Regenerate (do not hand-edit): `config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml`
- Regenerate (do not hand-edit): `config/rbac/role.yaml` (if affected)

- [ ] **Step 1: Confirm deepcopy still references the deleted type (pre-regeneration state)**

Run: `grep -n "PackageWebhookConfigsForInjector" api/v1alpha1/zz_generated.deepcopy.go`
Expected: matches at the DeepCopyInto/DeepCopy methods (~L238-249) and the union-field copy in `Transformation.DeepCopyInto` (~L387-390). These are stale until regenerated.

- [ ] **Step 2: Regenerate deepcopy**

Run: `make generate`
Expected: exits 0; `zz_generated.deepcopy.go` rewritten.

- [ ] **Step 3: Regenerate CRD manifests + RBAC**

Run: `make manifests`
Expected: exits 0; the CRD YAML rewritten.

- [ ] **Step 4: Verify the deleted type is gone from generated artifacts**

Run: `grep -rn "packageWebhookConfigsForInjector\|PackageWebhookConfigsForInjector" api/v1alpha1/zz_generated.deepcopy.go config/crd/ config/rbac/`
Expected: no output (zero matches).

- [ ] **Step 5: Verify the CRD CEL rule now has three terms, not four**

Run: `grep -n "exactly one transformation type must be set" config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml`
Then inspect the surrounding `rule:` line.
Expected: the rule counts `patch`, `rewriteWebhookURL`, `filterKinds` only — no `packageWebhookConfigsForInjector` term.

- [ ] **Step 6: Commit**

```bash
git add api/v1alpha1/zz_generated.deepcopy.go config/crd/ config/rbac/
git commit -m "chore(crd): regenerate deepcopy + manifests after type removal"
```

- [ ] Task 3 complete

---

## Task 4: Verify build, unit tests, and CEL admission tests are green

**Files:**
- No edits — verification only. Covers `internal/controller/cel_validation_test.go` (envtest CEL) and `api/v1alpha1/...` unit tests.

- [ ] **Step 1: Build the whole module**

Run: `make build`
Expected: exits 0 (compiles with the removed type).

- [ ] **Step 2: Run the full test suite (envtest CEL + unit)**

Run: `make test`
Expected: PASS. In particular `internal/controller/cel_validation_test.go`'s "transformation union" context still passes: an empty entry and a two-field entry are both rejected with `"exactly one transformation type must be set per entry"` (these tests do not construct `packageWebhookConfigsForInjector`, so no test edit was needed — confirm this holds).

- [ ] **Step 3: Confirm the removed variant is now rejected at admission (spec scenario)**

This is covered structurally: with the field removed from the type, the generated CRD structural schema prunes/rejects an unknown `packageWebhookConfigsForInjector` field. No new test is required by the spec beyond the existing union tests, but confirm the CRD schema has no `packageWebhookConfigsForInjector` property:

Run: `grep -n "packageWebhookConfigsForInjector" config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml`
Expected: no output (the property is gone, so a CR setting it is rejected by structural pruning).

- [ ] **Step 4: Run linters**

Run: `make check` (or `make run-golangci-lint` if `check` runs more than needed)
Expected: 0 issues.

- [ ] **Step 5: Validate the OpenSpec change still passes strict validation**

Run: `openspec validate remove-cross-stream-transformation --type change --strict`
Expected: `Change 'remove-cross-stream-transformation' is valid`.

- [ ] **Step 6: Commit (only if Step 4 auto-fixed formatting; otherwise skip)**

```bash
git add -A
git commit -m "chore: lint fixes after cross-stream type removal" || echo "nothing to commit"
```

- [ ] Task 4 complete

---

## Notes for the implementer

- **`rewriteWebhookURL` targeting CRDs is NOT implemented in this change.** This change's `crd-types` delta *specifies* that `rewriteWebhookURL` rewrites conversion-webhook CRD `clientConfig` (design.md §3.4.2, §9.2), but the transformation package (`internal/transform`) does not exist yet — it is built in the downstream `transformations` change (Phase 3). Do not create `internal/transform` here. The delta text is the forward contract; the failing-test-first implementation of it (including conversion-webhook CRD and non-webhook CRD fixtures) belongs to Phase 3, per `docs/implementation.md`.
- **`TargetKinds`** is already absent from the Go type, deepcopy, and CRD YAML; only the tracked live spec described it. This change reconciles the spec via the `crd-types` delta on archive — there is no code step for `TargetKinds` (nothing to remove in code). Do not add one.
- **Never hand-edit** `zz_generated.deepcopy.go`, `config/crd/bases/*`, or `config/rbac/role.yaml` — always regenerate via `make generate` / `make manifests` (AGENTS.md).
- **Do not delete** `// +kubebuilder:scaffold:*` markers.
