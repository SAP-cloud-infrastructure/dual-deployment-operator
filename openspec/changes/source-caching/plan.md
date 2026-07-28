# Source Caching (Phase 7.5) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Trailing `- [ ]` checkboxes mark task-group completion — check them off AFTER all steps in the group complete.

**Goal:** Add a sound, in-memory rendered-manifest cache to `internal/source` so repeat reconciles skip Helm pull+template / kustomize clone+krusty+transitive fetches, keyed by the resolved immutable content id (resolve-then-key).

**Architecture:** A `cachingSource` decorator wraps the `Source` returned by `source.From`. Before rendering it calls a new `Resolver.ResolveID` on the loader (git `ls-remote` → commit SHA; OCI → manifest digest; HTTP → index digest/version), builds a key `sourceKind|repoScope|resolvedID|mode|inputHash|namespace`, and checks a shared entry-count LRU. Hits skip the inner render; misses render fresh and store. The cache is a pure optimization — resolve/render failures fall through to the existing uncached path. Loaders (`helmLoader`, `gitResolver`) stay unchanged except for the added `ResolveID` method.

**Tech Stack:** Go, `helm.sh/helm/v3 v3.21.3` (`registry.Client.Resolve`), `github.com/go-git/go-git/v5 v5.19.1` (`Remote.ListContext`), `github.com/hashicorp/golang-lru/v2 v2.0.5` (promote to direct dep), Ginkgo/Gomega + stdlib `testing`. Build/test/lint per SAP go-makefile-maker: `go build ./...`, `go test ./...`, `make run-golangci-lint`.

**Key decisions locked (from design.md):** cache rendered `[]manifest.Manifest`; resolve-then-key; key includes `repoScope` (oci/http/git scheme+host) to prevent OCI↔HTTP collision; `sourceKind` kept as documented-redundant defensive tag; bare-SHA `?ref=` used as-is; tag/branch MUST resolve to a commit SHA (never the ref name) or error; in-memory entry-count LRU (default cap 32), no TTL; resolved-id logged (no CRD change); HTTP index digest→version→skip-caching fallback.

---

## Task 1: Promote golang-lru/v2 to a direct dependency

**Files:**
- Modify: `go.mod` (move `github.com/hashicorp/golang-lru/v2` from indirect to direct)

- [ ] **Step 1: Add an import reference so tidy promotes it**

Temporarily nothing to edit yet; the import lands in Task 4. Run tidy after Task 4's first file exists. For now, verify it is present in the graph:

Run: `grep 'hashicorp/golang-lru/v2' go.sum`
Expected: a line like `github.com/hashicorp/golang-lru/v2 v2.0.5 h1:...`

- [ ] **Step 2: Note** — the actual `go.mod` promotion happens automatically via `go mod tidy` once `renderCache` (Task 4) imports `github.com/hashicorp/golang-lru/v2`. No manual `go.mod` edit needed. This task is a checkpoint, not a standalone edit.

- [x] Task 1 complete (verified golang-lru/v2 v2.0.5 is in go.sum; promotion deferred to Task 4 tidy)

---

## Task 2: Cache key builder + canonical values hash

**Files:**
- Create: `internal/source/cachekey.go`
- Test: `internal/source/cachekey_test.go`

- [ ] **Step 1: Write the failing test**

```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import "testing"

func TestCacheKey_DistinctDimensions(t *testing.T) {
	base := keyParts{sourceKind: "helm", repoScope: "oci:reg.example.com", resolvedID: "sha256:aaa", mode: "seed", inputHash: "h1", namespace: "ns1"}
	k := base.String()

	// Each dimension change must produce a different key.
	cases := []keyParts{
		func() keyParts { c := base; c.sourceKind = "kustomize"; return c }(),
		func() keyParts { c := base; c.repoScope = "http:reg.example.com"; return c }(),
		func() keyParts { c := base; c.resolvedID = "sha256:bbb"; return c }(),
		func() keyParts { c := base; c.mode = "shoot"; return c }(),
		func() keyParts { c := base; c.inputHash = "h2"; return c }(),
		func() keyParts { c := base; c.namespace = "ns2"; return c }(),
	}
	for i, c := range cases {
		if c.String() == k {
			t.Fatalf("case %d: expected distinct key, got same as base %q", i, k)
		}
	}
}

func TestCacheKey_OCIvsHTTPNoCollision(t *testing.T) {
	oci := keyParts{sourceKind: "helm", repoScope: "oci:reg.example.com", resolvedID: "x", mode: "seed", inputHash: "h", namespace: "ns"}
	http := oci
	http.repoScope = "http:reg.example.com"
	if oci.String() == http.String() {
		t.Fatal("OCI and HTTP sources must not share a cache key")
	}
}

func TestHashValues_OrderInsensitive(t *testing.T) {
	a := map[string]any{"a": 1, "b": map[string]any{"c": 2, "d": 3}}
	b := map[string]any{"b": map[string]any{"d": 3, "c": 2}, "a": 1}
	if hashValues(a) != hashValues(b) {
		t.Fatal("hashValues must be insensitive to map key ordering")
	}
	c := map[string]any{"a": 2}
	if hashValues(a) == hashValues(c) {
		t.Fatal("different values must hash differently")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run 'TestCacheKey|TestHashValues' -v`
