// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

// Missing-git-binary: Render surfaces a credential-safe, actionable error
//
// krusty (sigs.k8s.io/kustomize/api) resolves remote kustomize bases by calling
// ClonerUsingGitExec, which shells out to a `git` binary on PATH. When the binary
// is absent, krusty emits an error message like "no 'git' program on path".
//
// Under the redact-and-preserve error policy, sanitizeKrustyError does NOT
// discard this message: a git-missing diagnostic carries no credentials and is
// exactly the actionable detail an operator needs. These tests verify that:
//
//  1. The sanitized error is non-nil (fail-closed invariant).
//  2. The actionable diagnostic ("no 'git' program on path") is PRESERVED.
//  3. Full end-to-end path: Render() propagates the error.
//
// Note: Finding 3 removed the reason-code prefix — it mislabelled local errors.
// The message is forwarded redacted-but-intact, with no prefix added.
//
// This is a pure-Go unit test; it does not build or run containers. The manual
// container verification procedure for a new image is documented in the
// deployment guide under docs/ (see PR #63).

package source

import (
	"context"
	"strings"
	"testing"
)

// TestGitMissingError_FailClosed verifies that a "git not on PATH"
// error from krusty is mapped to a non-nil render error (fail-closed) while
// preserving the actionable, credential-free diagnostic. Even if the git binary
// is accidentally absent from the operator image, the operator surfaces a clean
// error that tells the operator what went wrong. Finding 3 removed the reason-
// code prefix (it mislabelled local errors); the message is forwarded as-is.
func TestGitMissingError_FailClosed(t *testing.T) {
	// This is the exact error message krusty emits when it cannot find git:
	// see sigs.k8s.io/kustomize/pkg/git.ClonerUsingGitExec
	gitNotFoundErr := &gitNotFoundSimulatorError{msg: "no 'git' program on path"}

	sanitized := sanitizeKrustyError(gitNotFoundErr)

	// Must not be nil — fail-closed invariant.
	if sanitized == nil {
		t.Fatal("sanitizeKrustyError(gitNotFound): expected non-nil error, got nil")
	}

	msg := sanitized.Error()

	// The credential-free diagnostic is PRESERVED (redact-and-preserve policy).
	if !strings.Contains(msg, "no 'git' program on path") {
		t.Errorf("sanitized error should preserve the actionable git-missing detail, got: %q", msg)
	}
	// No reason-code prefix (Finding 3 removed it — it mislabelled local errors).
	for _, prefix := range []string{"GitExecFailed:", "KustomizeBuildFailed:", "RemoteUnreachable:"} {
		if strings.HasPrefix(msg, prefix) {
			t.Errorf("sanitizeKrustyError must not add a reason-code prefix (Finding 3), got: %q", msg)
		}
	}
}

// TestGitMissingError_EndToEndFailClosed verifies that Render() propagates the
// error through the full krustyRunHook path when git is absent.
func TestGitMissingError_EndToEndFailClosed(t *testing.T) {
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
	// The actionable git-missing detail is preserved (it carries no credentials).
	msg := err.Error()
	if !strings.Contains(msg, "no 'git' program on path") {
		t.Errorf("Render error should preserve the actionable git-missing detail, got: %q", msg)
	}
}

// gitNotFoundSimulatorError is a minimal error type that simulates krusty's
// "no 'git' program on path" error without requiring any real process execution.
type gitNotFoundSimulatorError struct{ msg string }

func (e *gitNotFoundSimulatorError) Error() string { return e.msg }
