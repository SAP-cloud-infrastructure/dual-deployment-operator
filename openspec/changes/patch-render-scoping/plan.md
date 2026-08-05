<!-- SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# patch-render-scoping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Trailing `- [ ]` "Task N complete" checkboxes mark task-group completion — check them off AFTER all steps in the group land.

**Goal:** Make a zero-match `patch` transformation a clean, silent no-op (never error, never stop the reconcile), so a single-render-scoped patch (e.g. the webhook-injector label on a shoot-only `ValidatingWebhookConfiguration`) succeeds instead of failing the seed render.

**Architecture:** A ~3-line deletion in [`internal/transform/patch.go`](../../../internal/transform/patch.go): remove the `matched == 0 → error` guard and the now-unused `matched` counter from `patch.Apply`, so a zero-match render returns its input unchanged — identical to how `rewriteWebhookURL` and `filterKinds` already behave. The controller transform loop is **unchanged** (its `if err != nil` branches simply never fire for zero-match). No CRD change, no controller change, no shared-interface change.

**Tech Stack:** Go, `k8s.io/apimachinery` unstructured, table-driven `testing` (`go test`), envtest not required (transform unit tests are pure). Equivalence harness in `internal/equivalence` gated by `RUN_EQUIVALENCE=1`.

---

## Task 1: Make `patch.Apply` a silent no-op on zero match (TDD)

**Files:**
- Test: `internal/transform/patch_test.go` (modify the table in `TestPatch`, lines 84-89 and add cases)
- Modify: `internal/transform/patch.go:30-57` (`Apply` + doc comment)

- [ ] **Step 1: Flip the existing "zero matches fails loud" test case to a no-op assertion**

In [`internal/transform/patch_test.go`](../../../internal/transform/patch_test.go), replace the existing case (lines 84-89):

```go
		{
			name:    "zero matches fails loud",
			spec:    &v1alpha1.PatchSpec{Target: v1alpha1.Selector{Kind: "Deployment", Name: "nope"}, StrategicMerge: mustJSON(`{}`)},
			input:   []manifest.Manifest{dep()},
			wantErr: "no matching resource",
		},
```

with a no-op case that asserts the input is returned unchanged and byte-identical:

```go
		{
			name:  "zero matches is a clean no-op",
			spec:  &v1alpha1.PatchSpec{Target: v1alpha1.Selector{Kind: "Deployment", Name: "nope"}, StrategicMerge: mustJSON(`{"spec":{"replicas":9}}`)},
			input: []manifest.Manifest{dep()},
			check: func(t *testing.T, out []manifest.Manifest) {
				if len(out) != 1 {
					t.Fatalf("len(out) = %d, want 1", len(out))
				}
				if r := nestedInt64(t, out[0], "spec", "replicas"); r != 1 {
					t.Fatalf("replicas = %d, want 1 (patch must not have applied)", r)
				}
			},
		},
```

- [ ] **Step 2: Add a second no-op case proving a non-empty stream passes through untouched**

Add this case immediately after the one from Step 1 (the shoot-only-VWC single-render scenario, exercised at the `Apply` level — the selector matches nothing in a seed-like stream of only a Deployment):

```go
		{
			name: "patch targeting absent kind no-ops the whole stream",
			spec: &v1alpha1.PatchSpec{
				Target:         v1alpha1.Selector{Kind: "ValidatingWebhookConfiguration"},
				StrategicMerge: mustJSON(`{"metadata":{"labels":{"dual-deployment-operator.cc.sap/webhook-injector":"metal-operator"}}}`),
			},
			input: []manifest.Manifest{dep()},
			check: func(t *testing.T, out []manifest.Manifest) {
				if len(out) != 1 {
					t.Fatalf("len(out) = %d, want 1", len(out))
				}
				if _, ok := out[0].Unstructured.GetLabels()["dual-deployment-operator.cc.sap/webhook-injector"]; ok {
					t.Fatalf("injector label must not be added to a non-matching Deployment")
				}
				if out[0].Unstructured.GetKind() != "Deployment" {
					t.Fatalf("kind = %q, want Deployment", out[0].Unstructured.GetKind())
				}
			},
		},
```

- [ ] **Step 3: Run the tests — expect FAIL**

Run: `go test ./internal/transform/ -run TestPatch -v`
Expected: FAIL — the two no-op cases fail because `Apply` still returns `errors.New("no matching resource for patch target selector")` (the `check` funcs are never reached; the harness hits the `if err != nil { t.Fatalf }` branch at line 113-115).

- [ ] **Step 4: Remove the fail-loud guard and the `matched` counter from `patch.Apply`**

In [`internal/transform/patch.go`](../../../internal/transform/patch.go), change `Apply` (lines 30-57). Update the doc comment and delete the counter + guard:

