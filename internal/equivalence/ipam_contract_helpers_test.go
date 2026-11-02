// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const ipamTargetLabel = "dual-deployment-operator.cc.sap/webhook-injector"
const ipamSeedNamespace = "shoot--cp--m-qa-de-200"
const ipamConversionURL = "https://ipam-capi-remote-webhook-service:443/convert"

var ipamPoolNames = []string{
	"globalinclusterippools.ipam.cluster.x-k8s.io", "globalinclusterprefixpools.ipam.cluster.x-k8s.io",
	"inclusterippools.ipam.cluster.x-k8s.io", "inclusterprefixpools.ipam.cluster.x-k8s.io",
}

// Lists are copied verbatim from the pinned legacy template / reviewed ruling.
// These are contracts, never values learned from the candidate being compared.
func ipamLegacyArgs() any {
	return ipamJSON(`["--webhook-config-name=ipam-capi-remote-webhook-config","--target-kubeconfig=/var/run/remote-kubeconfig/kubeconfig","--leader-election-id=ipam-capi-remote-webhook-injector-leader","--cert-secret-name=ipam-capi-remote-cert-secret","--cert-sans=ipam-capi-remote-webhook-service","--managed-resource-label=ipam-capi-remote-webhook-injector=true","--external-host=ipam-capi-remote-webhook-service","--external-port=443","--rotation-overlap-window=30s","--rotation-gate-timeout=30s"]`)
}

func ipamTargetArgs() any {
	return ipamJSON(`["--target-kubeconfig=/var/run/remote-kubeconfig/kubeconfig","--target-label=dual-deployment-operator.cc.sap/webhook-injector=ipam-capi-remote","--leader-election-id=ipam-capi-remote-webhook-injector-leader","--cert-secret-name=ipam-capi-remote-cert-secret","--cert-sans=ipam-capi-remote-webhook-service","--rotation-overlap-window=30s","--rotation-gate-timeout=30s"]`)
}

func ipamLegacyRules() any {
	return ipamJSON(`[
{"apiGroups":["admissionregistration.k8s.io"],"resources":["mutatingwebhookconfigurations","validatingwebhookconfigurations"],"verbs":["get","list","watch","create","update","patch"]},
{"apiGroups":["apiextensions.k8s.io"],"resources":["customresourcedefinitions"],"verbs":["get","list","watch"]}]`)
}

func ipamTargetRules() any {
	return ipamJSON(`[
{"apiGroups":["apiextensions.k8s.io"],"resources":["customresourcedefinitions"],"verbs":["get","patch"],"resourceNames":["globalinclusterippools.ipam.cluster.x-k8s.io","globalinclusterprefixpools.ipam.cluster.x-k8s.io","inclusterippools.ipam.cluster.x-k8s.io","inclusterprefixpools.ipam.cluster.x-k8s.io"]},
{"apiGroups":["apiextensions.k8s.io"],"resources":["customresourcedefinitions"],"verbs":["list","watch"]},
{"apiGroups":["admissionregistration.k8s.io"],"resources":["mutatingwebhookconfigurations"],"verbs":["get","patch"],"resourceNames":["ipam-capi-remote-mutating-webhook-configuration"]},
{"apiGroups":["admissionregistration.k8s.io"],"resources":["validatingwebhookconfigurations"],"verbs":["get","patch"],"resourceNames":["ipam-capi-remote-validating-webhook-configuration"]},
{"apiGroups":["admissionregistration.k8s.io"],"resources":["mutatingwebhookconfigurations","validatingwebhookconfigurations"],"verbs":["list","watch"]}]`)
}

func ipamJSON(raw string) any {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		panic(err) // invalid programmer-authored constant
	}
	return value
}

func ipamCopy(set ObjectSet) ObjectSet {
	out := ObjectSet{}
	for key, obj := range set {
		out[key] = obj.DeepCopy()
	}
	return out
}

func ipamFind(set ObjectSet, kind, name, namespace string) *unstructured.Unstructured {
	for _, obj := range set {
		if obj.GetKind() == kind && obj.GetName() == name && obj.GetNamespace() == namespace {
			return obj
		}
	}
	return nil
}

type ipamContractGuard struct{ report Report }

