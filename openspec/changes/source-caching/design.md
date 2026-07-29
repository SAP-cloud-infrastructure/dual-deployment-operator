# Design: source-caching (Phase 7.5)

## Context

Phase 7 (`production-source-loaders`, merged PR #8) shipped both production source loaders
**without caching**: every reconcile pulls the Helm chart fresh (temp dir → load → cleanup) and
re-fetches the kustomize root (krusty then re-fetches transitive bases). Reconcile is
10-minute-scale and the fleet is small, so this is correct and acceptable; caching was
deliberately split into this phase because a *sound* cache has its own design (mutable refs,
transitive-tag immutability, Helm/kustomize symmetry).

This change adds an in-memory **rendered-manifest cache** to `internal/source`, sound by
construction via resolve-then-key.

Current render pipeline (unchanged by this design, wrapped by it):
`Source.Render(ctx, mode, namespace)` → Helm (`Load` tgz → template) or kustomize
(`Resolve` → krusty) → `[]manifest.Manifest` → `manifest.ApplyNamespace`.

## Goals / Non-Goals

**Goals**
- Cache the rendered `[]manifest.Manifest`, symmetric across Helm and kustomize.
- Sound invalidation with no TTL (resolve-then-key on immutable content ids).
- Zero correctness dependence on the cache (pure optimization; failures fall through).
- No CRD change; resolved-id auditing via a log line.

**Non-Goals (this change)**
- No disk cache / `emptyDir` (D3 — future consideration; drops the Phase 9 `emptyDir` cross-ref for now).
- No CRD `status.resolvedSource` field (E2 — future follow-up).
- No full transitive-SHA resolution for kustomize (C2 — documented caveat instead).
- No single-flight dedup of concurrent misses (future optimization).
- Cache cap is a package constant, not CRD/flag-configurable (promotable later).

## Architecture

A `cachingSource` **decorator** around `Source`, inserted in `source.From(...)`. Reconciler and
loader fetch mechanics are unchanged in shape.

**New file `internal/source/cache.go`:**
- `cachingSource` — implements `Source`; holds the inner `Source`, a `Resolver`, and a shared `*renderCache`.
- `renderCache` — in-memory entry-count LRU, concurrency-safe, `map key→[]manifest.Manifest`.
- key builder `cacheKey(sourceKind, repoScope, resolvedID, mode, inputHash, namespace)`.

**New capability — resolved-id resolution:**
```go
// Resolver reports the immutable content id a source currently points at,
// without fetching the full artifact. OCI: manifest digest. git: commit SHA.
// HTTP Helm: index.yaml chart digest or version.
type Resolver interface {
    ResolveID(ctx context.Context, mode Mode) (id string, err error)
}
```
- `helmLoader` implements it: `oci://` → manifest digest (HEAD-equivalent); classic `http(s)://` →
  `index.yaml` entry `digest` if present else the resolved version string (F1); else a
  "skip-caching" sentinel.
- `gitResolver` implements it: `ls-remote` on `base`+`ref` → commit SHA (SHA-ref short-circuits,
  no round-trip). `mode` does not affect the git id.

**Wiring (`cmd/main.go`)**: construct one shared `renderCache` at manager start, pass into
`source.Deps`. `source.From` wraps the built inner source in `cachingSource` when a cache is
present; nil cache ⇒ today's uncached behavior (tests and no-cache path unaffected).

**Data flow per render:**
```
Render(mode, ns)
  └─ ResolveID(mode)                    ← cheap: HEAD (OCI) / ls-remote (git) / index.yaml (HTTP)
  └─ key = sourceKind | repoScope | resolvedID | mode | inputHash | namespace
  └─ cache.Get(key) ── hit ──▶ return cached []manifest.Manifest
  └─ miss ──▶ inner.Render(mode, ns)    ← existing Phase-7 path (pull+template / clone+krusty+transitive)
           └─ log "resolved <source>@<id>"
           └─ cache.Put(key, manifests); return
```

On a cache **miss**, `ResolveID` is a second network touch separate from the render's own fetch
(small, rare redundancy accepted to keep the loader/render layers decoupled). On a **hit**, only
the cheap resolve is paid and all expensive work is skipped.

## Cache key

```
cacheKey = sourceKind "|" repoScope "|" resolvedID "|" mode "|" inputHash "|" namespace
```

1. **`sourceKind`** — `"helm"` / `"kustomize"`. **Redundant with `repoScope`'s scheme today**
   (the scheme tokens `oci`/`http` ⟹ helm, `git` ⟹ kustomize are disjoint, so `repoScope` already
   determines the kind). Retained as an **explicit, self-documenting kind tag** — near-zero cost
   and hardens the key against a future `repoScope` change that might weaken the scheme prefix
   (e.g. reducing it to bare host). Not load-bearing for collision avoidance on its own.
2. **`repoScope`** — transport + host, so OCI vs HTTP Helm (ids computed over different bytes)
   can never collide and same-named charts on different hosts stay distinct:
   - OCI: `"oci:" + ociHost(repo)`
   - HTTP: `"http:" + hostOf(repo)`
   - git: `"git:" + hostOf(base)`
   (reuses existing `ociHost` / `hostOf` from `helmloader.go`.)
3. **`resolvedID`** — immutable content id from `ResolveID` (OCI `sha256:…` / git commit SHA /
   HTTP index digest-or-version). The B1 soundness anchor: moved ref → new id → miss.
   **Bare-SHA `?ref=` decision**: a pinned SHA is used **as-is** as the `resolvedID` (it is already
   immutable; `ls-remote` cannot expand/verify a non-tip SHA anyway — see the git resolver note).
   Consequence: pinning the same commit once as a short SHA and once as the full 40-char SHA yields
   two distinct keys → two cache entries for identical content. This is **harmless** (≤1 redundant
   render + LRU slot, never incorrect) and accepted rather than forbidding short SHAs at CRD/CEL.
4. **`mode`** — `seed` / `shoot`. Same source renders differently per mode; this is why the cache
   lives at the `Render(mode, …)` boundary, not the loader.
5. **`inputHash`** — `sha256(canonical inputs)`:
   - Helm: `canonical(values) ‖ name ‖ version` (values marshaled sorted-key so map order is
     irrelevant).
   - kustomize: `subPath` (+ root subpath). `resolvedID` covers file content; subPath selects the overlay.
6. **`namespace`** — `Render` applies it to the rendered output, so it must be in the key.

**Not in the key**: transformations (`patch`/`rewriteWebhookURL`/`filterKinds`) run after
`Source.Render`, on the cached-or-fresh output, always re-applied fresh.

## LRU (bounding, eviction, concurrency, failure)

- **Structure**: prefer `hashicorp/golang-lru/v2` (`lru.Cache[string, []manifest.Manifest]`,
  thread-safe, O(1) eviction) if already vendored; else a ~40-line mutex + `container/list` LRU.
- **Bound**: entry count, default **32** (steady state ≈ 5 operators × 2 modes = 10, plus
  headroom). Package constant; not CRD/flag-configurable this change.
- **Eviction**: pure LRU by recency on `Put` past cap. No TTL (resolve-then-key handles
  invalidation; stale keys age out). Entries are immutable by key — a content change is a new key.
- **Concurrency**: LRU internally locked; `Get`/`Put` race-safe. No single-flight of concurrent
  identical misses (rare, harmless double-render; future optimization).
- **Failure modes** (cache is a pure optimization — never fatal):
  - `ResolveID` fails → fall through to a direct `inner.Render` (Phase-7 behavior), cache nothing,
    log at low verbosity. The render's own fetch surfaces any real error.
  - `inner.Render` errors → propagate; never cache failures. Only successful non-nil renders stored.
- **Memory**: entries on the manager heap; negligible at cap 32 with small manifest slices. A
  future large-chart operator is the trigger to revisit D3 (disk).
- **Observability**: debug-level hit/miss log with `sourceKind` + `mode` + short `resolvedID`
  (never values, never credentials), plus the info-level `resolved …@id` line.

## Resolve-then-key per loader

Credentials reuse the per-source resolver already wired (`withHelmCreds` / `withGitCreds`), so an
authed source resolves authenticated.

**git (`gitResolver.ResolveID`)** — remote listing without cloning. VERIFIED against go-git
**v5.19.1**: `git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name:"origin", URLs:[]string{base}})`
then `remote.ListContext(ctx, &git.ListOptions{Auth: auth, PeelingOption: git.AppendPeeled})`
→ `[]*plumbing.Reference`. `git.ListOptions` v5.19.1 has an `Auth transport.AuthMethod` field
(confirmed) plus `InsecureSkipTLS`, `CABundle`, `Timeout`, `PeelingOption`. `memory` is
`github.com/go-git/go-git/v5/storage/memory`.
- Match order mirrors `fetchPinned`: tag ref (`refs/tags/<ref>`), then branch ref
  (`refs/heads/<ref>`) → `.Hash().String()` (full 40-char SHA).
- **Annotated tags**: with `PeelingOption: AppendPeeled`, the peeled commit is advertised as
  `refs/tags/<ref>^{}`. Resolve prefers the peeled ref (the actual commit) and falls back to the
  bare tag ref's commit hash (lightweight tags).
- **Canonical id — never the ref name**: a tag/branch ref MUST resolve to a commit SHA. It MUST NOT
  fall back to returning the ref *name* (e.g. the string `v1.0.0`) as the id — otherwise a tag that
  sometimes resolves to a SHA and sometimes to its name would produce two keys for identical content
  (spurious misses). If a tag/branch cannot be resolved to a SHA (not advertised / peeling
  unavailable / transient error), `ResolveID` returns an **error** → the caller renders uncached and
  caches nothing (the pure-optimization failure path), rather than degrading to a name-based id.
- **Bare-SHA pin — IMPORTANT CORRECTION**: `ls-remote` only advertises ref *tips*; an arbitrary
  commit SHA that is not a tip **cannot** be resolved this way. So for a bare-SHA `?ref=`, do NOT
  attempt advertisement resolution — the SHA is *already* the immutable id, so `ResolveID` returns
  it directly (a full-40-char SHA is used as-is; a short SHA is used as-is as the key component —
  we do not expand it, since expansion is impossible without a fetch and unnecessary for keying).
  This supersedes the earlier "normalize short SHA to full via advertisement" note, which was
  incorrect. Returns the resolved 40-char SHA for tag/branch refs, or the pinned SHA string as-is.

**Helm OCI (`helmLoader.ResolveID`, `oci://`)** — VERIFIED against helm **v3.21.3**: the existing
`registry.Client` already exposes `Resolve(ref string) (ocispec.Descriptor, error)` — a manifest
resolve (no blob-layer pull) whose `.Digest.String()` is the `sha256:…` manifest digest. It
internally strips the `oci://` prefix and normalizes the tag, and reuses the client's configured
auth (`c.authorizer`), `plainHTTP`, and custom `http.Client`. So we reuse the **exact** existing
`registry.NewClient(ClientOptEnableCache/ClientOptBasicAuth/ClientOptHTTPClient)` construction
already in `helmloader.go` and call `rc.Resolve(ref)` — **no raw ORAS integration needed**. Fully
sound (digests immutable). (Raw `oras-go/v2` v2.6.1 `remote.Repository.Resolve` is the fallback if
we ever bypass Helm, but it is not needed here.)

**Helm classic HTTP(S) — F1 fallback** — fetch/parse `index.yaml`, resolve the requested version;
use the index entry `digest` when present (sound), else the exact version string (immutable by
convention). Gracefully degrade to a "skip-caching" sentinel only when neither is determinable.

**Cost**: git = one `ls-remote`; OCI = one manifest fetch (no layers); HTTP = one `index.yaml` GET
(already how Helm discovers charts). All far cheaper than the pull+template / clone+krusty+transitive
work they gate.

**Implementation notes (resolved via research)**: the go-git v5.19.1 `Remote.ListContext` /
`ListOptions{Auth, PeelingOption}` and Helm v3.21.3 `registry.Client.Resolve` signatures are
verified above; compile-ready snippets exist for both. No library-signature unknowns remain for
the plan.

## Transitive-tag caveat (C1)

For kustomize the root SHA pins the committed kustomization files, but those reference upstream
bases by tag (e.g. ipam-capi → `cluster-api-ipam-provider-in-cluster//config/...?ref=v1.1.0`,
`raw.githubusercontent.com/.../cluster-api/v1.13.4/...`). A force-moved upstream tag under an
unchanged root SHA would serve a stale render until pod restart. **Decision: trust upstream
release tags + document this caveat** (matches how the fleet pins release tags; worst case fixed
by a restart). Full transitive-SHA resolution (C2) is deferred.

## Testing strategy (TDD: RED → GREEN → REFACTOR)

**Cache core (`cache_test.go`, no network):**
- Key builder determinism; each dimension varied → distinct key; **explicit OCI-vs-HTTP
  non-collision test** (same chart/version, different `repoScope` → different keys).
- Values canonicalization: reordered maps → identical `inputHash`.
- LRU: hit returns cached manifests; eviction past cap drops LRU entry; concurrent `Get`/`Put`
  under `-race`.
- Failure modes: `ResolveID` error → falls through, nothing cached; `Render` error → propagated,
  not cached.

**`cachingSource` decorator (fakes):** counting fake `Source` + fixed-id fake `Resolver`. Assert
1st = miss (inner called once), 2nd identical = hit (inner not called), changed id = miss.
Assert the `resolved …@id` log line per render.

**`ResolveID` per loader:** git against the in-process git server (returns known SHA; SHA-ref
short-circuit); OCI against the `registry:2`/in-process registry (manifest digest; authed
variant); HTTP-F1 index with/without `digest` and the skip-caching sentinel.

**No new e2e** — caching is transparent at the render boundary; existing coverage exercises the
wrapped path (nil-cache where not opted in).

## Spec capabilities

1. **`render-cache`** — LRU + key (incl. `repoScope`, canonical values hash), entry-count bound
   (default 32), LRU eviction, no-TTL, concurrency safety, pure-optimization failure semantics.
2. **`source-id-resolution`** — `Resolver` interface + `ResolveID` for git / OCI / HTTP (F1),
   credential reuse, documented C1 caveat.
3. **`render-audit-logging`** — E1 resolved-id log line.

## Verification gate

`go build ./...` · `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./...` ·
`make run-golangci-lint` · `make manifests generate` (must be a **no-op** — confirms no API
change) · `make check`.

## Open questions (RESOLVED via research)

- **Resolve-step library signatures** — RESOLVED. go-git **v5.19.1**: `git.NewRemote(memory.NewStorage(), &config.RemoteConfig{...})`
  + `remote.ListContext(ctx, &git.ListOptions{Auth, PeelingOption: git.AppendPeeled})`; `ListOptions`
  has an `Auth transport.AuthMethod` field. Helm **v3.21.3**: `registry.Client.Resolve(ref) (ocispec.Descriptor, error)`
  exists and is the integration point — `.Digest.String()` gives the manifest digest, reusing the
  existing client + auth; no raw ORAS needed. (`oras-go/v2` v2.6.1 `remote.Repository.Resolve` is a
  documented fallback only.) One correction surfaced: a bare-SHA `?ref=` cannot be resolved via
  `ls-remote` (only ref tips are advertised) — it is already immutable and used as-is for keying.
- **LRU dependency** — RESOLVED. `github.com/hashicorp/golang-lru/v2 v2.0.5` is **already in the
  dependency graph** (present in `go.sum`, pulled transitively), just not a *direct* dep in `go.mod`.
  Promoting it to direct is a one-line `go mod tidy` with no new module download and no new
  supply-chain surface. **Decision: use `hashicorp/golang-lru/v2`** rather than a hand-rolled LRU.
