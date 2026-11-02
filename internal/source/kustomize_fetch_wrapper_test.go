// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

// Task 3 — fail-closed sanitized fetch wrapper (TDD)
//
// This file contains the RED→GREEN TDD cycle for the sanitizing error wrapper
// around krusty.Run. Tests verify:
//
//  1. A krusty.Run failure whose error message contains a raw repository URL
//     is mapped to a stable, sanitized render error (no URL echoed back).
//  2. A krusty.Run failure whose error message contains embedded HTTP credentials
//     (user:token@host pattern) is sanitized — no credential echoed back.
//  3. A krusty.Run failure with an HTTP-error message (non-2xx response) is
//     mapped to a stable render error — no status code / URL detail leaked beyond
//     what is safe.
//  4. Any krusty.Run failure always produces a render error (fail-closed) —
//     never a nil error with a silent empty/partial result.
//  5. sanitizeKrustyError is a no-op on nil (success path is unaffected).
//
// Strategy: the production hook krustyRunHook (package-level, nil in production)
// allows tests to inject a synthetic krusty error without network access.

package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers shared by Task-3 tests
// ---------------------------------------------------------------------------

// buildMinimalKustomizeRoot writes a kustomization.yaml with no remote refs
// into a temp directory. krust.Run will succeed on it (purely local). Tests
// that need krust.Run to FAIL inject an error via krustyRunHook instead.
func buildMinimalKustomizeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	content := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`
	if err := os.WriteFile(filepath.Join(root, "kustomization.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// buildKustomizeSourceForRoot returns a kustomizeSource whose Resolve returns
// root directly (SeedPath="" → filepath.Join(root, "") = root).
func buildKustomizeSourceForRoot(t *testing.T, root string) *kustomizeSource {
	t.Helper()
	spec := fakeKustomizeSpec(root)
	return &kustomizeSource{
		spec:                        &spec,
		resolver:                    fakeRootResolver{baseDir: root},
		requireSHAPinnedRemoteBases: false,
	}
}

// withKrustyRunHook installs a one-shot hook that makes krusty.Run return
// syntheticErr. Restores the previous hook on test cleanup.
func withKrustyRunHook(t *testing.T, syntheticErr error) {
	t.Helper()
	old := krustyRunHook
	krustyRunHook = syntheticErr
	t.Cleanup(func() { krustyRunHook = old })
}

// ---------------------------------------------------------------------------
// 3.1 RED: sanitizeKrustyError — unit tests (function-level)
// ---------------------------------------------------------------------------

// TestSanitizeKrustyError_NilPassthrough verifies that a nil error is returned
// unchanged (success path unaffected).
func TestSanitizeKrustyError_NilPassthrough(t *testing.T) {
	if got := sanitizeKrustyError(nil); got != nil {
		t.Errorf("sanitizeKrustyError(nil) = %v, want nil", got)
	}
}

// TestSanitizeKrustyError_GitExecURLNotEchoed verifies that a git-exec error
// containing a raw repository URL is returned as a sanitized fixed-text error.
// The raw URL must not appear in the sanitized error message.
func TestSanitizeKrustyError_GitExecURLNotEchoed(t *testing.T) {
	// Simulate a krusty git-exec failure: "failed to run 'git fetch --depth=1
	// https://secret-internal.corp.example/infra/repo HEAD': ..."
	rawErr := fmt.Errorf("failed to run 'git fetch --depth=1 https://secret-internal.corp.example/infra/repo HEAD': exit status 128\nfatal: unable to access 'https://secret-internal.corp.example/infra/repo/': Could not resolve host: secret-internal.corp.example")

	sanitized := sanitizeKrustyError(rawErr)
	if sanitized == nil {
		t.Fatal("sanitizeKrustyError: expected non-nil error for git-exec failure, got nil")
	}

	msg := sanitized.Error()

	// The sanitized error must not echo the raw URL.
	if strings.Contains(msg, "secret-internal.corp.example") {
		t.Errorf("sanitized error leaks hostname: %q", msg)
	}
	if strings.Contains(msg, "infra/repo") {
		t.Errorf("sanitized error leaks repo path: %q", msg)
	}
	// The error must still be recognisable as a remote-fetch failure.
	if !strings.Contains(msg, "remote") && !strings.Contains(msg, "fetch") && !strings.Contains(msg, "build") {
		t.Errorf("sanitized error should describe the failure kind, got: %q", msg)
	}
}

// TestSanitizeKrustyError_CredentialBearingURLNotEchoed verifies that a raw
// error message containing an embedded credential (user:token@host pattern)
// is sanitized so no credential appears in the output.
func TestSanitizeKrustyError_CredentialBearingURLNotEchoed(t *testing.T) {
	// Simulate a scenario where the raw error somehow carries embedded creds
	// (e.g. from a mis-configured authSecretRef that ended up in a URL).
	rawErr := fmt.Errorf("failed to run 'git fetch --depth=1 https://myuser:SUPERSECRET@github.com/org/repo HEAD': exit status 128\nfatal: repository not found")

	sanitized := sanitizeKrustyError(rawErr)
	if sanitized == nil {
		t.Fatal("sanitizeKrustyError: expected non-nil error, got nil")
	}

	msg := sanitized.Error()
	if strings.Contains(msg, "SUPERSECRET") {
		t.Errorf("sanitized error leaks credential: %q", msg)
	}
	if strings.Contains(msg, "myuser") {
		t.Errorf("sanitized error leaks username: %q", msg)
	}
	if strings.Contains(msg, "github.com") {
		t.Errorf("sanitized error leaks hostname: %q", msg)
	}
}

// TestSanitizeKrustyError_HTTPErrorSanitized verifies that an HTTP loader error
// (non-2xx response) is mapped to a stable sanitized message. The raw URL and
// status-code details that might reveal internal routing are not echoed.
func TestSanitizeKrustyError_HTTPErrorSanitized(t *testing.T) {
	// Simulate the ErrHTTP format from krusty's internal loader:
	// "HTTP Error: status code 403 (Forbidden)" — this doesn't contain a URL
	// directly but the wrapping chain might. We verify sanitization still applies.
	rawErr := fmt.Errorf("HTTP Error: status code 403 (Forbidden) fetching https://internal-registry.corp.example/manifests/secret.yaml")

	sanitized := sanitizeKrustyError(rawErr)
	if sanitized == nil {
		t.Fatal("sanitizeKrustyError: expected non-nil error, got nil")
	}

	msg := sanitized.Error()
	if strings.Contains(msg, "internal-registry.corp.example") {
		t.Errorf("sanitized error leaks hostname: %q", msg)
	}
	if strings.Contains(msg, "secret.yaml") {
		t.Errorf("sanitized error leaks file path: %q", msg)
	}
}

// TestSanitizeKrustyError_NonFetchErrorPassedThrough verifies that a krusty
// error that is NOT a remote-fetch failure (e.g. a local YAML parse error, a
// missing resource file) is also sanitized — we apply a uniform sanitization
// policy so that any krusty error is safe to surface.
func TestSanitizeKrustyError_NonFetchErrorPassedThrough(t *testing.T) {
	// A local YAML error from krusty — should still be sanitized (no raw content)
	// but the error must not be nil.
	rawErr := errors.New("kustomization.yaml: error converting YAML to JSON: yaml: unmarshal errors")

	sanitized := sanitizeKrustyError(rawErr)
	if sanitized == nil {
		t.Fatal("sanitizeKrustyError: expected non-nil error for krusty build error, got nil")
	}
}

// ---------------------------------------------------------------------------
// 3.1 RED: end-to-end wrapper tests via krustyRunHook
// ---------------------------------------------------------------------------

// TestKrustyRunWrapper_FetchFailureProducesRenderError verifies that when
// krust.Run returns a raw git-exec error containing a URL, Render() propagates
// a render error (not nil) and the URL is not present in the error message.
func TestKrustyRunWrapper_FetchFailureProducesRenderError(t *testing.T) {
	root := buildMinimalKustomizeRoot(t)
	src := buildKustomizeSourceForRoot(t, root)

	// Inject a raw git-exec error containing a URL.
	syntheticErr := fmt.Errorf("failed to run 'git fetch --depth=1 https://private.corp.example/sensitive/repo HEAD': exit status 128\nfatal: unable to access 'https://private.corp.example/sensitive/repo/': Could not resolve host")
	withKrustyRunHook(t, syntheticErr)

	ms, err := src.Render(context.Background(), ModeSeed, "ns")

	// Must fail — never a silent partial render.
	if err == nil {
		t.Fatal("Render: expected error for krusty failure, got nil (silent partial render — FAIL)")
	}
	// Must not return a non-nil manifests slice alongside the error.
	if ms != nil {
		t.Errorf("Render: expected nil manifests on failure, got %d manifests", len(ms))
	}
	// The error message must not echo the raw URL.
	msg := err.Error()
	if strings.Contains(msg, "private.corp.example") {
		t.Errorf("Render error leaks hostname in message: %q", msg)
	}
	if strings.Contains(msg, "sensitive/repo") {
		t.Errorf("Render error leaks repo path in message: %q", msg)
	}
}

// TestKrustyRunWrapper_CredentialBearingFetchError verifies that a fetch error
// containing embedded credentials is sanitized end-to-end through Render().
func TestKrustyRunWrapper_CredentialBearingFetchError(t *testing.T) {
	root := buildMinimalKustomizeRoot(t)
	src := buildKustomizeSourceForRoot(t, root)

	syntheticErr := fmt.Errorf("failed to run 'git fetch --depth=1 https://deploy-token:ghp_TOKENSECRET123@github.com/org/private-repo HEAD': exit status 128")
	withKrustyRunHook(t, syntheticErr)

	_, err := src.Render(context.Background(), ModeSeed, "ns")

	if err == nil {
		t.Fatal("Render: expected error for krusty failure, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "ghp_TOKENSECRET123") {
		t.Errorf("Render error leaks token: %q", msg)
	}
	if strings.Contains(msg, "deploy-token") {
		t.Errorf("Render error leaks username: %q", msg)
	}
	if strings.Contains(msg, "private-repo") {
		t.Errorf("Render error leaks repo name: %q", msg)
	}
}

// TestKrustyRunWrapper_HTTPNon2xxProducesRenderError verifies that an HTTP
// non-2xx error from krusty's file loader produces a render error (fail-closed).
func TestKrustyRunWrapper_HTTPNon2xxProducesRenderError(t *testing.T) {
	root := buildMinimalKustomizeRoot(t)
	src := buildKustomizeSourceForRoot(t, root)

	// Simulate krusty's ErrHTTP error wrapping pattern.
	syntheticErr := fmt.Errorf("HTTP Error: status code 404 (Not Found) while fetching https://cdn.internal.corp.example/bases/v2/kustomization.yaml")
	withKrustyRunHook(t, syntheticErr)

	_, err := src.Render(context.Background(), ModeSeed, "ns")

	if err == nil {
		t.Fatal("Render: expected error for HTTP 404, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "cdn.internal.corp.example") {
		t.Errorf("Render error leaks internal hostname: %q", msg)
	}
	if strings.Contains(msg, "bases/v2/kustomization.yaml") {
		t.Errorf("Render error leaks internal path: %q", msg)
	}
}

// TestKrustyRunWrapper_SuccessUnaffected verifies that a successful krust.Run
// (nil error from the hook) does not get corrupted by the wrapper — Render
// still returns the parsed manifests without error.
func TestKrustyRunWrapper_SuccessUnaffected(t *testing.T) {
	root := buildMinimalKustomizeRoot(t)
	src := buildKustomizeSourceForRoot(t, root)

	// Hook returns nil — krust.Run is expected to succeed on a local-only tree.
	// We don't install any hook here; real krust.Run runs on the local temp dir.
	_, err := src.Render(context.Background(), ModeSeed, "ns")
	if err != nil {
		t.Fatalf("Render: unexpected error for local-only kustomization: %v", err)
	}
}

// TestKrustyRunWrapper_FailClosedNeverSilentPartialRender is a broad invariant
// test: any non-nil error returned from krust.Run must result in a non-nil
// error from Render(), regardless of what partial state krusty may have
// produced internally. Tests multiple synthetic error variants.
func TestKrustyRunWrapper_FailClosedNeverSilentPartialRender(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"git-not-found", fmt.Errorf("no 'git' program on path")},
		{"git-clone-fail", fmt.Errorf("failed to run 'git clone https://repo.example.com/org/r': exit status 128")},
		{"http-error", fmt.Errorf("HTTP Error: status code 500 (Internal Server Error)")},
		{"generic-error", errors.New("unexpected krusty internal failure")},
		{"nil-error", nil}, // control: nil → no error from Render
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := buildMinimalKustomizeRoot(t)
			src := buildKustomizeSourceForRoot(t, root)

			if tc.err != nil {
				withKrustyRunHook(t, tc.err)
				_, renderErr := src.Render(context.Background(), ModeSeed, "ns")
				if renderErr == nil {
					t.Errorf("case %q: Render returned nil error for krusty failure — silent partial render", tc.name)
				}
			} else {
				// nil synthetic error → real krust.Run on local tree → must succeed
				_, renderErr := src.Render(context.Background(), ModeSeed, "ns")
				if renderErr != nil {
					t.Errorf("case %q: Render returned unexpected error: %v", tc.name, renderErr)
				}
			}
		})
	}
}
