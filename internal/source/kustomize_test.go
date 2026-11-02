// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func newKustomize(t *testing.T) Source {
	t.Helper()
	s, err := From(v1alpha1.Source{Kustomize: &v1alpha1.KustomizeSource{
		URL: "ignored?ref=x", SeedPath: "seed", ShootPath: "shoot",
	}}, Deps{RootResolver: fakeRootResolver{baseDir: "testdata/kustomize"}})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	return s
}

func TestKustomizeSeedOverlayHasAdditionsAndUpstream(t *testing.T) {
	ms, err := newKustomize(t).Render(context.Background(), ModeSeed, "seed-ns")
	if err != nil {
		t.Fatalf("render seed: %v", err)
	}
	k := kinds(ms)
	if k["ConfigMap/upstream-cm"] != manifest.OriginUpstream {
		t.Errorf("upstream-cm origin = %q, want upstream", k["ConfigMap/upstream-cm"])
	}
	if k["ConfigMap/addition-cm"] != manifest.OriginAdditions {
		t.Errorf("addition-cm origin = %q, want additions", k["ConfigMap/addition-cm"])
	}
}

func TestKustomizeShootOverlayExcludesAdditions(t *testing.T) {
	ms, err := newKustomize(t).Render(context.Background(), ModeShoot, "shoot-ns")
	if err != nil {
		t.Fatalf("render shoot: %v", err)
	}
	k := kinds(ms)
	if _, ok := k["ConfigMap/addition-cm"]; ok {
		t.Error("addition-cm should not appear in shoot overlay")
	}
	if k["ConfigMap/upstream-cm"] != manifest.OriginUpstream {
		t.Errorf("upstream-cm origin = %q, want upstream", k["ConfigMap/upstream-cm"])
	}
}

func TestKustomizeAppliesTargetNamespace(t *testing.T) {
	ms, err := newKustomize(t).Render(context.Background(), ModeSeed, "seed-ns")
	if err != nil {
		t.Fatalf("render seed: %v", err)
	}
	for _, m := range ms {
		if m.Unstructured.GetKind() == "ConfigMap" && m.Unstructured.GetNamespace() != "seed-ns" {
			t.Errorf("%s namespace = %q, want seed-ns", m.Unstructured.GetName(), m.Unstructured.GetNamespace())
		}
	}
}

// ---------------------------------------------------------------------------
// Task 4 — 4.1 RED: classify → tree-walk → SHA-gate pipeline in Render
//
// These tests assert the full preflight pipeline (classify + tree-walk + optional
// SHA-gate) runs in ALL modes before kust.Run, including permissive mode.
// ---------------------------------------------------------------------------

// buildKustomizeRootWith writes a kustomization.yaml with the given content into
// a temp dir and returns a kustomizeSource whose Resolve targets that dir.
func buildKustomizeRootWith(t *testing.T, kustContent string, strict bool) *kustomizeSource {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(kustContent), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := fakeKustomizeSpec(root)
	return &kustomizeSource{
		spec:                        &spec,
		resolver:                    fakeRootResolver{baseDir: root},
		requireSHAPinnedRemoteBases: strict,
	}
}

