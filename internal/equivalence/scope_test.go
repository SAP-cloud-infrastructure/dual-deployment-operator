// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestScopeFiltersToComparedKinds(t *testing.T) {
	set := ObjectSet{}
	crd := obj("apiextensions.k8s.io/v1", "CustomResourceDefinition", "widgets.example.com")
	iss := obj("cert-manager.io/v1", "Issuer", "selfsigned")
	set[KeyOf(crd)] = crd
	set[KeyOf(iss)] = iss

	scoped := Scope{ComparedKinds: []string{"CustomResourceDefinition"}}.Apply(set)
	if _, ok := scoped[KeyOf(crd)]; !ok {
		t.Error("in-scope CRD must be kept")
	}
	if _, ok := scoped[KeyOf(iss)]; ok {
		t.Error("out-of-scope Issuer must be filtered out")
	}
}

func TestScopeApplyRetainsKnownDivergence(t *testing.T) {
	set := ObjectSet{}
	keep := obj("rbac.authorization.k8s.io/v1", "ClusterRole", "manager-role")
	diverge := obj("rbac.authorization.k8s.io/v1", "ClusterRole", "widgets-admin-role")
	set[KeyOf(keep)] = keep
	set[KeyOf(diverge)] = diverge

	scoped := Scope{
		ComparedKinds:    []string{"ClusterRole"},
		KnownDivergences: []ExclusionEntry{{Kind: "ClusterRole", Name: "widgets-admin-role"}},
	}.Apply(set)

	// Apply must RETAIN known-divergence objects (not drop them). Dropping them
	// pre-comparison would hide a both-sided field mismatch. Suppression is
	// CompareScoped's job, single-sided only.
	if _, ok := scoped[KeyOf(keep)]; !ok {
		t.Error("non-divergent ClusterRole must be kept")
	}
	if _, ok := scoped[KeyOf(diverge)]; !ok {
		t.Error("known-divergence ClusterRole must be RETAINED by Apply (suppression happens in CompareScoped)")
	}
}

func TestCompareScopedSuppressesSingleSidedKnownDivergence(t *testing.T) {
	// A known-divergence object present on only ONE side must not be reported.
	golden := ObjectSet{}
	op := ObjectSet{}
	only := obj("rbac.authorization.k8s.io/v1", "ClusterRole", "metal-token-rotate")
	golden[KeyOf(only)] = only // exists only in golden

	scope := Scope{
		ComparedKinds:    []string{"ClusterRole"},
		KnownDivergences: []ExclusionEntry{{Kind: "ClusterRole", Name: "metal-token-rotate"}},
	}
	r := scope.CompareScoped(scope.Apply(golden), scope.Apply(op))
	if !r.Equal() {
		t.Errorf("single-sided known divergence must be suppressed; got:\n%s", r.String())
	}
}

func TestCompareScopedDoesNotHideBothSidedFieldMismatch(t *testing.T) {
	// The Blocking regression: a known-divergence object that exists on BOTH sides
	// with a genuine field difference MUST still be reported as a mismatch —
	// knownDivergences only suppresses single-sided missing/extra, never field diffs.
	mk := func(rules string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRole",
			"metadata":   map[string]any{"name": "shared-role"},
			"rules":      []any{map[string]any{"verbs": []any{rules}}},
		}}
	}
	g := mk("get")
	o := mk("delete") // same object identity, different field
	golden := ObjectSet{KeyOf(g): g}
	op := ObjectSet{KeyOf(o): o}

	scope := Scope{
		ComparedKinds:    []string{"ClusterRole"},
		KnownDivergences: []ExclusionEntry{{Kind: "ClusterRole", Name: "shared-role"}},
	}
	r := scope.CompareScoped(scope.Apply(golden), scope.Apply(op))
	if r.Equal() {
		t.Fatal("known-divergence entry must NOT hide a both-sided field mismatch (false green)")
	}
	if len(r.Mismatched) != 1 {
		t.Errorf("expected 1 field mismatch on the both-sided divergence object; got %d:\n%s", len(r.Mismatched), r.String())
	}
}

func TestScopeEmptyComparedKindsComparesAll(t *testing.T) {
	set := ObjectSet{}
	iss := obj("cert-manager.io/v1", "Issuer", "selfsigned")
	set[KeyOf(iss)] = iss
	scoped := Scope{}.Apply(set)
	if _, ok := scoped[KeyOf(iss)]; !ok {
		t.Error("empty ComparedKinds means compare all kinds")
	}
}
