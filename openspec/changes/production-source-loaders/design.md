<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Design: production-source-loaders (Phase 7)

## Context

The operator renders a `DualDeploymentOperator` source twice per reconcile (seed + shoot) through the [`internal/source`](../../../internal/source/source.go) package. Phase 2 built the Helm and kustomize renderers against two injected seams — `ChartLoader.Load(ctx, repo, name, version)` and `RootResolver.Resolve(ctx, url, subPath)` — but shipped **only test fakes** (a loader that reads a chart from a local dir; a resolver that points at a local overlay dir). Phase 6 wired the reconciler with a mocked source. Consequently [`cmd/main.go`](../../../cmd/main.go) still carries `SourceDeps: source.Deps{}` with a `TODO(production-loaders)`, and live rendering fails until real fetchers exist.

**Current state:**
- `helmSource.Render` calls `h.loader.Load(ctx, repo, name, version)` → `*chart.Chart`, then Helm dry-run install. Unchanged by this design.
- `kustomizeSource.Render` calls `k.resolver.Resolve(ctx, url, subPath)` → local path + cleanup, then `krusty.Run` with `LoadRestrictionsNone` (krusty fetches transitive remote bases itself). Unchanged by this design.
- `source.From(spec, deps)` already returns a clean error when a required loader is nil, so a missing production loader degrades gracefully (surfaced as an `InvalidSource`-style condition) rather than panicking.

**Constraints:**
- The operator runs per-shoot in a `shoot--cp--*` namespace on the seed; it is meant to be tiny (~200Mi). Heap-resident chart caching is undesirable.
- The container filesystem may be read-only; credential handling must not write to disk.
- `v1alpha1` is pre-release with no deployed consumers — breaking CRD changes are free.
- Offline `make test` must stay green on a laptop with no registry/git credentials.

**Stakeholders:** the 5 managed operators (metal-operator, boot-operator, argora, khalkeon via Helm/OCI; ipam-capi via kustomize/git); Phase 8 (equivalence tests need real renders); Phase 9 (deployment chart must stamp Gardener egress labels); Phase 7.5 (source caching, which builds on these loaders).

**Verified facts (grounding this design):**
- keppel `oci://keppel.global.cloud.sap/ccloud-helm/<op>-remote` serves charts **anonymously** (token payload `"kea":{"anon":true}`, `helm pull` with no login succeeds).
- ipam-capi's git sources (sapcc/helm-charts + transitive kubernetes-sigs / raw.githubusercontent.com) are **public**.
- seed→github egress works on qa **conditional on** the Gardener networking labels (`to-dns`, `to-public-networks`, `to-private-networks`); an unlabeled pod cannot even resolve DNS. Smoke-tested 2026-07 on two seed landscapes: `a-qa-de-200` / `shoot--cp--m-qa-de-200` and `rt-qa-de-1` / `shoot--cp--m-qa-de-1` (identical results).
- Helm v3.17 SDK: OCI auth via inline `registry.ClientOptBasicAuth` on a per-pull `registry.Client` (no `Login`, no disk write); `actionConfig.RegistryClient` must be **non-nil** even for anonymous (else nil-panic).
- go-git v5: git HTTPS auth via `http.BasicAuth{Username, Password}`; tag/branch ref → `CloneOptions.ReferenceName` + `Depth:1`; SHA ref → `PlainInit` + `FetchContext(RefSpec "<sha>:<tempbranch>", Depth:1)` + `Checkout{Hash}`; `Auth:nil` clones public repos.

## Goals / Non-Goals

**Goals:**
- Implement a production Helm `ChartLoader` that pulls charts from **OCI registries (`oci://`) and classic HTTP(S) Helm repositories** — both first-class, tested paths — supporting basic/token credentials. It pulls to a temp dir, loads the chart, and cleans up each call (no caching in this change — see Non-Goals).
- Implement a production kustomize `RootResolver` that fetches a pinned-`?ref=` git root via **go-git** (pure-Go, no `git` binary), fail-closed on unresolvable ref, supporting HTTPS basic/token credentials.
- Source credentials from a per-source `authSecretRef` on the CRD (`HelmSource`, `KustomizeSource`), resolved per host.
- Wire both loaders into `cmd/main.go`, removing the `TODO(production-loaders)` stub.
- Migrate `GetEventRecorderFor → GetEventRecorder` (deprecation cleanup), as a distinct task.
- Keep `make test` green offline; run real network + real auth tests behind `DDO_ONLINE_TESTS`.