// TestTask4_StrictModeUnpinnedFailsBeforeKrusty verifies that in strict mode an
// unpinned (tag) remote reference is rejected by the preflight before krusty ever
// runs. The kustomizeFetchHook must NOT fire.
func TestTask4_StrictModeUnpinnedFailsBeforeKrusty(t *testing.T) {
	src := buildKustomizeRootWith(t, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/repo?ref=v1.2.3
`, true /* strict */)

	fetchReached := false
	old := kustomizeFetchHook
	kustomizeFetchHook = func() { fetchReached = true }
	defer func() { kustomizeFetchHook = old }()

	_, err := src.Render(context.Background(), ModeSeed, "ns")

	if fetchReached {
		t.Fatal("kust.Run was reached before SHA-gate rejection (zero-fetch invariant violated)")
	}
	if err == nil {
		t.Fatal("expected SHA-gate rejection for unpinned tag ref in strict mode, got nil")
	}
	if !strings.Contains(err.Error(), "SHA") && !strings.Contains(err.Error(), "pin") {
		t.Errorf("expected SHA-pin error, got: %v", err)
	}
}

// TestTask4_SSHRefRejectedInPermissiveMode verifies that an SSH (git@) reference
// is rejected by classification (not just the SHA gate) even in permissive mode.
// The kustomizeFetchHook must NOT fire.
func TestTask4_SSHRefRejectedInPermissiveMode(t *testing.T) {
	src := buildKustomizeRootWith(t, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - git@github.com:org/repo?ref=main
`, false /* permissive */)

	fetchReached := false
	old := kustomizeFetchHook
	kustomizeFetchHook = func() { fetchReached = true }
	defer func() { kustomizeFetchHook = old }()

	_, err := src.Render(context.Background(), ModeSeed, "ns")

	if fetchReached {
		t.Fatal("kust.Run was reached despite SSH ref (classification must fire in all modes)")
	}
	if err == nil {
		t.Fatal("expected classification rejection for SSH ref in permissive mode, got nil")
	}
	// Error must mention the reason but NOT echo the raw SSH URL.
	if strings.Contains(err.Error(), "git@") || strings.Contains(err.Error(), "github.com") {
		t.Errorf("error leaks SSH URL: %v", err)
	}
}

// TestTask4_EmbeddedCredentialRefRejectedInPermissiveMode verifies that a URL
// embedding credentials (user:token@host) is rejected by classification even in
// permissive mode. The kustomizeFetchHook must NOT fire.
func TestTask4_EmbeddedCredentialRefRejectedInPermissiveMode(t *testing.T) {
	src := buildKustomizeRootWith(t, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://user:SECRETTOKEN@github.com/org/repo?ref=main
`, false /* permissive */)

	fetchReached := false
	old := kustomizeFetchHook
	kustomizeFetchHook = func() { fetchReached = true }
	defer func() { kustomizeFetchHook = old }()

	_, err := src.Render(context.Background(), ModeSeed, "ns")

	if fetchReached {
		t.Fatal("kust.Run was reached despite embedded-credential URL (classification must fire in all modes)")
	}
	if err == nil {
		t.Fatal("expected classification rejection for embedded-credential URL in permissive mode, got nil")
	}
	// Sanitized: must not leak the credential.
	if strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Errorf("error leaks credential: %v", err)
	}
}

// TestTask4_PermissiveModeUnpinnedTagAllowed verifies that in permissive mode
// a remote reference pinned to a tag (not SHA) does NOT cause a preflight
// rejection — it passes through to krusty. The fetch hook MUST fire (positive control).
func TestTask4_PermissiveModeUnpinnedTagAllowed(t *testing.T) {
	src := buildKustomizeRootWith(t, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://unreachable.invalid/org/repo?ref=v1.2.3
`, false /* permissive */)

	fetchReached := false
	old := kustomizeFetchHook
	kustomizeFetchHook = func() { fetchReached = true }
	defer func() { kustomizeFetchHook = old }()

	// Render will fail (kust.Run can't reach unreachable.invalid) but the hook
	// must have fired before that network failure.
	src.Render(context.Background(), ModeSeed, "ns") //nolint:errcheck

	if !fetchReached {
		t.Fatal("kustomizeFetchHook was NOT called for permissive mode with a valid HTTPS remote ref — classification is blocking when it should not")
	}
}

