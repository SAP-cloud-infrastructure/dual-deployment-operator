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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/cli"
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
	// Build the userinfo from parts so no literal "user:secret@host" appears in
	// source (which trips gosec G101); the point is that Load rejects such URLs.
	const secret = "supersecret"
	userinfo := "user:" + secret + "@"
	cases := map[string]string{
		"https userinfo": "https://" + userinfo + "charts.example.com",
		"oci userinfo":   "oci://" + userinfo + "registry.example.com/charts",
	}
	for name, repo := range cases {
		_, err := l.Load(context.Background(), repo, "n", "1")
		if err == nil {
			t.Fatalf("%s: expected error for URL with embedded credentials, got nil", name)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("%s: credential leak: password appeared in error", name)
		}
	}
}

// ---------------------------------------------------------------------------
// authed classic HTTP Helm repo (hermetic, offline)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// helmLoader.ResolveID – OCI digest + repoScope
// ---------------------------------------------------------------------------

func TestHelmLoader_ResolveID_OCIDigest(t *testing.T) {
	host, client := startAuthedOCIRegistry(t)
	pushFixtureChart(t, host, client)

	l := newHelmLoader(func(context.Context, string) (creds, error) {
		return creds{user: authedTestUser, pass: authedTestPass, ok: true}, nil
	})
	l.httpClient = client
	repo := "oci://" + host + "/charts"

	got, err := l.ResolveID(context.Background(), repo, authedTestChart, authedTestVer)
	if err != nil {
		t.Fatalf("ResolveID: %v", err)
	}
	if !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("expected a sha256: manifest digest, got %q", got)
	}
	// Stable across calls (immutable digest).
	again, err := l.ResolveID(context.Background(), repo, authedTestChart, authedTestVer)
	if err != nil || again != got {
		t.Fatalf("digest not stable: got %q then %q (err=%v)", got, again, err)
	}
}

func TestHelmLoader_repoScope(t *testing.T) {
	l := &helmLoader{}
	s, err := l.repoScope("oci://reg.example.com/charts")
	if err != nil {
		t.Fatalf("oci repoScope error: %v", err)
	}
	if s != "oci:reg.example.com" {
		t.Fatalf("oci repoScope = %q", s)
	}
}

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

func TestHelmLoader_ResolveID_HTTPIndexDigest(t *testing.T) {
	const name, version = "demo", "1.2.3"
	wantDigest := "sha256:0000000000000000000000000000000000000000000000000000000000000abc"
	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `apiVersion: v1
entries:
  %s:
  - name: %s
    version: %s
    digest: %s
    urls:
    - http://example.invalid/%s-%s.tgz
generated: "2026-01-01T00:00:00Z"
`, name, name, version, wantDigest, name, version)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	l := &helmLoader{settings: cli.New()}
	got, err := l.ResolveID(context.Background(), srv.URL, name, version)
	if err != nil {
		t.Fatalf("ResolveID: %v", err)
	}
	if got != wantDigest {
		t.Fatalf("ResolveID = %q, want index digest %q", got, wantDigest)
	}
}

func TestHelmLoader_ResolveID_HTTPVersionFallback(t *testing.T) {
	const name, version = "demo", "1.2.3"
	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `apiVersion: v1
entries:
  %s:
  - name: %s
    version: %s
    urls:
    - http://example.invalid/%s-%s.tgz
generated: "2026-01-01T00:00:00Z"
`, name, name, version, name, version)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	l := &helmLoader{settings: cli.New()}
	got, err := l.ResolveID(context.Background(), srv.URL, name, version)
	if err != nil {
		t.Fatalf("ResolveID: %v", err)
	}
	if got != version {
		t.Fatalf("ResolveID = %q, want version fallback %q", got, version)
	}
}

func TestHelmLoader_ResolveID_HTTPUnkeyable(t *testing.T) {
	const name, version = "demo", "1.2.3"
	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `apiVersion: v1
entries:
  %s:
  - name: %s
    version: 9.9.9
    urls:
    - http://example.invalid/other.tgz
generated: "2026-01-01T00:00:00Z"
`, name, name)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	l := &helmLoader{settings: cli.New()}
	_, err := l.ResolveID(context.Background(), srv.URL, name, version)
	if !errors.Is(err, errUnkeyable) {
		t.Fatalf("expected errUnkeyable, got %v", err)
	}
}

