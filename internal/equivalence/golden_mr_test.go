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
	_ = unstructured.SetNestedField(sec.Object, base64.StdEncoding.EncodeToString([]byte(crdYAML)), "data", "objects.yaml")
	mr := obj("resources.gardener.cloud/v1alpha1", "ManagedResource", "mr-crd-endpoints")
	_ = unstructured.SetNestedSlice(mr.Object, []interface{}{map[string]interface{}{"name": "mr-crd-endpoints"}}, "spec", "secretRefs")

	docs, err := UnwrapManagedResources([]*unstructured.Unstructured{mr, sec})
	if err != nil {
		t.Fatalf("UnwrapManagedResources: %v", err)
	}

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

func TestUnwrapManagedResourcesPassesThroughUnrelated(t *testing.T) {
	// A plain object with no MR present must pass through untouched (identity-gated no-op).
	dep := obj("apps/v1", "Deployment", "controller-manager")
	docs, err := UnwrapManagedResources([]*unstructured.Unstructured{dep})
	if err != nil {
		t.Fatalf("UnwrapManagedResources: %v", err)
	}
	if len(docs) != 1 || docs[0].GetName() != "controller-manager" {
		t.Errorf("unrelated doc must pass through unchanged; got %d docs", len(docs))
	}
}
