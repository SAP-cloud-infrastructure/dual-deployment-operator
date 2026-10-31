// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"path/filepath"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Admission can inspect only the CR's root URL, not a fetched Kustomization or
// its transitive bases. Deliberately nonexistent repositories below are accepted
// when the root has a ref; resolving roots and validating bases belongs to render.
func TestKustomizeRootURLAdmission(t *testing.T) {
	useExistingCluster := false
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		UseExistingCluster:    &useExistingCluster,
	}
	config, err := testEnv.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("register API: %v", err)
	}
	k8sClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	for _, tc := range []struct {
		name     string
		query    string
		accepted bool
	}{
		{name: "missing-ref"},
		{name: "empty-ref", query: "?ref="},
		{name: "tag-ref", query: "?ref=v1.2.31", accepted: true},
		{name: "branch-ref", query: "?ref=main", accepted: true},
		{name: "sha-ref", query: "?ref=0123456789abcdef0123456789abcdef01234567", accepted: true},
		{name: "ref-after-query", query: "?timeout=30s&ref=v1", accepted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := &DualDeploymentOperator{
				ObjectMeta: metav1.ObjectMeta{Name: tc.name, Namespace: "default"},
				Spec: DualDeploymentOperatorSpec{
					Source: Source{Kustomize: &KustomizeSource{
						URL:      "https://admission.invalid/repository//root" + tc.query,
						SeedPath: "seed", ShootPath: "shoot",
					}},
					ShootAccess:    ShootAccessRef{SecretName: "shoot-access", Server: "https://shoot.invalid"},
					ShootNamespace: "remote-ns",
				},
			}
			err := k8sClient.Create(t.Context(), cr)
			if tc.accepted {
				if err != nil {
					t.Fatalf("root with nonempty ref must be admitted without fetching content: %v", err)
				}
				return
			}
			if !apierrors.IsInvalid(err) {
				t.Fatalf("expected invalid root URL rejection, got %v", err)
			}
			if !strings.Contains(err.Error(), "kustomize url must include a pinned ref= query parameter") {
				t.Fatalf("expected root-ref CEL message, got %v", err)
			}
		})
	}
}
