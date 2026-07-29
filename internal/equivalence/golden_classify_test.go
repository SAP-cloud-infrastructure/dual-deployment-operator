// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func obj(apiVersion, kind, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(apiVersion)
	u.SetKind(kind)
	u.SetName(name)
	return u
}

func TestClassifyUnwrapsInjectorConfigMapOnly(t *testing.T) {
	injector := obj("v1", "ConfigMap", "metal-operator-remote-webhook-config")
	_ = unstructured.SetNestedField(injector.Object,
		"apiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingWebhookConfiguration\nmetadata:\n  name: vwc\n",
		"data", "webhooks.yaml")
	appCM := obj("v1", "ConfigMap", "dns-record-template") // non-injector, must pass through

	docs := []*unstructured.Unstructured{injector, appCM}
	res, err := ClassifyGolden(docs, GoldenOpts{ChartFullname: "metal-operator-remote"})
	if err != nil {
		t.Fatalf("ClassifyGolden: %v", err)
	}

	if _, ok := res.Shoot[ResourceKey("admissionregistration.k8s.io/ValidatingWebhookConfiguration//vwc")]; !ok {
		t.Error("injector ConfigMap must be unwrapped into bare VWC in shoot set")
	}
	if _, ok := res.Shoot[KeyOf(injector)]; ok {
		t.Error("injector ConfigMap wrapper must be discarded")
	}
	if _, ok := res.Seed[KeyOf(appCM)]; !ok {
		t.Error("non-injector ConfigMap must pass through to keep+compare (seed)")
	}
}

func TestClassifyNoInjectorConfigMapIsNoOp(t *testing.T) {
	// A ConfigMap that is NOT the injector one (wrong sole-key) must be untouched.
	appCM := obj("v1", "ConfigMap", "boot-operator-remote-webhook-config-lookalike")
	res, err := ClassifyGolden([]*unstructured.Unstructured{appCM}, GoldenOpts{ChartFullname: "boot-operator-remote"})
	if err != nil {
		t.Fatalf("ClassifyGolden: %v", err)
	}
	if _, ok := res.Seed[KeyOf(appCM)]; !ok {
		t.Error("lookalike ConfigMap must pass through unchanged")
	}
}

func TestClassifyExcludesByKindAndName(t *testing.T) {
	owner := obj("v1", "ConfigMap", "owner-info")
	res, err := ClassifyGolden([]*unstructured.Unstructured{owner}, GoldenOpts{
		ChartFullname: "metal-operator-remote",
		Exclusions:    []ExclusionEntry{{Kind: "ConfigMap", Name: "owner-info"}},
	})
	if err != nil {
		t.Fatalf("ClassifyGolden: %v", err)
	}
	if len(res.Seed) != 0 || len(res.Shoot) != 0 {
		t.Error("excluded object must not appear in either set")
	}
}

func TestClassifyInjectorConfigMapLeadingSeparatorAndCRLF(t *testing.T) {
	injector := obj("v1", "ConfigMap", "metal-operator-remote-webhook-config")
	// Leading "---", CRLF endings, and two docs — the brittle splitter would drop the first.
	payload := "---\r\napiVersion: admissionregistration.k8s.io/v1\r\nkind: ValidatingWebhookConfiguration\r\nmetadata:\r\n  name: vwc1\r\n---\r\napiVersion: admissionregistration.k8s.io/v1\r\nkind: MutatingWebhookConfiguration\r\nmetadata:\r\n  name: mwc1\r\n"
	_ = unstructured.SetNestedField(injector.Object, payload, "data", "webhooks.yaml")

	res, err := ClassifyGolden([]*unstructured.Unstructured{injector}, GoldenOpts{ChartFullname: "metal-operator-remote"})
	if err != nil {
		t.Fatalf("ClassifyGolden: %v", err)
	}
	if _, ok := res.Shoot[ResourceKey("admissionregistration.k8s.io/ValidatingWebhookConfiguration//vwc1")]; !ok {
		t.Error("first webhook doc (after leading ---, CRLF) must be decoded")
	}
	if _, ok := res.Shoot[ResourceKey("admissionregistration.k8s.io/MutatingWebhookConfiguration//mwc1")]; !ok {
		t.Error("second webhook doc must be decoded")
	}
}
