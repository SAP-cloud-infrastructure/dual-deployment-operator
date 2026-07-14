# Design: source-renderers-helm-kustomize

## Context

The `dual-deployment-operator` scaffold (Phase 0+1, archived) provides the `v1alpha1` CRD types, a no-op reconciler, and CEL admission validation. The CRD's `spec.source` is a discriminated union — exactly one of `helm` or `kustomize` — and `spec.transformations` references an origin distinction (`upstream` | `additions`) that transformations select on.

Nothing yet turns a `spec.source` into manifests. This phase (Phase 2 in `docs/implementation.md`) builds the **source rendering layer**: the code that renders the source and produces the manifest streams the operator will later transform (Phase 3) and apply (Phase 5), driven by the reconciler (Phase 6).

The governing design is the **two-render pattern** (`docs/design.md` §3.5): the source is rendered twice per reconcile — once in `host` mode, once in `remote` mode — using mode-specific configuration so each render emits exactly the resources for its target cluster. No post-render split, no routing rules. Each render's output is a coherent, origin-tagged manifest stream.

**Constraints:**
- **No network in unit tests.** Production pulls Helm charts from a private OCI registry (`keppel.eu-de-1.cloud.sap`) and kustomize roots from `?ref=`-pinned git URLs. Tests must run offline in CI/envtest, so acquisition is behind pluggable interfaces with local fakes.
- **Origin tagging is a load-bearing output contract.** Phase 3's `patch: target:{origin: upstream}` depends on every manifest carrying a correct `Origin`. This must be correct and thoroughly tested now.
- **Match the committed interface.** `docs/design.md` §5.2 already defines `Source.Render(ctx, mode)` and the `Manifest` shape; this phase realizes them rather than inventing new structure.
- **Toolchain pins**: `k8s.io/*` v0.36.2, controller-runtime v0.24.1, Go 1.26.

**Stakeholders**: the operator's later phases (transform, deliver, reconcile) consume this layer; the five candidate operators (metal, boot, argora, khalkeon, ipam-capi) are the eventual sources.

## Goals / Non-Goals

**Goals:**
- Define `internal/manifest`: a `Manifest` type (`*unstructured.Unstructured` + `Origin`), a multi-doc YAML parser, and origin-tagging.
- Define `internal/source`: a `Source` interface, a `From(spec, deps)` discriminator factory, and two renderers (`helmSource`, `kustomizeSource`).
- Helm renderer: acquire chart via a pluggable `ChartLoader`, merge values in Helm precedence with top-precedence `mode` injection (+ runtime reject of user-set `mode`), render with `IncludeCRDs=true`, parse to origin-tagged manifests.
- Kustomize renderer: acquire overlay root via a pluggable `RootResolver` (mode → `hostPath`/`remotePath`), krusty-build, parse to origin-tagged manifests.
- Add `helm.sh/helm/v3` v3.21.3 and `sigs.k8s.io/kustomize/api` + `kyaml` v0.21.1 to go.mod (pinned; verified to require exactly `k8s.io/*` v0.36.2); build clean.
- **Phase success criterion** (design §Success table, Phase 2): both renderers produce parsable host/remote manifest streams for a metal-operator fixture with correct origin tags.

