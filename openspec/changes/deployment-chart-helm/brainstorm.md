## Design Summary

Build **chart 1** — the `dual-deployment-operator` controller Helm chart — at repo-root `chart/`, generated from the existing `config/*` kustomize scaffold via the kubebuilder helm plugin (`helm/v2-alpha`), and publishable as an OCI chart. Chart 1 is the fleet's **upstream controller artifact** (controller Deployment, ServiceAccount, seed-side RBAC, the `DualDeploymentOperator` CRD in `crds/`, leader-election Role, NetworkPolicy, probes/metrics) — self-contained and **registry-agnostic**: it ships a neutral `ghcr.io/...` image default and never references keppel. The wrapper chart 2 (`dual-deployment-operator-remote`, in `sapcc/helm-charts`) consumes chart 1 as a subchart and overrides the image to the keppel mirror; chart 2 and all sapcc/Gardener glue are out of scope here. This realizes Phase 9 (design.md §9.7, implementation.md Phase 9) for the operator repo only.

## Alternatives Considered

### Option A: kubebuilder helm plugin (chosen)
- **Approach**: Run `kubebuilder edit --plugins=helm/v2-alpha --output-dir=.` to generate `chart/` from `config/*`, then apply targeted post-generation customizations (Gardener egress labels, leader-election flag, probes, neutral image ref). Regenerable with `--force`.
- **Pros**: Matches the documented "chart is generated from `config/*`, not hand-written" rule (implementation.md Phase 9); `config/*` stays the single source of truth; regenerable; mirrors how upstream `ironcore-dev/metal-operator` ships its own chart.
- **Cons**: `--force` regeneration can clobber hand-edited files (incl. possibly `cmd/main.go`); post-gen customizations must be re-applied after each regen.
- **Why not chosen**: it IS chosen.

### Option B: Hand-authored chart/
- **Approach**: Write `chart/` templates by hand as plain Helm.
- **Pros**: Precise control; no plugin quirks.
- **Cons**: Diverges from the documented generation rule; cannot regenerate cleanly; drifts from `config/*` as markers/manifests change.
- **Why not chosen**: violates the "generated from config/*" invariant; creates a second, hand-maintained source of truth that will drift.

### Option C: Bundle chart 1 with Phase 9.5 image publish
- **Approach**: Deliver chart 1 plus the GHCR publish workflow (go-makefile-maker `pushContainerToGhcr`) in one change, since "image + chart 1 + chart 2 are one release unit".
- **Pros**: Ships a runnable release unit; chart 1's image ref becomes real immediately.
- **Cons**: Larger scope; couples chart-structure work to CI-workflow work with different review surfaces.
- **Why not chosen**: user scoped this change to chart 1 only. Phase 9.5 is a separate change.

## Agreed Approach

**Option A — kubebuilder helm plugin.** Generate `chart/` from `config/*` via `helm/v2-alpha --output-dir=.`, then apply the minimal post-generation customizations the plain scaffold leaves out. Chart 1 is a clean, reusable upstream artifact exactly like `ironcore-dev/metal-operator`'s own chart: it carries the controller + RBAC + CRD only, ships a neutral (keppel-free) image default, and is overridden by the downstream wrapper (chart 2). This keeps the ownership boundary the fleet already uses (upstream chart in the operator repo; sapcc/Gardener wrapper in `sapcc/helm-charts`) and preserves `config/*` as the single source of truth.

## Key Decisions

- **Scope = chart 1 only**: controller chart in THIS repo. Chart 2 (`dual-deployment-operator-remote`), the `shoot-rbac-bootstrap` ManagedResource, and the `remote-access` token-requestor Secret live in `sapcc/helm-charts` and are explicitly out of scope (implementation.md "Notes on chart-side changes (out of this repo's scope)").
- **Generation method = kubebuilder helm/v2-alpha plugin, `--output-dir=.`**: `chart/` is generated from `config/*`, never hand-authored. Re-run with `--force` after marker/manifest changes; `config/*` and `chart/` are complementary (dev-CI kustomize vs. published Helm), not competing.
- **Chart 1 is registry-agnostic (keppel-free)**: no keppel host appears anywhere in this repo's chart. This matches the upstream-chart convention (upstream `metal-operator`'s chart ships a neutral image; the `-remote` wrapper overrides it).
- **Image reference**: kubebuilder default YAML shape `manager.image.repository` + `manager.image.tag`. Default `repository: ghcr.io/SAP-cloud-infrastructure/dual-deployment-operator` (the real Phase-9.5 publish target — a pullable location so a standalone `helm install` works without an override); `tag` defaults to `.Chart.AppVersion`. Chart 2 overrides both to the keppel mirror (`keppel.global.cloud.sap/ccloud-ghcr-io-mirror/SAP-cloud-infrastructure/dual-deployment-operator`) when it depends on chart 1 as a subchart — verified as the fleet convention (metal-operator-remote, webhook-injector).
- **Seed RBAC = broad applier ClusterRole/ClusterRoleBinding**: via `config/rbac` markers. The seed render is not namespace-local (candidate charts emit cluster-scoped seed resources; patch/rewrite transforms can produce ClusterRoles). Grant covers namespaced + cluster-scoped seed-render kinds, `DualDeploymentOperator` CR watch, Secret get/list (shoot token-requestor), and Leases (leader election). Aligns to gardener-resource-manager / Flux (broad applier, tenants scoped by SA impersonation, not by narrowing the applier).
- **deployment.yaml post-gen customizations** (gaps the plain scaffold leaves):
  - Gardener egress pod labels `networking.gardener.cloud/{to-dns,to-public-networks,to-private-networks}: allowed` — REQUIRED (verified prerequisite for keppel/github fetches on the seed's deny-all NetworkPolicy).
  - `--leader-elect=true`; set `LeaderElectionReleaseOnCancel: true` in `cmd/main.go`.
  - liveness (`/healthz`) + readiness (`/readyz`) probes and metrics port confirmed wired (verify, don't rebuild — kubebuilder scaffolds from `config/`).
  - `replicas: 1` default.
- **No chart-cache `emptyDir`**: that volume is a Phase 7.5 deliverable (already complete/separate), not Phase 9.
- **CRD placement = `chart/crds/`**: Helm installs once, untemplated; the plugin handles placement.

## Open Questions

- [x] Chart 1 image reference — RESOLVED: neutral `ghcr.io/SAP-cloud-infrastructure/dual-deployment-operator`, keppel-free; chart 2 overrides. — owner: user (decided)
- [x] `--force` regeneration clobbering hand-written source — RESOLVED: the kubebuilder helm plugin writes only under its pinned `output:` dir (`chart/`), never `cmd/`/`internal/`/`api/`. Verified against the fleet precedent `ironcore-dev/metal-operator` (kubebuilder v4.15.0, `helm.kubebuilder.io/v2-alpha`, `output: dist`, 19 kinds + webhooks + a hand-maintained `cmd/main.go` all coexisting). So `cmd/main.go` is not at risk; only the chart's own post-gen customizations (egress labels, probes, leader-election flags) are overwritten on `--force` and must be re-applied. webhook-injector is NOT a precedent for this (no chart/, `controllerGen: enabled: false`, plain go-makefile-maker) — it informs only the Phase 9.5 GHCR image alignment. — owner: implementer (verify during apply)
