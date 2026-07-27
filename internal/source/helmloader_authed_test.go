// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

// TestHelmLoaderOCIAuthed exercises the production helmLoader's OCI pull path against
// an authenticated registry, fully hermetically and with zero extra module deps: it
// runs a hand-rolled in-process OCI-distribution registry (a content-addressable
// byte store gated by HTTP Basic auth) behind a httptest TLS server, pushes a fixture
// chart through helm's own registry client, then pulls it back through the production
// loader. This mirrors the in-process hermetic authed-git test, so all authenticated
// source paths (HTTP repo, OCI, git) run in the default test job with no external
// services.
//
// The loader's unexported httpClient seam is set to the httptest server's own
// cert-trusting client so the self-signed TLS handshake succeeds; production leaves
// that field nil. TLS is required because helm/ORAS refuse to forward Basic
// credentials over plain HTTP (GHSA-vh4v-2xq2-g5cg); the classic HTTP-repo auth path
// (TestHelmLoaderAuthedHTTPRepo) has no such restriction and needs no TLS.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/registry"
)

const (
	authedTestUser  = "ddo-test-user"
	authedTestPass  = "ddo-test-pass"
	authedTestChart = "ddo-fixture"
	authedTestVer   = "0.1.0"
)

// ociRegistry is a minimal in-memory OCI-distribution registry sufficient for helm's
// push+pull: content-addressable blobs/manifests keyed by digest, plus tag->digest
// resolution, all gated by HTTP Basic auth. Not a general registry; test-only.
type ociRegistry struct {
	user, pass string

	mu      sync.Mutex
	blobs   map[string][]byte // digest -> content (blobs and manifests)
	tags    map[string]string // "<repo>:<tag>" -> manifest digest
	ct      map[string]string // manifest digest -> content type
	uploads map[string]struct{}
}

func newOCIRegistry(user, pass string) *ociRegistry {
	return &ociRegistry{
		user: user, pass: pass,
		blobs:   map[string][]byte{},
		tags:    map[string]string{},
		ct:      map[string]string{},
		uploads: map[string]struct{}{},
	}
}

func digestOf(b []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }

func (r *ociRegistry) authOK(req *http.Request) bool {
	u, p, ok := req.BasicAuth()
	return ok && u == r.user && p == r.pass
}

