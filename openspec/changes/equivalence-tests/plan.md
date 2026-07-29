# Equivalence Tests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Trailing `- [ ]` checkboxes mark task-group completion — check off AFTER all steps complete.

**Goal:** Prove the operator's rendered output matches today's `<operator>-remote` wrapper charts for all five operators, gating every PR.

**Architecture:** A new `internal/equivalence` package with three concerns: (1) a golden helper that pulls the wrapper chart from GHCR and renders it via `helm template`, then classifies/unwraps/excludes into a bare-object set; (2) an operator-capture helper that drives `source.From → Render(seed/shoot) → transform.Build → Apply` and captures the two manifest streams; (3) a comparator that canonically normalizes both sides, applies a provenance/incidental allowlist, and deep-equals per resource. Per-operator fixtures pin the chart version + value baseline; one `t.Run` subtest per operator gates CI.

**Tech Stack:** Go, `helm.sh/helm/v3` SDK (`action.Pull` + `action.Install` client-only, `registry.Client` for OCI), `k8s.io/apimachinery` `unstructured`, existing `internal/source`, `internal/transform`, `internal/manifest` packages. Module: `github.com/SAP-cloud-infrastructure/dual-deployment-operator`. Test cmds use the project's SAP go-makefile-maker toolchain: `go test ./internal/equivalence/...` (network tests) and `go test ./internal/equivalence/... -run <Name>` for focused runs.

---

## Task 1: Manifest set model + resource identity key

**Files:**
- Create: `internal/equivalence/set.go`
- Test: `internal/equivalence/set_test.go`

- [ ] **Step 1: Write the failing test**

```go
package equivalence

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestResourceKeyIsVersionIndependent(t *testing.T) {
	a := &unstructured.Unstructured{}
	a.SetGroupVersionKind(schemaGVK("apps/v1", "Deployment"))
	a.SetNamespace("ns1")
	a.SetName("controller-manager")

	b := &unstructured.Unstructured{}
	b.SetGroupVersionKind(schemaGVK("apps/v2", "Deployment"))
	b.SetNamespace("ns1")
	b.SetName("controller-manager")

	if KeyOf(a) != KeyOf(b) {
		t.Fatalf("key should ignore version: %q != %q", KeyOf(a), KeyOf(b))
	}
	if KeyOf(a) != "apps/Deployment/ns1/controller-manager" {
		t.Fatalf("unexpected key %q", KeyOf(a))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/equivalence/ -run TestResourceKeyIsVersionIndependent -v`
Expected: FAIL — `undefined: KeyOf` / `undefined: schemaGVK`

- [ ] **Step 3: Write minimal implementation**

```go
package equivalence

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ResourceKey is the version-independent identity of a resource:
// group/kind/namespace/name. Version is deliberately excluded so an API-group
// version bump updates the same logical object rather than reading as add+remove.
type ResourceKey string

// KeyOf computes the version-independent key for an object.
func KeyOf(u *unstructured.Unstructured) ResourceKey {
	gvk := u.GroupVersionKind()
	return ResourceKey(fmt.Sprintf("%s/%s/%s/%s", gvk.Group, gvk.Kind, u.GetNamespace(), u.GetName()))
}

// schemaGVK parses an apiVersion+kind into a GroupVersionKind (test + internal helper).
func schemaGVK(apiVersion, kind string) schema.GroupVersionKind {
	gv, _ := schema.ParseGroupVersion(apiVersion)
	return gv.WithKind(kind)
}

// ObjectSet is a keyed collection of bare objects for comparison.
type ObjectSet map[ResourceKey]*unstructured.Unstructured

// Keys returns the sorted keys for deterministic iteration.
func (s ObjectSet) Keys() []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, string(k))
	}
	sortStrings(out)
	return out
}

func sortStrings(x []string) {
	for i := 1; i < len(x); i++ {
		for j := i; j > 0 && x[j-1] > x[j]; j-- {
			x[j-1], x[j] = x[j], x[j-1]
		}
	}
}

var _ = strings.TrimSpace
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/equivalence/ -run TestResourceKeyIsVersionIndependent -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/equivalence/set.go internal/equivalence/set_test.go
git commit -m "test(equivalence): resource identity key model"
```

- [x] Task 1 complete

---

## Task 2: Canonical normalization

**Files:**
- Create: `internal/equivalence/normalize.go`
- Test: `internal/equivalence/normalize_test.go`

Implements the comparator's normalization: sort maps, drop nulls/empty maps, so ordering/whitespace differences vanish. (Deep-equal in Task 4 relies on this.)

- [ ] **Step 1: Write the failing test**

```go
package equivalence

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestNormalizeDropsEmptyAnnotationsMap(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":        "x",
			"annotations": map[string]interface{}{},
		},
	}}
	Normalize(u)
	md, _, _ := unstructured.NestedMap(u.Object, "metadata")
	if _, ok := md["annotations"]; ok {
		t.Fatal("empty annotations map should be dropped by Normalize")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/equivalence/ -run TestNormalizeDropsEmptyAnnotationsMap -v`
Expected: FAIL — `undefined: Normalize`

- [ ] **Step 3: Write minimal implementation**

```go
package equivalence

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// Normalize canonicalizes an object in place so incidental differences
// (empty maps, nil values) do not cause false mismatches. Map key ordering is
// already normalized by Go map semantics + reflect.DeepEqual, so only empties
// and nils are pruned here.
func Normalize(u *unstructured.Unstructured) {
	u.Object = pruneEmpties(u.Object).(map[string]interface{})
}

func pruneEmpties(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, child := range t {
			pruned := pruneEmpties(child)
			if isEmpty(pruned) {
				delete(t, k)
				continue
			}
			t[k] = pruned
		}
		return t
	case []interface{}:
		for i := range t {
			t[i] = pruneEmpties(t[i])
		}
		return t
	default:
		return v
	}
}

func isEmpty(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case map[string]interface{}:
		return len(t) == 0
	default:
		return false
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/equivalence/ -run TestNormalizeDropsEmptyAnnotationsMap -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/equivalence/normalize.go internal/equivalence/normalize_test.go
git commit -m "test(equivalence): canonical object normalization"
```

