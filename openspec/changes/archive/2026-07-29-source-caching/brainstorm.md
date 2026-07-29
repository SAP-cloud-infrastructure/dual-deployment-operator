## Design Summary

Phase 7.5 adds a **rendered-manifest cache** to `internal/source`, sitting as a decorator
around `Source` at the single point where the Helm and kustomize paths converge on a
`[]manifest.Manifest` output. The cache is **sound by construction** via *resolve-then-key*:
before rendering, a cheap resolve step (OCI manifest digest via a HEAD-equivalent, git commit
SHA via `ls-remote`, HTTP chart digest/version via `index.yaml`) yields the immutable content
id the source currently points at; the cache is keyed on that resolved id rather than the
mutable ref string, so a moved `?ref=main` / re-tagged upstream automatically produces a cache
miss with no TTL. On a hit the operator skips the expensive work entirely — Helm pull+template,
or kustomize clone + krusty + all transitive base fetches. The cache is an in-memory,
entry-count-bounded LRU treated as a pure optimization: any resolve/render failure falls
through to the existing Phase-7 uncached path, and correctness never depends on the cache.

Key constraints that shaped the design: caching the **rendered manifests** (not the `.tgz`
or cloned root), keeping the loader/render layering untouched, a tiny fleet (~5 operators ×
2 modes ≈ 10 live keys) that makes an in-memory entry-count LRU sufficient, and no CRD change
(resolved-id auditing is a log line only).

## Alternatives Considered

### Option A: Wrap `Source` (cache at the render boundary) — CHOSEN
- **Approach**: A `cachingSource` decorator wraps the `Source` returned by `source.From(...)`.
  It calls a small `Resolver.ResolveID` capability on the loader to get the resolved content id,
  builds the cache key, checks a shared in-memory LRU, and on miss delegates to the inner
  `Render` and stores the resulting `[]manifest.Manifest`.
- **Pros**: caches exactly the rendered manifests (the requirement) at the one boundary where
  both Helm and kustomize converge → one cache, one key builder, fully symmetric. Inner
  `helmSource`/`kustomizeSource` stay untouched. Caching is orthogonal to rendering.
- **Cons**: on a cache **miss**, the resolve step is a second network touch separate from the
  render's own fetch (resolve, then the inner render fetches again) — a small, rare redundancy.
- **Why chosen**: the only approach that caches rendered manifests symmetrically without
  recoupling the loader and render layers.

### Option B: Cache inside each loader (`ChartLoader` / `RootResolver`)
- **Approach**: push caching down so `helmLoader.Load` and `gitResolver.Resolve` each
  resolve-then-key and cache their own output.
- **Pros**: resolve and fetch share one code path per loader (no separate resolve touch).
- **Cons**: the loaders return a `*chart.Chart` / a filesystem path, **not** rendered manifests
  — rendering (Helm template, krusty) happens *above* them. Caching rendered output here would
  force rendering down into the loaders, collapsing the clean loader/render split, and yields
  two caches / two key builders / asymmetric behavior.
- **Why not chosen**: conflicts with the explicit "cache the rendered manifests" requirement and
  the symmetric single-cache goal.

### Option C: Cache in the reconciler, keyed by CR + resolved id
- **Approach**: the controller holds the cache and performs the resolve; `Source` stays pure.
- **Pros**: cache lifecycle tied to the manager; easy to add metrics/status later.
- **Cons**: the reconciler would need to know how to resolve a digest/SHA per source kind,
  leaking source concerns upward and duplicating the Helm/kustomize discrimination that
  `source.From` already owns. Worse cohesion.
- **Why not chosen**: poor separation of concerns versus the decorator.

## Agreed Approach

**Option A — a `cachingSource` decorator around `Source`.** Inserted in `source.From(...)` when
a shared cache is present (nil cache ⇒ today's uncached behavior, so existing tests and the
no-cache path are unaffected). Per render:

```
Render(mode, ns)
  └─ ResolveID(mode)                    ← cheap: HEAD (OCI) / ls-remote (git) / index.yaml (HTTP)
  └─ key = sourceKind | repoScope | resolvedID | mode | inputHash | namespace
  └─ cache.Get(key) ── hit ──▶ return cached []manifest.Manifest   (skip pull+template / clone+krusty+transitive)
  └─ miss ──▶ inner.Render(mode, ns)    ← existing Phase-7 path
           └─ log "resolved <source>@<id>"   (auditing)
           └─ cache.Put(key, manifests); return
```

