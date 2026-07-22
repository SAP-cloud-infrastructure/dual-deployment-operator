<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Dual-Cluster Delivery Reconciler Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking; the trailing `- [ ]` after each task marks that whole task group complete — check it off only after the final step's commit lands.

**Goal:** Implement Phases 5 (delivery) and 6 (reconciler) — the dual-cluster server-side-apply delivery layer plus the reconcile loop that renders twice, transforms, applies per `spec.applyOrder`, prunes orphans, and reports status — replacing the no-op reconciler.

**Architecture:** Two new packages plus a rewired controller. `internal/deliver` holds a stateless `SSAApplier` (SSA with `ForceOwnership`, caBundle-safe apply, GET-after-apply health, cluster-scoped conflict guard, idempotent delete) plus pure `order.go`/`health.go`. `internal/clients` holds the host (in-cluster) and shoot (token+CA-from-Secret) client factories. The reconciler orchestrates render→transform→sort→apply→prune→status with a finalizer for deletion. A prerequisite CRD-types rename (`deletionPolicy`→`retentionPolicy`, `remoteKubeconfig`→`remoteAccess`, add `applyOrder`) lands first because the reconciler references the new field names.

**Tech Stack:** Go 1.26, controller-runtime (`sigs.k8s.io/controller-runtime`), `k8s.io/apimachinery` unstructured, kubebuilder markers, Ginkgo/Gomega + envtest for controller tests, plain `testing` for unit tests. Existing packages reused: `internal/manifest` (Manifest, Origin, `IsClusterScoped`, `ApplyNamespace`), `internal/source` (`From`, `Render`, `ModeHost`/`ModeRemote`), `internal/transform` (`Build`, `Apply`).

---

## Task 1: CRD types rename + applyOrder (prerequisite)

**Files:**
- Modify: `api/v1alpha1/dualdeploymentoperator_types.go`
- Test: `api/v1alpha1/dualdeploymentoperator_types_test.go`
- Regenerate: `api/v1alpha1/zz_generated.deepcopy.go`, `config/crd/bases/*.yaml` (via `make manifests generate`)

Rename `spec.deletionPolicy`→`spec.retentionPolicy` (type `DeletionPolicy`→`RetentionPolicy`, keep `.crds`), rename `spec.remoteKubeconfig`→`spec.remoteAccess` (type `RemoteKubeconfigRef`→`RemoteAccessRef`, new fields `SecretName`/`Server`/`TokenKey`/`CAKey`), add `spec.applyOrder`.

- [ ] **Step 1: Write failing test for the renamed/added fields**

Append to `api/v1alpha1/dualdeploymentoperator_types_test.go` (name follows the existing `...RoundTrip` convention — describes the behavior under test, the spec's fields hold their values, not the migration that produced them):

```go
func TestSpecFieldsRoundTrip(t *testing.T) {
	s := DualDeploymentOperatorSpec{
		RemoteAccess:    RemoteAccessRef{SecretName: "kc", Server: "https://api.example:443"},
		RemoteNamespace: "shoot--x--y",
		RetentionPolicy: RetentionPolicy{CRDs: "Retain"},
		ApplyOrder:      "HostFirst",
	}
	if s.RetentionPolicy.CRDs != "Retain" {
		t.Errorf("RetentionPolicy.CRDs = %q, want Retain", s.RetentionPolicy.CRDs)
	}
	if s.RemoteAccess.Server != "https://api.example:443" {
		t.Errorf("RemoteAccess.Server = %q", s.RemoteAccess.Server)
	}
	if s.ApplyOrder != "HostFirst" {
		t.Errorf("ApplyOrder = %q, want HostFirst", s.ApplyOrder)
	}
}
```

- [ ] **Step 2: Run test to verify it fails (compile error)**

Run: `go test ./api/v1alpha1/ -run TestSpecFieldsRoundTrip`
Expected: FAIL — `RemoteAccess`, `RemoteAccessRef`, `RetentionPolicy`, `ApplyOrder` undefined.

- [ ] **Step 3: Apply the type changes**

In `api/v1alpha1/dualdeploymentoperator_types.go`, replace the `RemoteKubeconfig` field and `DeletionPolicy` field in `DualDeploymentOperatorSpec`, and add `ApplyOrder`:

```go
	Source          Source          `json:"source"`
	RemoteAccess    RemoteAccessRef `json:"remoteAccess"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	RemoteNamespace string `json:"remoteNamespace"`
	// +optional
	Transformations []Transformation `json:"transformations,omitempty"`
	// +optional
	// +kubebuilder:default={crds:Retain}
	RetentionPolicy RetentionPolicy `json:"retentionPolicy,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=HostFirst;RemoteFirst
	// +kubebuilder:default=RemoteFirst
	ApplyOrder string `json:"applyOrder,omitempty"`
```

Replace the `RemoteKubeconfigRef` struct with:

```go
type RemoteAccessRef struct {
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName"`
	// +kubebuilder:validation:MinLength=1
	Server string `json:"server"`
	// +optional
	TokenKey string `json:"tokenKey,omitempty"`
	// +optional
	CAKey string `json:"caKey,omitempty"`
}
```

Rename the `DeletionPolicy` struct to `RetentionPolicy` (keep the `CRDs` field, its enum, and default markers unchanged).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./api/v1alpha1/ -run TestSpecFieldsRoundTrip`
Expected: PASS.

