// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func jsonVal(t *testing.T, s string) *apiextensionsv1.JSON {
	t.Helper()
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}

func newHelm(t *testing.T, spec *v1alpha1.HelmSource) Source {
	t.Helper()
	s, err := From(v1alpha1.Source{Helm: spec}, Deps{
		ChartLoader: fakeChartLoader{dir: "testdata/charts/demo"},
	})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	return s
}

func kinds(ms []manifest.Manifest) map[string]manifest.Origin {
	out := map[string]manifest.Origin{}
	for _, m := range ms {
		out[m.Unstructured.GetKind()+"/"+m.Unstructured.GetName()] = m.Origin
	}
	return out
}

func TestHelmSeedRenderEnablesControllerAndTagsOrigins(t *testing.T) {
	spec := &v1alpha1.HelmSource{
		Repo: "r", Name: "demo", Version: "0.1.0",
		SeedValues: jsonVal(t, `{"controllerManager":{"enable":true}}`),
	}
	ms, err := newHelm(t, spec).Render(context.Background(), ModeSeed, "seed-ns")
	if err != nil {
		t.Fatalf("render seed: %v", err)
	}
	k := kinds(ms)
	if k["Deployment/demo-controller-manager"] != manifest.OriginUpstream {
		t.Errorf("deployment origin = %q, want upstream (present=%v)", k["Deployment/demo-controller-manager"], hasKey(k, "Deployment/demo-controller-manager"))
	}
	if k["ConfigMap/demo-addition"] != manifest.OriginAdditions {
		t.Errorf("addition origin = %q, want additions", k["ConfigMap/demo-addition"])
	}
}

func TestHelmRenderAppliesTargetNamespace(t *testing.T) {
	spec := &v1alpha1.HelmSource{
		Repo: "r", Name: "demo", Version: "0.1.0",
		SeedValues: jsonVal(t, `{"controllerManager":{"enable":true}}`),
	}
	ms, err := newHelm(t, spec).Render(context.Background(), ModeSeed, "seed-ns")
	if err != nil {
		t.Fatalf("render seed: %v", err)
	}
	byName := map[string]manifest.Manifest{}
	for _, m := range ms {
		byName[m.Unstructured.GetKind()+"/"+m.Unstructured.GetName()] = m
	}
	if got := byName["ConfigMap/demo-addition"].Unstructured.GetNamespace(); got != "seed-ns" {
		t.Errorf("addition namespace = %q, want seed-ns", got)
	}
	if got := byName["Deployment/demo-controller-manager"].Unstructured.GetNamespace(); got != "seed-ns" {
		t.Errorf("deployment namespace = %q, want seed-ns", got)
	}
}

func TestHelmRenderShootNamespaceAndClusterScoped(t *testing.T) {
	spec := &v1alpha1.HelmSource{Repo: "r", Name: "demo", Version: "0.1.0"}
	ms, err := newHelm(t, spec).Render(context.Background(), ModeShoot, "shoot-ns")
	if err != nil {
		t.Fatalf("render shoot: %v", err)
	}
	for _, m := range ms {
		if m.Unstructured.GetKind() == "CustomResourceDefinition" {
			if got := m.Unstructured.GetNamespace(); got != "" {
				t.Errorf("CRD namespace = %q, want empty (cluster-scoped)", got)
			}
		}
	}
}

func TestHelmShootRenderIncludesCRDs(t *testing.T) {
	spec := &v1alpha1.HelmSource{Repo: "r", Name: "demo", Version: "0.1.0"}
	ms, err := newHelm(t, spec).Render(context.Background(), ModeShoot, "shoot-ns")
	if err != nil {
		t.Fatalf("render shoot: %v", err)
	}
	if _, ok := kinds(ms)["CustomResourceDefinition/demos.demo.cc.sap"]; !ok {
		t.Error("expected CRD in shoot render (IncludeCRDs)")
	}
	// seed-only Deployment must NOT appear in shoot render
	if _, ok := kinds(ms)["Deployment/demo-controller-manager"]; ok {
		t.Error("seed-only Deployment leaked into shoot render")
	}
}

func TestHelmRejectsUserSuppliedMode(t *testing.T) {
	spec := &v1alpha1.HelmSource{
		Repo: "r", Name: "demo", Version: "0.1.0",
		Values: jsonVal(t, `{"mode":"seed"}`),
	}
	_, err := newHelm(t, spec).Render(context.Background(), ModeSeed, "seed-ns")
	if err == nil {
		t.Fatal("expected error when user values set mode")
	}
}

func hasKey(m map[string]manifest.Origin, k string) bool { _, ok := m[k]; return ok }
