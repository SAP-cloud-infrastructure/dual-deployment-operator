// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestGoldenRenderMetalOperatorFromGit(t *testing.T) {
	requireEquivalenceEnabled(t)
	docs, err := RenderGolden(context.Background(), GoldenRenderReq{
		RepoURL:   "https://github.com/sapcc/helm-charts.git",
		SHA:       "33e68278ace26fe23e0c2b36a9266a58c9b47707",
		ChartPath: "system/metal-operator-remote",
		Namespace: "shoot--cp--m-qa-de-1",
		ValuesYAML: [][]byte{
			[]byte("global:\n  region: qa-de-1\n"),
		},
	})
	if err != nil {
		t.Fatalf("golden render failed: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("expected rendered documents from metal-operator-remote")
	}
}

// TestRenderGoldenKustomizeNamespaceHandling verifies that RenderGoldenKustomize
// mirrors manifest.ApplyNamespace exactly:
//   - cluster-scoped kinds (CRD, ClusterRole, …) always get an empty namespace,
//     even if krusty stamped one onto them
//   - namespaced resources without a namespace are stamped with req.Namespace
//   - namespaced resources that already have a namespace are left unchanged
//
// This is a pure offline test exercising applyGoldenKustomizeNamespace, the
// shared helper called by RenderGoldenKustomize, without a git clone.
func TestRenderGoldenKustomizeNamespaceHandling(t *testing.T) {
	docs := []*unstructured.Unstructured{
		makeTestObj("apiextensions.k8s.io/v1", "CustomResourceDefinition", "spurious-ns", "foos.example.com"),
		makeTestObj("rbac.authorization.k8s.io/v1", "ClusterRole", "", "foo-role"),
		makeTestObj("v1", "ServiceAccount", "", "foo-sa"),
		makeTestObj("v1", "ServiceAccount", "already-set", "bar-sa"),
	}
	// Call the production helper directly — no duplication.
	applyGoldenKustomizeNamespace(docs, "target-ns")

	cases := []struct {
		name   string
		wantNS string
	}{
		{"foos.example.com", ""},   // CRD: spurious namespace cleared
		{"foo-role", ""},           // ClusterRole: stays empty
		{"foo-sa", "target-ns"},    // SA without namespace: stamped
		{"bar-sa", "already-set"},  // SA with existing namespace: unchanged
	}
	byName := map[string]*unstructured.Unstructured{}
	for _, d := range docs {
		byName[d.GetName()] = d
	}
	for _, tc := range cases {
		d := byName[tc.name]
		if d == nil {
			t.Errorf("object %q not found", tc.name)
			continue
		}
		if got := d.GetNamespace(); got != tc.wantNS {
			t.Errorf("object %q: namespace = %q, want %q", tc.name, got, tc.wantNS)
		}
	}
}

func makeTestObj(apiVersion, kind, ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(apiVersion)
	u.SetKind(kind)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}
