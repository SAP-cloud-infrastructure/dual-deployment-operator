// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import "testing"

func TestLoadFixtureMetalOperator(t *testing.T) {
	f, err := LoadFixture("../../testdata/fixtures/metal-operator")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if f.RepoURL == "" || f.SHA == "" {
		t.Error("fixture must pin a git repo URL + SHA")
	}
	if f.ChartPath != "system/metal-operator-remote" {
		t.Errorf("chartPath = %q, want system/metal-operator-remote", f.ChartPath)
	}
	if f.ChartFullname != "metal-operator-remote" {
		t.Errorf("fullname = %q", f.ChartFullname)
	}
	if f.CR == nil || f.CR.Spec.Source.Helm == nil {
		t.Error("fixture CR must have a helm source")
	}
	if len(f.OverlayRaw) == 0 {
		t.Error("fixture must load overlay values bytes")
	}
	if len(f.Exclusions) != 1 || f.Exclusions[0].Kind != "ConfigMap" || f.Exclusions[0].Name != "owner-info" {
		t.Errorf("exclusions not parsed: %+v", f.Exclusions)
	}
}

func TestLoadFixtureIPAMCAPI(t *testing.T) {
	f, err := LoadFixture("../../testdata/fixtures/ipam-capi")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if f.RepoURL == "" || f.SHA == "" {
		t.Error("fixture must pin a git repo URL + SHA")
	}
	// ipam-capi uses kustomize golden render, not Helm; chartPath/chartFullname are empty.
	if f.KustomizePath != "system/kustomize/ipam-capi-remote" {
		t.Errorf("kustomizePath = %q, want system/kustomize/ipam-capi-remote", f.KustomizePath)
	}
	if f.CR == nil || f.CR.Spec.Source.Kustomize == nil {
		t.Error("ipam-capi fixture CR must have a kustomize source")
	}
	// OverlayRaw is nil: this fixture uses RenderGoldenKustomize, not RenderGolden (Helm).
	if len(f.OverlayRaw) != 0 {
		t.Error("ipam-capi fixture must not load overlay values bytes (no Helm golden)")
	}
}
