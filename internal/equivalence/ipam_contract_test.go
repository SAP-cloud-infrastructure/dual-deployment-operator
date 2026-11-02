// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/transform"
)

func TestIPAMMigrationContract(t *testing.T) {
	golden, candidate := ipamOfflineContractPair(t)
	before, err := json.Marshal([]Classified{golden, candidate})
	if err != nil {
		t.Fatal(err)
	}
	if report := compareIPAMContract(golden, candidate, "shoot--cp--m-qa-de-200"); !report.Equal() {
		t.Fatal(report.String())
	}
	after, err := json.Marshal([]Classified{golden, candidate})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("guarded comparison mutated raw evidence")
	}
}

func TestIPAMMigrationProvenance(t *testing.T) {
	for _, identity := range [][2]string{
		{"Service", "ipam-capi-remote-webhook-service"},
		{"Service", "ipam-capi-webhook-injector-metrics"},
		{"Deployment", "ipam-capi-controller-manager"},
		{"ServiceMonitor", "ipam-capi-webhook-injector"},
		{"ConfigMap", "ipam-capi-manager-config"},
	} {
		for _, label := range []string{"helm.sh/chart", "app.kubernetes.io/version", "app.kubernetes.io/managed-by", "app.kubernetes.io/instance"} {
			for _, side := range []string{"legacy", "candidate"} {
				t.Run(identity[0]+"/"+identity[1]+"/"+label+"/"+side, func(t *testing.T) {
					g, c := ipamOfflineContractPair(t)
					set := c.Seed
					if side == "legacy" {
						set = g.Seed
					}
					ipamSet(t, ipamFind(set, identity[0], identity[1], ipamSeedNamespace), "wrong-provenance", "metadata", "labels", label)
					if compareIPAMContract(g, c, ipamSeedNamespace).Equal() {
						t.Fatal("generic allowlist hid provenance drift")
					}
				})
			}
		}
	}
	g, _ := ipamOfflineContractPair(t)
	for key := range g.Seed {
		for _, value := range []string{"", "wrong"} {
			t.Run(string(key)+"/origin="+value, func(t *testing.T) {
				g, c := ipamOfflineContractPair(t)
				if value == "" {
					unstructured.RemoveNestedField(c.Seed[key].Object, "metadata", "annotations", manifest.OriginAnnotation)
				} else {
					ipamSet(t, c.Seed[key], value, "metadata", "annotations", manifest.OriginAnnotation)
				}
				if compareIPAMContract(g, c, ipamSeedNamespace).Equal() {
					t.Fatal("missing/wrong additions origin accepted")
				}
			})
		}
	}
	t.Run("manager-config-retained-labels", func(t *testing.T) {
		for _, label := range []string{"helm.sh/chart", "app.kubernetes.io/version", "app.kubernetes.io/name", "app.kubernetes.io/instance"} {
			g, c := ipamOfflineContractPair(t)
			unstructured.RemoveNestedField(ipamFind(c.Seed, "ConfigMap", "ipam-capi-manager-config", ipamSeedNamespace).Object, "metadata", "labels", label)
			if compareIPAMContract(g, c, ipamSeedNamespace).Equal() {
				t.Fatalf("retained manager label removed: %s", label)
			}
		}
	})
}

func TestIPAMMigrationContractInventory(t *testing.T) {
	golden, _ := ipamOfflineContractPair(t)
	if len(golden.Seed) != 15 || len(golden.Shoot) != 25 {
		t.Fatal("pinned historical inventory changed")
	}
	for mode, set := range map[string]ObjectSet{"seed": golden.Seed, "shoot": golden.Shoot} {
		for key := range set {
			t.Run(mode+"/"+string(key), func(t *testing.T) {
				g, c := ipamOfflineContractPair(t)
				a, b := g.Seed, c.Seed
				if mode == "shoot" {
					a, b = g.Shoot, c.Shoot
				}
				delete(b, key)
				if compareIPAMContract(g, c, ipamSeedNamespace).Equal() {
					t.Fatal("missing candidate identity accepted")
				}
				delete(a, key)
				if compareIPAMContract(g, c, ipamSeedNamespace).Equal() {
					t.Fatal("both-sided missing identity accepted")
				}
			})
		}
	}
}

