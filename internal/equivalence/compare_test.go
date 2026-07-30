// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func cm(name, val string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": name, "namespace": "ns"},
		"data":     map[string]any{"k": val},
	}}
}

func TestCompareReportsFieldDiffAndMissing(t *testing.T) {
	golden := ObjectSet{}
	op := ObjectSet{}
	g1 := cm("a", "v1")
	o1 := cm("a", "v2") // field diff
	g2 := cm("b", "v")  // only in golden -> missing on operator side
	golden[KeyOf(g1)] = g1
	golden[KeyOf(g2)] = g2
	op[KeyOf(o1)] = o1

	report := Compare(golden, op)
	if report.Equal() {
		t.Fatal("expected mismatches")
	}
	s := report.String()
	if !strings.Contains(s, "v1/ConfigMap/ns/a") {
		t.Errorf("expected field-diff for a; got:\n%s", s)
	}
	if !strings.Contains(s, "v1/ConfigMap/ns/b") || !strings.Contains(strings.ToLower(s), "missing") {
		t.Errorf("expected missing-on-operator for b; got:\n%s", s)
	}
}

func vwc(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admissionregistration.k8s.io/v1",
		"kind":       "ValidatingWebhookConfiguration",
		"metadata":   map[string]any{"name": name},
		"webhooks": []any{map[string]any{
			"name":         "w",
			"clientConfig": map[string]any{"url": "https://x/y"}, // no caBundle on either side
		}},
	}}
}

func TestCompareCABundleAbsentBothSidesEqual(t *testing.T) {
	golden := ObjectSet{}
	op := ObjectSet{}
	g := vwc("vwc")
	o := vwc("vwc")
	golden[KeyOf(g)] = g
	op[KeyOf(o)] = o
	if r := Compare(golden, op); !r.Equal() {
		t.Fatalf("caBundle-absent VWCs must compare equal; got:\n%s", r.String())
	}
}

func TestCompareReportsDifferingFieldPath(t *testing.T) {
	golden := ObjectSet{}
	op := ObjectSet{}
	g := cm("a", "v1")
	o := cm("a", "v2")
	golden[KeyOf(g)] = g
	op[KeyOf(o)] = o

	r := Compare(golden, op)
	s := r.String()
	if !strings.Contains(s, "data.k") {
		t.Errorf("expected differing field path 'data.k' in report; got:\n%s", s)
	}
}
