# Source Renderers (Helm + kustomize) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Trailing `- [ ] Task N complete` checkboxes mark task-group completion — check them off AFTER all steps in the group land.

**Goal:** Build the `internal/manifest` and `internal/source` packages that render a `DualDeploymentOperator` `spec.source` twice per reconcile (host mode + remote mode) into origin-tagged manifest streams, with chart/root acquisition behind pluggable interfaces so all rendering is unit-testable offline.

**Architecture:** `internal/manifest` owns the `Manifest` type, a multi-doc YAML parser, and the origin-tagging rule (annotation `dual-deployment-operator.cc.sap/origin` == `additions` → `OriginAdditions`, else fallback). `internal/source` exposes a `Source` interface (`Render(ctx, mode)`), a `From()` discriminator factory, a Helm renderer (values merge + top-precedence `mode` injection + `IncludeCRDs`), and a kustomize renderer (overlay selection by mode). Chart acquisition is behind a `ChartLoader` interface; kustomize-root acquisition behind a `RootResolver` interface. Both have real impls plus test fakes.

**Tech Stack:** Go 1.26, `helm.sh/helm/v3` **v3.21.3** (chart load + render), `sigs.k8s.io/kustomize/api` **v0.21.1** + `kustomize/kyaml` **v0.21.1** (`krusty` build, `filesys`), `k8s.io/apimachinery` (`unstructured`, YAML decode). These versions are verified to require exactly `k8s.io/*` v0.36.2, matching the repo's current pins. Tests are plain `go test` (no envtest, no cluster).

**Module path:** `github.com/SAP-cloud-infrastructure/dual-deployment-operator`