// TestTask4_LocalOnlyTreeUnchanged verifies that a local-only kustomization
// (no remote refs) still renders successfully in both modes. This is the
// regression guard: adding preflight must not break local trees.
func TestTask4_LocalOnlyTreeUnchanged(t *testing.T) {
	// Use the shared testdata fixture (local-only: no remote refs in testdata/kustomize)
	ms, err := newKustomize(t).Render(context.Background(), ModeSeed, "ns")
	if err != nil {
		t.Fatalf("Render (local-only, seed): %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("expected non-empty manifest list for local-only kustomize tree")
	}
	// Verify OriginUpstream tagging is preserved.
	found := false
	for _, m := range ms {
		if m.Origin == manifest.OriginUpstream {
			found = true
			break
		}
	}
	if !found {
		t.Error("no manifest with OriginUpstream found — ApplyNamespace or Parse may have changed")
	}
}

// TestTask4_StrictModeLocalOnlyTreeUnchanged verifies that enabling strict mode
// on a local-only tree (no remote refs) does not cause a preflight rejection.
func TestTask4_StrictModeLocalOnlyTreeUnchanged(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := fakeKustomizeSpec(root)
	src := &kustomizeSource{
		spec:                        &spec,
		resolver:                    fakeRootResolver{baseDir: root},
		requireSHAPinnedRemoteBases: true, // strict — but no remotes, so should pass
	}
	_, err := src.Render(context.Background(), ModeSeed, "ns")
	if err != nil {
		t.Fatalf("Render (strict, local-only): unexpected preflight error: %v", err)
	}
}

// TestTask4_ClassificationRunsBeforeKrustyInPermissiveMode is an omnibus check:
// the tree-walk (classification step) must fire in permissive mode, as evidenced
// by the fact that a cycle in the local tree is still detected even with
// requireSHAPinnedRemoteBases=false. The cycle error comes from treeWalkKustomize,
// so if treeWalk is skipped this test would fail silently via krusty trying to
// build a cyclic tree.
func TestTask4_ClassificationRunsBeforeKrustyInPermissiveMode(t *testing.T) {
	root := buildCyclicTree(t) // root → child → root (cycle)
	spec := v1alpha1.KustomizeSource{URL: root, SeedPath: "", ShootPath: ""}
	src := &kustomizeSource{
		spec:                        &spec,
		resolver:                    fakeRootResolver{baseDir: root},
		requireSHAPinnedRemoteBases: false, // permissive
	}

	fetchReached := false
	old := kustomizeFetchHook
	kustomizeFetchHook = func() { fetchReached = true }
	defer func() { kustomizeFetchHook = old }()

	_, err := src.Render(context.Background(), ModeSeed, "ns")

	if fetchReached {
		t.Fatal("kust.Run reached despite cycle in local tree (tree-walk must run before krusty)")
	}
	if err == nil {
		t.Fatal("expected cycle detection error in permissive mode, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") && !strings.Contains(err.Error(), "preflight") {
		t.Errorf("expected cycle/preflight error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Task 4 — 4.3 REFACTOR: preflightIsolationRoot unit tests
// ---------------------------------------------------------------------------

// TestPreflightIsolationRoot verifies that preflightIsolationRoot correctly
// strips the subPath suffix from the resolved root to compute the clone root.
func TestPreflightIsolationRoot(t *testing.T) {
	sep := string(filepath.Separator)
	cases := []struct {
		name     string
		root     string
		subPath  string
		wantRoot string
	}{
		{
			name:     "empty subPath returns root unchanged",
			root:     sep + "tmp" + sep + "clone",
			subPath:  "",
			wantRoot: sep + "tmp" + sep + "clone",
		},
		{
			name:     "single-level subPath stripped",
			root:     sep + "tmp" + sep + "clone" + sep + "seed",
			subPath:  "seed",
			wantRoot: sep + "tmp" + sep + "clone",
		},
		{
			name:     "multi-level subPath stripped",
			root:     sep + "tmp" + sep + "clone" + sep + "overlays" + sep + "seed",
			subPath:  "overlays" + sep + "seed",
			wantRoot: sep + "tmp" + sep + "clone",
		},
		{
			name:     "subPath with trailing slash stripped correctly",
			root:     sep + "tmp" + sep + "clone" + sep + "seed",
			subPath:  "seed" + sep,
			wantRoot: sep + "tmp" + sep + "clone",
		},
		{
			name:     "root does not end with subPath — fallback to root",
			root:     sep + "tmp" + sep + "clone",
			subPath:  "shoot", // root doesn't end with "shoot"
			wantRoot: sep + "tmp" + sep + "clone",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := preflightIsolationRoot(tc.root, tc.subPath)
			if filepath.Clean(got) != filepath.Clean(tc.wantRoot) {
				t.Errorf("preflightIsolationRoot(%q, %q) = %q, want %q", tc.root, tc.subPath, got, tc.wantRoot)
			}
		})
	}
}
