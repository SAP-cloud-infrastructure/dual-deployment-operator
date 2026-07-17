// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// mustJSON builds an apiextensionsv1.JSON from a raw JSON string.
func mustJSON(s string) *apiextensionsv1.JSON {
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}

// mustManifest parses a single YAML doc into a manifest.Manifest with the given origin.
func mustManifest(t *testing.T, y string, origin manifest.Origin) manifest.Manifest {
	t.Helper()
	m := map[string]any{}
	if err := yaml.Unmarshal([]byte(y), &m); err != nil {
		t.Fatalf("mustManifest: %v", err)
	}
	return manifest.Manifest{Unstructured: &unstructured.Unstructured{Object: m}, Origin: origin}
}

// cloneForAssert deep-copies a slice so a test can assert the original was not mutated.
func cloneForAssert(in []manifest.Manifest) []manifest.Manifest {
	out := make([]manifest.Manifest, len(in))
	for i, m := range in {
		out[i] = manifest.Manifest{Unstructured: m.Unstructured.DeepCopy(), Origin: m.Origin}
	}
	return out
}
