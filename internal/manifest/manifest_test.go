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

func TestStripInternalAnnotationsRemovesOriginAndEmptyMap(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "c", "annotations": map[string]any{OriginAnnotation: "additions"}},
	}}
	m := Manifest{Unstructured: u, Origin: OriginAdditions}
	m.StripInternalAnnotations()
	if _, found, err := unstructured.NestedMap(u.Object, "metadata", "annotations"); err != nil || found {
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
	// Stable: recomputing for the same identity yields the same value.
	first := OwnedByValue("ns", "name")
	second := OwnedByValue("ns", "name")
	if first != second {
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