```go
// Apply applies the strategic-merge XOR json patch to every manifest matching
// the target selector. A zero-match selector is a clean no-op: the input
// manifests are returned unchanged, no error (consistent with rewriteWebhookURL
// and filterKinds). Never mutates input in place.
func (p *patch) Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error) {
	hasSM := p.spec.StrategicMerge != nil
	hasJP := len(p.spec.JSONPatch) > 0
	if hasSM == hasJP {
		return nil, errors.New("exactly one of strategicMerge or jsonPatch must be set")
	}

	out := make([]manifest.Manifest, len(manifests))
	for i, m := range manifests {
		if !Match(m, p.spec.Target) {
			out[i] = m
			continue
		}
		patched, err := p.applyOne(m.Unstructured)
		if err != nil {
			return nil, fmt.Errorf("patch %s/%s: %w", m.Unstructured.GetKind(), m.Unstructured.GetName(), err)
		}
		out[i] = manifest.Manifest{Unstructured: patched, Origin: m.Origin}
	}
	return out, nil
}
```

(This deletes `matched := 0` at line 40, `matched++` at line 46, and the `if matched == 0 { return nil, errors.New(...) }` block at lines 53-55.)

- [ ] **Step 5: Run the tests — expect PASS**

Run: `go test ./internal/transform/ -run TestPatch -v`
Expected: PASS — all `TestPatch` subtests pass, including the two new no-op cases. The `both variants` / `neither variant` error cases still pass (that guard is untouched).

- [ ] **Step 6: Confirm the package still builds and vets clean**

Run: `go build ./... && go vet ./internal/transform/`
Expected: exit 0. (If `errors` becomes unused after the deletion — it does not, it is still used at the exactly-one guard — the build would fail; it stays imported.)

- [ ] **Step 7: Commit**

```bash
git add internal/transform/patch.go internal/transform/patch_test.go
git commit -m "fix(transform): make zero-match patch a silent no-op

A patch whose selector matches nothing now returns the input manifests
unchanged with no error, consistent with rewriteWebhookURL and filterKinds.
This lets a single-render-scoped patch (e.g. the webhook-injector label on a
shoot-only ValidatingWebhookConfiguration) succeed instead of failing the seed
render with SeedTransformFailed."
```

- [ ] Task 1 complete

---

## Task 2: Ordering regression — a no-op patch does not swallow later transforms (TDD)

Guards spec scenario S4: a no-op render must pass its unchanged manifests through so a later transformation still sees them.

**Files:**
- Test: `internal/transform/patch_test.go` (new standalone test function, appended after `TestPatch`)

- [ ] **Step 1: Write the failing ordering test**

Append this function to [`internal/transform/patch_test.go`](../../../internal/transform/patch_test.go) (after the existing `TestPatch`). It applies two patches in sequence: the first no-ops (targets an absent kind), the second must still patch the Deployment that passed through unchanged.

```go
func TestPatchNoOpPassesStreamToNextTransform(t *testing.T) {
	dep := mustManifest(t, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller-manager
spec:
  replicas: 1
`, manifest.OriginUpstream)

	// First patch targets a kind not present -> must no-op, returning the stream unchanged.
	first := &patch{spec: &v1alpha1.PatchSpec{
		Target:         v1alpha1.Selector{Kind: "ValidatingWebhookConfiguration"},
		StrategicMerge: mustJSON(`{"metadata":{"labels":{"x":"y"}}}`),
	}}
	// Second patch must still see the Deployment and patch it.
	second := &patch{spec: &v1alpha1.PatchSpec{
		Target:         v1alpha1.Selector{Kind: "Deployment"},
		StrategicMerge: mustJSON(`{"spec":{"replicas":7}}`),
	}}

	mid, err := first.Apply([]manifest.Manifest{dep})
	if err != nil {
		t.Fatalf("first.Apply() error = %v", err)
	}
	out, err := second.Apply(mid)
	if err != nil {
		t.Fatalf("second.Apply() error = %v", err)
	}
	if r := nestedInt64(t, out[0], "spec", "replicas"); r != 7 {
		t.Fatalf("replicas = %d, want 7 (second patch must see the passed-through Deployment)", r)
	}
}
```

- [ ] **Step 2: Run the test — expect PASS (green after Task 1)**

Run: `go test ./internal/transform/ -run TestPatchNoOpPassesStreamToNextTransform -v`
Expected: PASS. (This is a regression guard, not a new behavior — it depends on Task 1's no-op fix. If Task 1 were reverted, `first.Apply` would error and this test would fail at the first `t.Fatalf`.)

- [ ] **Step 3: Run the full transform package to confirm nothing else regressed**

Run: `go test ./internal/transform/`
Expected: `ok  github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/transform`

- [ ] **Step 4: Commit**

```bash
git add internal/transform/patch_test.go
git commit -m "test(transform): assert a no-op patch passes the stream to later transforms"
```

- [ ] Task 2 complete

---

## Task 3: Equivalence fixture — injector label lands on the shoot VWC only (RUN_EQUIVALENCE-gated)

Proves the motivating `metal-operator-remote-v2` case end-to-end through `source.From → Render → transform.Build → Apply`: the shoot render's `ValidatingWebhookConfiguration` carries the injector label; the seed render is a byte-identical no-op. This subtest is opt-in behind `RUN_EQUIVALENCE=1` because the golden render needs the internal keppel OCI registry.

**Files:**
- Inspect first: `internal/equivalence/` (harness + existing metal-operator subtest), `internal/equivalence/testdata/fixtures/metal-operator/`

- [ ] **Step 1: Read the existing metal-operator equivalence fixture + subtest to match its shape**

Run: `ls internal/equivalence/testdata/fixtures/metal-operator/ && grep -rn "metal-operator\|target-label\|webhook-injector\|ValidatingWebhookConfiguration\|RUN_EQUIVALENCE" internal/equivalence/`
Expected: locates the fixture layout (CR spec, known-divergences, ignoreLabels, canonicalNamespace) and the `RUN_EQUIVALENCE`-gated subtest for metal-operator. Do NOT invent a new harness — extend the existing metal-operator fixture/subtest.

- [ ] **Step 2: Add the injector-label `patch` to the metal-operator fixture CR's `transformations`**

In the metal-operator fixture's CR spec (the `spec.transformations` list — exact path from Step 1, typically `internal/equivalence/testdata/fixtures/metal-operator/<cr>.yaml`), add a `patch` that stamps the injector label on the shoot-only VWC:

```yaml
    - patch:
        target:
          kind: ValidatingWebhookConfiguration
        strategicMerge:
          metadata:
            labels:
              dual-deployment-operator.cc.sap/webhook-injector: metal-operator
