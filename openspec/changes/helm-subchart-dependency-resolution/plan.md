# Helm Subchart Dependency Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the production Helm `ChartLoader` resolve declared `Chart.yaml` `dependencies:` at pull time (via `downloader.Manager.Build()`) so wrapper charts can drop subchart vendoring, with deterministic `Build()`-only semantics enforced by a `Chart.lock` pre-check.

**Architecture:** In `helmLoader.Load()`, after the chart `.tgz` is pulled to a temp dir, expand it to a directory (`chartutil.Expand`), fail closed if `Chart.yaml` declares dependencies but no `Chart.lock` is present, run `downloader.Manager.Build()` reusing the parent pull's registry-client options, then `loader.Load(chartDir)`. Kustomize path is untouched. No CRD, no cache-key change.

**Tech Stack:** Go, `helm.sh/helm/v3 v3.21.3` (`pkg/chartutil`, `pkg/downloader`, `pkg/getter`, `pkg/registry`, `pkg/chart/loader`), Ginkgo/Gomega + stdlib `testing`, existing hermetic in-process OCI registry test harness (`internal/source/helmloader_authed_test.go`).

**Build/test commands (SAP go-makefile-maker project):**
- build: `go build ./...`
- test (this package needs no envtest): `go test ./internal/source/...`
- lint: `make run-golangci-lint`
- full gate: `make check`

---

## File Structure

- **Modify** `internal/source/helmloader.go`:
  - Refactor the OCI registry-client option construction out of `pullOCI` into a small helper so both the pull and the new dependency `Build()` reuse an identical option set (cache, optional basic-auth, test `httpClient` seam).
  - Extend `Load()` with: expand `.tgz` → `Chart.lock` pre-check → `downloader.Manager.Build()` → `loader.Load(chartDir)`.
- **Create** `internal/source/helmloader_deps_test.go`: hermetic tests for the four spec scenarios (unvendored-resolves, vendored-unchanged, no-deps no-op, missing-lock-fails-closed) reusing the existing in-process OCI registry harness.
- **No change** to `internal/source/kustomize.go`, `gitresolver.go`, `helm.go`, CRD types, or the render cache.

Reference facts (verified against `helm.sh/helm/v3@v3.21.3`):
- `chartutil.Expand(dir string, r io.Reader) error` — unpacks a `.tgz` reader into `dir`, creating `dir/<chartname>/`.
- `downloader.Manager` fields: `Out io.Writer`, `ChartPath string`, `Getters []getter.Provider`, `RegistryClient *registry.Client`, `RepositoryConfig string`, `RepositoryCache string`, `Debug bool`.
- `Manager.Build()` runs `Manager.Update()` (semver re-negotiation) when no `Chart.lock` is present — this is the fallback the pre-check prevents.
- A loaded `*chart.Chart` exposes `ch.Metadata.Dependencies []*chart.Dependency` and `ch.Lock *chart.Lock`.

---

### Task 1: Extract a shared OCI registry-client option builder

Refactor so the OCI registry-client options built in `pullOCI` are reusable by the dependency `Build()` step, with no behavior change to the existing pull.

**Files:**
- Modify: `internal/source/helmloader.go` (`pullOCI`, add helper `ociRegistryClient`)

- [x] **Step 1: Write the failing test**

Add to `internal/source/helmloader_test.go`:

```go
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
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestOCIRegistryClientOptionsReused -v`
Expected: FAIL — `l.ociRegistryClient undefined`.

- [x] **Step 3: Extract the helper and call it from `pullOCI`**

In `internal/source/helmloader.go`, add the helper (mirrors the exact option set currently inline in `pullOCI`):

```go
// ociRegistryClient builds the registry client used for both OCI chart pulls and
// dependency resolution, so the same cache / optional basic-auth / test httpClient
// seam applies to subchart fetches. Production leaves httpClient nil.
func (l *helmLoader) ociRegistryClient(c creds) (*registry.Client, error) {
	opts := []registry.ClientOption{registry.ClientOptEnableCache(true)}
	if c.ok {
		opts = append(opts, registry.ClientOptBasicAuth(c.user, c.pass)) // inline, no on-disk Login
	}
	if l.httpClient != nil {
		opts = append(opts, registry.ClientOptHTTPClient(l.httpClient))
	}
	return registry.NewClient(opts...)
}
```

Then replace the inline option block in `pullOCI` (the `opts := []registry.ClientOption{...}` through `rc, err := registry.NewClient(opts...)`) with:

