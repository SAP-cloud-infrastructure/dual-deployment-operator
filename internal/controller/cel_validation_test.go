// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

// These specs exercise the CEL x-kubernetes-validations embedded in the CRD
// against the real envtest API server, which enforces them at admission. They
// reuse the package-level k8sClient/ctx bootstrapped by the Ginkgo suite in
// suite_test.go — no second envtest is started.
var _ = Describe("CRD CEL validation", func() {
	applyCR := func(name string, spec ddov1alpha1.DualDeploymentOperatorSpec) error {
		if spec.ShootNamespace == "" {
			spec.ShootNamespace = "remote-ns"
		}
		cr := &ddov1alpha1.DualDeploymentOperator{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       spec,
		}
		return k8sClient.Create(ctx, cr)
	}
	validHelm := func() ddov1alpha1.Source {
		return ddov1alpha1.Source{Helm: &ddov1alpha1.HelmSource{Repo: "oci://x", Name: "y", Version: "1.0.0"}}
	}
	ra := func() ddov1alpha1.ShootAccessRef {
		return ddov1alpha1.ShootAccessRef{SecretName: "kc", Server: "https://api.example:6443"}
	}

	Context("source discriminator", func() {
		It("rejects when neither source variant is set", func() {
			err := applyCR("cel-src-neither", ddov1alpha1.DualDeploymentOperatorSpec{Source: ddov1alpha1.Source{}, ShootAccess: ra()})
			Expect(err).To(HaveOccurred(), "expected rejection when neither source variant set")
			Expect(err.Error()).To(ContainSubstring("exactly one of source.helm or source.kustomize must be set"))
		})

		It("rejects when both source variants are set", func() {
			err := applyCR("cel-src-both", ddov1alpha1.DualDeploymentOperatorSpec{
				Source: ddov1alpha1.Source{
					Helm:      &ddov1alpha1.HelmSource{Repo: "oci://x", Name: "y", Version: "1.0.0"},
					Kustomize: &ddov1alpha1.KustomizeSource{URL: "https://g//p?ref=v1", SeedPath: "seed", ShootPath: "shoot"},
				},
				ShootAccess: ra(),
			})
			Expect(err).To(HaveOccurred(), "expected rejection when both source variants set")
			Expect(err.Error()).To(ContainSubstring("exactly one of source.helm or source.kustomize must be set"))
		})

		It("accepts a valid helm source", func() {
			err := applyCR("cel-helm-ok", ddov1alpha1.DualDeploymentOperatorSpec{Source: validHelm(), ShootAccess: ra()})
			Expect(err).ToNot(HaveOccurred(), "expected acceptance for valid helm source")
		})

		It("accepts a valid kustomize source", func() {
			err := applyCR("cel-kust-ok", ddov1alpha1.DualDeploymentOperatorSpec{
				Source:      ddov1alpha1.Source{Kustomize: &ddov1alpha1.KustomizeSource{URL: "https://g//p?ref=v1", SeedPath: "seed", ShootPath: "shoot"}},
				ShootAccess: ra(),
			})
			Expect(err).ToNot(HaveOccurred(), "expected acceptance for valid kustomize source")
		})
	})

	Context("kustomize source constraints", func() {
		It("rejects a kustomize url without a pinned ref", func() {
			err := applyCR("cel-kust-noref", ddov1alpha1.DualDeploymentOperatorSpec{
				Source:      ddov1alpha1.Source{Kustomize: &ddov1alpha1.KustomizeSource{URL: "https://g//p", SeedPath: "seed", ShootPath: "shoot"}},
				ShootAccess: ra(),
			})
			Expect(err).To(HaveOccurred(), "expected rejection when kustomize url lacks ?ref=")
			Expect(err.Error()).To(ContainSubstring("kustomize url must include a pinned ref= query parameter"))
		})

		It("rejects a kustomize source missing seedPath", func() {
			err := applyCR("cel-kust-noseed", ddov1alpha1.DualDeploymentOperatorSpec{
				Source:      ddov1alpha1.Source{Kustomize: &ddov1alpha1.KustomizeSource{URL: "https://g//p?ref=v1", ShootPath: "shoot"}},
				ShootAccess: ra(),
			})
			Expect(err).To(HaveOccurred(), "expected rejection when seedPath missing")
		})

		It("rejects a kustomize source with seedPath truly omitted (unstructured)", func() {
			cr := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "dual-deployment-operator.cc.sap/v1alpha1",
				"kind":       "DualDeploymentOperator",
				"metadata":   map[string]any{"name": "cel-kust-noseed-omitted", "namespace": "default"},
				"spec": map[string]any{
					"source": map[string]any{
						"kustomize": map[string]any{"url": "https://g//p?ref=v1", "shootPath": "shoot"},
					},
					"shootAccess":    map[string]any{"secretName": "kc", "server": "https://api.example:6443"},
					"shootNamespace": "remote-ns",
				},
			}}
			err := k8sClient.Create(ctx, cr)
			Expect(err).To(HaveOccurred(), "expected rejection when seedPath key is absent")
		})
	})

	Context("transformation union", func() {
		It("rejects an empty transformation entry", func() {
			err := applyCR("cel-tf-empty", ddov1alpha1.DualDeploymentOperatorSpec{
				Source: validHelm(), ShootAccess: ra(),
				Transformations: []ddov1alpha1.Transformation{{}},
			})
			Expect(err).To(HaveOccurred(), "expected rejection for empty transformation entry")
			Expect(err.Error()).To(ContainSubstring("exactly one transformation type must be set per entry"))
		})

		It("rejects a transformation entry with two fields set", func() {
			err := applyCR("cel-tf-two", ddov1alpha1.DualDeploymentOperatorSpec{
				Source: validHelm(), ShootAccess: ra(),
				Transformations: []ddov1alpha1.Transformation{{
					FilterKinds:       &ddov1alpha1.FilterKindsSpec{Kinds: []string{"Service"}},
					RewriteWebhookURL: &ddov1alpha1.RewriteWebhookURLSpec{URLPrefix: "https://x:443"},
				}},
			})
			Expect(err).To(HaveOccurred(), "expected rejection for two transformation fields in one entry")
			Expect(err.Error()).To(ContainSubstring("exactly one transformation type must be set per entry"))
		})
	})

	Context("patch variant", func() {
		It("rejects a patch with both strategicMerge and jsonPatch set", func() {
			raw := apiextensionsv1.JSON{Raw: []byte(`{"metadata":{"labels":{"a":"b"}}}`)}
			err := applyCR("cel-patch-both", ddov1alpha1.DualDeploymentOperatorSpec{
				Source: validHelm(), ShootAccess: ra(),
				Transformations: []ddov1alpha1.Transformation{{
					Patch: &ddov1alpha1.PatchSpec{
						Target:         ddov1alpha1.Selector{Kind: "Deployment"},
						StrategicMerge: &raw,
						JSONPatch:      []ddov1alpha1.JSONPatchOp{{Op: "add", Path: "/x"}},
					},
				}},
			})
			Expect(err).To(HaveOccurred(), "expected rejection when both patch variants set")
			Expect(err.Error()).To(ContainSubstring("exactly one of patch.strategicMerge or patch.jsonPatch must be set"))
		})

		It("accepts a strategicMerge-only patch", func() {
			raw := apiextensionsv1.JSON{Raw: []byte(`{"metadata":{"labels":{"a":"b"}}}`)}
			err := applyCR("cel-patch-sm-ok", ddov1alpha1.DualDeploymentOperatorSpec{
				Source: validHelm(), ShootAccess: ra(),
				Transformations: []ddov1alpha1.Transformation{{
					Patch: &ddov1alpha1.PatchSpec{Target: ddov1alpha1.Selector{Kind: "Deployment"}, StrategicMerge: &raw},
				}},
			})
			Expect(err).ToNot(HaveOccurred(), "expected acceptance for strategicMerge-only patch")
		})

		It("rejects a JSON Patch with an invalid op", func() {
			err := applyCR("cel-jsonpatch-badop", ddov1alpha1.DualDeploymentOperatorSpec{
				Source: validHelm(), ShootAccess: ra(),
				Transformations: []ddov1alpha1.Transformation{{
					Patch: &ddov1alpha1.PatchSpec{
						Target:    ddov1alpha1.Selector{Kind: "Deployment"},
						JSONPatch: []ddov1alpha1.JSONPatchOp{{Op: "frobnicate", Path: "/x"}},
					},
				}},
			})
			Expect(err).To(HaveOccurred(), "expected rejection for invalid JSON Patch op (enum)")
			Expect(err.Error()).To(ContainSubstring("Unsupported value: \"frobnicate\""))
		})
	})

	Context("defaulting", func() {
		It("defaults retentionPolicy.crds to Retain", func() {
			name := "cel-retpol-default"
			Expect(applyCR(name, ddov1alpha1.DualDeploymentOperatorSpec{Source: validHelm(), ShootAccess: ra()})).To(Succeed())

			var got ddov1alpha1.DualDeploymentOperator
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: "default"}, &got)).To(Succeed())
			Expect(got.Spec.RetentionPolicy.CRDs).To(Equal("Retain"), "expected RetentionPolicy.CRDs to default to Retain")
		})

		It("defaults applyOrder to ShootFirst", func() {
			name := "cel-applyorder-default"
			Expect(applyCR(name, ddov1alpha1.DualDeploymentOperatorSpec{Source: validHelm(), ShootAccess: ra()})).To(Succeed())

			var got ddov1alpha1.DualDeploymentOperator
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: "default"}, &got)).To(Succeed())
			Expect(got.Spec.ApplyOrder).To(Equal("ShootFirst"), "expected ApplyOrder to default to ShootFirst")
		})
	})

	Context("shootNamespace", func() {
		newCR := func(name, shootNamespace string) *unstructured.Unstructured {
			spec := map[string]any{
				"source":      map[string]any{"helm": map[string]any{"repo": "oci://x", "name": "y", "version": "1.0.0"}},
				"shootAccess": map[string]any{"secretName": "kc", "server": "https://api.example:6443"},
			}
			if shootNamespace != "\x00" {
				spec["shootNamespace"] = shootNamespace
			}
			return &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "dual-deployment-operator.cc.sap/v1alpha1",
				"kind":       "DualDeploymentOperator",
				"metadata":   map[string]any{"name": name, "namespace": "default"},
				"spec":       spec,
			}}
		}

		It("rejects a CR without shootNamespace", func() {
			err := k8sClient.Create(ctx, newCR("cel-ns-missing", "\x00"))
			Expect(err).To(HaveOccurred(), "expected rejection when shootNamespace omitted")
			Expect(err.Error()).To(ContainSubstring("shootNamespace"))
		})

		It("rejects a shootNamespace that is not a DNS-1123 label", func() {
			err := k8sClient.Create(ctx, newCR("cel-ns-invalid", "Invalid_NS"))
			Expect(err).To(HaveOccurred(), "expected rejection for invalid shootNamespace")
			Expect(err.Error()).To(ContainSubstring("shootNamespace"))
		})

		It("accepts a valid DNS-1123 shootNamespace", func() {
			err := k8sClient.Create(ctx, newCR("cel-ns-ok", "metal-operator"))
			Expect(err).ToNot(HaveOccurred(), "expected acceptance for valid shootNamespace")
		})
	})
})
