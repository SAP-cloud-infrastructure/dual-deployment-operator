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
)

// ---------------------------------------------------------------------------
// 1.1  classifier tests (RED before remotepolicy.go exists)
// ---------------------------------------------------------------------------

func TestClassifyKustomizeReference_LocalPath(t *testing.T) {
	cases := []struct {
		raw  string
		want refKind
	}{
		{"./base", refKindLocal},
		{"../upstream", refKindLocal},
		{"base", refKindLocal},
		{"some/nested/path", refKindLocal},
		{".", refKindLocal},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, _, err := classifyKustomizeReference(tc.raw)
			if err != nil {
				t.Fatalf("classifyKustomizeReference(%q) returned unexpected error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("classifyKustomizeReference(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestClassifyKustomizeReference_RemoteGitDirectory_HTTPS(t *testing.T) {
	cases := []struct {
		raw string
	}{
		{"https://github.com/org/repo?ref=v1.2.3"},
		{"https://github.com/org/repo?ref=abc1234"},
		{"https://github.com/org/repo//path/to/base?ref=main"},
		{"https://gitlab.com/org/repo?ref=v2.0.0"},
		{"https://example.com/org/repo//subdir?ref=0123456789abcdef0123456789abcdef01234567"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, ref, err := classifyKustomizeReference(tc.raw)
			if err != nil {
				t.Fatalf("classifyKustomizeReference(%q) returned unexpected error: %v", tc.raw, err)
			}
			if got != refKindRemoteGitDir {
				t.Errorf("classifyKustomizeReference(%q) = %v, want refKindRemoteGitDir", tc.raw, got)
			}
			if ref == "" {
				t.Errorf("classifyKustomizeReference(%q) returned empty ref", tc.raw)
			}
		})
	}
}

func TestClassifyKustomizeReference_RemoteGitDirectory_GitHubShorthand(t *testing.T) {
	// GitHub shorthand: github.com/org/repo or github.com/org/repo//path — no scheme
	cases := []string{
		"github.com/org/repo?ref=v1",
		"github.com/org/repo//subpath?ref=abc1234",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			got, ref, err := classifyKustomizeReference(raw)
			if err != nil {
				t.Fatalf("classifyKustomizeReference(%q) returned unexpected error: %v", raw, err)
			}
			if got != refKindRemoteGitDir {
				t.Errorf("classifyKustomizeReference(%q) = %v, want refKindRemoteGitDir", raw, got)
			}
			if ref == "" {
				t.Errorf("classifyKustomizeReference(%q) returned empty ref", raw)
			}
		})
	}
}

func TestClassifyKustomizeReference_RemoteFile(t *testing.T) {
	cases := []string{
		"https://example.com/path/to/resource.yaml",
		"https://example.com/path/to/resource.yml",
		"https://example.com/path/to/resource.json",
		"https://raw.githubusercontent.com/org/repo/main/config.yaml",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			got, _, err := classifyKustomizeReference(raw)
			if err != nil {
				t.Fatalf("classifyKustomizeReference(%q) returned unexpected error: %v", raw, err)
			}
			if got != refKindRemoteFile {
				t.Errorf("classifyKustomizeReference(%q) = %v, want refKindRemoteFile", raw, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Rejection cases
// ---------------------------------------------------------------------------

func TestClassifyKustomizeReference_RejectSSH(t *testing.T) {
	cases := []string{
		"git@github.com:org/repo?ref=v1",
		"ssh://git.example.com/org/repo?ref=v1",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(raw)
			if err == nil {
				t.Errorf("classifyKustomizeReference(%q): expected rejection (SSH), got nil error", raw)
			}
			// Error must not echo any credential/raw URL content that could leak
			if strings.Contains(err.Error(), "git@") || strings.Contains(err.Error(), "@") {
				t.Errorf("credential leak in error for %q: %v", raw, err)
			}
		})
	}
}

func TestClassifyKustomizeReference_RejectUserinfo(t *testing.T) {
	cases := []string{
		"https://user:supersecret@github.com/org/repo?ref=v1",
		"https://token@gitlab.com/org/repo?ref=v1",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(raw)
			if err == nil {
				t.Errorf("classifyKustomizeReference(%q): expected rejection (userinfo), got nil error", raw)
			}
			// Error must not echo the credential
			if strings.Contains(err.Error(), "supersecret") || strings.Contains(err.Error(), "token@") {
				t.Errorf("credential leak in error for %q: %v", raw, err)
			}
		})
	}
}

func TestClassifyKustomizeReference_RejectURLFragment(t *testing.T) {
	cases := []string{
		"https://github.com/org/repo?ref=v1#section",
		"https://github.com/org/repo#readme",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(raw)
			if err == nil {
				t.Errorf("classifyKustomizeReference(%q): expected rejection (fragment), got nil error", raw)
			}
		})
	}
}