func (r *ociRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !r.authOK(req) {
		w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	p := req.URL.Path
	switch {
	case p == "/v2/" || p == "/v2":
		w.WriteHeader(http.StatusOK)

	case strings.HasSuffix(p, "/blobs/uploads/") && req.Method == http.MethodPost:
		id := digestOf(fmt.Appendf(nil, "%s-%d", p, len(r.uploads)))
		r.mu.Lock()
		r.uploads[id] = struct{}{}
		r.mu.Unlock()
		w.Header().Set("Location", "/v2/uploads/"+id)
		w.WriteHeader(http.StatusAccepted)

	case strings.HasPrefix(p, "/v2/uploads/") && req.Method == http.MethodPut:
		body, err := io.ReadAll(req.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		dg := req.URL.Query().Get("digest")
		r.mu.Lock()
		r.blobs[dg] = body
		delete(r.uploads, strings.TrimPrefix(p, "/v2/uploads/"))
		r.mu.Unlock()
		w.Header().Set("Docker-Content-Digest", dg)
		w.WriteHeader(http.StatusCreated)

	case strings.Contains(p, "/manifests/") && req.Method == http.MethodPut:
		repo, ref := splitManifestPath(p)
		body, err := io.ReadAll(req.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		dg := digestOf(body)
		r.mu.Lock()
		r.blobs[dg] = body
		r.ct[dg] = req.Header.Get("Content-Type")
		r.tags[repo+":"+ref] = dg
		r.mu.Unlock()
		w.Header().Set("Docker-Content-Digest", dg)
		w.WriteHeader(http.StatusCreated)

	case strings.Contains(p, "/manifests/") && (req.Method == http.MethodGet || req.Method == http.MethodHead):
		repo, ref := splitManifestPath(p)
		r.mu.Lock()
		dg := ref
		if !strings.HasPrefix(ref, "sha256:") {
			if td, ok := r.tags[repo+":"+ref]; ok {
				dg = td
			}
		}
		body, ok := r.blobs[dg]
		ct := r.ct[dg]
		r.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if ct == "" {
			ct = "application/vnd.oci.image.manifest.v1+json"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Docker-Content-Digest", dg)
		if req.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		writeBody(w, body)

	case strings.Contains(p, "/blobs/") && (req.Method == http.MethodGet || req.Method == http.MethodHead):
		_, dg, _ := strings.Cut(p, "/blobs/")
		r.mu.Lock()
		body, ok := r.blobs[dg]
		r.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", dg)
		if req.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		writeBody(w, body)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeBody(w http.ResponseWriter, body []byte) {
	if _, err := w.Write(body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// splitManifestPath parses /v2/<repo...>/manifests/<ref> into (repo, ref).
func splitManifestPath(p string) (repo, ref string) {
	before, after, _ := strings.Cut(p, "/manifests/")
	return strings.TrimPrefix(before, "/v2/"), after
}

// startAuthedOCIRegistry starts the hand-rolled registry behind a TLS httptest server
// and returns its host:port plus an http.Client trusting the server's cert.
func startAuthedOCIRegistry(t *testing.T) (host string, client *http.Client) {
	t.Helper()
	srv := httptest.NewTLSServer(newOCIRegistry(authedTestUser, authedTestPass))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	return u.Host, srv.Client()
}

// pushFixtureChart packages a minimal chart and pushes it via helm's registry client
// (trusting the test cert and authenticating), so the pulled bytes are self-consistent.
func pushFixtureChart(t *testing.T, host string, client *http.Client) {
	t.Helper()
	ch := &chart.Chart{Metadata: &chart.Metadata{
		APIVersion: chart.APIVersionV2,
		Name:       authedTestChart,
		Version:    authedTestVer,
	}}
	tgzPath, err := chartutil.Save(ch, t.TempDir())
	if err != nil {
		t.Fatalf("package fixture chart: %v", err)
	}
	data, err := os.ReadFile(tgzPath)
	if err != nil {
		t.Fatalf("read fixture tgz: %v", err)
	}
	rc, err := registry.NewClient(
		registry.ClientOptHTTPClient(client),
		registry.ClientOptBasicAuth(authedTestUser, authedTestPass),
	)
	if err != nil {
		t.Fatalf("registry client: %v", err)
	}
	if _, err := rc.Push(data, host+"/charts/"+authedTestChart+":"+authedTestVer); err != nil {
		t.Fatalf("push fixture chart: %v", err)
	}
}

func TestHelmLoaderOCIAuthed(t *testing.T) {
	host, client := startAuthedOCIRegistry(t)
	pushFixtureChart(t, host, client)

	ctx := context.Background()
	repo := "oci://" + host + "/charts"

	authed := newHelmLoader(func(context.Context, string) (creds, error) {
		return creds{user: authedTestUser, pass: authedTestPass, ok: true}, nil
	})
	authed.httpClient = client
	ch, err := authed.Load(ctx, repo, authedTestChart, authedTestVer)
	if err != nil {
		t.Fatalf("authed OCI pull: %v", err)
	}
	if ch == nil || ch.Metadata == nil || ch.Metadata.Name != authedTestChart {
		t.Fatalf("authed OCI pull: expected chart %q, got %+v", authedTestChart, ch)
	}

	const badPass = "wrong-password-intentionally-invalid"
	bad := newHelmLoader(func(context.Context, string) (creds, error) {
		return creds{user: authedTestUser, pass: badPass, ok: true}, nil
	})
	bad.httpClient = client
	_, badErr := bad.Load(ctx, repo, authedTestChart, authedTestVer)
	if badErr == nil {
		t.Fatal("wrong-password OCI pull: expected an error, got nil")
	}
	if strings.Contains(badErr.Error(), authedTestUser) {
		t.Fatal("credential leak: OCI username appeared in the error string")
	}
	if strings.Contains(badErr.Error(), authedTestPass) || strings.Contains(badErr.Error(), badPass) {
		t.Fatal("credential leak: OCI password appeared in the error string")
	}

	anon := newHelmLoader(nil)
	anon.httpClient = client
	if _, err := anon.Load(ctx, repo, authedTestChart, authedTestVer); err == nil {
		t.Fatal("anonymous OCI pull against authed registry: expected an error, got nil")
	}
}

// authedHelmRepoServer stands up an httptest.Server that serves a single-chart
// classic Helm repo (index.yaml + chart tgz) behind HTTP Basic auth, over plain
// HTTP — the classic-repo pull path has no HTTPS credential-forwarding restriction,
// unlike OCI above. Returns the server and the expected Authorization header value.
func authedHelmRepoServer(t *testing.T, chartName, chartVersion, user, pass string) (srv *httptest.Server, wantToken string) {
	t.Helper()
	tgzBytes := makeMinimalChartTGZ(t, chartName, chartVersion)
	tgzName := fmt.Sprintf("%s-%s.tgz", chartName, chartVersion)
	wantToken = "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != wantToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="helm"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/index.yaml":
			indexYAML := fmt.Sprintf(`apiVersion: v1
entries:
  %s:
  - name: %s
    version: %s
    urls:
    - %s/%s
generated: "2026-01-01T00:00:00Z"
`, chartName, chartName, chartVersion, srv.URL, tgzName)
			w.Header().Set("Content-Type", "text/yaml")
			if _, err := w.Write([]byte(indexYAML)); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		case "/" + tgzName:
			w.Header().Set("Content-Type", "application/octet-stream")
			if _, err := w.Write(tgzBytes); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		default:
			http.NotFound(w, r)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, wantToken
}

// TestHelmLoaderAuthedHTTPRepo proves that pullHTTP sends Basic credentials and
// a fully-authenticated pull against a Basic-auth-protected httptest server
// succeeds and returns a parsed chart. Also proves wrong credentials fail and
// the error does not contain the password.
func TestHelmLoaderAuthedHTTPRepo(t *testing.T) {
	const (
		chartName    = "testchart"
		chartVersion = "0.1.0"
		testUser     = "helmuser"
		testPass     = "supersecret"
		badPass      = "wrong-intentionally"
	)

	srv, _ := authedHelmRepoServer(t, chartName, chartVersion, testUser, testPass)
	ctx := context.Background()

	// --- correct credentials: pull must succeed and return a parsed chart ---
	goodLoader := newHelmLoader(func(_ context.Context, _ string) (creds, error) {
		return creds{user: testUser, pass: testPass, ok: true}, nil
	})
	ch, err := goodLoader.Load(ctx, srv.URL, chartName, chartVersion)
	if err != nil {
		t.Fatalf("authed HTTP repo pull with correct creds: %v", err)
	}
	if ch == nil || ch.Metadata == nil || ch.Metadata.Name != chartName {
		t.Fatalf("authed HTTP repo pull: expected chart name %q, got %v", chartName, ch)
	}

	// --- wrong credentials: must fail, must not leak password ---
	badLoader := newHelmLoader(func(_ context.Context, _ string) (creds, error) {
		return creds{user: testUser, pass: badPass, ok: true}, nil
	})
	_, badErr := badLoader.Load(ctx, srv.URL, chartName, chartVersion)
	if badErr == nil {
		t.Fatal("wrong-cred HTTP repo pull: expected an error, got nil")
	}
	if strings.Contains(badErr.Error(), testPass) || strings.Contains(badErr.Error(), badPass) {
		t.Fatal("credential leak: password appeared in wrong-cred error string")
	}

	// --- anonymous: no Authorization header sent, returns 401 error ---
	anonLoader := newHelmLoader(nil)
	_, anonErr := anonLoader.Load(ctx, srv.URL, chartName, chartVersion)
	if anonErr == nil {
		t.Fatal("anonymous HTTP repo pull against authed server: expected an error, got nil")
	}
}
