// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

// envtestFakeChartLoader loads a Helm chart from a local directory, ignoring
// repo/name/version. Used only in envtest-based controller tests to inject the
// demo testdata chart without pulling from a real registry.
type envtestFakeChartLoader struct{ dir string }

func (f envtestFakeChartLoader) Load(_ context.Context, _, _, _ string) (*chart.Chart, error) {
	return loader.Load(f.dir)
}

// switchableChartLoader is a chart loader for prune tests.
// pruneRound=false: renders CRD + orphan-cm ConfigMap (host mode only).
// pruneRound=true:  renders only the CRD (orphan-cm absent → prune fires).
type switchableChartLoader struct{ pruneRound bool }

func (s *switchableChartLoader) Load(_ context.Context, _, _, _ string) (*chart.Chart, error) {
	files := []*loader.BufferedFile{
		{
			Name: "Chart.yaml",
			Data: []byte("apiVersion: v2\nname: prune-test\nversion: 0.1.0\n"),
		},
		{
			Name: "crds/test-crd.yaml",
			Data: []byte(`apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: demos.demo.cc.sap
spec:
  group: demo.cc.sap
  names: {kind: Demo, listKind: DemoList, plural: demos, singular: demo}
  scope: Namespaced
  versions:
    - name: v1alpha1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
`),
		},
	}
	if !s.pruneRound {
		files = append(files, &loader.BufferedFile		{
			Name: "templates/orphan-cm.yaml",
			Data: []byte(`{{- if eq .Values.mode "seed" }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: orphan-cm
data:
  key: value
{{- end }}
`),
		})
	}
	return loader.LoadFiles(files)
}
