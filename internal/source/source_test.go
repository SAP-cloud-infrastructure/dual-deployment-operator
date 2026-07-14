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
	if ModeHost != "host" || ModeRemote != "remote" {
		t.Fatalf("modes = %q/%q, want host/remote", ModeHost, ModeRemote)
	}
}

func TestFromSelectsHelm(t *testing.T) {
	spec := v1alpha1.Source{Helm: &v1alpha1.HelmSource{Repo: "r", Name: "n", Version: "1.0.0"}}
	s, err := From(spec, Deps{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := s.(*helmSource); !ok {
		t.Errorf("From returned %T, want *helmSource", s)
	}
}

func TestFromSelectsKustomize(t *testing.T) {
	spec := v1alpha1.Source{Kustomize: &v1alpha1.KustomizeSource{URL: "u?ref=x", HostPath: "host", RemotePath: "remote"}}
	s, err := From(spec, Deps{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := s.(*kustomizeSource); !ok {
		t.Errorf("From returned %T, want *kustomizeSource", s)
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
