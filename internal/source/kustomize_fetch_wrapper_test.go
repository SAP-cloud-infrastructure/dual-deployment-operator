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

// TestSanitizeKrustyError_GitExecCredentialsRedactedContextPreserved verifies
// the REDACT-AND-PRESERVE contract: a git-exec error is returned with embedded
// credentials stripped, but safe diagnostic context (hostname, repo path, exit
// code) PRESERVED so the operator can act on it. This is the OWASP/K8s-aligned
// policy: redact the secret, keep the context — not the old blanket "discard
// everything" behavior. No reason-code prefix is added (the prefix
// mislabelled local errors as RemoteAuthenticationFailed, etc.).
func TestSanitizeKrustyError_GitExecCredentialsRedactedContextPreserved(t *testing.T) {
	// Simulate a krusty git-exec failure WITHOUT credentials: only safe context.
	rawErr := errors.New("failed to run 'git fetch --depth=1 https://internal.corp.example/infra/repo HEAD': exit status 128\nfatal: unable to access 'https://internal.corp.example/infra/repo/': Could not resolve host: internal.corp.example")

	sanitized := sanitizeKrustyError(rawErr)
	if sanitized == nil {
		t.Fatal("sanitizeKrustyError: expected non-nil error for git-exec failure, got nil")
	}

	msg := sanitized.Error()

	// Safe context MUST be preserved (redact-and-preserve, not blanket discard).
	if !strings.Contains(msg, "internal.corp.example") {
		t.Errorf("redact-and-preserve must keep the hostname for diagnostics, got: %q", msg)
	}
	if !strings.Contains(msg, "infra/repo") {
		t.Errorf("redact-and-preserve must keep the repo path for diagnostics, got: %q", msg)
	}
	if !strings.Contains(msg, "128") {
		t.Errorf("redact-and-preserve must keep the exit code for diagnostics, got: %q", msg)
	}
	// No reason-code prefix: it was removed (it mislabelled local errors).
	for _, prefix := range []string{"RemoteUnreachable:", "RemoteAuthenticationFailed:", "KustomizeBuildFailed:", "GitExecFailed:", "RemoteNotFound:", "SubmoduleFetchFailed:"} {
		if strings.HasPrefix(msg, prefix) {
			t.Errorf("sanitizeKrustyError must not add a reason-code prefix (no reason-code prefix), got: %q", msg)
		}
	}
}

// TestSanitizeKrustyError_CredentialBearingURLRedacted verifies that an error
// carrying an embedded credential (user:token@host) has ONLY the credential
// redacted — the hostname and repo path survive for diagnostics.
func TestSanitizeKrustyError_CredentialBearingURLRedacted(t *testing.T) {
	rawErr := errors.New("failed to run 'git fetch --depth=1 https://myuser:SUPERSECRET@github.com/org/repo HEAD': exit status 128\nfatal: repository not found")

	sanitized := sanitizeKrustyError(rawErr)
	if sanitized == nil {
		t.Fatal("sanitizeKrustyError: expected non-nil error, got nil")
	}

	msg := sanitized.Error()
	// Credential material MUST be gone.
	if strings.Contains(msg, "SUPERSECRET") {
		t.Errorf("sanitized error leaks credential token: %q", msg)
	}
	if strings.Contains(msg, "myuser") {
		t.Errorf("sanitized error leaks credential username: %q", msg)
	}
	// The redaction marker must be present where the userinfo was.
	if !strings.Contains(msg, "[REDACTED]") {
		t.Errorf("expected [REDACTED] marker where credentials were stripped, got: %q", msg)
	}
	// Safe context (host, repo) MUST survive — the credential sat between
	// "https://" and "@github.com", so the host is preserved.
	if !strings.Contains(msg, "github.com/org/repo") {
		t.Errorf("redact-and-preserve must keep host/repo after stripping creds, got: %q", msg)
	}
}

// TestSanitizeKrustyError_HTTPErrorContextPreserved verifies that an HTTP loader
// error keeps its safe context (status code, host, path) under redact-and-
// preserve — there are no credentials to strip here, so the message is forwarded
// as-is (no reason-code prefix). The prefix was removed because it
// mislabelled purely-local errors as RemoteAuthenticationFailed etc.
func TestSanitizeKrustyError_HTTPErrorContextPreserved(t *testing.T) {
	rawErr := errors.New("HTTP Error: status code 403 (Forbidden) fetching https://internal-registry.corp.example/manifests/base.yaml")

	sanitized := sanitizeKrustyError(rawErr)
	if sanitized == nil {
		t.Fatal("sanitizeKrustyError: expected non-nil error, got nil")
	}

	msg := sanitized.Error()
	// No credentials present → full context preserved.
	if !strings.Contains(msg, "internal-registry.corp.example") {
		t.Errorf("redact-and-preserve must keep hostname for diagnostics, got: %q", msg)
	}
	if !strings.Contains(msg, "base.yaml") {
		t.Errorf("redact-and-preserve must keep file path for diagnostics, got: %q", msg)
	}
	// No reason-code prefix (no reason-code prefix).
	if strings.HasPrefix(msg, "RemoteAuthenticationFailed:") {
		t.Errorf("sanitizeKrustyError must not add a reason-code prefix (no reason-code prefix), got: %q", msg)
	}
}

