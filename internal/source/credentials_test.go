// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCredentialsFromSecretData(t *testing.T) {
	// token wins over password
	c := credsFromSecretData(map[string][]byte{
		"username": []byte("u"), "password": []byte("p"), "token": []byte("tok"),
	})
	if c.user != "u" || c.pass != "tok" {
		t.Fatalf("token must win: got user=%q pass=%q", c.user, c.pass)
	}
	// token only => username defaults to "git" (for git HTTPS PAT)
	c = credsFromSecretData(map[string][]byte{"token": []byte("tok")})
	if c.user != "git" || c.pass != "tok" {
		t.Fatalf("token-only: got user=%q pass=%q", c.user, c.pass)
	}
	// basic auth
	c = credsFromSecretData(map[string][]byte{"username": []byte("u"), "password": []byte("p")})
	if c.user != "u" || c.pass != "p" || !c.ok {
		t.Fatalf("basic: got %+v", c)
	}
	// empty => not ok (anonymous)
	if credsFromSecretData(map[string][]byte{}).ok {
		t.Fatal("empty secret data must resolve to anonymous (ok=false)")
	}
}

// TestHelmLoaderSurfacesCredentialError verifies that a failing authSecretRef resolver
// causes Load to return an error containing "resolve credentials", not silent anonymous.
func TestHelmLoaderSurfacesCredentialError(t *testing.T) {
	ctx := context.Background()
	l := newHelmLoader(func(_ context.Context, _ string) (creds, error) {
		return creds{}, errors.New("boom")
	})
	_, err := l.Load(ctx, "oci://registry.example.com/charts", "mychart", "1.0.0")
	if err == nil {
		t.Fatal("expected error when credential resolve fails, got nil")
	}
	if !strings.Contains(err.Error(), "resolve credentials") {
		t.Fatalf("error must contain %q, got: %v", "resolve credentials", err)
	}
}

// TestHelmLoaderAnonymousOnNilResolver verifies that a nil resolver (no authSecretRef)
// does NOT fail at the credential step (it will fail on the actual network fetch, not before).
func TestHelmLoaderAnonymousOnNilResolver(t *testing.T) {
	ctx := context.Background()
	l := newHelmLoader(nil)
	_, err := l.Load(ctx, "oci://registry.example.com/charts", "mychart", "1.0.0")
	if err != nil && strings.Contains(err.Error(), "resolve credentials") {
		t.Fatalf("nil resolver must not produce a credential error, got: %v", err)
	}
	// err may be non-nil (network failure) — that's fine; the credential step must not be the cause.
}

// TestGitResolverSurfacesCredentialError verifies that a failing authSecretRef resolver
// causes Resolve to return an error containing "resolve credentials", not silent anonymous.
func TestGitResolverSurfacesCredentialError(t *testing.T) {
	ctx := context.Background()
	r := &gitResolver{
		resolve: func(_ context.Context, _ string) (creds, error) {
			return creds{}, errors.New("boom")
		},
	}
	_, cleanup, err := r.Resolve(ctx, "https://example.com/org/repo?ref=v1.0.0", "")
	if err == nil {
		t.Fatal("expected error when credential resolve fails, got nil")
	}
	if !strings.Contains(err.Error(), "resolve credentials") {
		t.Fatalf("error must contain %q, got: %v", "resolve credentials", err)
	}
	// cleanup must be safe to call even on error.
	cleanup()
}

// TestGitResolverAnonymousOnNilResolver verifies that a nil resolver (no authSecretRef)
// does NOT fail at the credential step.
func TestGitResolverAnonymousOnNilResolver(t *testing.T) {
	ctx := context.Background()
	r := &gitResolver{} // nil resolve
	_, cleanup, err := r.Resolve(ctx, "https://example.com/org/repo?ref=v1.0.0", "")
	if err != nil && strings.Contains(err.Error(), "resolve credentials") {
		t.Fatalf("nil resolver must not produce a credential error, got: %v", err)
	}
	// err may be non-nil (network failure) — that's fine.
	cleanup()
}
