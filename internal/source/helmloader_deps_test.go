// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/registry"
)

// writeChartDir writes a minimal expanded chart directory. If deps is true it adds
// a dependencies: entry to Chart.yaml; if lock is true it also writes a Chart.lock.
func writeChartDir(t *testing.T, name string, deps, lock bool) string {
	t.Helper()
	dir := t.TempDir()
	chartDir := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(chartDir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	cy := "apiVersion: v2\nname: " + name + "\nversion: 0.1.0\n"
	if deps {
		cy += "dependencies:\n  - name: sub\n    version: 0.1.0\n    repository: oci://example.test/charts\n"
	}
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte(cy), 0o644); err != nil {
		t.Fatal(err)
	}
	if lock {
		if err := os.WriteFile(filepath.Join(chartDir, "Chart.lock"), []byte("dependencies: []\ndigest: sha256:x\ngenerated: \"2026-01-01T00:00:00Z\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return chartDir
}

func TestRequireLockIfDeps(t *testing.T) {
	// deps + no lock => error
	if err := requireLockIfDeps(writeChartDir(t, "c", true, false)); err == nil {
		t.Fatal("expected error for dependency-declaring chart without Chart.lock")
	} else if !strings.Contains(err.Error(), "Chart.lock") {
		t.Fatalf("error should mention Chart.lock, got: %v", err)
	}
	// deps + lock => ok
	if err := requireLockIfDeps(writeChartDir(t, "c", true, true)); err != nil {
		t.Fatalf("deps+lock should pass: %v", err)
	}
	// no deps + no lock => ok (no requirement)
	if err := requireLockIfDeps(writeChartDir(t, "c", false, false)); err != nil {
		t.Fatalf("no-deps chart must not require a lock: %v", err)
	}
}

func TestHelmLoaderResolvesUnvendoredDependency(t *testing.T) {
	host, client := startAuthedOCIRegistry(t)
	// pushSubchartAndParent pushes sub-0.1.0 and parent-0.1.0 (parent declares sub as
	// a dependency + ships a committed Chart.lock, and does NOT vendor charts/). It
	// returns the local path to the exact parent .tgz pushed, for the invariant check.
	parentTGZ := pushSubchartAndParent(t, host, client)

	// Fixture invariant (CRITICAL): the pushed parent must NOT vendor the subchart, so
	// the subchart's presence after Load provably comes from Build() fetching it.
	assertNoVendoredSubcharts(t, parentTGZ)

	l := newHelmLoader(func(_ context.Context, _ string) (creds, error) {
		return creds{user: authedTestUser, pass: authedTestPass, ok: true}, nil
	})
	l.httpClient = client

	ch, err := l.Load(context.Background(), "oci://"+host, "parent", "0.1.0")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, d := range ch.Dependencies() {
		if d.Metadata != nil && d.Metadata.Name == "sub" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected resolved subchart 'sub' in loaded chart (fetched via Build), got none")
	}
}

func assertNoVendoredSubcharts(t *testing.T, tgzPath string) {
	t.Helper()
	f, err := os.Open(tgzPath)
	if err != nil {
		t.Fatalf("open parent tgz: %v", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip parent tgz: %v", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read parent tgz: %v", err)
		}
		if strings.Contains(hdr.Name, "/charts/") {
			t.Fatalf("fixture invariant violated: parent archive vendors a subchart (%q); "+
				"the unvendored-resolution test requires an empty charts/ so the pull is what makes it pass", hdr.Name)
		}
	}
}

// pushSubchartAndParent pushes two charts to the in-process OCI registry:
//   - sub v0.1.0 at host+"/charts/sub:0.1.0"
//   - parent v0.1.0 at host+"/parent:0.1.0" — declares sub as a dependency with a
//     committed Chart.lock, and does NOT vendor charts/ (so downloader.Manager.Build
//     is the only path by which sub can end up in the loaded chart).
//
// Returns the local path to the parent .tgz (before push) for the invariant check.
func pushSubchartAndParent(t *testing.T, host string, client *http.Client) string {
	t.Helper()

	rc, err := registry.NewClient(
		registry.ClientOptHTTPClient(client),
		registry.ClientOptBasicAuth(authedTestUser, authedTestPass),
	)
	if err != nil {
		t.Fatalf("registry client: %v", err)
	}

	// --- build and push sub chart to host+"/charts/sub:0.1.0" ---
	// downloader.Manager.Build will fetch from repository "oci://"+host+"/charts",
	// constructing the pull reference as host+"/charts/sub:0.1.0".
	sub := &chart.Chart{Metadata: &chart.Metadata{
		APIVersion: chart.APIVersionV2,
		Name:       "sub",
		Version:    "0.1.0",
	}}
	subTGZPath, err := chartutil.Save(sub, t.TempDir())
	if err != nil {
		t.Fatalf("save sub chart: %v", err)
	}
	subData, err := os.ReadFile(subTGZPath)
	if err != nil {
		t.Fatalf("read sub tgz: %v", err)
	}
	if _, err := rc.Push(subData, host+"/charts/sub:0.1.0"); err != nil {
		t.Fatalf("push sub chart: %v", err)
	}

	// --- build parent chart with dependencies declared but NOT vendored ---
	// dep points to the same registry so downloader.Manager.Build can fetch sub.
	dep := &chart.Dependency{
		Name:       "sub",
		Version:    "0.1.0",
		Repository: "oci://" + host + "/charts",
	}
	parentDeps := []*chart.Dependency{dep}

	// Compute the correct Chart.lock digest.  This mirrors the internal/resolver.HashReq
	// logic: json.Marshal([2][]*Dependency{req, lock}) → sha256 → "sha256:"+hex.
	// req and lock are the same in our case (lock pins exactly what Chart.yaml declares).
	hashInput, err := json.Marshal([2][]*chart.Dependency{parentDeps, parentDeps})
	if err != nil {
		t.Fatalf("marshal deps for digest: %v", err)
	}
	h := sha256.Sum256(hashInput)
	digest := "sha256:" + hex.EncodeToString(h[:])

	parent := &chart.Chart{
		Metadata: &chart.Metadata{
			APIVersion:   chart.APIVersionV2,
			Name:         "parent",
			Version:      "0.1.0",
			Dependencies: parentDeps,
		},
		Lock: &chart.Lock{
			Generated:    time.Now(),
			Digest:       digest,
			Dependencies: parentDeps,
		},
	}
	// IMPORTANT: do NOT call parent.SetDependencies(sub). chartutil.Save vendors any
	// sub-*chart.Chart objects attached to the parent — that would create charts/ in
	// the archive and break the invariant.

	parentTGZPath, err := chartutil.Save(parent, t.TempDir())
	if err != nil {
		t.Fatalf("save parent chart: %v", err)
	}

	parentData, err := os.ReadFile(parentTGZPath)
	if err != nil {
		t.Fatalf("read parent tgz: %v", err)
	}
	// Push to host+"/parent:0.1.0".  Load is called with repoURL="oci://"+host, so
	// pullOCI constructs ref "oci://"+host+"/parent" with version "0.1.0" — matching here.
	if _, err := rc.Push(parentData, host+"/parent:0.1.0"); err != nil {
		t.Fatalf("push parent chart: %v", err)
	}

	return parentTGZPath
}
