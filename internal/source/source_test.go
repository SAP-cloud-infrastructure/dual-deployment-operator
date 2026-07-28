// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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

func TestFromWiresAuthSecretRefCredentials(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "ns"},
		Data:       map[string][]byte{"token": []byte("tok")},
	}).Build()

	deps := Deps{
		ChartLoader:        NewHelmLoader(),
		RootResolver:       NewGitResolver(),
		CredentialResolver: &CredentialResolver{Client: cl, Namespace: "ns"},
	}
	spec := v1alpha1.Source{Helm: &v1alpha1.HelmSource{
		Repo: "oci://r", Name: "n", Version: "1",
		AuthSecretRef: &v1alpha1.SecretReference{Name: "creds"},
	}}
	if _, err := From(spec, deps); err != nil {
		t.Fatalf("From with authSecretRef: %v", err)
	}
}

func TestNewRenderCache_Public(t *testing.T) {
	// The exported constructor MUST return a usable cache under the default cap.
	// This is the wiring seam used by cmd/main.go.
	if c := NewRenderCache(); c == nil {
		t.Fatal("NewRenderCache() returned nil; the operator opts in via cmd/main.go and must always get a cache")
	}
}
