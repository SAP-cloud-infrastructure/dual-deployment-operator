// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

func newTestReconciler(objs ...client.Object) *DualDeploymentOperatorReconciler {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(ddov1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &DualDeploymentOperatorReconciler{Client: c, Scheme: scheme}
}

func TestReconcileMissingCRReturnsNoError(t *testing.T) {
	r := newTestReconciler()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "nope", Namespace: "ns"}})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.RequeueAfter != 0 || res.Requeue {
		t.Fatalf("expected empty result for missing CR, got %+v", res)
	}
}

func TestReconcileExistingCRRequeues10m(t *testing.T) {
	cr := &ddov1alpha1.DualDeploymentOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "metal-operator", Namespace: "shoot--cp--m-eu-de-1"},
		Spec: ddov1alpha1.DualDeploymentOperatorSpec{
			Source:           ddov1alpha1.Source{Helm: &ddov1alpha1.HelmSource{Repo: "oci://x", Name: "y", Version: "1.0.0"}},
			RemoteKubeconfig: ddov1alpha1.RemoteKubeconfigRef{SecretName: "kc", Key: "kubeconfig"},
		},
	}
	r := newTestReconciler(cr)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "metal-operator", Namespace: "shoot--cp--m-eu-de-1"}})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.RequeueAfter != 10*time.Minute {
		t.Fatalf("expected RequeueAfter=10m, got %v", res.RequeueAfter)
	}
	if res.Requeue {
		t.Fatalf("expected Requeue=false, got true")
	}
}
