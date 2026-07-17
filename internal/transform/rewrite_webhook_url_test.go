// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"testing"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

const prefix = "https://metal-operator-remote-webhook-service:443"

func TestRewriteWebhookURL_VWC(t *testing.T) {
	vwc := mustManifest(t, `
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: v
webhooks:
  - name: a.kb.io
    clientConfig:
      service:
        name: webhook-service
        namespace: kube-system
        path: /validate
      caBundle: QUJD
`, manifest.OriginUpstream)

	rt := &rewriteWebhookURL{spec: &v1alpha1.RewriteWebhookURLSpec{URLPrefix: prefix}}
	got, err := rt.Apply([]manifest.Manifest{vwc})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	whs := nestedSlice(t, got[0].Unstructured.Object, "webhooks")
	cc := whs[0].(map[string]any)["clientConfig"].(map[string]any)
	if cc["url"] != prefix+"/validate" {
		t.Fatalf("url = %v, want %v", cc["url"], prefix+"/validate")
	}
	if _, hasService := cc["service"]; hasService {
		t.Fatalf("service should be removed")
	}
	if cc["caBundle"] != "QUJD" {
		t.Fatalf("caBundle must be preserved, got %v", cc["caBundle"])
	}
}

func TestRewriteWebhookURL_ConversionCRD(t *testing.T) {
	crd := mustManifest(t, `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: c
spec:
  conversion:
    strategy: Webhook
    webhook:
      clientConfig:
        service:
          name: webhook-service
          namespace: kube-system
          path: /convert
        caBundle: WFla
`, manifest.OriginUpstream)

	rt := &rewriteWebhookURL{spec: &v1alpha1.RewriteWebhookURLSpec{URLPrefix: prefix}}
	got, err := rt.Apply([]manifest.Manifest{crd})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	cc, _ := nestedMap(t, got[0].Unstructured.Object, "spec", "conversion", "webhook", "clientConfig")
	if cc["url"] != prefix+"/convert" {
		t.Fatalf("crd url = %v, want %v", cc["url"], prefix+"/convert")
	}
	if cc["caBundle"] != "WFla" {
		t.Fatalf("crd caBundle must be preserved")
	}
}

func TestRewriteWebhookURL_NonWebhookCRDUntouched(t *testing.T) {
	crd := mustManifest(t, `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: plain
spec:
  group: g
`, manifest.OriginUpstream)
	rt := &rewriteWebhookURL{spec: &v1alpha1.RewriteWebhookURLSpec{URLPrefix: prefix}}
	got, err := rt.Apply([]manifest.Manifest{crd})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if _, found := nestedMap(t, got[0].Unstructured.Object, "spec", "conversion"); found {
		t.Fatalf("plain CRD must be untouched")
	}
}

func TestRewriteWebhookURL_ExistingURLLeftAlone_AndZeroMatchNoOp(t *testing.T) {
	already := mustManifest(t, `
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: m
webhooks:
  - name: a.kb.io
    clientConfig:
      url: https://existing:443/x
`, manifest.OriginUpstream)
	dep := mustManifest(t, "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d", manifest.OriginUpstream)

	rt := &rewriteWebhookURL{spec: &v1alpha1.RewriteWebhookURLSpec{URLPrefix: prefix}}
	got, err := rt.Apply([]manifest.Manifest{already, dep})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	whs := nestedSlice(t, got[0].Unstructured.Object, "webhooks")
	cc := whs[0].(map[string]any)["clientConfig"].(map[string]any)
	if cc["url"] != "https://existing:443/x" {
		t.Fatalf("existing url must be left alone, got %v", cc["url"])
	}
	if got[1].Unstructured.GetKind() != "Deployment" {
		t.Fatalf("non-webhook manifest must pass through")
	}
}
