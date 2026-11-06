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
		{ //nolint:gosec // test fixture, not a real credential
			raw:     "https://user:s3cr3t@github.com/org/repo?ref=v1",
			mustNot: "s3cr3t",
		},
		{
			raw:     "git@github.com:org/secret-repo.git?ref=v1",
			mustNot: "secret-repo",
		},
		// Malformed credential-bearing URLs must also not leak
		{ //nolint:gosec // test fixture, not a real credential
			raw:     "https://user:SECRET@host/%zz",
			mustNot: "SECRET",
		},
		// SCP-style with credential must not leak username or path
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
// git:: prefix stripping — SSH still rejected, HTTPS accepted
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
// Ambiguous paths with ?ref= (multi-segment, no //) must be rejected
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
// GitHub shorthand — exact ref extraction, no ?version= fallback
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
		"github.com/org/repo",            // no query at all
		"github.com/org/repo?version=v1", // version= not accepted (only ref=)
		"github.com/org/repo?ref=",       // empty ref=
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
// Deduplication — ordered set semantics
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
// Boundary checks on files + symlinks
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

// buildTreeWithAllThreeRefFields writes a kustomization.yaml that populates
// resources, components, AND bases simultaneously. This exercises the slice
// concatenation in walkDir and guards against the backing-array aliasing bug:
// `append(append(kust.Resources, kust.Components...), kust.Bases...)` would,
// when kust.Resources has spare capacity, write Components into Resources'
// backing array and silently drop/duplicate entries. The fix allocates a
// fresh slice, so all three fields must be discovered intact and in order.
func buildTreeWithAllThreeRefFields(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/org/res-a?ref=v1.0.0
  - https://github.com/org/res-b?ref=v1.1.0
components:
  - https://github.com/org/comp-a?ref=v2.0.0
bases:
  - https://github.com/org/base-a?ref=v3.0.0
`), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTreeWalkKustomize_AllThreeFields_NoSliceAliasing(t *testing.T) {
	root := buildTreeWithAllThreeRefFields(t)

	remotes, err := treeWalkKustomize(root)
	if err != nil {
		t.Fatalf("treeWalkKustomize: %v", err)
	}

	// Expected in resources→components→bases order (DFS, first-occurrence).
	want := []string{
		"https://github.com/org/res-a?ref=v1.0.0",
		"https://github.com/org/res-b?ref=v1.1.0",
		"https://github.com/org/comp-a?ref=v2.0.0",
		"https://github.com/org/base-a?ref=v3.0.0",
	}
	if len(remotes) != len(want) {
		t.Fatalf("expected %d remotes, got %d: %v", len(want), len(remotes), remotes)
	}
	for i, w := range want {
		if remotes[i].Raw != w {
			t.Errorf("remote[%d].Raw = %q, want %q", i, remotes[i].Raw, w)
		}
	}
}

// ---------------------------------------------------------------------------
// Round-2 review findings
// ---------------------------------------------------------------------------

// GitHub shorthand ambiguous path boundary
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
// Regression: malformed YAML with credential-bearing URL must not leak
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
	// The credential-bearing URL below is a test fixture, not a real credential.
	//nolint:gosec // test fixture: credential-bearing URL is intentional to verify sanitization
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
		"https://github.com/org/repo",   // no ref at all
		"./local/path",                  // local path
		"https://example.com/file.yaml", // remote file, no ref
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
		"v1.2.3", // semver tag
		"main",   // branch name
		"HEAD",   // symbolic ref
		"abc123", // short SHA (not 40 chars)
		"0123456789ABCDEF0123456789ABCDEF01234567",  // uppercase hex
		"0123456789abcdef0123456789abcdef0123456",   // 39 chars
		"0123456789abcdef0123456789abcdef012345678", // 41 chars
		"", // empty
		"ghijklmnopqrstuvwxyz0123456789abcdef1234", // non-hex chars
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
  - github.com/org/repo//overlays/prod?ref=v1.2.3
`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := preflightSHAGate(root, root, true /* requireSHAPinnedRemoteBases */)
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
  - github.com/org/repo//overlays/prod?ref=main
`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := preflightSHAGate(root, root, true)
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
  - github.com/org/repo//overlays/prod?ref=0123456789abcdef0123456789abcdef01234567
`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := preflightSHAGate(root, root, true)
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

	err := preflightSHAGate(root, root, false /* permissive */)
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
  - github.com/org/repo?ref=my-tag
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
//
// this test previously called real krusty against "unreachable.invalid",
// making an actual network attempt. Fixed: inject krustyRunHook to return a
// synthetic error immediately after the hook fires, verifying the hook fires
// without any real network access.
func TestPreflightSHAGate_FetchHookPositiveControl(t *testing.T) {
	root := t.TempDir()
	// A kustomization with a remote base in permissive mode — gate is a no-op,
	// so Render() must reach kust.Run() and fire the fetch hook.
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://example.com/org/repo?ref=v1.2.3
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
	oldFetch := kustomizeFetchHook
	kustomizeFetchHook = func() { fetchReached = true }
	defer func() { kustomizeFetchHook = oldFetch }()

	// inject a synthetic krusty error so no real network call is made.
	// The hook fires before krust.Run is called; krustyRunHook makes krust.Run
	// return immediately with this sentinel error.
	oldKrusty := krustyRunHook
	krustyRunHook = errors.New("synthetic krusty error for positive-control test")
	defer func() { krustyRunHook = oldKrusty }()

	// Render will fail with the synthetic error, but the fetch hook must have
	// fired before runKrusty is entered.
	_, renderErr := src.Render(context.Background(), ModeSeed, "ns")
	if renderErr == nil {
		t.Fatal("expected an error from Render (synthetic krusty hook), got nil")
	}

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

	err := preflightSHAGate(root, root, true)
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

	err := preflightSHAGate(root, root, true)
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

	err := preflightSHAGate(root, root, true)
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

// ---------------------------------------------------------------------------
// resolveAndCheckBoundary — security: fail-closed on filepath.Abs failure
// ---------------------------------------------------------------------------

// TestResolveAndCheckBoundary_NonExistentInBounds verifies that a non-existent
// path that is within the isolation root is handled gracefully (returns "", nil).
// This exercises the os.IsNotExist branch of resolveAndCheckBoundary.
func TestResolveAndCheckBoundary_NonExistentInBounds(t *testing.T) {
	root := t.TempDir()
	nonExistent := filepath.Join(root, "does-not-exist")

	got, err := resolveAndCheckBoundary(root, nonExistent)
	if err != nil {
		t.Errorf("resolveAndCheckBoundary: unexpected error for non-existent in-bounds path: %v", err)
	}
	if got != "" {
		t.Errorf("resolveAndCheckBoundary: expected empty string for non-existent path, got %q", got)
	}
}

// TestResolveAndCheckBoundary_NonExistentOutOfBounds verifies that a non-existent
// path that escapes the isolation root is rejected (returns an error, not nil).
// Fail-closed invariant: when the boundary cannot be confirmed, we must error.
func TestResolveAndCheckBoundary_NonExistentOutOfBounds(t *testing.T) {
	root := t.TempDir()
	// A path that resolves outside root via ..
	outsidePath := filepath.Join(root, "..", "escaped-dir")

	_, err := resolveAndCheckBoundary(root, outsidePath)
	if err == nil {
		t.Error("resolveAndCheckBoundary: expected boundary violation error for out-of-bounds non-existent path, got nil (fail-open — FAIL)")
	}
}

// ---------------------------------------------------------------------------
// Regression: malformed query string bypasses strict SHA enforcement
// ---------------------------------------------------------------------------

// TestClassifyHTTPURL_RejectsMalformedQuery verifies that a URL with a valid
// ?ref= but also an unparsable query fragment (e.g. ?ref=sha&bad=%zz) is
// rejected, not silently accepted with only the valid portion extracted.
//
// Background: url.URL.Query() silently drops malformed query fragments and
// returns partial results; a mixed valid+invalid query like ?ref=<sha>&bad=%zz
// would extract the valid SHA and pass the strict SHA gate, while krusty/git
// would see the whole unparsable query and default to HEAD — bypassing the
// pin entirely. We must use url.ParseQuery explicitly and reject on parse error.
func TestClassifyHTTPURL_RejectsMalformedQuery(t *testing.T) {
	cases := []string{
		// valid ?ref= combined with an unparsable fragment — must be REJECTED
		"https://github.com/org/repo?ref=0123456789abcdef0123456789abcdef01234567&bad=%zz",
		"https://example.com/org/repo?ref=abc123&x=%gg",
		// purely malformed query — must be REJECTED
		"https://github.com/org/repo?%zz",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(raw)
			if err == nil {
				t.Errorf("classifyKustomizeReference(%q): expected rejection for malformed query, got nil", raw)
			}
			// Error must not echo the raw URL content
			if strings.Contains(err.Error(), "github.com") || strings.Contains(err.Error(), "example.com") {
				t.Errorf("URL leaked in error for %q: %v", raw, err)
			}
		})
	}
}