**Non-Goals:**
- Transformations (Phase 3), delivery/SSA (Phase 5), reconciler wiring / two-render orchestration in the real reconcile loop (Phase 6).
- Production OCI-auth hardening and cache-eviction policy beyond a minimal working `ChartLoader` impl (cache strategy refined in Phase 6).
- Real remote git fetching wired into the reconciler (the real `RootResolver` impl is defined and buildable; its reconciler wiring is Phase 6).
- Equivalence tests against today's chart output (Phase 7).
- Pulling the five real upstream sources (metal-operator chart from `keppel.eu-de-1.cloud.sap`, ipam-capi kustomize from its pinned git ref, etc.) or verifying registry/git access from this environment. Phase 2 validates the rendering *code* against real-shaped local fixtures only. Whether a real source contains the expected resources is verified by the Phase 7 equivalence tests (operator output vs. today's `make build-` output); whether the operator can reach real sources is an environment/egress concern verified in a real shoot at Phase 4, with seed→source egress feasibility tracked in design.md §9.1 (gated before Phase 7). A local `helm pull` / git fetch succeeding here would not represent the in-cluster token-requestor + egress path, so it is deliberately not a Phase 2 signal.
- Any change to CRD types or admission validation.

## Decisions

**Decision: Package structure — `internal/source` + shared `internal/manifest` (Option A)**
- Chosen: One `internal/source` package with a `Source` interface, `From()` factory, and two renderer types; a separate `internal/manifest` package owning `Manifest`, the parser, and origin-tagging.
- Reason: The two renderers share only the *output contract* (manifest stream + origin tag), which is load-bearing for Phase 3; factoring it into `internal/manifest` writes/tests it once. They differ entirely on *input* (Helm values vs kustomize overlay path), so a single interface + per-source renderer is the natural seam. Matches design §5.2.
- Alternatives considered: (B) standalone free-function renderers with no interface — rejected: diverges from §5.2, forces later reconciler refactor, risks duplicating parse/tag logic. (C) unified renderer with an input strategy — rejected: conflates two unrelated input models, harder to test in isolation.

**Decision: Pluggable `ChartLoader` for Helm chart acquisition**
- Chosen: `ChartLoader.Load(ctx, repo, name, version) (*chart.Chart, error)`; production impl does OCI/HTTP pull + cache + auth; a fake loads a local chart dir. The renderer depends on the interface.
- Reason: Keeps render logic unit-testable offline; the real pull+cache+auth is a thin, separately-tested implementation.
- Alternatives considered: full inline pull+cache in the renderer — rejected: forces network/auth mocking into every render test. Local-path-only for this phase — rejected: leaves the renderer unusable against real repos and defers auth risk without buying much.

**Decision: Pluggable `RootResolver` for kustomize root acquisition**
- Chosen: `RootResolver.Resolve(ctx, url, subPath) (fsPath string, cleanup func(), err error)`; production impl resolves the remote `?ref=`-pinned git URL; a fake points at local testdata. krusty builds whatever root is yielded.
- Reason: Mirrors `ChartLoader`; render logic testable offline; remote git fetch is a thin swappable piece; avoids slow/flaky network in CI.
- Alternatives considered: direct remote krusty always — rejected: every test needs network to a pinned ref. Local-only this phase — rejected: defers the remote path entirely.

**Decision: Origin tagging — annotation-present → `additions`, else `upstream`; keep annotation**
- Chosen: Parser reads `dual-deployment-operator.cc.sap/origin`; present with value `additions` → `OriginAdditions`, otherwise (absent, empty, or any other value) → fallback (`OriginUpstream`). Store on `Manifest.Origin`; leave the annotation on the object. Full procedure in **§ Origin tagging procedure** below.
- Reason: Exactly the design's chosen mechanism (§3.3). Chart authors set the annotation via Helm `_helpers.tpl` / kustomize `commonAnnotations`; upstream resources lack it. Delivery strips internal annotations in a later phase, so keeping it now is harmless and lets transformations re-read it.
- Alternatives considered: strip annotation at parse — rejected: delivery already strips later; premature. Structural origin detection (trust subchart boundaries) — rejected: unreliable across Helm subchart boundaries; design explicitly chose the annotation.

### Origin tagging procedure

Every manifest returned by a render carries an `Origin` (`upstream` | `additions`). The renderer does **not infer** origin structurally — it reads a single, author-declared annotation and defaults everything else to `upstream`.

**Annotation key** (constant):

```
dual-deployment-operator.cc.sap/origin
```

**Decision procedure**, applied per manifest during `manifest.Parse(raw, fallback)` where `fallback = OriginUpstream`:

1. Read `metadata.annotations["dual-deployment-operator.cc.sap/origin"]` off the parsed object.
2. If the value is exactly `"additions"` → `Manifest.Origin = OriginAdditions`.
3. Otherwise — annotation absent, empty, or any other value → `Manifest.Origin = fallback` (`OriginUpstream`).
4. The annotation is **left on the object** (not stripped). `Manifest.Origin` mirrors it for cheap access; a later delivery phase strips internal annotations before apply.

Only `additions` is a recognized positive value. The default is `upstream` so that upstream chart authors — who never know to set our annotation — are always classified correctly without any action on their part. This is the load-bearing asymmetry: **our team labels our own additions; everything unlabeled is upstream by definition.**

**Who declares the annotation, and where:**

- **Helm**: the wrapper chart's own templates stamp it via a `_helpers.tpl` helper (design §3.5.2). The upstream operator ships as a Helm **subchart dependency**; Helm subcharts cannot post-process each other's output, so upstream resources emerge un-annotated and default to `upstream`.

  ```yaml
  # templates/webhook-service.yaml (a manifest authored by our team)
  metadata:
    annotations:
      dual-deployment-operator.cc.sap/origin: additions
  ```

- **Kustomize**: the `additions/host/` and `additions/remote/` overlays set it via `commonAnnotations` in their `kustomization.yaml` (design §3.5.3), stamping every resource in those directories. Upstream refs (`manager/`, `managedresources/`, `webhooks/`) are un-annotated and default to `upstream`.

**Why it matters (downstream consumer):** Phase 3's `patch` transformation targets by origin — e.g. `patch: target: {origin: upstream}` patches the upstream Deployment and deliberately spares a same-named `additions` resource. A mistagged resource would send the patch to the wrong object, so this classification is a load-bearing output contract of this phase and is covered by the parser's table-driven tests (annotation present → additions; absent → upstream; other value → upstream).

**Origin vs. routing — independent axes (do not conflate).** Origin (`upstream` | `additions`) is **not** a deployment/routing switch. These are two orthogonal questions answered by two unrelated mechanisms:

| Question | Answered by | Annotation involved? |
|---|---|---|
| **WHERE** a resource is applied (host cluster vs remote cluster) | **which render produced it** — the two-render pattern; Helm `{{ if eq .Values.mode "host"/"remote" }}` guards + `hostValues`/`remoteValues`, or kustomize `hostPath`/`remotePath` overlays | **No.** Destination is implicit from the pass; nothing tags a resource with its target cluster. |
| **WHO** authored a resource (upstream chart vs our additions) | the `dual-deployment-operator.cc.sap/origin` annotation → `Manifest.Origin` | **Yes** — this annotation, and only for authorship. |

The chart maintainer therefore does **not** inject any "host" or "remote" annotation — routing is already fully decided by which pass emits the resource. The only annotation they set is `origin: additions` on resources their own team wrote, so that **within a single render** a Phase 3 `patch: target:{origin: upstream}` can distinguish the upstream object from a same-named addition. Origin never crosses passes and never influences host-vs-remote placement. A single render legitimately contains both origins (e.g. the metal-operator host render holds the `upstream` controller-manager Deployment alongside `additions` Services/ConfigMaps — all destined for the host). Were origin ever used to route, that would regress to the single-render-plus-split design that §3.5.5 rejected.

**Validation stance (this phase):** the renderer trusts the author's label. It does **not** verify that an `additions`-tagged resource genuinely originated from our additions, nor reject unknown annotation values (they simply fall through to `upstream`). Detecting a mislabeled resource is out of scope here; the Phase 7 equivalence tests (operator output vs. today's chart output) are where such a mistake would surface. See Risks / Trade-offs.