Expected: FAIL — `undefined: keyParts`, `undefined: hashValues`

- [ ] **Step 3: Write minimal implementation**

```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// keyParts are the dimensions that uniquely identify a render for caching.
// sourceKind is redundant with repoScope's scheme today but retained as an
// explicit, self-documenting kind tag (see design.md).
type keyParts struct {
	sourceKind string // "helm" | "kustomize"
	repoScope  string // "oci:<host>" | "http:<host>" | "git:<host>"
	resolvedID string // OCI digest | git commit SHA | HTTP index digest-or-version
	mode       string // "seed" | "shoot"
	inputHash  string // hash of mode-specific inputs not covered by resolvedID
	namespace  string
}

// String renders a stable, collision-resistant cache key. "|" is a safe separator
// because no component contains it (hosts, hex ids, sha256 hex, k8s namespaces).
func (k keyParts) String() string {
	return strings.Join([]string{
		k.sourceKind, k.repoScope, k.resolvedID, k.mode, k.inputHash, k.namespace,
	}, "|")
}

// hashValues returns a sorted-key canonical hash of an arbitrary values map so
// that map ordering does not change the hash. json.Marshal sorts map keys.
func hashValues(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// A values map that fails to marshal cannot be keyed soundly; return a
		// sentinel that still hashes deterministically for identical failures.
		b = []byte("\x00unmarshalable")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/ -run 'TestCacheKey|TestHashValues' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/source/cachekey.go internal/source/cachekey_test.go
git commit -m "feat(source): add render cache key builder and canonical values hash"
```

- [x] Task 2 complete

---

## Task 3: renderCache — bounded LRU wrapper

**Files:**
- Create: `internal/source/rendercache.go`
- Test: `internal/source/rendercache_test.go`

- [ ] **Step 1: Write the failing test**

```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"sync"
	"testing"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

func TestRenderCache_GetPut(t *testing.T) {
	c, err := newRenderCache(2)
	if err != nil {
		t.Fatalf("newRenderCache: %v", err)
	}
	if _, ok := c.get("k1"); ok {
		t.Fatal("expected miss on empty cache")
	}
	want := []manifest.Manifest{{}}
	c.put("k1", want)
	got, ok := c.get("k1")
	if !ok || len(got) != len(want) {
		t.Fatalf("expected hit with %d manifests, got ok=%v len=%d", len(want), ok, len(got))
	}
}

func TestRenderCache_EvictsLRU(t *testing.T) {
	c, _ := newRenderCache(2)
	c.put("k1", []manifest.Manifest{{}})
	c.put("k2", []manifest.Manifest{{}})
	_, _ = c.get("k1")        // touch k1 so k2 is LRU
	c.put("k3", []manifest.Manifest{{}}) // evicts k2
	if _, ok := c.get("k2"); ok {
		t.Fatal("expected k2 to be evicted (LRU)")
	}
	if _, ok := c.get("k1"); !ok {
		t.Fatal("expected k1 to survive")
	}
	if _, ok := c.get("k3"); !ok {
		t.Fatal("expected k3 present")
	}
}

func TestRenderCache_ConcurrentRaceFree(t *testing.T) {
	c, _ := newRenderCache(8)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.put("k", []manifest.Manifest{{}})
			_, _ = c.get("k")
		}()
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestRenderCache -v`
Expected: FAIL — `undefined: newRenderCache`

- [ ] **Step 3: Write minimal implementation**