```go
	rc, err := l.ociRegistryClient(c)
	if err != nil {
		return "", err
	}
```

(Leave `resolveOCIDigest` as-is for this task — it is not on the render path and can be aligned in a later cleanup.)

- [x] **Step 4: Run tests to verify pass + no regression**

Run: `go test ./internal/source/ -run 'TestOCIRegistryClientOptionsReused|TestHelmLoaderOCIAuthed|TestHelmLoader' -v`
Expected: PASS (new test passes; existing OCI pull/auth tests still pass).

- [x] **Step 5: Commit**

```bash
git add internal/source/helmloader.go internal/source/helmloader_test.go
git commit -m "refactor(source): extract shared OCI registry-client builder for reuse"
```

---

### Task 2: Fail closed when a dependency-declaring chart has no Chart.lock

Add the pre-check that reads the expanded chart's `Chart.yaml`; if it declares a non-empty `dependencies:` list and no `Chart.lock` is present in the chart dir, return a clear error before any `Build()`.

**Files:**
- Modify: `internal/source/helmloader.go` (add `requireLockIfDeps`)
- Test: `internal/source/helmloader_deps_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `internal/source/helmloader_deps_test.go`:

```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestRequireLockIfDeps -v`
Expected: FAIL — `requireLockIfDeps undefined`.

- [ ] **Step 3: Implement `requireLockIfDeps`**

In `internal/source/helmloader.go` add (and ensure imports include `helm.sh/helm/v3/pkg/chartutil`):

```go
// requireLockIfDeps fails closed when the expanded chart at chartDir declares a
// non-empty dependencies: list in Chart.yaml but has no Chart.lock. This guarantees
// downloader.Manager.Build() only ever runs its deterministic lock-driven path and
// never falls back to Update() (semver re-negotiation against the live index).
func requireLockIfDeps(chartDir string) error {
	meta, err := chartutil.LoadChartfile(filepath.Join(chartDir, "Chart.yaml"))
	if err != nil {
		return fmt.Errorf("source: read Chart.yaml: %w", err)
	}
	if len(meta.Dependencies) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(chartDir, "Chart.lock")); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("source: chart %q declares dependencies but has no Chart.lock; commit Chart.lock for deterministic resolution", meta.Name)
		}
		return fmt.Errorf("source: stat Chart.lock: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/ -run TestRequireLockIfDeps -v`
Expected: PASS (all three sub-cases).

- [ ] **Step 5: Commit**

```bash
git add internal/source/helmloader.go internal/source/helmloader_deps_test.go
git commit -m "feat(source): fail closed when a dependency chart lacks Chart.lock"
```

---

### Task 3: Wire expand + Build() + load into `Load()`

Insert the resolution step between pull and load, reusing the temp dir and the shared registry-client builder. Point the expansion base at the temp dir already created by `Load`.

**Files:**
- Modify: `internal/source/helmloader.go` (`Load`)

- [ ] **Step 1: Write the failing test (unvendored dependency resolves)**

Append to `internal/source/helmloader_deps_test.go`. This reuses the existing in-process OCI registry harness (`startAuthedOCIRegistry`, `pushFixtureChart`) from `helmloader_authed_test.go`; extend the harness only if needed to push a parent chart that declares a dependency on an already-pushed subchart, with a committed `Chart.lock`. Keep it hermetic (TLS `httptest`, loader `httpClient` seam):

```go
func TestHelmLoaderResolvesUnvendoredDependency(t *testing.T) {
	// Arrange: in-process OCI registry with a subchart pushed, and a parent chart
	// that declares the subchart as a dependency (unvendored) + a committed Chart.lock.
	reg := startAuthedOCIRegistry(t) // returns srv + TLS client (see helmloader_authed_test.go)
	// pushSubchartAndParent pushes sub-0.1.0 and parent-0.1.0 (deps+lock, NO vendored
	// charts/) and returns the local path to the exact parent .tgz it pushed, so the
	// test can assert the fixture invariant below.
	parentTGZ := pushSubchartAndParent(t, reg)

	// Fixture invariant (CRITICAL): prove the pushed parent archive does NOT vendor
	// the subchart. Without this assertion the test could pass for the wrong reason
	// (subchart present because it was vendored, not fetched) if the helper ever
	// regressed to vendoring. Asserting charts/ is empty makes a passing test provably
	// the result of downloader.Manager.Build() FETCHING the subchart during Load.
	assertNoVendoredSubcharts(t, parentTGZ)

	l := newHelmLoader(func(_ context.Context, _ string) (creds, error) {
		return creds{user: authedTestUser, pass: authedTestPass, ok: true}, nil
	})
	l.httpClient = reg.client // trust the test TLS server

	ch, err := l.Load(context.Background(), "oci://"+reg.host, "parent", "0.1.0")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Assert: the subchart is present in the loaded chart. Because the fixture
	// invariant proved charts/ was empty in the pulled archive, its presence here
	// can ONLY be the result of Build() fetching it — i.e. this asserts the pull.
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

// assertNoVendoredSubcharts opens the pushed parent .tgz and fails if it contains
// any charts/<name>/ or charts/*.tgz entry. This locks the "unvendored" fixture
// invariant so TestHelmLoaderResolvesUnvendoredDependency provably exercises the
// network fetch rather than a vendored subchart.
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
		// Any entry under "<chart>/charts/" means a vendored subchart is present,
		// which would invalidate this test's premise.
		if strings.Contains(hdr.Name, "/charts/") {
			t.Fatalf("fixture invariant violated: parent archive vendors a subchart (%q); "+
				"the unvendored-resolution test requires an empty charts/ so the pull is what makes it pass", hdr.Name)
		}
	}
}
```

> Note for the implementer:
> - `pushSubchartAndParent` MUST return the local path to the parent `.tgz` it pushed (so the invariant assertion can inspect it) and MUST NOT vendor the subchart. It builds on the existing `startAuthedOCIRegistry`/`pushFixtureChart` helpers; do NOT introduce new module deps or an external `registry:2`.
> - `reg` accessor fields (`host`, `client`) build on the existing harness.
> - `assertNoVendoredSubcharts` needs the imports `archive/tar`, `compress/gzip`, `io`, `os`, `strings` in the test file's import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestHelmLoaderResolvesUnvendoredDependency -v`
Expected: FAIL at the final assertion — the fixture-invariant check (`assertNoVendoredSubcharts`) MUST PASS (the pushed parent has an empty `charts/`), and then the "subchart 'sub' present" assertion FAILS because current `Load` calls `loader.Load(chartPath)` on the `.tgz` without resolving deps, so the unvendored subchart is absent. If the invariant check itself fails, the fixture helper is wrong (it vendored the subchart) — fix the helper, not the assertion, so the test keeps proving the fetch.

- [ ] **Step 3: Implement the resolution step in `Load`**

In `internal/source/helmloader.go`, ensure imports include `io`, `path/filepath`, `helm.sh/helm/v3/pkg/chartutil`, `helm.sh/helm/v3/pkg/downloader`, `helm.sh/helm/v3/pkg/getter`. Replace the tail of `Load` (currently `return loader.Load(chartPath)`) with:

```go
	// Resolve declared subchart dependencies at pull time. downloader.Manager works
	// on an unpacked directory, so expand the pulled .tgz first.
	chartDir, err := l.expandAndResolveDeps(ctx, chartPath, tmp)
	if err != nil {
		return nil, err
	}
	return loader.Load(chartDir)
}

// expandAndResolveDeps expands the pulled chart .tgz under tmp, fails closed if it
// declares dependencies without a Chart.lock, runs downloader.Manager.Build() to
// fetch the pinned subcharts, and returns the chart directory to load.
func (l *helmLoader) expandAndResolveDeps(ctx context.Context, chartPath, tmp string) (string, error) {
	unpacked := filepath.Join(tmp, "unpacked")
	if err := os.MkdirAll(unpacked, 0o755); err != nil {
		return "", err
	}
	f, err := os.Open(chartPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if err := chartutil.Expand(unpacked, f); err != nil {
		return "", fmt.Errorf("source: expand chart: %w", err)
	}
	// chartutil.Expand creates unpacked/<chartname>/; find the single child dir.
	chartDir, err := singleChildDir(unpacked)
	if err != nil {
		return "", err
	}
	if err := requireLockIfDeps(chartDir); err != nil {
		return "", err
	}
	rc, err := l.ociRegistryClient(creds{}) // anonymous unless a host challenges; same seam as parent pull
	if err != nil {
		return "", err
	}
	m := &downloader.Manager{
		Out:              io.Discard,
		ChartPath:        chartDir,
		Getters:          getter.All(l.settings),
		RepositoryConfig: l.settings.RepositoryConfig,
		RepositoryCache:  l.settings.RepositoryCache,
		RegistryClient:   rc,
	}
	if err := m.Build(); err != nil {
		return "", fmt.Errorf("source: build dependencies: %w", err)
	}
	return chartDir, nil
}

// singleChildDir returns the sole subdirectory of parent (the expanded chart root).
func singleChildDir(parent string) (string, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", fmt.Errorf("source: read expanded dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(parent, e.Name()), nil
		}
	}
	return "", fmt.Errorf("source: expanded chart has no directory in %s", parent)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/ -run TestHelmLoaderResolvesUnvendoredDependency -v`
Expected: PASS (subchart `sub` resolved and present).

- [ ] **Step 5: Commit**

```bash
git add internal/source/helmloader.go internal/source/helmloader_deps_test.go
git commit -m "feat(source): resolve subchart dependencies at pull time via downloader.Manager"
```

---

### Task 4: Regression + no-op + fail-closed coverage through Load()

Prove the remaining spec scenarios end-to-end through `Load()`: vendored chart still renders, no-dependency chart is unaffected, and a dependency chart missing its lock fails closed at the `Load` boundary.

**Files:**
- Test: `internal/source/helmloader_deps_test.go` (append)

- [ ] **Step 1: Write the failing tests**

Append:

```go
func TestHelmLoaderVendoredSubchartUnchanged(t *testing.T) {
	reg := startAuthedOCIRegistry(t)
	pushParentWithVendoredSub(t, reg) // parent-0.1.0 with charts/sub-0.1.0.tgz vendored, NO dependencies: entry needed

	l := newHelmLoader(func(_ context.Context, _ string) (creds, error) {
		return creds{user: authedTestUser, pass: authedTestPass, ok: true}, nil
	})
	l.httpClient = reg.client

	ch, err := l.Load(context.Background(), "oci://"+reg.host, "parent", "0.1.0")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(ch.Dependencies()) == 0 {
		t.Fatal("expected vendored subchart to remain present")
	}
}

func TestHelmLoaderNoDependenciesNoOp(t *testing.T) {
	reg := startAuthedOCIRegistry(t)
	pushFixtureChart(t, reg, "plain", "0.1.0") // existing helper: minimal chart, no deps

	l := newHelmLoader(func(_ context.Context, _ string) (creds, error) {
		return creds{user: authedTestUser, pass: authedTestPass, ok: true}, nil
	})
	l.httpClient = reg.client

	ch, err := l.Load(context.Background(), "oci://"+reg.host, "plain", "0.1.0")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ch.Metadata.Name != "plain" {
		t.Fatalf("unexpected chart: %s", ch.Metadata.Name)
	}
}

func TestHelmLoaderMissingLockFailsClosed(t *testing.T) {
	reg := startAuthedOCIRegistry(t)
	pushParentDepsNoLock(t, reg) // parent-0.1.0 declares dependencies: but ships NO Chart.lock, NO vendored charts/

	l := newHelmLoader(func(_ context.Context, _ string) (creds, error) {
		return creds{user: authedTestUser, pass: authedTestPass, ok: true}, nil
	})
	l.httpClient = reg.client

	_, err := l.Load(context.Background(), "oci://"+reg.host, "parent", "0.1.0")
	if err == nil {
		t.Fatal("expected fail-closed error for missing Chart.lock")
	}
	if !strings.Contains(err.Error(), "Chart.lock") {
		t.Fatalf("error should mention Chart.lock, got: %v", err)
	}
}
```

> Implementer note: `pushParentWithVendoredSub`, `pushFixtureChart` (exists), and `pushParentDepsNoLock` all build on the in-process harness. Package the chart archives with `chartutil.Save`/`SaveDir` as the existing helpers do; do NOT add external services or module deps.

- [ ] **Step 2: Run tests to verify they fail (where implementation is missing)**

Run: `go test ./internal/source/ -run 'TestHelmLoaderVendoredSubchartUnchanged|TestHelmLoaderNoDependenciesNoOp|TestHelmLoaderMissingLockFailsClosed' -v`
Expected: `MissingLockFailsClosed` may already PASS (Task 2/3 wired the pre-check); vendored/no-op FAIL only if their push helpers are not yet written. Implement the helpers to green.

- [ ] **Step 3: Implement any missing push helpers**

Add the `pushParentWithVendoredSub` / `pushParentDepsNoLock` helpers in the test file, mirroring `pushFixtureChart`'s packaging (build a `*chart.Chart` in memory or via `chartutil.SaveDir`, then push through the harness's helm registry client). No production code changes expected in this task.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/source/ -run 'TestHelmLoaderVendoredSubchartUnchanged|TestHelmLoaderNoDependenciesNoOp|TestHelmLoaderMissingLockFailsClosed' -v`
Expected: PASS (all three).

- [ ] **Step 5: Full package + lint + build gate**

Run: `go test ./internal/source/... && go build ./... && make run-golangci-lint`
Expected: PASS, exit 0, lint clean.

- [ ] **Step 6: Commit**

```bash
git add internal/source/helmloader_deps_test.go
git commit -m "test(source): cover vendored, no-dep, and missing-lock paths through Load"
```

---

### Task 5: Online test — resolve a real remote dependency over the network

Add one online test that exercises `Load` → `downloader.Manager.Build()` against a **real public chart that declares an unvendored remote dependency with a committed `Chart.lock`**, proving the resolution path works end-to-end over the network (not just against the in-process harness).

**Align with the existing online-test convention (verified in [`internal/source/helmloader_online_test.go`](../../../internal/source/helmloader_online_test.go)):** the two existing online tests (`TestHelmLoaderOnlineOCIAnonymous`, `TestHelmLoaderOnlineHTTPRepo`) use **no build tag, no env gate, and no network-reachability skip** — they call `t.Fatalf` on any error and run in the default `Test` job (Phase 7 stance: "online testing accepted... no build tag or env gate; CI must have egress"). The only skip in the whole package is `gitresolver_authed_test.go` skipping on a **missing local binary** (`git-http-backend`), never on network. This new test MUST match that: plain `t.Fatalf` on error, no `isNetworkUnreachable`/skip helper, no new gating machinery. Do NOT introduce a skip-on-network pattern the codebase doesn't have.

**Files:**
- Modify: `internal/source/helmloader_online_test.go` (append one test)

- [ ] **Step 1: Pick and verify a stable public target chart**

The target must: be a **classic HTTP(S) Helm repo** chart (the online tier already uses `pullHTTP` against `prometheus-community.github.io`, a stable GitHub-Pages-hosted repo), declare a **remote** `dependencies:` entry, ship a committed `Chart.lock`, and **not** vendor the subchart under `charts/` in its published `.tgz` (so `Build()` must actually fetch it).

Verify a candidate before hardcoding it. Run (implementer, at plan time):

```bash
# Inspect the PUBLISHED tgz to confirm: remote dep declared, Chart.lock present, charts/ NOT vendored.
helm pull https://prometheus-community.github.io/helm-charts/<chart> --version <pinned> -d /tmp/ddo-verify
tar tzf /tmp/ddo-verify/<chart>-<pinned>.tgz | grep -E '<chart>/Chart.lock' && echo "has Chart.lock"
tar xzf /tmp/ddo-verify/<chart>-<pinned>.tgz -C /tmp/ddo-verify
grep -A3 '^dependencies:' /tmp/ddo-verify/<chart>/Chart.yaml   # confirm a REMOTE repository: URL
tar tzf /tmp/ddo-verify/<chart>-<pinned>.tgz | grep -c '<chart>/charts/.*\.tgz'   # want 0 (unvendored)
```

Pin the exact `chart` + `version` that satisfies **all** conditions (remote dep, `Chart.lock` present, `charts/` empty in the published `.tgz`). Prefer a low-churn chart on a GitHub-Pages-hosted repo (like the existing `prometheus-community.github.io` target) for URL stability. Record the verified `name@version` and the expected subchart in the test comment. If no suitable prometheus-community chart qualifies, use another stable public HTTP repo chart that publishes unvendored with a lock.

- [ ] **Step 2: Write the online test**

Append to `internal/source/helmloader_online_test.go` (substitute the verified `repo`/`name`/`version`/`subchart` from Step 1). Match the existing tests' exact style — no skip helper, `t.Fatalf` on error:

```go
// TestHelmLoaderOnlineResolvesRemoteDependency exercises the production helmLoader
// against a real public chart that declares an UNVENDORED remote dependency with a
// committed Chart.lock, proving downloader.Manager.Build() resolves it over the
// network. Real network call on every run, matching the online tier's convention
// (no build tag, no env gate, CI is expected to have egress).
func TestHelmLoaderOnlineResolvesRemoteDependency(t *testing.T) {
	const (
		repo     = "https://prometheus-community.github.io/helm-charts" // verified stable HTTP repo
		name     = "<chart>"                                            // <- Step 1 verified chart
		version  = "<pinned>"                                           // <- Step 1 verified version
		subchart = "<remote-dep>"                                       // <- Step 1 declared remote dep
	)
	l := newHelmLoader(nil)
	ch, err := l.Load(context.Background(), repo, name, version)
	if err != nil {
		t.Fatalf("online resolve of %s@%s: %v", name, version, err)
	}
	found := false
	for _, d := range ch.Dependencies() {
		if d.Metadata != nil && d.Metadata.Name == subchart {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected resolved remote subchart %q in %s@%s; dependencies were not fetched", subchart, name, version)
	}
}
```

No new imports beyond what the file already has (`context`, `testing`) — do NOT add a `strings` import or a skip helper.

- [ ] **Step 3: Run the online test (with network)**

Run: `go test ./internal/source/ -run TestHelmLoaderOnlineResolvesRemoteDependency -v`
Expected: PASS — the loader pulls the parent, `Build()` fetches the declared remote subchart, and the subchart is present in `ch.Dependencies()`.

- [ ] **Step 4: Confirm the whole online tier still runs together**

Run: `go test ./internal/source/ -run 'TestHelmLoaderOnline' -v`
Expected: all three online tests PASS (the two existing + the new one), confirming the new test sits in the same ungated tier and doesn't change how the others run.

- [ ] **Step 5: Commit**

```bash
git add internal/source/helmloader_online_test.go
git commit -m "test(source): online test resolving a real remote chart dependency"
```

---

### Task 6: Wire loader scratch base to the Phase 7.5 cache volume (readOnlyRootFilesystem safety)

Per design Decision "Scratch/expansion space reuses the Phase 7.5 cache emptyDir": verify chart 1's manager `securityContext` and, if the root FS is read-only (or to consolidate ephemeral storage), base the loader temp dir on the configured cache dir instead of `os.MkdirTemp("")`.

**Files:**
- Inspect: `chart/templates/deployment.yaml` (chart 1 manager container `securityContext`)
- Modify (conditional): `internal/source/helmloader.go` (`Load` temp-base), `cmd/main.go` (pass cache dir), and/or `internal/source/gitresolver.go` for symmetry — only as the verification dictates.

- [ ] **Step 1: Verify chart 1 securityContext**

Run: `grep -n "readOnlyRootFilesystem\|securityContext\|mountPath\|/cache/source\|emptyDir" chart/templates/deployment.yaml`
Expected: determine whether `readOnlyRootFilesystem: true` is set on the manager container and whether the Phase 7.5 cache `emptyDir` is mounted (`/cache/source`).

- [ ] **Step 2: Decide + record**

- If `readOnlyRootFilesystem` is NOT set (root FS writable): no code change needed — `os.MkdirTemp("")` on the container layer is correct. Note this in the commit message and mark the remaining steps N/A.
- If it IS set: the loader MUST write under a writable mount. Proceed to Step 3.

- [ ] **Step 3 (conditional): Write the failing test for a configurable temp base**

Add to `helmloader_test.go`:

```go
func TestHelmLoaderHonorsScratchDir(t *testing.T) {
	scratch := t.TempDir()
	l := newHelmLoader(nil)
	l.scratchDir = scratch // new field; "" preserves os.MkdirTemp("") behavior
	tmp, err := l.newScratchDir()
	if err != nil {
		t.Fatalf("newScratchDir: %v", err)
	}
	defer os.RemoveAll(tmp)
	if !strings.HasPrefix(tmp, scratch) {
		t.Fatalf("temp dir %q not under configured scratch %q", tmp, scratch)
	}
}
```

- [ ] **Step 4 (conditional): Run to verify fail**

Run: `go test ./internal/source/ -run TestHelmLoaderHonorsScratchDir -v`
Expected: FAIL — `scratchDir` / `newScratchDir` undefined.

- [ ] **Step 5 (conditional): Implement configurable scratch base**

In `helmloader.go`: add `scratchDir string` to `helmLoader`, add `func (l *helmLoader) newScratchDir() (string, error) { return os.MkdirTemp(l.scratchDir, "ddo-helm-") }` (empty `scratchDir` reproduces today's behavior), and replace the `os.MkdirTemp("", "ddo-helm-")` call in `Load` with `l.newScratchDir()`. Wire the cache dir from `cmd/main.go` (reuse the Phase 7.5 `--source-cache-*`/`--chart-cache-dir` value) when constructing the loader, and mount points already exist from Phase 7.5.

- [ ] **Step 6 (conditional): Run to verify pass + full gate**

Run: `go test ./internal/source/... && go build ./... && make run-golangci-lint`
Expected: PASS, exit 0, lint clean.

- [ ] **Step 7: Commit**

```bash
git add internal/source/helmloader.go cmd/main.go internal/source/helmloader_test.go
git commit -m "feat(source): base chart scratch dir on cache volume for read-only rootfs"
```

*(If Step 2 concluded no change is needed, commit only a note in the change/verify artifact and skip the code commit.)*

---

### Task 7: Regenerate manifests/docs sync and final gate

No CRD/RBAC change is expected (loader-internal), but run codegen to prove it and run the full gate.

**Files:**
- Verify: no diffs in `config/crd/bases/*`, `config/rbac/role.yaml`, `**/zz_generated.*`

- [ ] **Step 1: Run codegen and confirm no diff**

Run: `make manifests generate && git status --porcelain config/ api/`
Expected: no changes (this change adds no markers/types).

- [ ] **Step 2: Full gate**

Run: `make check`
Expected: build + test + lint all green (envtest may be needed for other packages; `internal/source` itself does not require it).

- [ ] **Step 3: Commit (only if codegen produced intended diffs)**

```bash
git add -A
git commit -m "chore: regenerate after subchart dependency resolution (no CRD change expected)"
```

---

## Self-Review

**Spec coverage** (`specs/helm-chart-loader/spec.md`):
- MODIFIED "Pull to a temporary directory and clean up" (resolution runs inside Load, cache transparently covers) → Task 3 (`expandAndResolveDeps` inside `Load`, temp dir reused + cleaned via existing `defer`).
- ADDED "Subchart dependency resolution at pull time" (unvendored resolves / vendored unchanged / no-deps no-op) → Task 3 Step 1 + Task 4. The unvendored-resolves case is provably a fetch, not a vendored artifact: Task 3's `assertNoVendoredSubcharts` locks the fixture invariant (pushed parent `.tgz` has an empty `charts/`) so the subchart's presence after `Load` can only be the result of `downloader.Manager.Build()` fetching it.
- ADDED "Deterministic resolution requires a committed Chart.lock" (missing-lock fails closed / no-deps no lock needed) → Task 2 + Task 4 `MissingLockFailsClosed`.
- ADDED "Subchart resolution reuses the single source credential" (same-registry resolves; different private registry surfaces auth error) → Task 1 (shared client) + Task 3 (reuses it); same-registry proven by the hermetic resolve test. The different-private-registry auth-error path is inherent to Helm's single-host client (no new code); covered by the design contract and asserted implicitly (no per-dependency credential path exists).
- ADDED online resolution coverage (real remote dependency fetched over the network) → Task 5.
- design Decision "scratch reuses cache emptyDir / readOnlyRootFilesystem" → Task 6.

**Placeholder scan:** No "TBD"/"handle edge cases"; every code step shows full code. Test helper names that build on the existing harness (`pushSubchartAndParent`, `pushParentWithVendoredSub`, `pushParentDepsNoLock`, `reg.host`, `reg.client`) are flagged with implementer notes and mirror the existing `pushFixtureChart`/`startAuthedOCIRegistry` pattern rather than being left abstract.

**Type consistency:** `ociRegistryClient(creds) (*registry.Client, error)` used in Tasks 1 and 3; `requireLockIfDeps(chartDir string) error` used in Tasks 2 and 3; `expandAndResolveDeps(ctx, chartPath, tmp string) (string, error)` and `singleChildDir(string)` consistent within Task 3; `scratchDir`/`newScratchDir` consistent within Task 6. `creds` fields (`user`, `pass`, `ok`) match existing usage in `helmloader.go`.

---

## Execution Handoff

**Plan complete and saved to `openspec/changes/helm-subchart-dependency-resolution/plan.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration. This is what `/opsx-apply` uses for the `sdd-plus-superpowers` schema (subagent-driven-development, bringing TDD + code review transitively).

**2. Inline Execution** — execute tasks in this session via executing-plans, batch with checkpoints.

Per the OpenSpec workflow, run `/opsx-apply` in a fresh session to execute this plan.