**Decision: `mode` injection at top precedence + runtime guard (Helm)**
- Chosen: Merge `chart.Values < spec.Values < spec.{Host,Remote}Values < {mode: <mode>}`; reject at runtime if merged user values already set `mode`.
- Reason: Matches design §3.5.2 (chart templates guard on `.Values.mode`). The runtime guard defends against CRs applied to API servers lacking the CEL rule.
- Alternatives considered: no runtime guard (rely solely on admission) — rejected: no defense without CEL. No mode injection — rejected: diverges from design, forces charts to reconstruct mode.

**Decision: Helm render via `action.Install{DryRun, ClientOnly, IncludeCRDs: true}`**
- Chosen: Client-only dry-run install to render templates, CRDs included in-stream.
- Reason: The remote render must deliver CRDs, so they must be in the manifest stream. Client-only avoids any cluster interaction during render.
- Alternatives considered: `helm template` equivalent without CRDs — rejected: would drop CRDs from the remote render.

**Decision: Manifest parser — lenient split, strict per-doc, empty-OK**
- Chosen: Split multi-doc YAML; skip empty/whitespace/comment-only/null docs; require `apiVersion`+`kind` on each real doc (error if missing); preserve valid docs as unstructured; empty render (0 manifests) returns an empty slice, not an error.
- Reason: Tolerates real chart output (trailing `---`, comments) while catching malformed objects before they reach downstream phases; legitimately-empty modes are valid.
- Alternatives considered: strict (empty render = error) — rejected: breaks intentionally-empty modes. Minimal (silently drop bad docs) — rejected: passes malformed objects downstream.