```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// defaultRenderCacheSize bounds the in-memory render cache by entry count.
// Steady state is ~5 operators x 2 modes = 10 live keys; 32 leaves headroom.
const defaultRenderCacheSize = 32

// renderCache is a concurrency-safe, entry-count-bounded LRU of rendered
// manifests, keyed by the cache key string. No TTL: invalidation comes from the
// resolved-id keying (a changed id is a new key). It is a pure optimization.
type renderCache struct {
	lru *lru.Cache[string, []manifest.Manifest]
}

func newRenderCache(size int) (*renderCache, error) {
	if size <= 0 {
		size = defaultRenderCacheSize
	}
	c, err := lru.New[string, []manifest.Manifest](size)
	if err != nil {
		return nil, err
	}
	return &renderCache{lru: c}, nil
}

func (c *renderCache) get(key string) ([]manifest.Manifest, bool) { return c.lru.Get(key) }

func (c *renderCache) put(key string, m []manifest.Manifest) { _ = c.lru.Add(key, m) }
```

- [ ] **Step 4: Run test to verify it passes (with race detector)**

Run: `go test ./internal/source/ -run TestRenderCache -race -v`
Expected: PASS, no race warnings

- [ ] **Step 5: Tidy to promote golang-lru/v2 to direct dep, then verify build**

Run: `go mod tidy && go build ./...`
Expected: `go.mod` now lists `github.com/hashicorp/golang-lru/v2` as a direct require (no `// indirect`); build exits 0

- [ ] **Step 6: Commit**

```bash
git add internal/source/rendercache.go internal/source/rendercache_test.go go.mod go.sum
git commit -m "feat(source): add bounded in-memory LRU render cache"
```

- [ ] Task 3 complete

---

## Task 4: errUnkeyable sentinel + git ResolveID/repoScope (ls-remote)

**Files:**
- Modify: `internal/source/source.go` (add `errUnkeyable` sentinel)
- Modify: `internal/source/gitresolver.go` (add `ResolveID` + `repoScope`)
- Test: `internal/source/gitresolver_test.go` (append; uses existing in-process git server harness in `gitresolver_authed_test.go`)

- [ ] **Step 1: Write the failing test**

Append to `internal/source/gitresolver_test.go`. This reuses the existing local-git-repo test pattern already in the package (see `gitresolver_test.go` / `gitresolver_authed_test.go` for the `file://` repo + commit/tag helpers; use the same helper names they define).

```go
func TestGitResolver_ResolveID_TagToSHA(t *testing.T) {
	// makeLocalRepo (existing helper in gitresolver_test.go) builds a repo with
	// one commit + tag "v1" and returns (fileURL, commitSHA).
	fileURL, wantSHA := makeLocalRepo(t)
	r := &gitResolver{}
	got, err := r.ResolveID(context.Background(), fileURL+"?ref=v1", ModeSeed)
	if err != nil {
		t.Fatalf("ResolveID: %v", err)
	}
	if got != wantSHA {
		t.Fatalf("ResolveID = %q, want commit SHA %q (never the tag name)", got, wantSHA)
	}
}

func TestGitResolver_ResolveID_UnresolvableRefErrors(t *testing.T) {
	fileURL, _ := makeLocalRepo(t)
	r := &gitResolver{}
	if _, err := r.ResolveID(context.Background(), fileURL+"?ref=nope", ModeSeed); err == nil {
		t.Fatal("expected error for a ref that resolves to no commit SHA (must not return the ref name)")
	}
}

func TestGitResolver_ResolveID_BareSHAUsedAsIs(t *testing.T) {
	r := &gitResolver{}
	sha := "0123456789abcdef0123456789abcdef01234567"
	got, err := r.ResolveID(context.Background(), "https://example.com/x/y?ref="+sha, ModeSeed)
	if err != nil {
		t.Fatalf("ResolveID: %v", err)
	}
	if got != sha {
		t.Fatalf("bare SHA must be used as-is: got %q want %q", got, sha)
	}
}

func TestGitResolver_repoScope(t *testing.T) {
	r := &gitResolver{}
	scope, err := r.repoScope("https://github.com/org/repo//root?ref=v1")
	if err != nil {
		t.Fatalf("repoScope: %v", err)
	}
	if scope != "git:github.com" {
		t.Fatalf("repoScope = %q, want git:github.com", scope)
	}
}
```