New surface: `internal/source/cache.go` (`cachingSource`, `renderCache` LRU, key builder) plus a
small `Resolver` interface (`ResolveID(ctx, mode) (id string, err error)`) implemented by
`helmLoader` (OCI digest / HTTP index) and `gitResolver` (`ls-remote`). `cmd/main.go` constructs
one shared `renderCache` at manager start and passes it into `source.Deps`.

## Key Decisions

- **Scope (A)**: cache the **rendered `[]manifest.Manifest`**, symmetric across Helm and
  kustomize. A kustomize hit skips clone + krusty + all transitive base fetches; a Helm hit
  skips pull + template.
- **Resolve-then-key (B1)**: key on the resolved immutable content id (OCI digest / git SHA /
  HTTP chart digest-or-version), not the mutable ref string. Auto-invalidates, no TTL. Requires
  adding a cheap resolve step (`ls-remote` for git, manifest HEAD for OCI, `index.yaml` for HTTP)
  that does not exist in the Phase-7 loaders.
- **Cache key** = `sourceKind | repoScope | resolvedID | mode | inputHash | namespace`.
  - `repoScope` = `oci:<host>` / `http:<host>` / `git:<host>` — an explicit transport+host
    component so an OCI Helm source and an HTTP Helm source (whose ids are computed over
    different bytes) can **never** collide, and same-named charts on different registries stay
    distinct. Reuses existing `ociHost`/`hostOf` helpers.
  - `inputHash` = `sha256(canonical inputs)` — Helm: canonical(values) ‖ name ‖ version;
    kustomize: subPath (+ root subpath). Values are canonicalized (sorted-key JSON) so map
    ordering does not change the hash.
  - `namespace` is in the key because `Render` applies it to the rendered output.
  - Transformations are **not** in the key — they run after `Source.Render`, on the cached
    output, and are always re-applied fresh.
- **Transitive-tag caveat (C1)**: for kustomize, key on the root SHA only; document that a
  force-moved upstream *transitive* tag under an unchanged root SHA can serve a stale render
  until pod restart. Not resolved via full transitive-SHA resolution in this change (that is
  the more-work option C2, deferred).
- **Storage (D1)**: in-memory LRU bounded by **entry count**, default cap **32** (steady state
  ≈ 10 keys). No disk, no `emptyDir`. Pure LRU eviction, no TTL. **D3 (disk cache on an
  `emptyDir`) is documented as a future consideration** for large-chart / cross-restart-warmth
  scenarios; this also drops the Phase 9 `emptyDir` cross-ref for now.
- **HTTP no-digest fallback (F1)**: resolve the chart version via `index.yaml`; use the index
  entry's `digest` when present (sound), else the exact version string (immutable by
  convention). Gracefully degrade to "skip caching" only when neither is determinable.
- **Auditing (E1)**: log a structured `resolved <source>@<id>` line per render. No CRD/`status`
  change (E2 remains an easy future follow-up).
- **Concurrency**: LRU is internally locked; `Get`/`Put` are race-safe. No single-flight dedup
  of concurrent identical misses (rare, harmless double-render; noted as a future optimization).
- **Failure semantics**: cache is a pure optimization — `ResolveID` failure ⇒ fall through to
  uncached render, cache nothing; `Render` error ⇒ propagate, never cache failures; only
  successful non-nil renders are stored.
- **Dependency**: use `hashicorp/golang-lru/v2` — RESOLVED via research: `v2.0.5` is already in
  the dependency graph (in `go.sum`, pulled transitively), just not a direct `go.mod` dep, so
  adoption is a one-line `go mod tidy` with no new module download / supply-chain surface. Chosen
  over a hand-rolled `container/list` LRU.

## Open Questions

- [x] Resolve-step library signatures — RESOLVED via research. go-git v5.19.1
  `git.NewRemote(memory.NewStorage(), …)` + `remote.ListContext(ctx, &git.ListOptions{Auth,
  PeelingOption: git.AppendPeeled})` (ListOptions has an `Auth` field); Helm v3.21.3
  `registry.Client.Resolve(ref) (ocispec.Descriptor, error)` is the OCI digest integration point
  (`.Digest.String()`), reusing the existing client + auth — no raw ORAS needed. Correction: a
  bare-SHA `?ref=` cannot be resolved via ls-remote (only ref tips advertised) → it is already
  immutable and used as-is for keying. Compile-ready snippets verified. See design.md.
- [x] `hashicorp/golang-lru/v2` — RESOLVED: already transitively vendored (`v2.0.5` in `go.sum`);
  promote to direct dep. Decision recorded above.