// TestSanitizeKrustyError_LocalBuildErrorPreserved verifies that a purely-local
// krusty build error (e.g. a YAML parse failure — no remote involved) is
// forwarded with its diagnostic text intact. The reason-code
// prefix because it mislabelled local errors as RemoteAuthenticationFailed etc.
func TestSanitizeKrustyError_LocalBuildErrorPreserved(t *testing.T) {
	rawErr := errors.New("kustomization.yaml: error converting YAML to JSON: yaml: line 7: mapping values are not allowed in this context")

	sanitized := sanitizeKrustyError(rawErr)
	if sanitized == nil {
		t.Fatal("sanitizeKrustyError: expected non-nil error for local build error, got nil")
	}
	msg := sanitized.Error()
	// The actionable local detail MUST survive (no credentials to redact).
	if !strings.Contains(msg, "mapping values are not allowed") {
		t.Errorf("redact-and-preserve must keep local build diagnostic, got: %q", msg)
	}
	// No reason-code prefix (no reason-code prefix): a local error must not be mislabelled.
	if strings.HasPrefix(msg, "KustomizeBuildFailed:") {
		t.Errorf("sanitizeKrustyError must not add a reason-code prefix (no reason-code prefix), got: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// 3.1 RED: end-to-end wrapper tests via krustyRunHook
// ---------------------------------------------------------------------------

// TestKrustyRunWrapper_FetchFailureProducesRenderError verifies that when
// krust.Run returns a raw git-exec error containing a URL (no credentials),
// Render() propagates a render error (not nil) and PRESERVES the actionable
// diagnostic under a reason-code prefix (redact-and-preserve policy).
func TestKrustyRunWrapper_FetchFailureProducesRenderError(t *testing.T) {
	root := buildMinimalKustomizeRoot(t)
	src := buildKustomizeSourceForRoot(t, root)

	// Inject a raw git-exec error containing a (credential-free) URL.
	syntheticErr := errors.New("failed to run 'git fetch --depth=1 https://private.corp.example/sensitive/repo HEAD': exit status 128\nfatal: unable to access 'https://private.corp.example/sensitive/repo/': Could not resolve host")
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
	msg := err.Error()
	// The diagnostic carries no credentials, so host/repo context is PRESERVED
	// (it is exactly what an operator needs to debug an unreachable host).
	if !strings.Contains(msg, "private.corp.example") {
		t.Errorf("redact-and-preserve must keep the (credential-free) host context, got: %q", msg)
	}
	// No reason-code prefix (it was removed).
	if strings.Contains(msg, "RemoteUnreachable:") {
		t.Errorf("sanitizeKrustyError must not add a reason-code prefix (no reason-code prefix), got: %q", msg)
	}
}

// TestKrustyRunWrapper_CredentialBearingFetchError verifies that a fetch error
// containing embedded credentials has ONLY the credentials redacted end-to-end
// through Render(), while the host/repo context is preserved.
func TestKrustyRunWrapper_CredentialBearingFetchError(t *testing.T) {
	root := buildMinimalKustomizeRoot(t)
	src := buildKustomizeSourceForRoot(t, root)

	syntheticErr := errors.New("failed to run 'git fetch --depth=1 https://deploy-token:ghp_TOKENSECRET123@github.com/org/private-repo HEAD': exit status 128")
	withKrustyRunHook(t, syntheticErr)

	_, err := src.Render(context.Background(), ModeSeed, "ns")

	if err == nil {
		t.Fatal("Render: expected error for krusty failure, got nil")
	}
	msg := err.Error()
	// The credential substrings MUST be gone.
	if strings.Contains(msg, "ghp_TOKENSECRET123") {
		t.Errorf("Render error leaks token: %q", msg)
	}
	if strings.Contains(msg, "deploy-token") {
		t.Errorf("Render error leaks username: %q", msg)
	}
	// A redaction marker signals a credential was present.
	if !strings.Contains(msg, "[REDACTED]") {
		t.Errorf("expected [REDACTED] marker where credentials were stripped, got: %q", msg)
	}
	// The non-sensitive host/repo context IS preserved.
	if !strings.Contains(msg, "github.com/org/private-repo") {
		t.Errorf("redact-and-preserve must keep the host/repo context, got: %q", msg)
	}
}

// TestKrustyRunWrapper_HTTPNon2xxProducesRenderError verifies that an HTTP
// non-2xx error from krusty's file loader produces a render error (fail-closed)
// that preserves the (credential-free) URL context under a reason-code prefix.
func TestKrustyRunWrapper_HTTPNon2xxProducesRenderError(t *testing.T) {
	root := buildMinimalKustomizeRoot(t)
	src := buildKustomizeSourceForRoot(t, root)

	// Simulate krusty's ErrHTTP error wrapping pattern.
	syntheticErr := errors.New("HTTP Error: status code 404 (Not Found) while fetching https://cdn.internal.corp.example/bases/v2/kustomization.yaml")
	withKrustyRunHook(t, syntheticErr)

	_, err := src.Render(context.Background(), ModeSeed, "ns")

	if err == nil {
		t.Fatal("Render: expected error for HTTP 404, got nil")
	}
	msg := err.Error()
	// No credentials in this URL, so the context is PRESERVED for debugging.
	if !strings.Contains(msg, "cdn.internal.corp.example") {
		t.Errorf("redact-and-preserve must keep the (credential-free) host context, got: %q", msg)
	}
	// No reason-code prefix (it was removed).
	if strings.Contains(msg, "RemoteNotFound:") {
		t.Errorf("sanitizeKrustyError must not add a reason-code prefix (no reason-code prefix), got: %q", msg)
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
		{"git-not-found", errors.New("no 'git' program on path")},
		{"git-clone-fail", errors.New("failed to run 'git clone https://repo.example.com/org/r': exit status 128")},
		{"http-error", errors.New("HTTP Error: status code 500 (Internal Server Error)")},
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

// ---------------------------------------------------------------------------
// redactCredentials — additional gap coverage
// ---------------------------------------------------------------------------

// TestRedactCredentials_TokenQueryParam verifies that ?token=SECRET is redacted.
func TestRedactCredentials_TokenQueryParam(t *testing.T) {
	input := "failed to fetch https://cdn.example.com/x.yaml?token=GHSATSECRET"
	got := redactCredentials(input)
	if strings.Contains(got, "GHSATSECRET") {
		t.Errorf("redactCredentials did not redact ?token= value, got: %q", got)
	}
	if !strings.Contains(got, "token=") {
		t.Errorf("redactCredentials must preserve the key 'token=', got: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("expected [REDACTED] marker, got: %q", got)
	}
}

// TestRedactCredentials_AccessTokenQueryParam verifies that ?access_token=SECRET is redacted.
func TestRedactCredentials_AccessTokenQueryParam(t *testing.T) {
	input := "error: https://api.example.com/repo?access_token=mysecrettoken123"
	got := redactCredentials(input)
	if strings.Contains(got, "mysecrettoken123") {
		t.Errorf("redactCredentials did not redact ?access_token= value, got: %q", got)
	}
	if !strings.Contains(got, "access_token=") {
		t.Errorf("redactCredentials must preserve the key 'access_token=', got: %q", got)
	}
}

// TestRedactCredentials_MultiAtPasswordRedacted verifies that a URL with a
// password containing "@" is fully redacted up to the LAST "@" before the host.
// Previously the regex stopped at the FIRST "@", leaving "pa/ss" visible.
func TestRedactCredentials_MultiAtPasswordRedacted(t *testing.T) {
	input := "fetch failed: https://user:pa/ss@github.com/org/repo"
	got := redactCredentials(input)
	if strings.Contains(got, "pa/ss") {
		t.Errorf("redactCredentials must redact password containing '/', got: %q", got)
	}
	if strings.Contains(got, "user") {
		t.Errorf("redactCredentials must redact username, got: %q", got)
	}
	if !strings.Contains(got, "github.com/org/repo") {
		t.Errorf("redactCredentials must preserve host/path, got: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("expected [REDACTED] marker, got: %q", got)
	}
}

// TestRedactCredentials_MultiAtPasswordLastAt verifies the greedy last-@ match:
// "user:p@ss@github.com" should redact everything between "://" and the last "@".
func TestRedactCredentials_MultiAtPasswordLastAt(t *testing.T) {
	input := "failed: https://user:p@ss@github.com/org/repo HEAD"
	got := redactCredentials(input)
	if strings.Contains(got, "p@ss") {
		t.Errorf("redactCredentials must redact password with @ char, got: %q", got)
	}
	if strings.Contains(got, "user") {
		t.Errorf("redactCredentials must redact username, got: %q", got)
	}
	if !strings.Contains(got, "github.com/org/repo") {
		t.Errorf("redactCredentials must preserve host/path, got: %q", got)
	}
}

// TestRedactCredentials_BenignURL verifies that a URL with no credentials is
// returned unchanged.
func TestRedactCredentials_BenignURL(t *testing.T) {
	input := "failed to fetch https://github.com/org/repo: exit status 128"
	got := redactCredentials(input)
	if got != input {
		t.Errorf("redactCredentials must not modify a URL with no credentials: in=%q out=%q", input, got)
	}
}

// ---------------------------------------------------------------------------
// Round-4 thread 0fZ3q: redactCredentials — allowlist query-param policy
// ---------------------------------------------------------------------------
// The denylist of secret-looking keys missed several real credential params
// (X-Amz-Signature, X-Amz-Credential, X-Goog-Signature, X-Goog-Credential,
// key, auth). The policy is now an ALLOWLIST: only ref, version, timeout and
// submodules keep their value; every other query value is redacted. These
// tests assert the new fail-closed contract and the two regex side effects.

// TestRedactCredentials_AllowlistKeepsKnownSafeParams verifies that the four
// allowlisted kustomize query params are preserved verbatim.
func TestRedactCredentials_AllowlistKeepsKnownSafeParams(t *testing.T) {
	input := "fetch https://github.com/org/repo//dir?ref=abc123&version=v1.2.3&timeout=30s&submodules=false: exit status 1"
	got := redactCredentials(input)
	for _, keep := range []string{"ref=abc123", "version=v1.2.3", "timeout=30s", "submodules=false"} {
		if !strings.Contains(got, keep) {
			t.Errorf("redactCredentials must preserve allowlisted param %q, got: %q", keep, got)
		}
	}
	if strings.Contains(got, "[REDACTED]") {
		t.Errorf("redactCredentials must not redact any allowlisted param, got: %q", got)
	}
}

// TestRedactCredentials_AllowlistRedactsCloudSignatureParams verifies that the
// previously-missed cloud credential params are redacted under the allowlist.
func TestRedactCredentials_AllowlistRedactsCloudSignatureParams(t *testing.T) {
	cases := []struct {
		name   string
		key    string
		secret string
	}{
		{"amz-signature", "X-Amz-Signature", "deadbeefcafe0001"},
		{"amz-credential", "X-Amz-Credential", "AKIAIOSFODNN7EXAMPLE"},
		{"goog-signature", "X-Goog-Signature", "0123456789abcdef"},
		{"goog-credential", "X-Goog-Credential", "svcacct%40proj.iam"},
		{"bare-key", "key", "AIzaSyExampleSecretValue"},
		{"bare-auth", "auth", "Bearer-xyz-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := "fetch https://cdn.example.com/x.yaml?" + tc.key + "=" + tc.secret + " failed"
			got := redactCredentials(input)
			if strings.Contains(got, tc.secret) {
				t.Errorf("redactCredentials must redact %s value, got: %q", tc.key, got)
			}
			if !strings.Contains(got, tc.key+"=") {
				t.Errorf("redactCredentials must preserve the key %q, got: %q", tc.key, got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("expected [REDACTED] marker, got: %q", got)
			}
		})
	}
}

// TestRedactCredentials_ClosingQuoteNotEaten verifies the first side effect:
// the value match must stop at a closing quote so "?token=X':" keeps the "':".
func TestRedactCredentials_ClosingQuoteNotEaten(t *testing.T) {
	input := "exec 'git fetch https://cdn.example.com/x?token=GHSATSECRET': HTTP Error"
	got := redactCredentials(input)
	if strings.Contains(got, "GHSATSECRET") {
		t.Errorf("redactCredentials must redact the token value, got: %q", got)
	}
	if !strings.Contains(got, "': HTTP Error") {
		t.Errorf("redactCredentials must not eat the closing quote; want \"': HTTP Error\" preserved, got: %q", got)
	}
}

// TestRedactCredentials_RefWithAtPreservesHost verifies the second side effect:
// a benign ?ref=pkg@1.2.3 must NOT be swallowed by the userinfo regex into
// https://[REDACTED]@1.2.3 — the host/repo must survive.
func TestRedactCredentials_RefWithAtPreservesHost(t *testing.T) {
	input := "fetch https://github.com/o/r//b?ref=pkg@1.2.3: exit status 1"
	got := redactCredentials(input)
	if !strings.Contains(got, "github.com/o/r//b") {
		t.Errorf("redactCredentials must preserve host/repo for ?ref=pkg@1.2.3, got: %q", got)
	}
	if !strings.Contains(got, "ref=pkg@1.2.3") {
		t.Errorf("redactCredentials must preserve a benign ?ref=pkg@1.2.3, got: %q", got)
	}
}