> The existing git test harness is `makeLocalRepo(t) (fileURL, sha string)` in `gitresolver_test.go` (commit + tag `v1`). Use it exactly; do NOT invent a new harness.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run 'TestGitResolver_ResolveID|TestGitResolver_repoScope' -v`
Expected: FAIL — `r.ResolveID undefined`, `r.repoScope undefined`

- [ ] **Step 3: Add errUnkeyable sentinel to source.go**

Insert after the `RootResolver` interface block in `internal/source/source.go`:

```go
// errUnkeyable signals a source cannot be soundly keyed for caching (e.g. an
// HTTP Helm repo whose index.yaml has neither a digest nor a matched version).
// The cachingSource decorator treats it like a resolve failure: render fresh,
// cache nothing.
var errUnkeyable = errors.New("source: unkeyable (skip caching)")
```

> Design note: we deliberately do NOT introduce a single `Resolver` interface. `gitResolver` and
> `helmLoader` have different natural call shapes — git resolves from a single `url`
> (`ResolveID(ctx, url, mode)`), Helm resolves from `(repo, name, version)`. Forcing both through
> one interface would distort one of them. Instead each loader exposes its own natural-shaped
> `ResolveID` + `repoScope` methods (Tasks 4–5), and the `cachingSource` decorator (Task 6) bridges
> them via a small `resolveFunc` closure per kind. This keeps each method honest and the decorator
> kind-agnostic.

- [ ] **Step 4: Implement git ResolveID + repoScope in gitresolver.go**

Add to `internal/source/gitresolver.go` (imports: add `github.com/go-git/go-git/v5/storage/memory`):

```go
// repoScope returns the transport+host scope for the cache key: "git:<host>".
func (r *gitResolver) repoScope(rawURL string) (string, error) {
	base, _, _, err := splitRef(rawURL)
	if err != nil {
		return "", err
	}
	return "git:" + hostOf(base), nil
}

// ResolveID resolves the pinned ?ref= to its immutable id without cloning.
// A bare SHA is returned as-is (ls-remote advertises only tips). A tag/branch
// MUST resolve to a commit SHA — never the ref name — or return an error.
func (r *gitResolver) ResolveID(ctx context.Context, rawURL string, _ Mode) (string, error) {
	base, _, ref, err := splitRef(rawURL)
	if err != nil {
		return "", err
	}
	if shaRe.MatchString(ref) {
		return ref, nil // already immutable; cannot expand a non-tip SHA via ls-remote
	}
	auth, err := r.authFor(ctx, base)
	if err != nil {
		return "", fmt.Errorf("source: resolve credentials: %w", err)
	}
	rem := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{base}})
	refs, err := rem.ListContext(ctx, &git.ListOptions{Auth: auth, PeelingOption: git.AppendPeeled})
	if err != nil {
		return "", fmt.Errorf("source: ls-remote %s: %w", base, err)
	}
	byName := make(map[string]*plumbing.Reference, len(refs))
	for _, ref := range refs {
		byName[ref.Name().String()] = ref
	}
	// Prefer peeled annotated-tag commit, then lightweight tag, then branch.
	for _, cand := range []string{
		"refs/tags/" + ref + "^{}",
		"refs/tags/" + ref,
		"refs/heads/" + ref,
	} {
		if rr, ok := byName[cand]; ok {
			return rr.Hash().String(), nil
		}
	}
	return "", fmt.Errorf("source: could not resolve ref %q on %s to a commit SHA", ref, base)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/source/ -run 'TestGitResolver' -v`
Expected: PASS (including the existing gitresolver tests)

- [ ] **Step 6: Commit**

```bash
git add internal/source/source.go internal/source/gitresolver.go internal/source/gitresolver_test.go
git commit -m "feat(source): add errUnkeyable sentinel and git ResolveID via ls-remote"
```

- [ ] Task 4 complete

---

## Task 5: Helm ResolveID (OCI digest + HTTP index fallback) + repoScope

**Files:**
- Modify: `internal/source/helmloader.go` (add `ResolveID`, `repoScope`, HTTP index resolution)
- Test: `internal/source/helmloader_test.go` (append; reuse the in-process registry / index harness in `helmloader_authed_test.go` / `helmloader_online_test.go`)

- [ ] **Step 1: Write the failing test**

Append to `internal/source/helmloader_test.go`. Reuse the existing in-process OCI registry harness (see `helmloader_authed_test.go` for how it stands up a registry + pushes a chart and the `httpClient` override).

```go
func TestHelmLoader_ResolveID_OCIDigest(t *testing.T) {
	// Existing harness in helmloader_authed_test.go: startAuthedOCIRegistry +
	// pushFixtureChart, with package consts authedTestUser/Pass/Chart/Ver.
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
	if s, _ := l.repoScope("oci://reg.example.com/charts"); s != "oci:reg.example.com" {
		t.Fatalf("oci repoScope = %q", s)
	}
	if s, _ := l.repoScope("https://charts.example.com"); s != "http:charts.example.com" {
		t.Fatalf("http repoScope = %q", s)
	}
}
```

> Reuse the existing OCI harness `startAuthedOCIRegistry`/`pushFixtureChart` and consts `authedTestUser/authedTestPass/authedTestChart/authedTestVer` from `helmloader_authed_test.go`. The harness does not expose the pushed digest, so assert the `sha256:` shape + stability rather than an exact value. Do NOT invent a new registry harness.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run 'TestHelmLoader_ResolveID|TestHelmLoader_repoScope' -v`
Expected: FAIL — `l.ResolveID undefined`, `l.repoScope undefined`

