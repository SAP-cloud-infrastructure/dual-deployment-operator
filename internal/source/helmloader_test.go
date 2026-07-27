// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// findTGZ
// ---------------------------------------------------------------------------

func TestFindTGZ(t *testing.T) {
	t.Run("finds tgz when present", func(t *testing.T) {
		dir := t.TempDir()
		tgzPath := filepath.Join(dir, "foo-1.2.3.tgz")
		if err := os.WriteFile(tgzPath, []byte("fake chart"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Also add a non-tgz file to ensure filtering works.
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("readme"), 0o600); err != nil {
			t.Fatal(err)
		}

		got, err := findTGZ(dir)
		if err != nil {
			t.Fatalf("findTGZ: unexpected error: %v", err)
		}
		if got != tgzPath {
			t.Fatalf("findTGZ = %q, want %q", got, tgzPath)
		}
	})

	t.Run("errors when no tgz present", func(t *testing.T) {
		dir := t.TempDir()
		// Put a non-tgz file so the dir is not empty.
		if err := os.WriteFile(filepath.Join(dir, "something.tar.gz"), []byte("nope"), 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := findTGZ(dir)
		if err == nil {
			t.Fatal("findTGZ: expected error for dir with no .tgz, got nil")
		}
		if !strings.Contains(err.Error(), "no chart tgz") {
			t.Fatalf("findTGZ error = %q, want it to contain %q", err.Error(), "no chart tgz")
		}
	})

	t.Run("errors on empty dir", func(t *testing.T) {
		dir := t.TempDir()
		_, err := findTGZ(dir)
		if err == nil {
			t.Fatal("findTGZ: expected error for empty dir, got nil")
		}
		if !strings.Contains(err.Error(), "no chart tgz") {
			t.Fatalf("findTGZ error = %q, want it to contain %q", err.Error(), "no chart tgz")
		}
	})

	t.Run("errors when dir does not exist (ReadDir fails)", func(t *testing.T) {
		dir := t.TempDir()
		nonExistent := filepath.Join(dir, "does-not-exist")
		_, err := findTGZ(nonExistent)
		if err == nil {
			t.Fatal("findTGZ: expected error for non-existent dir, got nil")
		}
		if !strings.Contains(err.Error(), "read chart dir") {
			t.Fatalf("findTGZ error = %q, want it to contain %q", err.Error(), "read chart dir")
		}
	})
}

// ---------------------------------------------------------------------------
// hostOf
// ---------------------------------------------------------------------------

func TestHostOf(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "valid https URL",
			input: "https://keppel.example/x",
			want:  "keppel.example",
		},
		{
			name:  "valid http URL with path",
			input: "http://charts.example.com/stable",
			want:  "charts.example.com",
		},
		{
			name:  "unparsable URL returns input unchanged",
			input: "not a url",
			want:  "not a url",
		},
		{
			name:  "URL with no host falls back to input",
			input: "relative/path",
			want:  "relative/path",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hostOf(tc.input)
			if got != tc.want {
				t.Fatalf("hostOf(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// splitRef
// ---------------------------------------------------------------------------

func TestSplitRef(t *testing.T) {
	t.Run("URL with // root subpath extracts cloneURL and rootSubPath", func(t *testing.T) {
		cloneURL, rootSubPath, ref, err := splitRef("https://x/y//p?ref=v1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cloneURL != "https://x/y" {
			t.Fatalf("cloneURL = %q, want %q", cloneURL, "https://x/y")
		}
		if rootSubPath != "p" {
			t.Fatalf("rootSubPath = %q, want %q", rootSubPath, "p")
		}
		if ref != "v1" {
			t.Fatalf("ref = %q, want %q", ref, "v1")
		}
	})

	t.Run("URL without // has empty rootSubPath", func(t *testing.T) {
		cloneURL, rootSubPath, ref, err := splitRef("https://x/y?ref=v2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cloneURL != "https://x/y" {
			t.Fatalf("cloneURL = %q, want %q", cloneURL, "https://x/y")
		}
		if rootSubPath != "" {
			t.Fatalf("rootSubPath = %q, want empty string", rootSubPath)
		}
		if ref != "v2" {
			t.Fatalf("ref = %q, want %q", ref, "v2")
		}
	})

	t.Run("missing ref query param", func(t *testing.T) {
		_, _, _, err := splitRef("https://x/y//p")
		if err == nil {
			t.Fatal("expected error for missing ?ref=, got nil")
		}
		if !strings.Contains(err.Error(), "missing pinned ?ref=") {
			t.Fatalf("error = %q, want it to contain %q", err.Error(), "missing pinned ?ref=")
		}
	})

	t.Run("malformed URL fails parse", func(t *testing.T) {
		_, _, _, err := splitRef("://bad")
		if err == nil {
			t.Fatal("expected error for malformed URL, got nil")
		}
		if !strings.Contains(err.Error(), "parse kustomize url") {
			t.Fatalf("error = %q, want it to contain %q", err.Error(), "parse kustomize url")
		}
	})

	t.Run("URL with userinfo is rejected", func(t *testing.T) {
		_, _, _, err := splitRef("https://user:pw@host/repo?ref=v1")
		if err == nil {
			t.Fatal("expected error for URL with embedded userinfo, got nil")
		}
		if strings.Contains(err.Error(), "pw") {
			t.Fatal("credential leak: password appeared in userinfo rejection error")
		}
	})
}

// ---------------------------------------------------------------------------
// withGitCreds
// ---------------------------------------------------------------------------

func TestWithGitCredsBindsFreshResolver(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Data:       map[string][]byte{"token": []byte("tok")},
	}).Build()

	cr := &CredentialResolver{Client: cl, Namespace: "ns"}
	ref := &v1alpha1.SecretReference{Name: "s"}

	gr := &gitResolver{} // original: resolve is nil
	got := withGitCreds(gr, cr, ref)

	// Must return a DIFFERENT pointer (fresh copy, not mutation of shared instance).
	gotGR, ok := got.(*gitResolver)
	if !ok {
		t.Fatalf("withGitCreds returned %T, want *gitResolver", got)
	}
	if gotGR == gr {
		t.Fatal("withGitCreds: returned same pointer; expected a fresh copy")
	}

	// The returned copy must have resolve set.
	if gotGR.resolve == nil {
		t.Fatal("withGitCreds: returned resolver has nil .resolve; credentials not bound")
	}

	// Calling resolve must return the correct credentials from the Secret.
	ctx := context.Background()
	c, err := gotGR.resolve(ctx, "")
	if err != nil {
		t.Fatalf("resolve: unexpected error: %v", err)
	}
	if c.user != "git" || c.pass != "tok" || !c.ok {
		t.Fatalf("resolve returned {user=%q pass=<redacted> ok=%v}, want {user=git pass=<tok> ok=true}",
			c.user, c.ok)
	}

	// The ORIGINAL gr must NOT be mutated.
	if gr.resolve != nil {
		t.Fatal("withGitCreds: mutated the original *gitResolver; expected it to remain unchanged")
	}
}

func TestWithGitCredsNoOpBranches(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	cr := &CredentialResolver{Client: cl, Namespace: "ns"}
	ref := &v1alpha1.SecretReference{Name: "s"}

	gr := &gitResolver{}

	// cr == nil => returns same instance unchanged.
	t.Run("nil cr returns same instance", func(t *testing.T) {
		got := withGitCreds(gr, nil, ref)
		if got != gr {
			t.Fatal("withGitCreds(gr, nil, ref): expected same instance, got different")
		}
	})

	// non-*gitResolver RootResolver => returns the fake unchanged.
	t.Run("non-gitResolver returns same instance", func(t *testing.T) {
		fakeRR := fakeRootResolver{baseDir: "/tmp"}
		got := withGitCreds(fakeRR, cr, ref)
		if got != fakeRR {
			t.Fatal("withGitCreds(fakeRootResolver, cr, ref): expected same fake instance returned")
		}
	})
}

// ---------------------------------------------------------------------------
// CredentialResolver.Resolve
// ---------------------------------------------------------------------------

func TestCredentialResolverResolve(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	existingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mysecret", Namespace: "ns"},
		Data: map[string][]byte{
			"username": []byte("u"),
			"password": []byte("p"),
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingSecret).Build()
	resolver := &CredentialResolver{Client: cl, Namespace: "ns"}
	ctx := context.Background()

	t.Run("nil ref returns anonymous creds", func(t *testing.T) {
		c, err := resolver.Resolve(ctx, nil)
		if err != nil {
			t.Fatalf("Resolve(nil): unexpected error: %v", err)
		}
		if c.ok {
			t.Fatal("Resolve(nil): expected anonymous creds (ok=false), got ok=true")
		}
	})

	t.Run("ref to nonexistent Secret returns error", func(t *testing.T) {
		ref := &v1alpha1.SecretReference{Name: "does-not-exist"}
		_, err := resolver.Resolve(ctx, ref)
		if err == nil {
			t.Fatal("Resolve(missing secret): expected error, got nil")
		}
		// The error must not reveal secret data (there is none here, but defensive check).
		if strings.Contains(err.Error(), "does-not-exist-value") {
			t.Fatal("Resolve: credential value appeared in error string")
		}
	})

	t.Run("ref to existing Secret returns correct creds", func(t *testing.T) {
		ref := &v1alpha1.SecretReference{Name: "mysecret"}
		c, err := resolver.Resolve(ctx, ref)
		if err != nil {
			t.Fatalf("Resolve(existing secret): unexpected error: %v", err)
		}
		if c.user != "u" || c.pass != "p" || !c.ok {
			t.Fatalf("Resolve returned {user=%q ok=%v}, want {user=u pass=<p> ok=true}",
				c.user, c.ok)
		}
	})
}

// ---------------------------------------------------------------------------
// helmLoader.Load scheme dispatch
// ---------------------------------------------------------------------------

func TestHelmLoaderRejectsUnknownScheme(t *testing.T) {
	l := newHelmLoader(nil)
	_, err := l.Load(context.Background(), "ftp://nope", "n", "1")
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("expected scheme error, got %v", err)
	}
}

func TestHelmLoaderRejectsURLCredentials(t *testing.T) {
	l := newHelmLoader(nil)
	for _, repo := range []string{
		"https://user:supersecret@charts.example.com",
		"oci://user:supersecret@registry.example.com/charts",
	} {
		_, err := l.Load(context.Background(), repo, "n", "1")
		if err == nil {
			t.Fatalf("expected error for URL with embedded credentials %q, got nil", repo)
		}
		if strings.Contains(err.Error(), "supersecret") {
			t.Fatalf("credential leak: password appeared in error for %q", repo)
		}
	}
}

// ---------------------------------------------------------------------------
// authed classic HTTP Helm repo (hermetic, offline)
// ---------------------------------------------------------------------------

// makeMinimalChartTGZ builds a minimal valid Helm chart tgz (Chart.yaml only)
// in memory and returns its bytes and the digest string for index.yaml.
func makeMinimalChartTGZ(t *testing.T, name, version string) []byte {
	t.Helper()
	chartYAML := fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", name, version)

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name: name + "/Chart.yaml",
		Mode: 0o600,
		Size: int64(len(chartYAML)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(chartYAML)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