## Risks / Trade-offs

- [go.mod version conflicts: `helm.sh/helm/v3` and `sigs.k8s.io/kustomize/api` might pull `k8s.io/*` versions conflicting with the current v0.36.2 pins] → **Resolved**: pinned to `helm.sh/helm/v3` v3.21.3 and `sigs.k8s.io/kustomize/api`/`kyaml` v0.21.1, each verified (via probe `go get` + `go build ./...`) to require exactly `k8s.io/*` v0.36.2 with no pin movement. Implementation uses these exact versions (not `@latest`); see plan Task 1.
- [Strategic-merge/origin annotation collision: a chart could set the origin annotation on an upstream resource by mistake] → Documented contract: only additions carry the annotation. Not enforced this phase; equivalence tests (Phase 7) would surface a miscategorization.
- [Fake fetchers diverge from real behavior (e.g., real OCI pull applies chart deps that the fake local dir doesn't)] → The fake loads a real chart directory (including its `Chart.yaml`/deps), so rendering exercises the same Helm code path; only acquisition differs.
- [krusty `LoadRestrictionsNone` allows broad file access] → Required for overlay `resources:` referencing sibling dirs; acceptable because the resolved root is a pinned, trusted source. No user-controlled path traversal beyond the pinned URL.
- [Empty-OK parser could mask a genuinely misconfigured render (wrong mode values → 0 resources)] → Accepted trade-off; a downstream health/status check (Phase 6) is the right place to flag an unexpectedly empty render, not the parser.

## Migration Plan

Greenfield additive change — two new packages, new deps, no modification to existing CRD types, controller, or webhook. No runtime migration concern.

Deployment steps:
1. Add deps at pinned versions (`go get helm.sh/helm/v3@v3.21.3 sigs.k8s.io/kustomize/api@v0.21.1 sigs.k8s.io/kustomize/kyaml@v0.21.1`); defer `go mod tidy` until code imports them (they'd otherwise be pruned).
2. Add `internal/manifest`, then `internal/source` with fakes and real impls.
3. Add unit tests + testdata fixtures.
4. `make build`, `make test`, `make lint-fix` green.

Rollback:
- Revert the feature branch; the scaffold (Phase 0+1) is unaffected since nothing existing imports the new packages until Phase 6.

## Open Questions

- [x] ~~Exact go.mod versions for `helm.sh/helm/v3` and `sigs.k8s.io/kustomize/api`~~ — **Resolved**: pinned `helm.sh/helm/v3` v3.21.3 + `sigs.k8s.io/kustomize/api`/`kyaml` v0.21.1, verified to require exactly `k8s.io/*` v0.36.2 (probe `go get` + `go build ./...` clean, pins unchanged).
- [ ] `ChartLoader` cache lifetime (process LRU vs per-reconcile) — Phase 2 defines the interface + a minimal working impl; cache policy refined when the reconciler wires it in. — owner: implementer (Phase 6)
