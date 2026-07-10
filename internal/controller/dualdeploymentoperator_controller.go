// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

const requeueInterval = 10 * time.Minute

// DualDeploymentOperatorReconciler reconciles a DualDeploymentOperator object.
type DualDeploymentOperatorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=dual-deployment-operator.cc.sap,resources=dualdeploymentoperators,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=dual-deployment-operator.cc.sap,resources=dualdeploymentoperators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dual-deployment-operator.cc.sap,resources=dualdeploymentoperators/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *DualDeploymentOperatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var cr ddov1alpha1.DualDeploymentOperator
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger.Info("reconciling", "name", cr.Name, "namespace", cr.Namespace)

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DualDeploymentOperatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ddov1alpha1.DualDeploymentOperator{}).
		Named("dualdeploymentoperator").
		Complete(r)
}
