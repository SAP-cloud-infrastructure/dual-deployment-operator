// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type kustomizeSource struct {
	spec     *v1alpha1.KustomizeSource
	resolver RootResolver
}

func (k *kustomizeSource) Render(_ context.Context, _ Mode) ([]manifest.Manifest, error) {
	panic("not implemented")
}