- [ ] **Step 3: Implement Helm repoScope + ResolveID**

Add to `internal/source/helmloader.go` (imports: add `ocispec "github.com/opencontainers/image-spec/specs-go/v1"` is NOT needed since we only read `.Digest.String()`; keep imports minimal):

```go
// repoScope returns the transport+host scope for the cache key.
func (l *helmLoader) repoScope(repo string) (string, error) {
	switch {
	case strings.HasPrefix(repo, "oci://"):
		return "oci:" + ociHost(repo), nil
	case strings.HasPrefix(repo, "http://"), strings.HasPrefix(repo, "https://"):
		return "http:" + hostOf(repo), nil
	default:
		return "", errors.New("source: unsupported chart repo scheme for repoScope")
	}
}

// ResolveID resolves the chart to its immutable id without pulling blob layers.
// OCI: the manifest digest (sha256:...). HTTP: the index.yaml entry digest, else
// the exact version string, else errUnkeyable (skip caching).
func (l *helmLoader) ResolveID(ctx context.Context, repo, name, version string) (string, error) {
	switch {
	case strings.HasPrefix(repo, "oci://"):
		return l.resolveOCIDigest(ctx, repo, name, version)
	case strings.HasPrefix(repo, "http://"), strings.HasPrefix(repo, "https://"):
		return l.resolveHTTPID(ctx, repo, name, version)
	default:
		return "", errors.New("source: unsupported chart repo scheme")
	}
}

func (l *helmLoader) resolveOCIDigest(ctx context.Context, repo, name, version string) (string, error) {
	c, err := l.credFor(ctx, ociHost(repo))
	if err != nil {
		return "", fmt.Errorf("source: resolve credentials: %w", err)
	}
	opts := []registry.ClientOption{registry.ClientOptEnableCache(true)}
	if c.ok {
		opts = append(opts, registry.ClientOptBasicAuth(c.user, c.pass))
	}
	if l.httpClient != nil {
		opts = append(opts, registry.ClientOptHTTPClient(l.httpClient))
	}
	rc, err := registry.NewClient(opts...)
	if err != nil {
		return "", err
	}
	ref := strings.TrimSuffix(strings.TrimPrefix(repo, "oci://"), "/") + "/" + name + ":" + version
	desc, err := rc.Resolve(ref) // Helm v3.21.3 registry.Client.Resolve; strips oci://, no blob pull
	if err != nil {
		return "", fmt.Errorf("source: resolve oci digest %s:%s: %w", name, version, err)
	}
	return desc.Digest.String(), nil
}
```

> The HTTP index resolution (`resolveHTTPID`) is implemented in Step 5 below to keep steps bite-sized.

- [ ] **Step 4: Run the OCI test to verify it passes**

Run: `go test ./internal/source/ -run 'TestHelmLoader_ResolveID_OCIDigest|TestHelmLoader_repoScope' -v`
Expected: PASS

- [ ] **Step 5: Implement HTTP index resolution + its test**

Add the HTTP test to `helmloader_test.go` (reuse the existing `index.yaml`-serving httptest harness in `helmloader_test.go`; match its helper name):

```go
func TestHelmLoader_ResolveID_HTTPIndexDigest(t *testing.T) {
	// Serve an anonymous index.yaml carrying a digest for the requested version.
	// makeMinimalChartTGZ exists in helmloader_test.go; we only need the digest here.
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
		// No digest field → fall back to the version string.
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
```

> These are self-contained `httptest` index servers (imports `net/http`, `net/http/httptest`, `fmt`, `cli` — all already used in the package tests). No new shared harness needed.

Add the implementation to `helmloader.go` (imports: add `"helm.sh/helm/v3/pkg/repo"` and `"sigs.k8s.io/yaml"` if not already present — verify against existing imports):

