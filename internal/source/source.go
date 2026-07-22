// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

// Package source renders a DualDeploymentOperator spec.source into origin-tagged
// manifest streams, once per mode (host or remote), per the two-render pattern.
package source

import (
	"context"
	"errors"

	"helm.sh/helm/v3/pkg/chart"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// Mode selects which render to produce; each render's output targets one cluster.
type Mode string

const (
	// ModeHost renders the host-cluster (seed) resources.
	ModeHost Mode = "host"
	// ModeRemote renders the remote-cluster (shoot) resources.
	ModeRemote Mode = "remote"
)

// Source renders the manifest stream for a specific mode.
type Source interface {
	Render(ctx context.Context, mode Mode, namespace string) ([]manifest.Manifest, error)
}

// ChartLoader acquires a Helm chart. Production pulls from OCI/HTTP; tests fake it.
type ChartLoader interface {
	Load(ctx context.Context, repo, name, version string) (*chart.Chart, error)
}

// RootResolver resolves a kustomize root (base URL + mode subpath) to a local
// filesystem path plus a cleanup func. Production fetches the pinned remote URL;
// tests resolve to a local overlay directory.
type RootResolver interface {
	Resolve(ctx context.Context, url, subPath string) (fsPath string, cleanup func(), err error)
}

// Deps holds the injectable fetchers a Source needs.
type Deps struct {
	ChartLoader  ChartLoader
	RootResolver RootResolver
}

// From constructs a Source from a spec discriminator plus its dependencies.
// Exactly one of spec.Helm / spec.Kustomize must be set.
func From(spec v1alpha1.Source, deps Deps) (Source, error) {
	switch {
	case spec.Helm != nil && spec.Kustomize == nil:
		if deps.ChartLoader == nil {
			return nil, errors.New("source: helm source requires a ChartLoader (none configured)")
		}
		return &helmSource{spec: spec.Helm, loader: deps.ChartLoader}, nil
	case spec.Kustomize != nil && spec.Helm == nil:
		if deps.RootResolver == nil {
			return nil, errors.New("source: kustomize source requires a RootResolver (none configured)")
		}
		return &kustomizeSource{spec: spec.Kustomize, resolver: deps.RootResolver}, nil
	default:
		return nil, errors.New("source: exactly one of source.helm or source.kustomize must be set")
	}
}
