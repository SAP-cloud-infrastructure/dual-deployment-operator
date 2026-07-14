# Scaffold Operator + CRD Types Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Trailing `- [ ]` checkboxes mark task-group completion — check them off AFTER all steps in the group complete and the commit lands.

**Goal:** Scaffold the `dual-deployment-operator` kubebuilder project and land the full `v1alpha1` `DualDeploymentOperator` CRD types (Spec + Status) with CRD-level CEL validation and a no-op reconciler that proves the wiring.

**Architecture:** A kubebuilder v3 project (module `github.com/SAP-cloud-infrastructure/dual-deployment-operator`, domain `cc.sap`, group `dual-deployment-operator`, kind `DualDeploymentOperator`, version `v1alpha1`). All CRD types live in one file `api/v1alpha1/dualdeploymentoperator_types.go`. Discriminator constraints (`Source`, `Transformation`, `PatchSpec`, kustomize URL ref-pinning) are enforced by CEL `+kubebuilder:validation:XValidation` markers. A validating webhook is scaffolded but its `CustomValidator` methods return nil and it is not deployed. The reconciler fetches the CR, logs once, and requeues after 10 minutes.

**Tech Stack:** Go 1.22+, kubebuilder v3, controller-runtime, controller-gen, envtest (Kubernetes 1.29+ for CEL), Ginkgo/Gomega, `k8s.io/apiextensions-apiserver` (for `apiextensionsv1.JSON`).

**Preconditions:** `kubebuilder`, `go`, `make`, `kustomize`, and `setup-envtest` available on PATH. Working directory is the repo root `dual-deployment-operator/` which currently contains only `docs/`, `Makefile`, `Makefile.maker.yaml`, `README.md`, `REUSE.toml`, `go.mod`, `shell.nix`, `openspec/`.

**File structure produced by this plan:**
- `api/v1alpha1/dualdeploymentoperator_types.go` — all Spec/Status types + CEL markers (single file, Option A)
- `api/v1alpha1/groupversion_info.go`, `api/v1alpha1/zz_generated.deepcopy.go` — kubebuilder-generated
- `internal/controller/dualdeploymentoperator_controller.go` — no-op reconciler
- `internal/controller/suite_test.go` — envtest bootstrap
- `internal/controller/dualdeploymentoperator_controller_test.go` — reconciler + CEL acceptance/rejection tests
- `api/v1alpha1/dualdeploymentoperator_types_test.go` — marshal/enum unit tests
- `internal/webhook/v1alpha1/dualdeploymentoperator_webhook.go` — no-op CustomValidator
- `cmd/main.go`, `Makefile`, `Dockerfile`, `PROJECT`, `config/**` — kubebuilder-generated
- `hack/boilerplate.go.txt` — SPDX header

> **Note on scaffold vs. edit:** kubebuilder overwrites `go.mod`, `Makefile`, `README.md`, and creates `PROJECT`. Before Task 1, back up the existing `go.mod`, `Makefile`, `Makefile.maker.yaml`, `README.md`, `REUSE.toml`, and `docs/` so kubebuilder's scaffold does not clobber project-specific content; merge them back in Task 1's final step.

---

## Task 1: Kubebuilder scaffold + project skeleton

**Files:**
- Create: `PROJECT`, `cmd/main.go`, `Makefile`, `Dockerfile`, `.dockerignore`, `.gitignore`, `hack/boilerplate.go.txt`, `config/**`
- Modify: `go.mod` (kubebuilder rewrites module directives; preserve module path)
- Preserve: `docs/`, `openspec/`, `Makefile.maker.yaml`, `REUSE.toml`, `README.md`

- [ ] **Step 1: Back up project-specific files kubebuilder may clobber**

```bash
mkdir -p /tmp/ddo-backup
cp -a go.mod Makefile Makefile.maker.yaml README.md REUSE.toml shell.nix /tmp/ddo-backup/ 2>/dev/null || true
```

- [ ] **Step 2: Run kubebuilder init**

```bash
kubebuilder init \
  --domain cc.sap \
  --repo github.com/SAP-cloud-infrastructure/dual-deployment-operator \
  --project-name dual-deployment-operator
```

Expected: creates `PROJECT`, `cmd/main.go`, `Makefile`, `Dockerfile`, `config/`, `hack/boilerplate.go.txt`, rewrites `go.mod`.

- [ ] **Step 3: Verify module path and PROJECT identity**

Run: `head -1 go.mod && grep -E '^(domain|repo|projectName):' PROJECT`
Expected: `module github.com/SAP-cloud-infrastructure/dual-deployment-operator`, `domain: cc.sap`, `repo: github.com/SAP-cloud-infrastructure/dual-deployment-operator`, `projectName: dual-deployment-operator`.