```go
func (l *helmLoader) resolveHTTPID(ctx context.Context, repoURL, name, version string) (string, error) {
	c, err := l.credFor(ctx, hostOf(repoURL))
	if err != nil {
		return "", fmt.Errorf("source: resolve credentials: %w", err)
	}
	idxURL := strings.TrimSuffix(repoURL, "/") + "/index.yaml"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, idxURL, nil)
	if err != nil {
		return "", err
	}
	if c.ok {
		req.SetBasicAuth(c.user, c.pass)
	}
	hc := l.httpClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("source: fetch index.yaml: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var idx repo.IndexFile
	if err := yaml.Unmarshal(body, &idx); err != nil {
		return "", fmt.Errorf("source: parse index.yaml: %w", err)
	}
	for _, e := range idx.Entries[name] {
		if e.Version == version {
			if e.Digest != "" {
				return e.Digest, nil // sound: index digest of the tgz
			}
			return version, nil // fall back to the (conventionally immutable) version
		}
	}
	return "", errUnkeyable // neither digest nor matched version → skip caching
}
```

> Verify the imports `io`, `net/http` are present (net/http already is); add `io`, `helm.sh/helm/v3/pkg/repo`, `sigs.k8s.io/yaml` as needed and run `goimports`/`make run-golangci-lint` to confirm.

- [ ] **Step 6: Run all Helm resolver tests**

Run: `go test ./internal/source/ -run 'TestHelmLoader_ResolveID' -v`
Expected: PASS (OCI digest, HTTP index digest)

- [ ] **Step 7: Commit**

```bash
git add internal/source/helmloader.go internal/source/helmloader_test.go
git commit -m "feat(source): add Helm ResolveID (OCI digest + HTTP index fallback)"
```

- [ ] Task 5 complete

---

## Task 6: cachingSource decorator + wire into From/Deps

**Files:**
- Create: `internal/source/cache.go` (`cachingSource`, per-kind key assembly, audit log)
- Modify: `internal/source/source.go` (`Deps.RenderCache`, wrap in `From`)
- Test: `internal/source/cache_test.go`

- [ ] **Step 1: Write the failing test (decorator hit/miss with fakes)**

```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

type fakeInner struct {
	calls int
	out   []manifest.Manifest
	err   error
}

func (f *fakeInner) Render(_ context.Context, _ Mode, _ string) ([]manifest.Manifest, error) {
	f.calls++
	return f.out, f.err
}

type fakeResolver struct {
	id    string
	scope string
	err   error
}

func (f *fakeResolver) resolve(_ context.Context, _ Mode) (id, scope string, err error) {
	return f.id, f.scope, f.err
}

func TestCachingSource_HitSkipsInner(t *testing.T) {
	cache, _ := newRenderCache(8)
	inner := &fakeInner{out: []manifest.Manifest{{}}}
	cs := &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "helm",
		resolve:    (&fakeResolver{id: "sha256:aaa", scope: "oci:h"}).resolve,
		inputHash:  "h1",
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 1 {
		t.Fatalf("expected inner called once (2nd is a hit), got %d", inner.calls)
	}
}

func TestCachingSource_ResolveErrorFallsThroughUncached(t *testing.T) {
	cache, _ := newRenderCache(8)
	inner := &fakeInner{out: []manifest.Manifest{{}}}
	cs := &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "helm",
		resolve:    (&fakeResolver{err: errors.New("boom")}).resolve,
		inputHash:  "h1",
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatalf("resolve failure must fall through, not error: %v", err)
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 2 {
		t.Fatalf("resolve error must not cache; expected 2 inner calls, got %d", inner.calls)
	}
}

func TestCachingSource_UnkeyableSkipsCache(t *testing.T) {
	cache, _ := newRenderCache(8)
	inner := &fakeInner{out: []manifest.Manifest{{}}}
	cs := &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "helm",
		resolve:    (&fakeResolver{err: errUnkeyable}).resolve,
		inputHash:  "h1",
	}
	_, _ = cs.Render(context.Background(), ModeSeed, "ns")
	_, _ = cs.Render(context.Background(), ModeSeed, "ns")
	if inner.calls != 2 {
		t.Fatalf("unkeyable must skip cache; expected 2 inner calls, got %d", inner.calls)
	}
}

func TestCachingSource_RenderErrorNotCached(t *testing.T) {
	cache, _ := newRenderCache(8)
	inner := &fakeInner{err: errors.New("render fail")}
	cs := &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "helm",
		resolve:    (&fakeResolver{id: "sha256:aaa", scope: "oci:h"}).resolve,
		inputHash:  "h1",
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err == nil {
		t.Fatal("expected error propagated")
	}
	if _, err := cs.Render(context.Background(), ModeSeed, "ns"); err == nil {
		t.Fatal("expected error again (not cached)")
	}
	if inner.calls != 2 {
		t.Fatalf("render error must not cache; expected 2 inner calls, got %d", inner.calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -run TestCachingSource -v`
