# Brainstorm: source-renderers-helm-kustomize

## Design Summary

Phase 2 of the `dual-deployment-operator` build-out: implement the **source rendering layer** that turns a `DualDeploymentOperator` CR's `spec.source` into two independent, origin-tagged manifest streams (one for the host render, one for the remote render), per the two-render pattern in `docs/design.md` §3.5.

Two source types are supported via a discriminated union — Helm (`helm.sh/helm/v3`) and kustomize (`sigs.k8s.io/kustomize/api/krusty`). Each renders the source **once per mode** (`host` / `remote`) and returns a `[]manifest.Manifest`, where every manifest carries an `Origin` (`upstream` | `additions`) that downstream Phase 3 transformations depend on (notably `patch: target:{origin: upstream}`).

The key constraints that shaped the approach: (1) unit tests must not require network access (private OCI registry, remote git), so chart acquisition and kustomize-root acquisition are behind pluggable interfaces with local fakes; (2) the output contract (manifest stream + origin tag) is load-bearing for later phases, so the parse/tag logic is written once in a shared package and tested thoroughly; (3) the design already committed to a `Source` interface (§5.2), so this phase realizes that interface rather than re-deriving structure.

**In scope**: `internal/manifest` (Manifest type, multi-doc YAML parser, origin tagging), `internal/source` (`Source` interface, `From()` factory, Helm + kustomize renderers, pluggable fetchers with real + fake impls), go.mod deps.

**Out of scope** (later phases): transformations (Phase 3), delivery/SSA (Phase 5), reconciler wiring (Phase 6).

## Alternatives Considered

### Option A: Single `Source` interface, two implementations, shared parse/manifest layer

- **Approach**: One `internal/source` package exposing `Source.Render(ctx, mode) ([]manifest.Manifest, error)` and a `From(spec, deps) (Source, error)` factory. Two concrete types — `helmSource`, `kustomizeSource` — each wrapping its pluggable fetcher (`ChartLoader` / `RootResolver`). A separate `internal/manifest` package owns the `Manifest` type, the multi-doc YAML parser, and origin-tagging, shared by both renderers.
- **Pros**: Matches design §5.2 exactly; reconciler depends on one interface; parse/origin logic written and tested once; fetchers swappable for tests; small, independently testable units; the two locked pluggable-fetcher decisions plug directly into it.
- **Cons**: Slightly more files up front (fetcher interfaces + fakes).
- **Why not chosen**: This IS the chosen approach.

### Option B: Two standalone renderers, no shared interface yet

- **Approach**: Ship `helm.Render()` and `kustomize.Render()` as free functions; the reconciler switches on the discriminator directly. Introduce the `Source` interface only in Phase 6 when the reconciler is built.
- **Pros**: Fewer abstractions this phase.
- **Cons**: Diverges from design §5.2; reconciler later needs refactoring to introduce the interface; parse/origin logic duplicated across the two renderers unless separately factored anyway.
- **Why not chosen**: Defers an interface the design already committed to, for no real gain, and risks duplicating the load-bearing parse/tag logic.

### Option C: Unified renderer with an input-strategy

- **Approach**: One `Renderer` struct holding both a chart-values strategy and a kustomize strategy, selecting internally by discriminator.
- **Pros**: One entry type.
- **Cons**: Conflates two unrelated input models (Helm values merge vs kustomize overlay path) in one struct; harder to test in isolation; violates the design's discriminated-union shape.
- **Why not chosen**: Over-couples two sources that share nothing on the input side; only the output contract is shared, which Option A already factors cleanly.

## Agreed Approach

**Option A** — the design's stated structure (§5.2). The two renderers genuinely share the *output contract* (manifest stream + origin tag) but differ entirely in *input* (Helm values merge vs kustomize overlay path selection). A shared `Source` interface + shared `internal/manifest` parse/tag layer + per-source pluggable fetch is the natural seam: each unit is small, has one clear purpose, and is independently testable. The two pluggable-fetcher decisions (`ChartLoader`, `RootResolver`) plug directly into this shape, giving clean test seams without network access.

## Key Decisions