```

This patch matches in the shoot render (which contains the VWC) and no-ops in the seed render (which does not) — the exact behavior Task 1 enables.

- [ ] **Step 3: Assert the label lands on the shoot VWC and the seed render is unaffected**

In the metal-operator equivalence subtest (exact function from Step 1), add an assertion on the operator-side capture: the shoot render's `ValidatingWebhookConfiguration` has label `dual-deployment-operator.cc.sap/webhook-injector=metal-operator`, and no seed-render resource carries it. If the golden render does not already contain this label (it won't — the label is operator-added, not chart-emitted), record it as a per-fixture known-divergence / ignoreLabel so the comparator does not flag the operator-added label as drift. Follow the existing known-divergences mechanism identified in Step 1.

- [ ] **Step 4: Run the gated equivalence subtest — expect PASS (or documented skip if registry unreachable)**

Run: `RUN_EQUIVALENCE=1 go test ./internal/equivalence/ -run 'Equivalence/metal-operator' -v`
Expected: PASS if the internal keppel OCI registry (`oci://keppel.global.cloud.sap/ccloud-helm/…`) is reachable. If it is unreachable from the current environment, the subtest is expected to fail at the golden-render fetch — this is the documented public-CI limitation; note it and rely on Task 1/2 unit coverage. The offline comparator/normalization unit tests MUST still pass:
Run: `go test ./internal/equivalence/`
Expected: `ok` (offline tests always run).

- [ ] **Step 5: Commit**

```bash
git add internal/equivalence/testdata/fixtures/metal-operator internal/equivalence
git commit -m "test(equivalence): prove injector label lands on shoot VWC only (metal-operator)"
```

- [ ] Task 3 complete

---

## Task 4: Documentation status flip

Per the handoff Definition of Done: flip the proposal status and re-sync context if the implemented shape differs.

**Files:**
- Modify: `docs/patch-render-scoping.md` (Status line)
- Verify only: `docs/context.md` (Revision 10), `docs/design.md` (§3.4.1, §3.4.4)

- [ ] **Step 1: Flip the proposal status**

In `docs/patch-render-scoping.md`, change the Status line from `Proposed` to `Implemented` and add a one-line note: implemented shape = "zero-match is a silent no-op (no controller backstop, no warning); consistent with rewriteWebhookURL/filterKinds". Run: `grep -n "Status" docs/patch-render-scoping.md` first to find the exact line.

- [ ] **Step 2: Verify context.md Revision 10 and design.md §3.4.1/§3.4.4 already match the implemented shape**

Run: `grep -n "silent no-op\|TransformNoMatch\|fails loud\|Revision 10" docs/context.md docs/design.md`
Expected: `docs/design.md` §3.4.1/§3.4.4 already describe the silent-no-op behavior (updated during design). `docs/context.md` Revision 10 must not still promise a controller backstop/warning — if it does, reword it to "silent no-op, consistent with the other transforms". Fix inline only if a mismatch is found.

- [ ] **Step 3: Full gate — build + transform tests + lint**

Run: `go build ./... && go test ./internal/transform/... && make run-golangci-lint`
Expected: build exit 0; transform tests `ok`; golangci-lint reports no NEW findings on the changed files (`internal/transform/patch.go`, `internal/transform/patch_test.go`). Re-verify any "pre-existing" lint claim against the change's own diff before accepting it.

- [ ] **Step 4: Commit**

```bash
git add docs/patch-render-scoping.md docs/context.md
git commit -m "docs: flip patch-render-scoping to Implemented (silent no-op shape)"
```

- [ ] Task 4 complete
