## Context

The operator's implementation (Phases 0–8) is complete: kubebuilder scaffold, `v1alpha1` CRD types, source rendering, transformations, dual-cluster SSA delivery, reconciler, production source loaders, source caching, and equivalence tests. What does **not** yet exist is a packaged deployment artifact — there is no `chart/` and no `dist/` in the repo.

Today the repo ships a standard kubebuilder **kustomize scaffold** under `config/*` (CRD in `config/crd/`, RBAC in `config/rbac/`, manager in `config/manager/`, plus `config/webhook`, `config/network-policy`, `config/certmanager`, `config/prometheus`, `config/default`, `config/samples`). This scaffold powers dev/CI tooling (`make deploy`, `make run`, envtest). Verified in a good state: `config/default` and `config/crd` build cleanly, and `make manifests generate` produces zero drift (codegen in sync).

Per design.md §9.7, the operator ships as **two charts in two repos**, mirroring the fleet's upstream-chart + wrapper-chart pattern (`ironcore-dev/metal-operator` publishes its own chart; `sapcc/helm-charts`'s `metal-operator-remote` wraps it):
- **Chart 1** — `dual-deployment-operator`, the upstream controller chart, lives in THIS repo at `chart/`, published as an OCI chart. **This change delivers chart 1 only.**
- **Chart 2** — `dual-deployment-operator-remote`, the wrapper + CR instances + sapcc/Gardener glue, lives in `sapcc/helm-charts`. Out of scope here.

**Constraints:** `chart/` must be generated from `config/*` via the kubebuilder helm plugin, not hand-written (the two are complementary, not competing). The chart must be registry-agnostic — no keppel host anywhere in this repo — because the downstream wrapper (chart 2) overrides the image to the keppel mirror when it consumes chart 1 as a subchart. The operator runs per-shoot in a `shoot--cp--*` namespace on the seed under a Gardener deny-all NetworkPolicy, so the pod template must carry the Gardener egress labels. Leader election is required even at `replicas: 1` (rolling updates transiently run two pods).

**Stakeholders:** operator maintainers (this repo), the `sapcc/helm-charts` chart-2 authors who depend on chart 1's image/values contract, and the per-shoot deploy pipeline (Concourse `helm-chart-pipeline` / Flux).

## Goals / Non-Goals

**Goals:**
- Generate `chart/` at repo root from `config/*` via `kubebuilder edit --plugins=helm/v2-alpha --output-dir=.`, regenerable with `--force`.
- Ship chart 1 as a self-contained, **keppel-free** upstream artifact: controller Deployment, ServiceAccount, broad seed-side RBAC (ClusterRole + ClusterRoleBinding), the `DualDeploymentOperator` CRD in `crds/`, leader-election Role, NetworkPolicy, and metrics/probes.
- Neutral image default `ghcr.io/SAP-cloud-infrastructure/dual-deployment-operator` with `tag` defaulting to `.Chart.AppVersion` (kubebuilder `manager.image.repository`+`tag` shape), so a standalone `helm install` is pullable and chart 2 can override to the keppel mirror.
- Stamp Gardener egress pod labels (`networking.gardener.cloud/{to-dns,to-public-networks,to-private-networks}: allowed`) on the manager pod template.
- Confirm `--leader-elect=true`, `LeaderElectionReleaseOnCancel: true`, liveness/readiness probes, and metrics port are wired and surfaced in chart 1's `deployment.yaml`.
- `helm lint chart/` and `helm template chart/` succeed; `make manifests generate` stays drift-free; build/lint/test green.

**In scope (added after the ownership decision below):**
- The shoot-applier RBAC bootstrap `ManagedResource` and the shoot token-requestor `Secret` — provisioned by chart 1 under `chart/templates/shoot-rbac/`, gated `shootRbac.enabled` (default off). See "Decision: Shoot-RBAC bootstrap + token-requestor Secret ownership" below.

**Non-Goals:**
- Chart 2 (`dual-deployment-operator-remote`) itself — lives in `sapcc/helm-charts` (consumes chart 1 as a subchart and sets the `shootRbac.*` values + CR instances).
- Workload-specific shoot RBAC (e.g. the webhook-injector ServiceAccount subject) — stays in the per-workload chart, not chart 1.
- Any keppel registry reference in this repo's chart.
- The GHCR image build/publish workflow (Phase 9.5 — separate change).
- Wiring the validating admission webhook (Phase 10 — scaffold only, deferred to v2).
- The chart-**cache** `emptyDir` volume (Phase 7.5 deliverable, already separate). NOTE: a separate writable **source-scratch** `emptyDir` IS in scope and required — see Decisions — because `readOnlyRootFilesystem: true` otherwise breaks the loader's per-render temp dir.

## Decisions

