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
// metadata.namespace. Cluster-scoped kinds are normalized to an empty namespace
// (a chart that stamps a bogus namespace on a cluster-scoped kind — e.g. the
// upstream metal-operator VWC using .Release.Namespace — would otherwise break
// status-diff prune identity, since the live cluster-scoped object has no
// namespace). Namespaced resources that already declare a namespace are left
// unchanged. An empty ns still normalizes cluster-scoped kinds.
func ApplyNamespace(manifests []Manifest, ns string) {
	for i := range manifests {
		u := manifests[i].Unstructured
		if IsClusterScoped(u.GetKind()) {
			if u.GetNamespace() != "" {
				u.SetNamespace("")
			}
			continue
		}
		if ns == "" {
			continue
		}
		if u.GetNamespace() != "" {
			continue
		}
		u.SetNamespace(ns)
	}
}
