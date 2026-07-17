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

func TestMatch(t *testing.T) {
	dep := mustManifest(t, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: metal-operator-controller-manager
`, manifest.OriginUpstream)
	svc := mustManifest(t, `
apiVersion: v1
kind: Service
metadata:
  name: other-controller
`, manifest.OriginAdditions)

	tests := []struct {
		name string
		m    manifest.Manifest
		sel  v1alpha1.Selector
		want bool
	}{
		{"kind exact match", dep, v1alpha1.Selector{Kind: "Deployment"}, true},
		{"kind mismatch", svc, v1alpha1.Selector{Kind: "Deployment"}, false},
		{"name glob match", dep, v1alpha1.Selector{Name: "metal-*"}, true},
		{"name glob mismatch", dep, v1alpha1.Selector{Name: "other-*"}, false},
		{"origin match", dep, v1alpha1.Selector{Origin: "upstream"}, true},
		{"origin mismatch", dep, v1alpha1.Selector{Origin: "additions"}, false},
		{"empty selector matches all", svc, v1alpha1.Selector{}, true},
		{"all fields match (AND) true", dep, v1alpha1.Selector{Kind: "Deployment", Name: "metal-*", Origin: "upstream"}, true},
		{"all fields match (AND) one false", dep, v1alpha1.Selector{Kind: "Deployment", Name: "metal-*", Origin: "additions"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.m, tc.sel); got != tc.want {
				t.Fatalf("Match() = %v, want %v", got, tc.want)
			}
		})
	}
}
