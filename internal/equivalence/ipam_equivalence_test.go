// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/transform"
)

func TestIPAMFixture(t *testing.T) {
	f, err := LoadFixture("../../testdata/fixtures/ipam-capi-remote")
	if err != nil {
		t.Fatal(err)
	}
	if f.SHA != "97d52c25ae067c00fd5f5b3e7a66771fdcbc10db" || f.CR.Spec.Source.Kustomize == nil {
		t.Fatal("expected independently pinned legacy Helm and candidate Kustomize inputs")
	}
	if f.CR.Namespace != ipamSeedNamespace || f.CR.Spec.ShootNamespace != "kube-system" {
		t.Fatal("fixture placement differs from the verified legacy/candidate release contract")
	}
	if f.CR.Spec.Source.Kustomize.URL != "https://github.com/sapcc/helm-charts//"+ipamCandidatePath+"?ref="+ipamCandidateSHA {
		t.Fatal("fixture must use the exact reviewed published candidate commit")
	}
}

func TestIPAMFixtureWebhookLabels(t *testing.T) {
	f, err := LoadFixture("../../testdata/fixtures/ipam-capi-remote")
	if err != nil {
		t.Fatal(err)
	}
	transforms, err := transform.Build(f.CR.Spec.Transformations)
	if err != nil {
		t.Fatal(err)
	}
	seed, shoot := ipamWebhookContractObjects()
	for key, obj := range shoot {
		obj.SetLabels(nil) // the pinned candidate's admission manifests are unlabelled
		ms := []manifest.Manifest{{Unstructured: obj}}
		for _, transformation := range transforms {
			ms, err = transformation.Apply(ms)
			if err != nil {
				t.Fatal(err)
			}
		}
		shoot[key] = ms[0].Unstructured
	}
	if err := ipamCheckWebhookTargets(seed, shoot); err != nil {
		t.Fatal(err)
	}
}

func TestIPAMWebhookTargetContract(t *testing.T) {
	seed, shoot := ipamWebhookContractObjects()
	if err := ipamCheckWebhookTargets(seed, shoot); err != nil {
		t.Fatal(err)
	}
	for key := range shoot {
		for _, value := range []string{"", "wrong-target"} {
			t.Run(string(key)+"/label="+value, func(t *testing.T) {
				seed, shoot := ipamWebhookContractObjects()
				shoot[key].SetLabels(map[string]string{"dual-deployment-operator.cc.sap/webhook-injector": value})
				if value == "" {
					shoot[key].SetLabels(nil)
				}
				if err := ipamCheckWebhookTargets(seed, shoot); err == nil {
					t.Fatal("missing/wrong webhook label escaped selector assertion")
				}
			})
		}
		t.Run(string(key)+"/missing", func(t *testing.T) {
			seed, shoot := ipamWebhookContractObjects()
			delete(shoot, key)
			if err := ipamCheckWebhookTargets(seed, shoot); err == nil {
				t.Fatal("missing webhook escaped selector assertion")
			}
		})
	}
	for _, arg := range []string{"--target-label=wrong=value", "--unrelated=true"} {
		seed, shoot := ipamWebhookContractObjects()
		for _, deployment := range seed {
			pod := deployment.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			pod["initContainers"].([]any)[0].(map[string]any)["args"] = []any{arg}
		}
		if err := ipamCheckWebhookTargets(seed, shoot); err == nil {
			t.Fatalf("wrong/missing injector selector escaped assertion: %s", arg)
		}
	}
}

// Small offline contract inputs exercise fixture configuration, not parity.
// The same assertion runs on the actual source/transform capture below.
func ipamWebhookContractObjects() (seed, shoot ObjectSet) {
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "ipam-capi-controller-manager"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"initContainers": []any{
			map[string]any{"name": "webhook-injector", "args": []any{"--target-label=dual-deployment-operator.cc.sap/webhook-injector=ipam-capi-remote"}},
		}}}},
	}}
	shoot = ObjectSet{}
	for kind, name := range map[string]string{
		"MutatingWebhookConfiguration":   "ipam-capi-remote-mutating-webhook-configuration",
		"ValidatingWebhookConfiguration": "ipam-capi-remote-validating-webhook-configuration",
	} {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "admissionregistration.k8s.io/v1", "kind": kind, "metadata": map[string]any{"name": name},
		}}
		obj.SetLabels(map[string]string{"dual-deployment-operator.cc.sap/webhook-injector": "ipam-capi-remote"})
		shoot[KeyOf(obj)] = obj
	}
	return ObjectSet{KeyOf(deployment): deployment}, shoot
}

