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

// TestEquivalenceIPAMCAPI is the fifth equivalence fixture. It proves that the
// operator's full KustomizeSource render pipeline produces the same objects as a
// direct krusty build of the same upstream-anchored overlay.
//
// # Golden source
//
// Golden side: RenderGoldenKustomize of
// system/kustomize/ipam-capi-remote/managedresources at the pinned
// sapcc/helm-charts SHA. This overlay is the canonical upstream-anchored reference:
// it contains ZERO SAP-specific content — it is a pure aggregation kustomization
// that points directly at the upstream ipam-capi project:
//
//   - github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster//config/crd?ref=v1.1.0
//   - github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster//config/rbac?ref=v1.1.0
//   - three raw.githubusercontent.com CAPI CRD files at v1.13.4
//
// with only a namePrefix (ipam-capi-) and namespace (kube-system) applied by the
// kustomization itself. There are no SAP-authored templates, additions, or patches.
// The golden is therefore effectively a direct render of the upstream
// kubernetes-sigs/cluster-api-ipam-provider-in-cluster project at v1.1.0, combined
// with the three CAPI CRD bases — the upstream project's own deliverable.
//
// NOTE: the upstream config/crd and config/rbac kustomizations cannot be rendered
// in isolation as a single golden (they are two separate roots combining different
// content), and config/rbac includes auth_proxy_* components that are not in scope
// here. The managedresources/ aggregation kustomization is the correct upstream-
// anchored golden because it selects exactly the CRD+RBAC subset the operator
// delivers, with the same namespace/prefix settings.
//
// # Operator side
//
// CaptureShootOnly (deliberate deviation from TestEquivalence): the seed/manager
// overlay at this SHA uses repo-root-relative patch paths that fail when
// KustomizeSource builds from the overlay subdir. Seed is skipped entirely.
// See testdata/fixtures/ipam-capi/README.md and
// docs/implementation.md §"ipam-capi equivalence is DEFERRED".
//
// # Why this is not self-comparison
//
// The golden (RenderGoldenKustomize) and operator side (CaptureShootOnly) both run
// krusty against the same overlay, but through entirely different code paths:
//   - Golden: git clone → checkout SHA → krusty.Run directly
//   - Operator: gitResolver.Resolve → preflightSHAGate → krusty.Run via kustomizeSource.Render
//
// The comparison catches any regression in the operator's preflighting, resolver,
// or render pipeline that causes it to diverge from the direct krusty output.
//
// # Gate
//
// Gated on RUN_KUSTOMIZE_EQUIVALENCE=1 (narrower than RUN_EQUIVALENCE, which also
// requires the internal keppel OCI registry). This test only needs outbound HTTPS
// access to github.com and raw.githubusercontent.com — available on public GitHub
// Actions runners and local dev. It is NOT enabled by default because live git
// clones and krusty remote-base fetches carry a flakiness risk (rate limits,
// transient network failures) that is unsuitable for the standard unit-test job.
// Enable it in a dedicated network-access job (or locally) by setting
// RUN_KUSTOMIZE_EQUIVALENCE=1. RUN_EQUIVALENCE=1 also enables it (superset).
func TestEquivalenceIPAMCAPI(t *testing.T) {
	requireKustomizeEquivalenceEnabled(t)

	f, err := LoadFixture("../../testdata/fixtures/ipam-capi")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	// Golden side: direct krusty build of the upstream-anchored managedresources/
	// overlay (no Helm, no SAP wrapper chart — pure upstream content).
	goldenDocs, err := RenderGoldenKustomize(context.Background(), KustomizeGoldenRenderReq{
		RepoURL:       f.RepoURL,
		SHA:           f.SHA,
		KustomizePath: f.KustomizePath + "/managedresources",
		Namespace:     f.CR.Spec.ShootNamespace,
	})
	if err != nil {
		t.Fatalf("golden render: %v", err)
	}

	golden := ObjectSet{}
	for _, d := range goldenDocs {
		if isExcluded(d, f.Exclusions) {
			continue
		}
		golden[KeyOf(d)] = d
	}

	// Operator side: shoot-only capture through the full production pipeline.
	deps := source.Deps{
		RootResolver: source.NewGitResolver(),
	}
	opShoot, err := CaptureShootOnly(context.Background(), f.CR, deps)
	if err != nil {
		t.Fatalf("operator capture: %v", err)
	}

	scope := Scope{ComparedKinds: f.ComparedKinds, KnownDivergences: f.KnownDivergences, IgnoreLabels: f.IgnoreLabels, CanonicalNamespace: f.CanonicalNamespace}
	goldenScoped, opScoped := scope.Apply(golden), scope.Apply(opShoot)

	if len(goldenScoped) == 0 {
		t.Fatalf("scoped golden set is empty — fixture kustomizePath/comparedKinds likely wrong")
	}
	if len(opScoped) == 0 {
		t.Fatalf("scoped operator set is empty — fixture CR/comparedKinds likely wrong")
	}

	if report := scope.CompareScoped(goldenScoped, opScoped); !report.Equal() {
		t.Errorf("shoot render mismatch:\n%s", report.String())
	}
}