- [ ] **Step 5: Regenerate manifests + deepcopy, confirm build**

Run: `make manifests generate && go build ./...`
Expected: exit 0; `config/crd/bases/*.yaml` shows `remoteAccess`/`retentionPolicy`/`applyOrder`; `zz_generated.deepcopy.go` references `RemoteAccessRef`/`RetentionPolicy`.

- [ ] **Step 6: Commit**

```bash
git add api/v1alpha1/ config/crd/bases/
git commit -m "feat(api): rename deletionPolicy->retentionPolicy, remoteKubeconfig->remoteAccess; add applyOrder"
```

- [x] Task 1 complete

---

## Task 2: manifest helpers — StripInternalAnnotations, owned-by label, AsManifest

**Files:**
- Modify: `internal/manifest/manifest.go`
- Test: `internal/manifest/manifest_test.go`

Add three helpers the delivery layer needs, kept in `manifest` to avoid an import cycle: strip the origin annotation before apply; stamp/read the CR-identity `owned-by` label; rebuild a minimal Manifest from a `ResourceStatus` for deletion.

- [ ] **Step 1: Write failing tests**

Append to `internal/manifest/manifest_test.go`:

```go
func TestStripInternalAnnotationsRemovesOriginAndEmptyMap(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "c", "annotations": map[string]any{OriginAnnotation: "additions"}},
	}}
	m := Manifest{Unstructured: u, Origin: OriginAdditions}
	m.StripInternalAnnotations()
	if _, found, _ := unstructured.NestedMap(u.Object, "metadata", "annotations"); found {
		t.Error("annotations map should be nil after removing the only key")
	}
}

func TestOwnedByValueIsFixedLengthAndInjective(t *testing.T) {
	v := OwnedByValue("ns", "name")
	if len(v) != 16 {
		t.Errorf("OwnedByValue len = %d, want 16 (fits the 63-char label-value limit)", len(v))
	}
	// Injective across the ns/name boundary: (ns="a", name="b_c") must differ from
	// (ns="a_b", name="c"). A raw "<ns>_<name>" join would collide here.
	if OwnedByValue("a", "b_c") == OwnedByValue("a_b", "c") {
		t.Error("OwnedByValue must not collide across the namespace/name boundary")
	}
	// Stable: same identity -> same value (survives restarts, re-reconciles,
	// and same-name re-creation, enabling re-adoption of retained objects).
	if OwnedByValue("ns", "name") != OwnedByValue("ns", "name") {
		t.Error("OwnedByValue must be deterministic for a given namespace/name")
	}
}

func TestSetAndGetOwnedByLabel(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ClusterRole", "metadata": map[string]any{"name": "cr"}}}
	m := Manifest{Unstructured: u}
	want := OwnedByValue("ns", "name")
	m.SetOwnedByLabel(want)
	if u.GetLabels()[OwnedByLabel] != want {
		t.Errorf("owned-by label = %q, want %q", u.GetLabels()[OwnedByLabel], want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/manifest/ -run 'TestStripInternalAnnotations|TestOwnedByValue|TestSetAndGetOwnedByLabel'`
Expected: FAIL — `StripInternalAnnotations`, `SetOwnedByLabel`, `OwnedByValue`, `OwnedByLabel` undefined.

- [ ] **Step 3: Implement the helpers**

Add to `internal/manifest/manifest.go` (add `crypto/sha256` and `encoding/hex` imports):

```go
// OwnedByLabel is the CR-identity ownership label key stamped on every applied
// object. Its VALUE is OwnedByValue(namespace, name) — a fixed-length hash, NOT the
// raw "<ns>_<name>", because a Kubernetes label value is capped at 63 chars (a raw
// join of a shoot-cp namespace + operator name overflows that) and a single-"_" join
// is non-injective. Reused by prune safety and the cluster-scoped conflict guard.
const OwnedByLabel = "dual-deployment-operator.cc.sap/owned-by"

// OwnedByValue derives the stable, fixed-length ownership label value for a CR from
// its namespace and name. The value is the first 16 hex chars (64 bits) of
// sha256("<namespace>/<name>"): well under the 63-char label-value limit, injective
// across the ns/name boundary (the "/" separator cannot appear in either DNS-1123
// component), and deterministic — so it survives operator restarts, re-reconciles,
// and same-name CR re-creation (enabling re-adoption of retained objects).
func OwnedByValue(namespace, name string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + name))
	return hex.EncodeToString(sum[:])[:16]
}

// StripInternalAnnotations removes the origin authorship annotation before apply
// and drops the annotations map entirely if it becomes empty.
func (m Manifest) StripInternalAnnotations() {
	anns := m.Unstructured.GetAnnotations()
	if anns == nil {
		return
	}
	delete(anns, OriginAnnotation)
	if len(anns) == 0 {
		m.Unstructured.SetAnnotations(nil)
		return
	}
	m.Unstructured.SetAnnotations(anns)
}

// SetOwnedByLabel stamps the CR-identity ownership label. The caller passes the value
// from OwnedByValue(cr.Namespace, cr.Name).
func (m Manifest) SetOwnedByLabel(ownedBy string) {
	labels := m.Unstructured.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[OwnedByLabel] = ownedBy
	m.Unstructured.SetLabels(labels)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/manifest/ -run 'TestStripInternalAnnotations|TestOwnedByValue|TestSetAndGetOwnedByLabel'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/manifest/
git commit -m "feat(manifest): add StripInternalAnnotations, SetOwnedByLabel, OwnedByValue hash, OwnedByLabel"
```

