// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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

})