Expected: FAIL — `undefined: cachingSource`

- [ ] **Step 3: Implement cachingSource in cache.go**

```go
// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// resolveFunc resolves a source (for a mode) to its immutable id + repoScope.
// It returns errUnkeyable to signal "skip caching, render fresh".
type resolveFunc func(ctx context.Context, mode Mode) (id, scope string, err error)

// cachingSource decorates an inner Source with a render-result cache, keyed by
// the resolved immutable content id (resolve-then-key). The cache is a pure
// optimization: any resolve failure falls through to a direct inner render.
type cachingSource struct {
	inner      Source
	cache      *renderCache
	sourceKind string      // "helm" | "kustomize"
	resolve    resolveFunc // closes over the loader's ResolveID + repoScope + url
	inputHash  string      // precomputed mode-independent portion; combined with mode below
}

func (c *cachingSource) Render(ctx context.Context, mode Mode, namespace string) ([]manifest.Manifest, error) {
	l := log.FromContext(ctx)

	id, scope, err := c.resolve(ctx, mode)
	switch {
	case errors.Is(err, errUnkeyable):
		l.V(1).Info("Source is unkeyable; rendering without cache", "kind", c.sourceKind)
		return c.inner.Render(ctx, mode, namespace)
	case err != nil:
		l.V(1).Info("Resolve failed; rendering without cache", "kind", c.sourceKind, "err", err.Error())
		return c.inner.Render(ctx, mode, namespace)
	}

	l.Info("Resolved source", "kind", c.sourceKind, "id", id) // E1 audit line

	key := keyParts{
		sourceKind: c.sourceKind,
		repoScope:  scope,
		resolvedID: id,
		mode:       string(mode),
		inputHash:  c.inputHash,
		namespace:  namespace,
	}.String()

	if m, ok := c.cache.get(key); ok {
		l.V(1).Info("Render cache hit", "kind", c.sourceKind, "mode", string(mode))
		return m, nil
	}
	m, err := c.inner.Render(ctx, mode, namespace)
	if err != nil {
		return nil, err // never cache failures
	}
	c.cache.put(key, m)
	l.V(1).Info("Render cache miss stored", "kind", c.sourceKind, "mode", string(mode))
	return m, nil
}
```

- [ ] **Step 4: Run decorator tests to verify they pass**

Run: `go test ./internal/source/ -run TestCachingSource -race -v`
Expected: PASS

- [ ] **Step 5: Wire RenderCache into Deps and wrap in From**

In `internal/source/source.go`, add to `Deps`:

```go
	// RenderCache, when non-nil, enables the render-result cache. When nil the
	// source is returned unwrapped (uncached behavior preserved).
	RenderCache *renderCache
```

Then wrap at the end of each `From` branch. Replace the helm branch return:

```go
	case spec.Helm != nil && spec.Kustomize == nil:
		if deps.ChartLoader == nil {
			return nil, errors.New("source: helm source requires a ChartLoader (none configured)")
		}
		loader := withHelmCreds(deps.ChartLoader, deps.CredentialResolver, spec.Helm.AuthSecretRef)
		inner := &helmSource{spec: spec.Helm, loader: loader}
		return wrapHelm(inner, loader, spec.Helm, deps.RenderCache), nil
```

and the kustomize branch return:

```go
	case spec.Kustomize != nil && spec.Helm == nil:
		if deps.RootResolver == nil {
			return nil, errors.New("source: kustomize source requires a RootResolver (none configured)")
		}
		resolver := withGitCreds(deps.RootResolver, deps.CredentialResolver, spec.Kustomize.AuthSecretRef)
		inner := &kustomizeSource{spec: spec.Kustomize, resolver: resolver}
		return wrapKustomize(inner, resolver, spec.Kustomize, deps.RenderCache), nil
```

Add the two wrap helpers to `cache.go`. These bind the loader's `ResolveID`/`repoScope` and precompute `inputHash`. If the loader is not the production type or cache is nil, return the inner source unwrapped:

