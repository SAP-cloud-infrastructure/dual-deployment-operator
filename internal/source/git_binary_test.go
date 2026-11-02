// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

// Task 6 — fail-closed assertion: missing git binary produces a sanitized error
//
// krusty (sigs.k8s.io/kustomize/api) resolves remote kustomize bases by calling
// ClonerUsingGitExec, which shells out to a `git` binary on PATH. When the binary
// is absent, krusty emits an error message like "no 'git' program on path".
//
// These tests verify that the existing sanitizeKrustyError wrapper (Task 3) covers
// the git-missing case:
//
//   1. A "no 'git' program on path" error is mapped to a fixed sanitized message.
//   2. The sanitized error must not echo the raw krusty message (no path details).
//   3. The error must not be nil (fail-closed invariant).
//   4. Full end-to-end path: Render() returns a sanitized error when git is absent.
//
// This is a pure-Go unit test; it does not build or run containers.
// See .superpowers/sdd/plan/task-6-report.md for the manual container verification
// procedure that must be run before promoting a new image to production.

package source

import (
	"context"
	"strings"
	"testing"
)

// TestGitMissingError_SanitizedFailClosed verifies that a "git not on PATH"
// error from krusty is mapped to a sanitized render error (fail-closed). This is
// the primary assertion for Task 6: even if the git binary is accidentally absent
// from the operator image, the operator surfaces a clean error rather than a raw
// krusty stack trace that might leak file paths or command details.
func TestGitMissingError_SanitizedFailClosed(t *testing.T) {
	// This is the exact error message krusty emits when it cannot find git:
	// see sigs.k8s.io/kustomize/pkg/git.ClonerUsingGitExec
	gitNotFoundErr := &gitNotFoundSimulatorError{msg: "no 'git' program on path"}

	sanitized := sanitizeKrustyError(gitNotFoundErr)

	// Must not be nil — fail-closed invariant.
	if sanitized == nil {
		t.Fatal("sanitizeKrustyError(gitNotFound): expected non-nil error, got nil")
	}

	msg := sanitized.Error()

	// Must not echo any path or command details from the raw error.
	if strings.Contains(msg, "no 'git'") {
		t.Errorf("sanitized error leaks raw krusty message: %q", msg)
	}
	if strings.Contains(msg, "program on path") {
		t.Errorf("sanitized error leaks path detail: %q", msg)
	}

	// Must be human-readable and describe the failure at a high level.
	if !strings.Contains(msg, "remote") && !strings.Contains(msg, "fetch") && !strings.Contains(msg, "build") {
		t.Errorf("sanitized error should describe the failure kind, got: %q", msg)
	}
}

// TestGitMissingError_EndToEndSanitized verifies that Render() propagates the
// sanitized error through the full krustyRunHook path when git is absent.
func TestGitMissingError_EndToEndSanitized(t *testing.T) {
	root := buildMinimalKustomizeRoot(t)
	src := buildKustomizeSourceForRoot(t, root)

	// Simulate krusty failing with the "no git on PATH" error.
	withKrustyRunHook(t, &gitNotFoundSimulatorError{msg: "no 'git' program on path"})

	ms, err := src.Render(context.Background(), ModeSeed, "ns")

	// Must fail — fail-closed invariant.
	if err == nil {
		t.Fatal("Render: expected error when git is missing, got nil")
	}
	// Must not return partial manifests alongside the error.
	if ms != nil {
		t.Errorf("Render: expected nil manifests when git is missing, got %d", len(ms))
	}
	// Error message must not echo raw krusty detail.
	msg := err.Error()
	if strings.Contains(msg, "no 'git'") {
		t.Errorf("Render error leaks raw git-missing message: %q", msg)
	}
	if strings.Contains(msg, "program on path") {
		t.Errorf("Render error leaks path detail: %q", msg)
	}
}

// gitNotFoundSimulatorError is a minimal error type that simulates krusty's
// "no 'git' program on path" error without requiring any real process execution.
type gitNotFoundSimulatorError struct{ msg string }

func (e *gitNotFoundSimulatorError) Error() string { return e.msg }
