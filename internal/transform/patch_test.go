// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"strings"
	"testing"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func TestPatch(t *testing.T) {
	dep := func() manifest.Manifest {
		return mustManifest(t, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller-manager
spec:
  replicas: 1
`, manifest.OriginUpstream)
	}
	vwc := func() manifest.Manifest {
		return mustManifest(t, `
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: v
`, manifest.OriginUpstream)
	}

	tests := []struct {
		name    string
		spec    *v1alpha1.PatchSpec
		input   []manifest.Manifest
		check   func(t *testing.T, out []manifest.Manifest)
		wantErr string
	}{
		{
			name: "strategicMerge sets replicas",
			spec: &v1alpha1.PatchSpec{
				Target:         v1alpha1.Selector{Kind: "Deployment"},
				StrategicMerge: mustJSON(`{"spec":{"replicas":3}}`),
			},
			input: []manifest.Manifest{dep()},
			check: func(t *testing.T, out []manifest.Manifest) {
				r, _, _ := nestedInt64(out[0], "spec", "replicas")
				if r != 3 {
					t.Fatalf("replicas = %d, want 3", r)
				}
			},
		},
		{
			name: "strategicMerge stamps injector label",
			spec: &v1alpha1.PatchSpec{
				Target:         v1alpha1.Selector{Kind: "ValidatingWebhookConfiguration"},
				StrategicMerge: mustJSON(`{"metadata":{"labels":{"dual-deployment-operator.cc.sap/webhook-injector":"metal-operator"}}}`),
			},
			input: []manifest.Manifest{vwc()},
			check: func(t *testing.T, out []manifest.Manifest) {
				if out[0].Unstructured.GetLabels()["dual-deployment-operator.cc.sap/webhook-injector"] != "metal-operator" {
					t.Fatalf("label not stamped: %v", out[0].Unstructured.GetLabels())
				}
			},
		},
		{
			name: "jsonPatch replaces replicas",
			spec: &v1alpha1.PatchSpec{
				Target:    v1alpha1.Selector{Kind: "Deployment"},
				JSONPatch: []v1alpha1.JSONPatchOp{{Op: "replace", Path: "/spec/replicas", Value: mustJSON(`5`)}},
			},
			input: []manifest.Manifest{dep()},
			check: func(t *testing.T, out []manifest.Manifest) {
				r, _, _ := nestedInt64(out[0], "spec", "replicas")
				if r != 5 {
					t.Fatalf("replicas = %d, want 5", r)
				}
			},
		},
		{
			name:    "zero matches fails loud",
			spec:    &v1alpha1.PatchSpec{Target: v1alpha1.Selector{Kind: "Deployment", Name: "nope"}, StrategicMerge: mustJSON(`{}`)},
			input:   []manifest.Manifest{dep()},
			wantErr: "no matching resource",
		},
		{
			name:    "both variants set is rejected",
			spec:    &v1alpha1.PatchSpec{Target: v1alpha1.Selector{Kind: "Deployment"}, StrategicMerge: mustJSON(`{}`), JSONPatch: []v1alpha1.JSONPatchOp{{Op: "test", Path: "/x"}}},
			input:   []manifest.Manifest{dep()},
			wantErr: "exactly one of strategicMerge or jsonPatch",
		},
		{
			name:    "neither variant set is rejected",
			spec:    &v1alpha1.PatchSpec{Target: v1alpha1.Selector{Kind: "Deployment"}},
			input:   []manifest.Manifest{dep()},
			wantErr: "exactly one of strategicMerge or jsonPatch",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &patch{spec: tc.spec}
			out, err := p.Apply(tc.input)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want contains %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			tc.check(t, out)
		})
	}
}
