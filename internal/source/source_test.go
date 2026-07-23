// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"testing"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

func TestModeValues(t *testing.T) {
	if ModeSeed != "seed" || ModeShoot != "shoot" {
		t.Fatalf("modes = %q/%q, want seed/shoot", ModeSeed, ModeShoot)
	}
}

func TestFromSelectsHelm(t *testing.T) {
	spec := v1alpha1.Source{Helm: &v1alpha1.HelmSource{Repo: "r", Name: "n", Version: "1.0.0"}}
	s, err := From(spec, Deps{ChartLoader: fakeChartLoader{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := s.(*helmSource); !ok {
		t.Errorf("From returned %T, want *helmSource", s)
	}
}

func TestFromHelmRequiresChartLoader(t *testing.T) {
	spec := v1alpha1.Source{Helm: &v1alpha1.HelmSource{Repo: "r", Name: "n", Version: "1.0.0"}}
	if _, err := From(spec, Deps{}); err == nil {
		t.Error("From should reject a helm source with no ChartLoader configured")
	}
}

func TestFromSelectsKustomize(t *testing.T) {
	spec := v1alpha1.Source{Kustomize: &v1alpha1.KustomizeSource{URL: "u?ref=x", SeedPath: "seed", ShootPath: "shoot"}}
	s, err := From(spec, Deps{RootResolver: fakeRootResolver{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := s.(*kustomizeSource); !ok {
		t.Errorf("From returned %T, want *kustomizeSource", s)
	}
}

func TestFromKustomizeRequiresRootResolver(t *testing.T) {
	spec := v1alpha1.Source{Kustomize: &v1alpha1.KustomizeSource{URL: "u?ref=x", SeedPath: "seed", ShootPath: "shoot"}}
	if _, err := From(spec, Deps{}); err == nil {
		t.Error("From should reject a kustomize source with no RootResolver configured")
	}
}

func TestFromRejectsNeither(t *testing.T) {
	if _, err := From(v1alpha1.Source{}, Deps{}); err == nil {
		t.Error("expected error when neither discriminator set")
	}
}

func TestFromRejectsBoth(t *testing.T) {
	spec := v1alpha1.Source{
		Helm:      &v1alpha1.HelmSource{},
		Kustomize: &v1alpha1.KustomizeSource{},
	}
	if _, err := From(spec, Deps{}); err == nil {
		t.Error("expected error when both discriminators set")
	}
}
