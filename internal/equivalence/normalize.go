// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// Normalize canonicalizes an object in place so incidental differences
// (empty maps, nil values) do not cause false mismatches. Map key ordering is
// already normalized by Go map semantics + reflect.DeepEqual, so only empties
// and nils are pruned here.
func Normalize(u *unstructured.Unstructured) {
	u.Object = pruneEmpties(u.Object).(map[string]any)
}

func pruneEmpties(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			pruned := pruneEmpties(child)
			if isEmpty(pruned) {
				delete(t, k)
				continue
			}
			t[k] = pruned
		}
		return t
	case []any:
		for i := range t {
			t[i] = pruneEmpties(t[i])
		}
		return t
	default:
		return v
	}
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}
