// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// allowlistedLabelKeys are provenance/incidental labels expected to differ
// between a Helm-rendered chart and the operator render. This list MUST NOT
// contain any transformation-produced key (e.g. the injector --target-label).
var allowlistedLabelKeys = []string{
	"helm.sh/chart",
	"helm.sh/resource-policy",
	"app.kubernetes.io/managed-by",
	"app.kubernetes.io/version",
	"dual-deployment-operator.cc.sap/origin",
	"dual-deployment-operator.cc.sap/owned-by",
}

// StripAllowlist removes provenance/incidental labels and annotations in place,
// before deep-equal. It never removes a transformation-output field.
func StripAllowlist(u *unstructured.Unstructured) {
	stripKeys(u, "metadata", "labels")
	stripKeys(u, "metadata", "annotations")
}

func stripKeys(u *unstructured.Unstructured, path ...string) {
	m, found, _ := unstructured.NestedMap(u.Object, path...)
	if !found {
		return
	}
	for _, k := range allowlistedLabelKeys {
		delete(m, k)
	}
	if len(m) == 0 {
		unstructured.RemoveNestedField(u.Object, path...)
		return
	}
	_ = unstructured.SetNestedMap(u.Object, m, path...)
}
