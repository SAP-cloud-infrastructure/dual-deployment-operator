// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"fmt"

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
		var err error
		switch cp.Unstructured.GetKind() {
		case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
			err = r.rewriteWebhookList(cp.Unstructured)
		case "CustomResourceDefinition":
			err = r.rewriteConversion(cp.Unstructured)
		}
		if err != nil {
			return nil, fmt.Errorf("rewriteWebhookURL %s/%s: %w", cp.Unstructured.GetKind(), cp.Unstructured.GetName(), err)
		}
		out[i] = cp
	}
	return out, nil
}

func (r *rewriteWebhookURL) rewriteWebhookList(u *unstructured.Unstructured) error {
	whs, found, err := unstructured.NestedSlice(u.Object, "webhooks")
	if err != nil || !found {
		return err
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
		return unstructured.SetNestedSlice(u.Object, whs, "webhooks")
	}
	return nil
}

func (r *rewriteWebhookURL) rewriteConversion(u *unstructured.Unstructured) error {
	strategy, _, err := unstructured.NestedString(u.Object, "spec", "conversion", "strategy")
	if err != nil {
		return err
	}
	if strategy != "Webhook" {
		return nil
	}
	cc, found, err := unstructured.NestedMap(u.Object, "spec", "conversion", "webhook", "clientConfig")
	if err != nil || !found {
		return err
	}
	if r.rewriteClientConfig(cc) {
		return unstructured.SetNestedMap(u.Object, cc, "spec", "conversion", "webhook", "clientConfig")
	}
	return nil
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
