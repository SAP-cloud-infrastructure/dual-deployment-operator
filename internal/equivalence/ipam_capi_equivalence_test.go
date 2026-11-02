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

// TestEquivalenceIPAMCAPI proves the operator's kustomize render for the
// ipam-capi shoot overlay matches the golden krusty build of
// system/kustomize/ipam-capi-remote/managedresources at the pinned SHA.
//
// # Scope
//
// Only the shoot/managedresources overlay is covered. The seed/manager overlay
// is deliberately excluded — see testdata/fixtures/ipam-capi/README.md and
// docs/implementation.md §"ipam-capi equivalence is DEFERRED" for the full
// rationale. In short: the manager overlay references patch files by
// repo-root-relative paths that only resolve when kustomize builds from the repo
// root; the operator's KustomizeSource builds from the overlay subdir instead,
// so those patch paths fail with "no such file". Closing the seed overlay
// requires extending KustomizeSource so the clone root and the kustomize-build
// dir can differ (CRD + source change, design §9.4).
//
// # Network access
//
// This test gates on RUN_EQUIVALENCE=1 (same as the four Helm fixtures). On the
// golden side krusty.Run fetches transitive remote bases from github.com and
// raw.githubusercontent.com at build time (ipam-capi pulls
// cluster-api-ipam-provider-in-cluster//config/*?ref=v1.1.0 and cluster-api
// CRD bases). On the operator side the production gitResolver clones
// sapcc/helm-charts at the pinned SHA. Both require network access; neither
// needs OCI registry credentials (all sources are public/anonymous).
func TestEquivalenceIPAMCAPI(t *testing.T) {
	requireEquivalenceEnabled(t)

	f, err := LoadFixture("../../testdata/fixtures/ipam-capi")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if !f.IsKustomize() {
		t.Fatalf("ipam-capi fixture must have kustomizePath set (got Helm fixture)")
	}

	// Golden side: clone sapcc/helm-charts at the pinned SHA and krusty-build
	// the managedresources overlay. This is the kustomize equivalent of the Helm
	// fixtures' RenderGolden + UnwrapManagedResources + ClassifyGolden sequence.
	// ipam-capi's shoot overlay is pure kustomize (no ManagedResource wrapper):
	// krusty emits the bare CRDs+RBAC directly, so no unwrapping is needed.
	goldenDocs, err := RenderGoldenKustomize(context.Background(), KustomizeGoldenRenderReq{
		RepoURL:       f.RepoURL,
		SHA:           f.SHA,
		KustomizePath: f.KustomizePath,
		Namespace:     f.CR.Spec.ShootNamespace,
	})
	if err != nil {
		t.Fatalf("golden kustomize render: %v", err)
	}

	// Build the golden shoot set: all docs are shoot-destined (the
	// managedresources overlay is pure shoot content — CRDs, RBAC, webhooks),
	// minus any enumerated exclusions.
	golden := ObjectSet{}
	for _, d := range goldenDocs {
		if isExcluded(d, f.Exclusions) {
			continue
		}
		golden[KeyOf(d)] = d
	}

	// Operator side: drive the real kustomize render+transform pipeline.
	deps := source.Deps{
		RootResolver: source.NewGitResolver(),
	}
	_, opShoot, err := CaptureOperator(context.Background(), f.CR, deps)
	if err != nil {
		t.Fatalf("operator capture: %v", err)
	}

	// Scoped equivalence: compare only the delivered kinds both sides are
	// expected to produce, dropping per-fixture known divergences.
	scope := Scope{
		ComparedKinds:      f.ComparedKinds,
		KnownDivergences:   f.KnownDivergences,
		IgnoreLabels:       f.IgnoreLabels,
		CanonicalNamespace: f.CanonicalNamespace,
	}
	goldenScoped := scope.Apply(golden)
	opShootScoped := scope.Apply(opShoot)

	// Non-vacuity floor: ipam-capi's shoot overlay delivers CRDs + RBAC,
	// so neither scoped set may be empty (a fixture typo that empties both
	// sides would let a vacuous Compare pass as a false green).
	if len(goldenScoped) == 0 {
		t.Fatalf("scoped golden shoot set is empty — fixture kustomizePath or comparedKinds likely wrong")
	}
	if len(opShootScoped) == 0 {
		t.Fatalf("scoped operator shoot set is empty — fixture CR or comparedKinds likely wrong")
	}

	shootReport := scope.CompareScoped(goldenScoped, opShootScoped)
	if !shootReport.Equal() {
		t.Errorf("ipam-capi shoot render mismatch:\n%s", shootReport.String())
	}
}