func TestClassifyKustomizeReference_RejectMissingRef_OnRemote(t *testing.T) {
	// A multi-segment path without // that could be a remote but has no ?ref= is ambiguous
	cases := []string{
		"https://github.com/org/repo/subdir", // no ?ref=, multi-segment path after repo — ambiguous
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(raw)
			if err == nil {
				t.Errorf("classifyKustomizeReference(%q): expected rejection (ambiguous no-// multi-segment or missing ?ref=), got nil", raw)
			}
		})
	}
}

func TestClassifyKustomizeReference_SanitizedErrors(t *testing.T) {
	// The raw URL / credentials must never appear verbatim in any error message.
	cases := []struct {
		raw     string
		mustNot string // substring that must NOT appear in error
	}{
		{
			raw:     "https://user:s3cr3t@github.com/org/repo?ref=v1",
			mustNot: "s3cr3t",
		},
		{
			raw:     "git@github.com:org/secret-repo.git?ref=v1",
			mustNot: "secret-repo",
		},
		// Finding 1: malformed credential-bearing URLs must also not leak
		{
			raw:     "https://user:SECRET@host/%zz",
			mustNot: "SECRET",
		},
		// Finding 1: SCP-style with credential must not leak username or path
		{
			raw:     "user:SECRET@host:repo",
			mustNot: "SECRET",
		},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(tc.raw)
			if err == nil {
				t.Fatalf("classifyKustomizeReference(%q): expected an error (reject), got nil", tc.raw)
			}
			if strings.Contains(err.Error(), tc.mustNot) {
				t.Errorf("sanitization failure: error contains %q for input %q: %v", tc.mustNot, tc.raw, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Finding 3: git:: prefix stripping — SSH still rejected, HTTPS accepted
// ---------------------------------------------------------------------------

func TestClassifyKustomizeReference_GitDoubleColon_SSHRejected(t *testing.T) {
	cases := []string{
		"git::ssh://git@host/repo?ref=v1",
		"git::git@github.com:org/repo?ref=v1",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(raw)
			if err == nil {
				t.Errorf("classifyKustomizeReference(%q): expected SSH rejection after git:: strip, got nil", raw)
			}
		})
	}
}

func TestClassifyKustomizeReference_GitDoubleColon_HTTPSAccepted(t *testing.T) {
	cases := []struct {
		raw     string
		wantRef string
	}{
		{"git::https://github.com/org/repo?ref=v1", "v1"},
		{"git::https://gitlab.com/org/repo//path?ref=abc123", "abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, ref, err := classifyKustomizeReference(tc.raw)
			if err != nil {
				t.Fatalf("classifyKustomizeReference(%q) unexpected error: %v", tc.raw, err)
			}
			if got != refKindRemoteGitDir {
				t.Errorf("classifyKustomizeReference(%q) = %v, want refKindRemoteGitDir", tc.raw, got)
			}
			if ref != tc.wantRef {
				t.Errorf("classifyKustomizeReference(%q) ref = %q, want %q", tc.raw, ref, tc.wantRef)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Finding 4: ambiguous paths with ?ref= (multi-segment, no //) must be rejected
// ---------------------------------------------------------------------------

func TestClassifyKustomizeReference_RejectAmbiguousWithRef(t *testing.T) {
	cases := []string{
		// multi-segment path after org/repo, no // separator, even though ?ref= present
		"https://example.com/org/repo/subdir?ref=v1",
		"https://github.com/org/repo/subdir?ref=v1",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(raw)
			if err == nil {
				t.Errorf("classifyKustomizeReference(%q): expected rejection (ambiguous path, no // boundary), got nil", raw)
			}
		})
	}
}

func TestClassifyKustomizeReference_AcceptDoubleSlashWithRef(t *testing.T) {
	// double-slash boundary makes the repo/path split unambiguous
	cases := []struct {
		raw     string
		wantRef string
	}{
		{"https://example.com/org/repo//subdir?ref=v1", "v1"},
		{"https://github.com/org/repo//subdir?ref=v1", "v1"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, ref, err := classifyKustomizeReference(tc.raw)
			if err != nil {
				t.Fatalf("classifyKustomizeReference(%q) unexpected error: %v", tc.raw, err)
			}
			if got != refKindRemoteGitDir {
				t.Errorf("classifyKustomizeReference(%q) = %v, want refKindRemoteGitDir", tc.raw, got)
			}
			if ref != tc.wantRef {
				t.Errorf("classifyKustomizeReference(%q) ref = %q, want %q", tc.raw, ref, tc.wantRef)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Finding 5: GitHub shorthand — exact ref extraction, no ?version= fallback
// ---------------------------------------------------------------------------

func TestClassifyKustomizeReference_GitHubShorthand_ExactRef(t *testing.T) {
	cases := []struct {
		raw     string
		wantRef string
	}{
		{"github.com/org/repo?ref=v1.2.3", "v1.2.3"},
		{"github.com/org/repo//path?ref=abc1234", "abc1234"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			_, ref, err := classifyKustomizeReference(tc.raw)
			if err != nil {
				t.Fatalf("classifyKustomizeReference(%q) unexpected error: %v", tc.raw, err)
			}
			if ref != tc.wantRef {
				t.Errorf("classifyKustomizeReference(%q) ref = %q, want %q", tc.raw, ref, tc.wantRef)
			}
		})
	}
}

func TestClassifyKustomizeReference_GitHubShorthand_MissingRef(t *testing.T) {
	cases := []string{
		"github.com/org/repo",                 // no query at all
		"github.com/org/repo?version=v1",      // version= not accepted (only ref=)
		"github.com/org/repo?ref=",            // empty ref=
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(raw)
			if err == nil {
				t.Errorf("classifyKustomizeReference(%q): expected rejection (missing ?ref=), got nil", raw)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Finding 6: deduplication — ordered set semantics
// ---------------------------------------------------------------------------

func buildTreeWithDuplicateRemotes(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// root lists the same remote twice in resources
	write(filepath.Join(root, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/repo?ref=v1.0.0
  - ./child
  - https://github.com/org/repo?ref=v1.0.0
`)
	// child also references the same remote
	write(filepath.Join(root, "child", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/repo?ref=v1.0.0
`)
	return root
}

func TestTreeWalkKustomize_Deduplicates(t *testing.T) {
	root := buildTreeWithDuplicateRemotes(t)

	remotes, err := treeWalkKustomize(root)
	if err != nil {
		t.Fatalf("treeWalkKustomize: %v", err)
	}
	if len(remotes) != 1 {
		t.Fatalf("expected 1 deduplicated remote, got %d: %v", len(remotes), remotes)
	}
	if remotes[0].Raw != "https://github.com/org/repo?ref=v1.0.0" {
		t.Errorf("unexpected remote ref: %q", remotes[0].Raw)
	}
}

func buildTreeWithSharedLocalSubtree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// root → child-a and child-b, both of which reference the same remote
	write(filepath.Join(root, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ./child-a
  - ./child-b
`)
	write(filepath.Join(root, "child-a", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/shared?ref=v2.0.0
`)
	write(filepath.Join(root, "child-b", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/shared?ref=v2.0.0
`)
	return root
}

func TestTreeWalkKustomize_DeduplicatesSharedSubtree(t *testing.T) {
	root := buildTreeWithSharedLocalSubtree(t)

	remotes, err := treeWalkKustomize(root)
	if err != nil {
		t.Fatalf("treeWalkKustomize: %v", err)
	}
	if len(remotes) != 1 {
		t.Fatalf("expected 1 deduplicated remote from shared subtree, got %d: %v", len(remotes), remotes)
	}
	if remotes[0].Raw != "https://github.com/org/shared?ref=v2.0.0" {
		t.Errorf("unexpected remote ref: %q", remotes[0].Raw)
	}
}

func buildTreeWithOrderedMultipleRemotes(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// first remote is in root, second in child — order must be preserved
	write(filepath.Join(root, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/first?ref=v1.0.0
  - ./child
`)
	write(filepath.Join(root, "child", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/second?ref=v2.0.0
`)
	return root
}

func TestTreeWalkKustomize_PreservesFirstDiscoveryOrder(t *testing.T) {
	root := buildTreeWithOrderedMultipleRemotes(t)

	remotes, err := treeWalkKustomize(root)
	if err != nil {
		t.Fatalf("treeWalkKustomize: %v", err)
	}
	if len(remotes) != 2 {
		t.Fatalf("expected 2 ordered remotes, got %d: %v", len(remotes), remotes)
	}
	if remotes[0].Raw != "https://github.com/org/first?ref=v1.0.0" {
		t.Errorf("remotes[0] = %q, want first", remotes[0].Raw)
	}
	if remotes[1].Raw != "https://github.com/org/second?ref=v2.0.0" {
		t.Errorf("remotes[1] = %q, want second", remotes[1].Raw)
	}
}

// ---------------------------------------------------------------------------
// Finding 2: boundary checks on files + symlinks
// ---------------------------------------------------------------------------

func TestTreeWalkKustomize_RejectsFileEscapeViaSymlink(t *testing.T) {
	if !supportsSymlinks(t) {
		t.Skip("symlinks not supported")
	}
	root := t.TempDir()
	outside := t.TempDir()

	// Create an outside file
	if err := os.WriteFile(filepath.Join(outside, "outside.yaml"), []byte("kind: ConfigMap\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Symlink inside root pointing to outside file
	if err := os.Symlink(filepath.Join(outside, "outside.yaml"), filepath.Join(root, "escaping.yaml")); err != nil {
		t.Fatal(err)
	}
	// kustomization.yaml references the symlink (which resolves outside root)
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ./escaping.yaml
`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := treeWalkKustomize(root)
	if err == nil {
		t.Fatal("treeWalkKustomize: expected boundary violation for symlinked file escape, got nil")
	}
	if !strings.Contains(err.Error(), "boundary") && !strings.Contains(err.Error(), "escape") {
		t.Errorf("expected boundary error, got: %v", err)
	}
}

func TestTreeWalkKustomize_RejectsFileTraversalEscape(t *testing.T) {
	root := t.TempDir()
	// kustomization.yaml references a file outside root via ../
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../outside.yaml
`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := treeWalkKustomize(root)
	if err == nil {
		t.Fatal("treeWalkKustomize: expected boundary violation for ../ file escape, got nil")
	}
	if !strings.Contains(err.Error(), "boundary") && !strings.Contains(err.Error(), "escape") {
		t.Errorf("expected boundary error, got: %v", err)
	}
}

// supportsSymlinks returns true if symlinks can be created in t.TempDir().
func supportsSymlinks(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	err := os.Symlink(dir, filepath.Join(dir, "test-link"))
	return err == nil
}

// ---------------------------------------------------------------------------
// 1.3  tree-walk tests (RED before treeWalkKustomize exists)
// ---------------------------------------------------------------------------

// buildKustomizeTree creates a temporary directory tree for tree-walk tests.
// It returns the root dir.
//
// Structure:
//
//	root/
//	  kustomization.yaml      – resources: [./child, https://github.com/org/r?ref=v1]
//	  child/
//	    kustomization.yaml    – resources: [./leaf]
//	  leaf/
//	    kustomization.yaml    – resources: [] (no further refs)
func buildSimpleLocalTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	write := func(path string, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(filepath.Join(root, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ./child
  - https://github.com/org/repo?ref=v1.0.0
`)
	write(filepath.Join(root, "child", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ./leaf
`)
	write(filepath.Join(root, "child", "leaf", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`)

	return root
}

func TestTreeWalkKustomize_CollectsRemoteRefs(t *testing.T) {
	root := buildSimpleLocalTree(t)

	remotes, err := treeWalkKustomize(root)
	if err != nil {
		t.Fatalf("treeWalkKustomize: %v", err)
	}

	if len(remotes) != 1 {
		t.Fatalf("expected 1 remote reference, got %d: %v", len(remotes), remotes)
	}
	if remotes[0].Raw != "https://github.com/org/repo?ref=v1.0.0" {
		t.Errorf("unexpected remote ref: %q", remotes[0].Raw)
	}
	if remotes[0].Kind != refKindRemoteGitDir {
		t.Errorf("unexpected kind: %v", remotes[0].Kind)
	}
}

// buildCyclicTree creates a tree with a cycle: root → child → root (via relative path).
// Since the walker must detect cycles by canonical path, we simulate by having root
// reference a subdirectory that re-references the same root via ../../.
// (A genuine cycle means the same canonical dir appears twice in the walk stack.)
func buildCyclicTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	rootKust := filepath.Join(root, "kustomization.yaml")
	childKust := filepath.Join(root, "child", "kustomization.yaml")

	if err := os.MkdirAll(filepath.Dir(childKust), 0o700); err != nil {
		t.Fatal(err)
	}
	// root references ./child
	if err := os.WriteFile(rootKust, []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ./child
`), 0o600); err != nil {
		t.Fatal(err)
	}
	// child references ../  (back to root) — this forms a cycle
	if err := os.WriteFile(childKust, []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../
`), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTreeWalkKustomize_DetectsCycles(t *testing.T) {
	root := buildCyclicTree(t)

	_, err := treeWalkKustomize(root)
	if err == nil {
		t.Fatal("treeWalkKustomize: expected cycle detection error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected error to mention 'cycle', got: %v", err)
	}
}

// buildEscapingTree creates a tree where a resource entry tries to escape the root
// via symlink or path traversal.
func buildEscapingTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// root/kustomization.yaml references ../../outside — path traversal escape
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../outside
`), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTreeWalkKustomize_RejectsPathEscape(t *testing.T) {
	root := buildEscapingTree(t)

	_, err := treeWalkKustomize(root)
	if err == nil {
		t.Fatal("treeWalkKustomize: expected path-escape error, got nil")
	}
	if !strings.Contains(err.Error(), "escape") && !strings.Contains(err.Error(), "outside") && !strings.Contains(err.Error(), "boundary") {
		t.Errorf("expected error to mention path escape, got: %v", err)
	}
}

func buildTreeWithComponents(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// uses 'components' field instead of 'resources' for a remote ref
	write(filepath.Join(root, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
components:
  - https://github.com/org/components?ref=v2.0.0
`)
	return root
}

func TestTreeWalkKustomize_VisitsComponentsField(t *testing.T) {
	root := buildTreeWithComponents(t)

	remotes, err := treeWalkKustomize(root)
	if err != nil {
		t.Fatalf("treeWalkKustomize: %v", err)
	}
	if len(remotes) != 1 {
		t.Fatalf("expected 1 remote from components field, got %d: %v", len(remotes), remotes)
	}
	if remotes[0].Raw != "https://github.com/org/components?ref=v2.0.0" {
		t.Errorf("unexpected remote ref: %q", remotes[0].Raw)
	}
}

func buildTreeWithBasesField(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// uses the legacy 'bases' field
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
bases:
  - https://github.com/org/legacybase?ref=v3.0.0
`), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTreeWalkKustomize_VisitsBasesField(t *testing.T) {
	root := buildTreeWithBasesField(t)

	remotes, err := treeWalkKustomize(root)
	if err != nil {
		t.Fatalf("treeWalkKustomize: %v", err)
	}
	if len(remotes) != 1 {
		t.Fatalf("expected 1 remote from bases field, got %d: %v", len(remotes), remotes)
	}
	if remotes[0].Raw != "https://github.com/org/legacybase?ref=v3.0.0" {
		t.Errorf("unexpected remote ref: %q", remotes[0].Raw)
	}
}

// ---------------------------------------------------------------------------
// Round-2 review findings
// ---------------------------------------------------------------------------

// Finding 4 (round 2): GitHub shorthand ambiguous path boundary
// github.com/org/repo/subdir?ref=v1 must be rejected (same ambiguity as HTTPS).
// github.com/org/repo?ref=v1 (no subpath) must still be accepted.

func TestClassifyKustomizeReference_GitHubShorthand_RejectsAmbiguousSubdir(t *testing.T) {
	cases := []string{
		"github.com/org/repo/subdir?ref=v1",
		"github.com/org/repo/deep/nested?ref=v1",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(raw)
			if err == nil {
				t.Errorf("classifyKustomizeReference(%q): expected rejection (ambiguous shorthand path, no // boundary), got nil", raw)
			}
		})
	}
}

func TestClassifyKustomizeReference_GitHubShorthand_AcceptsUnambiguous(t *testing.T) {
	cases := []struct {
		raw     string
		wantRef string
	}{
		// bare org/repo — unambiguous (exactly 2 segments, no subpath)
		{"github.com/org/repo?ref=v1", "v1"},
		// double-slash boundary — unambiguous even with subpath
		{"github.com/org/repo//subdir?ref=v1", "v1"},
		{"github.com/org/repo//deep/nested?ref=abc1234", "abc1234"},
		// .git/ boundary — unambiguous (regression: round-3 finding)
		{"github.com/org/repo.git/subdir?ref=v1", "v1"},
		{"github.com/org/repo.git?ref=v1", "v1"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, ref, err := classifyKustomizeReference(tc.raw)
			if err != nil {
				t.Fatalf("classifyKustomizeReference(%q) unexpected error: %v", tc.raw, err)
			}
			if got != refKindRemoteGitDir {
				t.Errorf("classifyKustomizeReference(%q) = %v, want refKindRemoteGitDir", tc.raw, got)
			}
			if ref != tc.wantRef {
				t.Errorf("classifyKustomizeReference(%q) ref = %q, want %q", tc.raw, ref, tc.wantRef)
			}
		})
	}
}

// Coverage gap: RejectsFileTraversalEscape but with the outside file actually
// created, confirming the boundary check fires even when the target exists.
func TestTreeWalkKustomize_RejectsExistingFileOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	// Create the outside file so os.Stat succeeds — boundary must still be caught.
	if err := os.WriteFile(filepath.Join(outside, "outside.yaml"), []byte("kind: ConfigMap\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// kustomization.yaml references the outside file via a relative traversal.
	// We need the relative path from root to the outside file.
	rel, err := filepath.Rel(root, filepath.Join(outside, "outside.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - `+rel+`
`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, walkErr := treeWalkKustomize(root)
	if walkErr == nil {
		t.Fatal("treeWalkKustomize: expected boundary violation for existing file outside root, got nil")
	}
	if !strings.Contains(walkErr.Error(), "boundary") && !strings.Contains(walkErr.Error(), "escape") {
		t.Errorf("expected boundary error, got: %v", walkErr)
	}
}

// Coverage gap: symlinked kustomization.yaml pointing outside the root.
func TestTreeWalkKustomize_RejectsSymlinkedKustomizationFile(t *testing.T) {
	if !supportsSymlinks(t) {
		t.Skip("symlinks not supported")
	}
	root := t.TempDir()
	outside := t.TempDir()

	// Create a real kustomization.yaml outside the root.
	if err := os.WriteFile(filepath.Join(outside, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Symlink root/kustomization.yaml → outside/kustomization.yaml
	if err := os.Symlink(
		filepath.Join(outside, "kustomization.yaml"),
		filepath.Join(root, "kustomization.yaml"),
	); err != nil {
		t.Fatal(err)
	}

	_, walkErr := treeWalkKustomize(root)
	if walkErr == nil {
		t.Fatal("treeWalkKustomize: expected boundary violation for symlinked kustomization.yaml outside root, got nil")
	}
	if !strings.Contains(walkErr.Error(), "boundary") && !strings.Contains(walkErr.Error(), "escape") {
		t.Errorf("expected boundary error, got: %v", walkErr)
	}
}

// ---------------------------------------------------------------------------
// P1 regression: malformed YAML with credential-bearing URL must not leak
// ---------------------------------------------------------------------------

// TestTreeWalkKustomize_MalformedYAML_NoCredentialLeak ensures that when a
// kustomization.yaml contains malformed YAML whose parse error message might
// echo back a credential-bearing URL key, that credential is NOT included in
// the returned error.
func TestTreeWalkKustomize_MalformedYAML_NoCredentialLeak(t *testing.T) {
	root := t.TempDir()

	// Write a kustomization.yaml that is syntactically invalid YAML.
	// The content includes a credential-bearing URL that a naive %w wrapping of
	// yaml.Unmarshal could echo back verbatim in the error message.
	malformed := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  https://user:SUPERSECRET@github.com/org/repo?ref=v1:
    - invalid_yaml_structure: [unclosed
`
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(malformed), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := treeWalkKustomize(root)
	if err == nil {
		t.Fatal("expected parse error for malformed YAML, got nil")
	}
	// The raw credential must not appear in the error.
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Errorf("credential leaked in parse error: %v", err)
	}
	// The full URL (or even the hostname) must not appear.
	if strings.Contains(err.Error(), "github.com") {
		t.Errorf("URL leaked in parse error: %v", err)
	}
	// The error must still be meaningful (not silently swallowed).
	if !strings.Contains(err.Error(), "parse") && !strings.Contains(err.Error(), "YAML") && !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected a parse/YAML error message, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Task 2 — Step 2.1 RED: ref extraction and SHA predicate
// ---------------------------------------------------------------------------

// TestExtractRef_GitQueryParam tests that extractRef returns the ?ref= value
// from HTTPS git-directory references.
func TestExtractRef_GitQueryParam(t *testing.T) {
	cases := []struct {
		raw     string
		wantRef string
	}{
		{"https://github.com/org/repo?ref=v1.2.3", "v1.2.3"},
		{"https://github.com/org/repo?ref=abc1234", "abc1234"},
		{"https://github.com/org/repo//path?ref=main", "main"},
		{"https://example.com/org/repo//sub?ref=0123456789abcdef0123456789abcdef01234567", "0123456789abcdef0123456789abcdef01234567"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got := extractRef(tc.raw)
			if got != tc.wantRef {
				t.Errorf("extractRef(%q) = %q, want %q", tc.raw, got, tc.wantRef)
			}
		})
	}
}

// TestExtractRef_RawGitHubPathSegment tests ref extraction from
// raw.githubusercontent.com URLs, where the ref is the path segment
// immediately after org/repo (i.e. the 3rd path segment: /org/repo/<ref>/...).
func TestExtractRef_RawGitHubPathSegment(t *testing.T) {
	cases := []struct {
		raw     string
		wantRef string
	}{
		{"https://raw.githubusercontent.com/org/repo/main/config.yaml", "main"},
		{"https://raw.githubusercontent.com/org/repo/v1.2.3/path/to/file.yaml", "v1.2.3"},
		{"https://raw.githubusercontent.com/org/repo/0123456789abcdef0123456789abcdef01234567/x.yaml", "0123456789abcdef0123456789abcdef01234567"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got := extractRef(tc.raw)
			if got != tc.wantRef {
				t.Errorf("extractRef(%q) = %q, want %q", tc.raw, got, tc.wantRef)
			}
		})
	}
}

// TestExtractRef_GenericURL_QueryParam tests that extractRef returns the ?ref=
// value from generic HTTPS URLs (non-raw-githubusercontent).
func TestExtractRef_GenericURL_QueryParam(t *testing.T) {
	cases := []struct {
		raw     string
		wantRef string
	}{
		{"https://gitlab.com/org/repo?ref=feature-branch", "feature-branch"},
		{"https://example.com/org/repo//subdir?ref=deadbeef", "deadbeef"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got := extractRef(tc.raw)
			if got != tc.wantRef {
				t.Errorf("extractRef(%q) = %q, want %q", tc.raw, got, tc.wantRef)
			}
		})
	}
}

// TestExtractRef_NoRef tests that extractRef returns "" when no ref is present.
func TestExtractRef_NoRef(t *testing.T) {
	cases := []string{
		"https://github.com/org/repo",         // no ref at all
		"./local/path",                         // local path
		"https://example.com/file.yaml",        // remote file, no ref
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			got := extractRef(raw)
			if got != "" {
				t.Errorf("extractRef(%q) = %q, want empty string", raw, got)
			}
		})
	}
}

// TestIsSHAPinned tests the 40-char lowercase-hex SHA predicate.
func TestIsSHAPinned(t *testing.T) {
	valid := []string{
		"0123456789abcdef0123456789abcdef01234567",
		"abcdef0123456789abcdef0123456789abcdef01",
		"0000000000000000000000000000000000000000",
		"ffffffffffffffffffffffffffffffffffffffff",
	}
	for _, sha := range valid {
		t.Run("valid_"+sha[:8], func(t *testing.T) {
			if !isSHAPinned(sha) {
				t.Errorf("isSHAPinned(%q) = false, want true", sha)
			}
		})
	}

	invalid := []string{
		"v1.2.3",                                   // semver tag
		"main",                                      // branch name
		"HEAD",                                      // symbolic ref
		"abc123",                                    // short SHA (not 40 chars)
		"0123456789ABCDEF0123456789ABCDEF01234567",  // uppercase hex
		"0123456789abcdef0123456789abcdef0123456",   // 39 chars
		"0123456789abcdef0123456789abcdef012345678", // 41 chars
		"",                                          // empty
		"ghijklmnopqrstuvwxyz0123456789abcdef1234",  // non-hex chars
	}
	for _, sha := range invalid {
		t.Run("invalid_"+sha, func(t *testing.T) {
			if isSHAPinned(sha) {
				t.Errorf("isSHAPinned(%q) = true, want false", sha)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Task 2 — Step 2.3 RED: preflight gate tests
// ---------------------------------------------------------------------------

// TestPreflightSHAGate_StrictRejectsTag asserts that in strict mode, a remote
// reference pinned to a tag (not a SHA) is rejected before any fetch.
func TestPreflightSHAGate_StrictRejectsTag(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/repo?ref=v1.2.3
`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := preflightSHAGate(root, true /* requireSHAPinnedRemoteBases */)
	if err == nil {
		t.Fatal("preflightSHAGate: expected rejection for tag ref in strict mode, got nil")
	}
	if !strings.Contains(err.Error(), "SHA") && !strings.Contains(err.Error(), "pin") {
		t.Errorf("expected SHA-pin error, got: %v", err)
	}
}

// TestPreflightSHAGate_StrictRejectsBranch asserts that in strict mode, a remote
// reference pinned to a branch name is rejected.
func TestPreflightSHAGate_StrictRejectsBranch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/repo?ref=main
`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := preflightSHAGate(root, true)
	if err == nil {
		t.Fatal("preflightSHAGate: expected rejection for branch ref in strict mode, got nil")
	}
	if !strings.Contains(err.Error(), "SHA") && !strings.Contains(err.Error(), "pin") {
		t.Errorf("expected SHA-pin error, got: %v", err)
	}
}

// TestPreflightSHAGate_StrictAcceptsSHA asserts that in strict mode, a remote
// reference pinned to a full 40-hex SHA is accepted.
func TestPreflightSHAGate_StrictAcceptsSHA(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/repo?ref=0123456789abcdef0123456789abcdef01234567
`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := preflightSHAGate(root, true)
	if err != nil {
		t.Fatalf("preflightSHAGate: unexpected rejection for SHA ref in strict mode: %v", err)
	}
}

// TestPreflightSHAGate_PermissiveAcceptsTag asserts that in permissive (default)
// mode, a remote reference pinned to a tag is accepted.
func TestPreflightSHAGate_PermissiveAcceptsTag(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/repo?ref=v1.2.3
`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := preflightSHAGate(root, false /* permissive */)
	if err != nil {
		t.Fatalf("preflightSHAGate: unexpected rejection in permissive mode: %v", err)
	}
}

// fakeKustomizeSpec returns a KustomizeSource that, when used with a
// fakeRootResolver{baseDir: root}, causes Resolve to return root itself
// (SeedPath="" → filepath.Join(root, "") = root).
func fakeKustomizeSpec(root string) v1alpha1.KustomizeSource {
	return v1alpha1.KustomizeSource{URL: root, SeedPath: "", ShootPath: ""}
}

// TestPreflightSHAGate_StrictNoNetworkCalls verifies deterministically that strict
// rejection happens with zero network/git calls.
//
// Strategy: we exercise the full kustomizeSource.Render() path (not just
// preflightSHAGate in isolation) so the instrumentation is at the actual fetch
// boundary (kustomizeFetchHook, placed immediately before krust.Run in
// kustomize.go). Tests install the hook and assert it was NOT called — proving
// the gate short-circuits before krusty ever attempts a remote fetch.
//
// Sub-cases:
//   - named-tag: a ?ref=my-tag reference — clearly not a SHA
//   - undeterminable-ref: a remote file URL (https://example.com/file.yaml)
//     where extractRef returns "" because there is no ?ref= query param and
//     the URL is not a raw.githubusercontent.com path-segment form. An empty
//     string fails isSHAPinned, so the gate rejects it before any fetch.
func TestPreflightSHAGate_StrictNoNetworkCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		kust string // content of kustomization.yaml written into the root
	}{
		{
			name: "named-tag",
			kust: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://unreachable.invalid/org/repo?ref=my-tag
`,
		},
		{
			// Remote file URL with no ?ref= parameter: extractRef returns ""
			// (genuinely undeterminable — not a named branch, just absent).
			// The gate must reject this before any fetch attempt.
			name: "undeterminable-ref-no-query",
			kust: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://example.com/upstream-manifests.yaml
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(tc.kust), 0o600); err != nil {
				t.Fatal(err)
			}

			// Build a kustomizeSource that targets root directly (fakeRootResolver
			// returns root for any subPath, so SeedPath="" resolves to root).
			spec := fakeKustomizeSpec(root)
			src := &kustomizeSource{
				spec:                        &spec,
				resolver:                    fakeRootResolver{baseDir: root},
				requireSHAPinnedRemoteBases: true,
			}

			// Install the sentinel hook at the actual fetch boundary.
			// If Render() reaches kust.Run() the hook fires — proving the gate
			// failed to stop the fetch.
			fetchReached := false
			old := kustomizeFetchHook
			kustomizeFetchHook = func() { fetchReached = true }
			defer func() { kustomizeFetchHook = old }()

			_, err := src.Render(context.Background(), ModeSeed, "ns")

			// The hook must NOT have fired — the gate must have rejected first.
			if fetchReached {
				t.Fatal("kust.Run was reached before SHA-gate rejection (zero-fetch invariant violated)")
			}
			// Must return an error (SHA-gate rejection, not a network error).
			if err == nil {
				t.Fatal("expected SHA-gate rejection, got nil")
			}
			if !strings.Contains(err.Error(), "SHA") && !strings.Contains(err.Error(), "pin") {
				t.Errorf("expected SHA-pin error, got: %v", err)
			}
		})
	}
}

// TestPreflightSHAGate_FetchHookPositiveControl is the positive control for the
// kustomizeFetchHook instrumentation: it proves the hook actually fires when the
// gate passes (permissive mode) so the zero-fetch tests are not vacuously true.
func TestPreflightSHAGate_FetchHookPositiveControl(t *testing.T) {
	root := t.TempDir()
	// A kustomization with a remote base in permissive mode — gate is a no-op,
	// so Render() must reach kust.Run() and fire the hook.
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://unreachable.invalid/org/repo?ref=v1.2.3
`), 0o600); err != nil {
		t.Fatal(err)
	}

	spec2 := fakeKustomizeSpec(root)
	src := &kustomizeSource{
		spec:                        &spec2,
		resolver:                    fakeRootResolver{baseDir: root},
		requireSHAPinnedRemoteBases: false, // permissive — gate is a no-op
	}

	fetchReached := false
	old := kustomizeFetchHook
	kustomizeFetchHook = func() { fetchReached = true }
	defer func() { kustomizeFetchHook = old }()

	// Render will fail (kust.Run can't reach unreachable.invalid) but the hook
	// must have fired before that failure.
	src.Render(context.Background(), ModeSeed, "ns") //nolint:errcheck

	if !fetchReached {
		t.Fatal("kustomizeFetchHook was NOT called in permissive mode — instrumentation is dead code (positive control failed)")
	}
}

// TestPreflightSHAGate_StrictRejectsRemoteFile asserts that remote file
// references (raw.githubusercontent.com) with a non-SHA ref are rejected
// in strict mode when the ref cannot be determined as a SHA.
func TestPreflightSHAGate_StrictRejectsRemoteFileNonSHARef(t *testing.T) {
	root := t.TempDir()
	// raw.githubusercontent.com — classified as remote file, ref is in path
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://raw.githubusercontent.com/org/repo/main/config.yaml
`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := preflightSHAGate(root, true)
	if err == nil {
		t.Fatal("preflightSHAGate: expected rejection for non-SHA ref in raw.githubusercontent.com URL, got nil")
	}
	if !strings.Contains(err.Error(), "SHA") && !strings.Contains(err.Error(), "pin") {
		t.Errorf("expected SHA-pin error, got: %v", err)
	}
}

// TestPreflightSHAGate_StrictAcceptsRemoteFileSHARef asserts that remote file
// references with a full 40-hex SHA in the path segment are accepted in strict mode.
func TestPreflightSHAGate_StrictAcceptsRemoteFileSHARef(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://raw.githubusercontent.com/org/repo/0123456789abcdef0123456789abcdef01234567/config.yaml
`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := preflightSHAGate(root, true)
	if err != nil {
		t.Fatalf("preflightSHAGate: unexpected rejection for SHA-pinned raw.githubusercontent.com ref: %v", err)
	}
}

// TestPreflightSHAGate_StrictSanitizedError verifies the error message does not
// leak the raw URL or any credential-bearing fragments.
func TestPreflightSHAGate_StrictSanitizedError(t *testing.T) {
	root := t.TempDir()
	// Use a URL with a tag and include a token-like segment that must not leak.
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/secret-internal-repo?ref=release-2026
`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := preflightSHAGate(root, true)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Must not echo the raw URL or sensitive parts
	if strings.Contains(err.Error(), "secret-internal-repo") {
		t.Errorf("error leaks URL content: %v", err)
	}
	if strings.Contains(err.Error(), "release-2026") {
		t.Errorf("error leaks ref value: %v", err)
	}
}