// This is a value-checked configuration assertion, NOT a parity exception.
// Both exact admission identities must match the actual injector selector.
func ipamCheckWebhookTargets(seed, shoot ObjectSet) error {
	const selector = "--target-label=dual-deployment-operator.cc.sap/webhook-injector=ipam-capi-remote"
	selectors := 0
	for _, obj := range seed {
		if obj.GetAPIVersion() != "apps/v1" || obj.GetKind() != "Deployment" || obj.GetName() != "ipam-capi-controller-manager" {
			continue
		}
		containers, _, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "initContainers")
		if err != nil {
			return err
		}
		for _, raw := range containers {
			container, ok := raw.(map[string]any)
			if !ok || container["name"] != "webhook-injector" {
				continue
			}
			args, _, err := unstructured.NestedStringSlice(container, "args")
			if err != nil {
				return err
			}
			for _, arg := range args {
				if strings.HasPrefix(arg, "--target-label=") {
					if arg != selector {
						return fmt.Errorf("injector selector differs from required %s", selector)
					}
					selectors++
				}
			}
		}
	}
	if selectors != 1 {
		return fmt.Errorf("expected exactly one injector %s, got %d", selector, selectors)
	}
	for kind, name := range map[string]string{
		"MutatingWebhookConfiguration":   "ipam-capi-remote-mutating-webhook-configuration",
		"ValidatingWebhookConfiguration": "ipam-capi-remote-validating-webhook-configuration",
	} {
		key := ResourceKey("admissionregistration.k8s.io/" + kind + "//" + name)
		obj := shoot[key]
		if obj == nil || obj.GetAPIVersion() != "admissionregistration.k8s.io/v1" || obj.GetLabels()["dual-deployment-operator.cc.sap/webhook-injector"] != "ipam-capi-remote" {
			return fmt.Errorf("%s must exist and match injector target label exactly", key)
		}
	}
	return nil
}

func TestIPAMInventoryNotVacuous(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "expected"},
	}}
	set := ObjectSet{KeyOf(u): u}
	for _, tc := range []struct {
		name      string
		set       ObjectSet
		want      []string
		wantError bool
	}{
		{"exact", set, []string{"v1/ConfigMap//expected"}, false},
		{"both empty", nil, nil, true},
		{"missing", nil, []string{"v1/ConfigMap//expected"}, true},
		{"extra", set, nil, true},
		{"wrong identity same count", set, []string{"v1/ConfigMap//wrong"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ipamCheckInventory(tc.set, tc.want); (err != nil) != tc.wantError {
				t.Fatalf("inventory error=%v, wantError=%v", err, tc.wantError)
			}
		})
	}
}

