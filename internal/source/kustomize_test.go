// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"
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
	// Scheme-less github.com shorthand reaches git (not krusty's HTTP-first file
	// loader), so in strict mode it flows to the SHA-pin loop and is rejected for
	// the unpinned tag ref — exactly the assertion this test encodes. (An
	// https:// spelling would instead be rejected earlier by the resource-like
	// HTTPS rule, which is covered by the dedicated rejection tests.)
	src := buildKustomizeRootWith(t, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - github.com/org/repo?ref=v1.2.3
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

// TestTask4_PermissiveModeAllowedTreePassesPreflight verifies that in permissive
// mode a remote reference pinned to a tag (not SHA) passes the preflight and
// does NOT cause a classification rejection. Strategy: use a local overlay with
// a valid HTTPS tag ref and verify via krustyRunHook that krusty is actually
// invoked (no pre-krusty rejection). The hook makes krust.Run return a
// synthetic error (no network needed); we only check that the error is a
// krusty-level error, not a preflight rejection.
func TestTask4_PermissiveModeAllowedTreePassesPreflight(t *testing.T) {
	src := buildKustomizeRootWith(t, `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/repo?ref=v1.2.3
`, false /* permissive */)

	fetchReached := false
	oldFetch := kustomizeFetchHook
	kustomizeFetchHook = func() { fetchReached = true }
	defer func() { kustomizeFetchHook = oldFetch }()

	// Inject a synthetic krusty error so krust.Run never goes to the network.
	syntheticErr := errors.New("synthetic-krusty-failure")
	oldRun := krustyRunHook
	krustyRunHook = syntheticErr
	defer func() { krustyRunHook = oldRun }()

	_, err := src.Render(context.Background(), ModeSeed, "ns")

	// The fetch hook must have fired — preflight did not block the tag ref.
	if !fetchReached {
		t.Fatal("kustomizeFetchHook was NOT called in permissive mode — preflight incorrectly blocked a valid HTTPS tag ref")
	}
	// The error must be a krusty-level (sanitized) render error, not a preflight error.
	if err == nil {
		t.Fatal("expected a render error (from injected krusty hook), got nil")
	}
	if strings.Contains(err.Error(), "preflight") || strings.Contains(err.Error(), "SHA") || strings.Contains(err.Error(), "pin") {
		t.Errorf("error looks like a preflight rejection but should be a krusty error: %v", err)
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
// Task 4 Fix-round-1 — RED tests
// ---------------------------------------------------------------------------

// buildOverlayTree creates a realistic clone-like directory tree:
//
//	cloneDir/
//	  base/
//	    kustomization.yaml   (resources: [upstream-cm.yaml])
//	    upstream-cm.yaml
//	  overlay/
//	    kustomization.yaml   (resources: [../base, <remoteRef>])
//
// The fakeRootResolver is configured with baseDir=cloneDir and subPath="overlay",
// so Render() receives cloneDir+"/overlay" as the krusty root and should walk
// starting at cloneDir+"/overlay" (not at cloneDir).
func buildOverlayTreeWithRemote(t *testing.T, remoteRef string) *kustomizeSource {
	t.Helper()
	cloneDir := t.TempDir()

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(filepath.Join(cloneDir, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - upstream-cm.yaml
`)
	write(filepath.Join(cloneDir, "base", "upstream-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: upstream-cm
data:
  key: value
`)
	write(filepath.Join(cloneDir, "overlay", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../base
  - `+remoteRef+`
`)

	spec := v1alpha1.KustomizeSource{URL: cloneDir, SeedPath: "overlay", ShootPath: "overlay"}
	src := &kustomizeSource{
		spec:                        &spec,
		resolver:                    fakeRootResolver{baseDir: cloneDir},
		requireSHAPinnedRemoteBases: false,
	}
	return src
}

// buildOverlayTreeNoRemote creates the same overlay structure but with no remote ref.
// Used to test that the positive render path works correctly with a subpath overlay.
func buildOverlayTreeNoRemote(t *testing.T) (cloneDir string, src *kustomizeSource) {
	t.Helper()
	cloneDir = t.TempDir()

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(filepath.Join(cloneDir, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - upstream-cm.yaml
`)
	write(filepath.Join(cloneDir, "base", "upstream-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: upstream-cm
data:
  key: value
`)
	write(filepath.Join(cloneDir, "overlay", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../base
`)

	spec := v1alpha1.KustomizeSource{URL: cloneDir, SeedPath: "overlay", ShootPath: "overlay"}
	src = &kustomizeSource{
		spec:                        &spec,
		resolver:                    fakeRootResolver{baseDir: cloneDir},
		requireSHAPinnedRemoteBases: false,
	}
	return cloneDir, src
}

// TestTask4Fix_P1_SSHRefInOverlayRejectedWithSubPath is the RED test for P1:
// an SSH ref inside an overlay (which lives beneath the isolationRoot) must be
// rejected even when subPath is non-empty. Bug: the current code walks from the
// isolationRoot (cloneDir), not the overlay (cloneDir/overlay), so it misses the
// SSH ref entirely and the preflight passes.
func TestTask4Fix_P1_SSHRefInOverlayRejectedWithSubPath(t *testing.T) {
	src := buildOverlayTreeWithRemote(t, "git@github.com:org/repo?ref=main")
	src.requireSHAPinnedRemoteBases = false // permissive — but SSH must still be rejected

	fetchReached := false
	old := kustomizeFetchHook
	kustomizeFetchHook = func() { fetchReached = true }
	defer func() { kustomizeFetchHook = old }()

	_, err := src.Render(context.Background(), ModeSeed, "ns")

	if fetchReached {
		t.Fatal("kust.Run reached despite SSH ref in overlay (classification must inspect overlay, not parent)")
	}
	if err == nil {
		t.Fatal("expected SSH rejection for overlay with subPath, got nil — P1 bug: preflight scanned parent instead of overlay")
	}
}

// TestTask4Fix_P1_StrictModeOverlayRefRejectedWithSubPath verifies that in strict
// mode, an unpinned remote ref inside an overlay (with subPath) is caught.
// Bug: walking from isolationRoot instead of overlay misses the ref.
func TestTask4Fix_P1_StrictModeOverlayRefRejectedWithSubPath(t *testing.T) {
	// Scheme-less github shorthand reaches the git route (not krusty's HTTP-first
	// file loader), so it flows to the SHA-pin loop; an unpinned tag is then
	// rejected for being non-SHA-pinned (the assertion below). An https:// form
	// here would instead be rejected earlier by the resource-like-HTTPS rule.
	src := buildOverlayTreeWithRemote(t, "github.com/org/repo?ref=v1.2.3")
	src.requireSHAPinnedRemoteBases = true // strict

	fetchReached := false
	old := kustomizeFetchHook
	kustomizeFetchHook = func() { fetchReached = true }
	defer func() { kustomizeFetchHook = old }()

	_, err := src.Render(context.Background(), ModeSeed, "ns")

	if fetchReached {
		t.Fatal("kust.Run reached before SHA-gate (zero-fetch invariant violated for overlay with subPath)")
	}
	if err == nil {
		t.Fatal("expected SHA-pin rejection for overlay with subPath in strict mode, got nil — P1 bug")
	}
	if !strings.Contains(err.Error(), "SHA") && !strings.Contains(err.Error(), "pin") {
		t.Errorf("expected SHA-pin error, got: %v", err)
	}
}

// TestTask4Fix_P1_OverlayBeneathParentNoKustomization verifies regression (a):
// an overlay beneath a parent directory that has NO kustomization must still
// have its own refs correctly classified/gated.
func TestTask4Fix_P1_OverlayBeneathParentNoKustomization(t *testing.T) {
	cloneDir := t.TempDir()

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// cloneDir has NO kustomization.yaml — only the overlay does.
	write(filepath.Join(cloneDir, "overlay", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - git@github.com:org/repo?ref=main
`)

	spec := v1alpha1.KustomizeSource{URL: cloneDir, SeedPath: "overlay", ShootPath: "overlay"}
	src := &kustomizeSource{
		spec:                        &spec,
		resolver:                    fakeRootResolver{baseDir: cloneDir},
		requireSHAPinnedRemoteBases: false,
	}

	fetchReached := false
	old := kustomizeFetchHook
	kustomizeFetchHook = func() { fetchReached = true }
	defer func() { kustomizeFetchHook = old }()

	_, err := src.Render(context.Background(), ModeSeed, "ns")

	if fetchReached {
		t.Fatal("kust.Run reached despite SSH ref — parent had no kustomization, overlay was not inspected")
	}
	if err == nil {
		t.Fatal("expected rejection of SSH ref in overlay beneath a parent with no kustomization, got nil")
	}
}

// TestTask4Fix_P1_OverlayBeneathParentWithUnrelatedKustomization verifies regression (b):
// an overlay beneath a parent that has an UNRELATED kustomization (local-only, valid)
// must not cause the parent's kustomization to incorrectly reject the overlay.
func TestTask4Fix_P1_OverlayBeneathParentWithUnrelatedKustomization(t *testing.T) {
	cloneDir := t.TempDir()

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The parent cloneDir has its own kustomization — local-only, valid.
	write(filepath.Join(cloneDir, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ./some-local-dir
`)
	// The overlay has only local refs — should render fine.
	write(filepath.Join(cloneDir, "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	write(filepath.Join(cloneDir, "base", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
`)
	write(filepath.Join(cloneDir, "overlay", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../base
`)

	spec := v1alpha1.KustomizeSource{URL: cloneDir, SeedPath: "overlay", ShootPath: "overlay"}
	src := &kustomizeSource{
		spec:                        &spec,
		resolver:                    fakeRootResolver{baseDir: cloneDir},
		requireSHAPinnedRemoteBases: false,
	}

	_, err := src.Render(context.Background(), ModeSeed, "ns")
	if err != nil {
		t.Fatalf("expected successful render for overlay beneath parent with unrelated kustomization, got: %v", err)
	}
}

// TestTask4Fix_P2_PermissiveModeAllowedTreeRendersSuccessfully replaces
// TestTask4_PermissiveModeUnpinnedTagAllowed. Instead of contacting
// unreachable.invalid and discarding the error, this test uses a deterministic
// local fixture to prove the allowed-tree path actually renders successfully.
//
// Strategy: build a local clone tree with an overlay that has only valid local
// refs. This proves that permissive mode does not block a legitimate local-only
// tree, and the render returns real manifests — not just that a hook fired.
func TestTask4Fix_P2_PermissiveModeAllowedTreeRendersSuccessfully(t *testing.T) {
	_, src := buildOverlayTreeNoRemote(t)

	ms, err := src.Render(context.Background(), ModeSeed, "ns")
	if err != nil {
		t.Fatalf("Render (permissive, local-only overlay): unexpected error: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("Render returned empty manifest list — expected at least one manifest")
	}
	found := false
	for _, m := range ms {
		if m.Unstructured.GetKind() == "ConfigMap" && m.Unstructured.GetName() == "upstream-cm" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected ConfigMap/upstream-cm in rendered manifests, not found")
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

// ---------------------------------------------------------------------------
// Boundary isolation with an in-repo //root subpath (finding #4)
// ---------------------------------------------------------------------------
//
// When the source URL carries a `//root` subpath (e.g.
// github.com/org/repo//stacks/app?ref=<sha>), the RootResolver returns
// cloneDir/<rootSubPath>/<subPath>. krusty receives that as its entry point,
// but the isolation boundary for local path/symlink checks must be the CLONE
// root (cloneDir) — NOT cloneDir/<rootSubPath> and NOT the overlay dir —
// because a kustomize overlay legitimately references sibling directories that
// live anywhere within the clone (../base, ../../common).
//
// These two tests exercise the full Render() preflight with
// requireSHAPinnedRemoteBases=false (so only the boundary logic runs, not the
// SHA gate). The sibling-reach refs are LOCAL, so classification is
// refKindLocal and only the boundary checks fire. fakeRootResolver parses the
// URL's //root via splitRef, so the resolved root matches the on-disk layout
// cloneDir/stacks/app/overlay.

// buildRootSubPathTree writes a clone tree rooted at cloneDir with an in-repo
// //root of "stacks/app" and a per-mode subPath of "overlay", plus a sibling
// "common" directory at the clone root. The overlay's single local ref is
// overlayRef. It returns a kustomizeSource whose URL encodes the //root so the
// resolver reproduces the production layout.
func buildRootSubPathTree(t *testing.T, overlayRef string) *kustomizeSource {
	t.Helper()
	cloneDir := t.TempDir()

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Sibling base at the CLONE root (above the in-repo //root "stacks/app").
	write(filepath.Join(cloneDir, "common", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	write(filepath.Join(cloneDir, "common", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: common-cm
`)

	// The overlay lives at cloneDir/stacks/app/overlay (rootSubPath=stacks/app,
	// subPath=overlay). Its single ref is overlayRef (a local relative path).
	write(filepath.Join(cloneDir, "stacks", "app", "overlay", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - `+overlayRef+`
`)

	// URL encodes the in-repo //root so splitRef yields rootSubPath="stacks/app".
	// The SHA in ?ref= only needs to be present for splitRef to parse the root;
	// it is not SHA-gated here because requireSHAPinnedRemoteBases is false and
	// the overlay ref is local.
	url := "github.com/org/repo//stacks/app?ref=0123456789abcdef0123456789abcdef01234567"
	spec := v1alpha1.KustomizeSource{URL: url, SeedPath: "overlay", ShootPath: "overlay"}
	return &kustomizeSource{
		spec:                        &spec,
		resolver:                    fakeRootResolver{baseDir: cloneDir},
		requireSHAPinnedRemoteBases: false,
	}
}

// TestBoundaryIsolation_InCloneSiblingReachAccepted verifies that an overlay
// beneath an in-repo //root can reach a sibling directory that lives at the
// clone root (../../../common from cloneDir/stacks/app/overlay == cloneDir/common)
// without tripping the path-boundary check. The isolation root is stripped back
// to cloneDir, so this reference stays inside the clone and the render succeeds.
func TestBoundaryIsolation_InCloneSiblingReachAccepted(t *testing.T) {
	// From cloneDir/stacks/app/overlay: ../=app, ../../=stacks, ../../../=cloneDir.
	src := buildRootSubPathTree(t, "../../../common")

	ms, err := src.Render(context.Background(), ModeSeed, "ns")
	if err != nil {
		t.Fatalf("expected in-clone sibling reach (../../../common) to be accepted, got: %v", err)
	}
	found := false
	for _, m := range ms {
		if m.Unstructured.GetKind() == "ConfigMap" && m.Unstructured.GetName() == "common-cm" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected ConfigMap/common-cm in rendered manifests, not found")
	}
}

// TestBoundaryIsolation_GenuineEscapeRejected verifies that an overlay beneath
// an in-repo //root that references a path genuinely ESCAPING above the clone
// root is still rejected by the preflight boundary check. ../../../../escape
// from cloneDir/stacks/app/overlay resolves to the PARENT of cloneDir — outside
// the clone — and must be refused before any fetch/build.
func TestBoundaryIsolation_GenuineEscapeRejected(t *testing.T) {
	// ../../../../ from cloneDir/stacks/app/overlay == parent-of-cloneDir.
	src := buildRootSubPathTree(t, "../../../../escape")

	fetchReached := false
	old := kustomizeFetchHook
	kustomizeFetchHook = func() { fetchReached = true }
	defer func() { kustomizeFetchHook = old }()

	_, err := src.Render(context.Background(), ModeSeed, "ns")

	if fetchReached {
		t.Fatal("kust.Run reached despite a path escaping the clone root")
	}
	if err == nil {
		t.Fatal("expected rejection of a path escaping the clone root, got nil")
	}
}
