// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"bytes"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// Parse splits a multi-document YAML byte stream into one Manifest per real
// Kubernetes object. Empty, whitespace-only, comment-only, and null documents
// are skipped. Each real document must declare apiVersion and kind, else an
// error is returned. Origin is derived from the OriginAnnotation: value
// "additions" -> OriginAdditions; absent/empty/other -> fallback. An empty
// render (no real objects) returns an empty slice and no error.
func Parse(raw []byte, fallback Origin) ([]Manifest, error) {
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var out []Manifest
	idx := 0
	for {
		obj := map[string]interface{}{}
		err := dec.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("manifest: decode document %d: %w", idx, err)
		}
		idx++
		if len(obj) == 0 {
			continue // empty / comment-only / null document
		}
		u := &unstructured.Unstructured{Object: obj}
		if u.GetAPIVersion() == "" {
			return nil, fmt.Errorf("manifest: document %d is missing apiVersion", idx-1)
		}
		if u.GetKind() == "" {
			return nil, fmt.Errorf("manifest: document %d is missing kind", idx-1)
		}
		out = append(out, Manifest{Unstructured: u, Origin: originOf(u, fallback)})
	}
	return out, nil
}

func originOf(u *unstructured.Unstructured, fallback Origin) Origin {
	if u.GetAnnotations()[OriginAnnotation] == string(OriginAdditions) {
		return OriginAdditions
	}
	return fallback
}