```go
func wrapHelm(inner Source, loader ChartLoader, spec *v1alpha1.HelmSource, cache *renderCache) Source {
	hl, ok := loader.(*helmLoader)
	if !ok || cache == nil {
		return inner
	}
	// inputHash: mode-specific values are folded in per-mode inside resolve via
	// the merged values; here we hash the static name+version+common values.
	base := map[string]any{"name": spec.Name, "version": spec.Version}
	if common, err := mergeValues(spec.Values, nil); err == nil {
		base["values"] = common
	}
	return &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "helm",
		inputHash:  hashValues(base),
		resolve: func(ctx context.Context, _ Mode) (string, string, error) {
			scope, err := hl.repoScope(spec.Repo)
			if err != nil {
				return "", "", err
			}
			id, err := hl.ResolveID(ctx, spec.Repo, spec.Name, spec.Version)
			if err != nil {
				return "", "", err
			}
			return id, scope, nil
		},
	}
}

func wrapKustomize(inner Source, resolver RootResolver, spec *v1alpha1.KustomizeSource, cache *renderCache) Source {
	gr, ok := resolver.(*gitResolver)
	if !ok || cache == nil {
		return inner
	}
	return &cachingSource{
		inner:      inner,
		cache:      cache,
		sourceKind: "kustomize",
		inputHash:  hashValues(map[string]any{"seedPath": spec.SeedPath, "shootPath": spec.ShootPath}),
		resolve: func(ctx context.Context, _ Mode) (string, string, error) {
			scope, err := gr.repoScope(spec.URL)
			if err != nil {
				return "", "", err
			}
			id, err := gr.ResolveID(ctx, spec.URL, ModeSeed) // mode irrelevant for git id
			if err != nil {
				return "", "", err
			}
			return id, scope, nil
		},
	}
}
```

> `inputHash` note: mode selects `seedPath`/`shootPath` (kustomize) or `seedValues`/`shootValues` (helm). Since `mode` is already a key dimension AND both paths/values are in `inputHash`, per-mode uniqueness is guaranteed. Keeping the full set (both modes' inputs) in `inputHash` is intentional — it is stable across the two `Render` calls and mode disambiguates them. This satisfies the "differing values → distinct keys" and "mode is part of the key" scenarios.

- [ ] **Step 6: Run the full source package (with race)**

Run: `go test ./internal/source/ -race`
Expected: PASS (all existing + new tests)

- [ ] **Step 7: Verify wiring compiles across the module**

Run: `go build ./...`
Expected: exit 0

- [ ] **Step 8: Commit**

```bash
git add internal/source/cache.go internal/source/source.go internal/source/cache_test.go
git commit -m "feat(source): add cachingSource decorator and wire render cache into From/Deps"
```

- [ ] Task 6 complete

---

## Task 7: Construct the shared cache in the manager

**Files:**
- Modify: `cmd/main.go:195-197` (add `RenderCache` to `source.Deps`)

- [ ] **Step 1: Read the current wiring**

Run: `sed -n '190,200p' cmd/main.go`
Expected: shows `SourceDeps: source.Deps{ ChartLoader: source.NewHelmLoader(), RootResolver: source.NewGitResolver() }`

- [ ] **Step 2: Add a NewRenderCache constructor to source.go**

Export a constructor so `cmd/main.go` can build the shared cache without touching internals:

```go
// NewRenderCache returns a shared render cache with the default capacity, or nil
// (uncached) if construction fails — the operator MUST still run without a cache.
func NewRenderCache() *renderCache {
	c, err := newRenderCache(defaultRenderCacheSize)
	if err != nil {
		return nil
	}
	return c
}
```

- [ ] **Step 3: Wire it in cmd/main.go**

Modify the `source.Deps` literal:

```go
		SourceDeps: source.Deps{
			ChartLoader:  source.NewHelmLoader(),
			RootResolver: source.NewGitResolver(),
			RenderCache:  source.NewRenderCache(),
		},
```

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: exit 0

- [ ] **Step 5: Commit**

```bash
git add internal/source/source.go cmd/main.go
git commit -m "feat: construct shared render cache in manager wiring"
```

- [ ] Task 7 complete

---

## Task 8: Full verification gate + codegen no-op check

**Files:** none (verification only)

- [ ] **Step 1: Confirm no CRD/API change slipped in (must be a no-op)**

Run: `make manifests generate && git status --porcelain`
Expected: no changes to `config/crd/**`, `config/rbac/role.yaml`, or `zz_generated.*` (empty porcelain output for those paths) — proves the change added no API surface

- [ ] **Step 2: Lint**

Run: `make run-golangci-lint`
Expected: no findings in the new/modified files. If any appear, fix them (verify against the diff — do not dismiss as pre-existing without checking).

- [ ] **Step 3: Full unit test suite with race**

Run: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./... -race`
Expected: PASS (internal/source needs no envtest; the flag covers controller/webhook suites)

- [ ] **Step 4: Full build**

Run: `go build ./...`
Expected: exit 0

- [ ] **Step 5: Final commit if any lint fixes were made**

```bash
git add -A
git commit -m "chore(source): lint fixes for render cache"
```

- [ ] Task 8 complete
