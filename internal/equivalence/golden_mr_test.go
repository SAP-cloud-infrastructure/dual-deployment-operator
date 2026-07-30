// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"encoding/base64"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestUnwrapManagedResourcesEmitsBareObjects(t *testing.T) {
	crdYAML := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: endpoints.metal\n"
	sec := obj("v1", "Secret", "mr-crd-endpoints")
	err := unstructured.SetNestedField(sec.Object, base64.StdEncoding.EncodeToString([]byte(crdYAML)), "data", "objects.yaml")
	if err != nil {
		t.Fatal(err)
	}
	mr := obj("resources.gardener.cloud/v1alpha1", "ManagedResource", "mr-crd-endpoints")
	err = unstructured.SetNestedSlice(mr.Object, []any{map[string]any{"name": "mr-crd-endpoints"}}, "spec", "secretRefs")
	if err != nil {
		t.Fatal(err)
	}

	fromMR, passthrough, err := UnwrapManagedResources([]*unstructured.Unstructured{mr, sec})
	if err != nil {
		t.Fatalf("UnwrapManagedResources: %v", err)
	}

	var foundCRD bool
	for _, d := range fromMR {
		if d.GetKind() == "CustomResourceDefinition" && d.GetName() == "endpoints.metal" {
			foundCRD = true
		}
	}
	if !foundCRD {
		t.Error("bare CRD must be emitted from the MR's paired Secret into fromMR")
	}
	for _, d := range append(fromMR, passthrough...) {
		if d.GetKind() == "ManagedResource" || (d.GetKind() == "Secret" && d.GetName() == "mr-crd-endpoints") {
			t.Errorf("wrapper %s/%s must be discarded", d.GetKind(), d.GetName())
		}
	}
}

func TestUnwrapManagedResourcesPassesThroughUnrelated(t *testing.T) {
	// A plain object with no MR present must pass through untouched (identity-gated no-op).
	dep := obj("apps/v1", "Deployment", "controller-manager")
	fromMR, passthrough, err := UnwrapManagedResources([]*unstructured.Unstructured{dep})
	if err != nil {
		t.Fatalf("UnwrapManagedResources: %v", err)
	}
	if len(fromMR) != 0 {
		t.Errorf("no MR present, so fromMR must be empty; got %d", len(fromMR))
	}
	if len(passthrough) != 1 || passthrough[0].GetName() != "controller-manager" {
		t.Errorf("unrelated doc must pass through unchanged; got %d docs", len(passthrough))
	}
}

func TestUnwrapManagedResourcesFailsOnMissingSecret(t *testing.T) {
	mr := obj("resources.gardener.cloud/v1alpha1", "ManagedResource", "mr-orphan")
	if err := unstructured.SetNestedSlice(mr.Object, []any{map[string]any{"name": "absent-secret"}}, "spec", "secretRefs"); err != nil {
		t.Fatal(err)
	}
	_, _, err := UnwrapManagedResources([]*unstructured.Unstructured{mr})
	if err == nil {
		t.Fatal("MR referencing an absent Secret must fail closed (else golden shoot set silently incomplete)")
	}
}

func TestUnwrapManagedResourcesFailsOnEmptyObjectsYAML(t *testing.T) {
	sec := obj("v1", "Secret", "mr-empty")
	if err := unstructured.SetNestedField(sec.Object, "", "data", "objects.yaml"); err != nil {
		t.Fatal(err)
	}
	mr := obj("resources.gardener.cloud/v1alpha1", "ManagedResource", "mr-empty")
	if err := unstructured.SetNestedSlice(mr.Object, []any{map[string]any{"name": "mr-empty"}}, "spec", "secretRefs"); err != nil {
		t.Fatal(err)
	}
	_, _, err := UnwrapManagedResources([]*unstructured.Unstructured{mr, sec})
	if err == nil {
		t.Fatal("MR secret with empty data[objects.yaml] must fail closed")
	}
}