// This public-source case is deliberately separate from TestEquivalence: it
// needs network, but no internal Helm registry and no Kubernetes cluster.
// Do not parallelize: the local TLS Git server temporarily replaces go-git's
// process-wide HTTPS transport, restoring it at test cleanup.
func TestEquivalenceIPAMCapiRemote(t *testing.T) {
	if os.Getenv("RUN_EQUIVALENCE") == "" {
		t.Skip("set RUN_EQUIVALENCE=1 for public Git/HTTPS source acquisition")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	f, err := LoadFixture("../../testdata/fixtures/ipam-capi-remote")
	if err != nil {
		t.Fatal(err)
	}
	docs, err := RenderGolden(ctx, GoldenRenderReq{
		RepoURL: f.RepoURL, SHA: f.SHA, ChartPath: f.ChartPath,
		ValuesYAML: [][]byte{f.OverlayRaw}, Namespace: f.CR.Namespace,
		APIVersions: []string{"monitoring.coreos.com/v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	shoot, passthrough, err := UnwrapManagedResources(docs)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := ClassifyGolden(passthrough, shoot, GoldenOpts{ChartFullname: f.ChartFullname})
	if err != nil {
		t.Fatal(err)
	}
	// Helm omits namespace on release-scoped resources. Model its placement,
	// not a canonical namespace rewrite: explicit namespaces remain unchanged.
	golden.Seed = ipamReleaseNamespace(golden.Seed, f.CR.Namespace)
	golden.Shoot = ipamReleaseNamespace(golden.Shoot, f.CR.Spec.ShootNamespace)
	f.CR.Spec.Source.Kustomize.URL = stageIPAMCandidate(t, ctx, f.CR.Spec.Source.Kustomize.URL)
	seed, shootSet, err := CaptureOperator(ctx, f.CR, source.Deps{RootResolver: source.NewGitResolver(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("webhook-target-contract", func(t *testing.T) {
		if err := ipamCheckSixTargets(seed, shootSet); err != nil {
			t.Fatal(err)
		}
		for key, obj := range shootSet {
			if obj.GetKind() != "MutatingWebhookConfiguration" && obj.GetKind() != "ValidatingWebhookConfiguration" {
				continue
			}
			for _, value := range []string{"", "wrong-target"} {
				t.Run(obj.GetName()+"/label="+value, func(t *testing.T) {
					mutant := maps.Clone(shootSet)
					mutant[key] = obj.DeepCopy()
					labels := mutant[key].GetLabels()
					labels["dual-deployment-operator.cc.sap/webhook-injector"] = value
					if value == "" {
						delete(labels, "dual-deployment-operator.cc.sap/webhook-injector")
					}
					mutant[key].SetLabels(labels)
					if err := ipamCheckWebhookTargets(seed, mutant); err == nil {
						t.Fatal("actual capture's missing/wrong target label escaped assertion")
					}
				})
			}
		}
	})
	for name, pair := range map[string][2]ObjectSet{
		"seed": {golden.Seed, seed}, "shoot": {golden.Shoot, shootSet},
	} {
		for i, side := range []string{"golden", "candidate"} {
			t.Logf("%s %s inventory (%d):\n%s", name, side, len(pair[i]), strings.Join(ipamInventory(pair[i]), "\n"))
			want := ipamExpectedInventory(t, f.Dir, name)
			if err := ipamCheckInventory(pair[i], want); err != nil {
				t.Errorf("%s %s: %v", name, side, err)
			}
		}
		t.Run(name+"/sensitivity", func(t *testing.T) { ipamMutationChecks(t, pair[1]) })
		if raw := Compare(ipamCopy(pair[0]), ipamCopy(pair[1])); !raw.Equal() {
			t.Logf("raw %s differences before guarded migration comparison:\n%s", name, raw.String())
		}
	}
	if report := compareIPAMContract(golden, Classified{Seed: seed, Shoot: shootSet}, f.CR.Namespace); !report.Equal() {
		t.Errorf("guarded migration mismatch:\n%s", report.String())
	}
}

// Exact identities, not merely counts or overlap, guard both sides separately.
func ipamCheckInventory(set ObjectSet, want []string) error {
	got := ipamInventory(set)
	if len(got) == 0 || len(want) == 0 || !slices.Equal(got, want) {
		return fmt.Errorf("inventory mismatch: got %v, want nonempty %v", got, want)
	}
	return nil
}

func ipamExpectedInventory(t *testing.T, dir, mode string) []string {
	t.Helper()
	inventories, err := ipamLoadInventory(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(inventories[mode]) // same exact inventory for BOTH sides
	sort.Strings(want)
	return want
}

func ipamLoadInventory(dir string) (map[string][]string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "inventory.yaml"))
	if err != nil {
		return nil, err
	}
	var inventories map[string][]string
	if err := yaml.Unmarshal(b, &inventories); err != nil {
		return nil, err
	}
	return inventories, nil
}

// Every actual resource is mutation-tested through the same full-object
// comparison used above. No synthetic schemas or candidate fingerprints.
func ipamMutationChecks(t *testing.T, set ObjectSet) {
	t.Helper()
	for _, original := range set {
		t.Run(original.GetKind()+"/"+original.GetName(), func(t *testing.T) {
			key := KeyOf(original)
			baseline := ObjectSet{key: original.DeepCopy()}
			mutant := ObjectSet{key: original.DeepCopy()}
			if report := Compare(baseline, mutant); !report.Equal() {
				t.Fatalf("unmodified resource differed: %s", report.String())
			}
			obj := mutant[key].Object
			var expectedPath string
			switch original.GetKind() {
			case "CustomResourceDefinition":
				version := obj["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)
				version["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)["type"] = "equivalence-mutation"
				expectedPath = "spec.versions[0].schema.openAPIV3Schema.type"
			case "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration":
				obj["webhooks"].([]any)[0].(map[string]any)["failurePolicy"] = "equivalence-mutation"
				expectedPath = "webhooks[0].failurePolicy"
			case "Role", "ClusterRole":
				obj["rules"].([]any)[0].(map[string]any)["verbs"] = []any{"equivalence-mutation"}
				expectedPath = "rules[0].verbs"
			case "RoleBinding", "ClusterRoleBinding":
				obj["roleRef"].(map[string]any)["name"] = "equivalence-mutation"
				expectedPath = "roleRef.name"
			case "Deployment":
				pod := obj["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
				pod["containers"].([]any)[0].(map[string]any)["image"] = "equivalence.invalid/mutated:v1"
				expectedPath = "spec.template.spec.containers[0].image"
			default:
				// Other fields are still compared; a non-allowlisted metadata label
				// is an observable mutation even for token-requestor Secret stubs.
				mutant[key].SetLabels(map[string]string{"equivalence-mutation": "true"})
				expectedPath = "metadata.labels"
			}
			report := Compare(baseline, mutant)
			if report.Equal() || len(report.Mismatched) != 1 || !strings.Contains(report.String(), expectedPath) {
				t.Fatalf("mutation at %s escaped comparison: %s", expectedPath, report.String())
			}
			t.Logf("detected %s mutation", expectedPath)
			if report := Compare(baseline, ObjectSet{}); len(report.MissingOp) != 1 {
				t.Fatal("resource removal escaped comparison")
			}
			if err := ipamCheckInventory(ObjectSet{}, ipamInventory(baseline)); err == nil {
				t.Fatal("resource removal escaped inventory check")
			}
		})
	}
}

func ipamReleaseNamespace(set ObjectSet, ns string) ObjectSet {
	out := ObjectSet{}
	for _, u := range set {
		manifest.ApplyNamespace([]manifest.Manifest{{Unstructured: u}}, ns)
		out[KeyOf(u)] = u
	}
	return out
}

func ipamInventory(set ObjectSet) []string {
	keys := make([]string, 0, len(set))
	for _, u := range set {
		keys = append(keys, displayKey(u))
	}
	sort.Strings(keys)
	return keys
}
