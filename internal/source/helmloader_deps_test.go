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

// writeChartDir writes a minimal expanded chart dir. A non-empty depRepo adds one
// dependency on that repository; lock writes a Chart.lock; vendorSub writes a
// packed charts/sub-0.1.0.tgz so the dependency counts as already vendored.
func writeChartDir(t *testing.T, depRepo string, lock, vendorSub bool) string {
	t.Helper()
	const name = "c"
	dir := t.TempDir()
	chartDir := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(chartDir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	cy := "apiVersion: v2\nname: " + name + "\nversion: 0.1.0\n"
	if depRepo != "" {
		cy += "dependencies:\n  - name: sub\n    version: 0.1.0\n    repository: " + depRepo + "\n"
	}
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte(cy), 0o644); err != nil {
		t.Fatal(err)
	}
	if lock {
		if err := os.WriteFile(filepath.Join(chartDir, "Chart.lock"), []byte("dependencies: []\ndigest: sha256:x\ngenerated: \"2026-01-01T00:00:00Z\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if vendorSub {
		sub := &chart.Chart{Metadata: &chart.Metadata{APIVersion: chart.APIVersionV2, Name: "sub", Version: "0.1.0"}}
		if _, err := chartutil.Save(sub, filepath.Join(chartDir, "charts")); err != nil {
			t.Fatalf("vendor sub: %v", err)
		}
	}
	return chartDir
}

func TestResolveDepsPlan(t *testing.T) {
	const ociRepo = "oci://example.test/charts"

	needsBuild, err := resolveDepsPlan(writeChartDir(t, ociRepo, true, false))
	if err != nil {
		t.Fatalf("oci dep + lock, unvendored should pass: %v", err)
	}
	if !needsBuild {
		t.Fatal("oci dep + lock, unvendored must require a Build")
	}

	if _, err := resolveDepsPlan(writeChartDir(t, ociRepo, false, false)); err == nil {
		t.Fatal("expected error for unvendored dependency without Chart.lock")
	} else if !strings.Contains(err.Error(), "Chart.lock") {
		t.Fatalf("error should mention Chart.lock, got: %v", err)
	}

	if needsBuild, err := resolveDepsPlan(writeChartDir(t, "", false, false)); err != nil || needsBuild {
		t.Fatalf("no-deps chart: needsBuild=%v err=%v", needsBuild, err)
	}

	if _, err := resolveDepsPlan(writeChartDir(t, "https://charts.example.com", true, false)); err == nil {
		t.Fatal("expected error for unvendored HTTP(S)-repo dependency")
	} else if !strings.Contains(err.Error(), "oci://") {
		t.Fatalf("error should point at the oci:// requirement, got: %v", err)
	}

	// Regression: an HTTP(S)-repo dependency that IS vendored under charts/ must be
	// accepted with no Build (Helm loads the vendored subchart directly).
	if needsBuild, err := resolveDepsPlan(writeChartDir(t, "https://charts.example.com", false, true)); err != nil || needsBuild {
		t.Fatalf("vendored HTTP dep must be accepted with no Build: needsBuild=%v err=%v", needsBuild, err)
	}

	// Regression (partial vendoring): an unvendored OCI dep forces a Build, and
	// Build re-resolves the whole Chart.lock — so a co-declared vendored HTTP(S)
	// dep must be rejected fail-closed rather than fail opaquely inside Build.
	if _, err := resolveDepsPlan(writeMixedChartDir(t)); err == nil {
		t.Fatal("expected error for mixed unvendored-OCI + vendored-HTTP deps (Build re-resolves whole lock)")
	} else if !strings.Contains(err.Error(), "oci://") {
		t.Fatalf("error should point at the oci:// requirement, got: %v", err)
	}
}

// writeMixedChartDir writes a chart declaring two deps: an unvendored OCI dep
// (forces a Build) and a vendored HTTP(S) dep. Because Build re-resolves the whole
// Chart.lock, the HTTP(S) dep is not Build-safe even though it is vendored.
func writeMixedChartDir(t *testing.T) string {
	t.Helper()
	const name = "c"
	dir := t.TempDir()
	chartDir := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(chartDir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	cy := "apiVersion: v2\nname: " + name + "\nversion: 0.1.0\n" +
		"dependencies:\n" +
		"  - name: ocisub\n    version: 0.1.0\n    repository: oci://example.test/charts\n" +
		"  - name: httpsub\n    version: 0.1.0\n    repository: https://charts.example.com\n"
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte(cy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.lock"), []byte("dependencies: []\ndigest: sha256:x\ngenerated: \"2026-01-01T00:00:00Z\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	httpsub := &chart.Chart{Metadata: &chart.Metadata{APIVersion: chart.APIVersionV2, Name: "httpsub", Version: "0.1.0"}}
	if _, err := chartutil.Save(httpsub, filepath.Join(chartDir, "charts")); err != nil {
		t.Fatalf("vendor httpsub: %v", err)
	}
	return chartDir
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

// pushParentWithVendoredSub pushes a parent chart that vendors its subchart under
// charts/ by attaching the sub as a Go chart.Chart object before Save, which causes
// chartutil.Save to write charts/sub-0.1.0.tgz inside the archive. The parent
// declares NO Metadata.Dependencies, so resolveDepsPlan is satisfied without a
// Chart.lock. Returns the chart name that was pushed (for use in Load).
func pushParentWithVendoredSub(t *testing.T, host string, client *http.Client) string {
	t.Helper()
	const parentName = "vendored-parent"

	rc, err := registry.NewClient(
		registry.ClientOptHTTPClient(client),
		registry.ClientOptBasicAuth(authedTestUser, authedTestPass),
	)
	if err != nil {
		t.Fatalf("registry client: %v", err)
	}

	sub := &chart.Chart{Metadata: &chart.Metadata{
		APIVersion: chart.APIVersionV2,
		Name:       "sub",
		Version:    "0.1.0",
	}}

	parent := &chart.Chart{Metadata: &chart.Metadata{
		APIVersion: chart.APIVersionV2,
		Name:       parentName,
		Version:    "0.1.0",
	}}
	// Attach sub as a vendored dependency. chartutil.Save writes it to charts/sub-0.1.0.tgz
	// inside the parent archive. No Metadata.Dependencies entry is added so resolveDepsPlan
	// returns nil (no lock required) and downloader.Manager.Build() is a no-op.
	parent.AddDependency(sub)

	parentTGZPath, err := chartutil.Save(parent, t.TempDir())
	if err != nil {
		t.Fatalf("save vendored parent chart: %v", err)
	}
	data, err := os.ReadFile(parentTGZPath)
	if err != nil {
		t.Fatalf("read vendored parent tgz: %v", err)
	}
	if _, err := rc.Push(data, host+"/"+parentName+":0.1.0"); err != nil {
		t.Fatalf("push vendored parent chart: %v", err)
	}
	return parentName
}

// pushNoDepChart pushes a minimal chart with no declared dependencies, for testing
// the no-op path through expandAndResolveDeps.
func pushNoDepChart(t *testing.T, host string, client *http.Client) string {
	t.Helper()
	const chartName = "nodep-chart"

	rc, err := registry.NewClient(
		registry.ClientOptHTTPClient(client),
		registry.ClientOptBasicAuth(authedTestUser, authedTestPass),
	)
	if err != nil {
		t.Fatalf("registry client: %v", err)
	}

	ch := &chart.Chart{Metadata: &chart.Metadata{
		APIVersion: chart.APIVersionV2,
		Name:       chartName,
		Version:    "0.1.0",
	}}
	tgzPath, err := chartutil.Save(ch, t.TempDir())
	if err != nil {
		t.Fatalf("save no-dep chart: %v", err)
	}
	data, err := os.ReadFile(tgzPath)
	if err != nil {
		t.Fatalf("read no-dep tgz: %v", err)
	}
	if _, err := rc.Push(data, host+"/"+chartName+":0.1.0"); err != nil {
		t.Fatalf("push no-dep chart: %v", err)
	}
	return chartName
}

// pushParentDepsNoLock pushes a parent that declares a dependency in Chart.yaml but
// ships no Chart.lock and no vendored charts/. This is the fixture for the fail-closed
// pre-check: resolveDepsPlan must reject it before Build runs.
func pushParentDepsNoLock(t *testing.T, host string, client *http.Client) string {
	t.Helper()
	const parentName = "lockless-parent"

	rc, err := registry.NewClient(
		registry.ClientOptHTTPClient(client),
		registry.ClientOptBasicAuth(authedTestUser, authedTestPass),
	)
	if err != nil {
		t.Fatalf("registry client: %v", err)
	}

	dep := &chart.Dependency{
		Name:       "sub",
		Version:    "0.1.0",
		Repository: "oci://example.test/charts",
	}
	parent := &chart.Chart{
		Metadata: &chart.Metadata{
			APIVersion:   chart.APIVersionV2,
			Name:         parentName,
			Version:      "0.1.0",
			Dependencies: []*chart.Dependency{dep},
		},
		// Lock deliberately left nil so chartutil.Save does not write Chart.lock.
	}
	// No sub chart attached: charts/ is absent in the saved archive.
	tgzPath, err := chartutil.Save(parent, t.TempDir())
	if err != nil {
		t.Fatalf("save lockless parent chart: %v", err)
	}
	data, err := os.ReadFile(tgzPath)
	if err != nil {
		t.Fatalf("read lockless parent tgz: %v", err)
	}
	if _, err := rc.Push(data, host+"/"+parentName+":0.1.0"); err != nil {
		t.Fatalf("push lockless parent chart: %v", err)
	}
	return parentName
}

// TestHelmLoaderVendoredSubchartUnchanged verifies that a parent chart shipping its
// subchart already vendored under charts/ loads cleanly and the subchart is visible
// in ch.Dependencies(). Vendoring is intentional here; unlike
// TestHelmLoaderResolvesUnvendoredDependency there is no assertNoVendoredSubcharts
// invariant — the presence of charts/ IS the fixture.
func TestHelmLoaderVendoredSubchartUnchanged(t *testing.T) {
	host, client := startAuthedOCIRegistry(t)
	parentName := pushParentWithVendoredSub(t, host, client)

	l := newHelmLoader(func(_ context.Context, _ string) (creds, error) {
		return creds{user: authedTestUser, pass: authedTestPass, ok: true}, nil
	})
	l.httpClient = client

	ch, err := l.Load(context.Background(), "oci://"+host, parentName, "0.1.0")
	if err != nil {
		t.Fatalf("Load with vendored subchart: %v", err)
	}
	if len(ch.Dependencies()) == 0 {
		t.Fatal("expected vendored subchart in loaded chart, got none")
	}
}

// TestHelmLoaderNoDependenciesNoOp verifies that a plain chart with no declared
// dependencies loads unchanged through the full Load path (no Build, no lock check).
func TestHelmLoaderNoDependenciesNoOp(t *testing.T) {
	host, client := startAuthedOCIRegistry(t)
	chartName := pushNoDepChart(t, host, client)

	l := newHelmLoader(func(_ context.Context, _ string) (creds, error) {
		return creds{user: authedTestUser, pass: authedTestPass, ok: true}, nil
	})
	l.httpClient = client

	ch, err := l.Load(context.Background(), "oci://"+host, chartName, "0.1.0")
	if err != nil {
		t.Fatalf("Load with no-dep chart: %v", err)
	}
	if ch.Metadata.Name != chartName {
		t.Fatalf("expected chart name %q, got %q", chartName, ch.Metadata.Name)
	}
}

// TestHelmLoaderMissingLockFailsClosed verifies that Load returns an error containing
// "Chart.lock" when the chart declares dependencies but ships no Chart.lock and no
// vendored charts/. This exercises the fail-closed pre-check in expandAndResolveDeps.
func TestHelmLoaderMissingLockFailsClosed(t *testing.T) {
	host, client := startAuthedOCIRegistry(t)
	parentName := pushParentDepsNoLock(t, host, client)

	l := newHelmLoader(func(_ context.Context, _ string) (creds, error) {
		return creds{user: authedTestUser, pass: authedTestPass, ok: true}, nil
	})
	l.httpClient = client

	_, err := l.Load(context.Background(), "oci://"+host, parentName, "0.1.0")
	if err == nil {
		t.Fatal("expected error for dependency-declaring chart with no Chart.lock, got nil")
	}
	if !strings.Contains(err.Error(), "Chart.lock") {
		t.Fatalf("error should mention Chart.lock, got: %v", err)
	}
}