- [x] Task 2 complete

---

## Task 3: deliver — fixed intra-render ordering (pure functions)

**Files:**
- Create: `internal/deliver/order.go`
- Test: `internal/deliver/order_test.go`

Sort a manifest set by fixed kind priority for apply (Namespace → CRD → RBAC → other → webhooks) and the exact reverse for delete. Not consumer-configurable.

- [ ] **Step 1: Write failing test**

Create `internal/deliver/order_test.go`:

```go
package deliver

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func man(kind string) manifest.Manifest {
	return manifest.Manifest{Unstructured: &unstructured.Unstructured{Object: map[string]any{"kind": kind}}}
}

func kinds(ms []manifest.Manifest) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Unstructured.GetKind()
	}
	return out
}

func TestSortForApplyOrdersDependenciesFirst(t *testing.T) {
	in := []manifest.Manifest{man("ValidatingWebhookConfiguration"), man("Deployment"), man("ClusterRole"), man("CustomResourceDefinition"), man("Namespace")}
	got := kinds(SortForApply(in))
	want := []string{"Namespace", "CustomResourceDefinition", "ClusterRole", "Deployment", "ValidatingWebhookConfiguration"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SortForApply = %v, want %v", got, want)
		}
	}
}

func TestSortForDeleteIsReverseOfApply(t *testing.T) {
	in := []manifest.Manifest{man("Namespace"), man("CustomResourceDefinition"), man("Deployment")}
	applied := kinds(SortForApply(in))
	deleted := kinds(SortForDelete(in))
	for i := range applied {
		if applied[i] != deleted[len(deleted)-1-i] {
			t.Fatalf("delete %v is not reverse of apply %v", deleted, applied)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/deliver/ -run TestSort`
Expected: FAIL — `SortForApply`/`SortForDelete` undefined.

- [ ] **Step 3: Implement ordering**

Create `internal/deliver/order.go`:

```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

// Package deliver applies a manifest set to a target cluster via server-side apply.
package deliver

import (
	"sort"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// applyPriority is the fixed intra-render apply order. Lower sorts first.
// Unlisted kinds fall into the "other" tier (priority 3), stable within it.
//
// Tiers rank by DEPENDENCY ("what must exist before what"), NOT by cluster-vs-
// namespaced scope — the two scopes are interleaved across tiers by that logic
// (Namespace and CRD are cluster-scoped and go early because namespaced objects /
// CRs depend on them; webhooks are cluster-scoped and go last so admission does not
// intercept the earlier applies).
//   - Namespace (0): namespaced objects need their namespace first.
//   - CRD (1): a CR of that kind cannot apply until its CRD is Established.
//   - RBAC (2): one FLAT tier on purpose — ClusterRole/Role/binding/SA have no
//     create-time ordering dependency, because Kubernetes late-binds roleRef and
//     subjects (resolved by the authorizer at request time, not at binding create).
//     A binding applied before its role/SA is valid; it simply grants nothing until
//     the role/SA lands (same reconcile). Do NOT split this into role-before-binding
//     sub-tiers — that guards a failure mode Kubernetes does not have.
//   - other (3): workloads/config that consume the above.
//   - webhook configs (4): applied last so admission webhooks do not intercept or
//     reject the earlier resources in this same render.
func applyPriority(kind string) int {
	switch kind {
	case "Namespace":
		return 0
	case "CustomResourceDefinition":
		return 1
	case "ClusterRole", "ClusterRoleBinding", "Role", "RoleBinding", "ServiceAccount":
		return 2
	case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
		return 4
	default:
		return 3
	}
}

// SortForApply returns a new slice sorted by the fixed kind priority (stable).
func SortForApply(ms []manifest.Manifest) []manifest.Manifest {
	out := append([]manifest.Manifest(nil), ms...)
	sort.SliceStable(out, func(i, j int) bool {
		return applyPriority(out[i].Unstructured.GetKind()) < applyPriority(out[j].Unstructured.GetKind())
	})
	return out
}

// SortForDelete returns the exact reverse of SortForApply.
func SortForDelete(ms []manifest.Manifest) []manifest.Manifest {
	applied := SortForApply(ms)
	out := make([]manifest.Manifest, len(applied))
	for i := range applied {
		out[len(applied)-1-i] = applied[i]
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/deliver/ -run TestSort`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/deliver/order.go internal/deliver/order_test.go
git commit -m "feat(deliver): fixed intra-render apply/delete ordering"
```

- [x] Task 3 complete

---

## Task 4: deliver — per-kind health computation (pure functions)

**Files:**
- Create: `internal/deliver/health.go`
- Test: `internal/deliver/health_test.go`

Compute `HealthState` from a live object: Deployment/StatefulSet by replicas, CRD by `Established`, everything else exists=Healthy. WebhookConfigurations are NOT special-cased (never read caBundle). A nil object → Unknown.

- [ ] **Step 1: Write failing test**

Create `internal/deliver/health_test.go`:

```go
package deliver

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

