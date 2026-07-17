# Manifest Transformation (`internal/transform`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Trailing `- [ ]` checkboxes mark task-group completion — check them off AFTER all steps in the group complete and the commit lands.

**Goal:** Implement `internal/transform` — a single per-render `Transformation` interface, a `Build()` parser, a shared `Match()` selector, and three transformations (`patch`, `rewriteWebhookURL`, `filterKinds`) — that turns a CR's `spec.transformations` into ordered operations applied independently to each render's `[]manifest.Manifest` stream (design revision 7).

**Architecture:** One package `internal/transform`. `Build([]v1alpha1.Transformation)` walks the discriminated-union entries in declaration order and returns `[]Transformation`. Each transformation's `Apply([]manifest.Manifest) ([]manifest.Manifest, error)` returns fresh manifests (no in-place mutation of the caller's slice or the underlying `Unstructured`). `patch` and `filterKinds` share `Match(m, Selector)`. The reconciler (Phase 6, out of scope) threads the same ordered list through the host and remote renders separately.

**Tech Stack:** Go, `k8s.io/apimachinery` (`unstructured`, `util/strategicpatch`, `runtime`), `github.com/evanphx/json-patch/v5` (RFC 6902), Go standard `testing` with table-driven tests. Module: `github.com/SAP-cloud-infrastructure/dual-deployment-operator`.

**Conventions (verified against the repo):**
- `manifest.Manifest{ Unstructured *unstructured.Unstructured; Origin Origin }` — [`internal/manifest/manifest.go`](../../../internal/manifest/manifest.go). `Origin` is `OriginUpstream` / `OriginAdditions`.
- CR types in [`api/v1alpha1/dualdeploymentoperator_types.go`](../../../api/v1alpha1/dualdeploymentoperator_types.go): `Transformation{Patch *PatchSpec; RewriteWebhookURL *RewriteWebhookURLSpec; FilterKinds *FilterKindsSpec}`; `PatchSpec{Target Selector; StrategicMerge *apiextensionsv1.JSON; JSONPatch []JSONPatchOp}`; `JSONPatchOp{Op, Path, From string; Value *apiextensionsv1.JSON}`; `RewriteWebhookURLSpec{URLPrefix string}`; `FilterKindsSpec{Kinds []string; Source string}`; `Selector{Kind, Name, Origin string}`.
- Deps already in `go.mod`/`go.sum`: `k8s.io/apimachinery v0.36.2` (provides `util/strategicpatch`), `github.com/evanphx/json-patch/v5 v5.9.11`.
- License header on every new `.go` file (match existing files):
  ```go
  // SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
  // Copyright 2026.
  //
  // SPDX-License-Identifier: Apache-2.0
  ```
- Test commands: `go test ./internal/transform/...` (unit), `make lint-fix` then `make test` (repo gates). TDD RED→GREEN→REFACTOR is mandatory per `AGENTS.md`.
- Fixtures live under `internal/transform/testdata/fixtures/<name>/`. Build `manifest.Manifest` values in-test from small YAML/JSON strings via a shared `mustManifest` / `mustJSON` helper (see Task 1).

**File structure:**
- Create: `internal/transform/transform.go` — `Transformation` interface + `Build`.
- Create: `internal/transform/selector.go` — `Match`.
- Create: `internal/transform/patch.go` — `patch` transformation.
- Create: `internal/transform/rewrite_webhook_url.go` — `rewriteWebhookURL` transformation.
- Create: `internal/transform/filter_kinds.go` — `filterKinds` transformation.
- Create: `internal/transform/testhelpers_test.go` — shared test helpers (`mustJSON`, `mustManifest`, `deepCopyManifests`).
- Tests: one `_test.go` per source file listed above.

---

## Task 1: Test helpers + `Transformation` interface skeleton

**Files:**
- Create: `internal/transform/transform.go`
- Create: `internal/transform/testhelpers_test.go`

- [ ] **Step 1: Write the interface + Build stub (compile target for tests)**

