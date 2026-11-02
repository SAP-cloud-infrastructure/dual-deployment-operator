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

// TestEquivalenceIPAMCAPI is the fifth equivalence fixture. It follows the exact
// same structure as TestEquivalence (the four Helm-source fixtures), with one
// deliberate deviation: CaptureShootOnly is used instead of CaptureOperator so
// that only the shoot render runs. The seed/manager overlay at this SHA uses
// repo-root-relative patch paths that only resolve when kustomize builds from the
// repo root; KustomizeSource builds from the overlay subdir, so the seed render
// fails. See testdata/fixtures/ipam-capi/README.md and
// docs/implementation.md §"ipam-capi equivalence is DEFERRED" for the full
// rationale. Only the shoot comparison is made; seed is left nil and not asserted.
func TestEquivalenceIPAMCAPI(t *testing.T) {
	requireEquivalenceEnabled(t)

	f, err := LoadFixture("../../testdata/fixtures/ipam-capi")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	// Golden side: identical to TestEquivalence — render the wrapper chart from
	// git at the pinned SHA, unwrap ManagedResources, classify.
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

	// Operator side: shoot only (deliberate deviation from TestEquivalence which
	// calls CaptureOperator for both seed+shoot). The seed render is skipped
	// because the seed/manager overlay at this SHA has repo-root-relative patch
	// paths that fail when KustomizeSource builds from the overlay subdir.
	deps := source.Deps{
		RootResolver: source.NewGitResolver(),
	}
	opShoot, err := CaptureShootOnly(context.Background(), f.CR, deps)
	if err != nil {
		t.Fatalf("operator capture: %v", err)
	}

	// Scoped comparison — identical to TestEquivalence, shoot side only.
	scope := Scope{ComparedKinds: f.ComparedKinds, KnownDivergences: f.KnownDivergences, IgnoreLabels: f.IgnoreLabels, CanonicalNamespace: f.CanonicalNamespace}
	goldenShoot, opShootScoped := scope.Apply(golden.Shoot), scope.Apply(opShoot)

	if len(goldenShoot) == 0 {
		t.Fatalf("scoped golden shoot set is empty — fixture comparedKinds/scoping likely wrong")
	}
	if len(opShootScoped) == 0 {
		t.Fatalf("scoped operator shoot set is empty — fixture CR/scoping likely wrong")
	}

	if shootReport := scope.CompareScoped(goldenShoot, opShootScoped); !shootReport.Equal() {
		t.Errorf("shoot render mismatch:\n%s", shootReport.String())
	}
}
