// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

// TestGitResolverOnlineKustomizeRender exercises the production kustomize path
// end-to-end against a real public git repo: gitResolver fetches a pinned ?ref=
// root, krusty builds the overlay, and Render returns parsed manifests. Makes a
// real network call on every run.

import (
	"context"
	"slices"
	"testing"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

func TestGitResolverOnlineKustomizeRender(t *testing.T) {
	spec := v1alpha1.Source{Kustomize: &v1alpha1.KustomizeSource{
		URL:       "https://github.com/kubernetes-sigs/kustomize?ref=kustomize/v5.4.3",
		SeedPath:  "examples/helloWorld",
		ShootPath: "examples/helloWorld",
	}}
	src, err := From(spec, Deps{RootResolver: NewGitResolver()})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	manifests, err := src.Render(context.Background(), ModeSeed, "")
	if err != nil {
		t.Fatalf("render kustomize overlay from public repo: %v", err)
	}
	if len(manifests) == 0 {
		t.Fatal("expected rendered manifests from helloWorld overlay, got none")
	}
	// helloWorld renders a Deployment, Service, and ConfigMap — assert at least
	// one known kind is present to prove a real build, not an empty result.
	var kinds []string
	for _, m := range manifests {
		kinds = append(kinds, m.Unstructured.GetKind())
	}
	if !slices.Contains(kinds, "Deployment") {
		t.Fatalf("expected a Deployment in the helloWorld render, got kinds: %v", kinds)
	}
}
