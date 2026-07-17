// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type filterKinds struct {
	spec *v1alpha1.FilterKindsSpec
}

func (f *filterKinds) Type() string { return "filterKinds" }

// Apply drops manifests whose kind is listed in spec.Kinds. When spec.Source is
// set, only manifests of that origin are dropped. Non-matching manifests are
// returned unchanged and in original order. Zero matches is a no-op (no error).
func (f *filterKinds) Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error) {
	drop := make(map[string]struct{}, len(f.spec.Kinds))
	for _, k := range f.spec.Kinds {
		drop[k] = struct{}{}
	}
	out := make([]manifest.Manifest, 0, len(manifests))
	for _, m := range manifests {
		_, kindListed := drop[m.Unstructured.GetKind()]
		originMatch := f.spec.Source == "" || string(m.Origin) == f.spec.Source
		if kindListed && originMatch {
			continue // dropped
		}
		out = append(out, m)
	}
	return out, nil
}
