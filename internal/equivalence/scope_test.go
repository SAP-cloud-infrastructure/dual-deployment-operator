// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import "testing"

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

func TestScopeDropsKnownDivergence(t *testing.T) {
	set := ObjectSet{}
	keep := obj("rbac.authorization.k8s.io/v1", "ClusterRole", "manager-role")
	diverge := obj("rbac.authorization.k8s.io/v1", "ClusterRole", "widgets-admin-role")
	set[KeyOf(keep)] = keep
	set[KeyOf(diverge)] = diverge

	scoped := Scope{
		ComparedKinds:    []string{"ClusterRole"},
		KnownDivergences: []ExclusionEntry{{Kind: "ClusterRole", Name: "widgets-admin-role"}},
	}.Apply(set)

	if _, ok := scoped[KeyOf(keep)]; !ok {
		t.Error("non-divergent ClusterRole must be kept")
	}
	if _, ok := scoped[KeyOf(diverge)]; ok {
		t.Error("known-divergence ClusterRole must be dropped")
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
