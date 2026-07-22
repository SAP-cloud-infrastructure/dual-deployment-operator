// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package deliver

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

func TestDeploymentHealthProgressingUntilAvailable(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"kind":   "Deployment",
		"spec":   map[string]any{"replicas": int64(2)},
		"status": map[string]any{"availableReplicas": int64(1)},
	}}
	if got := computeHealth(u); got != ddov1alpha1.HealthProgressing {
		t.Errorf("health = %q, want Progressing", got)
	}
	u.Object["status"] = map[string]any{"availableReplicas": int64(2)}
	if got := computeHealth(u); got != ddov1alpha1.HealthHealthy {
		t.Errorf("health = %q, want Healthy", got)
	}
}

func TestWebhookConfigHealthIsExistenceOnly(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"kind": "ValidatingWebhookConfiguration"}}
	if got := computeHealth(u); got != ddov1alpha1.HealthHealthy {
		t.Errorf("health = %q, want Healthy (existence-only, caBundle ignored)", got)
	}
}

func TestNilObjectIsUnknown(t *testing.T) {
	if got := computeHealth(nil); got != ddov1alpha1.HealthUnknown {
		t.Errorf("health = %q, want Unknown", got)
	}
}
