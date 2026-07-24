//go:build online

// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

// TestHelmLoaderOCIAuthed exercises the production helmLoader against a real OCI
// registry that requires authentication. It is gated behind:
//   - //go:build online  (compile-time: only included under `go test -tags online`)
//   - requireOnline(t)   (runtime: skips unless DDO_ONLINE_TESTS=1)
//   - env gate           (runtime: skips unless DDO_TEST_OCI_* vars are all set)
//
// Required env vars:
//   DDO_TEST_OCI_URL      oci:// URL prefix of the registry (e.g. oci://registry.example.com/project)
//   DDO_TEST_OCI_USER     username for basic auth
//   DDO_TEST_OCI_PASS     password / token for basic auth
//   DDO_TEST_OCI_CHART    chart name within the registry
//   DDO_TEST_OCI_VERSION  chart version to pull
//
// Credential-leak safety: also asserts that a wrong-password variant returns an
// error whose text contains neither the username nor the password.

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestHelmLoaderOCIAuthed(t *testing.T) {
	requireOnline(t)

	ociURL := os.Getenv("DDO_TEST_OCI_URL")
	ociUser := os.Getenv("DDO_TEST_OCI_USER")
	ociPass := os.Getenv("DDO_TEST_OCI_PASS")
	ociChart := os.Getenv("DDO_TEST_OCI_CHART")
	ociVersion := os.Getenv("DDO_TEST_OCI_VERSION")

	if ociURL == "" || ociUser == "" || ociPass == "" || ociChart == "" || ociVersion == "" {
		t.Skip("set DDO_TEST_OCI_URL, DDO_TEST_OCI_USER, DDO_TEST_OCI_PASS, DDO_TEST_OCI_CHART, " +
			"DDO_TEST_OCI_VERSION to run authenticated OCI pull test")
	}

	ctx := context.Background()

	// --- authenticated pull: should succeed ---
	loader := newHelmLoader(func(_ context.Context, _ string) (creds, error) {
		return creds{user: ociUser, pass: ociPass, ok: true}, nil
	})
	ch, err := loader.Load(ctx, ociURL, ociChart, ociVersion)
	if err != nil {
		t.Fatalf("authed OCI pull: %v", err)
	}
	if ch == nil || ch.Metadata == nil || ch.Metadata.Name == "" {
		t.Fatal("authed OCI pull: expected a non-empty parsed chart")
	}

	// --- wrong-password variant: must fail, must not leak credentials ---
	badLoader := newHelmLoader(func(_ context.Context, _ string) (creds, error) {
		return creds{user: ociUser, pass: "wrong-password-intentionally-invalid", ok: true}, nil
	})
	_, badErr := badLoader.Load(ctx, ociURL, ociChart, ociVersion)
	if badErr == nil {
		t.Fatal("wrong-password OCI pull: expected an error, got nil")
	}
	if strings.Contains(badErr.Error(), ociUser) {
		t.Fatal("credential leak: OCI username appeared in the error string")
	}
	if strings.Contains(badErr.Error(), ociPass) {
		t.Fatal("credential leak: OCI password appeared in the error string")
	}
}