func TestIPAMMigrationContractSensitivity(t *testing.T) {
	tests := map[string]func(*testing.T, Classified, Classified){}
	for key := range ipamSixTargetKeys() {
		for _, value := range []string{"", "wrong"} {
			tests[string(key)+"/label="+value] = func(_ *testing.T, _ Classified, c Classified) {
				labels := c.Shoot[key].GetLabels()
				labels[ipamTargetLabel] = value
				if value == "" {
					delete(labels, ipamTargetLabel)
				}
				c.Shoot[key].SetLabels(labels)
			}
		}
	}
	for _, name := range []string{"clusters.cluster.x-k8s.io", "ipaddresses.ipam.cluster.x-k8s.io", "ipaddressclaims.ipam.cluster.x-k8s.io"} {
		tests[name+"/core-conversion"] = func(t *testing.T, _ Classified, c Classified) {
			ipamSet(t, ipamFind(c.Shoot, "CustomResourceDefinition", name, ""), "Webhook", "spec", "conversion", "strategy")
		}
		tests[name+"/core-target"] = func(t *testing.T, _ Classified, c Classified) {
			ipamSet(t, ipamFind(c.Shoot, "CustomResourceDefinition", name, ""), "ipam-capi-remote", "metadata", "labels", ipamTargetLabel)
		}
		tests[name+"/historical-conversion"] = func(t *testing.T, g Classified, _ Classified) {
			ipamSet(t, ipamFind(g.Shoot, "CustomResourceDefinition", name, ""), ipamJSON(`{"strategy":"None"}`), "spec", "conversion")
		}
	}
	for _, name := range ipamPoolNames {
		for suffix, value := range map[string]any{"url": "https://wrong.invalid/convert", "caBundle": "captured-ca", "service": ipamJSON(`{"name":"wrong","namespace":"kube-system"}`)} {
			tests[name+"/"+suffix] = func(t *testing.T, _ Classified, c Classified) {
				ipamSet(t, ipamFind(c.Shoot, "CustomResourceDefinition", name, ""), value, "spec", "conversion", "webhook", "clientConfig", suffix)
			}
		}
		tests[name+"/empty-static-ca"] = func(t *testing.T, _ Classified, c Classified) {
			ipamSet(t, ipamFind(c.Shoot, "CustomResourceDefinition", name, ""), "", "spec", "conversion", "webhook", "clientConfig", "caBundle")
		}
		tests[name+"/path"] = func(t *testing.T, _ Classified, c Classified) {
			ipamSet(t, ipamFind(c.Shoot, "CustomResourceDefinition", name, ""), "https://ipam-capi-remote-webhook-service:443/wrong", "spec", "conversion", "webhook", "clientConfig", "url")
		}
		tests[name+"/review-versions"] = func(t *testing.T, _ Classified, c Classified) {
			ipamSet(t, ipamFind(c.Shoot, "CustomResourceDefinition", name, ""), []any{"v1beta1"}, "spec", "conversion", "webhook", "conversionReviewVersions")
		}
		tests[name+"/historical-service"] = func(t *testing.T, g Classified, _ Classified) {
			ipamSet(t, ipamFind(g.Shoot, "CustomResourceDefinition", name, ""), "different", "spec", "conversion", "webhook", "clientConfig", "service", "name")
		}
		tests[name+"/historical-ca"] = func(t *testing.T, g Classified, _ Classified) {
			ipamSet(t, ipamFind(g.Shoot, "CustomResourceDefinition", name, ""), "captured-ca", "spec", "conversion", "webhook", "clientConfig", "caBundle")
		}
	}
	g, _ := ipamOfflineContractPair(t)
	for key, obj := range g.Shoot {
		if obj.GetKind() == "CustomResourceDefinition" {
			tests[string(key)+"/schema"] = func(_ *testing.T, _ Classified, c Classified) {
				c.Shoot[key].Object["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)["type"] = "string"
			}
		}
		if strings.HasSuffix(obj.GetKind(), "WebhookConfiguration") {
			for field, value := range map[string]any{"caBundle": "captured-ca", "url": "https://wrong.invalid/path"} {
				tests[string(key)+"/"+field] = func(_ *testing.T, _ Classified, c Classified) {
					c.Shoot[key].Object["webhooks"].([]any)[0].(map[string]any)["clientConfig"].(map[string]any)[field] = value
				}
			}
			tests[string(key)+"/policy"] = func(_ *testing.T, _ Classified, c Classified) {
				c.Shoot[key].Object["webhooks"].([]any)[0].(map[string]any)["failurePolicy"] = "Ignore"
			}
			tests[string(key)+"/entry-removed"] = func(_ *testing.T, _ Classified, c Classified) {
				c.Shoot[key].Object["webhooks"] = c.Shoot[key].Object["webhooks"].([]any)[:3]
			}
		}
	}
	for key, obj := range g.Seed {
		if obj.GetKind() == "NetworkPolicy" {
			tests[string(key)+"/policy"] = func(t *testing.T, _ Classified, c Classified) {
				ipamSet(t, c.Seed[key], []any{"Ingress", "Egress"}, "spec", "policyTypes")
			}
		}
	}
	for _, side := range []string{"legacy", "candidate"} {
		for _, prefix := range []string{"--cert-sans=", "--target-kubeconfig=", "--rotation-overlap-window=", "--cert-secret-name="} {
			tests[side+"/"+prefix] = func(_ *testing.T, g, c Classified) {
				set := c.Seed
				if side == "legacy" {
					set = g.Seed
				}
				args := ipamInjector(ipamFind(set, "Deployment", "ipam-capi-controller-manager", ipamSeedNamespace)).Object["args"].([]any)
				for i, arg := range args {
					if strings.HasPrefix(arg.(string), prefix) {
						args[i] = prefix + "wrong"
					}
				}
			}
		}
	}
	tests["duplicate-selector"] = func(_ *testing.T, _ Classified, c Classified) {
		obj := ipamInjector(ipamFind(c.Seed, "Deployment", "ipam-capi-controller-manager", ipamSeedNamespace))
		obj.Object["args"] = append(obj.Object["args"].([]any), "--target-label="+ipamTargetLabel+"=ipam-capi-remote")
	}
	tests["injector-image"] = func(_ *testing.T, _ Classified, c Classified) {
		ipamInjector(ipamFind(c.Seed, "Deployment", "ipam-capi-controller-manager", ipamSeedNamespace)).Object["image"] = "example.invalid/wrong:v1"
	}
	tests["manager-image"] = func(_ *testing.T, _ Classified, c Classified) {
		ipamFind(c.Seed, "Deployment", "ipam-capi-controller-manager", ipamSeedNamespace).Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "example.invalid/wrong:v1"
	}
	tests["origin-value"] = func(t *testing.T, _ Classified, c Classified) {
		ipamSet(t, ipamFind(c.Seed, "Deployment", "ipam-capi-controller-manager", ipamSeedNamespace), "wrong", "spec", "template", "metadata", "annotations", "dual-deployment-operator.cc.sap/origin")
	}
	tests["origin-legacy-present"] = func(t *testing.T, g Classified, _ Classified) {
		ipamSet(t, ipamFind(g.Seed, "Deployment", "ipam-capi-controller-manager", ipamSeedNamespace), "additions", "spec", "template", "metadata", "annotations", "dual-deployment-operator.cc.sap/origin")
	}
	tests["unrelated-pod-annotation"] = func(t *testing.T, _ Classified, c Classified) {
		ipamSet(t, ipamFind(c.Seed, "Deployment", "ipam-capi-controller-manager", ipamSeedNamespace), "wrong", "spec", "template", "metadata", "annotations", "unrelated")
	}
	for _, change := range []string{"broad-patch", "update", "list-restricted", "historical-rules", "wrong-name", "extra-rule"} {
		tests["RBAC/"+change] = func(_ *testing.T, g, c Classified) {
			obj := ipamFind(c.Shoot, "ClusterRole", "ipam-capi-remote-webhook-injector", "")
			rules := obj.Object["rules"].([]any)
			switch change {
			case "broad-patch":
				delete(rules[0].(map[string]any), "resourceNames")
			case "update":
				rules[0].(map[string]any)["verbs"] = []any{"get", "patch", "update"}
			case "list-restricted":
				rules[1].(map[string]any)["resourceNames"] = []any{ipamPoolNames[0]}
			case "historical-rules":
				ipamFind(g.Shoot, "ClusterRole", "ipam-capi-remote-webhook-injector", "").Object["rules"] = ipamTargetRules()
			case "wrong-name":
				rules[0].(map[string]any)["resourceNames"] = []any{"other.example.com"}
			case "extra-rule":
				obj.Object["rules"] = append(rules, map[string]any{"apiGroups": []any{"*"}, "resources": []any{"*"}, "verbs": []any{"*"}})
			}
		}
	}
	tests["shoot-binding"] = func(_ *testing.T, _ Classified, c Classified) {
		ipamFind(c.Shoot, "ClusterRoleBinding", "ipam-capi-remote-webhook-injector-rolebinding", "").Object["subjects"].([]any)[0].(map[string]any)["name"] = "wrong"
	}
	tests["monitor-other-namespace"] = func(t *testing.T, _ Classified, c Classified) {
		ipamSet(t, ipamFind(c.Seed, "ServiceMonitor", "ipam-capi-webhook-injector", ipamSeedNamespace), map[string]any{"matchNames": []any{"other"}}, "spec", "namespaceSelector")
	}
	tests["monitor-service-selector"] = func(t *testing.T, _ Classified, c Classified) {
		ipamSet(t, ipamFind(c.Seed, "ServiceMonitor", "ipam-capi-webhook-injector", ipamSeedNamespace), "wrong", "spec", "selector", "matchLabels", "app.kubernetes.io/name")
	}
	tests["seed-service-port"] = func(_ *testing.T, _ Classified, c Classified) {
		ipamFind(c.Seed, "Service", "ipam-capi-remote-webhook-service", ipamSeedNamespace).Object["spec"].(map[string]any)["ports"].([]any)[0].(map[string]any)["targetPort"] = int64(9444)
	}
	tests["shoot-metrics-port"] = func(_ *testing.T, _ Classified, c Classified) {
		ipamFind(c.Shoot, "Service", "ipam-capi-controller-manager-metrics-service", "kube-system").Object["spec"].(map[string]any)["ports"].([]any)[0].(map[string]any)["targetPort"] = "wrong"
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			g, c := ipamOfflineContractPair(t)
			mutate(t, g, c)
			if report := compareIPAMContract(g, c, ipamSeedNamespace); report.Equal() {
				t.Fatal("mutation escaped guarded comparison")
			}
		})
	}
}

