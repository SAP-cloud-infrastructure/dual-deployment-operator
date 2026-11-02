// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
