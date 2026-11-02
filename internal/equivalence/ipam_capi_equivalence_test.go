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

// TestEquivalenceIPAMCAPI proves that the operator's kustomize render for the
// ipam-capi shoot overlay produces output equivalent to today's ipam-capi-remote
// Helm chart's shoot content at the same pinned sapcc/helm-charts SHA.
//
// This follows the same pattern as the four Helm-source fixtures: golden side =
// git clone + helm dependency build + helm template of system/ipam-capi-remote/;
// operator side = source.From → Render (KustomizeSource, shoot mode) → transform.Build.
// Both sides are scoped to the managedresources/shoot content, compared per resource
// after normalization and allowlist stripping.
//
// # Scope: shoot overlay only
//
// Only the shoot/managedresources overlay is compared. The seed/manager overlay
// is deliberately excluded — see testdata/fixtures/ipam-capi/README.md and
// docs/implementation.md §"ipam-capi equivalence is DEFERRED" for the full
// rationale. In short: the manager overlay references patch files by repo-root-
// relative paths that only resolve when kustomize builds from the repo root;
// the operator's KustomizeSource builds from the overlay subdir instead, so
// those patch paths fail. Closing the seed overlay requires extending
// KustomizeSource (CRD + source change, design §9.4) — explicitly out of scope.
// CaptureShootOnly is used to avoid triggering the broken seed render.
//
// # Network access
//
// This test gates on RUN_EQUIVALENCE=1. The golden Helm render requires:
//   - git clone of github.com/sapcc/helm-charts
//   - helm dependency build pulling subcharts from the internal keppel OCI
//     registry (keppel.global.cloud.sap/ccloud-helm/...) — same as the other
//     four Helm fixtures
//
// The operator-side kustomize render requires:
//   - git clone of github.com/sapcc/helm-charts (same SHA)
//   - krusty fetching transitive remote bases from github.com and
//     raw.githubusercontent.com (cluster-api-ipam-provider-in-cluster ?ref=v1.1.0,
//     cluster-api CRD bases v1.13.4) — public/anonymous
func TestEquivalenceIPAMCAPI(t *testing.T) {
	requireEquivalenceEnabled(t)

	f, err := LoadFixture("../../testdata/fixtures/ipam-capi")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	// Golden side: render the ipam-capi-remote Helm wrapper chart from git at
	// the pinned SHA. system/ipam-capi-remote/ is the helmified wrapper (the
	// current deployed baseline), same structure as the other four operator
	// fixtures. Its shoot content is wrapped in ManagedResources and must be
	// unwrapped before comparison.
	goldenDocs, err := RenderGolden(context.Background(), GoldenRenderReq{
		RepoURL:    f.RepoURL,
		SHA:        f.SHA,
		ChartPath:  f.ChartPath,
		ValuesYAML: [][]byte{f.OverlayRaw},
		Namespace:  f.CR.Namespace,
	})
	if err != nil {
		t.Fatalf("golden helm render: %v", err)
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

	// Operator side: drive ONLY the shoot render+transform pipeline. The seed
	// render is deliberately skipped — the ipam-capi seed/manager overlay
	// uses repo-root-relative patch paths that only resolve when building from
	// the repo root, not from the overlay subdir that KustomizeSource uses.
	// CaptureShootOnly avoids triggering that broken seed render entirely.
	deps := source.Deps{
		RootResolver: source.NewGitResolver(),
	}
	opShoot, err := CaptureShootOnly(context.Background(), f.CR, deps)
	if err != nil {
		t.Fatalf("operator shoot capture: %v", err)
	}

	// Scoped equivalence (Decision B): compare only the delivered kinds both
	// sides are expected to produce, dropping per-fixture known divergences.
	scope := Scope{
		ComparedKinds:      f.ComparedKinds,
		KnownDivergences:   f.KnownDivergences,
		IgnoreLabels:       f.IgnoreLabels,
		CanonicalNamespace: f.CanonicalNamespace,
	}
	goldenShoot := scope.Apply(golden.Shoot)
	opShootScoped := scope.Apply(opShoot)

	// Non-vacuity floor: ipam-capi's shoot overlay delivers CRDs + RBAC,
	// so neither scoped set may be empty. A fixture typo (bad comparedKinds,
	// over-broad suppression) that empties both sides would pass a vacuous
	// Compare — guard against that false green.
	if len(goldenShoot) == 0 {
		t.Fatalf("scoped golden shoot set is empty — fixture comparedKinds or chartPath likely wrong")
	}
	if len(opShootScoped) == 0 {
		t.Fatalf("scoped operator shoot set is empty — fixture CR or comparedKinds likely wrong")
	}

	shootReport := scope.CompareScoped(goldenShoot, opShootScoped)
	if !shootReport.Equal() {
		t.Errorf("ipam-capi shoot render mismatch:\n%s", shootReport.String())
	}
}