// This offline POSITIVE models the approved proposal, not the current candidate
// pin. Only the online case can establish that the separately authored source
// satisfies it. The complete historical Helm render is independently captured.
func ipamOfflineContractPair(t *testing.T) (golden, candidate Classified) {
	t.Helper()
	data, err := os.ReadFile("../../testdata/fixtures/ipam-capi-remote/legacy-render.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256(data)); digest != "af953c4e6620feda1ef7885c5c0e73eafe58e2a8670280f8a9cfce7f2b2765f0" {
		t.Fatal("independent pinned legacy snapshot changed; review provenance, do not refresh to match candidate")
	}
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = z.Close() }()
	var docs []*unstructured.Unstructured
	if err := json.NewDecoder(z).Decode(&docs); err != nil {
		t.Fatal(err)
	}
	shoot, passthrough, err := UnwrapManagedResources(docs)
	if err != nil {
		t.Fatal(err)
	}
	golden, err = ClassifyGolden(passthrough, shoot, GoldenOpts{ChartFullname: "ipam-capi-remote"})
	if err != nil {
		t.Fatal(err)
	}
	golden.Seed = ipamReleaseNamespace(golden.Seed, ipamSeedNamespace)
	golden.Shoot = ipamReleaseNamespace(golden.Shoot, "kube-system")
	candidate = Classified{Seed: ipamCopy(golden.Seed), Shoot: ipamCopy(golden.Shoot)}
	for _, obj := range candidate.Seed {
		ipamSet(t, obj, "additions", "metadata", "annotations", manifest.OriginAnnotation)
		for label := range ipamProvenanceLabels(obj.GetKind(), obj.GetName()) {
			unstructured.RemoveNestedField(obj.Object, "metadata", "labels", label)
		}
	}
	for key := range ipamSixTargetKeys() {
		obj := candidate.Shoot[key]
		if obj == nil {
			t.Fatalf("historical target missing %s", key)
		}
		labels := obj.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[ipamTargetLabel] = "ipam-capi-remote"
		obj.SetLabels(labels)
	}
	for _, name := range []string{"clusters.cluster.x-k8s.io", "ipaddresses.ipam.cluster.x-k8s.io", "ipaddressclaims.ipam.cluster.x-k8s.io"} {
		ipamSet(t, ipamFind(candidate.Shoot, "CustomResourceDefinition", name, ""), ipamJSON(`{"strategy":"None"}`), "spec", "conversion")
	}
	for _, name := range ipamPoolNames {
		obj := ipamFind(candidate.Shoot, "CustomResourceDefinition", name, "")
		unstructured.RemoveNestedField(obj.Object, "spec", "conversion", "webhook", "clientConfig", "service")
		ipamSet(t, obj, ipamConversionURL, "spec", "conversion", "webhook", "clientConfig", "url")
	}
	deployment := ipamFind(candidate.Seed, "Deployment", "ipam-capi-controller-manager", ipamSeedNamespace)
	ipamSet(t, ipamInjector(deployment), ipamTargetArgs(), "args")
	ipamSet(t, deployment, "additions", "spec", "template", "metadata", "annotations", "dual-deployment-operator.cc.sap/origin")
	ipamSet(t, ipamFind(candidate.Shoot, "ClusterRole", "ipam-capi-remote-webhook-injector", ""), ipamTargetRules(), "rules")
	unstructured.RemoveNestedField(ipamFind(candidate.Seed, "ServiceMonitor", "ipam-capi-webhook-injector", ipamSeedNamespace).Object, "spec", "namespaceSelector")
	return golden, candidate
}