// TestClassifyGitHubShorthand_RejectsMalformedQuery mirrors
// TestClassifyHTTPURL_RejectsMalformedQuery for the github.com/org/repo
// shorthand classifier. The shorthand path previously swallowed the
// url.ParseQuery error (if err == nil), so a mixed valid+invalid query like
// ?ref=<sha>&bad=%zz extracted the valid SHA and passed the strict SHA gate,
// while krusty/git would see the whole unparsable query and default to HEAD —
// bypassing the pin. The classifier must reject on parse error, consistent
// with the HTTPS classifier, and must not echo the raw reference.
func TestClassifyGitHubShorthand_RejectsMalformedQuery(t *testing.T) {
	cases := []string{
		// valid ?ref= combined with an unparsable fragment — must be REJECTED
		"github.com/org/repo?ref=0123456789abcdef0123456789abcdef01234567&bad=%zz",
		"github.com/org/repo//path?ref=abc1234&x=%gg",
		// purely malformed query — must be REJECTED
		"github.com/org/repo?%zz",
		"github.com/org/repo//path?%zz",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(raw)
			if err == nil {
				t.Errorf("classifyKustomizeReference(%q): expected rejection for malformed query, got nil", raw)
			}
			// Error must not echo the raw reference content
			if strings.Contains(err.Error(), "github.com") {
				t.Errorf("reference leaked in error for %q: %v", raw, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Regression: SCP-without-@ and file:// should be rejected, not local
// ---------------------------------------------------------------------------

// TestClassifyKustomizeReference_RejectsSCPWithoutAt verifies that SCP-like
// references WITHOUT a "user@" prefix (e.g. "github.com:org/repo?ref=main")
// are rejected rather than silently treated as a local path.
func TestClassifyKustomizeReference_RejectsSCPWithoutAt(t *testing.T) {
	cases := []string{
		"github.com:org/repo?ref=main",
		"gitlab.com:org/repo?ref=v1",
		"host.example.com:path/to/repo",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(raw)
			if err == nil {
				t.Errorf("classifyKustomizeReference(%q): expected rejection for SCP-without-@ form, got nil", raw)
			}
			// Error must not echo raw URL content
			if strings.Contains(err.Error(), raw) {
				t.Errorf("raw URL leaked in error for %q: %v", raw, err)
			}
		})
	}
}

// TestClassifyKustomizeReference_RejectsFileScheme verifies that file:// URLs
// are rejected (they are not valid remote git bases and must not be treated as
// local paths, since they could reference arbitrary filesystem locations).
func TestClassifyKustomizeReference_RejectsFileScheme(t *testing.T) {
	cases := []string{
		"file:///outside/repo?ref=main",
		"file:///etc/passwd",
		"file://localhost/path/to/dir",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(raw)
			if err == nil {
				t.Errorf("classifyKustomizeReference(%q): expected rejection for file:// scheme, got nil", raw)
			}
			// Error must not echo the raw path
			if strings.Contains(err.Error(), "/outside") || strings.Contains(err.Error(), "/etc") {
				t.Errorf("path leaked in error for %q: %v", raw, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Regression: absolute resource references bypass isolation root
// ---------------------------------------------------------------------------

// TestTreeWalkKustomize_RejectsAbsolutePath verifies that an absolute filesystem
// path in a kustomization's resources field is rejected outright.
//
// Background: filepath.Join(absDir, "/abs/path") produces absDir+"/abs/path"
// which looks inside the root to the DDO boundary check, but krusty's
// LoadRestrictionsNone interprets the kustomization's original absolute value
// directly — bypassing the isolation boundary entirely.
func TestTreeWalkKustomize_RejectsAbsolutePath(t *testing.T) {
	root := t.TempDir()

	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - /etc/passwd\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := treeWalkKustomize(root)
	if err == nil {
		t.Fatal("treeWalkKustomize: expected rejection for absolute path reference, got nil — isolation boundary escape not blocked")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("expected error to mention 'absolute', got: %v", err)
	}
}

// TestTreeWalkKustomize_RejectsAbsolutePathVariants verifies multiple absolute
// path forms are all rejected (the rule: no absolute references at all).
func TestTreeWalkKustomize_RejectsAbsolutePathVariants(t *testing.T) {
	cases := []string{
		"/etc/passwd",
		"/tmp/evil-kustomization",
		"/var/secrets/credentials.yaml",
	}
	for _, absRef := range cases {
		t.Run(absRef, func(t *testing.T) {
			root := t.TempDir()
			kustContent := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - " + absRef + "\n"
			if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(kustContent), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := treeWalkKustomize(root)
			if err == nil {
				t.Errorf("treeWalkKustomize: absolute ref %q should be rejected, got nil", absRef)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Regression: credential-bearing paths must not leak in preflight errors
// ---------------------------------------------------------------------------

// TestResolveAndCheckBoundary_NoCredentialLeakOnError verifies that when
// resolveAndCheckBoundary encounters a filesystem error on a path with
// credential-like content, the credential does NOT appear in the returned error.
func TestResolveAndCheckBoundary_NoCredentialLeakOnError(t *testing.T) {
	root := t.TempDir()
	// A non-existent path whose name embeds a "credential" token.
	// resolveAndCheckBoundary sees this as non-existent-and-in-bounds → no
	// error expected, but the token must not appear if an error IS returned.
	credPath := filepath.Join(root, "user:SUPERSECRET@host:repo", "path")

	_, err := resolveAndCheckBoundary(root, credPath)
	// Either nil (non-existent in-bounds) or an error — in both cases no leak.
	if err != nil && strings.Contains(err.Error(), "SUPERSECRET") {
		t.Errorf("credential leaked in resolveAndCheckBoundary error: %v", err)
	}
}

// TestPreflightSHAGate_CredentialBearingRefNoLeak verifies that a
// kustomization.yaml with a credential-bearing URL in resources (embedded
// userinfo) is rejected and the credential NEVER appears in the error message.
func TestPreflightSHAGate_CredentialBearingRefNoLeak(t *testing.T) {
	root := t.TempDir()
	// The URL string below contains a fake credential to verify sanitization —
	// it is a test fixture, not a real secret.
	kustYAML := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n" +
		"  - https://user:SUPERSECRET@github.com/org/repo?ref=0123456789abcdef0123456789abcdef01234567\n"
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(kustYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	err := preflightSHAGate(root, root, true)
	if err == nil {
		t.Fatal("expected rejection for credential-bearing URL, got nil")
	}
	// The credential must not appear in ANY part of the error.
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Errorf("credential leaked in preflightSHAGate error: %v", err)
	}
	if strings.Contains(err.Error(), "github.com") {
		t.Errorf("URL leaked in preflightSHAGate error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Residual regression: SCP-colon bypass via @ appearing later in path
// ---------------------------------------------------------------------------

// TestClassifyKustomizeReference_RejectsSCPColonWithLaterAt is the exact
// reproduction case for the SCP-colon residual bypass. The reference
// "github.com:org/repo//base@variant?ref=main" has a colon before the first
// slash (making it SCP-like) but also has an '@' later in the path/query.
// The OLD code rejected based on whether '@' appeared ANYWHERE in the string,
// so this form was incorrectly classified as 'local' (bypassing all checks).
// The FIXED code checks only whether '@' appears BEFORE the colon.
func TestClassifyKustomizeReference_RejectsSCPColonWithLaterAt(t *testing.T) {
	cases := []string{
		// Exact reproduction from the finding: colon before first slash, @ after
		"github.com:org/repo//base@variant?ref=main",
		// Variation: @ in query string only
		"github.com:org/repo?ref=main@abc",
		// Variation: multiple @ after the colon
		"host.example.com:org/repo//path@tag@extra",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, _, err := classifyKustomizeReference(raw)
			if err == nil {
				t.Errorf("classifyKustomizeReference(%q): expected rejection for SCP-colon form (@ appears after colon), got nil — SHA enforcement bypass is open", raw)
			}
			// Error must not echo raw URL content
			if strings.Contains(err.Error(), raw) {
				t.Errorf("raw URL leaked in error for %q: %v", raw, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Residual regression: OS read errors in readKustomizationBounded must
// not embed credential-bearing path text
// ---------------------------------------------------------------------------

// TestReadKustomizationBounded_NoCredentialLeakOnIsADirectory is the exact
// reproduction case for the OS-read-error leak residual. A directory named with a
// credential-bearing component (e.g. "user:SUPERSECRET@host") that contains a
// subdirectory named "kustomization.yaml" triggers an "is a directory" OS error
// in os.ReadFile. The OLD code wrapped this with %w, causing the full path
// (including the credential) to appear in the error string. The FIXED code
// discards the raw OS error and returns a sanitized fixed-text message.
func TestReadKustomizationBounded_NoCredentialLeakOnIsADirectory(t *testing.T) {
	root := t.TempDir()

	// Create a directory named with a fake credential (test fixture only).
	// Inside it, create a directory also named "kustomization.yaml" — this
	// triggers an "is a directory" error from os.ReadFile, which embeds the
	// full path in its error string.
	credDir := filepath.Join(root, "user:SUPERSECRET@host")
	kustAsDir := filepath.Join(credDir, "kustomization.yaml")
	if err := os.MkdirAll(kustAsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Call readKustomizationBounded on the credential-bearing directory.
	_, err := readKustomizationBounded(root, credDir)

	// It must return an error (the "kustomization.yaml" entry is a directory,
	// not a readable file).
	if err == nil {
		t.Fatal("readKustomizationBounded: expected error for kustomization.yaml-as-directory, got nil")
	}
	// The credential substring must NOT appear anywhere in the error message.
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Errorf("credential leaked via 'is a directory' OS error in readKustomizationBounded: %v", err)
	}
	// The raw path must not appear either.
	if strings.Contains(err.Error(), "user:SUPERSECRET") {
		t.Errorf("path with credential leaked in readKustomizationBounded error: %v", err)
	}
}

// TestReadKustomizationBounded_NoCredentialLeakOnEvalSymlinksError verifies
// that when filepath.EvalSymlinks fails on the kustomization.yaml path
// (e.g. because a dangling symlink embeds a credential-bearing target path),
// the credential does NOT appear in the returned error.
func TestReadKustomizationBounded_NoCredentialLeakOnEvalSymlinksError(t *testing.T) {
	root := t.TempDir()

	// Create a directory named with a fake credential (test fixture only).
	credDir := filepath.Join(root, "user:SUPERSECRET@host")
	if err := os.MkdirAll(credDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a dangling symlink named "kustomization.yaml" pointing to a
	// non-existent path that itself has a credential-bearing name. This causes
	// EvalSymlinks to return an lstat error containing the target path.
	kustSymlink := filepath.Join(credDir, "kustomization.yaml")
	danglingTarget := filepath.Join(root, "user:ANOTHER_SECRET@evil")
	if err := os.Symlink(danglingTarget, kustSymlink); err != nil {
		t.Fatal(err)
	}

	_, err := readKustomizationBounded(root, credDir)

	// A dangling symlink (non-existent target) is treated as "not found" by
	// os.IsNotExist — so the file is simply skipped, returning (nil, nil).
	// If the error is non-nil however, the credential must not leak.
	if err != nil && strings.Contains(err.Error(), "SUPERSECRET") {
		t.Errorf("credential leaked via EvalSymlinks error in readKustomizationBounded: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "ANOTHER_SECRET") {
		t.Errorf("symlink target credential leaked in readKustomizationBounded error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Strict-mode acceptance matrix (D2'): krusty v0.21.1 dispatches any http(s)
// URL to its HTTP file loader FIRST (accumulateFile → IsRemoteFile checks only
// the scheme), falling back to a git clone only on failure. So an https:// URL
// in a resource-like field (resources/bases, and path-valued
// generators/transformers/validators) can be served arbitrary content over
// plain HTTP regardless of ?ref= — the pin is not honored. The components field
// is the exception: upstream calls Loader.New directly, taking the git route.
//
// These tests lock the matrix: https:// git-dir refs are rejected in strict
// mode outside components (regardless of // or .git), accepted inside
// components; scheme-less github.com shorthand reaches git and is accepted
// everywhere; raw.githubusercontent.com SHA-pinned files are accepted in
// resource-like fields but rejected in components (which needs directories);
// and a remote value in a local-only field is always rejected in strict mode.
// ---------------------------------------------------------------------------

// writeKust is a small helper: writes a kustomization.yaml with the given body
// into a fresh temp dir and returns the dir.
func writeKust(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	content := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n" + body
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestPreflightSHAGate_StrictRejectsHTTPSGitDirOutsideComponents asserts that an
// https:// git-directory reference in resources/bases is rejected in strict
// mode even when it carries a full 40-hex SHA, because krusty would HTTP-GET it
// before ever consulting git (so ?ref= pins nothing). All three spellings
// (repo-root, //subpath, .git/subpath) must be rejected.
func TestPreflightSHAGate_StrictRejectsHTTPSGitDirOutsideComponents(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	cases := []struct {
		name string
		ref  string
	}{
		{"repo-root", "https://github.com/org/repo?ref=" + sha},
		{"double-slash-subpath", "https://github.com/org/repo//overlays/prod?ref=" + sha},
		{"dot-git-subpath", "https://github.com/org/repo.git/overlays/prod?ref=" + sha},
	}
	for _, field := range []string{"resources", "bases"} {
		for _, tc := range cases {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				root := writeKust(t, field+":\n  - "+tc.ref+"\n")
				err := preflightSHAGate(root, root, true)
				if err == nil {
					t.Fatalf("expected strict rejection of https:// git-dir ref %q in %s, got nil", tc.ref, field)
				}
				// Must not leak the raw URL.
				if strings.Contains(err.Error(), "github.com/org/repo") {
					t.Errorf("error leaks raw URL: %v", err)
				}
			})
		}
	}
}

// TestPreflightSHAGate_StrictAcceptsHTTPSGitDirInComponents asserts that the
// same https:// git-directory ref that is rejected in resources is ACCEPTED in
// the components field (upstream takes the git route there) when SHA-pinned.
func TestPreflightSHAGate_StrictAcceptsHTTPSGitDirInComponents(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	cases := []string{
		"https://github.com/org/repo//components/base?ref=" + sha,
		"https://github.com/org/repo.git/components/base?ref=" + sha,
	}
	for _, ref := range cases {
		t.Run(ref, func(t *testing.T) {
			root := writeKust(t, "components:\n  - "+ref+"\n")
			if err := preflightSHAGate(root, root, true); err != nil {
				t.Fatalf("expected strict acceptance of SHA-pinned https:// git-dir ref in components, got: %v", err)
			}
		})
	}
}

// TestPreflightSHAGate_StrictRejectsUnpinnedHTTPSGitDirInComponents asserts that
// even inside components, an https:// git-dir ref that is NOT SHA-pinned (tag or
// branch) is rejected by the final SHA-pin loop.
func TestPreflightSHAGate_StrictRejectsUnpinnedHTTPSGitDirInComponents(t *testing.T) {
	root := writeKust(t, "components:\n  - https://github.com/org/repo//components/base?ref=v1.2.3\n")
	err := preflightSHAGate(root, root, true)
	if err == nil {
		t.Fatal("expected strict rejection of unpinned https:// git-dir ref in components, got nil")
	}
	if !strings.Contains(err.Error(), "SHA") && !strings.Contains(err.Error(), "pin") {
		t.Errorf("expected SHA-pin error, got: %v", err)
	}
}

// TestPreflightSHAGate_StrictAcceptsSchemelessShorthandEverywhere asserts that a
// scheme-less github.com shorthand ref pinned to a SHA is accepted in both
// resources and components (it always reaches the git route).
func TestPreflightSHAGate_StrictAcceptsSchemelessShorthandEverywhere(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	for _, field := range []string{"resources", "components", "bases"} {
		t.Run(field, func(t *testing.T) {
			root := writeKust(t, field+":\n  - github.com/org/repo//overlays/prod?ref="+sha+"\n")
			if err := preflightSHAGate(root, root, true); err != nil {
				t.Fatalf("expected strict acceptance of scheme-less shorthand in %s, got: %v", field, err)
			}
		})
	}
}

// TestPreflightSHAGate_StrictRejectsRawGitHubFileInComponents asserts that a
// raw.githubusercontent.com file URL is rejected in the components field: the
// components field requires a directory/kustomization, not a single file. (It
// is accepted in resources — covered by TestPreflightSHAGate_StrictAcceptsRemoteFileSHARef.)
func TestPreflightSHAGate_StrictRejectsRawGitHubFileInComponents(t *testing.T) {
	root := writeKust(t, "components:\n  - https://raw.githubusercontent.com/org/repo/0123456789abcdef0123456789abcdef01234567/config.yaml\n")
	err := preflightSHAGate(root, root, true)
	if err == nil {
		t.Fatal("expected strict rejection of raw.githubusercontent.com file URL in components, got nil")
	}
}

// TestPreflightSHAGate_StrictRejectsNonRawRemoteFile asserts that an https://
// file URL (ending .yaml/.yml/.json) on a host other than
// raw.githubusercontent.com is rejected in strict mode even with a ?ref=<sha>,
// because the file is fetched over plain HTTP and the server can return anything.
func TestPreflightSHAGate_StrictRejectsNonRawRemoteFile(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	cases := []string{
		"https://example.com/manifests/config.yaml",
		"https://cdn.internal.corp/config.yaml?ref=" + sha,
		"https://raw.githubusercontent.com.evil.example/org/repo/" + sha + "/config.yaml",
	}
	for _, ref := range cases {
		t.Run(ref, func(t *testing.T) {
			root := writeKust(t, "resources:\n  - "+ref+"\n")
			err := preflightSHAGate(root, root, true)
			if err == nil {
				t.Fatalf("expected strict rejection of non-raw remote file %q, got nil", ref)
			}
			if strings.Contains(err.Error(), "example.com") || strings.Contains(err.Error(), "corp") {
				t.Errorf("error leaks host: %v", err)
			}
		})
	}
}

// TestPreflightSHAGate_StrictRejectsRemoteInLocalOnlyField asserts that a remote
// reference in a local-only field (one whose value is consumed as a local file
// path: crds, configurations, openapi.path, patches[].path, replacements[].path,
// generator/transformer/validator config, configMap/secretGenerator files/envs)
// is rejected in strict mode, even if it is an otherwise-acceptable
// SHA-pinned raw.githubusercontent.com URL.
func TestPreflightSHAGate_StrictRejectsRemoteInLocalOnlyField(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	rawFile := "https://raw.githubusercontent.com/org/repo/" + sha + "/crd.yaml"
	cases := []struct {
		name string
		body string
	}{
		{"crds", "crds:\n  - " + rawFile + "\n"},
		{"configurations", "configurations:\n  - " + rawFile + "\n"},
		{"openapi", "openapi:\n  path: " + rawFile + "\n"},
		{"patches-path", "patches:\n  - path: " + rawFile + "\n"},
		{"replacements-path", "replacements:\n  - path: " + rawFile + "\n"},
		{"configMapGenerator-files", "configMapGenerator:\n  - name: cm\n    files:\n      - " + rawFile + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := writeKust(t, tc.body)
			err := preflightSHAGate(root, root, true)
			if err == nil {
				t.Fatalf("expected strict rejection of remote ref in local-only field %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "local-only") {
				t.Errorf("expected a local-only-field rejection message, got: %v", err)
			}
		})
	}
}

// TestPreflightSHAGate_StrictRejectsSameURLWhenAlsoInResources asserts the
// dedup fix (walkDir keys on raw+field): a URL that appears in BOTH components
// (where it would be accepted) AND resources (where it must be rejected) is
// rejected — the stricter resources occurrence is not dropped by dedup.
func TestPreflightSHAGate_StrictRejectsSameURLWhenAlsoInResources(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	ref := "https://github.com/org/repo//base?ref=" + sha
	root := writeKust(t, "components:\n  - "+ref+"\nresources:\n  - "+ref+"\n")
	err := preflightSHAGate(root, root, true)
	if err == nil {
		t.Fatal("expected strict rejection because the same URL also appears in resources (dedup must not drop the stricter occurrence), got nil")
	}
	if strings.Contains(err.Error(), "github.com/org/repo") {
		t.Errorf("error leaks raw URL: %v", err)
	}
}

// TestPreflightSHAGate_PermissiveAllowsHTTPSGitDirInResources asserts the
// flag-off invariant: the strict-only rules (local-only-field rejection,
// raw-file canonicalization, HTTPS-git-dir rejection) do NOT apply in permissive
// mode, so main's accepted inputs still render. An https:// git-dir ref in
// resources passes the gate (no rejection) when requireSHAPinnedRemoteBases=false.
func TestPreflightSHAGate_PermissiveAllowsHTTPSGitDirInResources(t *testing.T) {
	root := writeKust(t, "resources:\n  - https://github.com/org/repo?ref=v1.2.3\n")
	if err := preflightSHAGate(root, root, false); err != nil {
		t.Fatalf("permissive mode must not apply strict rules; unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Round-3 Finding 1 & 2: inline doc guard + fieldTreeEdge recursion
// ---------------------------------------------------------------------------

// TestCollectFieldRefs_InlineGeneratorDocSkipped verifies that an inline
// multiline YAML document in the generators: field (containing "\n") is NOT
// classified as a reference — it is silently skipped just like the pre-existing
// guard on patchesStrategicMerge. Without the guard, the inline doc's first
// line (e.g. "apiVersion: builtin") would be fed to classifyKustomizeReference
// and misread as an SCP host:path reference, causing a spurious tree-walk error.
func TestCollectFieldRefs_InlineGeneratorDocSkipped(t *testing.T) {
	inline := "apiVersion: builtin\nkind: PrefixSuffixTransformer\nmetadata:\n  name: pfx\nprefix: foo-\n"
	kust := &kustomizationYAML{
		Generators: []string{inline},
	}
	refs := collectFieldRefs(kust)
	for _, r := range refs {
		if r.raw == inline || strings.Contains(r.raw, "\n") {
			t.Errorf("inline generator doc must be skipped, but got ref: %q (field %s)", r.raw, r.field)
		}
	}
}

// TestCollectFieldRefs_InlineTransformerDocSkipped is the same guard for
// transformers: field.
func TestCollectFieldRefs_InlineTransformerDocSkipped(t *testing.T) {
	inline := "apiVersion: builtin\nkind: PrefixSuffixTransformer\nmetadata:\n  name: pfx\nprefix: foo-\n"
	kust := &kustomizationYAML{
		Transformers: []string{inline},
	}
	refs := collectFieldRefs(kust)
	for _, r := range refs {
		if strings.Contains(r.raw, "\n") {
			t.Errorf("inline transformer doc must be skipped, but got ref: %q", r.raw)
		}
	}
}

// TestCollectFieldRefs_InlineValidatorDocSkipped is the same guard for
// validators: field.
func TestCollectFieldRefs_InlineValidatorDocSkipped(t *testing.T) {
	inline := "apiVersion: builtin\nkind: SomeValidator\n"
	kust := &kustomizationYAML{
		Validators: []string{inline},
	}
	refs := collectFieldRefs(kust)
	for _, r := range refs {
		if strings.Contains(r.raw, "\n") {
			t.Errorf("inline validator doc must be skipped, but got ref: %q", r.raw)
		}
	}
}

// TestPreflightSHAGate_InlineTransformerRendersOK_FlagOn verifies that a
// kustomization.yaml with an inline PrefixSuffixTransformer doc (no remote
// refs) passes the preflight gate with requireSHAPinnedRemoteBases=true AND
// false — i.e. the inline doc does not cause a spurious error.
func TestPreflightSHAGate_InlineTransformerRendersOK_FlagOn(t *testing.T) {
	inline := "apiVersion: builtin\nkind: PrefixSuffixTransformer\nmetadata:\n  name: pfx\nprefix: foo-\n"
	body := "transformers:\n  - |\n    " + strings.ReplaceAll(inline, "\n", "\n    ") + "\n"
	root := writeKust(t, body)
	for _, strict := range []bool{false, true} {
		if err := preflightSHAGate(root, root, strict); err != nil {
			t.Errorf("strict=%v: inline transformer doc must not cause preflight error, got: %v", strict, err)
		}
	}
}

// TestPreflightSHAGate_TransformersLocalDirUnpinnedBase_StrictRejects verifies
// Finding 2: a transformers: entry pointing at a local DIRECTORY that itself
// contains a kustomization.yaml with an unpinned remote base must be REJECTED
// in strict mode (the fieldTreeEdge category causes walkDir to recurse into it).
func TestPreflightSHAGate_TransformersLocalDirUnpinnedBase_StrictRejects(t *testing.T) {
	root := t.TempDir()
	// ./t/kustomization.yaml has an unpinned remote base
	tDir := filepath.Join(root, "t")
	if err := os.MkdirAll(tDir, 0o755); err != nil {
		t.Fatal(err)
	}
	innerKust := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - github.com/org/repo//base?ref=main\n"
	if err := os.WriteFile(filepath.Join(tDir, "kustomization.yaml"), []byte(innerKust), 0o600); err != nil {
		t.Fatal(err)
	}
	// root kustomization references ./t as a transformer dir
	rootKust := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\ntransformers:\n  - ./t\n"
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(rootKust), 0o600); err != nil {
		t.Fatal(err)
	}

	err := preflightSHAGate(root, root, true)
	if err == nil {
		t.Fatal("strict mode must reject unpinned remote base reachable via transformers-referenced local dir, got nil")
	}
}

// TestPreflightSHAGate_TransformersDirectRemote_Rejected verifies that a
// direct remote value in a transformers: entry is rejected in strict mode
// (same as fieldLocalOnly rule 1 — remote in a tree-edge field is disallowed).
func TestPreflightSHAGate_TransformersDirectRemote_Rejected(t *testing.T) {
	root := writeKust(t, "transformers:\n  - github.com/org/repo//transformer?ref=main\n")
	err := preflightSHAGate(root, root, true)
	if err == nil {
		t.Fatal("strict mode must reject direct remote value in transformers field, got nil")
	}
}

// TestPreflightSHAGate_TransformersLocalDirPinnedBase_StrictAccepts verifies
// that a transformers: entry pointing at a local directory whose kustomization
// references only a SHA-pinned remote base IS accepted in strict mode.
func TestPreflightSHAGate_TransformersLocalDirPinnedBase_StrictAccepts(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	root := t.TempDir()
	tDir := filepath.Join(root, "t")
	if err := os.MkdirAll(tDir, 0o755); err != nil {
		t.Fatal(err)
	}
	innerKust := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - github.com/org/repo//base?ref=" + sha + "\n"
	if err := os.WriteFile(filepath.Join(tDir, "kustomization.yaml"), []byte(innerKust), 0o600); err != nil {
		t.Fatal(err)
	}
	rootKust := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\ntransformers:\n  - ./t\n"
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(rootKust), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := preflightSHAGate(root, root, true); err != nil {
		t.Errorf("strict mode must accept SHA-pinned remote base reachable via transformers dir, got: %v", err)
	}
}
