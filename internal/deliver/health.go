// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package deliver

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

func computeHealth(u *unstructured.Unstructured) ddov1alpha1.HealthState {
	if u == nil {
		return ddov1alpha1.HealthUnknown
	}
	switch u.GetKind() {
	case "Deployment", "StatefulSet":
		return replicaHealth(u)
	case "CustomResourceDefinition":
		return crdHealth(u)
	// Only kinds with an async readiness signal the operator can observe are graded
	// deeper (replicas above, CRD Established above). Everything else is existence-only:
	//   - Service: no candidate render delivers a LoadBalancer Service (all profiles
	//     filterKinds out Service; only ClusterIP-family would survive, which is ready
	//     on creation). Do NOT add a Service case — LoadBalancer grading would be dead
	//     code and ClusterIP is already correct here.
	//   - ValidatingWebhookConfiguration / MutatingWebhookConfiguration: the operator
	//     does not own caBundle, so it must not grade health on it (caBundle strip).
	// All fall through to exists=Healthy.
	default:
		return ddov1alpha1.HealthHealthy
	}
}

func replicaHealth(u *unstructured.Unstructured) ddov1alpha1.HealthState {
	replicas, _, errR := unstructured.NestedInt64(u.Object, "spec", "replicas")
	available, _, errA := unstructured.NestedInt64(u.Object, "status", "availableReplicas")
	if errR != nil || errA != nil {
		return ddov1alpha1.HealthProgressing
	}
	if available >= replicas {
		return ddov1alpha1.HealthHealthy
	}
	return ddov1alpha1.HealthProgressing
}

func crdHealth(u *unstructured.Unstructured) ddov1alpha1.HealthState {
	conds, _, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err != nil {
		return ddov1alpha1.HealthProgressing
	}
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cm["type"] == "Established" && cm["status"] == "True" {
			return ddov1alpha1.HealthHealthy
		}
	}
	return ddov1alpha1.HealthProgressing
}
