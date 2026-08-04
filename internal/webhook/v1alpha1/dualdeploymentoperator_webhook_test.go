// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dualdeploymentoperatorv1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

var _ = Describe("DualDeploymentOperator Webhook", func() {
	var (
		obj       *dualdeploymentoperatorv1alpha1.DualDeploymentOperator
		oldObj    *dualdeploymentoperatorv1alpha1.DualDeploymentOperator
		validator DualDeploymentOperatorCustomValidator
	)

	BeforeEach(func() {
		obj = &dualdeploymentoperatorv1alpha1.DualDeploymentOperator{}
		oldObj = &dualdeploymentoperatorv1alpha1.DualDeploymentOperator{}
		validator = DualDeploymentOperatorCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	Context("When creating or updating DualDeploymentOperator under Validating Webhook", func() {
		// v1: validation is delegated to CEL rules on the CRD, so every hook is a no-op.
		It("admits creation with no warnings or error", func() {
			warnings, err := validator.ValidateCreate(ctx, obj)
			Expect(err).ToNot(HaveOccurred())
			Expect(warnings).To(BeNil())
		})

		It("admits updates with no warnings or error", func() {
			warnings, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).ToNot(HaveOccurred())
			Expect(warnings).To(BeNil())
		})

		It("admits deletion with no warnings or error", func() {
			warnings, err := validator.ValidateDelete(ctx, obj)
			Expect(err).ToNot(HaveOccurred())
			Expect(warnings).To(BeNil())
		})
	})

	Context("HelmSource OCI-only CEL admission (via envtest k8sClient)", func() {
		makeHelmDDO := func(repo string) *dualdeploymentoperatorv1alpha1.DualDeploymentOperator {
			return &dualdeploymentoperatorv1alpha1.DualDeploymentOperator{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "cel-helm-test-",
					Namespace:    "default",
				},
				Spec: dualdeploymentoperatorv1alpha1.DualDeploymentOperatorSpec{
					Source: dualdeploymentoperatorv1alpha1.Source{
						Helm: &dualdeploymentoperatorv1alpha1.HelmSource{
							Repo:    repo,
							Name:    "my-chart",
							Version: "1.0.0",
						},
					},
					ShootNamespace: "demo-ns",
					ShootAccess: dualdeploymentoperatorv1alpha1.ShootAccessRef{
						SecretName: "kc",
						Server:     "https://shoot.example",
					},
				},
			}
		}

		It("rejects an https:// helm repo", func() {
			cr := makeHelmDDO("https://charts.example.com")
			err := k8sClient.Create(ctx, cr)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("oci://"))
		})

		It("rejects a scheme-less helm repo", func() {
			cr := makeHelmDDO("charts.example.com")
			err := k8sClient.Create(ctx, cr)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("oci://"))
		})

		It("admits an oci:// helm repo", func() {
			cr := makeHelmDDO("oci://keppel.global.cloud.sap/ccloud-helm")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
		})
	})

})