- [x] Task 2 complete

---

## Task 3: Allowlist stripping (provenance + operator-internal, never transformation output)

**Files:**
- Create: `internal/equivalence/allowlist.go`
- Test: `internal/equivalence/allowlist_test.go`

Implements spec `equivalence-comparator` → "Allowlist limited to provenance and incidental fields".

- [ ] **Step 1: Write the failing test**

```go
package equivalence

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestStripAllowlistRemovesProvenanceAndInternalLabels(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{
			"name": "x",
			"labels": map[string]interface{}{
				"helm.sh/chart":                            "metal-0.6.30",
				"app.kubernetes.io/managed-by":             "Helm",
				"dual-deployment-operator.cc.sap/owned-by": "abc",
				"dual-deployment-operator.cc.sap/webhook-injector": "metal-operator", // transformation output — MUST survive
				"app":                                      "keep-me",
			},
		},
	}}
	StripAllowlist(u)
	labels, _, _ := unstructured.NestedStringMap(u.Object, "metadata", "labels")
	if _, ok := labels["helm.sh/chart"]; ok {
		t.Error("helm.sh/chart should be stripped")
	}
	if _, ok := labels["dual-deployment-operator.cc.sap/owned-by"]; ok {
		t.Error("owned-by should be stripped")
	}
	if labels["dual-deployment-operator.cc.sap/webhook-injector"] != "metal-operator" {
		t.Error("injector target-label is a transformation output and MUST NOT be stripped")
	}
	if labels["app"] != "keep-me" {
		t.Error("unrelated label must survive")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/equivalence/ -run TestStripAllowlistRemovesProvenanceAndInternalLabels -v`
Expected: FAIL — `undefined: StripAllowlist`

- [ ] **Step 3: Write minimal implementation**

```go
package equivalence

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// allowlistedLabelKeys are provenance/incidental labels expected to differ
// between a Helm-rendered chart and the operator render. This list MUST NOT
// contain any transformation-produced key (e.g. the injector --target-label).
var allowlistedLabelKeys = []string{
	"helm.sh/chart",
	"app.kubernetes.io/managed-by",
	"app.kubernetes.io/version",
	"dual-deployment-operator.cc.sap/origin",
	"dual-deployment-operator.cc.sap/owned-by",
}

// StripAllowlist removes provenance/incidental labels and annotations in place,
// before deep-equal. It never removes a transformation-output field.
func StripAllowlist(u *unstructured.Unstructured) {
	stripKeys(u, "metadata", "labels")
	stripKeys(u, "metadata", "annotations")
}

func stripKeys(u *unstructured.Unstructured, path ...string) {
	m, found, _ := unstructured.NestedMap(u.Object, path...)
	if !found {
		return
	}
	for _, k := range allowlistedLabelKeys {
		delete(m, k)
	}
	if len(m) == 0 {
		unstructured.RemoveNestedField(u.Object, path...)
		return
	}
	_ = unstructured.SetNestedMap(u.Object, m, path...)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/equivalence/ -run TestStripAllowlistRemovesProvenanceAndInternalLabels -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/equivalence/allowlist.go internal/equivalence/allowlist_test.go
git commit -m "test(equivalence): provenance/internal-label allowlist"
```

- [x] Task 3 complete

---

## Task 4: Comparator (deep-equal + per-resource diff report)

**Files:**
- Create: `internal/equivalence/compare.go`
- Test: `internal/equivalence/compare_test.go`

Implements spec `equivalence-comparator` → "Canonical normalization and per-resource comparison" (mismatch, missing/extra reporting).

- [ ] **Step 1: Write the failing test**

```go
package equivalence

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func cm(name, key, val string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"name": name, "namespace": "ns"},
		"data":     map[string]interface{}{key: val},
	}}
}

func TestCompareReportsFieldDiffAndMissing(t *testing.T) {
	golden := ObjectSet{}
	op := ObjectSet{}
	g1 := cm("a", "k", "v1")
	o1 := cm("a", "k", "v2") // field diff
	g2 := cm("b", "k", "v")  // only in golden -> missing on operator side
	golden[KeyOf(g1)] = g1
	golden[KeyOf(g2)] = g2
	op[KeyOf(o1)] = o1

	report := Compare(golden, op)
	if report.Equal() {
		t.Fatal("expected mismatches")
	}
	s := report.String()
	if !strings.Contains(s, "v1/ConfigMap/ns/a") {
		t.Errorf("expected field-diff for a; got:\n%s", s)
	}
	if !strings.Contains(s, "v1/ConfigMap/ns/b") || !strings.Contains(strings.ToLower(s), "missing") {
		t.Errorf("expected missing-on-operator for b; got:\n%s", s)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/equivalence/ -run TestCompareReportsFieldDiffAndMissing -v`
Expected: FAIL — `undefined: Compare`

- [ ] **Step 3: Write minimal implementation**

