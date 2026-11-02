// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"math"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestNormalizeDropsEmptyAnnotationsMap(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":        "x",
			"annotations": map[string]any{},
		},
	}}
	Normalize(u)
	md, _, err := unstructured.NestedMap(u.Object, "metadata")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := md["annotations"]; ok {
		t.Fatal("empty annotations map should be dropped by Normalize")
	}
}

func TestNormalizeJSONNumbers(t *testing.T) {
	// Strategic-merge JSON decoding uses float64; YAML decoding retains int64.
	// This difference is representation, not Kubernetes manifest drift.
	for _, tc := range []struct {
		name        string
		input, want any
	}{
		{"integer float", float64(8081), int64(8081)},
		{"integer", int64(8081), int64(8081)},
		{"fraction", 1.5, 1.5},
		{"upper boundary", math.Exp2(63), math.Exp2(63)},
		{"lower boundary", -math.Exp2(63), int64(math.MinInt64)},
		{"large integer", int64(9007199254740993), int64(9007199254740993)},
		{"numeric string", "8081", "8081"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{"values": []any{map[string]any{"number": tc.input}}}}
			Normalize(u)
			got := u.Object["values"].([]any)[0].(map[string]any)["number"]
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v (%T), want %v (%T)", got, got, tc.want, tc.want)
			}
		})
	}
}