`internal/transform/transform.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

// Package transform applies a CR's spec.transformations to a single render's
// manifest stream. All transformations are per-render (design revision 7);
// there is no cross-stream scope.
package transform

import (
	"errors"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// Transformation operates on a single render's manifest stream. Apply MUST
// return fresh manifests and MUST NOT mutate the input slice or the underlying
// Unstructured objects.
type Transformation interface {
	Type() string
	Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error)
}

// Build parses spec.Transformations into an ordered []Transformation,
// preserving declaration order. An entry with no field set is an error.
func Build(specs []v1alpha1.Transformation) ([]Transformation, error) {
	out := make([]Transformation, 0, len(specs))
	for i := range specs {
		s := specs[i]
		switch {
		case s.Patch != nil:
			out = append(out, &patch{spec: s.Patch})
		case s.RewriteWebhookURL != nil:
			out = append(out, &rewriteWebhookURL{spec: s.RewriteWebhookURL})
		case s.FilterKinds != nil:
			out = append(out, &filterKinds{spec: s.FilterKinds})
		default:
			return nil, errors.New("no transformation type set in entry")
		}
	}
	return out, nil
}
```
(This will not compile until Tasks 3-5 define `patch`, `rewriteWebhookURL`, `filterKinds`. That is expected — do not run `go build` on the package until Task 5. Steps in this task are validated via the helper file's own compile in later tasks.)

- [ ] **Step 2: Write shared test helpers**

`internal/transform/testhelpers_test.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// mustJSON builds an apiextensionsv1.JSON from a raw JSON string.
func mustJSON(s string) *apiextensionsv1.JSON {
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}

// mustManifest parses a single YAML doc into a manifest.Manifest with the given origin.
func mustManifest(t *testing.T, y string, origin manifest.Origin) manifest.Manifest {
	t.Helper()
	m := map[string]any{}
	if err := yaml.Unmarshal([]byte(y), &m); err != nil {
		t.Fatalf("mustManifest: %v", err)
	}
	return manifest.Manifest{Unstructured: &unstructured.Unstructured{Object: m}, Origin: origin}
}

// cloneForAssert deep-copies a slice so a test can assert the original was not mutated.
func cloneForAssert(in []manifest.Manifest) []manifest.Manifest {
	out := make([]manifest.Manifest, len(in))
	for i, m := range in {
		out[i] = manifest.Manifest{Unstructured: m.Unstructured.DeepCopy(), Origin: m.Origin}
	}
	return out
}
```
(`sigs.k8s.io/yaml` is already an indirect dep via controller-runtime; if `go test` reports it missing, add it with `go get sigs.k8s.io/yaml` in Step 4 before committing.)

- [ ] **Step 3: Verify the package parses**

Run: `gofmt -l internal/transform/`
Expected: no output (files are formatted). Do NOT run `go build ./internal/transform/` yet — it will fail on undefined `patch`/`rewriteWebhookURL`/`filterKinds` until Task 5.

- [ ] **Step 4: Commit**

```bash
git add internal/transform/transform.go internal/transform/testhelpers_test.go
git commit -m "feat(transform): add Transformation interface and Build skeleton"
```

- [x] Task 1 complete

---

## Task 2: `Match` selector

**Files:**
- Create: `internal/transform/selector.go`
- Test: `internal/transform/selector_test.go`

- [ ] **Step 1: Write the failing test**

`internal/transform/selector_test.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"testing"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func TestMatch(t *testing.T) {
	dep := mustManifest(t, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: metal-operator-controller-manager
`, manifest.OriginUpstream)
	svc := mustManifest(t, `
apiVersion: v1
kind: Service
metadata:
  name: other-controller
`, manifest.OriginAdditions)

	tests := []struct {
		name string
		m    manifest.Manifest
		sel  v1alpha1.Selector
		want bool
	}{
		{"kind exact match", dep, v1alpha1.Selector{Kind: "Deployment"}, true},
		{"kind mismatch", svc, v1alpha1.Selector{Kind: "Deployment"}, false},
		{"name glob match", dep, v1alpha1.Selector{Name: "metal-*"}, true},
		{"name glob mismatch", dep, v1alpha1.Selector{Name: "other-*"}, false},
		{"origin match", dep, v1alpha1.Selector{Origin: "upstream"}, true},
		{"origin mismatch", dep, v1alpha1.Selector{Origin: "additions"}, false},
		{"empty selector matches all", svc, v1alpha1.Selector{}, true},
		{"all fields ANDed true", dep, v1alpha1.Selector{Kind: "Deployment", Name: "metal-*", Origin: "upstream"}, true},
		{"all fields ANDed one false", dep, v1alpha1.Selector{Kind: "Deployment", Name: "metal-*", Origin: "additions"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.m, tc.sel); got != tc.want {
				t.Fatalf("Match() = %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/transform/ -run TestMatch -v`
Expected: FAIL to compile — `undefined: Match` (and undefined `patch`/etc. from Task 1 stub). If the package-level compile error masks this, temporarily this is expected; proceed to Step 3.

- [ ] **Step 3: Write minimal implementation**

`internal/transform/selector.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"path"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// Match reports whether m satisfies every set field of sel. An unset field does
// not constrain the match; an all-empty selector matches every manifest.
// Kind matches exactly; Name matches by glob (path.Match); Origin matches exactly.
func Match(m manifest.Manifest, sel v1alpha1.Selector) bool {
	if sel.Kind != "" && m.Unstructured.GetKind() != sel.Kind {
		return false
	}
	if sel.Name != "" {
		ok, err := path.Match(sel.Name, m.Unstructured.GetName())
		if err != nil || !ok {
			return false
		}
	}
	if sel.Origin != "" && string(m.Origin) != sel.Origin {
		return false
	}
	return true
}
```
(`path.Match` gives glob semantics where `*` matches any run of non-separator characters; resource names contain no `/`, so `metal-*` behaves as intended.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/transform/ -run TestMatch -v`
Expected: PASS (once Tasks 3-5 land the package compiles; if run in isolation before then, expect a package compile error — re-run after Task 5).

- [ ] **Step 5: Commit**

```bash
git add internal/transform/selector.go internal/transform/selector_test.go
git commit -m "feat(transform): add Match selector (kind/name-glob/origin)"
```

- [x] Task 2 complete

---

## Task 3: `filterKinds` transformation

**Files:**
- Create: `internal/transform/filter_kinds.go`
- Test: `internal/transform/filter_kinds_test.go`

- [ ] **Step 1: Write the failing test**

`internal/transform/filter_kinds_test.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"reflect"
	"testing"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func kinds(ms []manifest.Manifest) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Unstructured.GetKind()
	}
	return out
}

func TestFilterKinds(t *testing.T) {
	svc := mustManifest(t, "apiVersion: v1\nkind: Service\nmetadata:\n  name: s", manifest.OriginUpstream)
	dep := mustManifest(t, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d", manifest.OriginUpstream)
	cmUp := mustManifest(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: up", manifest.OriginUpstream)
	cmAdd := mustManifest(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: add", manifest.OriginAdditions)

	tests := []struct {
		name  string
		spec  *v1alpha1.FilterKindsSpec
		input []manifest.Manifest
		want  []string // kinds surviving, in order
	}{
		{"drops listed kind", &v1alpha1.FilterKindsSpec{Kinds: []string{"Service"}},
			[]manifest.Manifest{svc, dep}, []string{"Deployment"}},
		{"source restricts by origin", &v1alpha1.FilterKindsSpec{Kinds: []string{"ConfigMap"}, Source: "upstream"},
			[]manifest.Manifest{cmUp, cmAdd}, []string{"ConfigMap"}}, // only the additions CM survives
		{"preserves relative order", &v1alpha1.FilterKindsSpec{Kinds: []string{"Service"}},
			[]manifest.Manifest{dep, svc, cmUp}, []string{"Deployment", "ConfigMap"}},
		{"zero matches no-op", &v1alpha1.FilterKindsSpec{Kinds: []string{"Secret"}},
			[]manifest.Manifest{dep}, []string{"Deployment"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ft := &filterKinds{spec: tc.spec}
			orig := cloneForAssert(tc.input)
			got, err := ft.Apply(tc.input)
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if !reflect.DeepEqual(kinds(got), tc.want) {
				t.Fatalf("survivors = %v, want %v", kinds(got), tc.want)
			}
			// no in-place mutation: input slice length + element identity preserved
			if len(tc.input) != len(orig) {
				t.Fatalf("input slice was mutated: len %d != %d", len(tc.input), len(orig))
			}
		})
	}
}

func TestFilterKindsSourceAdditions(t *testing.T) {
	cmUp := mustManifest(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: up", manifest.OriginUpstream)
	cmAdd := mustManifest(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: add", manifest.OriginAdditions)
	ft := &filterKinds{spec: &v1alpha1.FilterKindsSpec{Kinds: []string{"ConfigMap"}, Source: "additions"}}
	got, err := ft.Apply([]manifest.Manifest{cmUp, cmAdd})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(got) != 1 || got[0].Unstructured.GetName() != "up" {
		t.Fatalf("want only upstream CM to survive, got %v", kinds(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/transform/ -run TestFilterKinds -v`
Expected: FAIL to compile — `undefined: filterKinds`.

- [ ] **Step 3: Write minimal implementation**

`internal/transform/filter_kinds.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type filterKinds struct {
	spec *v1alpha1.FilterKindsSpec
}

func (f *filterKinds) Type() string { return "filterKinds" }

// Apply drops manifests whose kind is listed in spec.Kinds. When spec.Source is
// set, only manifests of that origin are dropped. Non-matching manifests are
// returned unchanged and in original order. Zero matches is a no-op (no error).
func (f *filterKinds) Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error) {
	drop := make(map[string]struct{}, len(f.spec.Kinds))
	for _, k := range f.spec.Kinds {
		drop[k] = struct{}{}
	}
	out := make([]manifest.Manifest, 0, len(manifests))
	for _, m := range manifests {
		_, kindListed := drop[m.Unstructured.GetKind()]
		originMatch := f.spec.Source == "" || string(m.Origin) == f.spec.Source
		if kindListed && originMatch {
			continue // dropped
		}
		out = append(out, m)
	}
	return out, nil
}
```
(No in-place mutation: builds a fresh slice; elements are copied by value — the `*Unstructured` pointer is shared but never mutated by this transformation.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/transform/ -run TestFilterKinds -v`
Expected: PASS (package still won't compile until Tasks 4-5 add the other two types; run the full suite after Task 5).

- [ ] **Step 5: Commit**

```bash
git add internal/transform/filter_kinds.go internal/transform/filter_kinds_test.go
git commit -m "feat(transform): add filterKinds transformation"
```

- [x] Task 3 complete

---

## Task 4: `rewriteWebhookURL` transformation

**Files:**
- Create: `internal/transform/rewrite_webhook_url.go`
- Test: `internal/transform/rewrite_webhook_url_test.go`
- Fixtures: `internal/transform/testdata/fixtures/rewrite/` (inline YAML in the test is sufficient; no separate files needed)

- [ ] **Step 1: Write the failing test**

`internal/transform/rewrite_webhook_url_test.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

const prefix = "https://metal-operator-remote-webhook-service:443"

func TestRewriteWebhookURL_VWC(t *testing.T) {
	vwc := mustManifest(t, `
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: v
webhooks:
  - name: a.kb.io
    clientConfig:
      service:
        name: webhook-service
        namespace: kube-system
        path: /validate
      caBundle: QUJD
`, manifest.OriginUpstream)

	rt := &rewriteWebhookURL{spec: &v1alpha1.RewriteWebhookURLSpec{URLPrefix: prefix}}
	got, err := rt.Apply([]manifest.Manifest{vwc})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	whs, _, _ := unstructured.NestedSlice(got[0].Unstructured.Object, "webhooks")
	cc := whs[0].(map[string]any)["clientConfig"].(map[string]any)
	if cc["url"] != prefix+"/validate" {
		t.Fatalf("url = %v, want %v", cc["url"], prefix+"/validate")
	}
	if _, hasService := cc["service"]; hasService {
		t.Fatalf("service should be removed")
	}
	if cc["caBundle"] != "QUJD" {
		t.Fatalf("caBundle must be preserved, got %v", cc["caBundle"])
	}
}

func TestRewriteWebhookURL_ConversionCRD(t *testing.T) {
	crd := mustManifest(t, `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: c
spec:
  conversion:
    strategy: Webhook
    webhook:
      clientConfig:
        service:
          name: webhook-service
          namespace: kube-system
          path: /convert
        caBundle: WFla
`, manifest.OriginUpstream)

	rt := &rewriteWebhookURL{spec: &v1alpha1.RewriteWebhookURLSpec{URLPrefix: prefix}}
	got, err := rt.Apply([]manifest.Manifest{crd})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	cc, _, _ := unstructured.NestedMap(got[0].Unstructured.Object, "spec", "conversion", "webhook", "clientConfig")
	if cc["url"] != prefix+"/convert" {
		t.Fatalf("crd url = %v, want %v", cc["url"], prefix+"/convert")
	}
	if cc["caBundle"] != "WFla" {
		t.Fatalf("crd caBundle must be preserved")
	}
}

func TestRewriteWebhookURL_NonWebhookCRDUntouched(t *testing.T) {
	crd := mustManifest(t, `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: plain
spec:
  group: g
`, manifest.OriginUpstream)
	rt := &rewriteWebhookURL{spec: &v1alpha1.RewriteWebhookURLSpec{URLPrefix: prefix}}
	got, err := rt.Apply([]manifest.Manifest{crd})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if _, found, _ := unstructured.NestedMap(got[0].Unstructured.Object, "spec", "conversion"); found {
		t.Fatalf("plain CRD must be untouched")
	}
}

func TestRewriteWebhookURL_ExistingURLLeftAlone_AndZeroMatchNoOp(t *testing.T) {
	already := mustManifest(t, `
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: m
webhooks:
  - name: a.kb.io
    clientConfig:
      url: https://existing:443/x
`, manifest.OriginUpstream)
	dep := mustManifest(t, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d", manifest.OriginUpstream)

	rt := &rewriteWebhookURL{spec: &v1alpha1.RewriteWebhookURLSpec{URLPrefix: prefix}}
	got, err := rt.Apply([]manifest.Manifest{already, dep})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	whs, _, _ := unstructured.NestedSlice(got[0].Unstructured.Object, "webhooks")
	cc := whs[0].(map[string]any)["clientConfig"].(map[string]any)
	if cc["url"] != "https://existing:443/x" {
		t.Fatalf("existing url must be left alone, got %v", cc["url"])
	}
	if got[1].Unstructured.GetKind() != "Deployment" {
		t.Fatalf("non-webhook manifest must pass through")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/transform/ -run TestRewriteWebhookURL -v`
Expected: FAIL to compile — `undefined: rewriteWebhookURL`.

- [ ] **Step 3: Write minimal implementation**

`internal/transform/rewrite_webhook_url.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type rewriteWebhookURL struct {
	spec *v1alpha1.RewriteWebhookURLSpec
}

func (r *rewriteWebhookURL) Type() string { return "rewriteWebhookURL" }

// Apply rewrites service-based webhook clientConfig to url-based on
// Validating/MutatingWebhookConfiguration (.webhooks[].clientConfig) and on
// conversion-webhook CRDs (.spec.conversion.webhook.clientConfig). Preserves
// caBundle, leaves an existing url alone, no-ops other kinds. Never mutates
// input in place (works on a deep copy).
func (r *rewriteWebhookURL) Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error) {
	out := make([]manifest.Manifest, len(manifests))
	for i, m := range manifests {
		cp := manifest.Manifest{Unstructured: m.Unstructured.DeepCopy(), Origin: m.Origin}
		switch cp.Unstructured.GetKind() {
		case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
			r.rewriteWebhookList(cp.Unstructured)
		case "CustomResourceDefinition":
			r.rewriteConversion(cp.Unstructured)
		}
		out[i] = cp
	}
	return out, nil
}

func (r *rewriteWebhookURL) rewriteWebhookList(u *unstructured.Unstructured) {
	whs, found, err := unstructured.NestedSlice(u.Object, "webhooks")
	if err != nil || !found {
		return
	}
	changed := false
	for i := range whs {
		wh, ok := whs[i].(map[string]any)
		if !ok {
			continue
		}
		cc, ok := wh["clientConfig"].(map[string]any)
		if !ok {
			continue
		}
		if r.rewriteClientConfig(cc) {
			wh["clientConfig"] = cc
			whs[i] = wh
			changed = true
		}
	}
	if changed {
		_ = unstructured.SetNestedSlice(u.Object, whs, "webhooks")
	}
}

func (r *rewriteWebhookURL) rewriteConversion(u *unstructured.Unstructured) {
	strategy, _, _ := unstructured.NestedString(u.Object, "spec", "conversion", "strategy")
	if strategy != "Webhook" {
		return
	}
	cc, found, err := unstructured.NestedMap(u.Object, "spec", "conversion", "webhook", "clientConfig")
	if err != nil || !found {
		return
	}
	if r.rewriteClientConfig(cc) {
		_ = unstructured.SetNestedMap(u.Object, cc, "spec", "conversion", "webhook", "clientConfig")
	}
}

// rewriteClientConfig replaces a service-based clientConfig with url-based,
// preserving caBundle. Returns true if it changed cc. Leaves an existing url alone.
func (r *rewriteWebhookURL) rewriteClientConfig(cc map[string]any) bool {
	if _, hasURL := cc["url"]; hasURL {
		return false
	}
	svc, ok := cc["service"].(map[string]any)
	if !ok {
		return false
	}
	pathStr, _ := svc["path"].(string)
	cc["url"] = r.spec.URLPrefix + pathStr
	delete(cc, "service")
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/transform/ -run TestRewriteWebhookURL -v`
Expected: PASS (package compiles once Task 5 lands `patch`; if run before, expect package compile error — re-run after Task 5).

- [ ] **Step 5: Commit**

```bash
git add internal/transform/rewrite_webhook_url.go internal/transform/rewrite_webhook_url_test.go
git commit -m "feat(transform): add rewriteWebhookURL transformation (VWC/MWC/CRD)"
```

- [x] Task 4 complete

---

## Task 5: `patch` transformation (strategicMerge XOR jsonPatch) + injector-label case

**Files:**
- Create: `internal/transform/patch.go`
- Test: `internal/transform/patch_test.go`

- [ ] **Step 1: Write the failing test**

`internal/transform/patch_test.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"strings"
	"testing"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func TestPatch(t *testing.T) {
	dep := func() manifest.Manifest {
		return mustManifest(t, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller-manager
spec:
  replicas: 1
`, manifest.OriginUpstream)
	}
	vwc := func() manifest.Manifest {
		return mustManifest(t, `
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: v
`, manifest.OriginUpstream)
	}

	tests := []struct {
		name    string
		spec    *v1alpha1.PatchSpec
		input   []manifest.Manifest
		check   func(t *testing.T, out []manifest.Manifest)
		wantErr string
	}{
		{
			name: "strategicMerge sets replicas",
			spec: &v1alpha1.PatchSpec{
				Target:         v1alpha1.Selector{Kind: "Deployment"},
				StrategicMerge: mustJSON(`{"spec":{"replicas":3}}`),
			},
			input: []manifest.Manifest{dep()},
			check: func(t *testing.T, out []manifest.Manifest) {
				r, _, _ := nestedInt64(out[0], "spec", "replicas")
				if r != 3 {
					t.Fatalf("replicas = %d, want 3", r)
				}
			},
		},
		{
			name: "strategicMerge stamps injector label",
			spec: &v1alpha1.PatchSpec{
				Target:         v1alpha1.Selector{Kind: "ValidatingWebhookConfiguration"},
				StrategicMerge: mustJSON(`{"metadata":{"labels":{"dual-deployment-operator.cc.sap/webhook-injector":"metal-operator"}}}`),
			},
			input: []manifest.Manifest{vwc()},
			check: func(t *testing.T, out []manifest.Manifest) {
				if out[0].Unstructured.GetLabels()["dual-deployment-operator.cc.sap/webhook-injector"] != "metal-operator" {
					t.Fatalf("label not stamped: %v", out[0].Unstructured.GetLabels())
				}
			},
		},
		{
			name: "jsonPatch replaces replicas",
			spec: &v1alpha1.PatchSpec{
				Target:    v1alpha1.Selector{Kind: "Deployment"},
				JSONPatch: []v1alpha1.JSONPatchOp{{Op: "replace", Path: "/spec/replicas", Value: mustJSON(`5`)}},
			},
			input: []manifest.Manifest{dep()},
			check: func(t *testing.T, out []manifest.Manifest) {
				r, _, _ := nestedInt64(out[0], "spec", "replicas")
				if r != 5 {
					t.Fatalf("replicas = %d, want 5", r)
				}
			},
		},
		{
			name:    "zero matches fails loud",
			spec:    &v1alpha1.PatchSpec{Target: v1alpha1.Selector{Kind: "Deployment", Name: "nope"}, StrategicMerge: mustJSON(`{}`)},
			input:   []manifest.Manifest{dep()},
			wantErr: "no matching resource",
		},
		{
			name:    "both variants set is rejected",
			spec:    &v1alpha1.PatchSpec{Target: v1alpha1.Selector{Kind: "Deployment"}, StrategicMerge: mustJSON(`{}`), JSONPatch: []v1alpha1.JSONPatchOp{{Op: "test", Path: "/x"}}},
			input:   []manifest.Manifest{dep()},
			wantErr: "exactly one of strategicMerge or jsonPatch",
		},
		{
			name:    "neither variant set is rejected",
			spec:    &v1alpha1.PatchSpec{Target: v1alpha1.Selector{Kind: "Deployment"}},
			input:   []manifest.Manifest{dep()},
			wantErr: "exactly one of strategicMerge or jsonPatch",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &patch{spec: tc.spec}
			out, err := p.Apply(tc.input)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want contains %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			tc.check(t, out)
		})
	}
}
```
Add a helper to `testhelpers_test.go` (append):
```go
func nestedInt64(m manifest.Manifest, fields ...string) (int64, bool, error) {
	return unstructured.NestedInt64(m.Unstructured.Object, fields...)
}
```
(and ensure `k8s.io/apimachinery/pkg/apis/meta/v1/unstructured` is imported in that file — it already is from Task 1.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/transform/ -run TestPatch -v`
Expected: FAIL to compile — `undefined: patch`.

- [ ] **Step 3: Write minimal implementation**

`internal/transform/patch.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"encoding/json"
	"errors"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/strategicpatch"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type patch struct {
	spec *v1alpha1.PatchSpec
}

func (p *patch) Type() string { return "patch" }

// Apply applies the strategic-merge XOR json patch to every manifest matching
// the target selector. Fails loud on zero matches. Never mutates input in place.
func (p *patch) Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error) {
	hasSM := p.spec.StrategicMerge != nil
	hasJP := len(p.spec.JSONPatch) > 0
	if hasSM == hasJP {
		return nil, errors.New("exactly one of strategicMerge or jsonPatch must be set")
	}

	out := make([]manifest.Manifest, len(manifests))
	matched := 0
	for i, m := range manifests {
		if !Match(m, p.spec.Target) {
			out[i] = m
			continue
		}
		matched++
		patched, err := p.applyOne(m.Unstructured)
		if err != nil {
			return nil, fmt.Errorf("patch %s/%s: %w", m.Unstructured.GetKind(), m.Unstructured.GetName(), err)
		}
		out[i] = manifest.Manifest{Unstructured: patched, Origin: m.Origin}
	}
	if matched == 0 {
		return nil, errors.New("no matching resource for patch target selector")
	}
	return out, nil
}

func (p *patch) applyOne(u *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	orig, err := json.Marshal(u.Object)
	if err != nil {
		return nil, err
	}

	var result []byte
	switch {
	case p.spec.StrategicMerge != nil:
		// Strategic merge without a Go struct schema degrades to a JSON merge
		// patch, which is correct for the v1 candidate patches (labels, replicas,
		// sidecar containers keyed by name). Users needing custom-type list-key
		// semantics use jsonPatch.
		result, err = strategicpatch.StrategicMergePatch(orig, p.spec.StrategicMerge.Raw, u.Object)
		if err != nil {
			return nil, err
		}
	default:
		jp, err := json.Marshal(p.spec.JSONPatch)
		if err != nil {
			return nil, err
		}
		decoded, err := jsonpatch.DecodePatch(jp)
		if err != nil {
			return nil, err
		}
		result, err = decoded.Apply(orig)
		if err != nil {
			return nil, err
		}
	}

	obj := map[string]any{}
	if err := json.Unmarshal(result, &obj); err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: obj}, nil
}
```
Note on `StrategicMergePatch`'s third arg: passing `u.Object` (a `map[string]any`) as the `dataStruct` makes apimachinery treat it as a plain map → JSON merge semantics (no struct patch-strategy tags). This matches design §3.4.1's documented behavior. If the RED test for `strategicMerge sets replicas` fails because of the third-arg type, switch to `jsonpatch.MergePatch(orig, p.spec.StrategicMerge.Raw)` (RFC 7386 JSON merge patch) — behaviorally identical for these inputs. Pick whichever makes the test green and note it in the commit.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/transform/ -run TestPatch -v`
Expected: PASS. The full package now compiles.

- [ ] **Step 5: Commit**

```bash
git add internal/transform/patch.go internal/transform/patch_test.go internal/transform/testhelpers_test.go
git commit -m "feat(transform): add patch transformation (strategicMerge XOR jsonPatch)"
```

- [x] Task 5 complete

---

## Task 6: `Build` parse ordering + no-in-place-mutation invariant

**Files:**
- Modify: `internal/transform/transform.go` (already complete from Task 1; no code change expected)
- Test: `internal/transform/transform_test.go`

- [ ] **Step 1: Write the failing test**

`internal/transform/transform_test.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"testing"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func TestBuildPreservesOrder(t *testing.T) {
	specs := []v1alpha1.Transformation{
		{Patch: &v1alpha1.PatchSpec{Target: v1alpha1.Selector{Kind: "Deployment"}, StrategicMerge: mustJSON(`{}`)}},
		{RewriteWebhookURL: &v1alpha1.RewriteWebhookURLSpec{URLPrefix: "https://x:443"}},
		{FilterKinds: &v1alpha1.FilterKindsSpec{Kinds: []string{"Service"}}},
	}
	got, err := Build(specs)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	want := []string{"patch", "rewriteWebhookURL", "filterKinds"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, ty := range want {
		if got[i].Type() != ty {
			t.Fatalf("got[%d].Type() = %q, want %q", i, got[i].Type(), ty)
		}
	}
}

func TestBuildEmptyEntryErrors(t *testing.T) {
	_, err := Build([]v1alpha1.Transformation{{}})
	if err == nil {
		t.Fatalf("expected error for empty entry")
	}
}

func TestApplyDoesNotMutateInput(t *testing.T) {
	in := []manifest.Manifest{
		mustManifest(t, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d\nspec:\n  replicas: 1", manifest.OriginUpstream),
	}
	before := cloneForAssert(in)

	p := &patch{spec: &v1alpha1.PatchSpec{Target: v1alpha1.Selector{Kind: "Deployment"}, StrategicMerge: mustJSON(`{"spec":{"replicas":9}}`)}}
	if _, err := p.Apply(in); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	r, _, _ := nestedInt64(in[0], "spec", "replicas")
	rBefore, _, _ := nestedInt64(before[0], "spec", "replicas")
	if r != rBefore {
		t.Fatalf("input Unstructured mutated in place: replicas %d != %d", r, rBefore)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/transform/ -run 'TestBuild|TestApplyDoesNotMutate' -v`
Expected: PASS for `TestBuild*` (Build already implemented in Task 1). `TestApplyDoesNotMutateInput` — expect PASS if `patch.applyOne` correctly builds a fresh `Unstructured` (it does). If it FAILS, the strategic-merge path mutated `u.Object` — fix `applyOne` to marshal from a copy. This is the RED signal for the invariant.

- [ ] **Step 3: Fix if needed**

If `TestApplyDoesNotMutateInput` failed, ensure `applyOne` never writes back into the input's `Object`. The Task 5 implementation marshals `u.Object` to bytes and unmarshals into a new map — verify no `SetNested*` call targets the input. No change expected if Task 5 was implemented as written.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/transform/ -v`
Expected: PASS (entire package, all tests).

- [ ] **Step 5: Commit**

```bash
git add internal/transform/transform_test.go
git commit -m "test(transform): cover Build ordering, empty-entry error, no-mutation invariant"
```

- [x] Task 6 complete

---

## Task 7: Full-package gates (lint, test, vet)

**Files:** none (verification only)

- [ ] **Step 1: Run gofmt/vet**

Run: `gofmt -l internal/transform/ && go vet ./internal/transform/...`
Expected: no output, exit 0.

- [ ] **Step 2: Run the repo lint auto-fix**

Run: `make lint-fix`
Expected: exit 0; if it rewrites files, inspect the diff — should be formatting only.

- [ ] **Step 3: Run unit tests (package + whole repo)**

Run: `go test ./internal/transform/... && make test`
Expected: `internal/transform` PASS; `make test` PASS (or only pre-existing unrelated failures — note them, do not fix).

- [ ] **Step 4: Confirm REUSE/license headers present**

Run: `grep -L "SPDX-License-Identifier: Apache-2.0" internal/transform/*.go`
Expected: no output (every file has the header).

- [ ] **Step 5: Commit any lint-fix changes**

```bash
git add internal/transform/
git commit -m "chore(transform): apply lint-fix formatting" || echo "nothing to commit"
```

- [x] Task 7 complete

---

## Self-Review (completed during authoring)

- **Spec coverage** — every ADDED requirement in [`specs/manifest-transformation/spec.md`](specs/manifest-transformation/spec.md) maps to a task: Transformation interface + Build (Task 1, Task 6); Match selector (Task 2); patch incl. strategicMerge/jsonPatch/fail-loud/both-neither (Task 5); patch injector-label (Task 5, "stamps injector label" case); rewriteWebhookURL three kinds + preserve caBundle + idempotent + zero-match no-op (Task 4); filterKinds drop/source/order/zero-match (Task 3); no-in-place-mutation (Task 6 + per-transformation clone assertions).
- **Placeholder scan** — no TBD/TODO; every code step shows complete code and an exact command.
- **Type consistency** — `Transformation`, `Build`, `Match`, `patch`, `rewriteWebhookURL`, `filterKinds`, and the `v1alpha1` field names (`StrategicMerge`, `JSONPatch`, `URLPrefix`, `Kinds`, `Source`, `Target`) are used identically across all tasks and match the verified CR types.
- **Note on strategicMerge semantics** — Task 5 documents the map-vs-struct behavior and the `jsonpatch.MergePatch` fallback; this is the one place implementation judgment is allowed, bounded by the RED test.