**Non-Goals:**
- **Any source caching (deferred to Phase 7.5).** Both loaders fetch fresh each reconcile: the Helm loader pulls the chart tgz to a temp dir and cleans it up; the kustomize resolver re-fetches the root (and krusty re-fetches transitive bases). A unified, resolve-then-key, `emptyDir`-backed cache for BOTH loaders (Helm chart artifact keyed by resolved OCI digest; kustomize render keyed by resolved root SHA) is scoped as **Phase 7.5 "Source caching"**, immediately after this change. Deferred because it is a distinct feature with its own soundness design (ref→digest/SHA resolution, transitive-tag immutability), not part of "make the loaders render real sources end-to-end".
- Helm-repo TLS knobs (`CaFile` / `InsecureSkipTLSverify` for classic HTTP(S) repos) — deferred (no repo is known to need a custom CA; add with its own test if one does).
- SSH git authentication — deferred to a future change (no source uses it; avoids host-key-verification surface).
- Operator-global credential config — credentials are per-source via CRD `authSecretRef` only.
- Chart 1 deployment-chart changes (Gardener egress labels) — a **Phase 9** deliverable, documented (design §9.1, implementation.md Phase 9) but not built here. (The chart-cache `emptyDir` mount belongs to Phase 7.5, not Phase 9.)
- Changes to the renderer logic or the `Source`/`ChartLoader`/`RootResolver` interface signatures.
- Equivalence testing against today's chart output — that is Phase 8.

## Capabilities

**New capabilities:**
- `helm-chart-loader` — production `ChartLoader`: scheme dispatch (`oci://` → registry client; `http(s)://` → classic repo `RepoURL` pull), basic/token auth on both, pull-to-temp-dir + cleanup per call (no cache — deferred to Phase 7.5).
- `kustomize-root-resolver` — production git `RootResolver`: pinned-`?ref=` fetch via go-git (tag/branch and SHA paths), fail-closed, HTTPS auth.
- `source-credentials` — per-source `authSecretRef` credential resolution: `RegistryCredentials` (OCI inline basic-auth + classic HTTP(S) repo `Username`/`Password`) and `GitCredentials` (git HTTPS basic/token) reading a namespaced Secret, per-host, token-non-leaking.

**Modified capabilities:**
- `crd-types` — add optional `authSecretRef` to `HelmSource` and `KustomizeSource`; regenerate CRD/RBAC/deepcopy. Breaking `v1alpha1` (pre-release).
- `noop-reconciler` / manager wiring — `cmd/main.go` constructs and injects the production `source.Deps`; migrate the event recorder off the deprecated API. (Reconcile behavior unchanged; only construction + recorder type.)

## Decisions

**Decision: both loaders in one change**
- Chosen: implement Helm OCI loader + go-git kustomize resolver together.
- Reason: egress (the one blocker for the kustomize path) is verified resolved; Phase 8 equivalence needs real renders of all 5 operators, so splitting would block Phase 8 on a second cycle. The two loaders are independent behind separate interfaces.
- Alternatives: Helm-only now, defer kustomize (rejected — no remaining reason to split); see brainstorm Option B.

**Decision: go-git for the git root fetch**
- Chosen: `github.com/go-git/go-git/v5`.
- Reason: pure-Go (no `git` binary in the image), explicit `?ref=` pinning, fail-closed on unresolvable ref, credentials never touch a subprocess argv; matches Flux's source-controller.
- Alternatives: `hashicorp/go-getter` (can shell out to `git`, broader surface); reuse krusty's internal getter (not exposed as a reusable root-fetcher API).

