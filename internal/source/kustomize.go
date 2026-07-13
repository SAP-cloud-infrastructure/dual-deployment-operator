// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"fmt"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type kustomizeSource struct {
	spec     *v1alpha1.KustomizeSource
	resolver RootResolver
}

func (k *kustomizeSource) Render(ctx context.Context, mode Mode) ([]manifest.Manifest, error) {
	subPath := k.spec.HostPath
	if mode == ModeRemote {
		subPath = k.spec.RemotePath
	}

	root, cleanup, err := k.resolver.Resolve(ctx, k.spec.URL, subPath)
	if err != nil {
		return nil, fmt.Errorf("source: resolve kustomize root (mode=%s): %w", mode, err)
	}
	defer cleanup()

	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = types.LoadRestrictionsNone
	kust := krusty.MakeKustomizer(opts)
	resMap, err := kust.Run(filesys.MakeFsOnDisk(), root)
	if err != nil {
		return nil, fmt.Errorf("source: kustomize build (mode=%s): %w", mode, err)
	}
	yamlBytes, err := resMap.AsYaml()
	if err != nil {
		return nil, fmt.Errorf("source: kustomize serialize: %w", err)
	}
	return manifest.Parse(yamlBytes, manifest.OriginUpstream)
}