func ipamSet(t *testing.T, obj *unstructured.Unstructured, value any, path ...string) {
	t.Helper()
	if obj == nil {
		t.Fatalf("missing object for %v", path)
	}
	if err := unstructured.SetNestedField(obj.Object, value, path...); err != nil {
		t.Fatal(err)
	}
}

func TestIPAMSeedBindingPlacement(t *testing.T) {
	for _, ns := range []string{ipamSeedNamespace, "alternate-seed"} {
		t.Run(ns, func(t *testing.T) {
			golden, candidate := ipamOfflineContractPair(t)
			f, err := LoadFixture("../../testdata/fixtures/ipam-capi-remote")
			if err != nil {
				t.Fatal(err)
			}
			// Model the proposal's reusable source: subject deliberately has no namespace.
			binding := ipamFind(candidate.Seed, "ClusterRoleBinding", "ipam-capi-remote-webhook-injector", "")
			subject := binding.Object["subjects"].([]any)[0].(map[string]any)
			delete(subject, "namespace")
			found := false
			for i := range f.CR.Spec.Transformations {
				p := f.CR.Spec.Transformations[i].Patch
				if p == nil || p.Target.Kind != "ClusterRoleBinding" || p.Target.Name != "ipam-capi-remote-webhook-injector" {
					continue
				}
				if p.Target.Origin != "additions" {
					t.Fatal("required seed placement patch must target origin additions")
				}
				if len(p.JSONPatch) != 1 || p.JSONPatch[0].Op != "add" || p.JSONPatch[0].Path != "/subjects/0/namespace" {
					t.Fatal("required exact placement patch changed")
				}
				if p.JSONPatch[0].Value == nil || string(p.JSONPatch[0].Value.Raw) != `"`+ipamSeedNamespace+`"` {
					t.Fatal("primary namespace patch disagrees with verified contract")
				}
				p.JSONPatch[0].Value.Raw = []byte(`"` + ns + `"`)
				found = true
			}
			if !found {
				t.Fatal("required seed CRB jsonPatch missing")
			}
			transforms, err := transform.Build(f.CR.Spec.Transformations)
			if err != nil {
				t.Fatal(err)
			}
			wrongOrigin := []manifest.Manifest{{Unstructured: binding.DeepCopy(), Origin: manifest.OriginUpstream}}
			for _, tr := range transforms {
				wrongOrigin, err = tr.Apply(wrongOrigin)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, found := wrongOrigin[0].Unstructured.Object["subjects"].([]any)[0].(map[string]any)["namespace"]; found {
				t.Fatal("placement patch changed a non-additions resource")
			}
			ms := []manifest.Manifest{{Unstructured: binding, Origin: manifest.OriginAdditions}}
			for _, tr := range transforms {
				ms, err = tr.Apply(ms)
				if err != nil {
					t.Fatal(err)
				}
			}
			candidate.Seed[KeyOf(binding)] = ms[0].Unstructured
			// Model normal Helm/Kustomize namespace placement for the alternate scenario;
			// comparison itself must never rewrite subject namespaces.
			for _, set := range []ObjectSet{golden.Seed, candidate.Seed} {
				for key, obj := range set {
					if obj.GetNamespace() == ipamSeedNamespace {
						obj.SetNamespace(ns)
						delete(set, key)
						set[KeyOf(obj)] = obj
					}
				}
			}
			ipamSet(t, ipamFind(golden.Seed, "ClusterRoleBinding", "ipam-capi-remote-webhook-injector", ""), []any{map[string]any{"kind": "ServiceAccount", "name": "ipam-capi-remote-webhook-injector", "namespace": ns}}, "subjects")
			ipamSet(t, ipamFind(golden.Seed, "ServiceMonitor", "ipam-capi-webhook-injector", ns), map[string]any{"matchNames": []any{ns}}, "spec", "namespaceSelector")
			if report := compareIPAMContract(golden, candidate, ns); !report.Equal() {
				t.Fatal(report.String())
			}
			ipamFind(candidate.Seed, "ClusterRoleBinding", "ipam-capi-remote-webhook-injector", "").Object["subjects"].([]any)[0].(map[string]any)["namespace"] = "wrong-seed"
			if compareIPAMContract(golden, candidate, ns).Equal() {
				t.Fatal("wrong binding subject normalized away")
			}
		})
	}
}
