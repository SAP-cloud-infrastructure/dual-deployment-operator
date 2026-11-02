// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
)

// Called ONLY after a successful guard. Delete that leaf and ancestors made
// empty by its deletion. An absent leaf does not authorize touching an already
// empty ancestor, nor may empty/null siblings elsewhere disappear.
func ipamRemoveGuardedField(object map[string]any, path ...string) bool {
	if len(path) == 0 {
		return false
	}
	if len(path) == 1 {
		_, found := object[path[0]]
		if found {
			delete(object, path[0])
		}
		return found
	}
	child, ok := object[path[0]].(map[string]any)
	if !ok || !ipamRemoveGuardedField(child, path[1:]...) {
		return false
	}
	if len(child) == 0 {
		delete(object, path[0])
	}
	return true
}

// Same lossless float64 -> int64 rule as Normalize, but deliberately NO
// empty-map/null pruning. Presence has API semantics in policies and schemas.
func ipamCanonicalNumbers(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			v[key] = ipamCanonicalNumbers(child)
		}
		return v
	case []any:
		for i := range v {
			v[i] = ipamCanonicalNumbers(v[i])
		}
		return v
	case float64:
		if v >= -0x1p63 && v < 0x1p63 && math.Trunc(v) == v {
			return int64(v)
		}
		return v
	default:
		return value
	}
}

// The guarded caller owns these copies. Do not call Compare/Normalize here:
// their generic Helm-fixture allowlist and empty pruning are not IPAM's contract.
func compareIPAMRemaining(golden, candidate ObjectSet) Report {
	var r Report
	for key, old := range golden {
		next, found := candidate[key]
		if !found {
			r.MissingOp = append(r.MissingOp, displayKey(old))
			continue
		}
		a, b := ipamCanonicalNumbers(old.Object), ipamCanonicalNumbers(next.Object)
		if !reflect.DeepEqual(a, b) {
			r.Mismatched = append(r.Mismatched, fmt.Sprintf("%s (fields differ: %s)", displayKey(old), strings.Join(diffPaths(a, b, ""), ", ")))
		}
	}
	for key, next := range candidate {
		if _, found := golden[key]; !found {
			r.ExtraOp = append(r.ExtraOp, displayKey(next))
		}
	}
	sort.Strings(r.Mismatched)
	sort.Strings(r.MissingOp)
	sort.Strings(r.ExtraOp)
	return r
}