func TestDeploymentHealthProgressingUntilAvailable(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"kind": "Deployment",
		"spec": map[string]any{"replicas": int64(2)},
		"status": map[string]any{"availableReplicas": int64(1)},
	}}
	if got := computeHealth(u); got != ddov1alpha1.HealthProgressing {
		t.Errorf("health = %q, want Progressing", got)
	}
	u.Object["status"] = map[string]any{"availableReplicas": int64(2)}
	if got := computeHealth(u); got != ddov1alpha1.HealthHealthy {
		t.Errorf("health = %q, want Healthy", got)
	}
}

func TestWebhookConfigHealthIsExistenceOnly(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"kind": "ValidatingWebhookConfiguration"}}
	if got := computeHealth(u); got != ddov1alpha1.HealthHealthy {
		t.Errorf("health = %q, want Healthy (existence-only, caBundle ignored)", got)
	}
}

func TestNilObjectIsUnknown(t *testing.T) {
	if got := computeHealth(nil); got != ddov1alpha1.HealthUnknown {
		t.Errorf("health = %q, want Unknown", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/deliver/ -run 'TestDeploymentHealth|TestWebhookConfigHealth|TestNilObject'`
Expected: FAIL — `computeHealth` undefined.

- [ ] **Step 3: Implement health**

Create `internal/deliver/health.go`:

```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package deliver

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

func computeHealth(u *unstructured.Unstructured) ddov1alpha1.HealthState {
	if u == nil {
		return ddov1alpha1.HealthUnknown
	}
	switch u.GetKind() {
	case "Deployment", "StatefulSet":
		return replicaHealth(u)
	case "CustomResourceDefinition":
		return crdHealth(u)
	// Only kinds with an async readiness signal the operator can observe are graded
	// deeper (replicas above, CRD Established above). Everything else is existence-only:
	//   - Service: no candidate render delivers a LoadBalancer Service (all profiles
	//     filterKinds out Service; only ClusterIP-family would survive, which is ready
	//     on creation). Do NOT add a Service case — LoadBalancer grading would be dead
	//     code and ClusterIP is already correct here.
	//   - ValidatingWebhookConfiguration / MutatingWebhookConfiguration: the operator
	//     does not own caBundle, so it must not grade health on it (§caBundle strip).
	// All fall through to exists=Healthy.
	default:
		return ddov1alpha1.HealthHealthy
	}
}

func replicaHealth(u *unstructured.Unstructured) ddov1alpha1.HealthState {
	replicas, _, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
	available, _, _ := unstructured.NestedInt64(u.Object, "status", "availableReplicas")
	if available >= replicas {
		return ddov1alpha1.HealthHealthy
	}
	return ddov1alpha1.HealthProgressing
}

func crdHealth(u *unstructured.Unstructured) ddov1alpha1.HealthState {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cm["type"] == "Established" && cm["status"] == "True" {
			return ddov1alpha1.HealthHealthy
		}
	}
	return ddov1alpha1.HealthProgressing
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/deliver/ -run 'TestDeploymentHealth|TestWebhookConfigHealth|TestNilObject'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/deliver/health.go internal/deliver/health_test.go
git commit -m "feat(deliver): per-kind health computation (caBundle-agnostic webhooks)"
```

- [x] Task 4 complete

---

## Task 5: deliver — caBundle leaf-only strip (pure functions)

**Files:**
- Create: `internal/deliver/cabundle.go`
- Test: `internal/deliver/cabundle_test.go`

Remove only the `caBundle` leaf from webhook configs and conversion-webhook CRDs, unconditionally, preserving `clientConfig.url`. Guarantees the operator never owns caBundle.

- [ ] **Step 1: Write failing test**

Create `internal/deliver/cabundle_test.go`:

```go
package deliver

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestStripCABundleFromWebhooksLeafOnly(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"kind": "ValidatingWebhookConfiguration",
		"webhooks": []any{map[string]any{
			"clientConfig": map[string]any{"url": "https://x/y", "caBundle": "AAAA"},
		}},
	}}
	stripCABundleFromWebhooks(u)
	wh := u.Object["webhooks"].([]any)[0].(map[string]any)
	cc := wh["clientConfig"].(map[string]any)
	if _, present := cc["caBundle"]; present {
		t.Error("caBundle leaf should be removed")
	}
	if cc["url"] != "https://x/y" {
		t.Error("clientConfig.url must be preserved")
	}
}

func TestStripCABundleUnconditionalNoOp(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"kind":     "ValidatingWebhookConfiguration",
		"webhooks": []any{map[string]any{"clientConfig": map[string]any{"url": "https://x/y"}}},
	}}
	stripCABundleFromWebhooks(u) // must not panic, no-op
	wh := u.Object["webhooks"].([]any)[0].(map[string]any)
	if wh["clientConfig"].(map[string]any)["url"] != "https://x/y" {
		t.Error("no-op strip damaged the object")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/deliver/ -run TestStripCABundle`
Expected: FAIL — `stripCABundleFromWebhooks` undefined.

- [ ] **Step 3: Implement strip**

Create `internal/deliver/cabundle.go`:

```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package deliver

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// prepareForApply strips the caBundle leaf from webhook configs / conversion CRDs.
func prepareForApply(u *unstructured.Unstructured) {
	switch u.GetKind() {
	case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
		stripCABundleFromWebhooks(u)
	case "CustomResourceDefinition":
		stripCABundleFromCRDConversion(u)
	}
}

// stripCABundleFromWebhooks removes ONLY webhooks[].clientConfig.caBundle, on every
// entry, unconditionally. Never removes the parent clientConfig or the webhook entry.
func stripCABundleFromWebhooks(u *unstructured.Unstructured) {
	whs, found, _ := unstructured.NestedSlice(u.Object, "webhooks")
	if !found {
		return
	}
	for i := range whs {
		wh, ok := whs[i].(map[string]any)
		if !ok {
			continue
		}
		unstructured.RemoveNestedField(wh, "clientConfig", "caBundle")
		whs[i] = wh
	}
	_ = unstructured.SetNestedSlice(u.Object, whs, "webhooks")
}

// stripCABundleFromCRDConversion removes only spec.conversion.webhook.clientConfig.caBundle.
func stripCABundleFromCRDConversion(u *unstructured.Unstructured) {
	unstructured.RemoveNestedField(u.Object, "spec", "conversion", "webhook", "clientConfig", "caBundle")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/deliver/ -run TestStripCABundle`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/deliver/cabundle.go internal/deliver/cabundle_test.go
git commit -m "feat(deliver): leaf-only unconditional caBundle strip"
```

- [x] Task 5 complete

---

## Task 6: deliver — SSAApplier (apply, conflict guard, health, idempotent delete)

**Files:**
- Create: `internal/deliver/applier.go`
- Test: `internal/deliver/applier_test.go` (uses `sigs.k8s.io/controller-runtime/pkg/client/fake`)

The `Applier` interface + `SSAApplier`: SSA apply with `ForceOwnership`, origin-annotation strip, owned-by label stamp, caBundle strip, cluster-scoped conflict guard (GET-before-apply; refuse foreign-owned), GET-after-apply health, idempotent delete. `ResourceStatus` is the CRD type `ddov1alpha1.ResourceStatus`.

- [ ] **Step 1: Write failing tests**

Create `internal/deliver/applier_test.go`:

```go
package deliver

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// clusterRole builds a test ClusterRole. ownedBy is an OPAQUE ownership value (in
// production it is manifest.OwnedByValue(ns,name), a hash) — these tests only need two
// distinct owner strings, so "owner-a"/"owner-b" stand in as opaque identities; the
// applier compares them as strings and never parses or hashes them.
func clusterRole(name, ownedBy string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"})
	u.SetName(name)
	if ownedBy != "" {
		u.SetLabels(map[string]string{manifest.OwnedByLabel: ownedBy})
	}
	return u
}

func TestApplyStampsOwnedByAndReturnsStatus(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	a := &SSAApplier{Client: c, FieldManager: "dual-deployment-operator", Cluster: "host"}
	m := manifest.Manifest{Unstructured: clusterRole("cr", "")}
	st, err := a.Apply(context.Background(), m, "owner-a")
	if err != nil {
		t.Fatalf("Apply err = %v", err)
	}
	if st.Kind != "ClusterRole" || st.Name != "cr" {
		t.Errorf("status = %+v", st)
	}
}

func TestApplyRefusesForeignOwnedClusterScoped(t *testing.T) {
	existing := clusterRole("cr", "owner-b") // owned by a DIFFERENT CR
	c := fake.NewClientBuilder().WithObjects(existing).Build()
	a := &SSAApplier{Client: c, FieldManager: "dual-deployment-operator", Cluster: "host"}
	m := manifest.Manifest{Unstructured: clusterRole("cr", "")}
	st, err := a.Apply(context.Background(), m, "owner-a")
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if st.Health != ddov1alpha1.HealthDegraded {
		t.Errorf("health = %q, want Degraded", st.Health)
	}
}

func TestDeleteIgnoresNotFound(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	a := &SSAApplier{Client: c, FieldManager: "dual-deployment-operator", Cluster: "host"}
	if err := a.Delete(context.Background(), manifest.Manifest{Unstructured: clusterRole("gone", "")}); err != nil {
		t.Errorf("Delete of absent object = %v, want nil", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/deliver/ -run 'TestApply|TestDelete'`
Expected: FAIL — `SSAApplier`, `Applier` undefined.

- [ ] **Step 3: Implement the applier**

Create `internal/deliver/applier.go`:

```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package deliver

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// Applier abstracts writing/deleting a single manifest on a target cluster. ownedBy is
// the caller's per-CR ownership value (manifest.OwnedByValue(cr.Namespace, cr.Name)),
// passed as an argument rather than held on the applier so the applier stays stateless
// and one instance is safely reused across concurrent reconciles of different CRs.
type Applier interface {
	Apply(ctx context.Context, m manifest.Manifest, ownedBy string) (ddov1alpha1.ResourceStatus, error)
	Delete(ctx context.Context, m manifest.Manifest) error
}

// SSAApplier is a stateless server-side-apply Applier. It holds only the client, field
// manager, and cluster label — NO per-reconcile mutable state — so a single instance is
// reused across reconciles (and concurrent CRs) for the same cluster. The per-CR
// ownership value is passed to Apply as an argument, not stored on the struct.
type SSAApplier struct {
	Client       client.Client
	FieldManager string
	Cluster      string // "host" or "remote", for logging
}

var _ Applier = (*SSAApplier)(nil)

func (a *SSAApplier) Apply(ctx context.Context, m manifest.Manifest, ownedBy string) (ddov1alpha1.ResourceStatus, error) {
	m.StripInternalAnnotations()
	m.SetOwnedByLabel(ownedBy)
	prepareForApply(m.Unstructured)

	u := m.Unstructured
	status := ddov1alpha1.ResourceStatus{
		Kind: u.GetKind(), APIVersion: u.GetAPIVersion(),
		Namespace: u.GetNamespace(), Name: u.GetName(),
		LastApplied: &metav1.Time{Time: time.Now()},
	}

	// Defensive cluster-scoped conflict guard (cluster-scoped kinds only).
	if manifest.IsClusterScoped(u.GetKind()) {
		if owner, conflict := a.foreignOwner(ctx, u, ownedBy); conflict {
			status.Health = ddov1alpha1.HealthDegraded
			status.Message = fmt.Sprintf("cluster-scoped %s %q already owned by CR %q; refusing to overwrite (single-install-per-seed)", u.GetKind(), u.GetName(), owner)
			return status, fmt.Errorf("%s", status.Message)
		}
	}

	if err := a.Client.Patch(ctx, u, client.Apply, client.FieldOwner(a.FieldManager), client.ForceOwnership); err != nil {
		status.Health = ddov1alpha1.HealthDegraded
		status.Message = err.Error()
		return status, err
	}

	current := a.get(ctx, u)
	status.Health = computeHealth(current)
	if current == nil {
		status.Message = "apply succeeded but read-back GET failed"
	}
	return status, nil
}

func (a *SSAApplier) Delete(ctx context.Context, m manifest.Manifest) error {
	if err := a.Client.Delete(ctx, m.Unstructured); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// foreignOwner reports whether the live cluster-scoped object is owned by a CR OTHER
// than ownedBy.
func (a *SSAApplier) foreignOwner(ctx context.Context, u *unstructured.Unstructured, ownedBy string) (string, bool) {
	live := a.get(ctx, u)
	if live == nil {
		return "", false
	}
	owner := live.GetLabels()[manifest.OwnedByLabel]
	return owner, owner != "" && owner != ownedBy
}

// get reads the live object; returns nil on any error (caller treats nil as absent/Unknown).
func (a *SSAApplier) get(ctx context.Context, u *unstructured.Unstructured) *unstructured.Unstructured {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(u.GroupVersionKind())
	if err := a.Client.Get(ctx, client.ObjectKeyFromObject(u), live); err != nil {
		return nil
	}
	return live
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/deliver/ -run 'TestApply|TestDelete'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/deliver/applier.go internal/deliver/applier_test.go
git commit -m "feat(deliver): SSAApplier with conflict guard, health, idempotent delete"
```

- [x] Task 6 complete

---

## Task 7: clients — host and shoot client factories

**Files:**
- Create: `internal/clients/clients.go`
- Test: `internal/clients/clients_test.go`

Host factory returns a `client.Client` from the manager's REST config. Shoot factory builds a `rest.Config` from a token-requestor Secret (`{Host: server, BearerToken, CAData}`), with the three-state gate: missing Secret → fatal; token/CA absent-or-empty → `errShootCredentialsNotReady`; present+non-empty → build.

- [ ] **Step 1: Write failing test for the credentials gate**

Create `internal/clients/clients_test.go`:

```go
package clients

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestBuildShootRestConfigCredentialsNotReady(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{"token": {}, "bundle.crt": []byte("CA")}}
	_, err := ShootRESTConfig(secret, "token", "bundle.crt", "https://api:443")
	if !errors.Is(err, ErrShootCredentialsNotReady) {
		t.Errorf("err = %v, want ErrShootCredentialsNotReady", err)
	}
}

func TestBuildShootRestConfigReady(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{"token": []byte("tok"), "bundle.crt": []byte("CA")}}
	cfg, err := ShootRESTConfig(secret, "token", "bundle.crt", "https://api:443")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.Host != "https://api:443" || cfg.BearerToken != "tok" || string(cfg.TLSClientConfig.CAData) != "CA" {
		t.Errorf("cfg = %+v", cfg)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/clients/ -run TestBuildShootRestConfig`
Expected: FAIL — `ShootRESTConfig`, `ErrShootCredentialsNotReady` undefined.

- [ ] **Step 3: Implement the factories**

Create `internal/clients/clients.go`:

```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

// Package clients builds host (in-cluster) and shoot (token+CA-from-Secret) clients.
package clients

import (
	"errors"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ErrShootCredentialsNotReady signals the token-requestor Secret exists but Gardener
// has not populated token/CA yet (absent or empty). Benign; caller maps it to a wait.
var ErrShootCredentialsNotReady = errors.New("shoot credentials not yet populated")

// HostClient returns a client for the seed using the manager's REST config.
func HostClient(cfg *rest.Config, opts client.Options) (client.Client, error) {
	return client.New(cfg, opts)
}

// ShootRESTConfig builds a shoot rest.Config from a token-requestor Secret. Token and
// CA must both be present and non-empty; otherwise ErrShootCredentialsNotReady.
func ShootRESTConfig(secret *corev1.Secret, tokenKey, caKey, server string) (*rest.Config, error) {
	if tokenKey == "" {
		tokenKey = "token"
	}
	if caKey == "" {
		caKey = "bundle.crt"
	}
	token := secret.Data[tokenKey]
	caData := secret.Data[caKey]
	if len(token) == 0 || len(caData) == 0 {
		return nil, ErrShootCredentialsNotReady
	}
	return &rest.Config{
		Host:            server,
		BearerToken:     string(token),
		TLSClientConfig: rest.TLSClientConfig{CAData: caData},
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/clients/ -run TestBuildShootRestConfig`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/clients/
git commit -m "feat(clients): host + shoot client factories with three-state credentials gate"
```

- [x] Task 7 complete

---

## Task 8: reconciler — render→transform→sort→apply per applyOrder

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller.go`
- Test: `internal/controller/dualdeploymentoperator_controller_test.go` (envtest, Ginkgo)

Replace the no-op reconcile with the pipeline: fetch → finalizer → render×2 → transform → build shoot applier (three-state) → sort → apply per `applyOrder` with the RemoteFirst gate (any remote failure gates host) → status. Prune and deletion are Tasks 9–10.

- [ ] **Step 1: Write failing envtest for two-render delivery**

Add to `internal/controller/dualdeploymentoperator_controller_test.go` a spec that applies a CR with a fake/stub source and asserts `status.Ready` becomes populated and `status.hostResources`/`remoteResources` are non-empty. (Follow the existing `suite_test.go` envtest harness; use a source that renders a trivial ConfigMap per mode.)

```go
It("delivers host and remote renders and populates status", func() {
	cr := newTestCR("deliver-a", "shoot--x--y") // helper: valid Source, RemoteAccess, RemoteNamespace
	Expect(k8sClient.Create(ctx, cr)).To(Succeed())
	Eventually(func(g Gomega) {
		got := &ddov1alpha1.DualDeploymentOperator{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
		g.Expect(got.Status.Conditions).ToNot(BeEmpty())
		g.Expect(got.Status.LastReconcile).ToNot(BeNil())
	}).Should(Succeed())
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test` (or `go test ./internal/controller/ -run TestControllers`)
Expected: FAIL — no-op reconciler never writes status/conditions.

- [ ] **Step 3: Implement the main loop (apply path only; prune/delete stubbed)**

Rewrite `Reconcile` in `internal/controller/dualdeploymentoperator_controller.go` following `docs/implementation.md` Phase 6 §Main loop: add `FinalizerName`, `FieldManagerName`, `HostApplier deliver.Applier` and `Recorder record.EventRecorder` fields; derive the ownership value once per reconcile as `ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)` and thread it through every apply call (the appliers are stateless and shared — `ownedBy` is passed as an argument to `Apply`, never stored on the applier); render twice via `source.From(cr.Spec.Source, deps)` + `Render(ctx, mode, ns)`; `transform.Build` + apply to both; `buildShootApplier` (Task 7) returning the three-state phase; `deliver.SortForApply` each; the `remoteFirst` switch with the RemoteFirst gate (`anyFailed`/`allFailed` helpers); write status via `computeConditions`. Add helper funcs `errStatus`, `applyAll(ctx, applier, manifests, ownedBy)` (loops `applier.Apply(ctx, m, ownedBy)` over the set, continue-on-error, returns the `[]ResourceStatus`), `setCondition`, `finishNotReady`, `anyFailed`, `allFailed`, `computeConditions` in the same package; the `applyHost` closure captures `ownedBy` and calls `r.applyAll(ctx, hostApplier, hostManifests, ownedBy)`. Wire `HostApplier` + `Recorder` in `SetupWithManager`/`cmd/main.go`.

(Reference the exact code blocks in `docs/implementation.md` lines 877–1057 — reproduce them, substituting `cr.Spec.RemoteAccess`/`RetentionPolicy`/`ApplyOrder` field names from Task 1.)

- [ ] **Step 4: Run test to verify it passes**

Run: `make test`
Expected: PASS — status/conditions populated after reconcile.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/ cmd/main.go
git commit -m "feat(controller): two-render reconcile with applyOrder-gated delivery"
```

- [x] Task 8 complete

---

## Task 9: reconciler — prune orphans (status-diff, owned-by-guarded)

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller.go`
- Test: `internal/controller/dualdeploymentoperator_controller_test.go`

Add `r.prune`: diff previous `status.*Resources` against the new render by version-independent key `group/kind/namespace/name`; delete orphans in reverse intra-render order and reverse `applyOrder` across renders; skip CRDs under `retentionPolicy.crds=Retain`; verify the owned-by label before deleting (skip on mismatch).

- [ ] **Step 1: Write failing test — orphan pruned, CRD retained**

Add an envtest spec: reconcile a CR that first renders {ConfigMap A, CRD C}, then re-render dropping A; assert A is deleted and C is retained (default `Retain`); assert a status resource whose owned-by label mismatches is NOT deleted.

- [ ] **Step 2: Run test to verify it fails**

Run: `make test`
Expected: FAIL — no prune yet; orphan A persists.

- [ ] **Step 3: Implement prune**

Add `identityKey(rs) string` (drops APIVersion group-version, keeps group+kind+ns+name), `func (r *...) prune(ctx, applier, prev []ResourceStatus, current []manifest.Manifest, cr)` that builds a current-key set, iterates `SortStatusForDelete(prev)`, skips keys still present, skips CRDs under Retain, GETs each candidate and skips if its owned-by label ≠ this CR, else `applier.Delete(rs.AsManifest())`. Add `deliver.SortStatusForDelete([]ResourceStatus)` and `manifest`-level `ResourceStatus.AsManifest()` (reconstruct a minimal Unstructured with GVK + ns/name). Wire the two `r.prune` calls into the reconcile loop per `applyOrder` reversal (per `docs/implementation.md` §step 8).

- [ ] **Step 4: Run test to verify it passes**

Run: `make test`
Expected: PASS — orphan deleted, CRD retained, mismatched-label object untouched.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/ internal/deliver/
git commit -m "feat(controller): status-diff prune with retentionPolicy + owned-by guard"
```

- [x] Task 9 complete

---

## Task 10: reconciler — finalizer-driven deletion with ShootUnreachable safety

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller.go`
- Test: `internal/controller/dualdeploymentoperator_controller_test.go`

Add finalizer on first reconcile; `reconcileDelete` tears down remote resources in reverse `applyOrder` (host via ownerRef GC), respecting `retentionPolicy.crds`; remove finalizer only when remote cleanup is confirmed; on unreachable shoot, keep finalizer + emit `ShootUnreachable` condition/Event + requeue (never assume gone).

- [ ] **Step 1: Write failing test — finalizer added, remote deleted, CRD retained**

Add an envtest spec: create CR → assert finalizer present; delete CR → assert non-CRD remote resources deleted, CRD retained, finalizer removed after success.

- [ ] **Step 2: Run test to verify it fails**

Run: `make test`
Expected: FAIL — no finalizer/deletion handling yet.

- [ ] **Step 3: Implement deletion**

Add `FinalizerName = "dual-deployment-operator.cc.sap/finalizer"`; the deletion branch in `Reconcile` (`!cr.DeletionTimestamp.IsZero()` → `reconcileDelete`); `reconcileDelete` per `docs/implementation.md` §Deletion — build shoot applier (unreachable → keep finalizer + `ShootUnreachable` Event/condition + 30s requeue), else `SortStatusForDelete(cr.Status.RemoteResources)`, skip CRDs under Retain, `Delete` each, aggregate errors (keep finalizer + requeue on failure), remove finalizer only on full success.

- [ ] **Step 4: Run test to verify it passes**

Run: `make test`
Expected: PASS — finalizer lifecycle + retention honored.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/
git commit -m "feat(controller): finalizer deletion with ShootUnreachable orphan-safety"
```

- [x] Task 10 complete

---

## Task 11: full verification — lint, test, manifests, RBAC markers

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller.go` (RBAC markers)
- Regenerate: `config/rbac/role.yaml` (via `make manifests`)

Add the RBAC markers the reconciler needs (secrets get/list/watch, events create/patch, and the broad host-render apply verbs), regenerate, and run the full gate.

- [ ] **Step 1: Add/confirm RBAC markers**

Ensure the controller carries `+kubebuilder:rbac` markers for: the CR + status + finalizers (already present); `secrets` get/list/watch; `events` create/patch; leases for leader election; and the host-render apply verbs (broad create/update/delete/get/list/watch on the host kinds). Per the cluster-clients spec, the host grant is provisioned by the operator's own chart (Phase 8), but the generated `role.yaml` markers must cover what the manager binds.

- [ ] **Step 2: Regenerate manifests**

Run: `make manifests generate`
Expected: `config/rbac/role.yaml` updated with the new rules.

- [ ] **Step 3: Run lint**

Run: `make lint-fix`
Expected: exit 0, no remaining lint errors.

- [ ] **Step 4: Run the full test suite**

Run: `make test`
Expected: PASS — all unit + envtest specs green.

- [ ] **Step 5: Confirm build**

Run: `go build ./... && go vet ./...`
Expected: exit 0.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/ config/rbac/
git commit -m "chore: RBAC markers + regenerated role for delivery reconciler"
```

- [x] Task 11 complete
