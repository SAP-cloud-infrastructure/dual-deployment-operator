// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// Scope narrows the equivalence comparison per Decision B: only objects whose
// kind is in ComparedKinds are compared, objects matching a KnownDivergence
// (kind+name) are dropped so they are never reported as missing/extra, and
// IgnoreLabels are per-fixture label keys stripped before comparison (legacy
// delivery-mechanism labels baked into today's pre-render that the operator's
// clean render does not reproduce, e.g. an old ManagedResource injector label).
// An empty ComparedKinds means "compare all kinds".
type Scope struct {
	ComparedKinds    []string
	KnownDivergences []ExclusionEntry
	IgnoreLabels     []string
}

func (s Scope) comparesKind(kind string) bool {
	if len(s.ComparedKinds) == 0 {
		return true
	}
	for _, k := range s.ComparedKinds {
		if k == kind {
			return true
		}
	}
	return false
}

func (s Scope) isKnownDivergence(u *unstructured.Unstructured) bool {
	for _, e := range s.KnownDivergences {
		if u.GetKind() == e.Kind && u.GetName() == e.Name {
			return true
		}
	}
	return false
}

// Apply returns a copy of set containing only in-scope objects (compared kind,
// not a known divergence), with per-fixture IgnoreLabels stripped.
func (s Scope) Apply(set ObjectSet) ObjectSet {
	out := ObjectSet{}
	for k, u := range set {
		if !s.comparesKind(u.GetKind()) {
			continue
		}
		if s.isKnownDivergence(u) {
			continue
		}
		out[k] = s.stripIgnoredLabels(u)
	}
	return out
}

func (s Scope) stripIgnoredLabels(u *unstructured.Unstructured) *unstructured.Unstructured {
	if len(s.IgnoreLabels) == 0 {
		return u
	}
	labels, found, _ := unstructured.NestedMap(u.Object, "metadata", "labels")
	if !found {
		return u
	}
	c := u.DeepCopy()
	for _, key := range s.IgnoreLabels {
		delete(labels, key)
	}
	if len(labels) == 0 {
		unstructured.RemoveNestedField(c.Object, "metadata", "labels")
	} else {
		_ = unstructured.SetNestedMap(c.Object, labels, "metadata", "labels")
	}
	return c
}
