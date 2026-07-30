// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"context"
	"fmt"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/transform"
)

// CaptureOperator drives the operator's render+transform pipeline for both modes
// and returns the seed and shoot object sets, captured before the delivery layer.
// It runs NO reconciler, envtest, or delivery applier.
func CaptureOperator(ctx context.Context, cr *v1alpha1.DualDeploymentOperator, deps source.Deps) (seedSet, shootSet ObjectSet, err error) {
	src, err := source.From(cr.Spec.Source, deps)
	if err != nil {
		return nil, nil, fmt.Errorf("capture: source.From: %w", err)
	}
	transforms, err := transform.Build(cr.Spec.Transformations)
	if err != nil {
		return nil, nil, fmt.Errorf("capture: transform.Build: %w", err)
	}
	seed, err := renderMode(ctx, src, source.ModeSeed, cr.Namespace, transforms)
	if err != nil {
		return nil, nil, fmt.Errorf("capture: seed: %w", err)
	}
	shoot, err := renderMode(ctx, src, source.ModeShoot, cr.Spec.ShootNamespace, transforms)
	if err != nil {
		return nil, nil, fmt.Errorf("capture: shoot: %w", err)
	}
	return seed, shoot, nil
}

func renderMode(ctx context.Context, src source.Source, mode source.Mode, ns string, transforms []transform.Transformation) (ObjectSet, error) {
	ms, err := src.Render(ctx, mode, ns)
	if err != nil {
		return nil, err
	}
	for _, t := range transforms {
		ms, err = t.Apply(ms)
		if err != nil {
			return nil, err
		}
	}
	set := ObjectSet{}
	for i := range ms {
		u := ms[i].Unstructured
		set[KeyOf(u)] = u
	}
	return set, nil
}
