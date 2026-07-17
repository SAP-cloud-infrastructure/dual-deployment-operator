// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

// Package transform applies a CR's spec.transformations to a single render's
// manifest stream. All transformations are per-render (design revision 7);
// there is no cross-stream scope.
package transform

import (
	"fmt"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// Transformation operates on a single render's manifest stream. Apply MUST
// return fresh manifests and MUST NOT mutate the input slice or the underlying
// Unstructured objects.
type Transformation interface {
	Type() string
	Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error)
}

// Build parses spec.Transformations into an ordered []Transformation,
// preserving declaration order. Each entry MUST set exactly one transformation
// type; an entry with none or more than one set is an error (mirrors the CRD
// union CEL rule so programmatic callers fail the same way as admission).
func Build(specs []v1alpha1.Transformation) ([]Transformation, error) {
	out := make([]Transformation, 0, len(specs))
	for i := range specs {
		s := specs[i]
		set := 0
		if s.Patch != nil {
			set++
		}
		if s.RewriteWebhookURL != nil {
			set++
		}
		if s.FilterKinds != nil {
			set++
		}
		if set != 1 {
			return nil, fmt.Errorf("exactly one transformation type must be set per entry, got %d", set)
		}
		switch {
		case s.Patch != nil:
			out = append(out, &patch{spec: s.Patch})
		case s.RewriteWebhookURL != nil:
			out = append(out, &rewriteWebhookURL{spec: s.RewriteWebhookURL})
		case s.FilterKinds != nil:
			out = append(out, &filterKinds{spec: s.FilterKinds})
		}
	}
	return out, nil
}
