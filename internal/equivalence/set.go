// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ResourceKey is the version-independent identity of a resource:
// group/kind/namespace/name. Version is deliberately excluded so an API-group
// version bump updates the same logical object rather than reading as add+remove.
type ResourceKey string

// KeyOf computes the version-independent key for an object.
func KeyOf(u *unstructured.Unstructured) ResourceKey {
	gvk := u.GroupVersionKind()
	return ResourceKey(fmt.Sprintf("%s/%s/%s/%s", gvk.Group, gvk.Kind, u.GetNamespace(), u.GetName()))
}

// schemaGVK parses an apiVersion+kind into a GroupVersionKind.
func schemaGVK(apiVersion, kind string) schema.GroupVersionKind {
	gv, _ := schema.ParseGroupVersion(apiVersion)
	return gv.WithKind(kind)
}

// ObjectSet is a keyed collection of bare objects for comparison.
type ObjectSet map[ResourceKey]*unstructured.Unstructured

// Keys returns the sorted keys for deterministic iteration.
func (s ObjectSet) Keys() []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}