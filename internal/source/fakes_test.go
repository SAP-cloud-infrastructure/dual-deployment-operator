// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

// fakeChartLoader loads a chart from a local directory, ignoring repo/name/version.
type fakeChartLoader struct{ dir string }

func (f fakeChartLoader) Load(_ context.Context, _, _, _ string) (*chart.Chart, error) {
	return loader.Load(f.dir)
}

// fakeRootResolver resolves any url+subPath to baseDir/subPath on local disk.
type fakeRootResolver struct{ baseDir string }

func (f fakeRootResolver) Resolve(_ context.Context, _, subPath string) (string, func(), error) {
	return f.baseDir + "/" + subPath, func() {}, nil
}
