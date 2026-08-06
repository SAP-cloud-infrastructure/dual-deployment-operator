// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package deliver

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
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

func rs(apiVersion, kind, ns, name string) ddov1alpha1.ResourceStatus {
	return ddov1alpha1.ResourceStatus{APIVersion: apiVersion, Kind: kind, Namespace: ns, Name: name}
}

func statusKinds(ss []ddov1alpha1.ResourceStatus) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Kind
	}
	return out
}

func TestSortStatusForDeleteIsReverseOfApplyPriority(t *testing.T) {
	// Given: statuses in random apply order
	in := []ddov1alpha1.ResourceStatus{
		rs("v1", "Namespace", "", "ns1"),
		rs("apiextensions.k8s.io/v1", "CustomResourceDefinition", "", "crd1"),
		rs("apps/v1", "Deployment", "default", "dep1"),
	}
	got := statusKinds(SortStatusForDelete(in))
	// Expect reverse of apply priority: Deployment, CRD, Namespace
	want := []string{"Deployment", "CustomResourceDefinition", "Namespace"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SortStatusForDelete = %v, want %v", got, want)
		}
	}
}

func TestManifestFromStatusReconstructsGVKAndMeta(t *testing.T) {
	// Given: a ResourceStatus with group/version in APIVersion
	st := rs("apps/v1", "Deployment", "default", "my-dep")
	m := ManifestFromStatus(st)

	u := m.Unstructured
	if u.GetKind() != "Deployment" {
		t.Errorf("Kind = %q, want Deployment", u.GetKind())
	}
	if u.GetAPIVersion() != "apps/v1" {
		t.Errorf("APIVersion = %q, want apps/v1", u.GetAPIVersion())
	}
	if u.GetNamespace() != "default" {
		t.Errorf("Namespace = %q, want default", u.GetNamespace())
	}
	if u.GetName() != "my-dep" {
		t.Errorf("Name = %q, want my-dep", u.GetName())
	}
}

func TestIdentityKeyIgnoresNamespaceForClusterScoped(t *testing.T) {
	// A cluster-scoped VWC persisted in status with a bogus namespace (from a chart
	// that stamped .Release.Namespace) must key identically to the same VWC rendered
	// with an empty namespace — otherwise prune sees a phantom orphan and deletes it.
	st := rs("admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration", "kube-system", "vwc")
	m := manifest.Manifest{Unstructured: &unstructured.Unstructured{}}
	m.Unstructured.SetGroupVersionKind(schema.GroupVersionKind{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingWebhookConfiguration"})
	m.Unstructured.SetName("vwc")

	if StatusIdentityKey(st) != ManifestIdentityKey(m) {
		t.Errorf("keys differ: status=%q manifest=%q (must match for cluster-scoped regardless of namespace)",
			StatusIdentityKey(st), ManifestIdentityKey(m))
	}
}

func TestManifestFromStatusStripsNamespaceForClusterScoped(t *testing.T) {
	st := rs("admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration", "kube-system", "vwc")
	if got := ManifestFromStatus(st).Unstructured.GetNamespace(); got != "" {
		t.Errorf("Namespace = %q, want empty (cluster-scoped)", got)
	}
}

func TestManifestFromStatusCoreGroupAPIVersion(t *testing.T) {
	// Given: a core-group ResourceStatus (APIVersion = "v1", group = "")
	st := rs("v1", "ConfigMap", "kube-system", "my-cm")
	m := ManifestFromStatus(st)

	u := m.Unstructured
	if u.GetKind() != "ConfigMap" {
		t.Errorf("Kind = %q, want ConfigMap", u.GetKind())
	}
	if u.GetAPIVersion() != "v1" {
		t.Errorf("APIVersion = %q, want v1", u.GetAPIVersion())
	}
	if u.GetName() != "my-cm" {
		t.Errorf("Name = %q, want my-cm", u.GetName())
	}
}
