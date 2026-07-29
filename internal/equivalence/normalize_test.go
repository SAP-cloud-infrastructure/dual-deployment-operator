// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestNormalizeDropsEmptyAnnotationsMap(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":        "x",
			"annotations": map[string]interface{}{},
		},
	}}
	Normalize(u)
	md, _, _ := unstructured.NestedMap(u.Object, "metadata")
	if _, ok := md["annotations"]; ok {
		t.Fatal("empty annotations map should be dropped by Normalize")
	}
}