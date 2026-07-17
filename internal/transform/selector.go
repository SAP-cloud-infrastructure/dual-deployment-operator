// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"path"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// Match reports whether m satisfies every set field of sel. An unset field does
// not constrain the match; an all-empty selector matches every manifest.
// Kind matches exactly; Name matches by glob (path.Match); Origin matches exactly.
func Match(m manifest.Manifest, sel v1alpha1.Selector) bool {
	if sel.Kind != "" && m.Unstructured.GetKind() != sel.Kind {
		return false
	}
	if sel.Name != "" {
		ok, err := path.Match(sel.Name, m.Unstructured.GetName())
		if err != nil || !ok {
			return false
		}
	}
	if sel.Origin != "" && string(m.Origin) != sel.Origin {
		return false
	}
	return true
}
