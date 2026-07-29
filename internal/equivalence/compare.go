// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Report accumulates per-resource equivalence findings.
type Report struct {
	Mismatched []string // displayKey + reason
	MissingOp  []string // in golden, absent on operator side
	ExtraOp    []string // on operator side, absent in golden
}

// Equal reports whether the two sides are equivalent.
func (r Report) Equal() bool {
	return len(r.Mismatched) == 0 && len(r.MissingOp) == 0 && len(r.ExtraOp) == 0
}

func (r Report) String() string {
	var b strings.Builder
	for _, m := range r.Mismatched {
		fmt.Fprintf(&b, "MISMATCH %s\n", m)
	}
	for _, m := range r.MissingOp {
		fmt.Fprintf(&b, "MISSING on operator side: %s\n", m)
	}
	for _, e := range r.ExtraOp {
		fmt.Fprintf(&b, "EXTRA on operator side: %s\n", e)
	}
	return b.String()
}

// displayKey renders a human-readable identity including apiVersion.
func displayKey(u *unstructured.Unstructured) string {
	return fmt.Sprintf("%s/%s/%s/%s", u.GetAPIVersion(), u.GetKind(), u.GetNamespace(), u.GetName())
}

// diffPaths recursively walks two normalized values and returns dotted paths where they differ.
func diffPaths(a, b interface{}, prefix string) []string {
	var paths []string

	nextPath := func(key string) string {
		if prefix == "" {
			return key
		}
		return prefix + "." + key
	}

	switch ta := a.(type) {
	case map[string]interface{}:
		tb, ok := b.(map[string]interface{})
		if !ok {
			return []string{prefix}
		}
		allKeys := make(map[string]struct{})
		for k := range ta {
			allKeys[k] = struct{}{}
		}
		for k := range tb {
			allKeys[k] = struct{}{}
		}

		var sortedKeys []string
		for k := range allKeys {
			sortedKeys = append(sortedKeys, k)
		}
		sort.Strings(sortedKeys)

		for _, k := range sortedKeys {
			va, okA := ta[k]
			vb, okB := tb[k]
			if !okA || !okB {
				paths = append(paths, nextPath(k))
			} else {
				paths = append(paths, diffPaths(va, vb, nextPath(k))...)
			}
		}

	case []interface{}:
		tb, ok := b.([]interface{})
		if !ok {
			return []string{prefix}
		}
		if len(ta) != len(tb) {
			return []string{prefix}
		}
		for i := range ta {
			idxPath := fmt.Sprintf("%s[%d]", prefix, i)
			paths = append(paths, diffPaths(ta[i], tb[i], idxPath)...)
		}

	default:
		if !reflect.DeepEqual(a, b) {
			return []string{prefix}
		}
	}

	return paths
}

// Compare normalizes + allowlist-strips both sides and deep-equals per resource.
func Compare(golden, op ObjectSet) Report {
	var r Report
	prep := func(u *unstructured.Unstructured) *unstructured.Unstructured {
		StripAllowlist(u)
		Normalize(u)
		return u
	}
	for k, gobj := range golden {
		oobj, ok := op[k]
		if !ok {
			r.MissingOp = append(r.MissingOp, displayKey(gobj))
			continue
		}
		gPrep := prep(gobj)
		oPrep := prep(oobj)
		if !reflect.DeepEqual(gPrep.Object, oPrep.Object) {
			paths := diffPaths(gPrep.Object, oPrep.Object, "")
			r.Mismatched = append(r.Mismatched, fmt.Sprintf("%s (fields differ: %s)", displayKey(gobj), strings.Join(paths, ", ")))
		}
	}
	for k, oobj := range op {
		if _, ok := golden[k]; !ok {
			r.ExtraOp = append(r.ExtraOp, displayKey(oobj))
		}
	}

	sort.Strings(r.Mismatched)
	sort.Strings(r.MissingOp)
	sort.Strings(r.ExtraOp)

	return r
}