- **Package split (`internal/source` + `internal/manifest`)**: The output contract (parse + origin tag) is shared and load-bearing for Phase 3; factoring it into `internal/manifest` means it is written and tested once. `internal/source` holds the interface, factory, and the two renderers.

- **Pluggable `ChartLoader` interface for Helm chart acquisition**: `ChartLoader.Load(ctx, repo, name, version) (*chart.Chart, error)`. Production impl does OCI/HTTP pull with an LRU cache keyed by repo+name+version and OCI auth from pull secrets (private registry `keppel.eu-de-1.cloud.sap`). A fake impl loads a chart from a local directory. Rationale: keeps render logic unit-testable now without network/auth; the real pull+cache+auth is a thin, separately-tested implementation.

- **Pluggable `RootResolver` interface for kustomize root acquisition**: `RootResolver.Resolve(ctx, url, subPath) (fsPath, cleanup, error)`. Production impl resolves the remote `?ref=`-pinned git URL; a fake impl points at local testdata overlay dirs. krusty builds whatever root the resolver yields. Rationale: mirrors the ChartLoader choice — render logic testable now, remote git fetch is a thin swappable piece; avoids slow/flaky network in CI/envtest.

- **Origin tagging: annotation value `additions` → `OriginAdditions`, else `upstream`**: During parse, read `dual-deployment-operator.cc.sap/origin` off each manifest. Value exactly `additions` (set by chart `_helpers.tpl` for Helm or kustomize `commonAnnotations` on `additions/` overlays) → `OriginAdditions`; absent/empty/any other value → default `OriginUpstream`. Store on `Manifest.Origin`; **keep the annotation on the object** (delivery strips internal annotations in a later phase; transformations may re-read it). The default-to-upstream asymmetry is deliberate: upstream chart authors never set our annotation, so anything unlabeled is upstream by definition. The renderer trusts the author's label and does not validate it (mis-tags surface in Phase 7 equivalence tests). Full decision procedure — key, per-manifest steps, who declares it, downstream `patch` consumer, and validation stance — is documented in `design.md` § *Origin tagging procedure*. Rationale: exactly matches design §3.3; the annotation is the design's chosen origin mechanism.

- **`mode` value injection at top precedence + runtime guard**: For the Helm renderer, values merge in precedence order `chart.Values < spec.Values < spec.{Host,Remote}Values < {mode: <mode>}`. The operator injects `mode` at highest precedence. The renderer also rejects at runtime if merged user values already set `mode` (belt-and-suspenders; CRD/CEL admission should also reject `spec.values.mode`). Rationale: matches design §3.5.2 where chart templates guard on `.Values.mode`; the runtime guard defends against CRs applied to API servers without the CEL rule.

- **Helm render uses `action.Install{DryRun, ClientOnly, IncludeCRDs: true}`**: CRDs must be in the rendered stream (the remote render delivers them). Client-only dry-run avoids any cluster interaction during render.

- **Manifest parser: lenient split, strict per-doc, empty-OK**: Split multi-doc YAML; skip empty/whitespace/comment-only/null docs; **require `apiVersion` + `kind` on each real doc** (error if missing); preserve all valid docs as unstructured; an empty render (0 manifests) is allowed and returned as an empty slice, not an error. Rationale: tolerates real chart output (trailing `---`, comment blocks) while catching genuinely malformed objects before they reach transformations/delivery; legitimately-empty modes (a chart whose host or remote render is intentionally empty) are valid.

## Open Questions

- [x] ~~Exact go.mod versions for `helm.sh/helm/v3` and `sigs.k8s.io/kustomize/api`~~ — **Resolved**: pinned `helm.sh/helm/v3` v3.21.3 + `sigs.k8s.io/kustomize/api`/`kyaml` v0.21.1, verified to require exactly `k8s.io/*` v0.36.2 (probe `go get` + `go build ./...` clean, pins unchanged). See plan Task 1.
- [ ] Whether the real `ChartLoader` cache is process-lifetime LRU or a simpler per-reconcile cache — deferred to when the reconciler wires it in (Phase 6); Phase 2 only defines the interface and a real impl sufficient for rendering. — owner: implementer (Phase 6 refinement)
