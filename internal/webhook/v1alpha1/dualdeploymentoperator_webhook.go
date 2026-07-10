/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	dualdeploymentoperatorv1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var dualdeploymentoperatorlog = logf.Log.WithName("dualdeploymentoperator-resource")

// SetupDualDeploymentOperatorWebhookWithManager registers the webhook for DualDeploymentOperator in the manager.
func SetupDualDeploymentOperatorWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &dualdeploymentoperatorv1alpha1.DualDeploymentOperator{}).
		WithValidator(&DualDeploymentOperatorCustomValidator{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: If you want to customise the 'path', use the flags '--defaulting-path' or '--validation-path'.
// +kubebuilder:webhook:path=/validate-dual-deployment-operator-cc-sap-v1alpha1-dualdeploymentoperator,mutating=false,failurePolicy=fail,sideEffects=None,groups=dual-deployment-operator.cc.sap,resources=dualdeploymentoperators,verbs=create;update,versions=v1alpha1,name=vdualdeploymentoperator-v1alpha1.kb.io,admissionReviewVersions=v1

// DualDeploymentOperatorCustomValidator struct is responsible for validating the DualDeploymentOperator resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type DualDeploymentOperatorCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type DualDeploymentOperator.
func (v *DualDeploymentOperatorCustomValidator) ValidateCreate(_ context.Context, obj *dualdeploymentoperatorv1alpha1.DualDeploymentOperator) (admission.Warnings, error) {
	dualdeploymentoperatorlog.Info("Validation for DualDeploymentOperator upon creation", "name", obj.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type DualDeploymentOperator.
func (v *DualDeploymentOperatorCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *dualdeploymentoperatorv1alpha1.DualDeploymentOperator) (admission.Warnings, error) {
	dualdeploymentoperatorlog.Info("Validation for DualDeploymentOperator upon update", "name", newObj.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type DualDeploymentOperator.
func (v *DualDeploymentOperatorCustomValidator) ValidateDelete(_ context.Context, obj *dualdeploymentoperatorv1alpha1.DualDeploymentOperator) (admission.Warnings, error) {
	dualdeploymentoperatorlog.Info("Validation for DualDeploymentOperator upon deletion", "name", obj.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
