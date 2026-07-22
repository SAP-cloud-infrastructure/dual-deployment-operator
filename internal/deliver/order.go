// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

// Package deliver applies a manifest set to a target cluster via server-side apply.
package deliver

import (
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// applyPriority is the fixed intra-render apply order. Lower sorts first.
// Unlisted kinds fall into the "other" tier (priority 3), stable within it.
//
// Tiers rank by DEPENDENCY ("what must exist before what"), NOT by cluster-vs-
// namespaced scope — the two scopes are interleaved across tiers by that logic
// (Namespace and CRD are cluster-scoped and go early because namespaced objects /
// CRs depend on them; webhooks are cluster-scoped and go last so admission does not
// intercept the earlier applies).
//   - Namespace (0): namespaced objects need their namespace first.
//   - CRD (1): a CR of that kind cannot apply until its CRD is Established.
//   - RBAC (2): one FLAT tier on purpose — ClusterRole/Role/binding/SA have no
//     create-time ordering dependency, because Kubernetes late-binds roleRef and
//     subjects (resolved by the authorizer at request time, not at binding create).
//     A binding applied before its role/SA is valid; it simply grants nothing until
//     the role/SA lands (same reconcile). Do NOT split this into role-before-binding
//     sub-tiers — that guards a failure mode Kubernetes does not have.
//   - other (3): workloads/config that consume the above.
//   - webhook configs (4): applied last so admission webhooks do not intercept or
//     reject the earlier resources in this same render.
func applyPriority(kind string) int {
	switch kind {
	case "Namespace":
		return 0
	case "CustomResourceDefinition":
		return 1
	case "ClusterRole", "ClusterRoleBinding", "Role", "RoleBinding", "ServiceAccount":
		return 2
	case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
		return 4
	default:
		return 3
	}
}

// SortForApply returns a new slice sorted by the fixed kind priority (stable).
func SortForApply(ms []manifest.Manifest) []manifest.Manifest {
	out := append([]manifest.Manifest(nil), ms...)
	sort.SliceStable(out, func(i, j int) bool {
		return applyPriority(out[i].Unstructured.GetKind()) < applyPriority(out[j].Unstructured.GetKind())
	})
	return out
}

// SortForDelete returns the exact reverse of SortForApply.
func SortForDelete(ms []manifest.Manifest) []manifest.Manifest {
	applied := SortForApply(ms)
	out := make([]manifest.Manifest, len(applied))
	for i := range applied {
		out[len(applied)-1-i] = applied[i]
	}
	return out
}

// SortStatusForDelete returns a new slice of ResourceStatus sorted in the reverse
// of the fixed intra-render apply priority (same logic as SortForDelete but operating
// on status entries, using Kind to derive priority).
func SortStatusForDelete(prev []ddov1alpha1.ResourceStatus) []ddov1alpha1.ResourceStatus {
	out := append([]ddov1alpha1.ResourceStatus(nil), prev...)
	sort.SliceStable(out, func(i, j int) bool {
		return applyPriority(out[i].Kind) > applyPriority(out[j].Kind)
	})
	return out
}

// ManifestFromStatus rebuilds a minimal manifest.Manifest from a ResourceStatus,
// carrying only GVK, namespace, and name — enough for Get or Delete calls.
// Group is parsed from APIVersion ("apps/v1" → group "apps"; "v1" → group "").
func ManifestFromStatus(rs ddov1alpha1.ResourceStatus) manifest.Manifest {
	gv, err := schema.ParseGroupVersion(rs.APIVersion)
	if err != nil {
		gv = schema.GroupVersion{}
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gv.Group,
		Version: gv.Version,
		Kind:    rs.Kind,
	})
	u.SetNamespace(rs.Namespace)
	u.SetName(rs.Name)
	return manifest.Manifest{Unstructured: u}
}

// StatusIdentityKey returns a version-independent identity key for a ResourceStatus:
// "group/kind/namespace/name". API version is intentionally excluded so that a
// version upgrade (e.g. v1beta1 → v1) does not create a spurious orphan.
func StatusIdentityKey(rs ddov1alpha1.ResourceStatus) string {
	gv, err := schema.ParseGroupVersion(rs.APIVersion)
	if err != nil {
		gv = schema.GroupVersion{}
	}
	return strings.Join([]string{gv.Group, rs.Kind, rs.Namespace, rs.Name}, "/")
}

// ManifestIdentityKey returns the same version-independent key for a Manifest.
func ManifestIdentityKey(m manifest.Manifest) string {
	gvk := m.Unstructured.GroupVersionKind()
	return strings.Join([]string{gvk.Group, gvk.Kind, m.Unstructured.GetNamespace(), m.Unstructured.GetName()}, "/")
}
