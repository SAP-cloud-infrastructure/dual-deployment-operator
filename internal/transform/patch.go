// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"encoding/json"
	"errors"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type patch struct {
	spec *v1alpha1.PatchSpec
}

func (p *patch) Type() string { return "patch" }

// Apply applies the strategic-merge XOR json patch to every manifest matching
// the target selector. Fails loud on zero matches. Never mutates input in place.
func (p *patch) Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error) {
	hasSM := p.spec.StrategicMerge != nil
	hasJP := len(p.spec.JSONPatch) > 0
	if hasSM == hasJP {
		return nil, errors.New("exactly one of strategicMerge or jsonPatch must be set")
	}

	out := make([]manifest.Manifest, len(manifests))
	matched := 0
	for i, m := range manifests {
		if !Match(m, p.spec.Target) {
			out[i] = m
			continue
		}
		matched++
		patched, err := p.applyOne(m.Unstructured)
		if err != nil {
			return nil, fmt.Errorf("patch %s/%s: %w", m.Unstructured.GetKind(), m.Unstructured.GetName(), err)
		}
		out[i] = manifest.Manifest{Unstructured: patched, Origin: m.Origin}
	}
	if matched == 0 {
		return nil, errors.New("no matching resource for patch target selector")
	}
	return out, nil
}

func (p *patch) applyOne(u *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	orig, err := json.Marshal(u.Object)
	if err != nil {
		return nil, err
	}

	var result []byte
	switch {
	case p.spec.StrategicMerge != nil:
		// No Go struct schema is available for arbitrary Kubernetes objects, so
		// this degrades to a JSON merge patch (RFC 7386), which is correct for
		// the v1 candidate patches (labels, replicas, sidecar containers keyed
		// by name). Users needing custom-type list-key semantics use jsonPatch.
		result, err = jsonpatch.MergePatch(orig, p.spec.StrategicMerge.Raw)
		if err != nil {
			return nil, err
		}
	default:
		jp, err := json.Marshal(p.spec.JSONPatch)
		if err != nil {
			return nil, err
		}
		decoded, err := jsonpatch.DecodePatch(jp)
		if err != nil {
			return nil, err
		}
		result, err = decoded.Apply(orig)
		if err != nil {
			return nil, err
		}
	}

	res := &unstructured.Unstructured{}
	if err := res.UnmarshalJSON(result); err != nil {
		return nil, err
	}
	return res, nil
}
