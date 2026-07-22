// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"testing"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func newKustomize(t *testing.T) Source {
	t.Helper()
	s, err := From(v1alpha1.Source{Kustomize: &v1alpha1.KustomizeSource{
		URL: "ignored?ref=x", SeedPath: "host", ShootPath: "remote",
	}}, Deps{RootResolver: fakeRootResolver{baseDir: "testdata/kustomize"}})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	return s
}

func TestKustomizeSeedOverlayHasAdditionsAndUpstream(t *testing.T) {
	ms, err := newKustomize(t).Render(context.Background(), ModeSeed, "seed-ns")
	if err != nil {
		t.Fatalf("render seed: %v", err)
	}
	k := kinds(ms)
	if k["ConfigMap/upstream-cm"] != manifest.OriginUpstream {
		t.Errorf("upstream-cm origin = %q, want upstream", k["ConfigMap/upstream-cm"])
	}
	if k["ConfigMap/addition-cm"] != manifest.OriginAdditions {
		t.Errorf("addition-cm origin = %q, want additions", k["ConfigMap/addition-cm"])
	}
}

func TestKustomizeShootOverlayExcludesAdditions(t *testing.T) {
	ms, err := newKustomize(t).Render(context.Background(), ModeShoot, "shoot-ns")
	if err != nil {
		t.Fatalf("render shoot: %v", err)
	}
	k := kinds(ms)
	if _, ok := k["ConfigMap/addition-cm"]; ok {
		t.Error("addition-cm should not appear in shoot overlay")
	}
	if k["ConfigMap/upstream-cm"] != manifest.OriginUpstream {
		t.Errorf("upstream-cm origin = %q, want upstream", k["ConfigMap/upstream-cm"])
	}
}

func TestKustomizeAppliesTargetNamespace(t *testing.T) {
	ms, err := newKustomize(t).Render(context.Background(), ModeSeed, "seed-ns")
	if err != nil {
		t.Fatalf("render seed: %v", err)
	}
	for _, m := range ms {
		if m.Unstructured.GetKind() == "ConfigMap" && m.Unstructured.GetNamespace() != "seed-ns" {
			t.Errorf("%s namespace = %q, want seed-ns", m.Unstructured.GetName(), m.Unstructured.GetNamespace())
		}
	}
}
