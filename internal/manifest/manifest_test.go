// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOriginConstantValues(t *testing.T) {
	if OriginUpstream != "upstream" {
		t.Errorf("OriginUpstream = %q, want %q", OriginUpstream, "upstream")
	}
	if OriginAdditions != "additions" {
		t.Errorf("OriginAdditions = %q, want %q", OriginAdditions, "additions")
	}
	if OriginAnnotation != "dual-deployment-operator.cc.sap/origin" {
		t.Errorf("OriginAnnotation = %q, want %q", OriginAnnotation, "dual-deployment-operator.cc.sap/origin")
	}
}

func TestManifestExposesObjectAndOrigin(t *testing.T) {
	u := &unstructured.Unstructured{}
	u.SetKind("ConfigMap")
	u.SetName("demo")
	m := Manifest{Unstructured: u, Origin: OriginAdditions}

	if m.Unstructured.GetKind() != "ConfigMap" {
		t.Errorf("kind = %q, want ConfigMap", m.Unstructured.GetKind())
	}
	if m.Origin != OriginAdditions {
		t.Errorf("origin = %q, want additions", m.Origin)
	}
}