**Decision: classic HTTP(S) Helm repos are a first-class, tested path**
- Chosen: `Load` dispatches on repo scheme — `oci://` → registry client + OCI pull; `http(s)://` → `action.Pull` with `RepoURL` + `Username`/`Password` (basic/token). Both pull a `.tgz` into a temp dir that is cleaned up after the chart is loaded.
- Reason: keeps the loader general to any chart source the `HelmSource` schema allows; the incremental cost over OCI-only is one extra `action.Pull` branch + a classic-repo test tier (local static `helm repo index` server, anonymous + authed).
- Alternatives: hard-error on non-`oci://` (rejected by user — would reject a legitimate chart source and force a second change if one appears); warn-and-try (rejected — untested path). TLS knobs (`CaFile`/`InsecureSkipTLSverify`) deferred (Non-Goals) — add with a test if a repo needs a custom CA.

**Decision: no source caching in this change (deferred to Phase 7.5)**
- Chosen: both loaders fetch fresh each reconcile. The Helm loader pulls the chart `.tgz` to a temp dir, `loader.Load`s it, returns the chart, and removes the temp dir; the kustomize resolver re-fetches the root each call (and krusty re-fetches transitive bases).
- Reason: reconcile is 10-minute-scale and ipam-capi is the only kustomize operator, so re-fetch cost is small; keeping caching out of this change keeps Phase 7 focused on "render real sources end-to-end" and lets the caching design (which has real soundness questions — ref→digest/SHA resolution, transitive-tag immutability, Helm-vs-kustomize symmetry) be brainstormed on its own.
- Deferred to **Phase 7.5 "Source caching"** (documented in `docs/implementation.md`, not built here): a unified, `emptyDir`-backed, **resolve-then-key** cache for BOTH loaders — Helm chart artifact keyed by resolved OCI digest, kustomize render keyed by resolved root SHA — so a mutable `?ref=`/tag is resolved to an immutable key each reconcile (auto-invalidates on change, no TTL).
- Alternatives considered and deferred with it: in-memory `*chart.Chart` LRU (rejected — every cached chart resident in heap, and its "full" mode is an uncatchable OOMKill vs. a disk cache's catchable `ENOSPC`); a disk LRU tgz cache keyed by `repo|name|version` string (rejected as the Phase-7 approach — the version string is not provably immutable, whereas Phase 7.5's resolved-digest key is).

**Decision: full authentication, real and tested (not stubs)**
- Chosen: build `RegistryCredentials` + `GitCredentials` as wired, tested paths.
- Reason: user directive — either build auth properly or omit it; no half-wired seam. A no-op stub is untestable dead code.
- Alternatives: anonymous-only with deferred seams (brainstorm Option C, rejected by user).