**Every new `.go` file MUST start with the repo SPDX header:**
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0
```

**File structure created by this plan:**
- Create: `internal/manifest/manifest.go` — `Manifest`, `Origin`, constants, `originAnnotation` const
- Create: `internal/manifest/parse.go` — `Parse(raw, fallback)` multi-doc splitter + validation + origin tagging
- Create: `internal/manifest/parse_test.go`, `internal/manifest/manifest_test.go` — table-driven tests
- Create: `internal/source/source.go` — `Mode`, `Source`, `Deps`, `From()` factory, fetcher interfaces
- Create: `internal/source/helm.go` — Helm renderer + real `ChartLoader`
- Create: `internal/source/kustomize.go` — kustomize renderer + real `RootResolver`
- Create: `internal/source/fakes_test.go` — fake `ChartLoader` / `RootResolver`
- Create: `internal/source/helm_test.go`, `internal/source/kustomize_test.go`, `internal/source/source_test.go`
- Create: `internal/source/testdata/charts/demo/**` — a small real Helm chart fixture (upstream subchart + additions template + CRD)
- Create: `internal/source/testdata/kustomize/**` — kustomize base + `host/` + `remote/` overlays with an `additions/` commonAnnotations layer
- Modify: `go.mod` / `go.sum` — add `helm.sh/helm/v3`, `sigs.k8s.io/kustomize/api`, `sigs.k8s.io/kustomize/kyaml`

---

## Task 1: Add dependencies (pinned) and confirm they resolve

**Files:**
- Modify: `go.mod`, `go.sum`

**Pinned versions (verified compatible with the current `k8s.io/*` v0.36.2 / controller-runtime v0.24.1 pins — each requires exactly `k8s.io/*` v0.36.2, so no pin moves):**
- `helm.sh/helm/v3` → `v3.21.3`
- `sigs.k8s.io/kustomize/api` → `v0.21.1`
- `sigs.k8s.io/kustomize/kyaml` → `v0.21.1`

> **Do NOT use `@latest`.** Use the exact versions above. **Do NOT run `go mod tidy` in this task** — no code imports these modules yet, so `tidy` would prune them right back out. They become permanent direct requires once `internal/source/helm.go` and `internal/source/kustomize.go` import them (Tasks 8 and 10); the final `go mod tidy` happens in Task 11.

- [ ] **Step 1: Add the pinned modules**

Run:
```bash
go get helm.sh/helm/v3@v3.21.3
go get sigs.k8s.io/kustomize/api@v0.21.1
go get sigs.k8s.io/kustomize/kyaml@v0.21.1
```
Expected: `go: added helm.sh/helm/v3 v3.21.3`, `go: added sigs.k8s.io/kustomize/api v0.21.1`, `go: added sigs.k8s.io/kustomize/kyaml v0.21.1`. No `k8s.io/*` upgrade messages.

- [ ] **Step 2: Verify the k8s pins did NOT move and the module still builds**

Run:
```bash
grep -E "k8s.io/(api|apimachinery|client-go|apiextensions-apiserver|apiserver) v" go.mod
go build ./...
```
Expected: every `k8s.io/*` line still reads `v0.36.2`; `go build ./...` exits 0. (These three deps appear in `go.mod` now — likely as `// indirect` until Task 8/10 import them. That is fine; do not tidy them away.)

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "build: pin helm/v3 v3.21.3 and kustomize v0.21.1 for source rendering"
```

- [x] Task 1 complete

---

## Task 2: Manifest type and Origin constants

**Files:**
- Create: `internal/manifest/manifest.go`
- Test: `internal/manifest/manifest_test.go`

- [ ] **Step 1: Write the failing test**

`internal/manifest/manifest_test.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOriginConstantValues(t *testing.T) {
	if OriginUpstream != "upstream" {
		t.Errorf("OriginUpstream = %q, want %q", OriginUpstream, "upstream")
	}
	if OriginAdditions != "additions" {
		t.Errorf("OriginAdditions = %q, want %q", OriginAdditions, "additions")
	}
	if OriginAnnotation != "dual-deployment-operator.cc.sap/origin" {
		t.Errorf("OriginAnnotation = %q, want %q", OriginAnnotation, "dual-deployment-operator.cc.sap/origin")
	}
}

func TestManifestExposesObjectAndOrigin(t *testing.T) {
	u := &unstructured.Unstructured{}
	u.SetKind("ConfigMap")
	u.SetName("demo")
	m := Manifest{Unstructured: u, Origin: OriginAdditions}

	if m.Unstructured.GetKind() != "ConfigMap" {
		t.Errorf("kind = %q, want ConfigMap", m.Unstructured.GetKind())
	}
	if m.Origin != OriginAdditions {
		t.Errorf("origin = %q, want additions", m.Origin)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/manifest/ -run 'TestOrigin|TestManifestExposes' -v`
Expected: FAIL — package `manifest` does not compile (`Origin`, `OriginUpstream`, `OriginAdditions`, `OriginAnnotation`, `Manifest` undefined).

- [ ] **Step 3: Write minimal implementation**

`internal/manifest/manifest.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

// Package manifest defines the Manifest value type and the multi-document
// YAML parser that turns rendered source output into origin-tagged manifests.
package manifest

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// OriginAnnotation is the metadata annotation a chart/kustomization author sets
// on resources their own team wrote. Its presence with value "additions"
// classifies a manifest as OriginAdditions; absence defaults to the caller's
// fallback (upstream). It never influences host-vs-remote routing.
const OriginAnnotation = "dual-deployment-operator.cc.sap/origin"

// Origin classifies who authored a manifest: the upstream chart/kustomization
// or our own additions. It is NOT a destination/routing signal.
type Origin string

const (
	// OriginUpstream marks a resource that came from the upstream subchart or reference.
	OriginUpstream Origin = "upstream"
	// OriginAdditions marks a resource authored by our team (carries OriginAnnotation).
	OriginAdditions Origin = "additions"
)

// Manifest is a single rendered Kubernetes object plus its origin classification.
// It carries no target/destination field: destination is implicit from which
// render (host or remote) produced it.
type Manifest struct {
	Unstructured *unstructured.Unstructured
	Origin       Origin
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/manifest/ -run 'TestOrigin|TestManifestExposes' -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/manifest/manifest.go internal/manifest/manifest_test.go
git commit -m "feat(manifest): add Manifest type and Origin constants"
```

- [x] Task 2 complete

---

## Task 3: Multi-document parsing + skip-empty behavior

**Files:**
- Create: `internal/manifest/parse.go`
- Test: `internal/manifest/parse_test.go`

- [ ] **Step 1: Write the failing test**

`internal/manifest/parse_test.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package manifest

import "testing"

func TestParseMultipleDocsInOrder(t *testing.T) {
	raw := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: a
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: b
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: c
`)
	got, err := Parse(raw, OriginUpstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].Unstructured.GetName() != want {
			t.Errorf("doc[%d] name = %q, want %q", i, got[i].Unstructured.GetName(), want)
		}
	}
}

func TestParseSkipsEmptyAndCommentDocs(t *testing.T) {
	raw := []byte(`# leading comment only
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: real
---

---
# trailing comment doc
`)
	got, err := Parse(raw, OriginUpstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Unstructured.GetName() != "real" {
		t.Errorf("name = %q, want real", got[0].Unstructured.GetName())
	}
}

func TestParseEmptyRenderReturnsEmpty(t *testing.T) {
	got, err := Parse([]byte("# only a comment\n---\n\n"), OriginUpstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/manifest/ -run TestParse -v`
Expected: FAIL — `Parse` undefined.

- [ ] **Step 3: Write minimal implementation**

`internal/manifest/parse.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"bytes"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// Parse splits a multi-document YAML byte stream into one Manifest per real
// Kubernetes object. Empty, whitespace-only, comment-only, and null documents
// are skipped. Each real document must declare apiVersion and kind, else an
// error is returned. Origin is derived from the OriginAnnotation: value
// "additions" -> OriginAdditions; absent/empty/other -> fallback. An empty
// render (no real objects) returns an empty slice and no error.
func Parse(raw []byte, fallback Origin) ([]Manifest, error) {
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var out []Manifest
	idx := 0
	for {
		obj := map[string]interface{}{}
		err := dec.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("manifest: decode document %d: %w", idx, err)
		}
		idx++
		if len(obj) == 0 {
			continue // empty / comment-only / null document
		}
		u := &unstructured.Unstructured{Object: obj}
		if u.GetAPIVersion() == "" {
			return nil, fmt.Errorf("manifest: document %d is missing apiVersion", idx-1)
		}
		if u.GetKind() == "" {
			return nil, fmt.Errorf("manifest: document %d is missing kind", idx-1)
		}
		out = append(out, Manifest{Unstructured: u, Origin: originOf(u, fallback)})
	}
	return out, nil
}

func originOf(u *unstructured.Unstructured, fallback Origin) Origin {
	if u.GetAnnotations()[OriginAnnotation] == string(OriginAdditions) {
		return OriginAdditions
	}
	return fallback
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/manifest/ -run TestParse -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
git add internal/manifest/parse.go internal/manifest/parse_test.go
git commit -m "feat(manifest): add multi-doc YAML parser with skip-empty behavior"
```

- [x] Task 3 complete

---

## Task 4: Per-document validation (require apiVersion + kind)

**Files:**
- Test: `internal/manifest/parse_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `internal/manifest/parse_test.go`:
```go
func TestParseRejectsMissingAPIVersion(t *testing.T) {
	raw := []byte(`kind: ConfigMap
metadata:
  name: broken
`)
	_, err := Parse(raw, OriginUpstream)
	if err == nil {
		t.Fatal("expected error for missing apiVersion, got nil")
	}
	if !contains(err.Error(), "apiVersion") {
		t.Errorf("error = %q, want it to mention apiVersion", err.Error())
	}
}

func TestParseRejectsMissingKind(t *testing.T) {
	raw := []byte(`apiVersion: v1
metadata:
  name: broken
`)
	_, err := Parse(raw, OriginUpstream)
	if err == nil {
		t.Fatal("expected error for missing kind, got nil")
	}
	if !contains(err.Error(), "kind") {
		t.Errorf("error = %q, want it to mention kind", err.Error())
	}
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}
```
Add `"bytes"` to the test file imports if not already present.

- [ ] **Step 2: Run test to verify it fails or passes**

Run: `go test ./internal/manifest/ -run 'TestParseRejects' -v`
Expected: PASS — the validation was implemented in Task 3 Step 3. (These tests lock the behavior in explicitly per the spec; if either FAILS, fix `Parse` so the missing-field checks fire before appending.)

- [ ] **Step 3: Commit**

```bash
git add internal/manifest/parse_test.go
git commit -m "test(manifest): lock apiVersion/kind validation behavior"
```

- [ ] Task 4 complete

---

## Task 5: Origin tagging tests (additions / fallback / retained annotation)

**Files:**
- Test: `internal/manifest/parse_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `internal/manifest/parse_test.go`:
```go
func TestParseOriginAdditions(t *testing.T) {
	raw := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: mine
  annotations:
    dual-deployment-operator.cc.sap/origin: additions
`)
	got, err := Parse(raw, OriginUpstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].Origin != OriginAdditions {
		t.Errorf("origin = %q, want additions", got[0].Origin)
	}
	// annotation retained on the object
	if got[0].Unstructured.GetAnnotations()[OriginAnnotation] != "additions" {
		t.Error("origin annotation should be retained on the object")
	}
}

func TestParseOriginFallbackWhenAbsent(t *testing.T) {
	raw := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: theirs
`)
	got, err := Parse(raw, OriginUpstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].Origin != OriginUpstream {
		t.Errorf("origin = %q, want upstream", got[0].Origin)
	}
}

func TestParseOriginUnknownValueFallsBack(t *testing.T) {
	raw := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: weird
  annotations:
    dual-deployment-operator.cc.sap/origin: foreign
`)
	got, err := Parse(raw, OriginUpstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].Origin != OriginUpstream {
		t.Errorf("origin = %q, want upstream (unknown value falls back)", got[0].Origin)
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./internal/manifest/ -run TestParseOrigin -v`
Expected: PASS (all three — behavior implemented in Task 3).

- [ ] **Step 3: Run the whole package**

Run: `go test ./internal/manifest/ -v`
Expected: PASS (all tests).

- [ ] **Step 4: Commit**

```bash
git add internal/manifest/parse_test.go
git commit -m "test(manifest): lock origin-tagging rule (additions/fallback/retained)"
```

- [ ] Task 5 complete

---

## Task 6: Source interface, Mode, and From() discriminator

**Files:**
- Create: `internal/source/source.go`
- Test: `internal/source/source_test.go`

- [ ] **Step 1: Write the failing test**

`internal/source/source_test.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"testing"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

func TestModeValues(t *testing.T) {
	if ModeHost != "host" || ModeRemote != "remote" {
		t.Fatalf("modes = %q/%q, want host/remote", ModeHost, ModeRemote)
	}
}

func TestFromSelectsHelm(t *testing.T) {
	spec := v1alpha1.Source{Helm: &v1alpha1.HelmSource{Repo: "r", Name: "n", Version: "1.0.0"}}
	s, err := From(spec, Deps{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := s.(*helmSource); !ok {
		t.Errorf("From returned %T, want *helmSource", s)
	}
}

func TestFromSelectsKustomize(t *testing.T) {
	spec := v1alpha1.Source{Kustomize: &v1alpha1.KustomizeSource{URL: "u?ref=x", HostPath: "host", RemotePath: "remote"}}
	s, err := From(spec, Deps{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := s.(*kustomizeSource); !ok {
		t.Errorf("From returned %T, want *kustomizeSource", s)
	}
}

func TestFromRejectsNeither(t *testing.T) {
	if _, err := From(v1alpha1.Source{}, Deps{}); err == nil {
		t.Error("expected error when neither discriminator set")
	}
}

func TestFromRejectsBoth(t *testing.T) {
	spec := v1alpha1.Source{
		Helm:      &v1alpha1.HelmSource{},
		Kustomize: &v1alpha1.KustomizeSource{},
	}
	if _, err := From(spec, Deps{}); err == nil {
		t.Error("expected error when both discriminators set")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run 'TestMode|TestFrom' -v`
Expected: FAIL — `ModeHost`, `From`, `Deps`, `helmSource`, `kustomizeSource` undefined.

- [ ] **Step 3: Write minimal implementation**

`internal/source/source.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

// Package source renders a DualDeploymentOperator spec.source into origin-tagged
// manifest streams, once per mode (host or remote), per the two-render pattern.
package source

import (
	"context"
	"errors"

	"helm.sh/helm/v3/pkg/chart"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// Mode selects which render to produce; each render's output targets one cluster.
type Mode string

const (
	// ModeHost renders the host-cluster (seed) resources.
	ModeHost Mode = "host"
	// ModeRemote renders the remote-cluster (shoot) resources.
	ModeRemote Mode = "remote"
)

// Source renders the manifest stream for a specific mode.
type Source interface {
	Render(ctx context.Context, mode Mode) ([]manifest.Manifest, error)
}

// ChartLoader acquires a Helm chart. Production pulls from OCI/HTTP; tests fake it.
type ChartLoader interface {
	Load(ctx context.Context, repo, name, version string) (*chart.Chart, error)
}

// RootResolver resolves a kustomize root (base URL + mode subpath) to a local
// filesystem path plus a cleanup func. Production fetches the pinned remote URL;
// tests resolve to a local overlay directory.
type RootResolver interface {
	Resolve(ctx context.Context, url, subPath string) (fsPath string, cleanup func(), err error)
}

// Deps holds the injectable fetchers a Source needs.
type Deps struct {
	ChartLoader  ChartLoader
	RootResolver RootResolver
}

// From constructs a Source from a spec discriminator plus its dependencies.
// Exactly one of spec.Helm / spec.Kustomize must be set.
func From(spec v1alpha1.Source, deps Deps) (Source, error) {
	switch {
	case spec.Helm != nil && spec.Kustomize == nil:
		return &helmSource{spec: spec.Helm, loader: deps.ChartLoader}, nil
	case spec.Kustomize != nil && spec.Helm == nil:
		return &kustomizeSource{spec: spec.Kustomize, resolver: deps.RootResolver}, nil
	default:
		return nil, errors.New("source: exactly one of source.helm or source.kustomize must be set")
	}
}
```

Also create minimal stub types so the package compiles (real logic added in Tasks 8/10). `internal/source/helm.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"

type helmSource struct {
	spec   *v1alpha1.HelmSource
	loader ChartLoader
}
```

`internal/source/kustomize.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"

type kustomizeSource struct {
	spec     *v1alpha1.KustomizeSource
	resolver RootResolver
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/ -run 'TestMode|TestFrom' -v`
Expected: PASS (all five).

- [ ] **Step 5: Commit**

```bash
git add internal/source/source.go internal/source/helm.go internal/source/kustomize.go internal/source/source_test.go
git commit -m "feat(source): add Source interface, Mode, and From discriminator factory"
```

- [ ] Task 6 complete

---

## Task 7: Test fakes and Helm chart fixture

**Files:**
- Create: `internal/source/fakes_test.go`
- Create: `internal/source/testdata/charts/demo/Chart.yaml`
- Create: `internal/source/testdata/charts/demo/values.yaml`
- Create: `internal/source/testdata/charts/demo/templates/deployment.yaml`
- Create: `internal/source/testdata/charts/demo/templates/addition.yaml`
- Create: `internal/source/testdata/charts/demo/crds/demo-crd.yaml`

- [ ] **Step 1: Create the fake fetchers**

`internal/source/fakes_test.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

// fakeChartLoader loads a chart from a local directory, ignoring repo/name/version.
type fakeChartLoader struct{ dir string }

func (f fakeChartLoader) Load(_ context.Context, _, _, _ string) (*chart.Chart, error) {
	return loader.Load(f.dir)
}

// fakeRootResolver resolves any url+subPath to baseDir/subPath on local disk.
type fakeRootResolver struct{ baseDir string }

func (f fakeRootResolver) Resolve(_ context.Context, _, subPath string) (string, func(), error) {
	return f.baseDir + "/" + subPath, func() {}, nil
}
```

- [ ] **Step 2: Create the Helm chart fixture**

`internal/source/testdata/charts/demo/Chart.yaml`:
```yaml
apiVersion: v2
name: demo
version: 0.1.0
```

`internal/source/testdata/charts/demo/values.yaml`:
```yaml
mode: ""
controllerManager:
  enable: false
```

`internal/source/testdata/charts/demo/templates/deployment.yaml` (upstream-style, no origin annotation, host-mode only):
```yaml
{{- if and (eq .Values.mode "host") .Values.controllerManager.enable }}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo-controller-manager
spec:
  replicas: 1
  selector:
    matchLabels: {app: demo}
  template:
    metadata:
      labels: {app: demo}
    spec:
      containers:
        - name: manager
          image: demo:latest
{{- end }}
```

`internal/source/testdata/charts/demo/templates/addition.yaml` (our addition, carries origin annotation, host-mode only):
```yaml
{{- if eq .Values.mode "host" }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo-addition
  annotations:
    dual-deployment-operator.cc.sap/origin: additions
data:
  hello: world
{{- end }}
```

`internal/source/testdata/charts/demo/crds/demo-crd.yaml` (CRD, delivered in remote mode via IncludeCRDs):
```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: demos.demo.cc.sap
spec:
  group: demo.cc.sap
  names: {kind: Demo, listKind: DemoList, plural: demos, singular: demo}
  scope: Namespaced
  versions:
    - name: v1alpha1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
```

- [ ] **Step 3: Commit**

```bash
git add internal/source/fakes_test.go internal/source/testdata/charts/
git commit -m "test(source): add fake fetchers and demo Helm chart fixture"
```

- [ ] Task 7 complete

---

## Task 8: Helm renderer — values merge, mode injection, IncludeCRDs, origin tagging

**Files:**
- Modify: `internal/source/helm.go`
- Test: `internal/source/helm_test.go`

- [ ] **Step 1: Write the failing test**

`internal/source/helm_test.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func jsonVal(t *testing.T, s string) *apiextensionsv1.JSON {
	t.Helper()
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}

func newHelm(t *testing.T, spec *v1alpha1.HelmSource) Source {
	t.Helper()
	s, err := From(v1alpha1.Source{Helm: spec}, Deps{
		ChartLoader: fakeChartLoader{dir: "testdata/charts/demo"},
	})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	return s
}

func kinds(ms []manifest.Manifest) map[string]manifest.Origin {
	out := map[string]manifest.Origin{}
	for _, m := range ms {
		out[m.Unstructured.GetKind()+"/"+m.Unstructured.GetName()] = m.Origin
	}
	return out
}

func TestHelmHostRenderEnablesControllerAndTagsOrigins(t *testing.T) {
	spec := &v1alpha1.HelmSource{
		Repo: "r", Name: "demo", Version: "0.1.0",
		HostValues: jsonVal(t, `{"controllerManager":{"enable":true}}`),
	}
	ms, err := newHelm(t, spec).Render(context.Background(), ModeHost)
	if err != nil {
		t.Fatalf("render host: %v", err)
	}
	k := kinds(ms)
	if k["Deployment/demo-controller-manager"] != manifest.OriginUpstream {
		t.Errorf("deployment origin = %q, want upstream (present=%v)", k["Deployment/demo-controller-manager"], hasKey(k, "Deployment/demo-controller-manager"))
	}
	if k["ConfigMap/demo-addition"] != manifest.OriginAdditions {
		t.Errorf("addition origin = %q, want additions", k["ConfigMap/demo-addition"])
	}
}

func TestHelmRemoteRenderIncludesCRDs(t *testing.T) {
	spec := &v1alpha1.HelmSource{Repo: "r", Name: "demo", Version: "0.1.0"}
	ms, err := newHelm(t, spec).Render(context.Background(), ModeRemote)
	if err != nil {
		t.Fatalf("render remote: %v", err)
	}
	if _, ok := kinds(ms)["CustomResourceDefinition/demos.demo.cc.sap"]; !ok {
		t.Error("expected CRD in remote render (IncludeCRDs)")
	}
	// host-only Deployment must NOT appear in remote render
	if _, ok := kinds(ms)["Deployment/demo-controller-manager"]; ok {
		t.Error("host-only Deployment leaked into remote render")
	}
}

func TestHelmRejectsUserSuppliedMode(t *testing.T) {
	spec := &v1alpha1.HelmSource{
		Repo: "r", Name: "demo", Version: "0.1.0",
		Values: jsonVal(t, `{"mode":"host"}`),
	}
	_, err := newHelm(t, spec).Render(context.Background(), ModeHost)
	if err == nil {
		t.Fatal("expected error when user values set mode")
	}
}

func hasKey(m map[string]manifest.Origin, k string) bool { _, ok := m[k]; return ok }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestHelm -v`
Expected: FAIL — `helmSource` has no `Render` method.

- [ ] **Step 3: Write minimal implementation**

Replace `internal/source/helm.go` with:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"encoding/json"
	"fmt"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type helmSource struct {
	spec   *v1alpha1.HelmSource
	loader ChartLoader
}

func (h *helmSource) Render(ctx context.Context, mode Mode) ([]manifest.Manifest, error) {
	ch, err := h.loader.Load(ctx, h.spec.Repo, h.spec.Name, h.spec.Version)
	if err != nil {
		return nil, fmt.Errorf("source: load chart: %w", err)
	}

	userVals, err := mergeValues(h.spec.Values, h.modeValues(mode))
	if err != nil {
		return nil, err
	}
	if _, set := userVals["mode"]; set {
		return nil, fmt.Errorf("source: 'mode' is operator-controlled and must not be set in values")
	}
	userVals["mode"] = string(mode)

	cfg := new(action.Configuration)
	inst := action.NewInstall(cfg)
	inst.DryRun = true
	inst.ClientOnly = true
	inst.IncludeCRDs = true
	inst.ReleaseName = h.spec.Name
	inst.Namespace = "default"
	_ = cli.New()

	rel, err := inst.RunWithContext(ctx, ch, userVals)
	if err != nil {
		return nil, fmt.Errorf("source: helm render (mode=%s): %w", mode, err)
	}
	return manifest.Parse([]byte(rel.Manifest), manifest.OriginUpstream)
}

func (h *helmSource) modeValues(mode Mode) *v1alpha1.JSON {
	switch mode {
	case ModeHost:
		return h.spec.HostValues
	case ModeRemote:
		return h.spec.RemoteValues
	}
	return nil
}

// mergeValues merges common values then mode-specific values (mode-specific wins).
func mergeValues(common, modeSpecific *v1alpha1.JSON) (map[string]interface{}, error) {
	base := map[string]interface{}{}
	for _, j := range []*v1alpha1.JSON{common, modeSpecific} {
		if j == nil || len(j.Raw) == 0 {
			continue
		}
		m := map[string]interface{}{}
		if err := json.Unmarshal(j.Raw, &m); err != nil {
			return nil, fmt.Errorf("source: parse values: %w", err)
		}
		base = chartutil.CoalesceTables(m, base)
	}
	return base, nil
}
```

Note: `v1alpha1.JSON` is the alias the CRD types use for `apiextensionsv1.JSON`. If the types package re-exports it differently, import `apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"` and use `*apiextensionsv1.JSON` to match the field types on `HelmSource`. Verify against `api/v1alpha1/dualdeploymentoperator_types.go` before writing — use whichever type the `Values`/`HostValues`/`RemoteValues` fields actually declare.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/ -run TestHelm -v`
Expected: PASS (all three). If `CoalesceTables` precedence is inverted (mode-specific must win), swap the argument order so mode-specific overrides common; re-run until green.

- [ ] **Step 5: Commit**

```bash
git add internal/source/helm.go internal/source/helm_test.go
git commit -m "feat(source): implement Helm renderer with mode injection and IncludeCRDs"
```

- [ ] Task 8 complete

---

## Task 9: Kustomize overlay fixtures

**Files:**
- Create: `internal/source/testdata/kustomize/base/kustomization.yaml`
- Create: `internal/source/testdata/kustomize/base/upstream-cm.yaml`
- Create: `internal/source/testdata/kustomize/host/kustomization.yaml`
- Create: `internal/source/testdata/kustomize/remote/kustomization.yaml`
- Create: `internal/source/testdata/kustomize/additions/host/kustomization.yaml`
- Create: `internal/source/testdata/kustomize/additions/host/addition-cm.yaml`

- [ ] **Step 1: Create the base**

`internal/source/testdata/kustomize/base/kustomization.yaml`:
```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - upstream-cm.yaml
```

`internal/source/testdata/kustomize/base/upstream-cm.yaml`:
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: upstream-cm
data:
  from: upstream
```

- [ ] **Step 2: Create the additions overlay (carries origin annotation)**

`internal/source/testdata/kustomize/additions/host/kustomization.yaml`:
```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
commonAnnotations:
  dual-deployment-operator.cc.sap/origin: additions
resources:
  - addition-cm.yaml
```

`internal/source/testdata/kustomize/additions/host/addition-cm.yaml`:
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: addition-cm
data:
  from: additions
```

- [ ] **Step 3: Create the host and remote overlays**

`internal/source/testdata/kustomize/host/kustomization.yaml`:
```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../base
  - ../additions/host
```

`internal/source/testdata/kustomize/remote/kustomization.yaml`:
```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../base
```

- [ ] **Step 4: Commit**

```bash
git add internal/source/testdata/kustomize/
git commit -m "test(source): add kustomize base + host/remote overlay fixtures"
```

- [ ] Task 9 complete

---

## Task 10: Kustomize renderer — overlay selection + origin tagging

**Files:**
- Modify: `internal/source/kustomize.go`
- Test: `internal/source/kustomize_test.go`

- [ ] **Step 1: Write the failing test**

`internal/source/kustomize_test.go`:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"testing"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func newKustomize(t *testing.T) Source {
	t.Helper()
	s, err := From(v1alpha1.Source{Kustomize: &v1alpha1.KustomizeSource{
		URL: "ignored?ref=x", HostPath: "host", RemotePath: "remote",
	}}, Deps{RootResolver: fakeRootResolver{baseDir: "testdata/kustomize"}})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	return s
}

func TestKustomizeHostOverlayHasAdditionsAndUpstream(t *testing.T) {
	ms, err := newKustomize(t).Render(context.Background(), ModeHost)
	if err != nil {
		t.Fatalf("render host: %v", err)
	}
	k := kinds(ms)
	if k["ConfigMap/upstream-cm"] != manifest.OriginUpstream {
		t.Errorf("upstream-cm origin = %q, want upstream", k["ConfigMap/upstream-cm"])
	}
	if k["ConfigMap/addition-cm"] != manifest.OriginAdditions {
		t.Errorf("addition-cm origin = %q, want additions", k["ConfigMap/addition-cm"])
	}
}

func TestKustomizeRemoteOverlayExcludesAdditions(t *testing.T) {
	ms, err := newKustomize(t).Render(context.Background(), ModeRemote)
	if err != nil {
		t.Fatalf("render remote: %v", err)
	}
	k := kinds(ms)
	if _, ok := k["ConfigMap/addition-cm"]; ok {
		t.Error("addition-cm should not appear in remote overlay")
	}
	if k["ConfigMap/upstream-cm"] != manifest.OriginUpstream {
		t.Errorf("upstream-cm origin = %q, want upstream", k["ConfigMap/upstream-cm"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestKustomize -v`
Expected: FAIL — `kustomizeSource` has no `Render` method.

- [ ] **Step 3: Write minimal implementation**

Replace `internal/source/kustomize.go` with:
```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"fmt"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type kustomizeSource struct {
	spec     *v1alpha1.KustomizeSource
	resolver RootResolver
}

func (k *kustomizeSource) Render(ctx context.Context, mode Mode) ([]manifest.Manifest, error) {
	subPath := k.spec.HostPath
	if mode == ModeRemote {
		subPath = k.spec.RemotePath
	}

	root, cleanup, err := k.resolver.Resolve(ctx, k.spec.URL, subPath)
	if err != nil {
		return nil, fmt.Errorf("source: resolve kustomize root (mode=%s): %w", mode, err)
	}
	defer cleanup()

	opts := krusty.MakeDefaultOptions()
	kust := krusty.MakeKustomizer(opts)
	resMap, err := kust.Run(filesys.MakeFsOnDisk(), root)
	if err != nil {
		return nil, fmt.Errorf("source: kustomize build (mode=%s): %w", mode, err)
	}
	yamlBytes, err := resMap.AsYaml()
	if err != nil {
		return nil, fmt.Errorf("source: kustomize serialize: %w", err)
	}
	return manifest.Parse(yamlBytes, manifest.OriginUpstream)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/ -run TestKustomize -v`
Expected: PASS (both). If krusty rejects relative `../` roots under default load restrictions, set `opts.LoadRestrictions = types.LoadRestrictionsNone` (import `sigs.k8s.io/kustomize/api/types`) and re-run.

- [ ] **Step 5: Commit**

```bash
git add internal/source/kustomize.go internal/source/kustomize_test.go
git commit -m "feat(source): implement kustomize renderer with overlay selection"
```

- [ ] Task 10 complete

---

## Task 11: Full-package verification and lint

**Files:** (no new files)

- [ ] **Step 1: Run the full source + manifest test suites**

Run: `go test ./internal/manifest/ ./internal/source/ -v`
Expected: PASS (all tests, no network access).

- [ ] **Step 2: Vet and build the whole module**

Run:
```bash
go vet ./...
go build ./...
```
Expected: exit 0.

- [ ] **Step 3: Lint (repo convention)**

Run: `make lint-fix` (or `make check` static-check portion)
Expected: no new lint errors in `internal/manifest/` or `internal/source/`. Fix any reported issues (unused imports, error-wrap style) and re-run.

- [ ] **Step 4: Commit any lint fixups**

```bash
git add -A
git commit -m "chore(source): lint fixups for source rendering packages"
```

- [ ] Task 11 complete

---

## Notes for the implementer

- **Type alias verification (Task 8):** Before writing `helm.go`, open `api/v1alpha1/dualdeploymentoperator_types.go` and confirm the exact type of `HelmSource.Values` / `HostValues` / `RemoteValues`. The plan assumes `*apiextensionsv1.JSON` (per design §Phase 1). Use whatever the field actually declares; adjust `mergeValues` and `jsonVal` accordingly.
- **Helm SDK API drift:** `action.NewInstall` / `RunWithContext` signatures vary slightly across `helm/v3` minor versions. If `RunWithContext` is unavailable, use `inst.Run(ch, vals)`. The behavior asserted by the tests (host/remote disjoint sets, CRDs present, mode rejected) is what matters, not the exact SDK call.
- **kustomize load restrictions:** local relative roots (`../base`) may need `LoadRestrictionsNone`; the remote production `RootResolver` (later phase) will resolve to a single fetched tree, so this is a test-fixture concern.
- **No cluster, no network:** every test in this plan runs with `go test`; none require envtest or a Kind cluster. E2E/equivalence testing is deferred to the reconciler (Phase 6) and equivalence (Phase 7) phases per the design.
- **Origin is authorship, not routing:** never use `Origin` to decide host-vs-remote. Destination is implicit from which `Render(mode)` produced the manifest (design §"Origin vs. routing — independent axes").
