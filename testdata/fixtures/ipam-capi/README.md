# ipam-capi equivalence fixture

This is the fifth equivalence fixture in the suite. It proves that the operator's
full `KustomizeSource` render pipeline produces the same objects as a direct
krusty build of the same upstream-anchored overlay.

## Upstream source / golden

Golden side: `RenderGoldenKustomize` of
`system/kustomize/ipam-capi-remote/managedresources` at the pinned SHA
`33e68278ace26fe23e0c2b36a9266a58c9b47707` in `sapcc/helm-charts`.

This overlay is the upstream-anchored reference for ipam-capi. It contains **zero
SAP-specific content** — it is a pure aggregation kustomization pointing directly at
the upstream project:

| Upstream source | Content |
|----------------|---------|
| `github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster//config/crd?ref=v1.1.0` | Four pool CRDs with conversion webhooks and clusterctl labels |
| `github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster//config/rbac?ref=v1.1.0` | Leader-election, manager, and auth-proxy RBAC |
| `raw.githubusercontent.com/kubernetes-sigs/cluster-api/v1.13.4/config/crd/bases/cluster.x-k8s.io_clusters.yaml` | CAPI Cluster CRD |
| `raw.githubusercontent.com/.../ipam.cluster.x-k8s.io_ipaddresses.yaml` | CAPI IPAddress CRD |
| `raw.githubusercontent.com/.../ipam.cluster.x-k8s.io_ipaddressclaims.yaml` | CAPI IPAddressClaim CRD |

The aggregation applies `namePrefix: ipam-capi-` and `namespace: kube-system` — the only
SAP-supplied transforms. There are no SAP-authored resource templates, additions, or patches.

The upstream project is **`kubernetes-sigs/cluster-api-ipam-provider-in-cluster`** (CAPI
in-cluster IPAM provider), pinned at release `v1.1.0`
(tag SHA `bc27dd724e20a0543f441d55183778a73db26294`). The three CAPI CRD bases come from
`kubernetes-sigs/cluster-api` at `v1.13.4`.

### Why not render the upstream project directly

`config/crd` and `config/rbac` are two separate kustomize roots and cannot be rendered as
a single golden on their own. The `managedresources/` aggregation kustomization is the
correct upstream-anchored golden: it references both roots unfiltered (including the
`auth_proxy_*` RBAC components from upstream `config/rbac`, which the operator does
deliver — `comparedKinds` in `fixture.yaml` is not an exclusion filter, it only scopes
which kinds this fixture asserts on) with identical namespace/prefix settings — effectively
a direct render of the upstream project's deliverable.

## Why this is not self-comparison

Golden (`RenderGoldenKustomize`) and operator side (`CaptureShootOnly`) both invoke krusty
on the same overlay, but through entirely different code paths:

- **Golden**: `git clone` → `checkout SHA` → `krusty.Run` directly
- **Operator**: `gitResolver.Resolve` → `preflightSHAGate` → `krusty.Run` via `kustomizeSource.Render`

The comparison catches regressions in the operator's preflight, resolver, or render pipeline
that cause it to diverge from a direct krusty output — for example a bug in
`preflightSHAGate` or in namespace application. Note this is a successful-render test only:
it exercises the happy path through `sanitizeKrustyError`'s call site but cannot validate
sanitization behavior itself (that is covered separately by Task 3's unit tests).

## Deliberate deviation: shoot-only capture

`CaptureShootOnly` is used instead of `CaptureOperator`. The `seed/manager` overlay at this
SHA uses repo-root-relative patch paths that fail when `KustomizeSource` builds from the
overlay subdir. Seed is skipped entirely.

See **`docs/implementation.md`** → *"`ipam-capi` equivalence is DEFERRED to a standalone
future follow-up"* for the complete deferral rationale. The `managedresources/` (shoot) overlay
is self-contained and renders today; that is what this fixture covers.

## Network requirements

Gated on `RUN_KUSTOMIZE_EQUIVALENCE=1` (or `RUN_EQUIVALENCE=1`, which is a
superset). Both sides require outbound HTTPS network access to `github.com` and
`raw.githubusercontent.com` to fetch the upstream krusty remote bases. All sources
are public/anonymous — no OCI registry credentials needed (unlike the four Helm
fixtures which additionally require the internal keppel OCI registry for
`helm dependency build`).

The test is **not** in the default `Test` CI job because live git clones and
krusty remote-base fetches carry a flakiness risk (rate limits, transient network
failures from CI runners to github.com) that is unsuitable for the standard
unit-test run. Enable it in a dedicated network-access CI job by setting
`RUN_KUSTOMIZE_EQUIVALENCE=1`. Public GitHub Actions runners do have access to
`github.com`, but the risk of transient failures in high-concurrency runs is real.

> **TODO (follow-up):** add an in-image equivalence step — build the operator
> image and exercise the ipam-capi remote-base render from inside the container —
> so the `git` binary that ships in the image is also exercised end-to-end, not
> only via `go test`. The Dockerfile comment for the krusty `git` binary tracks
> this.

## Files

| File | Purpose |
|------|---------|
| `fixture.yaml` | Pinned SHA, kustomize overlay path, scope, divergences |
| `cr.yaml` | `DualDeploymentOperator` CR with KustomizeSource, `shootPath: managedresources` |
| `README.md` | This file |
