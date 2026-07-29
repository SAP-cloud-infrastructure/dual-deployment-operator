// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"context"
	"testing"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source"
)

// operators enumerates the fixtures exercised by the equivalence suite. One
// subtest per operator; each runs in the default test job (no build tag / env
// gate) so equivalence gates every PR.
var operators = []string{
	"metal-operator",
	"khalkeon",
}

// TestEquivalence proves the operator's rendered output matches today's
// <operator>-remote wrapper chart for each fixture. Golden side: render the
// wrapper chart from git at a pinned SHA, then classify/unwrap/exclude into bare
// seed/shoot object sets. Operator side: drive the render+transform pipeline.
// Both sides are compared per resource after normalization + allowlist stripping.
func TestEquivalence(t *testing.T) {
	for _, op := range operators {
		t.Run(op, func(t *testing.T) {
			f, err := LoadFixture("../../testdata/fixtures/" + op)
			if err != nil {
				t.Fatalf("load fixture: %v", err)
			}

			// Golden side: render the wrapper chart from git at the pinned SHA.
			goldenDocs, err := RenderGolden(context.Background(), GoldenRenderReq{
				RepoURL:    f.RepoURL,
				SHA:        f.SHA,
				ChartPath:  f.ChartPath,
				ValuesYAML: [][]byte{f.OverlayRaw},
				Namespace:  f.CR.Namespace,
			})
			if err != nil {
				t.Fatalf("golden render: %v", err)
			}
			shootFromMR, passthrough, err := UnwrapManagedResources(goldenDocs)
			if err != nil {
				t.Fatalf("golden unwrap: %v", err)
			}
			golden, err := ClassifyGolden(passthrough, shootFromMR, GoldenOpts{
				ChartFullname: f.ChartFullname,
				Exclusions:    f.Exclusions,
			})
			if err != nil {
				t.Fatalf("golden classify: %v", err)
			}

			// Operator side: drive the real render+transform pipeline.
			deps := source.Deps{
				ChartLoader:  source.NewHelmLoader(),
				RootResolver: source.NewGitResolver(),
			}
			opSeed, opShoot, err := CaptureOperator(context.Background(), f.CR, deps)
			if err != nil {
				t.Fatalf("operator capture: %v", err)
			}

			// Scoped equivalence (Decision B): compare only the delivered kinds both
			// sides are expected to produce, dropping per-fixture known divergences.
			scope := Scope{ComparedKinds: f.ComparedKinds, KnownDivergences: f.KnownDivergences, IgnoreLabels: f.IgnoreLabels, CanonicalNamespace: f.CanonicalNamespace}
			seedReport := Compare(scope.Apply(golden.Seed), scope.Apply(opSeed))
			shootReport := Compare(scope.Apply(golden.Shoot), scope.Apply(opShoot))
			if !seedReport.Equal() {
				t.Errorf("seed render mismatch:\n%s", seedReport.String())
			}
			if !shootReport.Equal() {
				t.Errorf("shoot render mismatch:\n%s", shootReport.String())
			}
		})
	}
}
