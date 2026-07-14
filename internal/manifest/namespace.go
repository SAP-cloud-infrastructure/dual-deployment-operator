// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package manifest

// clusterScopedKinds is a static set of well-known cluster-scoped kinds, used
// to decide namespace application while rendering is offline (no RESTMapper).
// A live mapper can replace this in the reconciler phase.
var clusterScopedKinds = map[string]struct{}{
	"CustomResourceDefinition":       {},
	"ClusterRole":                    {},
	"ClusterRoleBinding":             {},
	"Namespace":                      {},
	"PriorityClass":                  {},
	"StorageClass":                   {},
	"ClusterIssuer":                  {},
	"APIService":                     {},
	"ValidatingWebhookConfiguration": {},
	"MutatingWebhookConfiguration":   {},
	"PersistentVolume":               {},
	"IngressClass":                   {},
	"RuntimeClass":                   {},
	"PriorityLevelConfiguration":     {},
	"FlowSchema":                     {},
	"CSIDriver":                      {},
	"CSINode":                        {},
	"VolumeAttachment":               {},
	"NodeClass":                      {},
}

// IsClusterScoped reports whether kind is a known cluster-scoped kind.
func IsClusterScoped(kind string) bool {
	_, ok := clusterScopedKinds[kind]
	return ok
}

// ApplyNamespace stamps ns onto each namespaced manifest that lacks an explicit
// metadata.namespace. Cluster-scoped kinds and resources that already declare a
// namespace are left unchanged. An empty ns is a no-op.
func ApplyNamespace(manifests []Manifest, ns string) {
	if ns == "" {
		return
	}
	for i := range manifests {
		u := manifests[i].Unstructured
		if IsClusterScoped(u.GetKind()) {
			continue
		}
		if u.GetNamespace() != "" {
			continue
		}
		u.SetNamespace(ns)
	}
}
