// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package deliver

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestStripCABundleFromWebhooksLeafOnly(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"kind": "ValidatingWebhookConfiguration",
		"webhooks": []any{map[string]any{
			"clientConfig": map[string]any{"url": "https://x/y", "caBundle": "AAAA"},
		}},
	}}
	stripCABundleFromWebhooks(u)
	wh := u.Object["webhooks"].([]any)[0].(map[string]any)
	cc := wh["clientConfig"].(map[string]any)
	if _, present := cc["caBundle"]; present {
		t.Error("caBundle leaf should be removed")
	}
	if cc["url"] != "https://x/y" {
		t.Error("clientConfig.url must be preserved")
	}
}

func TestStripCABundleUnconditionalNoOp(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"kind":     "ValidatingWebhookConfiguration",
		"webhooks": []any{map[string]any{"clientConfig": map[string]any{"url": "https://x/y"}}},
	}}
	stripCABundleFromWebhooks(u) // must not panic, no-op
	wh := u.Object["webhooks"].([]any)[0].(map[string]any)
	if wh["clientConfig"].(map[string]any)["url"] != "https://x/y" {
		t.Error("no-op strip damaged the object")
	}
}

func TestStripCABundleFromCRDConversionLeafOnly(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"kind": "CustomResourceDefinition",
		"spec": map[string]any{
			"conversion": map[string]any{
				"strategy": "Webhook",
				"webhook": map[string]any{
					"clientConfig": map[string]any{
						"service":  map[string]any{"name": "svc", "namespace": "ns", "path": "/convert"},
						"caBundle": "AAAA",
					},
				},
			},
		},
	}}
	stripCABundleFromCRDConversion(u)

	cc, _, err := unstructured.NestedMap(u.Object, "spec", "conversion", "webhook", "clientConfig")
	if err != nil {
		t.Fatalf("NestedMap clientConfig: %v", err)
	}
	if _, present := cc["caBundle"]; present {
		t.Error("conversion caBundle leaf should be removed")
	}
	if _, present := cc["service"]; !present {
		t.Error("conversion clientConfig.service must be preserved")
	}
	strategy, _, err := unstructured.NestedString(u.Object, "spec", "conversion", "strategy")
	if err != nil {
		t.Fatalf("NestedString strategy: %v", err)
	}
	if strategy != "Webhook" {
		t.Error("conversion.strategy must be preserved")
	}
}

func TestPrepareForApplyDispatchesByKind(t *testing.T) {
	crd := &unstructured.Unstructured{Object: map[string]any{
		"kind": "CustomResourceDefinition",
		"spec": map[string]any{
			"conversion": map[string]any{
				"webhook": map[string]any{"clientConfig": map[string]any{"caBundle": "BBBB"}},
			},
		},
	}}
	prepareForApply(crd)
	_, present, err := unstructured.NestedString(crd.Object, "spec", "conversion", "webhook", "clientConfig", "caBundle")
	if err != nil {
		t.Fatalf("NestedString caBundle: %v", err)
	}
	if present {
		t.Error("prepareForApply must strip conversion caBundle on a CRD")
	}
}