```go
package equivalence

import (
	"fmt"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Report accumulates per-resource equivalence findings.
type Report struct {
	Mismatched []string // key + human-readable reason
	MissingOp  []string // in golden, absent on operator side
	ExtraOp    []string // on operator side, absent in golden
}

// Equal reports whether the two sides are equivalent.
func (r Report) Equal() bool {
	return len(r.Mismatched) == 0 && len(r.MissingOp) == 0 && len(r.ExtraOp) == 0
}

func (r Report) String() string {
	var b strings.Builder
	for _, m := range r.Mismatched {
		fmt.Fprintf(&b, "MISMATCH %s\n", m)
	}
	for _, m := range r.MissingOp {
		fmt.Fprintf(&b, "MISSING on operator side: %s\n", m)
	}
	for _, e := range r.ExtraOp {
		fmt.Fprintf(&b, "EXTRA on operator side: %s\n", e)
	}
	return b.String()
}

// Compare normalizes + allowlist-strips both sides and deep-equals per resource.
func Compare(golden, op ObjectSet) Report {
	var r Report
	prep := func(u *unstructured.Unstructured) *unstructured.Unstructured {
		StripAllowlist(u)
		Normalize(u)
		return u
	}
	for k, gobj := range golden {
		oobj, ok := op[k]
		if !ok {
			r.MissingOp = append(r.MissingOp, string(k))
			continue
		}
		if !reflect.DeepEqual(prep(gobj).Object, prep(oobj).Object) {
			r.Mismatched = append(r.Mismatched, fmt.Sprintf("%s (fields differ)", k))
		}
	}
	for k := range op {
		if _, ok := golden[k]; !ok {
			r.ExtraOp = append(r.ExtraOp, string(k))
		}
	}
	return r
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/equivalence/ -run TestCompareReportsFieldDiffAndMissing -v`
Expected: PASS

- [ ] **Step 5: Add caBundle-parity test (both sides absent → equal)**

Append to `internal/equivalence/compare_test.go` (spec `equivalence-comparator` → "caBundle parity without special handling"):

```go
func vwc(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "admissionregistration.k8s.io/v1",
		"kind":       "ValidatingWebhookConfiguration",
		"metadata":   map[string]interface{}{"name": name},
		"webhooks": []interface{}{map[string]interface{}{
			"name":         "w",
			"clientConfig": map[string]interface{}{"url": "https://x/y"}, // no caBundle on either side
		}},
	}}
}

func TestCompareCABundleAbsentBothSidesEqual(t *testing.T) {
	golden := ObjectSet{}
	op := ObjectSet{}
	g := vwc("vwc")
	o := vwc("vwc")
	golden[KeyOf(g)] = g
	op[KeyOf(o)] = o
	if r := Compare(golden, op); !r.Equal() {
		t.Fatalf("caBundle-absent VWCs must compare equal; got:\n%s", r.String())
	}
}
```

- [ ] **Step 6: Run both comparator tests to verify they pass**

Run: `go test ./internal/equivalence/ -run 'TestCompareReportsFieldDiffAndMissing|TestCompareCABundleAbsentBothSidesEqual' -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/equivalence/compare.go internal/equivalence/compare_test.go
git commit -m "test(equivalence): per-resource deep-equal comparator + report + caBundle parity"
```

- [x] Task 4 complete

---

## Task 5: Golden classify/unwrap/exclude (identity-gated buckets)

**Files:**
- Create: `internal/equivalence/golden_classify.go`
- Test: `internal/equivalence/golden_classify_test.go`

Implements spec `equivalence-golden-render` → classification, unwrapping, exclusion, and the no-op-safe invariant. Operates on `[]*unstructured.Unstructured` (parsed golden docs) → seed `ObjectSet` + shoot `ObjectSet`.

- [ ] **Step 1: Write the failing test (injector unwrap + non-injector passthrough + MR unwrap + no-op-safe)**

```go
package equivalence

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func obj(apiVersion, kind, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(apiVersion)
	u.SetKind(kind)
	u.SetName(name)
	return u
}

func TestClassifyUnwrapsInjectorConfigMapOnly(t *testing.T) {
	injector := obj("v1", "ConfigMap", "metal-operator-remote-webhook-config")
	_ = unstructured.SetNestedField(injector.Object, "apiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingWebhookConfiguration\nmetadata:\n  name: vwc\n", "data", "webhooks.yaml")
	appCM := obj("v1", "ConfigMap", "dns-record-template") // non-injector, must pass through

	docs := []*unstructured.Unstructured{injector, appCM}
	res := ClassifyGolden(docs, GoldenOpts{ChartFullname: "metal-operator-remote"})

	if _, ok := res.Shoot[ResourceKey("admissionregistration.k8s.io/ValidatingWebhookConfiguration//vwc")]; !ok {
		t.Error("injector ConfigMap must be unwrapped into bare VWC in shoot set")
	}
	if _, ok := res.Shoot[KeyOf(injector)]; ok {
		t.Error("injector ConfigMap wrapper must be discarded")
	}
	if _, ok := res.Seed[KeyOf(appCM)]; !ok {
		t.Error("non-injector ConfigMap must pass through to keep+compare (seed)")
	}
}

func TestClassifyNoInjectorConfigMapIsNoOp(t *testing.T) {
	// boot-operator style: a ConfigMap that is NOT the injector one must be untouched.
	appCM := obj("v1", "ConfigMap", "boot-operator-remote-webhook-config-lookalike")
	res := ClassifyGolden([]*unstructured.Unstructured{appCM}, GoldenOpts{ChartFullname: "boot-operator-remote"})
	if _, ok := res.Seed[KeyOf(appCM)]; !ok {
		t.Error("lookalike ConfigMap (wrong sole-key) must pass through unchanged")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/equivalence/ -run TestClassify -v`
Expected: FAIL — `undefined: ClassifyGolden` / `undefined: GoldenOpts`

- [ ] **Step 3: Write minimal implementation**