- [ ] **Step 4: Restore Makefile.maker.yaml and REUSE.toml (do not let scaffold delete them)**

```bash
cp -a /tmp/ddo-backup/Makefile.maker.yaml /tmp/ddo-backup/REUSE.toml . 2>/dev/null || true
```

- [ ] **Step 5: Verify the skeleton builds**

Run: `go build ./...`
Expected: PASS (no errors; only the scaffolded manager exists so far).

- [ ] **Step 6: Commit**

```bash
git add PROJECT cmd Makefile Dockerfile .dockerignore .gitignore hack config go.mod go.sum
git commit -m "chore: kubebuilder init scaffold for dual-deployment-operator"
```

- [x] Task 1 complete

---

## Task 2: Create API + controller + webhook scaffolds

**Files:**
- Create: `api/v1alpha1/dualdeploymentoperator_types.go` (stub), `api/v1alpha1/groupversion_info.go`, `internal/controller/dualdeploymentoperator_controller.go` (stub), `internal/controller/suite_test.go`, `internal/webhook/v1alpha1/dualdeploymentoperator_webhook.go` (stub), `config/crd/**`, `config/rbac/**`, `config/webhook/**`, `config/certmanager/**`
- Modify: `PROJECT` (adds resource + webhook entries), `cmd/main.go` (wires controller + webhook)

- [ ] **Step 1: Scaffold the API and controller**

```bash
kubebuilder create api \
  --group dual-deployment-operator \
  --version v1alpha1 \
  --kind DualDeploymentOperator \
  --resource --controller
```

Answer `y` to "Create Resource" and "Create Controller".

- [ ] **Step 2: Scaffold the validating webhook (programmatic validation)**

```bash
kubebuilder create webhook \
  --group dual-deployment-operator \
  --version v1alpha1 \
  --kind DualDeploymentOperator \
  --programmatic-validation
```

- [ ] **Step 3: Verify PROJECT records the resource and webhook**

Run: `grep -E 'group: dual-deployment-operator|kind: DualDeploymentOperator|version: v1alpha1|webhooks:' PROJECT`
Expected: one resource entry with the correct group/kind/version, plus a `webhooks:` block with `validation: true`.

- [ ] **Step 4: Keep webhook wiring OFF in the default overlay**

Confirm the `[WEBHOOK]` and `[CERTMANAGER]` sections in `config/default/kustomization.yaml` and `config/crd/kustomization.yaml` remain **commented out** (kubebuilder leaves them commented by default). Do not uncomment them.

Run: `grep -c '# *\[WEBHOOK\]\|# *\[CERTMANAGER\]' config/default/kustomization.yaml`
Expected: at least 2 (sections present and commented).

