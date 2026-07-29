// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

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