```go
package equivalence

import (
	"encoding/base64"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// GoldenOpts drives identity-gated classification.
type GoldenOpts struct {
	ChartFullname string           // e.g. "metal-operator-remote"
	Exclusions    []ExclusionEntry // enumerated kind+name to drop (render emits no equivalent)
}

// ExclusionEntry identifies an object to exclude by kind AND name (never substring).
type ExclusionEntry struct {
	Kind string
	Name string
}

// Classified is the golden set partitioned after unwrap/exclude.
type Classified struct {
	Seed  ObjectSet
	Shoot ObjectSet
}

// ClassifyGolden buckets rendered docs. Every manipulation is identity-gated:
// unrecognized docs default to keep-and-compare (never silently dropped).
func ClassifyGolden(docs []*unstructured.Unstructured, opts GoldenOpts) Classified {
	out := Classified{Seed: ObjectSet{}, Shoot: ObjectSet{}}
	for _, d := range docs {
		if isExcluded(d, opts.Exclusions) {
			continue
		}
		switch {
		case isManagedResource(d):
			// MR itself is discarded; its Secret payload is unwrapped elsewhere
			// in the render (paired-Secret walk). See unwrapManagedResources.
			continue
		case isInjectorConfigMap(d, opts.ChartFullname):
			for _, wh := range decodeWebhooks(d) {
				out.Shoot[KeyOf(wh)] = wh
			}
		default:
			out.Seed[KeyOf(d)] = d // default: keep+compare (seed bucket)
		}
	}
	return out
}

func isExcluded(u *unstructured.Unstructured, ex []ExclusionEntry) bool {
	for _, e := range ex {
		if u.GetKind() == e.Kind && u.GetName() == e.Name {
			return true
		}
	}
	return false
}

func isManagedResource(u *unstructured.Unstructured) bool {
	return u.GetKind() == "ManagedResource" &&
		u.GroupVersionKind().Group == "resources.gardener.cloud"
}

// isInjectorConfigMap matches ONLY name==<fullname>-webhook-config AND sole data
// key "webhooks.yaml". Never matches on kind==ConfigMap alone.
func isInjectorConfigMap(u *unstructured.Unstructured, fullname string) bool {
	if u.GetKind() != "ConfigMap" || u.GetName() != fullname+"-webhook-config" {
		return false
	}
	data, found, _ := unstructured.NestedMap(u.Object, "data")
	if !found || len(data) != 1 {
		return false
	}
	_, ok := data["webhooks.yaml"]
	return ok
}

func decodeWebhooks(u *unstructured.Unstructured) []*unstructured.Unstructured {
	raw, _, _ := unstructured.NestedString(u.Object, "data", "webhooks.yaml")
	return splitYAMLDocs([]byte(raw))
}

// splitYAMLDocs parses a multi-doc YAML string into objects.
func splitYAMLDocs(b []byte) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, doc := range splitOnYAMLSeparator(b) {
		if len(doc) == 0 {
			continue
		}
		m := map[string]interface{}{}
		if err := yaml.Unmarshal(doc, &m); err != nil || len(m) == 0 {
			continue
		}
		out = append(out, &unstructured.Unstructured{Object: m})
	}
	return out
}

var _ = base64.StdEncoding // used by the MR-Secret unwrap in Task 6
```

Add `splitOnYAMLSeparator` (bytes split on `\n---\n`) in the same file:

```go
import "bytes"

func splitOnYAMLSeparator(b []byte) [][]byte {
	return bytes.Split(b, []byte("\n---\n"))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/equivalence/ -run TestClassify -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/equivalence/golden_classify.go internal/equivalence/golden_classify_test.go
git commit -m "test(equivalence): identity-gated golden classify/unwrap/exclude"
```

- [x] Task 5 complete

---

## Task 6: ManagedResource + Secret payload unwrap

**Files:**
- Modify: `internal/equivalence/golden_classify.go`
- Test: `internal/equivalence/golden_mr_test.go`

Implements spec `equivalence-golden-render` → "ManagedResource-wrapped CRDs/RBAC are unwrapped" (follow `spec.secretRefs` → paired `Secret` → base64-decode `data["objects.yaml"]`). This is a whole-stream pass (needs the Secrets), so it runs before `ClassifyGolden` and rewrites the doc list.

- [ ] **Step 1: Write the failing test**

```go
package equivalence

import (
	"encoding/base64"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestUnwrapManagedResourcesEmitsBareObjects(t *testing.T) {
	crdYAML := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: endpoints.metal\n"
	sec := obj("v1", "Secret", "mr-crd-endpoints")
	_ = unstructured.SetNestedField(sec.Object, base64.StdEncoding.EncodeToString([]byte(crdYAML)), "data", "objects.yaml")
	mr := obj("resources.gardener.cloud/v1alpha1", "ManagedResource", "mr-crd-endpoints")
	_ = unstructured.SetNestedSlice(mr.Object, []interface{}{map[string]interface{}{"name": "mr-crd-endpoints"}}, "spec", "secretRefs")

	docs := UnwrapManagedResources([]*unstructured.Unstructured{mr, sec})

	var foundCRD bool
	for _, d := range docs {
		if d.GetKind() == "CustomResourceDefinition" && d.GetName() == "endpoints.metal" {
			foundCRD = true
		}
		if d.GetKind() == "ManagedResource" || (d.GetKind() == "Secret" && d.GetName() == "mr-crd-endpoints") {
			t.Errorf("wrapper %s/%s must be discarded", d.GetKind(), d.GetName())
		}
	}
	if !foundCRD {
		t.Error("bare CRD must be emitted from the MR's paired Secret")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/equivalence/ -run TestUnwrapManagedResources -v`
Expected: FAIL — `undefined: UnwrapManagedResources`

- [ ] **Step 3: Write minimal implementation (append to golden_classify.go)**