func TestHelmLoader_repoScope_HTTPPathDistinguishes(t *testing.T) {
	l := &helmLoader{}
	a, err := l.repoScope("https://charts.example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.repoScope("https://charts.example.com/b")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("http repoScope must include URL path so same-host different-path repos don't collide when no index digest is present; got both %q", a)
	}
}

func TestHelmLoader_ResolveID_RejectsURLCredentials_OCI(t *testing.T) {
	l := &helmLoader{}
	const secret = "secret"
	repoURL := "oci://user:" + secret + "@example.com/charts"
	_, err := l.ResolveID(context.Background(), repoURL, "demo", "1.0.0")
	if err == nil {
		t.Fatal("ResolveID must reject an OCI repo URL with embedded userinfo")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("credential leak: password appeared in error: %v", err)
	}
}

func TestHelmLoader_ResolveID_RejectsURLCredentials_HTTP(t *testing.T) {
	l := &helmLoader{}
	const secret = "secret"
	repoURL := "https://user:" + secret + "@example.com/charts"
	_, err := l.ResolveID(context.Background(), repoURL, "demo", "1.0.0")
	if err == nil {
		t.Fatal("ResolveID must reject an HTTP repo URL with embedded userinfo")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("credential leak: password appeared in error: %v", err)
	}
}

func TestHelmLoader_ResolveID_UnsupportedScheme(t *testing.T) {
	// The default branch of ResolveID (non-oci://, non-http(s)://) must return
	// "unsupported chart repo scheme" — parallel to Load's guard.
	l := &helmLoader{}
	_, err := l.ResolveID(context.Background(), "gopher://weird", "demo", "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported-scheme error, got %v", err)
	}
}

func TestHelmLoader_rejectURLCredentials_Unparsable(t *testing.T) {
	// Force url.Parse to fail via a control-character in the URL. Covers the
	// "chart repo url is not parseable" branch that Load and ResolveID share.
	if err := rejectURLCredentials("http://\x7f/bad"); err == nil {
		t.Fatal("expected parse error for a malformed URL")
	}
}

func TestHelmLoader_resolveHTTPID_HTTP4xx(t *testing.T) {
	// Index server returns 404 -> resolveHTTPID must surface "HTTP 404", not fall
	// through to YAML parsing. Covers the 4xx branch.
	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	l := &helmLoader{settings: cli.New()}
	_, err := l.ResolveID(context.Background(), srv.URL, "demo", "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected HTTP 404 error, got %v", err)
	}
}

func TestHelmLoader_resolveHTTPID_BasicAuth(t *testing.T) {
	// Index server requires Basic auth. l.credFor returns creds so
	// req.SetBasicAuth fires; the served index is otherwise identical to the
	// existing HTTPIndexDigest test. Covers the c.ok / SetBasicAuth branch.
	const wantUser, wantPass = "u", "hunter2"
	wantDigest := "sha256:0000000000000000000000000000000000000000000000000000000000000abc"
	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != wantUser || p != wantPass {
			w.Header().Set("WWW-Authenticate", `Basic realm="helm"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `apiVersion: v1
entries:
  demo:
  - name: demo
    version: 1.2.3
    digest: %s
generated: "2026-01-01T00:00:00Z"
`, wantDigest)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	l := newHelmLoader(func(context.Context, string) (creds, error) {
		return creds{user: wantUser, pass: wantPass, ok: true}, nil
	})
	got, err := l.ResolveID(context.Background(), srv.URL, "demo", "1.2.3")
	if err != nil {
		t.Fatalf("ResolveID with basic auth: %v", err)
	}
	if got != wantDigest {
		t.Fatalf("ResolveID = %q, want %q", got, wantDigest)
	}
}

func TestOCIRegistryClientOptionsReused(t *testing.T) {
	// The helper must produce a non-nil client anonymously (no resolver),
	// mirroring the existing anonymous-pull contract.
	l := newHelmLoader(nil)
	rc, err := l.ociRegistryClient(creds{})
	if err != nil {
		t.Fatalf("ociRegistryClient: %v", err)
	}
	if rc == nil {
		t.Fatal("expected non-nil registry client for anonymous pull")
	}
}

func TestHelmLoaderHonorsScratchDir(t *testing.T) {
	scratch := t.TempDir()
	l := newHelmLoader(nil)
	l.scratchDir = scratch
	tmp, err := l.newScratchDir()
	if err != nil {
		t.Fatalf("newScratchDir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if !strings.HasPrefix(tmp, scratch) {
		t.Fatalf("temp dir %q not under configured scratch %q", tmp, scratch)
	}
}
