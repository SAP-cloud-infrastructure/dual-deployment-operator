// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"slices"
	"strings"

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

// Apply returns a copy of set containing only in-scope objects (compared kind),
// with per-fixture IgnoreLabels stripped and, when CanonicalNamespace is set,
// namespace placement canonicalized. Objects are re-keyed after namespace
// canonicalization so identity aligns across sides.
//
// Known-divergence objects are deliberately RETAINED here — they are NOT dropped
// before comparison. Dropping them from both sides would let a both-sided object
// with a genuine field difference vanish silently (false green). Suppression of
// known divergences happens in CompareScoped, and ONLY for single-sided
// missing/extra — never for a field mismatch on an object present on both sides.
func (s Scope) Apply(set ObjectSet) ObjectSet {
	out := ObjectSet{}
	for _, u := range set {
		if !s.comparesKind(u.GetKind()) {
			continue
		}
		obj := s.stripIgnoredLabels(u)
		obj = s.canonicalizeNamespace(obj)
		out[KeyOf(obj)] = obj
	}
	return out
}

// CompareScoped runs Compare over the scoped golden/operator sets, then drops
// ONLY the single-sided (missing/extra) findings whose object is a declared
// known divergence. Field mismatches on both-sided objects are never suppressed,
// so a known-divergence entry cannot hide real drift.
func (s Scope) CompareScoped(golden, op ObjectSet) Report {
	r := Compare(golden, op)
	if len(s.KnownDivergences) == 0 {
		return r
	}
	r.MissingOp = s.dropKnownDivergent(r.MissingOp)
	r.ExtraOp = s.dropKnownDivergent(r.ExtraOp)
	return r
}

// dropKnownDivergent removes entries whose displayKey names a known-divergence
// kind+name. displayKey is "<apiVersion>/<kind>/<namespace>/<name>", so we match
// on the kind (3rd-from-... ) and name (last) segments.
func (s Scope) dropKnownDivergent(keys []string) []string {
	out := keys[:0:0]
	for _, k := range keys {
		if !s.keyIsKnownDivergence(k) {
			out = append(out, k)
		}
	}
	return out
}

func (s Scope) keyIsKnownDivergence(displayKey string) bool {
	parts := strings.Split(displayKey, "/")
	if len(parts) < 4 {
		return false
	}
	// parts: [apiVersionGroup..., kind, namespace, name] — kind is 2nd, name is last.
	kind := parts[len(parts)-3]
	name := parts[len(parts)-1]
	for _, e := range s.KnownDivergences {
		if e.Kind == kind && e.Name == name {
			return true
		}
	}
	return false
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
