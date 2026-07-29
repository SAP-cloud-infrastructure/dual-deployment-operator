package equivalence

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestStripAllowlistRemovesProvenanceAndInternalLabels(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{
			"name": "x",
			"labels": map[string]any{
				"helm.sh/chart":                                    "metal-0.6.30",
				"app.kubernetes.io/managed-by":                     "Helm",
				"dual-deployment-operator.cc.sap/owned-by":         "abc",
				"dual-deployment-operator.cc.sap/webhook-injector": "metal-operator", // transformation output — MUST survive
				"app": "keep-me",
			},
		},
	}}
	StripAllowlist(u)
	labels, _, _ := unstructured.NestedStringMap(u.Object, "metadata", "labels")
	if _, ok := labels["helm.sh/chart"]; ok {
		t.Error("helm.sh/chart should be stripped")
	}
	if _, ok := labels["dual-deployment-operator.cc.sap/owned-by"]; ok {
		t.Error("owned-by should be stripped")
	}
	if labels["dual-deployment-operator.cc.sap/webhook-injector"] != "metal-operator" {
		t.Error("injector target-label is a transformation output and MUST NOT be stripped")
	}
	if labels["app"] != "keep-me" {
		t.Error("unrelated label must survive")
	}
}