// nil means ABSENT, not null or an empty field. Guard first, strip only on success.
func (g *ipamContractGuard) field(obj *unstructured.Unstructured, want any, path ...string) bool {
	if obj == nil {
		g.report.Mismatched = append(g.report.Mismatched, "contract resource missing: "+strings.Join(path, "."))
		return false
	}
	got, found, err := unstructured.NestedFieldNoCopy(obj.Object, path...)
	if err != nil || (want == nil && found) || (want != nil && (!found || !reflect.DeepEqual(got, want))) {
		g.report.Mismatched = append(g.report.Mismatched, displayKey(obj)+" contract: "+strings.Join(path, "."))
		return false
	}
	return true
}

func (g *ipamContractGuard) delta(old, next *unstructured.Unstructured, oldValue, newValue any, path ...string) {
	a, b := g.field(old, oldValue, path...), g.field(next, newValue, path...)
	if a && b {
		ipamRemoveGuardedField(old.Object, path...)
		ipamRemoveGuardedField(next.Object, path...)
	}
}

func ipamSixTargetKeys() map[ResourceKey]bool {
	keys := map[ResourceKey]bool{
		"admissionregistration.k8s.io/MutatingWebhookConfiguration//ipam-capi-remote-mutating-webhook-configuration":     true,
		"admissionregistration.k8s.io/ValidatingWebhookConfiguration//ipam-capi-remote-validating-webhook-configuration": true,
	}
	for _, name := range ipamPoolNames {
		keys[ResourceKey("apiextensions.k8s.io/CustomResourceDefinition//"+name)] = true
	}
	return keys
}

func ipamCheckSixTargets(seed, shoot ObjectSet) error {
	if err := ipamCheckWebhookTargets(seed, shoot); err != nil {
		return err
	}
	want := ipamSixTargetKeys()
	for _, set := range []ObjectSet{seed, shoot} {
		for key, obj := range set {
			value, found := obj.GetLabels()[ipamTargetLabel]
			if !found {
				continue
			}
			if !want[key] || value != "ipam-capi-remote" {
				return fmt.Errorf("unexpected injector target label: %s", key)
			}
			delete(want, key)
		}
	}
	if len(want) != 0 {
		return fmt.Errorf("missing injector targets: %v", want)
	}
	return nil
}

// These exact losses were checked against the independent pinned Helm render.
// In particular, the restored manager ConfigMap RETAINS chart/version labels.
func ipamProvenanceLabels(kind, name string) map[string]string {
	if kind == "ConfigMap" && name == "ipam-capi-manager-config" {
		return map[string]string{"app.kubernetes.io/managed-by": "Helm"}
	}
	if (kind == "Service" && (name == "ipam-capi-remote-webhook-service" || name == "ipam-capi-webhook-injector-metrics")) ||
		(kind == "Deployment" && name == "ipam-capi-controller-manager") ||
		(kind == "ServiceMonitor" && name == "ipam-capi-webhook-injector") {
		return map[string]string{"helm.sh/chart": "ipam-capi-remote-1.2.37", "app.kubernetes.io/version": "0.1.0", "app.kubernetes.io/managed-by": "Helm"}
	}
	return nil
}