```go
// UnwrapManagedResources replaces each ManagedResource + its paired Secret(s)
// with the bare objects encoded in Secret.data["objects.yaml"]. Docs that are
// not MRs or MR-referenced Secrets pass through unchanged (identity-gated).
func UnwrapManagedResources(docs []*unstructured.Unstructured) []*unstructured.Unstructured {
	secretsByName := map[string]*unstructured.Unstructured{}
	for _, d := range docs {
		if d.GetKind() == "Secret" {
			secretsByName[d.GetName()] = d
		}
	}
	referenced := map[string]bool{}
	var out []*unstructured.Unstructured
	for _, d := range docs {
		if !isManagedResource(d) {
			continue // handled in second pass to preserve order predictably
		}
		refs, _, _ := unstructured.NestedSlice(d.Object, "spec", "secretRefs")
		for _, r := range refs {
			m, ok := r.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			sec, ok := secretsByName[name]
			if !ok {
				continue
			}
			referenced[name] = true
			enc, _, _ := unstructured.NestedString(sec.Object, "data", "objects.yaml")
			raw, err := base64.StdEncoding.DecodeString(enc)
			if err != nil {
				continue
			}
			out = append(out, splitYAMLDocs(raw)...)
		}
	}
	// Pass through everything that is neither an MR nor an MR-referenced Secret.
	for _, d := range docs {
		if isManagedResource(d) {
			continue
		}
		if d.GetKind() == "Secret" && referenced[d.GetName()] {
			continue
		}
		out = append(out, d)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/equivalence/ -run TestUnwrapManagedResources -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/equivalence/golden_classify.go internal/equivalence/golden_mr_test.go
git commit -m "test(equivalence): unwrap ManagedResource+Secret payloads to bare objects"
```

- [x] Task 6 complete

---

## Task 7: Golden render via GHCR pull + helm template

**Files:**
- Create: `internal/equivalence/golden_render.go`
- Test: `internal/equivalence/golden_render_test.go` (network — default job)

Implements spec `equivalence-golden-render` → "Golden chart render from GHCR" (pull packaged chart, `helm template` with `values.yaml` + overlay, parse to docs). Uses helm SDK `registry.Client` + `action.Pull` (OCI) then `action.Install{ClientOnly, DryRun, IncludeCRDs}` to template.

- [ ] **Step 1: Write the failing network test (metal-operator)**

```go
package equivalence

import (
	"context"
	"testing"
)

func TestGoldenRenderMetalOperatorPulls(t *testing.T) {
	docs, err := RenderGolden(context.Background(), GoldenRenderReq{
		OCIRef:  "oci://ghcr.io/sapcc/helm-charts/charts/metal-operator-remote",
		Version: "0.6.30",
	})
	if err != nil {
		t.Fatalf("golden render failed: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("expected rendered documents from metal-operator-remote")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/equivalence/ -run TestGoldenRenderMetalOperatorPulls -v`
Expected: FAIL — `undefined: RenderGolden` / `undefined: GoldenRenderReq`

- [ ] **Step 3: Write minimal implementation**

```go
package equivalence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// GoldenRenderReq describes a golden pull+template.
type GoldenRenderReq struct {
	OCIRef     string   // oci://ghcr.io/sapcc/helm-charts/charts/<op>-remote
	Version    string   // pinned chart version
	ValuesYAML [][]byte // wrapper values.yaml + representative per-cluster overlay
	Namespace  string   // release namespace for templating
}

// RenderGolden pulls the packaged chart from GHCR and templates it, returning
// parsed documents. Fails loudly on pull/template error (never skips).
func RenderGolden(ctx context.Context, req GoldenRenderReq) ([]*unstructured.Unstructured, error) {
	tmp, err := os.MkdirTemp("", "ddo-golden-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	settings := cli.New()
	regClient, err := registry.NewClient()
	if err != nil {
		return nil, fmt.Errorf("golden: registry client: %w", err)
	}
	cfg := &action.Configuration{RegistryClient: regClient}

	pull := action.NewPullWithOpts(action.WithConfig(cfg))
	pull.Settings = settings
	pull.DestDir = tmp
	pull.Version = req.Version
	if _, err := pull.Run(req.OCIRef); err != nil {
		return nil, fmt.Errorf("golden: pull %s@%s: %w", req.OCIRef, req.Version, err)
	}

	tgz, err := firstTgz(tmp)
	if err != nil {
		return nil, err
	}
	ch, err := loader.Load(tgz)
	if err != nil {
		return nil, fmt.Errorf("golden: load chart: %w", err)
	}

	vals, err := mergeValuesYAML(req.ValuesYAML)
	if err != nil {
		return nil, err
	}

	inst := action.NewInstall(cfg)
	inst.DryRun = true
	inst.ClientOnly = true
	inst.IncludeCRDs = true
	inst.ReleaseName = ch.Name()
	inst.Namespace = req.Namespace
	rel, err := inst.Run(ch, vals)
	if err != nil {
		return nil, fmt.Errorf("golden: template %s@%s: %w", req.OCIRef, req.Version, err)
	}
	return splitYAMLDocs([]byte(rel.Manifest)), nil
}

func firstTgz(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tgz") {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("golden: no .tgz pulled into %s", dir)
}
```

Add `mergeValuesYAML` (merge the values byte-slices into one `map[string]interface{}`) using `helm.sh/helm/v3/pkg/chartutil.CoalesceTables` or `sigs.k8s.io/yaml`:

