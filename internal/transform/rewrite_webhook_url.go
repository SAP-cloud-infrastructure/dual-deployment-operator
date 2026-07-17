// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type rewriteWebhookURL struct {
	spec *v1alpha1.RewriteWebhookURLSpec
}

func (r *rewriteWebhookURL) Type() string { return "rewriteWebhookURL" }

// Apply rewrites service-based webhook clientConfig to url-based on
// Validating/MutatingWebhookConfiguration (.webhooks[].clientConfig) and on
// conversion-webhook CRDs (.spec.conversion.webhook.clientConfig). Preserves
// caBundle, leaves an existing url alone, no-ops other kinds. Never mutates
// input in place (works on a deep copy).
func (r *rewriteWebhookURL) Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error) {
	out := make([]manifest.Manifest, len(manifests))
	for i, m := range manifests {
		cp := manifest.Manifest{Unstructured: m.Unstructured.DeepCopy(), Origin: m.Origin}
		switch cp.Unstructured.GetKind() {
		case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
			r.rewriteWebhookList(cp.Unstructured)
		case "CustomResourceDefinition":
			r.rewriteConversion(cp.Unstructured)
		}
		out[i] = cp
	}
	return out, nil
}

func (r *rewriteWebhookURL) rewriteWebhookList(u *unstructured.Unstructured) {
	whs, found, err := unstructured.NestedSlice(u.Object, "webhooks")
	if err != nil || !found {
		return
	}
	changed := false
	for i := range whs {
		wh, ok := whs[i].(map[string]any)
		if !ok {
			continue
		}
		cc, ok := wh["clientConfig"].(map[string]any)
		if !ok {
			continue
		}
		if r.rewriteClientConfig(cc) {
			wh["clientConfig"] = cc
			whs[i] = wh
			changed = true
		}
	}
	if changed {
		_ = unstructured.SetNestedSlice(u.Object, whs, "webhooks")
	}
}

func (r *rewriteWebhookURL) rewriteConversion(u *unstructured.Unstructured) {
	strategy, _, _ := unstructured.NestedString(u.Object, "spec", "conversion", "strategy")
	if strategy != "Webhook" {
		return
	}
	cc, found, err := unstructured.NestedMap(u.Object, "spec", "conversion", "webhook", "clientConfig")
	if err != nil || !found {
		return
	}
	if r.rewriteClientConfig(cc) {
		_ = unstructured.SetNestedMap(u.Object, cc, "spec", "conversion", "webhook", "clientConfig")
	}
}

// rewriteClientConfig replaces a service-based clientConfig with url-based,
// preserving caBundle. Returns true if it changed cc. Leaves an existing url alone.
func (r *rewriteWebhookURL) rewriteClientConfig(cc map[string]any) bool {
	if _, hasURL := cc["url"]; hasURL {
		return false
	}
	svc, ok := cc["service"].(map[string]any)
	if !ok {
		return false
	}
	pathStr, _ := svc["path"].(string)
	cc["url"] = r.spec.URLPrefix + pathStr
	delete(cc, "service")
	return true
}
