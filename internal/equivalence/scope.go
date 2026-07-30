// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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
	// CanonicalNamespace, when set, rewrites each in-scope object's
	// metadata.namespace and any rbac subjects[].namespace to this value before
	// comparison. It neutralizes a namespace-only divergence where today's
	// committed pre-render hardcodes a stale namespace (e.g. "default") while the
	// operator uses the CR's shootNamespace (the value the real shoot uses). Opt-in
	// per fixture and justified there; it normalizes placement only, never other
	// fields, so genuine drift is still caught.
	CanonicalNamespace string
}

func (s Scope) comparesKind(kind string) bool {
	if len(s.ComparedKinds) == 0 {
		return true
	}
	return slices.Contains(s.ComparedKinds, kind)
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
// not a known divergence), with per-fixture IgnoreLabels stripped and, when
// CanonicalNamespace is set, namespace placement canonicalized. Objects are
// re-keyed after namespace canonicalization so identity aligns across sides.
func (s Scope) Apply(set ObjectSet) ObjectSet {
	out := ObjectSet{}
	for _, u := range set {
		if !s.comparesKind(u.GetKind()) {
			continue
		}
		if s.isKnownDivergence(u) {
			continue
		}
		obj := s.stripIgnoredLabels(u)
		obj = s.canonicalizeNamespace(obj)
		out[KeyOf(obj)] = obj
	}
	return out
}

func (s Scope) canonicalizeNamespace(u *unstructured.Unstructured) *unstructured.Unstructured {
	if s.CanonicalNamespace == "" {
		return u
	}
	c := u
	if u.GetNamespace() != "" && u.GetNamespace() != s.CanonicalNamespace {
		c = u.DeepCopy()
		c.SetNamespace(s.CanonicalNamespace)
	}
	subjects, found, err := unstructured.NestedSlice(c.Object, "subjects")
	if err != nil || !found {
		return c
	}
	if c == u {
		c = u.DeepCopy()
		subjects, _, err = unstructured.NestedSlice(c.Object, "subjects")
		if err != nil {
			return c
		}
	}
	changed := false
	for i, raw := range subjects {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if ns, _ := m["namespace"].(string); ns != "" && ns != s.CanonicalNamespace {
			m["namespace"] = s.CanonicalNamespace
			subjects[i] = m
			changed = true
		}
	}
	if changed {
		if err := unstructured.SetNestedSlice(c.Object, subjects, "subjects"); err != nil {
			return c
		}
	}
	return c
}

func (s Scope) stripIgnoredLabels(u *unstructured.Unstructured) *unstructured.Unstructured {
	if len(s.IgnoreLabels) == 0 {
		return u
	}
	labels, found, err := unstructured.NestedMap(u.Object, "metadata", "labels")
	if err != nil || !found {
		return u
	}
	c := u.DeepCopy()
	for _, key := range s.IgnoreLabels {
		delete(labels, key)
	}
	if len(labels) == 0 {
		unstructured.RemoveNestedField(c.Object, "metadata", "labels")
	} else {
		if err := unstructured.SetNestedMap(c.Object, labels, "metadata", "labels"); err != nil {
			return u
		}
	}
	return c
}