```go
import "sigs.k8s.io/yaml"

func mergeValuesYAML(docs [][]byte) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	for _, d := range docs {
		m := map[string]interface{}{}
		if err := yaml.Unmarshal(d, &m); err != nil {
			return nil, fmt.Errorf("golden: parse values: %w", err)
		}
		out = deepMerge(out, m)
	}
	return out, nil
}

func deepMerge(base, over map[string]interface{}) map[string]interface{} {
	for k, v := range over {
		if bv, ok := base[k].(map[string]interface{}); ok {
			if ov, ok := v.(map[string]interface{}); ok {
				base[k] = deepMerge(bv, ov)
				continue
			}
		}
		base[k] = v
	}
	return base
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/equivalence/ -run TestGoldenRenderMetalOperatorPulls -v`
Expected: PASS (requires GHCR egress)

- [ ] **Step 5: Commit**

```bash
git add internal/equivalence/golden_render.go internal/equivalence/golden_render_test.go
git commit -m "test(equivalence): golden GHCR pull + helm template render"
```

- [x] Task 7 complete

---

## Task 8: Operator-side capture (render + transform, no delivery)

**Files:**
- Create: `internal/equivalence/operator_capture.go`
- Test: `internal/equivalence/operator_capture_test.go`

Implements spec `equivalence-operator-capture`. Drives `source.From → Render(seed/shoot) → transform.Build → Apply`, returns two `ObjectSet`s. No reconciler/envtest/applier.

- [ ] **Step 1: Write the failing test (fake ChartLoader/RootResolver via source.Deps)**

```go
package equivalence

import (
	"context"
	"testing"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source"
)

func TestCaptureOperatorReturnsTwoSets(t *testing.T) {
	// Minimal helm CR pointing at a fake loader that returns a one-Deployment chart.
	cr := &v1alpha1.DualDeploymentOperator{}
	cr.Namespace = "ns"
	cr.Spec.ShootNamespace = "shoot-ns"
	cr.Spec.Source = v1alpha1.Source{Helm: &v1alpha1.HelmSource{Repo: "x", Name: "demo", Version: "0"}}

	deps := source.Deps{ChartLoader: fakeChartLoader(t), RootResolver: nil}
	seed, shoot, err := CaptureOperator(context.Background(), cr, deps)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(seed) == 0 && len(shoot) == 0 {
		t.Fatal("expected at least one captured resource")
	}
}
```

