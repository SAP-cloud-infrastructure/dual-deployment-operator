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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	clientscheme "k8s.io/client-go/kubernetes/scheme"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type patch struct {
	spec *v1alpha1.PatchSpec
}

func (p *patch) Type() string { return "patch" }

// Apply applies the strategic-merge XOR json patch to every manifest matching
// the target selector. A zero-match selector is a clean no-op: the input
// manifests are returned unchanged, no error (consistent with rewriteWebhookURL
// and filterKinds). Never mutates input in place.
func (p *patch) Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error) {
	hasSM := p.spec.StrategicMerge != nil
	hasJP := len(p.spec.JSONPatch) > 0
	if hasSM == hasJP {
		return nil, errors.New("exactly one of strategicMerge or jsonPatch must be set")
	}

	out := make([]manifest.Manifest, len(manifests))
	for i, m := range manifests {
		if !Match(m, p.spec.Target) {
			out[i] = m
			continue
		}
		patched, err := p.applyOne(m.Unstructured)
		if err != nil {
			return nil, fmt.Errorf("patch %s/%s: %w", m.Unstructured.GetKind(), m.Unstructured.GetName(), err)
		}
		out[i] = manifest.Manifest{Unstructured: patched, Origin: m.Origin}
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
		result, err = strategicMerge(u, orig, p.spec.StrategicMerge.Raw)
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

// strategicMerge applies patch using Kubernetes strategic-merge semantics
// (list merge keys such as containers[].name) when u's GVK resolves to a
// built-in typed struct in the client-go scheme. For types with no registered
// struct (e.g. custom resources), it falls back to an RFC 7386 JSON merge patch.
func strategicMerge(u *unstructured.Unstructured, orig, patch []byte) ([]byte, error) {
	if dataStruct, ok := typedStructFor(u.GroupVersionKind()); ok {
		return strategicpatch.StrategicMergePatch(orig, patch, dataStruct)
	}
	return jsonpatch.MergePatch(orig, patch)
}

func typedStructFor(gvk schema.GroupVersionKind) (runtime.Object, bool) {
	obj, err := clientscheme.Scheme.New(gvk)
	if err != nil {
		return nil, false
	}
	if _, isUnstructured := obj.(runtime.Unstructured); isUnstructured {
		return nil, false
	}
	return obj, true
}