**Decision: Chart generation method**
- Chosen: kubebuilder helm plugin `helm/v2-alpha` with `--output-dir=.` (writes to repo-root `chart/`).
- Reason: design.md §9.7 / implementation.md Phase 9 mandate that `chart/` is generated from `config/*`, keeping a single source of truth and matching how upstream `ironcore-dev/metal-operator` ships its chart. Regenerable with `--force`.
- Alternatives considered: hand-authored `chart/` — rejected (diverges from the generation rule, cannot regenerate cleanly, drifts from `config/*`).

**Decision: Scope = chart 1 only**
- Chosen: deliver only the controller chart in this repo (chart 2 itself stays in `sapcc/helm-charts`).
- Reason: chart 2 belongs to `sapcc/helm-charts` per the fleet's upstream/wrapper ownership boundary; user scoped this change to chart 1.
- Alternatives considered: bundling chart 2 documentation or the Phase 9.5 publish workflow — rejected (out of scope; separate review surfaces).

**Decision: Shoot-RBAC bootstrap + token-requestor Secret ownership** (revised mid-implementation)
- Chosen: the shoot-applier RBAC bootstrap (`ManagedResource` → the shoot `ServiceAccount` + a broad apply-scoped shoot `ClusterRole`+binding for it, delivered as one atomic unit) AND the Gardener token-requestor `Secret` are provisioned by **chart 1** under `chart/templates/shoot-rbac/`, gated `shootRbac.enabled` (default `false`), parameterized by `shootRbac.{serviceAccountName,serviceAccountNamespace,secretName,namespace}`. The bootstrap creates the ServiceAccount itself (design.md:942) — not any per-workload render — because the token-requestor Secret mints a token for it, the binding binds to it, and the operator cannot self-bootstrap its own identity. The applier SA defaults to a **dedicated, operator-owned identity** `dual-deployment-operator-shoot-applier` (NOT the workload's SA), and the token-requestor Secret defaults to the workload-agnostic name `dual-deployment-operator-shoot-access`.
- Reason: both are operator install-time plumbing — they exist *because the operator is present*, are identical for every managed workload, and are keyed only by the shoot ServiceAccount. Housing them in the operator install chart (rather than each per-workload chart) removes duplication across the fleet, keeps the credential name uniform, and lets the operator only ever *read* the Secret. This reverses the original plan, which placed both in the downstream `sapcc/helm-charts` workload chart (`-remote`). The corresponding templates were deleted from the workload chart (`metal-operator-remote-v2`) in `sapcc/helm-charts`, and its controller-manager volume now references the Secret name via a configurable value (no hard link). Workload-specific shoot RBAC (webhook-injector SA subject) stays in the workload chart. **Dedicated applier SA (identity separation):** the operator's applier SA MUST be distinct from any workload SA. When it was pointed at the workload's own SA (`metal-operator-controller-manager`, part of the workload shoot render), CR deletion tore that SA down mid-finalizer, invalidating the operator's token (401) and deadlocking the finalizer with shoot resources orphaned (self-deauthentication). A dedicated operator-owned SA is never in a workload teardown set, so the operator keeps valid credentials through cleanup; it also shrinks blast radius (the workload SA no longer carries the broad `bind`/`escalate` applier grant). Correspondingly, the workload keeps its OWN token-requestor Secret (`metal-operator-remote-kubeconfig`, minted for `metal-operator-controller-manager`) for the controller-manager's runtime shoot access — the two identities and two Secrets are deliberately decoupled (design.md §3.6.7, §3.7).
- Alternatives considered: keep both in the workload chart (original scope) — rejected because it duplicates identical bootstrap/Secret logic per operator and couples the credential name to a workload; require an explicit (empty + `required`) SA/Secret name — rejected in favor of a dedicated/generic default that is still overridable; reuse the workload SA as the applier identity — rejected (causes the finalizer deadlock above).
- Implemented by branch commits `7bb8fb9` (bootstrap MR), `9c09682` (token-requestor Secret), `906d936` (generic Secret name), `c41aa6c` (bootstrap creates the SA), `1274dd8` (dedicated applier-SA default). Paired `sapcc/helm-charts` `-v2` commit `871dd0c` restores the workload's own token-requestor Secret and decouples the controller-manager mount from the operator applier Secret.

**Decision: Registry-agnostic image, neutral ghcr.io default**
- Chosen: `manager.image.repository: ghcr.io/SAP-cloud-infrastructure/dual-deployment-operator`, `manager.image.tag` defaults to `.Chart.AppVersion`; no keppel host in this repo.
- Reason: chart 1 is a reusable upstream artifact (like upstream `metal-operator`'s chart, which ships a neutral image and lets the consumer set the registry). The ghcr.io path is the real Phase-9.5 publish target, so a standalone `helm install` is pullable without an override. Chart 2 overrides `repository`/`tag` to the keppel mirror (`keppel.global.cloud.sap/ccloud-ghcr-io-mirror/SAP-cloud-infrastructure/dual-deployment-operator`) when consuming chart 1 as a subchart — verified fleet convention (metal-operator-remote, webhook-injector).
- Alternatives considered: keppel-mirror path baked into chart 1 — rejected (couples the upstream chart to a specific registry; the user requires keppel be overridden by chart 2, not embedded here). Bare kubebuilder default `repository: controller` — rejected in favor of a real pullable ghcr.io path.

**Decision: Broad seed applier ClusterRole/ClusterRoleBinding**
- Chosen: broad applier grant via `config/rbac` markers — namespaced + cluster-scoped seed-render kinds, `DualDeploymentOperator` CR watch, Secret get/list (shoot token-requestor), Leases.
- Reason: the seed render is not namespace-local — candidate charts (metal-operator, ipam-capi) emit cluster-scoped seed resources, and patch/rewrite transforms can produce ClusterRoles; a narrow namespaced Role would fail on the first real reconcile. Aligns to gardener-resource-manager / Flux (broad applier, tenants scoped by SA impersonation).
- Alternatives considered: narrow namespaced Role first — rejected (docs already prove cluster-scoped seed resources exist).

**Decision: deployment.yaml post-generation customizations**
- Chosen: Gardener egress pod labels; `--leader-elect=true` + `LeaderElectionReleaseOnCancel: true`; liveness/readiness probes + metrics port confirmed; `replicas: 1`; a writable `source-scratch` `emptyDir` (mounted at `--source-scratch-dir`). No chart-**cache** `emptyDir`.
- Reason: these are the gaps the plain kubebuilder scaffold leaves — egress labels are a verified networking prerequisite; leader election is required for safe rolling updates; probes prevent shipping a probe-less Deployment; the source-scratch volume is required because `readOnlyRootFilesystem: true` breaks the loader's per-render `os.MkdirTemp` without a writable mount. The chart-**cache** `emptyDir` (distinct) belongs to Phase 7.5.
- Alternatives considered: relying on the plugin's raw output — rejected (no egress labels, no confirmed probes → non-functional on the seed).

## Risks / Trade-offs

- [`kubebuilder edit --force` regenerates the chart] → The helm plugin's write scope is confined to its pinned `output:` dir (`chart/`); it does **not** touch `cmd/`, `internal/`, or `api/`. Verified against the fleet precedent `ironcore-dev/metal-operator` (kubebuilder v4.15.0, `helm.kubebuilder.io/v2-alpha` with `output: dist`, 19 kinds + webhooks + hand-maintained `cmd/main.go` — all coexisting; the plugin only owns `dist/chart/**`). So `cmd/main.go` (production loaders wiring + event recorder) is NOT at risk from chart regeneration. The residual `--force` risk is only **within `chart/`**: hand-applied post-gen customizations (egress labels, probes, leader-election flags) are overwritten and must be re-applied.
- [Post-generation customizations (egress labels, probes, leader-election flags) are lost on the next `--force` regen] → Document the customization set in the plan; treat "regenerate chart" as "regenerate then re-apply the documented customizations", and verify via `helm template` assertions.
- [Neutral ghcr.io image default is not yet pullable until Phase 9.5 publishes it] → Acceptable: chart 1 standalone install is a dev/test convenience; production always goes through chart 2 which overrides the ref. The default documents intent and gives a real target once 9.5 lands.
- [Chart drifts from `config/*` if `config/*` changes but the chart is not regenerated] → CI/`make` step to regenerate and assert no drift (same discipline as `make manifests generate`).

## Migration Plan

Greenfield addition (new `chart/` directory; no existing chart to migrate). Deployment/consumption steps:

1. Generate `chart/` via the kubebuilder helm plugin (`--output-dir=.`).
2. Apply post-generation customizations (image default, egress labels, leader-election, probes, broad RBAC via markers + `make manifests`).
3. `helm lint chart/` and `helm template chart/` succeed; `make manifests generate` drift-free; build/lint/test green.
4. Chart 1 is published as an OCI chart by the release process (Phase 9.5 provides the image; publishing the chart itself is part of the release unit). Chart 2 in `sapcc/helm-charts` later declares chart 1 as a dependency and overrides the image to the keppel mirror.

Rollback:
- Chart 1 is additive; removing/reverting the `chart/` directory restores the prior state with no runtime impact (nothing consumes it until chart 2 exists downstream).

## Open Questions

- [x] Chart 1 image reference — RESOLVED: neutral `ghcr.io/SAP-cloud-infrastructure/dual-deployment-operator`, keppel-free; chart 2 overrides to the keppel mirror. — owner: user (decided)
- [x] `--force` regeneration clobbering hand-written source — RESOLVED: the helm plugin writes only under its pinned `output:` dir (`chart/`), never `cmd/`/`internal/`/`api/`. Confirmed against `ironcore-dev/metal-operator` (same kubebuilder v4.15.0 + helm/v2-alpha, hand-maintained `cmd/main.go` coexists). Only the chart's own post-gen customizations are at `--force` risk; document them so they are re-applied on regen. (webhook-injector is NOT a precedent here — it has no chart/ and `controllerGen: enabled: false`; it uses plain go-makefile-maker, relevant only to the Phase 9.5 GHCR image alignment.) — owner: implementer (verify during apply)