// Only approved, identity-specific old/new leaves are discounted. There are NO
// object exclusions. Raw inputs remain intact for diagnostics and sensitivity.
func compareIPAMContract(golden, candidate Classified, ns string) Report {
	g := &ipamContractGuard{}
	inventories, err := ipamLoadInventory("../../testdata/fixtures/ipam-capi-remote")
	if err != nil {
		return Report{Mismatched: []string{err.Error()}}
	}
	for mode, pair := range map[string][2]ObjectSet{"seed": {golden.Seed, candidate.Seed}, "shoot": {golden.Shoot, candidate.Shoot}} {
		want := inventories[mode]
		for i, key := range want {
			want[i] = strings.ReplaceAll(key, "/"+ipamSeedNamespace+"/", "/"+ns+"/")
		}
		sort.Strings(want)
		for _, set := range pair {
			if err := ipamCheckInventory(set, want); err != nil {
				g.report.Mismatched = append(g.report.Mismatched, mode+": "+err.Error())
			}
		}
	}
	if err := ipamCheckSixTargets(candidate.Seed, candidate.Shoot); err != nil {
		g.report.Mismatched = append(g.report.Mismatched, err.Error())
	}
	old := Classified{Seed: ipamCopy(golden.Seed), Shoot: ipamCopy(golden.Shoot)}
	next := Classified{Seed: ipamCopy(candidate.Seed), Shoot: ipamCopy(candidate.Shoot)}
	for key, a := range old.Seed {
		b := next.Seed[key]
		g.delta(a, b, nil, "additions", "metadata", "annotations", "dual-deployment-operator.cc.sap/origin")
		for label, value := range ipamProvenanceLabels(a.GetKind(), a.GetName()) {
			g.delta(a, b, value, nil, "metadata", "labels", label)
		}
	}
	for _, pair := range [][2]ObjectSet{{old.Seed, next.Seed}, {old.Shoot, next.Shoot}} {
		if len(pair[0]) == 0 || len(pair[1]) == 0 {
			g.report.Mismatched = append(g.report.Mismatched, "empty migration comparison")
		}
	}
	for _, name := range []string{"clusters.cluster.x-k8s.io", "ipaddresses.ipam.cluster.x-k8s.io", "ipaddressclaims.ipam.cluster.x-k8s.io"} {
		a, b := ipamFind(old.Shoot, "CustomResourceDefinition", name, ""), ipamFind(next.Shoot, "CustomResourceDefinition", name, "")
		if g.field(a, nil, "spec", "conversion") && g.field(b, ipamJSON(`{"strategy":"None"}`), "spec", "conversion") {
			ipamRemoveGuardedField(b.Object, "spec", "conversion", "strategy")
		}
		g.field(a, nil, "metadata", "labels", ipamTargetLabel)
		g.field(b, nil, "metadata", "labels", ipamTargetLabel)
	}
	for _, name := range ipamPoolNames {
		a, b := ipamFind(old.Shoot, "CustomResourceDefinition", name, ""), ipamFind(next.Shoot, "CustomResourceDefinition", name, "")
		oldConversion := ipamJSON(`{"strategy":"Webhook","webhook":{"conversionReviewVersions":["v1"],"clientConfig":{"service":{"name":"webhook-service","namespace":"kube-system","path":"/convert"}}}}`)
		newConversion := ipamJSON(`{"strategy":"Webhook","webhook":{"conversionReviewVersions":["v1"],"clientConfig":{"url":"` + ipamConversionURL + `"}}}`)
		okOld, okNew := g.field(a, oldConversion, "spec", "conversion"), g.field(b, newConversion, "spec", "conversion")
		if okOld && okNew {
			ipamRemoveGuardedField(a.Object, "spec", "conversion", "webhook", "clientConfig", "service")
			ipamRemoveGuardedField(b.Object, "spec", "conversion", "webhook", "clientConfig", "url")
		}
		g.delta(a, b, nil, "ipam-capi-remote", "metadata", "labels", ipamTargetLabel)
	}
	for kind, name := range map[string]string{
		"MutatingWebhookConfiguration":   "ipam-capi-remote-mutating-webhook-configuration",
		"ValidatingWebhookConfiguration": "ipam-capi-remote-validating-webhook-configuration",
	} {
		a, b := ipamFind(old.Shoot, kind, name, ""), ipamFind(next.Shoot, kind, name, "")
		g.delta(a, b, nil, "ipam-capi-remote", "metadata", "labels", ipamTargetLabel)
		for _, obj := range []*unstructured.Unstructured{a, b} {
			if obj == nil {
				continue
			}
			webhooks, _, err := unstructured.NestedSlice(obj.Object, "webhooks")
			if err != nil || len(webhooks) != 4 {
				g.report.Mismatched = append(g.report.Mismatched, displayKey(obj)+" must contain four webhooks")
			}
			for _, hook := range webhooks {
				m, ok := hook.(map[string]any)
				if !ok {
					g.report.Mismatched = append(g.report.Mismatched, "invalid webhook")
					continue
				}
				g.field(&unstructured.Unstructured{Object: m}, nil, "clientConfig", "caBundle")
			}
		}
	}
	a, b := ipamFind(old.Seed, "Deployment", "ipam-capi-controller-manager", ns), ipamFind(next.Seed, "Deployment", "ipam-capi-controller-manager", ns)
	g.delta(ipamInjector(a), ipamInjector(b), ipamLegacyArgs(), ipamTargetArgs(), "args")
	g.delta(a, b, nil, "additions", "spec", "template", "metadata", "annotations", "dual-deployment-operator.cc.sap/origin")
	a, b = ipamFind(old.Shoot, "ClusterRole", "ipam-capi-remote-webhook-injector", ""), ipamFind(next.Shoot, "ClusterRole", "ipam-capi-remote-webhook-injector", "")
	g.delta(a, b, ipamLegacyRules(), ipamTargetRules(), "rules")
	for _, pair := range [][3]string{
		{ns, "ipam-capi-remote-webhook-injector", "ipam-capi-remote-webhook-injector"},
		{"kube-system", "ipam-capi-remote-webhook-injector-rolebinding", "ipam-capi-controller-manager"},
	} {
		sets := []ObjectSet{old.Seed, next.Seed}
		if pair[1] != "ipam-capi-remote-webhook-injector" {
			sets = []ObjectSet{old.Shoot, next.Shoot}
		}
		for _, set := range sets {
			binding := ipamFind(set, "ClusterRoleBinding", pair[1], "")
			g.field(binding, []any{map[string]any{"kind": "ServiceAccount", "name": pair[2], "namespace": pair[0]}}, "subjects")
			g.field(binding, ipamJSON(`{"apiGroup":"rbac.authorization.k8s.io","kind":"ClusterRole","name":"ipam-capi-remote-webhook-injector"}`), "roleRef")
			if ipamFind(set, "ServiceAccount", pair[2], pair[0]) == nil {
				g.report.Mismatched = append(g.report.Mismatched, "bound ServiceAccount missing from delivered namespace")
			}
		}
	}
	a, b = ipamFind(old.Seed, "ServiceMonitor", "ipam-capi-webhook-injector", ns), ipamFind(next.Seed, "ServiceMonitor", "ipam-capi-webhook-injector", ns)
	coLocated := true
	for _, set := range []ObjectSet{old.Seed, next.Seed} {
		monitor := ipamFind(set, "ServiceMonitor", "ipam-capi-webhook-injector", ns)
		service := ipamFind(set, "Service", "ipam-capi-webhook-injector-metrics", ns)
		if monitor == nil || service == nil {
			coLocated = false
			continue
		}
		selector, _, err := unstructured.NestedStringMap(monitor.Object, "spec", "selector", "matchLabels")
		if err != nil || len(selector) == 0 {
			coLocated = false
		}
		for key, val := range selector {
			if service.GetLabels()[key] != val {
				coLocated = false
			}
		}
	}
	if coLocated {
		g.delta(a, b, map[string]any{"matchNames": []any{ns}}, nil, "spec", "namespaceSelector")
	} else {
		g.report.Mismatched = append(g.report.Mismatched, "monitor and selected Service are not co-located")
	}
	for _, pair := range [][2]ObjectSet{{old.Seed, next.Seed}, {old.Shoot, next.Shoot}} {
		// No generic allowlist or recursive empty pruning on this strict path.
		r := compareIPAMRemaining(pair[0], pair[1])
		g.report.Mismatched = append(g.report.Mismatched, r.Mismatched...)
		g.report.MissingOp = append(g.report.MissingOp, r.MissingOp...)
		g.report.ExtraOp = append(g.report.ExtraOp, r.ExtraOp...)
	}
	return g.report
}

// Returns a view so removing the guarded args leaf affects only that container.
func ipamInjector(deployment *unstructured.Unstructured) *unstructured.Unstructured {
	if deployment == nil {
		return nil
	}
	value, _, err := unstructured.NestedFieldNoCopy(deployment.Object, "spec", "template", "spec", "initContainers")
	if err != nil {
		return nil
	}
	containers, _ := value.([]any)
	var result *unstructured.Unstructured
	for _, raw := range containers {
		container, ok := raw.(map[string]any)
		if ok && container["name"] == "webhook-injector" {
			if result != nil {
				return nil
			}
			result = &unstructured.Unstructured{Object: container}
		}
	}
	return result
}
