// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

func rs(health ddov1alpha1.HealthState) ddov1alpha1.ResourceStatus {
	return ddov1alpha1.ResourceStatus{Health: health}
}

func TestAnyFailed(t *testing.T) {
	cases := []struct {
		name string
		in   []ddov1alpha1.ResourceStatus
		want bool
	}{
		{"nil slice", nil, false},
		{"empty slice", []ddov1alpha1.ResourceStatus{}, false},
		{"all healthy", []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthHealthy), rs(ddov1alpha1.HealthHealthy)}, false},
		{"one degraded", []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthHealthy), rs(ddov1alpha1.HealthDegraded)}, true},
		{"progressing not failed", []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthProgressing)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := anyFailed(tc.in); got != tc.want {
				t.Fatalf("anyFailed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAllFailed(t *testing.T) {
	cases := []struct {
		name string
		in   []ddov1alpha1.ResourceStatus
		want bool
	}{
		{"nil slice", nil, false},
		{"empty slice", []ddov1alpha1.ResourceStatus{}, false},
		{"all degraded", []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthDegraded), rs(ddov1alpha1.HealthDegraded)}, true},
		{"mixed", []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthDegraded), rs(ddov1alpha1.HealthHealthy)}, false},
		{"single degraded", []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthDegraded)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := allFailed(tc.in); got != tc.want {
				t.Fatalf("allFailed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAnyProgressing(t *testing.T) {
	cases := []struct {
		name string
		in   []ddov1alpha1.ResourceStatus
		want bool
	}{
		{"nil slice", nil, false},
		{"all healthy", []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthHealthy)}, false},
		{"progressing", []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthProgressing)}, true},
		{"unknown counts as progressing", []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthUnknown)}, true},
		{"degraded not progressing", []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthDegraded)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := anyProgressing(tc.in); got != tc.want {
				t.Fatalf("anyProgressing = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReconcileInvalidSourceWritesNotReady(t *testing.T) {
	cr := &ddov1alpha1.DualDeploymentOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "bad-source",
			Namespace:  "ns",
			Finalizers: []string{FinalizerName},
		},
		Spec: ddov1alpha1.DualDeploymentOperatorSpec{
			Source: ddov1alpha1.Source{
				Helm: &ddov1alpha1.HelmSource{Repo: "oci://unused", Name: "demo", Version: "0.1.0"},
			},
			ShootAccess:    ddov1alpha1.ShootAccessRef{SecretName: "s", Server: "https://x"},
			ShootNamespace: "kube-system",
		},
	}
	r := newTestReconciler(cr)

	_, err := r.Reconcile(context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}})
	if err == nil {
		t.Fatal("expected an error when the Helm source has no ChartLoader configured")
	}

	got := &ddov1alpha1.DualDeploymentOperator{}
	if gErr := r.Get(context.Background(), types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, got); gErr != nil {
		t.Fatalf("get CR: %v", gErr)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if cond == nil {
		t.Fatal("expected a Ready condition to be written")
	}
	if cond.Status != metav1.ConditionFalse || cond.Reason != "InvalidSource" {
		t.Fatalf("Ready condition = {%s %s}, want {False InvalidSource}", cond.Status, cond.Reason)
	}
	if got.Status.LastReconcile == nil {
		t.Error("expected LastReconcile to be set on error status")
	}
}

func TestComputeConditions(t *testing.T) {
	cases := []struct {
		name       string
		seed       []ddov1alpha1.ResourceStatus
		shoot      []ddov1alpha1.ResourceStatus
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{
			name:       "all healthy is Ready",
			seed:       []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthHealthy)},
			shoot:      []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthHealthy)},
			wantStatus: metav1.ConditionTrue,
			wantReason: "ReconcileSuccess",
		},
		{
			name:       "empty is Ready",
			seed:       nil,
			shoot:      nil,
			wantStatus: metav1.ConditionTrue,
			wantReason: "ReconcileSuccess",
		},
		{
			name:       "degraded on seed wins over progressing",
			seed:       []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthDegraded)},
			shoot:      []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthProgressing)},
			wantStatus: metav1.ConditionFalse,
			wantReason: "ResourcesDegraded",
		},
		{
			name:       "degraded on shoot is not Ready",
			seed:       []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthHealthy)},
			shoot:      []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthDegraded)},
			wantStatus: metav1.ConditionFalse,
			wantReason: "ResourcesDegraded",
		},
		{
			name:       "progressing without degraded reports Progressing",
			seed:       []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthHealthy)},
			shoot:      []ddov1alpha1.ResourceStatus{rs(ddov1alpha1.HealthProgressing)},
			wantStatus: metav1.ConditionFalse,
			wantReason: "Progressing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conds := computeConditions(tc.seed, tc.shoot)
			if len(conds) != 1 {
				t.Fatalf("computeConditions returned %d conditions, want 1", len(conds))
			}
			c := conds[0]
			if c.Type != "Ready" {
				t.Errorf("condition type = %q, want Ready", c.Type)
			}
			if c.Status != tc.wantStatus {
				t.Errorf("condition status = %q, want %q", c.Status, tc.wantStatus)
			}
			if c.Reason != tc.wantReason {
				t.Errorf("condition reason = %q, want %q", c.Reason, tc.wantReason)
			}
		})
	}
}
