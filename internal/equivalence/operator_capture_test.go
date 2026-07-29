// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"context"
	"path/filepath"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source"
)

// eqFakeChartLoader loads a chart from a local dir, ignoring repo/name/version.
type eqFakeChartLoader struct{ dir string }

func (f eqFakeChartLoader) Load(_ context.Context, _, _, _ string) (*chart.Chart, error) {
	return loader.Load(f.dir)
}

func TestCaptureOperatorReturnsSets(t *testing.T) {
	cr := &v1alpha1.DualDeploymentOperator{}
	cr.Namespace = "ns"
	cr.Spec.ShootNamespace = "shoot-ns"
	cr.Spec.Source = v1alpha1.Source{Helm: &v1alpha1.HelmSource{Repo: "x", Name: "demo", Version: "0"}}

	deps := source.Deps{ChartLoader: eqFakeChartLoader{dir: filepath.Join("..", "source", "testdata", "charts", "demo")}}
	seed, shoot, err := CaptureOperator(context.Background(), cr, deps)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(seed) == 0 && len(shoot) == 0 {
		t.Fatal("expected at least one captured resource across seed+shoot")
	}
}
