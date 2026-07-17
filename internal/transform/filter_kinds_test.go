// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"reflect"
	"testing"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func kinds(ms []manifest.Manifest) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Unstructured.GetKind()
	}
	return out
}

func TestFilterKinds(t *testing.T) {
	svc := mustManifest(t, "apiVersion: v1\nkind: Service\nmetadata:\n  name: s", manifest.OriginUpstream)
	dep := mustManifest(t, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d", manifest.OriginUpstream)
	cmUp := mustManifest(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: up", manifest.OriginUpstream)
	cmAdd := mustManifest(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: add", manifest.OriginAdditions)

	tests := []struct {
		name  string
		spec  *v1alpha1.FilterKindsSpec
		input []manifest.Manifest
		want  []string // kinds surviving, in order
	}{
		{"drops listed kind", &v1alpha1.FilterKindsSpec{Kinds: []string{"Service"}},
			[]manifest.Manifest{svc, dep}, []string{"Deployment"}},
		{"source restricts by origin", &v1alpha1.FilterKindsSpec{Kinds: []string{"ConfigMap"}, Source: "upstream"},
			[]manifest.Manifest{cmUp, cmAdd}, []string{"ConfigMap"}}, // only the additions CM survives
		{"preserves relative order", &v1alpha1.FilterKindsSpec{Kinds: []string{"Service"}},
			[]manifest.Manifest{dep, svc, cmUp}, []string{"Deployment", "ConfigMap"}},
		{"zero matches no-op", &v1alpha1.FilterKindsSpec{Kinds: []string{"Secret"}},
			[]manifest.Manifest{dep}, []string{"Deployment"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ft := &filterKinds{spec: tc.spec}
			orig := cloneForAssert(tc.input)
			got, err := ft.Apply(tc.input)
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if !reflect.DeepEqual(kinds(got), tc.want) {
				t.Fatalf("survivors = %v, want %v", kinds(got), tc.want)
			}
			if len(tc.input) != len(orig) {
				t.Fatalf("input slice was mutated: len %d != %d", len(tc.input), len(orig))
			}
		})
	}
}

func TestFilterKindsSourceAdditions(t *testing.T) {
	cmUp := mustManifest(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: up", manifest.OriginUpstream)
	cmAdd := mustManifest(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: add", manifest.OriginAdditions)
	ft := &filterKinds{spec: &v1alpha1.FilterKindsSpec{Kinds: []string{"ConfigMap"}, Source: "additions"}}
	got, err := ft.Apply([]manifest.Manifest{cmUp, cmAdd})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(got) != 1 || got[0].Unstructured.GetName() != "up" {
		t.Fatalf("want only upstream CM to survive, got %v", kinds(got))
	}
}
