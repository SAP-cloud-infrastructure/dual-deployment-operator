// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package deliver

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// prepareForApply strips the caBundle leaf from webhook configs / conversion CRDs.
func prepareForApply(u *unstructured.Unstructured) {
	switch u.GetKind() {
	case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
		stripCABundleFromWebhooks(u)
	case "CustomResourceDefinition":
		stripCABundleFromCRDConversion(u)
	}
}

// stripCABundleFromWebhooks removes ONLY webhooks[].clientConfig.caBundle, on every
// entry, unconditionally. Never removes the parent clientConfig or the webhook entry.
func stripCABundleFromWebhooks(u *unstructured.Unstructured) {
	whs, found, err := unstructured.NestedSlice(u.Object, "webhooks")
	if err != nil || !found {
		return
	}
	for i := range whs {
		wh, ok := whs[i].(map[string]any)
		if !ok {
			continue
		}
		unstructured.RemoveNestedField(wh, "clientConfig", "caBundle")
		whs[i] = wh
	}
	if err := unstructured.SetNestedSlice(u.Object, whs, "webhooks"); err != nil {
		return
	}
}

// stripCABundleFromCRDConversion removes only spec.conversion.webhook.clientConfig.caBundle.
func stripCABundleFromCRDConversion(u *unstructured.Unstructured) {
	unstructured.RemoveNestedField(u.Object, "spec", "conversion", "webhook", "clientConfig", "caBundle")
}