**Decision: credentials from a per-source CRD `authSecretRef` (not operator-global)**
- Chosen: optional `authSecretRef` on `HelmSource` and `KustomizeSource`, naming a Secret in the CR's namespace; single ref per source; fixed multi-key Secret (keys `username`/`password`/`token` — option 1A, no configurable `*Key` fields); resolver picks by what's present (`token` wins over `password` if both set).
- Reason: mirrors the existing `shootAccess.secretName` convention; keeps the CR as the single config surface (design §3.3); the operator's seed RBAC already reads Secrets.
- Alternatives: operator-global host→cred map (rejected — wastes per-CR granularity, doesn't fit the CR-is-config-surface principle, rotation needs a Pod restart).

**Decision: OCI auth inline, never `Login`**
- Chosen: `registry.NewClient(ClientOptEnableCache(true) [+ ClientOptBasicAuth(user,pass)])` per pull, assigned to `actionConfig.RegistryClient`; anonymous = same client without `ClientOptBasicAuth`.
- Reason: `Login` writes credentials to disk (fails on read-only FS) and has a hostname-vs-URL-scheme gotcha; inline creds avoid both. A non-nil `RegistryClient` is mandatory even anonymously (nil → SDK panic).
- Alternatives: `registryClient.Login(host, LoginOptBasicAuth(...))` (rejected — disk write, scheme gotcha).

**Decision: git HTTPS-only auth; SSH deferred**
- Chosen: `http.BasicAuth{Username, Password}` (PAT → Password, or user+pass); no SSH.
- Reason: no source uses SSH; SSH adds host-key-verification surface (`InsecureIgnoreHostKey` is a security smell to ship). Documented as a future change.
- Alternatives: include SSH now (rejected — YAGNI + security surface).

**Decision: git ref pinning fail-closed, tag/branch vs SHA**
- Chosen: tag/branch → shallow clone via `CloneOptions.ReferenceName` + `Depth:1`; SHA → `PlainInit` + `CreateRemote` + `FetchContext(RefSpec "<sha>:<tempbranch>", Depth:1)` + `Checkout{Hash}`. Error if the ref can't be resolved — never build `HEAD`.
- Reason: git servers don't allow cloning a bare SHA via `ReferenceName`; the fetch-by-refspec path is the documented go-git approach. Fail-closed prevents silently rendering the wrong revision.
- Alternatives: always full clone then checkout (rejected — heavier, still needs the fail-closed check).

**Decision: recorder migration as a distinct final task**
- Chosen: `GetEventRecorderFor → GetEventRecorder`, field type `record.EventRecorder → events.EventRecorder`, all call sites + test fake, drop `//nolint:staticcheck`; separate task + success criterion.
- Reason: in the Phase 7 plan, small, natural passenger on the `cmd/main.go` edit; kept isolated so it doesn't blur into loader work.
- Alternatives: separate change (rejected — re-touches `cmd/main.go` twice); drop entirely (rejected — leaves a known deprecation).

## Risks / Trade-offs

- [Breaking `v1alpha1` CRD change adding `authSecretRef`] → Pre-release, no deployed consumers; `authSecretRef` is optional (absent = anonymous), so existing sample CRs stay valid. Regenerate CRD/RBAC/deepcopy; add a CEL/type test.
- [Re-fetching every reconcile is wasteful] → accepted for this change: reconcile is 10-minute-scale, charts are small, and shallow git fetches are cheap; ipam-capi is the only kustomize operator. Caching is the explicit subject of Phase 7.5, deferred deliberately.
- [Live rendering fails without the Phase 9 egress labels] → Documented as an explicit Phase 9 chart requirement (design §9.1, implementation.md Phase 9); egress labels are a chart, not code, gap.
- [Credential material leaks into logs/errors] → Never log tokens; a dedicated test asserts the auth-failure error string is token-free.
- [go-git SHA fetch-by-refspec unsupported by some git servers] → github supports it (the only host in scope); fail-closed surfaces a clear error rather than a wrong render; documented per-landscape re-verify.
- [Online tests flaky/credential-dependent in CI] → Gated behind `DDO_ONLINE_TESTS`; offline tier (fakes + local git server) is the default and always green.

## Migration Plan

Deployment steps:
1. Land this change (loaders + `authSecretRef` + wiring + recorder migration); `make manifests generate` regenerates CRD/RBAC/deepcopy.
2. Existing sample/fixture CRs need no edit (`authSecretRef` optional; all current sources anonymous).
3. Phase 8 consumes the real loaders for equivalence tests.
4. Phase 9 chart stamps the three Gardener egress labels on the operator Pod (documented cross-ref). Phase 7.5 adds source caching on top of these loaders.

Rollback:
- Revert the change; `source.From` returns to failing cleanly with the fakes-only build. No data migration (CRD `authSecretRef` is additive/optional; no CR sets it yet).

## Open Questions

_Both resolved during design (2026-07); recorded here and folded into the Decisions above._

- [x] **`authSecretRef` Secret key names → fixed conventional keys (option 1A).** The resolver reads a fixed key set: `username` + `password` (basic auth: OCI, classic HTTP(S) repo, git HTTPS) and `token` (used as password — git PAT with username `"git"`; bearer-style for OCI/HTTP). No configurable `*Key` fields (unlike `shootAccess`'s `TokenKey`/`CAKey`) — all current sources are anonymous, so no legacy Secret exists to be incompatible with; adding optional `*Key` fields later is backward-compatible if ever needed. Matches the widespread `kubernetes.io/basic-auth` `username`/`password` convention. If both `token` and `password` are present, `token` wins (a spec detail to encode as a scenario).
- [x] **Chart caching → deferred entirely to Phase 7.5.** Originally scoped as a disk-backed LRU tgz cache in this change; removed after review (user decision) because the sound, useful design (resolve-then-key against an immutable OCI digest / git SHA, `emptyDir`-backed, symmetric across both loaders) is a distinct feature with its own soundness questions, not part of "render real sources end-to-end". Phase 7 fetches fresh each reconcile; Phase 7.5 owns caching.
