// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import "testing"

func TestCacheKey_DistinctDimensions(t *testing.T) {
	base := keyParts{sourceKind: "helm", repoScope: "oci:reg.example.com", resolvedID: "sha256:aaa", mode: "seed", inputHash: "h1", namespace: "ns1"}
	k := base.String()

	cases := []keyParts{
		func() keyParts { c := base; c.sourceKind = "kustomize"; return c }(),
		func() keyParts { c := base; c.repoScope = "http:reg.example.com"; return c }(),
		func() keyParts { c := base; c.resolvedID = "sha256:bbb"; return c }(),
		func() keyParts { c := base; c.mode = "shoot"; return c }(),
		func() keyParts { c := base; c.inputHash = "h2"; return c }(),
		func() keyParts { c := base; c.namespace = "ns2"; return c }(),
	}
	for i, c := range cases {
		if c.String() == k {
			t.Fatalf("case %d: expected distinct key, got same as base %q", i, k)
		}
	}
}

func TestCacheKey_OCIvsHTTPNoCollision(t *testing.T) {
	oci := keyParts{sourceKind: "helm", repoScope: "oci:reg.example.com", resolvedID: "x", mode: "seed", inputHash: "h", namespace: "ns"}
	http := oci
	http.repoScope = "http:reg.example.com"
	if oci.String() == http.String() {
		t.Fatal("OCI and HTTP sources must not share a cache key")
	}
}

func TestHashValues_OrderInsensitive(t *testing.T) {
	a := map[string]any{"a": 1, "b": map[string]any{"c": 2, "d": 3}}
	b := map[string]any{"b": map[string]any{"d": 3, "c": 2}, "a": 1}
	if hashValues(a) != hashValues(b) {
		t.Fatal("hashValues must be insensitive to map key ordering")
	}
	c := map[string]any{"a": 2}
	if hashValues(a) == hashValues(c) {
		t.Fatal("different values must hash differently")
	}
}
