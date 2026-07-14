// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func nsManifest(kind, name, namespace string) Manifest {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("v1")
	u.SetKind(kind)
	u.SetName(name)
	if namespace != "" {
		u.SetNamespace(namespace)
	}
	return Manifest{Unstructured: u, Origin: OriginUpstream}
}

func TestApplyNamespaceStampsEmptyNamespaced(t *testing.T) {
	ms := []Manifest{nsManifest("ConfigMap", "cm", "")}
	ApplyNamespace(ms, "target-ns")
	if got := ms[0].Unstructured.GetNamespace(); got != "target-ns" {
		t.Errorf("namespace = %q, want target-ns", got)
	}
}

func TestApplyNamespacePreservesExplicit(t *testing.T) {
	ms := []Manifest{nsManifest("ConfigMap", "cm", "explicit-ns")}
	ApplyNamespace(ms, "target-ns")
	if got := ms[0].Unstructured.GetNamespace(); got != "explicit-ns" {
		t.Errorf("namespace = %q, want explicit-ns (unchanged)", got)
	}
}

func TestApplyNamespaceSkipsClusterScoped(t *testing.T) {
	for _, kind := range []string{"CustomResourceDefinition", "ClusterRole", "ClusterRoleBinding", "Namespace"} {
		ms := []Manifest{nsManifest(kind, "obj", "")}
		ApplyNamespace(ms, "target-ns")
		if got := ms[0].Unstructured.GetNamespace(); got != "" {
			t.Errorf("%s: namespace = %q, want empty (cluster-scoped)", kind, got)
		}
	}
}

func TestApplyNamespaceEmptyTargetIsNoOp(t *testing.T) {
	ms := []Manifest{nsManifest("ConfigMap", "cm", "")}
	ApplyNamespace(ms, "")
	if got := ms[0].Unstructured.GetNamespace(); got != "" {
		t.Errorf("namespace = %q, want empty (no-op)", got)
	}
}

func TestIsClusterScoped(t *testing.T) {
	if !IsClusterScoped("CustomResourceDefinition") {
		t.Error("CustomResourceDefinition should be cluster-scoped")
	}
	if IsClusterScoped("ConfigMap") {
		t.Error("ConfigMap should not be cluster-scoped")
	}
}
