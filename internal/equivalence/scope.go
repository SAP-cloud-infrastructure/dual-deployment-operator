// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// Scope narrows the equivalence comparison per Decision B: only objects whose
// kind is in ComparedKinds are compared, and objects matching a KnownDivergence
// (kind+name) are dropped so they are never reported as missing/extra. An empty
// ComparedKinds means "compare all kinds".
type Scope struct {
	ComparedKinds    []string
	KnownDivergences []ExclusionEntry
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
// not a known divergence).
func (s Scope) Apply(set ObjectSet) ObjectSet {
	out := ObjectSet{}
	for k, u := range set {
		if !s.comparesKind(u.GetKind()) {
			continue
		}
		if s.isKnownDivergence(u) {
			continue
		}
		out[k] = u
	}
	return out
}
