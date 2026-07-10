// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	dualdeploymentoperatorv1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

// SetupDualDeploymentOperatorWebhookWithManager registers the webhook for DualDeploymentOperator in the manager.
func SetupDualDeploymentOperatorWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &dualdeploymentoperatorv1alpha1.DualDeploymentOperator{}).
		WithValidator(&DualDeploymentOperatorCustomValidator{}).
		Complete()
}

// NOTE: If you want to customise the 'path', use the flags '--defaulting-path' or '--validation-path'.
// +kubebuilder:webhook:path=/validate-dual-deployment-operator-cc-sap-v1alpha1-dualdeploymentoperator,mutating=false,failurePolicy=fail,sideEffects=None,groups=dual-deployment-operator.cc.sap,resources=dualdeploymentoperators,verbs=create;update,versions=v1alpha1,name=vdualdeploymentoperator-v1alpha1.kb.io,admissionReviewVersions=v1

// DualDeploymentOperatorCustomValidator struct is responsible for validating the DualDeploymentOperator resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type DualDeploymentOperatorCustomValidator struct{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type DualDeploymentOperator.
// v1: all admission validation is handled by CEL rules on the CRD.
func (v *DualDeploymentOperatorCustomValidator) ValidateCreate(_ context.Context, _ *dualdeploymentoperatorv1alpha1.DualDeploymentOperator) (admission.Warnings, error) {
	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type DualDeploymentOperator.
// v1: all admission validation is handled by CEL rules on the CRD.
func (v *DualDeploymentOperatorCustomValidator) ValidateUpdate(_ context.Context, _, _ *dualdeploymentoperatorv1alpha1.DualDeploymentOperator) (admission.Warnings, error) {
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type DualDeploymentOperator.
// v1: all admission validation is handled by CEL rules on the CRD.
func (v *DualDeploymentOperatorCustomValidator) ValidateDelete(_ context.Context, _ *dualdeploymentoperatorv1alpha1.DualDeploymentOperator) (admission.Warnings, error) {
	return nil, nil
}