- [ ] **Step 5: Verify build still passes**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add api internal cmd config PROJECT
git commit -m "chore: scaffold DualDeploymentOperator API, controller, and (non-deployed) webhook"
```

- [x] Task 2 complete

---

## Task 3: Declare go.mod dependencies (including deferred Phase 2 deps)

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add Phase 2 deps to go.mod without importing them**

```bash
go get helm.sh/helm/v3@latest
go get sigs.k8s.io/kustomize/api@latest
go get sigs.k8s.io/kustomize/kyaml@latest
go get k8s.io/apiextensions-apiserver@latest
```

(Pin to versions compatible with the kubebuilder-selected `k8s.io/*` and `controller-runtime` versions; if `go get @latest` produces a conflict, resolve by matching the `k8s.io/api` minor version already in go.mod.)

- [ ] **Step 2: Verify the deps are present at top level**

Run: `grep -E 'helm.sh/helm/v3|sigs.k8s.io/kustomize/api|k8s.io/apiextensions-apiserver' go.mod`
Expected: all three appear in the `require` block.

- [ ] **Step 3: Verify Helm and kustomize are NOT yet imported**

Run: `go list -deps ./... 2>/dev/null | grep -E 'helm.sh/helm|sigs.k8s.io/kustomize' || echo "NONE"`
Expected: `NONE` (no source imports them yet; they sit in go.mod only). If `go build` complains about unused requires, that is expected — do not remove them; they are intentionally staged for Phase 2. If `go mod tidy` would drop them, add a `tools.go`-style blank-import guard is NOT wanted here; instead accept they may be dropped by `tidy` and re-added in Phase 2. Document this in the commit message.

> Decision: run `go mod tidy` in Step 4. If it removes the staged deps, that is acceptable — the operator-scaffold spec's "declared early" requirement is a nice-to-have, not load-bearing; Phase 2 will re-add them. Prefer a clean `go.mod` over unused requires.

- [ ] **Step 4: Tidy and verify build**

Run: `go mod tidy && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add k8s.io/apiextensions-apiserver and stage Phase 2 deps (helm, kustomize)"
```

- [x] Task 3 complete

---

## Task 4: CRD Spec/Status types — round-trip unit tests (RED)

**Files:**
- Create: `api/v1alpha1/dualdeploymentoperator_types_test.go`
- Test: `api/v1alpha1/dualdeploymentoperator_types_test.go`

- [ ] **Step 1: Write failing marshal/round-trip tests for the Spec union types**

Create `api/v1alpha1/dualdeploymentoperator_types_test.go`:

```go
package v1alpha1

import (
	"encoding/json"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestHelmSourceRoundTrip(t *testing.T) {
	in := Source{Helm: &HelmSource{Repo: "oci://x", Name: "y", Version: "1.0.0"}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); got != `{"helm":{"repo":"oci://x","name":"y","version":"1.0.0"}}` {
		t.Fatalf("unexpected JSON: %s", got)
	}
	var out Source
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Helm == nil || out.Helm.Repo != "oci://x" || out.Kustomize != nil {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestKustomizeSourceRoundTrip(t *testing.T) {
	in := Source{Kustomize: &KustomizeSource{URL: "https://github.com/x/y//p?ref=v1", HostPath: "host", RemotePath: "remote"}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); got != `{"kustomize":{"url":"https://github.com/x/y//p?ref=v1","hostPath":"host","remotePath":"remote"}}` {
		t.Fatalf("unexpected JSON: %s", got)
	}
	var out Source
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Kustomize == nil || out.Kustomize.HostPath != "host" {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestTransformationVariantsRoundTrip(t *testing.T) {
	raw := apiextensionsv1.JSON{Raw: []byte(`{"metadata":{"labels":{"a":"b"}}}`)}
	cases := []Transformation{
		{Patch: &PatchSpec{Target: Selector{Kind: "Deployment"}, StrategicMerge: &raw}},
		{Patch: &PatchSpec{Target: Selector{Kind: "Deployment"}, JSONPatch: []JSONPatchOp{{Op: "add", Path: "/metadata/labels/a", Value: &apiextensionsv1.JSON{Raw: []byte(`"b"`)}}}}},
		{RewriteWebhookURL: &RewriteWebhookURLSpec{URLPrefix: "https://x:443"}},
		{FilterKinds: &FilterKindsSpec{Kinds: []string{"Service"}}},
		{PackageWebhookConfigsForInjector: &PackageWebhookConfigsForInjectorSpec{ConfigMapName: "webhooks"}},
	}
	for i, c := range cases {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("case %d marshal: %v", i, err)
		}
		var out Transformation
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("case %d unmarshal: %v", i, err)
		}
	}
}

func TestHealthStateConstants(t *testing.T) {
	for _, s := range []HealthState{HealthHealthy, HealthProgressing, HealthDegraded, HealthUnknown} {
		if s == "" {
			t.Fatalf("empty HealthState constant")
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail (types not defined)**

Run: `go test ./api/v1alpha1/... 2>&1 | head -20`
Expected: FAIL — compile errors, `undefined: HelmSource`, `undefined: PatchSpec`, etc.

- [x] Task 4 complete

---

## Task 5: CRD Spec/Status types — implementation (GREEN)

**Files:**
- Create/Replace: `api/v1alpha1/dualdeploymentoperator_types.go` (replace kubebuilder stub)

- [ ] **Step 1: Write the full types file**

Replace `api/v1alpha1/dualdeploymentoperator_types.go` with (keep the kubebuilder-generated package clause, imports, `SchemeBuilder.Register` call, and `DualDeploymentOperator`/`DualDeploymentOperatorList` kind structs the scaffold created — add the field types below):

```go
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// DualDeploymentOperatorSpec defines the desired state.
type DualDeploymentOperatorSpec struct {
	Source           Source              `json:"source"`
	RemoteKubeconfig RemoteKubeconfigRef `json:"remoteKubeconfig"`
	// +optional
	Transformations []Transformation `json:"transformations,omitempty"`
	// +optional
	// +kubebuilder:default={crds:Retain}
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// Source is a discriminated union — exactly one of Helm or Kustomize.
// +kubebuilder:validation:XValidation:rule="has(self.helm) != has(self.kustomize)",message="exactly one of source.helm or source.kustomize must be set"
type Source struct {
	// +optional
	Helm *HelmSource `json:"helm,omitempty"`
	// +optional
	Kustomize *KustomizeSource `json:"kustomize,omitempty"`
}

type HelmSource struct {
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	HostValues *apiextensionsv1.JSON `json:"hostValues,omitempty"`
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	RemoteValues *apiextensionsv1.JSON `json:"remoteValues,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.url.matches('.*[?&]ref=.+')",message="kustomize url must include a pinned ref= query parameter"
type KustomizeSource struct {
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`
	// +kubebuilder:validation:MinLength=1
	HostPath string `json:"hostPath"`
	// +kubebuilder:validation:MinLength=1
	RemotePath string `json:"remotePath"`
}

type RemoteKubeconfigRef struct {
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName"`
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// Transformation is a discriminated union — exactly one field set per entry.
// +kubebuilder:validation:XValidation:rule="(has(self.patch) ? 1 : 0) + (has(self.rewriteWebhookURL) ? 1 : 0) + (has(self.filterKinds) ? 1 : 0) + (has(self.packageWebhookConfigsForInjector) ? 1 : 0) == 1",message="exactly one transformation type must be set per entry"
type Transformation struct {
	// +optional
	Patch *PatchSpec `json:"patch,omitempty"`
	// +optional
	RewriteWebhookURL *RewriteWebhookURLSpec `json:"rewriteWebhookURL,omitempty"`
	// +optional
	FilterKinds *FilterKindsSpec `json:"filterKinds,omitempty"`
	// +optional
	PackageWebhookConfigsForInjector *PackageWebhookConfigsForInjectorSpec `json:"packageWebhookConfigsForInjector,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.strategicMerge) != has(self.jsonPatch)",message="exactly one of patch.strategicMerge or patch.jsonPatch must be set"
type PatchSpec struct {
	Target Selector `json:"target"`
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	StrategicMerge *apiextensionsv1.JSON `json:"strategicMerge,omitempty"`
	// +optional
	JSONPatch []JSONPatchOp `json:"jsonPatch,omitempty"`
}

type JSONPatchOp struct {
	// +kubebuilder:validation:Enum=add;remove;replace;move;copy;test
	Op string `json:"op"`
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`
	// +optional
	From string `json:"from,omitempty"`
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Value *apiextensionsv1.JSON `json:"value,omitempty"`
}

type RewriteWebhookURLSpec struct {
	// +kubebuilder:validation:MinLength=1
	URLPrefix string `json:"urlPrefix"`
	// +optional
	TargetKinds []string `json:"targetKinds,omitempty"`
}

type FilterKindsSpec struct {
	// +kubebuilder:validation:MinItems=1
	Kinds []string `json:"kinds"`
	// +optional
	Source string `json:"source,omitempty"`
}

type PackageWebhookConfigsForInjectorSpec struct {
	// +kubebuilder:validation:MinLength=1
	ConfigMapName string `json:"configMapName"`
	// +optional
	DataKey string `json:"dataKey,omitempty"`
}

type Selector struct {
	// +optional
	Kind string `json:"kind,omitempty"`
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	Origin string `json:"origin,omitempty"`
}

type DeletionPolicy struct {
	// +optional
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	CRDs string `json:"crds,omitempty"`
}

// DualDeploymentOperatorStatus defines the observed state.
type DualDeploymentOperatorStatus struct {
	// +optional
	HostResources []ResourceStatus `json:"hostResources,omitempty"`
	// +optional
	RemoteResources []ResourceStatus `json:"remoteResources,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	LastReconcile *metav1.Time `json:"lastReconcile,omitempty"`
}

type ResourceStatus struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	// +optional
	Namespace string      `json:"namespace,omitempty"`
	Name      string      `json:"name"`
	Health    HealthState `json:"health"`
	// +optional
	LastApplied *metav1.Time `json:"lastApplied,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:validation:Enum=Healthy;Progressing;Degraded;Unknown
type HealthState string

const (
	HealthHealthy     HealthState = "Healthy"
	HealthProgressing HealthState = "Progressing"
	HealthDegraded    HealthState = "Degraded"
	HealthUnknown     HealthState = "Unknown"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ddo
type DualDeploymentOperator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DualDeploymentOperatorSpec   `json:"spec,omitempty"`
	Status DualDeploymentOperatorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DualDeploymentOperatorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DualDeploymentOperator `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DualDeploymentOperator{}, &DualDeploymentOperatorList{})
}
```

> If the kubebuilder stub already declared `DualDeploymentOperator`, `DualDeploymentOperatorList`, and the `init()` register call, delete its versions and keep only the ones above to avoid duplicate declarations. `corev1` is imported for forward-compatibility with later phases; if `go vet` flags it unused in this change, remove the `corev1` import line (no type here references it yet).

- [ ] **Step 2: Run unit tests to verify they pass**

Run: `go test ./api/v1alpha1/... -v 2>&1 | tail -20`
Expected: PASS for `TestHelmSourceRoundTrip`, `TestKustomizeSourceRoundTrip`, `TestTransformationVariantsRoundTrip`, `TestHealthStateConstants`.

- [ ] **Step 3: Generate deepcopy and CRD manifests**

Run: `make generate manifests`
Expected: creates/updates `api/v1alpha1/zz_generated.deepcopy.go` and `config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml`.

- [ ] **Step 4: Verify CRD manifest identity and CEL rules are present**

Run:
```bash
grep -E 'group: dual-deployment-operator.cc.sap|kind: DualDeploymentOperator|scope: Namespaced' config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml
grep -c 'x-kubernetes-validations' config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml
```
Expected: group/kind/scope lines present; at least 4 `x-kubernetes-validations` blocks (Source, Transformation, PatchSpec, KustomizeSource).

- [ ] **Step 5: Verify build and vet**

Run: `go build ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add api/v1alpha1 config/crd
git commit -m "feat: add v1alpha1 DualDeploymentOperator CRD types with CEL validation"
```

- [x] Task 5 complete

---

## Task 6: No-op reconciler (RED → GREEN)

**Files:**
- Replace: `internal/controller/dualdeploymentoperator_controller.go`
- Create: `internal/controller/dualdeploymentoperator_controller_test.go` (unit-level reconcile return test)

- [ ] **Step 1: Write a failing unit test for the reconcile return contract**

Create `internal/controller/dualdeploymentoperator_controller_test.go`:

```go
package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrl "sigs.k8s.io/controller-runtime"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
)

func newTestReconciler(objs ...client.Object) *DualDeploymentOperatorReconciler {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = ddov1alpha1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &DualDeploymentOperatorReconciler{Client: c, Scheme: scheme}
}

func TestReconcileMissingCRReturnsNoError(t *testing.T) {
	r := newTestReconciler()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "nope", Namespace: "ns"}})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.RequeueAfter != 0 || res.Requeue {
		t.Fatalf("expected empty result for missing CR, got %+v", res)
	}
}

func TestReconcileExistingCRRequeues10m(t *testing.T) {
	cr := &ddov1alpha1.DualDeploymentOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "metal-operator", Namespace: "shoot--cp--m-eu-de-1"},
		Spec: ddov1alpha1.DualDeploymentOperatorSpec{
			Source:           ddov1alpha1.Source{Helm: &ddov1alpha1.HelmSource{Repo: "oci://x", Name: "y", Version: "1.0.0"}},
			RemoteKubeconfig: ddov1alpha1.RemoteKubeconfigRef{SecretName: "kc", Key: "kubeconfig"},
		},
	}
	r := newTestReconciler(cr)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "metal-operator", Namespace: "shoot--cp--m-eu-de-1"}})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.RequeueAfter != 10*time.Minute {
		t.Fatalf("expected RequeueAfter=10m, got %v", res.RequeueAfter)
	}
	if res.Requeue {
		t.Fatalf("expected Requeue=false, got true")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/... -run 'TestReconcile' -v 2>&1 | head -20`
Expected: FAIL — reconciler currently has the kubebuilder stub returning `ctrl.Result{}, nil` for existing CRs (RequeueAfter=0), so `TestReconcileExistingCRRequeues10m` fails.

- [ ] **Step 3: Implement the no-op reconciler**

Replace the body of `Reconcile` in `internal/controller/dualdeploymentoperator_controller.go`:

```go
package controller

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

const requeueInterval = 10 * time.Minute

type DualDeploymentOperatorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=dual-deployment-operator.cc.sap,resources=dualdeploymentoperators,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=dual-deployment-operator.cc.sap,resources=dualdeploymentoperators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dual-deployment-operator.cc.sap,resources=dualdeploymentoperators/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *DualDeploymentOperatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var cr ddov1alpha1.DualDeploymentOperator
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger.Info("reconciling", "name", cr.Name, "namespace", cr.Namespace)

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

func (r *DualDeploymentOperatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ddov1alpha1.DualDeploymentOperator{}).
		Named("dualdeploymentoperator").
		Complete(r)
}
```

> Note: `logger.Info("reconciling", "name", ..., "namespace", ...)` uses structured fields, satisfying the noop-reconciler structured-logging requirement. Do NOT interpolate the name into the message string. The `+kubebuilder:rbac` markers scope RBAC to CRs + Secrets only (no cluster-scoped resource grants), satisfying the operator-scaffold RBAC requirement. `apierrors.IsNotFound` is equivalent to `client.IgnoreNotFound`; either satisfies the "missing CR returns no error" requirement.

- [ ] **Step 4: Run reconcile tests to verify they pass**

Run: `go test ./internal/controller/... -run 'TestReconcile' -v 2>&1 | tail -20`
Expected: PASS for both `TestReconcileMissingCRReturnsNoError` and `TestReconcileExistingCRRequeues10m`.

- [ ] **Step 5: Regenerate RBAC manifests and verify scope**

Run: `make manifests && grep -A5 'resources:' config/rbac/role.yaml | grep -E 'dualdeploymentoperators|secrets'`
Expected: role grants on `dualdeploymentoperators` (+ `/status`, `/finalizers`) and `secrets`, nothing else cluster-scoped.

- [ ] **Step 6: Commit**

```bash
git add internal/controller config/rbac
git commit -m "feat: no-op reconciler that logs and requeues after 10m"
```

- [x] Task 6 complete

---

## Task 7: No-op validating webhook (methods return nil)

**Files:**
- Modify: `internal/webhook/v1alpha1/dualdeploymentoperator_webhook.go`

- [ ] **Step 1: Ensure CustomValidator methods return nil**

Edit `internal/webhook/v1alpha1/dualdeploymentoperator_webhook.go` so `ValidateCreate`, `ValidateUpdate`, and `ValidateDelete` each return `(nil, nil)` immediately (kubebuilder's default stub already does this, but confirm no TODO panics remain):

```go
func (v *DualDeploymentOperatorCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *DualDeploymentOperatorCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *DualDeploymentOperatorCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 3: Verify default overlay excludes webhook + certmanager**

Run: `kustomize build config/default 2>/dev/null | grep -cE 'kind: ValidatingWebhookConfiguration|kind: Certificate|kind: Issuer' || echo 0`
Expected: `0` (webhook/cert resources not rendered in the default overlay).

- [ ] **Step 4: Commit**

```bash
git add internal/webhook
git commit -m "chore: keep validating webhook scaffolded but no-op and not deployed"
```

- [x] Task 7 complete

---

## Task 8: envtest suite bootstrap + CRD install

**Files:**
- Modify: `internal/controller/suite_test.go` (kubebuilder scaffold; ensure it loads the CRD and pins K8s version)
- Modify: `Makefile` (pin `ENVTEST_K8S_VERSION`)

- [ ] **Step 1: Pin ENVTEST_K8S_VERSION to 1.29+ in the Makefile**

Edit `Makefile`: set `ENVTEST_K8S_VERSION = 1.31.0` (or the latest 1.29+ available via `setup-envtest list`). Verify the variable appears near the top.

Run: `grep -E '^ENVTEST_K8S_VERSION' Makefile`
Expected: one line, version ≥ 1.29.

- [ ] **Step 2: Confirm suite_test.go points CRDDirectoryPaths at config/crd/bases**

Inspect `internal/controller/suite_test.go`; confirm `testEnv = &envtest.Environment{ CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true }` and that `ddov1alpha1.AddToScheme(scheme)` is registered. Adjust if the scaffold used a different relative path.

- [ ] **Step 3: Run the envtest suite (bootstrap only)**

Run: `make test 2>&1 | tail -30`
Expected: PASS — envtest starts, CRD installs, existing controller tests pass. This confirms the CRD is Established in envtest.

- [ ] **Step 4: Commit**

```bash
git add Makefile internal/controller/suite_test.go
git commit -m "test: pin envtest to 1.29+ and bootstrap CRD install"
```

- [x] Task 8 complete

---

## Task 9: envtest CEL acceptance/rejection tests (RED → GREEN via existing CRD)

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller_test.go` (add envtest-backed CEL cases) OR create `internal/controller/cel_validation_test.go`

> These tests run against the real envtest API server (1.29+), which enforces the CEL rules embedded in the CRD from Task 5. They are GREEN immediately if Task 5's CEL markers are correct; if any fail, fix the marker in `api/v1alpha1/dualdeploymentoperator_types.go` and re-run `make manifests`.

- [ ] **Step 1: Write envtest CEL cases**

Create `internal/controller/cel_validation_test.go`:

```go
package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

// k8sClient and ctx are provided by suite_test.go (kubebuilder envtest scaffold).

func applyCR(t *testing.T, name string, spec ddov1alpha1.DualDeploymentOperatorSpec) error {
	t.Helper()
	cr := &ddov1alpha1.DualDeploymentOperator{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       spec,
	}
	return k8sClient.Create(context.Background(), cr)
}

func validHelm() ddov1alpha1.Source {
	return ddov1alpha1.Source{Helm: &ddov1alpha1.HelmSource{Repo: "oci://x", Name: "y", Version: "1.0.0"}}
}
func kc() ddov1alpha1.RemoteKubeconfigRef {
	return ddov1alpha1.RemoteKubeconfigRef{SecretName: "kc", Key: "kubeconfig"}
}

func TestCEL_SourceNeither_Rejected(t *testing.T) {
	err := applyCR(t, "cel-src-neither", ddov1alpha1.DualDeploymentOperatorSpec{Source: ddov1alpha1.Source{}, RemoteKubeconfig: kc()})
	if err == nil {
		t.Fatal("expected rejection when neither source variant set")
	}
}

func TestCEL_SourceBoth_Rejected(t *testing.T) {
	err := applyCR(t, "cel-src-both", ddov1alpha1.DualDeploymentOperatorSpec{
		Source: ddov1alpha1.Source{
			Helm:      &ddov1alpha1.HelmSource{Repo: "oci://x", Name: "y", Version: "1.0.0"},
			Kustomize: &ddov1alpha1.KustomizeSource{URL: "https://g//p?ref=v1", HostPath: "host", RemotePath: "remote"},
		},
		RemoteKubeconfig: kc(),
	})
	if err == nil {
		t.Fatal("expected rejection when both source variants set")
	}
}

func TestCEL_HelmValid_Accepted(t *testing.T) {
	if err := applyCR(t, "cel-helm-ok", ddov1alpha1.DualDeploymentOperatorSpec{Source: validHelm(), RemoteKubeconfig: kc()}); err != nil {
		t.Fatalf("expected acceptance, got %v", err)
	}
}

func TestCEL_KustomizeNoRef_Rejected(t *testing.T) {
	err := applyCR(t, "cel-kust-noref", ddov1alpha1.DualDeploymentOperatorSpec{
		Source:           ddov1alpha1.Source{Kustomize: &ddov1alpha1.KustomizeSource{URL: "https://g//p", HostPath: "host", RemotePath: "remote"}},
		RemoteKubeconfig: kc(),
	})
	if err == nil {
		t.Fatal("expected rejection when kustomize url lacks ?ref=")
	}
}

func TestCEL_KustomizeMissingHostPath_Rejected(t *testing.T) {
	err := applyCR(t, "cel-kust-nohost", ddov1alpha1.DualDeploymentOperatorSpec{
		Source:           ddov1alpha1.Source{Kustomize: &ddov1alpha1.KustomizeSource{URL: "https://g//p?ref=v1", RemotePath: "remote"}},
		RemoteKubeconfig: kc(),
	})
	if err == nil {
		t.Fatal("expected rejection when hostPath missing")
	}
}

func TestCEL_TransformationEmpty_Rejected(t *testing.T) {
	err := applyCR(t, "cel-tf-empty", ddov1alpha1.DualDeploymentOperatorSpec{
		Source: validHelm(), RemoteKubeconfig: kc(),
		Transformations: []ddov1alpha1.Transformation{{}},
	})
	if err == nil {
		t.Fatal("expected rejection for empty transformation entry")
	}
}

func TestCEL_TransformationTwoFields_Rejected(t *testing.T) {
	err := applyCR(t, "cel-tf-two", ddov1alpha1.DualDeploymentOperatorSpec{
		Source: validHelm(), RemoteKubeconfig: kc(),
		Transformations: []ddov1alpha1.Transformation{{
			FilterKinds:       &ddov1alpha1.FilterKindsSpec{Kinds: []string{"Service"}},
			RewriteWebhookURL: &ddov1alpha1.RewriteWebhookURLSpec{URLPrefix: "https://x:443"},
		}},
	})
	if err == nil {
		t.Fatal("expected rejection for two transformation fields in one entry")
	}
}

func TestCEL_PatchBothVariants_Rejected(t *testing.T) {
	raw := apiextensionsv1.JSON{Raw: []byte(`{"metadata":{"labels":{"a":"b"}}}`)}
	err := applyCR(t, "cel-patch-both", ddov1alpha1.DualDeploymentOperatorSpec{
		Source: validHelm(), RemoteKubeconfig: kc(),
		Transformations: []ddov1alpha1.Transformation{{
			Patch: &ddov1alpha1.PatchSpec{
				Target:         ddov1alpha1.Selector{Kind: "Deployment"},
				StrategicMerge: &raw,
				JSONPatch:      []ddov1alpha1.JSONPatchOp{{Op: "add", Path: "/x"}},
			},
		}},
	})
	if err == nil {
		t.Fatal("expected rejection when both patch variants set")
	}
}

func TestCEL_DeletionPolicyDefaultsRetain(t *testing.T) {
	name := "cel-delpol-default"
	if err := applyCR(t, name, ddov1alpha1.DualDeploymentOperatorSpec{Source: validHelm(), RemoteKubeconfig: kc()}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var got ddov1alpha1.DualDeploymentOperator
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "default"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec.DeletionPolicy.CRDs != "Retain" {
		t.Fatalf("expected DeletionPolicy.CRDs=Retain, got %q", got.Spec.DeletionPolicy.CRDs)
	}
}
```

> Add the missing import `"sigs.k8s.io/controller-runtime/pkg/client"` to the test file for the `client.ObjectKey` usage in `TestCEL_DeletionPolicyDefaultsRetain`. `k8sClient` and any `ctx` come from `suite_test.go`.

- [ ] **Step 2: Run the envtest CEL suite**

Run: `make test 2>&1 | tail -40`
Expected: PASS for all `TestCEL_*` cases. If a rejection case unexpectedly passes (CR accepted), the corresponding CEL marker is wrong or envtest is < 1.29 — fix the marker in the types file, re-run `make manifests`, re-run.

- [ ] **Step 3: Commit**

```bash
git add internal/controller/cel_validation_test.go
git commit -m "test: envtest coverage for CEL discriminator + defaulting rules"
```

- [x] Task 9 complete

---

## Task 10: Boilerplate header + full verification gate

**Files:**
- Modify: `hack/boilerplate.go.txt` (SPDX header)
- Verify: whole repo

- [ ] **Step 1: Set the SPDX boilerplate header**

Set `hack/boilerplate.go.txt` to an SPDX header for SAP-cloud-infrastructure, e.g.:

```
/*
SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and dual-deployment-operator contributors
SPDX-License-Identifier: Apache-2.0
*/
```

- [ ] **Step 2: Regenerate so generated files pick up the header**

Run: `make generate manifests && head -4 api/v1alpha1/zz_generated.deepcopy.go`
Expected: the generated file begins with the SPDX header.

- [ ] **Step 3: Full gate — fmt, vet, build, test, manifests clean**

Run:
```bash
make fmt vet build
make test
make manifests generate
git diff --exit-code config/ api/v1alpha1/zz_generated.deepcopy.go
```
Expected: `make fmt vet build` PASS; `make test` PASS (unit + all envtest incl. CEL); `git diff --exit-code` returns 0 (manifests/deepcopy already committed and up to date). If the diff is non-empty, commit the regenerated files.

- [ ] **Step 4: Verify Makefile targets exist**

Run: `for t in manifests generate fmt vet test build run docker-build install uninstall deploy undeploy; do make -n $t >/dev/null 2>&1 && echo "$t OK" || echo "$t MISSING"; done`
Expected: all `OK`.

- [ ] **Step 5: Commit**

```bash
git add hack/boilerplate.go.txt api config
git commit -m "chore: SPDX boilerplate header on generated files"
```

- [x] Task 10 complete

---

## Task 11: OpenSpec doc-deviation reconciliation (HostPath/RemotePath)

**Files:**
- Modify: `docs/design.md` §3.3, `docs/implementation.md` §Phase 1

> The design decision "KustomizeSource.HostPath and RemotePath are both required, no defaults" deviates from the docs, which describe them as optional with defaults. Update the docs to match the implemented behavior so future readers aren't misled.

- [ ] **Step 1: Update docs/design.md §3.3**

Change the `hostPath`/`remotePath` description from "optional; default `host`/`remote`" to "required; no default — must be set explicitly per CR". Update the ipam-capi CR example if it relied on the default.

- [ ] **Step 2: Update docs/implementation.md §Phase 1**

Change the `KustomizeSource` Go struct comment and field tags to show `hostPath`/`remotePath` as required (`MinLength=1`, no `omitempty`, no default).

- [ ] **Step 3: Verify no other doc references the defaults**

Run: `grep -rn 'default.*host.*remote\|hostPath.*optional\|remotePath.*optional' docs/ || echo "CLEAN"`
Expected: `CLEAN` or only intentional historical mentions.

- [ ] **Step 4: Commit**

```bash
git add docs/design.md docs/implementation.md
git commit -m "docs: mark kustomize hostPath/remotePath as required (match implementation)"
```

- [x] Task 11 complete

---

## Verification against specs (self-review checklist for the executor)

Before marking the change done, confirm each spec requirement maps to a task:

- **operator-scaffold**: kubebuilder init/create (Task 1-2), directory layout (Task 1-2), go.mod deps (Task 3), manager Deployment (Task 2 scaffold), RBAC namespace-scoped (Task 6), Makefile targets + envtest pin (Task 8, 10), Dockerfile (Task 1), boilerplate (Task 10).
- **crd-types**: CRD registration (Task 5), all Spec/Status types + fields (Task 5), deepcopy (Task 5), group version registration (Task 1-2 scaffold + Task 5), round-trip tests (Task 4-5).
- **cel-admission-validation**: Source/Transformation/PatchSpec/kustomize-URL CEL (Task 5 markers, Task 9 tests), webhook-not-deployed (Task 7), K8s 1.29+ (Task 8).
- **noop-reconciler**: watch/SetupWithManager (Task 6), missing-CR (Task 6), 10-min requeue + one log line (Task 6), no-mutation (implicit — reconciler writes nothing; Task 6), health/metrics endpoints + leader-election flag (Task 2 scaffold defaults, verified by `make test` manager bootstrap in Task 8).