(`fakeChartLoader` reuses the existing test fake pattern from `internal/source` — a loader returning a `*chart.Chart` from an in-test template. Copy the smallest existing fake from `internal/source/*_test.go`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/equivalence/ -run TestCaptureOperatorReturnsTwoSets -v`
Expected: FAIL — `undefined: CaptureOperator`

- [ ] **Step 3: Write minimal implementation**

```go
package equivalence

import (
	"context"
	"fmt"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/transform"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// CaptureOperator drives the render+transform pipeline for both modes and returns
// the seed and shoot object sets, captured before the delivery layer.
func CaptureOperator(ctx context.Context, cr *v1alpha1.DualDeploymentOperator, deps source.Deps) (ObjectSet, ObjectSet, error) {
	src, err := source.From(cr.Spec.Source, deps)
	if err != nil {
		return nil, nil, fmt.Errorf("capture: source.From: %w", err)
	}
	transforms, err := transform.Build(cr.Spec.Transformations)
	if err != nil {
		return nil, nil, fmt.Errorf("capture: transform.Build: %w", err)
	}
	seed, err := renderMode(ctx, src, source.ModeSeed, cr.Namespace, transforms)
	if err != nil {
		return nil, nil, fmt.Errorf("capture: seed: %w", err)
	}
	shoot, err := renderMode(ctx, src, source.ModeShoot, cr.Spec.ShootNamespace, transforms)
	if err != nil {
		return nil, nil, fmt.Errorf("capture: shoot: %w", err)
	}
	return seed, shoot, nil
}

func renderMode(ctx context.Context, src source.Source, mode source.Mode, ns string, transforms []transform.Transformation) (ObjectSet, error) {
	ms, err := src.Render(ctx, mode, ns)
	if err != nil {
		return nil, err
	}
	for _, t := range transforms {
		ms, err = t.Apply(ms)
		if err != nil {
			return nil, err
		}
	}
	set := ObjectSet{}
	for i := range ms {
		u := ms[i].Unstructured
		set[KeyOf(u)] = u
	}
	return set, nil
}

var _ = unstructured.Unstructured{}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/equivalence/ -run TestCaptureOperatorReturnsTwoSets -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/equivalence/operator_capture.go internal/equivalence/operator_capture_test.go
git commit -m "test(equivalence): operator render+transform capture (no delivery)"
```

- [x] Task 8 complete

---

## Task 9: Fixture model + metal-operator fixture

**Files:**
- Create: `internal/equivalence/fixture.go`
- Create: `testdata/fixtures/metal-operator/fixture.yaml`
- Create: `testdata/fixtures/metal-operator/cr.yaml`
- Create: `testdata/fixtures/metal-operator/overlay-values.yaml`
- Test: `internal/equivalence/fixture_test.go`

Implements spec `equivalence-operator-capture` → "Fixture-driven capture inputs" (pinned value baseline; CR bridges upstream-enabled vs wrapper-disabled).

- [ ] **Step 1: Write the failing test (load + shape)**

```go
package equivalence

import "testing"

func TestLoadFixtureMetalOperator(t *testing.T) {
	f, err := LoadFixture("../../testdata/fixtures/metal-operator")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if f.ChartVersion != "0.6.30" {
		t.Errorf("chart version = %q, want 0.6.30", f.ChartVersion)
	}
	if f.ChartFullname != "metal-operator-remote" {
		t.Errorf("fullname = %q", f.ChartFullname)
	}
	if f.CR == nil || f.CR.Spec.Source.Helm == nil {
		t.Error("fixture CR must have a helm source")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/equivalence/ -run TestLoadFixtureMetalOperator -v`
Expected: FAIL — `undefined: LoadFixture` and missing fixture files

- [ ] **Step 3a: Create `testdata/fixtures/metal-operator/fixture.yaml`**

```yaml
operator: metal-operator
chartFullname: metal-operator-remote
ociRef: oci://ghcr.io/sapcc/helm-charts/charts/metal-operator-remote
chartVersion: "0.6.30"
overlayValues: overlay-values.yaml
cr: cr.yaml
# Objects the operator render emits no equivalent for (kind+name, justified).
exclusions:
  - kind: ConfigMap
    name: owner-info
    reason: "owner-info subchart output; operator render emits no equivalent"
```

- [ ] **Step 3b: Create `testdata/fixtures/metal-operator/overlay-values.yaml`**

```yaml
# Representative per-cluster overlay (cc/kube-secrets shape for one chosen shoot).
controllerManager:
  manager:
    env:
      KUBERNETES_SERVICE_HOST: "api.m-qa-de-1.internal"
```

- [ ] **Step 3c: Create `testdata/fixtures/metal-operator/cr.yaml`** (bridges upstream-enabled)

```yaml
apiVersion: dual-deployment-operator.cc.sap/v1alpha1
kind: DualDeploymentOperator
metadata:
  name: metal-operator-remote
  namespace: shoot--cp--m-qa-de-1
spec:
  shootNamespace: kube-system
  source:
    helm:
      repo: oci://ghcr.io/ironcore-dev/charts
      name: metal-operator
      version: 0.6.2-crds
      shootValues:
        rbac: {enable: true}
        crd: {enable: true}
        webhook: {enable: true}
      seedValues:
        controllerManager: {enable: true}
  shootAccess:
    secretName: metal-operator-remote-kubeconfig
    server: https://api.m-qa-de-1.internal
  transformations: []
```

- [ ] **Step 3d: Write `LoadFixture` in `internal/equivalence/fixture.go`**

```go
package equivalence

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"sigs.k8s.io/yaml"
)

// Fixture pins one operator's equivalence inputs.
type Fixture struct {
	Operator      string           `json:"operator"`
	ChartFullname string           `json:"chartFullname"`
	OCIRef        string           `json:"ociRef"`
	ChartVersion  string           `json:"chartVersion"`
	OverlayValues string           `json:"overlayValues"`
	CRFile        string           `json:"cr"`
	Exclusions    []ExclusionEntry `json:"exclusions"`

	CR         *v1alpha1.DualDeploymentOperator `json:"-"`
	OverlayRaw []byte                           `json:"-"`
	dir        string
}

// LoadFixture reads fixture.yaml + the referenced CR and overlay.
func LoadFixture(dir string) (*Fixture, error) {
	b, err := os.ReadFile(filepath.Join(dir, "fixture.yaml"))
	if err != nil {
		return nil, err
	}
	var f Fixture
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("fixture: parse: %w", err)
	}
	f.dir = dir

	crBytes, err := os.ReadFile(filepath.Join(dir, f.CRFile))
	if err != nil {
		return nil, err
	}
	cr := &v1alpha1.DualDeploymentOperator{}
	if err := yaml.Unmarshal(crBytes, cr); err != nil {
		return nil, fmt.Errorf("fixture: parse cr: %w", err)
	}
	f.CR = cr

	f.OverlayRaw, err = os.ReadFile(filepath.Join(dir, f.OverlayValues))
	if err != nil {
		return nil, err
	}
	return &f, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/equivalence/ -run TestLoadFixtureMetalOperator -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/equivalence/fixture.go internal/equivalence/fixture_test.go testdata/fixtures/metal-operator/
git commit -m "test(equivalence): fixture model + metal-operator fixture"
```

- [x] Task 9 complete

---

## Task 10: End-to-end harness + metal-operator gating subtest

**Files:**
- Create: `internal/equivalence/equivalence_test.go` (the gating suite)
- Modify: `internal/equivalence/golden_render.go` if a values accessor is needed

Wires golden (Task 5/6/7) + operator (Task 8) + comparator (Task 4) through the fixture (Task 9). Implements spec `equivalence-comparator` → "Per-operator equivalence subtests gate CI".

- [ ] **Step 1: Write the failing gating test**

```go
package equivalence

import (
	"context"
	"testing"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source"
)

func TestEquivalence(t *testing.T) {
	operators := []string{"metal-operator"} // fanned out in Task 11
	for _, op := range operators {
		t.Run(op, func(t *testing.T) {
			f, err := LoadFixture("../../testdata/fixtures/" + op)
			if err != nil {
				t.Fatalf("fixture: %v", err)
			}

			// Golden: pull wrapper values.yaml from the chart + overlay, render, classify.
			goldenDocs, err := RenderGolden(context.Background(), GoldenRenderReq{
				OCIRef:     f.OCIRef,
				Version:    f.ChartVersion,
				ValuesYAML: [][]byte{f.OverlayRaw}, // wrapper values.yaml is chart-default; overlay layered
				Namespace:  f.CR.Namespace,
			})
			if err != nil {
				t.Fatalf("golden render: %v", err)
			}
			goldenDocs = UnwrapManagedResources(goldenDocs)
			golden := ClassifyGolden(goldenDocs, GoldenOpts{ChartFullname: f.ChartFullname, Exclusions: f.Exclusions})

			// Operator: real source fetch through the production loaders.
			deps := source.Deps{ChartLoader: source.NewHelmLoader(), RootResolver: source.NewGitResolver()}
			opSeed, opShoot, err := CaptureOperator(context.Background(), f.CR, deps)
			if err != nil {
				t.Fatalf("operator capture: %v", err)
			}

			seedReport := Compare(golden.Seed, opSeed)
			shootReport := Compare(golden.Shoot, opShoot)
			if !seedReport.Equal() {
				t.Errorf("seed mismatch:\n%s", seedReport.String())
			}
			if !shootReport.Equal() {
				t.Errorf("shoot mismatch:\n%s", shootReport.String())
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/equivalence/ -run TestEquivalence/metal-operator -v`
Expected: FAIL — mismatches reported (fixture bridging not yet perfect) OR a wiring error. Read the per-resource report; this is the triage entry point.

- [ ] **Step 3: Iterate fixture + allowlist to green (run-and-triage)**

Adjust `testdata/fixtures/metal-operator/cr.yaml` (bridging values), `overlay-values.yaml`, and `fixture.yaml` `exclusions` based on the residual per-resource diff. For each residual difference, decide: (a) a fixture/bridging fix (config mismatch), (b) a provenance/incidental field → add to `allowlistedLabelKeys` in `allowlist.go` with a comment, or (c) a real operator/chart divergence → a genuine finding to record. NEVER allowlist a transformation output.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/equivalence/ -run TestEquivalence/metal-operator -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/equivalence/equivalence_test.go internal/equivalence/allowlist.go testdata/fixtures/metal-operator/
git commit -m "test(equivalence): metal-operator end-to-end equivalence subtest (green)"
```

- [x] Task 10 complete

---

## Task 11: Fan out to the remaining four operators

**Files:**
- Create: `testdata/fixtures/boot-operator/{fixture.yaml,cr.yaml,overlay-values.yaml}`
- Create: `testdata/fixtures/argora-operator/{...}`
- Create: `testdata/fixtures/khalkeon/{...}`
- Create: `testdata/fixtures/ipam-capi/{...}`
- Modify: `internal/equivalence/equivalence_test.go` (operators slice)

Pinned versions: `boot-operator-remote` `0.4.21`, `argora-operator-remote` `0.0.60`, `khalkeon-remote` `0.1.1`, `ipam-capi-remote` `1.2.31`. boot/argora/khalkeon have NO injector ConfigMap (verifies the no-op-safe path); ipam-capi is a kustomize source (CR uses `spec.source.kustomize` + `patch` transforms per design §9.4).

- [ ] **Step 1: Add boot-operator fixture (no webhooks; filterKinds only)**

Create `testdata/fixtures/boot-operator/fixture.yaml`:

```yaml
operator: boot-operator
chartFullname: boot-operator-remote
ociRef: oci://ghcr.io/sapcc/helm-charts/charts/boot-operator-remote
chartVersion: "0.4.21"
overlayValues: overlay-values.yaml
cr: cr.yaml
exclusions: []
```

Create `testdata/fixtures/boot-operator/cr.yaml` (helm source, `filterKinds: [Service]` transform; enable upstream rbac/crd) and `overlay-values.yaml` (minimal). Then extend the operators slice:

```go
operators := []string{"metal-operator", "boot-operator"}
```

- [ ] **Step 2: Run boot-operator subtest**

Run: `go test ./internal/equivalence/ -run TestEquivalence/boot-operator -v`
Expected: FAIL first, then triage fixture to PASS

- [ ] **Step 3: Add argora-operator + khalkeon fixtures (same pattern), extend slice**

Create both fixture dirs (versions `0.0.60`, `0.1.1`); CRs helm-source with their `filterKinds` transforms (argora: Service, ConfigMap, Secret; khalkeon: Service). Extend slice to include both. Run each subtest, triage to green.

Run: `go test ./internal/equivalence/ -run 'TestEquivalence/(argora-operator|khalkeon)' -v`
Expected: PASS after triage

- [ ] **Step 4: Add ipam-capi fixture (kustomize source)**

Create `testdata/fixtures/ipam-capi/fixture.yaml` (version `1.2.31`) and `cr.yaml` with `spec.source.kustomize` (url with pinned `?ref=`, `seedPath`/`shootPath`) plus `patch` transforms carrying image tag + `kubernetesServiceHost` per design §9.4. Extend slice to all five. Run and triage — this is the highest-signal case (live kustomize vs frozen helmify snapshot).

Run: `go test ./internal/equivalence/ -run TestEquivalence -v`
Expected: PASS for all five subtests

- [ ] **Step 5: Commit**

```bash
git add internal/equivalence/equivalence_test.go testdata/fixtures/
git commit -m "test(equivalence): fan out equivalence subtests to all five operators"
```

- [ ] Task 11 complete

---

## Task 12: Verify full suite + CI gating shape

**Files:**
- Verify only (no new code unless a gap surfaces)

Confirms spec `equivalence-comparator` → "Per-operator equivalence subtests gate CI" (default `Test` job, no build tag/env gate) and offline comparator unit tests (Tasks 1-6) coexist.

- [ ] **Step 1: Run the whole equivalence package**

Run: `go test ./internal/equivalence/... -v`
Expected: PASS — all five gating subtests + all offline unit tests

- [ ] **Step 2: Confirm no build tag / env gate**

Run: `grep -rn "//go:build\|t.Skip\|os.Getenv" internal/equivalence/`
Expected: no build tag and no network-gating `t.Skip`/env check (offline unit tests + network gating subtests all run in the default job)

- [ ] **Step 3: Confirm offline unit tests run without network**

Run: `go test ./internal/equivalence/ -run 'TestResourceKey|TestNormalize|TestStripAllowlist|TestCompare|TestClassify|TestUnwrap|TestLoadFixture' -v`
Expected: PASS with no network access

- [ ] **Step 4: Full project gate**

Run: `make check`
Expected: build + lint + tests green (equivalence tests included in the default test job)

- [ ] **Step 5: Commit (if any wiring adjustments were needed)**

```bash
git add -A
git commit -m "test(equivalence): verify full suite runs in default job, no gate"
```

- [ ] Task 12 complete
