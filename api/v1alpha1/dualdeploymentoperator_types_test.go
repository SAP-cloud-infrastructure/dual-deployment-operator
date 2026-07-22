// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"encoding/json"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestHelmSourceRoundTrip(t *testing.T) {
	in := Source{Helm: &HelmSource{Repo: "oci://x", Name: "y", Version: "1.0.0"}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); got != `{"helm":{"repo":"oci://x","name":"y","version":"1.0.0"}}` {
		t.Fatalf("unexpected JSON: %s", got)
	}
	var out Source
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Helm == nil || out.Helm.Repo != "oci://x" || out.Kustomize != nil {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestKustomizeSourceRoundTrip(t *testing.T) {
	in := Source{Kustomize: &KustomizeSource{URL: "https://github.com/x/y//p?ref=v1", HostPath: "host", RemotePath: "remote"}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); got != `{"kustomize":{"url":"https://github.com/x/y//p?ref=v1","hostPath":"host","remotePath":"remote"}}` {
		t.Fatalf("unexpected JSON: %s", got)
	}
	var out Source
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Kustomize == nil || out.Kustomize.HostPath != "host" {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestTransformationVariantsRoundTrip(t *testing.T) {
	raw := apiextensionsv1.JSON{Raw: []byte(`{"metadata":{"labels":{"a":"b"}}}`)}
	cases := []Transformation{
		{Patch: &PatchSpec{Target: Selector{Kind: "Deployment"}, StrategicMerge: &raw}},
		{Patch: &PatchSpec{Target: Selector{Kind: "Deployment"}, JSONPatch: []JSONPatchOp{{Op: "add", Path: "/metadata/labels/a", Value: &apiextensionsv1.JSON{Raw: []byte(`"b"`)}}}}},
		{RewriteWebhookURL: &RewriteWebhookURLSpec{URLPrefix: "https://x:443"}},
		{FilterKinds: &FilterKindsSpec{Kinds: []string{"Service"}}},
	}
	for i, c := range cases {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("case %d marshal: %v", i, err)
		}
		var out Transformation
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("case %d unmarshal: %v", i, err)
		}
	}
}

func TestHealthStateConstants(t *testing.T) {
	for _, s := range []HealthState{HealthHealthy, HealthProgressing, HealthDegraded, HealthUnknown} {
		if s == "" {
			t.Fatalf("empty HealthState constant")
		}
	}
}

func TestSpecFieldsRoundTrip(t *testing.T) {
	s := DualDeploymentOperatorSpec{
		RemoteAccess:    RemoteAccessRef{SecretName: "kc", Server: "https://api.example:443"},
		RemoteNamespace: "shoot--x--y",
		RetentionPolicy: RetentionPolicy{CRDs: "Retain"},
		ApplyOrder:      "HostFirst",
	}
	if s.RetentionPolicy.CRDs != "Retain" {
		t.Errorf("RetentionPolicy.CRDs = %q, want Retain", s.RetentionPolicy.CRDs)
	}
	if s.RemoteAccess.Server != "https://api.example:443" {
		t.Errorf("RemoteAccess.Server = %q", s.RemoteAccess.Server)
	}
	if s.RemoteNamespace != "shoot--x--y" {
		t.Errorf("RemoteNamespace = %q, want shoot--x--y", s.RemoteNamespace)
	}
	if s.ApplyOrder != "HostFirst" {
		t.Errorf("ApplyOrder = %q, want HostFirst", s.ApplyOrder)
	}
}
