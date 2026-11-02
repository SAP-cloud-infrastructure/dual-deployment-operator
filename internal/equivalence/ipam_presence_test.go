// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestReviewRejectsCrossNamespacePolicyBroadening(t *testing.T) {
	for _, tc := range []struct{ name, direction, peers string }{
		{"ipam-capi-ingress-from-kube-apiserver", "ingress", "from"},
		{"kube-apiserver-egress-to-ipam-capi", "egress", "to"},
	} {
		t.Run(tc.direction, func(t *testing.T) {
			golden, candidate := ipamOfflineContractPair(t)
			if r := compareIPAMContract(golden, candidate, ipamSeedNamespace); !r.Equal() {
				t.Fatalf("approved control differs: %s", r.String())
			}
			policy := ipamFind(candidate.Seed, "NetworkPolicy", tc.name, ipamSeedNamespace)
			peer := policy.Object["spec"].(map[string]any)[tc.direction].([]any)[0].(map[string]any)[tc.peers].([]any)[0].(map[string]any)
			if _, found := peer["namespaceSelector"]; found {
				t.Fatal("baseline unexpectedly has namespaceSelector")
			}
			if _, found := peer["podSelector"]; !found {
				t.Fatal("baseline must select same-namespace apiserver/manager pods")
			}
			// The ONLY change: same-namespace pods become matching pods in ALL
			// namespaces. Do not normalize away this meaningful empty map.
			peer["namespaceSelector"] = map[string]any{}
			r := compareIPAMContract(golden, candidate, ipamSeedNamespace)
			path := "spec." + tc.direction + "[0]." + tc.peers + "[0].namespaceSelector"
			if r.Equal() || !strings.Contains(r.String(), path) {
				t.Fatalf("namespaceSelector:{} broadening escaped comparison at %s: %s", path, r.String())
			}
		})
	}
}

func TestReviewGuardRemovalIsPathLocal(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		oldValue, newValue  any
	}{
		{"newly empty ancestor", `{"metadata":{"name":"x","annotations":{"approved":"additions"}}}`, `{"metadata":{"name":"x"}}`, nil, "additions"},
		{"preexisting empty sibling", `{"metadata":{"name":"x","annotations":{"approved":"additions","empty":{},"null":null}}}`, `{"metadata":{"name":"x","annotations":{"empty":{},"null":null}}}`, nil, "additions"},
		{"already absent leaf", `{"metadata":{"name":"x","annotations":{}}}`, `{"metadata":{"name":"x","annotations":{}}}`, nil, nil},
		{"failed guard", `{"metadata":{"name":"x","annotations":{"approved":"wrong"}}}`, `{"metadata":{"name":"x","annotations":{"approved":"wrong"}}}`, nil, "additions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "x"}}}
			next := &unstructured.Unstructured{Object: ipamJSON(tc.before).(map[string]any)}
			guard := &ipamContractGuard{}
			guard.delta(old, next, tc.oldValue, tc.newValue, "metadata", "annotations", "approved")
			if !reflect.DeepEqual(next.Object, ipamJSON(tc.after)) {
				t.Fatalf("guard removed or retained the wrong fields: %#v", next.Object)
			}
			if (tc.name == "failed guard") == guard.report.Equal() {
				t.Fatalf("unexpected guard result: %s", guard.report.String())
			}
		})
	}
}

func TestReviewNumericCanonicalizationPreservesPresence(t *testing.T) {
	for _, tc := range []struct{ input, want any }{
		{float64(8081), int64(8081)}, {int64(8081), int64(8081)}, {1.5, 1.5},
		{math.Exp2(63), math.Exp2(63)}, {-math.Exp2(63), int64(math.MinInt64)},
		{int64(9007199254740993), int64(9007199254740993)}, {"8081", "8081"},
	} {
		input := map[string]any{"nested": []any{tc.input, map[string]any{}, nil}, "null": nil, "empty": map[string]any{}}
		want := map[string]any{"nested": []any{tc.want, map[string]any{}, nil}, "null": nil, "empty": map[string]any{}}
		if got := ipamCanonicalNumbers(input); !reflect.DeepEqual(got, want) {
			t.Fatalf("canonicalization changed value or presence: got %#v, want %#v", got, want)
		}
	}
}

func TestReviewPreservesSchemaAndPolicyPresence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"empty-map", map[string]any{}},
		{"null", nil},
	} {
		for _, kind := range []string{"policy", "schema"} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				g, c := ipamOfflineContractPair(t)
				if r := compareIPAMContract(g, c, ipamSeedNamespace); !r.Equal() {
					t.Fatalf("approved control differs: %s", r.String())
				}
				var parent map[string]any
				var key, path string
				if kind == "policy" {
					policy := ipamFind(c.Seed, "NetworkPolicy", "ipam-capi-ingress-from-kube-apiserver", ipamSeedNamespace)
					parent = policy.Object["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)["from"].([]any)[0].(map[string]any)
					key, path = "namespaceSelector", "spec.ingress[0].from[0].namespaceSelector"
				} else {
					crd := ipamFind(c.Shoot, "CustomResourceDefinition", "clusters.cluster.x-k8s.io", "")
					parent = crd.Object["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
					key, path = "additionalProperties", "spec.versions[0].schema.openAPIV3Schema.additionalProperties"
				}
				if _, found := parent[key]; found {
					t.Fatalf("baseline already contains %s", path)
				}
				parent[key] = tc.value
				r := compareIPAMContract(g, c, ipamSeedNamespace)
				if r.Equal() || !strings.Contains(r.String(), path) {
					t.Fatalf("%s presence escaped comparison: %s", path, r.String())
				}
			})
		}
	}
}
