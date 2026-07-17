// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"testing"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func TestBuildPreservesOrder(t *testing.T) {
	specs := []v1alpha1.Transformation{
		{Patch: &v1alpha1.PatchSpec{Target: v1alpha1.Selector{Kind: "Deployment"}, StrategicMerge: mustJSON(`{}`)}},
		{RewriteWebhookURL: &v1alpha1.RewriteWebhookURLSpec{URLPrefix: "https://x:443"}},
		{FilterKinds: &v1alpha1.FilterKindsSpec{Kinds: []string{"Service"}}},
	}
	got, err := Build(specs)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	want := []string{"patch", "rewriteWebhookURL", "filterKinds"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, ty := range want {
		if got[i].Type() != ty {
			t.Fatalf("got[%d].Type() = %q, want %q", i, got[i].Type(), ty)
		}
	}
}

func TestBuildEmptyEntryErrors(t *testing.T) {
	_, err := Build([]v1alpha1.Transformation{{}})
	if err == nil {
		t.Fatalf("expected error for empty entry")
	}
}

func TestApplyDoesNotMutateInput(t *testing.T) {
	in := []manifest.Manifest{
		mustManifest(t, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d\nspec:\n  replicas: 1", manifest.OriginUpstream),
	}
	before := cloneForAssert(in)

	p := &patch{spec: &v1alpha1.PatchSpec{Target: v1alpha1.Selector{Kind: "Deployment"}, StrategicMerge: mustJSON(`{"spec":{"replicas":9}}`)}}
	if _, err := p.Apply(in); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	r, _, _ := nestedInt64(in[0], "spec", "replicas")
	rBefore, _, _ := nestedInt64(before[0], "spec", "replicas")
	if r != rBefore {
		t.Fatalf("input Unstructured mutated in place: replicas %d != %d", r, rBefore)
	}
}
