# dual-deployment-operator — Design Document

**Status:** Proposal
**Tracking issue:** [cc/unified-kubernetes#1294](https://github.wdf.sap.corp/cc/unified-kubernetes/issues/1294)
**Predecessor POC:** [sapcc/helm-charts#11633](https://github.com/sapcc/helm-charts/pull/11633) (kustomize-based, archived)
**Original problem:** [cc/unified-kubernetes#1169](https://github.wdf.sap.corp/cc/unified-kubernetes/issues/1169)

---

## TL;DR

A kubebuilder operator (`dual-deployment-operator`) that pulls **one artifact per operator** (Helm chart or kustomize source) from `sapcc/helm-charts`, **renders it twice with mode-specific configuration** (once for host, once for remote), applies typed Go transformations to each render independently, and **applies each render directly to its target Kubernetes API** — host resources to the seed via the operator's own service account, remote resources to the shoot via a Gardener-provided kubeconfig.

Key characteristics:

- **Two-render, no split.** Chart/kustomization determines what belongs to host vs remote via mode-specific configuration (Helm: `hostValues`/`remoteValues`; kustomize: `hostPath`/`remotePath` selecting overlay directories). Each render produces only the resources for its target cluster. Operator does not decide routing; the source decides via what it emits per mode.
- **One artifact per operator, self-contained.** No wrapper + additions split.
- **Operator supports both Helm and kustomize** via a CR source discriminator (`spec.source.helm` xor `spec.source.kustomize`).
- **Small transformation menu:** 4 types — `patch` (DSL: strategic-merge or JSON Patch), `rewriteWebhookURL`, `filterKinds`, `packageWebhookConfigsForInjector` (cross-stream). No general-purpose DSL, no scripting. `patch` replaces the historical `injectInitContainer` and `addLabels` typed types for patch-shaped operations. `renameKind` is not needed (was an artifact of ManagedResource-based delivery).
- **Dual-cluster direct apply** — operator holds two Kubernetes clients per CR, applies host + remote uniformly with server-side apply + drift correction + per-resource health tracking. No `ManagedResource` wrapping.
- **webhook-injector retained, unchanged.** It keeps its existing contract: read WebhookConfigurations from a source ConfigMap (`--webhook-config-name`), inject the current caBundle, and apply them to the shoot; plus TLS cert generation and rotation. The operator produces the source ConfigMap (via `packageWebhookConfigsForInjector`) but does NOT deliver WebhookConfigurations, does NOT stamp caBundle anywhere, and does NOT co-own any injector-managed field. The operator and injector write disjoint resources on the shoot — no shared-resource field ownership is required.
- **Zero `make build-` targets remain.** All pre-rendered YAML deleted.
- **Estimated size:** ~1000-1300 lines of Go, shared across all five current remote operators.
- **Deployment topology**: per-shoot (one operator instance per shoot-cp namespace), matching the webhook-injector's existing per-shoot footprint.

---

## 1. Background

### 1.1 The deployment topology

Five kubebuilder operators are deployed today in a split host/remote topology:

- **`metal-operator`** — provisions bare-metal servers (`metal.ironcore.dev`)
- **`boot-operator`** — manages iPXE boot config
- **`argora-operator`** — SAP-CC integration for NetBox
- **`khalkeon`** — CobaltCore inventory sync
- **`ipam-capi`** — Cluster API in-cluster IPAM provider

For each:

- The operator's Pod (`controller-manager` Deployment) runs in the **seed (host) cluster**, in a per-shoot control-plane namespace such as `shoot--cp--m-eu-de-1`.
- The operator's CRDs, ClusterRoles, ClusterRoleBindings, ServiceAccount, and webhook configurations live in a separate **virtual (remote) cluster** — a workerless Kubernetes API server with no Pods, where the operator's domain objects are served.
- The operator authenticates to the virtual cluster's API server via a kubeconfig (token-requestor pattern with Gardener-rotated tokens) and reconciles domain objects there.
- Webhook callbacks from the virtual cluster's API server reach the operator Pod in the seed via URL-based `clientConfig` (not service-based — the virtual cluster has no Pods to route a Service to).

### 1.2 Current implementation

For each of the five operators there is a wrapper Helm chart under `system/<operator>-remote/` (or for ipam-capi a kustomize source under `system/kustomize/ipam-capi-remote/` that helmifies into `system/ipam-capi-remote/`). Each wrapper:

1. Depends on the upstream operator's Helm chart (or kustomize source for ipam-capi)
2. **Disables** the upstream's `controllerManager`, `rbac`, `crd`, `webhook` at install time (`values.yaml`)
3. Pre-renders the upstream at **chart-source time** via `make build-<operator>-remote` — a Makefile target that runs `helm template` (or `kubectl kustomize | helmify`) piped through `yq`/`sed` for transformations:
   - Filter unwanted kinds (Service, sometimes ConfigMap/Secret)
   - Rename `Role` → `ClusterRole` (upstream emits namespace-scoped Roles; we deploy to a shared namespace and need cluster-wide grants)
   - Rewrite Validating/MutatingWebhookConfiguration `clientConfig.service` → `.url` (virtual cluster can't route to seed Services)
   - Label the CRDs+RBAC `ManagedResource` (metal-operator only) with the webhook-injector's `--managed-resource-label` so the injector's `ManagedResourceReconciler` finds it, walks its embedded CRDs, and stamps caBundle into those with a conversion webhook
   - Inject the webhook-injector sidecar as an `initContainer` into upstream's Deployment (metal + ipam-capi)
4. Commits the transformed YAML into the chart: `managedresources/*.yaml`, `webhooks.yaml`, `templates/controller-manager.yaml`
5. Delivers remote content via **three separate paths**:
   - **CRDs, RBAC, ServiceAccount**: `templates/managedresource.yaml` (chart Helm template) wraps each pre-baked YAML doc as a `ManagedResource + Secret` pair. Gardener's `gardener-resource-manager` (GRM) in each shoot's control plane picks up the MR and applies to the shoot.
   - **WebhookConfigurations**: `webhooks.yaml` (pre-rendered with URL rewrite) is packed into a `webhook-config` ConfigMap on the host via `.Files.Get`. The **webhook-injector sidecar** (mounted alongside each operator Pod) reads this ConfigMap and applies the WebhookConfigurations to the shoot cluster via a mounted shoot kubeconfig.
   - **caBundle rotation**: webhook-injector also generates and rotates TLS certs, storing them in a seed-side Secret. On rotation it (a) re-applies the shoot's WebhookConfigurations with a fresh caBundle (via the source-ConfigMap path above), and (b) stamps `.spec.conversion.webhook.clientConfig.caBundle` into CRDs embedded in the labelled seed-side `ManagedResource` (via its `ManagedResourceReconciler`, selected by `--managed-resource-label`). Note: the injector's CRD-stamping path depends on the CRDs being delivered as a Gardener `ManagedResource` on the seed — it does not read any label on the shoot-side CRD objects themselves.

Concretely: `system/metal-operator-remote/managedresources/crds-and-rbac.yaml` is 5542 lines of pre-rendered upstream content, committed to git and regenerated by `make build-metal-operator-remote`.

Total across five operators: **~30,000 lines of committed generated YAML**, five bespoke Makefile targets with per-chart `sed`/`yq` invocations, no shared abstraction, and three separate remote-delivery mechanisms.

### 1.3 What hurts

- **Silent drift.** `make build-<operator>-remote` is not run in CI. Chart bumps depend on someone remembering to run it before committing. `yq`/`sed` version differences produce non-deterministic diffs.
- **Generated files pollute reviews.** PRs bumping upstream versions carry thousand-line diffs of rendered YAML that reviewers must skim manually to detect regressions.
- **Per-operator bespoke Makefile logic.** Five operators, five slightly-different Makefile targets. Adding a sixth operator means writing a sixth target.
- **Pre-rendering breaks values-time customization.** Any change that would depend on install-time values (e.g., URL prefix per-cluster, sidecar image tag override) can't easily be expressed because the content is baked at chart-source time.
- **Three separate remote-delivery paths increase surface area for bugs.** Split among GRM (MR), webhook-injector (WebhookConfig delivery), and webhook-injector (cert lifecycle). Debugging a broken remote state requires knowing which of the three delivered which resource.
- **The kustomize POC ([sapcc/helm-charts#11633](https://github.com/sapcc/helm-charts/pull/11633)) showed that a pure-tooling swap doesn't help.** Substituting kustomize for Makefile+yq produced **+79% LOC and +130% file count** vs. the current chart pattern. The problem isn't the tool; it's the pre-render-and-commit model.

### 1.4 What the POC established

The archived POC (`openspec/changes/archive/2026-05-13-poc-kustomize-metal-operator-remote/`) attempted to replace `make build-metal-operator-remote` with a pure kustomize overlay. Findings:

- **Kustomize can express the transformations** but doing so requires substantially more configuration than the current Makefile approach.
- **Flux `HelmRelease.postRenderers`** only supports kustomize, not arbitrary string composition — so URL rewrites via string prefix cannot be expressed at deploy time via Flux alone.
- **Webhook `clientConfig` cannot be avoided at chart level.** Upstream tightly couples webhook definitions (paths, names) to the controller code; we must consume upstream's WebhookConfigurations and transform them, not fork them.
- The chart's "consume upstream unchanged, transform locally" pattern is unavoidable; the question is only *where* the transformations run.

The POC's LOC verdict:

| | current chart | kustomize POC |
|---|---|---|
| LOC (per operator) | 820 | 1470 |
| File count | 10 | 23 |

The POC was archived with the verdict: **kustomize as a tooling substitute is worse than what we have. The next iteration must move the transformations out of chart-source time entirely** — either into deploy time (a controller) or eliminate them (upstream changes).

---

## 2. Alternatives considered

### 2.1 Rendering approach alternatives

Nine alternatives were surveyed for the rendering-and-transformation problem. Summary:

| Alternative | Why not |
|---|---|
| Keep current (Helm + Makefile sed/yq) | Fragile; drift-prone; 30k lines of committed generated files; not addressable in CI |
| Dual-kustomization POC | +79% LOC, +130% files vs. current — objectively worse |
| Pure Helm chart, no upstream fork | Webhook `clientConfig.service` must be rewritten to URL; Helm subcharts cannot post-process each other's output |
| Flux `HelmRelease.postRenderers` (kustomize) | Kustomize can't do string-prefix URL construction; also can't inject an initContainer whose spec references chart-time values cleanly |
| Gardener extension | Extensions are tied to shoot lifecycle events; our trigger is a manual deployment decision, not a shoot state change |
| Pipeline operator with a DSL | Reinvents kustomize with 3-5 transformations that don't justify a DSL; DSL invites scope creep |
| Chart-baked with operator for delivery only | Doesn't eliminate `make build-`; committed generated files stay |
| Chart-with-upstream-dep + operator for transforms (no kustomize support) | Works for 4/5 charts. Ipam-capi's upstream is kustomize, no Helm dep possible → still needs a `make build-` step. Asymmetric across the fleet. |
| **Operator with source discriminator (Helm + kustomize)** | Works uniformly for all 5. Requires operator to support two rendering engines (Helm SDK + krusty). **Chosen.** |

### 2.2 Delivery mechanism alternatives

Independent of the rendering choice, four delivery options were considered for getting rendered resources to their target clusters:

| Option | Host delivery | Remote CRDs/RBAC delivery | Remote WebhookConfig delivery | Cert lifecycle |
|---|---|---|---|---|
| **A. Preserve today's mechanisms** (baseline) | Flux HelmRelease + Helm install | ManagedResource + GRM | webhook-injector sidecar reads seed ConfigMap, applies to shoot | webhook-injector generates + rotates certs, patches caBundles in shoot |
| **B. Option 1** | Operator directly (SSA) | Operator directly (second Kubernetes client) | webhook-injector unchanged | webhook-injector unchanged |
| **C. Option 2 (chosen)** | Operator directly (SSA) | Operator directly (second Kubernetes client) | **webhook-injector, from a source ConfigMap the operator produces** (`packageWebhookConfigsForInjector`) | webhook-injector unchanged (certs + caBundle injection into the WebhookConfigs it delivers) |
| **D. Option 3** | Operator directly (SSA) | Operator directly (second Kubernetes client) | Operator directly | **Operator absorbs cert lifecycle** — webhook-injector deleted |

The GRM-based baseline (A) is the current architecture. Options B/C/D progressively fold delivery mechanisms into the operator.

#### 2.2.1 Rejected: Option A (preserve MR + GRM for remote CRDs/RBAC)

Argued for keeping the design as originally written in this doc's previous revision. Trade-offs:

- ✅ Gardener-idiomatic
- ✅ GRM provides drift correction, health tracking, keepObjects, token rotation
- ❌ Two separate remote-delivery paths (GRM for CRDs/RBAC, injector for WebhookConfigs) — same surface-area problem as today
- ❌ MR wrapping adds a Kubernetes-object-per-resource with a Secret-per-resource — proliferation
- ❌ Chart must emit `templates/managedresource.yaml`, adding complexity to the wrapper
- ❌ GRM benefits are **not additive to the operator's necessary responsibilities**: the operator must implement drift correction and health tracking for **host resources anyway** (Flux HelmRelease doesn't drift-correct at the individual resource level, doesn't track per-resource health). Once the operator has these mechanisms for host, applying the same code to remote via a second Kubernetes client is nearly zero additional work. GRM adds a layer of indirection with no differential benefit.

**Rejected because**: MR/GRM's benefits are illusory relative to work we must do for host anyway. The abstraction adds complexity without adding capability.

#### 2.2.2 Rejected: Option 1 (direct apply for CRDs/RBAC only, injector unchanged)

The half-measure. Trade-offs:

- ✅ Removes MR wrapping for CRDs/RBAC
- ✅ Uses the operator's drift-correction/health machinery uniformly for one class of remote resources
- ❌ WebhookConfigurations still delivered via `webhook-config` ConfigMap + webhook-injector reads-and-applies loop
- ❌ Chart still has to emit the `webhook-config` ConfigMap, which is populated via `.Files.Get "webhooks.yaml"` — but under the new design there is no `webhooks.yaml` (upstream renders live). The ConfigMap-based delivery becomes awkward: the chart would need to emit a ConfigMap containing rendered WebhookConfigs, which the injector would then re-render and apply. Duplicative and clumsy.
- ❌ Doesn't reduce the number of remote-delivery paths (still 2 after eliminating MR: injector for WebhookConfigs + operator for CRDs/RBAC)
- ❌ Chart complexity retains the injector's contract (specific ConfigMap name, label conventions, etc.)

**Rejected because**: it retains the awkwardness of injector's WebhookConfig-delivery role without meaningful simplification. If we're going to change delivery, we should change it decisively.

#### 2.2.3 Chosen: Option 2 (direct apply for CRDs/RBAC AND WebhookConfigs, injector for certs only)

Trade-offs:

- ✅ **Operator is the single delivery path for CRDs, RBAC, ServiceAccounts, and the operator's own additions** — applied directly via one Kubernetes client, no `ManagedResource` wrapping
- ✅ Chart emits **no delivery-shaped resources** — no MR wrapper template, no hand-authored `webhook-config` ConfigMap, no `.Files.Get` indirection. Chart is purely a manifest source.
- ✅ Drift correction and health tracking apply uniformly to host and remote via the same operator machinery (which we build anyway for host)
- ✅ Deployment topology becomes crisper: one operator per shoot-cp namespace with two Kubernetes clients (seed + shoot)
- ✅ webhook-injector keeps its existing, unchanged contract (source-ConfigMap delivery of WebhookConfigs + cert lifecycle) — no injector code change required
- ✅ CR status is the single source of truth for the resources the operator delivers
- ⚠️ WebhookConfigurations are still delivered by the injector, not the operator — the operator produces the injector's source ConfigMap via the `packageWebhookConfigsForInjector` cross-stream transformation (§3.4.6). This preserves the injector's contract without a `make build-` step.
- ⚠️ Requires the operator to hold a shoot kubeconfig — same credential model as the webhook-injector today, via Gardener token-requestor. No new mechanism.
- ⚠️ Operator and injector write **disjoint** resources on the shoot (operator: CRDs/RBAC/SA/additions; injector: WebhookConfigurations). No shared-resource field ownership is required, so no SSA co-ownership discipline is needed. The injector uses `Create`/`Update`, not SSA.

**Preferred over Option 1 because**:

| Aspect | Option 1 | Option 2 |
|---|---|---|
| Remote delivery paths | 2 (operator + injector) | 1 (operator only) |
| Chart complexity | Still has `webhook-config` ConfigMap contract with injector | Chart emits pure manifest resources; no delivery contracts |
| WebhookConfig update flow | Chart → ConfigMap → injector reads → injector applies | Chart → operator applies directly |
| Injector's role | Full delivery + certs + caBundle | Delivery of WebhookConfigs (from operator-produced ConfigMap) + certs + caBundle injection |
| Operator scope | Applies to host + partial remote | Applies to host + full remote |
| Debug story when webhook fails to appear in shoot | "Which of the two paths is stuck? Check injector logs and CR status" | "Check CR status" |
| Adding a new remote resource kind | Depends on kind — CRD/RBAC goes through operator, WebhookConfig-like things go through injector, other kinds need routing decision | Everything goes through operator, kind-agnostic |

The comparison table above is the concrete list of reasons for choosing Option 2 over Option 1. Option 1 is a partial simplification that leaves an asymmetry between delivery paths for CRDs and delivery paths for WebhookConfigs. Option 2 achieves the same delivery uniformity we'd need anyway to reason about remote state coherently, at the cost of teaching the operator to apply one additional resource kind (WebhookConfiguration is not special from a Kubernetes-client perspective).

**Preferred over Option 3 (absorbing injector entirely) because**:

Option 3 would require the operator to own TLS cert generation, rotation timing with overlap windows, and caBundle propagation. This is well-worn code in the webhook-injector today with known behavior. Reimplementing it in the operator is real work with real regression risk, for the marginal benefit of removing one Kubernetes Deployment (the injector). Not worth the trade in v1.

Option 3 remains a valid future consolidation if the injector becomes a maintenance burden or if we want a strictly single-component deployment. It is not a v1 goal.

---

## 3. Design

### 3.1 Architecture

```
                  DualDeploymentOperator (CR, in seed's shoot-cp-* namespace)
                            │
                            ▼
              ┌─────────────────────────────┐
              │   dual-deployment-operator  │
              │  (Go binary in shoot-cp-*)  │
              └─────────────────────────────┘
                            │
              ┌─────────────┴─────────────┐
              ▼                           ▼
   HOST RENDER                      REMOTE RENDER
   Helm SDK: values +               Helm SDK: values +
     hostValues                       remoteValues
        or                              or
   Krusty: url/hostPath             Krusty: url/remotePath
              │                           │
              ▼                           ▼
    Host manifest stream         Remote manifest stream
              │                           │
              ▼                           ▼
    Per-render transforms        Per-render transforms
    (5 typed types)              (5 typed types)
              │                           │
              └─────────────┬─────────────┘
                            │
                            ▼
             Cross-stream transforms
             (packageWebhookConfigsForInjector, ...)
             (may move resources between renders)
                            │
              ┌─────────────┴─────────────┐
              ▼                           ▼
    seed Kubernetes client       shoot Kubernetes client
    (in-cluster SA)              (kubeconfig from Gardener
              │                   token-requestor Secret)
              ▼                           │
   Server-side apply to seed              ▼
              │                Server-side apply to shoot
              │                           │
              └─────────────┬─────────────┘
                            │
                            ▼
         Per-resource drift correction + health
              (unified across both clients)
                            │
                            ▼
                     CR status update
```

Two independent renders per reconcile. Each render produces only the resources for its target cluster — the chart/kustomization is responsible for emitting the right set per mode via values or overlay path. Operator does not decide routing; the source decides.

**Cross-stream transformations** run after per-render transforms complete. Currently used for one purpose: `packageWebhookConfigsForInjector` moves WebhookConfigurations from the remote render into a ConfigMap in the host render, so the webhook-injector sidecar can consume them via its source-ConfigMap contract (§3.4.6, §3.8).

**Coexistence with webhook-injector**:

```
webhook-injector (sidecar of each operator Pod, per shoot)
     │
     ├── Watches cert Secret in seed
     ├── Generates + rotates TLS certs (writes to cert Secret)
     ├── Watches source ConfigMap (produced by operator's
     │   packageWebhookConfigsForInjector transformation)
     └── Applies WebhookConfigurations from the ConfigMap to the
         shoot with the current caBundle injected, keeping them
         current as certs rotate
```

Injector's role is essentially unchanged from today (source ConfigMap → deliver WebhookConfigs with caBundle injected + rotate certs). The only difference: the source ConfigMap is produced by the operator from live-rendered WebhookConfigurations rather than by the chart from pre-baked `webhooks.yaml`.

The operator and injector write **disjoint** resources on the shoot:
- Operator (field manager `dual-deployment-operator`) applies CRDs, RBAC, ServiceAccounts, and the operator's own additions. It does not touch WebhookConfigurations and does not stamp caBundle anywhere.
- Injector applies WebhookConfigurations (with caBundle) via `Create`/`Update` — it does not use server-side apply, and it does not co-own any resource with the operator.

Because the two components never write the same resource on the shoot, no SSA field-ownership coordination is required.

**CRD conversion-webhook caBundle (open limitation)**: the injector's only CRD caBundle-stamping path is its `ManagedResourceReconciler`, which selects Gardener `ManagedResource` objects on the seed by `--managed-resource-label` and stamps caBundle into their embedded CRDs. This design deliberately does not produce `ManagedResource` objects, so that path does not fire. For operators whose CRDs carry a conversion webhook (metal-operator today), the caBundle on `.spec.conversion.webhook.clientConfig` is therefore **not** managed by this design as written. See §9.2 for the open question and candidate resolutions. The operator does not take on this responsibility — it is out of scope per the operator/injector separation.

### 3.2 Custom Resource

Full example (metal-operator, Helm source):

```yaml
apiVersion: dual-deployment-operator.cc.sap/v1alpha1
kind: DualDeploymentOperator
metadata:
  name: metal-operator
  namespace: shoot--cp--m-eu-de-1
spec:
  source:
    # Exactly one of {helm, kustomize} must be set.
    helm:
      repo: oci://keppel.eu-de-1.cloud.sap/ccloud-helm
      name: metal-operator-remote
      version: "0.7.x"

      # Common values — applied to BOTH host and remote renders.
      # Per-cluster stuff (image args, mac database, apiserver URL) lives here.
      values:
        metal-operator-core:
          controllerManager:
            manager:
              args: [--mac-prefixes-file=/etc/macdb/macdb.yaml, ...]
              env: {KUBECONFIG: /var/run/remote-kubeconfig/kubeconfig, ...}
        macdb: {...}
        apiserverURL: "api.m-eu-de-1.cp..."
        webhookInjector: {image: "keppel.../webhook-injector", tag: "sha-..."}

      # Host-only overrides — applied ONLY to the host render.
      # Enables the parts of the upstream subchart that belong on host.
      hostValues:
        metal-operator-core:
          controllerManager: {enable: true}

      # Remote-only overrides — applied ONLY to the remote render.
      # Enables the parts of the upstream subchart that belong on remote.
      remoteValues:
        metal-operator-core:
          rbac:    {enable: true}
          crd:     {enable: true}
          webhook: {enable: true}

  # Kubeconfig for the shoot cluster.
  # Mounted from a Gardener token-requestor-managed Secret in this namespace.
  remoteKubeconfig:
    secretName: metal-operator-remote-kubeconfig
    key: kubeconfig

  transformations:
    # Sidecar injection via strategic-merge patch on the upstream Deployment.
    - patch:
        target: {kind: Deployment, name: metal-operator-controller-manager}
        strategicMerge:
          spec:
            template:
              spec:
                initContainers:
                  - name: webhook-injector
                    image: "keppel.global.cloud.sap/.../webhook-injector:sha-fd8a075..."
                    args: [--webhook-config-name=metal-operator-remote-webhook-config]
                    volumeMounts:
                      - {name: webhook-certs, mountPath: /tmp/k8s-webhook-server/serving-certs, readOnly: true}
                volumes:
                  - name: webhook-certs
                    emptyDir: {}

    - rewriteWebhookURL:
        urlPrefix: "https://metal-operator-remote-webhook-service:443"

    - filterKinds:
        kinds: [Service]

    # Cross-stream: packages remote-render WebhookConfigurations into a
    # ConfigMap on the host so the webhook-injector sidecar can consume them.
    # Removes WebhookConfigurations from the remote-render (they're delivered
    # by the injector, not by the operator's direct apply to the shoot).
    - packageWebhookConfigsForInjector:
        configMapName: metal-operator-remote-webhook-config


status:
  # Populated by the operator on each reconcile.
  hostResources:
    - {kind: Deployment, name: metal-operator-controller-manager, health: Healthy, lastApplied: ...}
    - ...
  remoteResources:
    - {kind: CustomResourceDefinition, name: endpoints.metal.ironcore.dev, health: Healthy, lastApplied: ...}
    - ...
  conditions:
    - {type: Ready, status: "True", ...}
    - {type: HostReconciled, status: "True", ...}
    - {type: RemoteReconciled, status: "True", ...}
```

For ipam-capi (kustomize source):

```yaml
spec:
  source:
    kustomize:
      url: "https://github.com/sapcc/helm-charts//system/kustomize/ipam-capi-remote/?ref=v1.2.31"
      hostPath: "host"
      remotePath: "remote"
  remoteKubeconfig:
    secretName: ipam-capi-remote-kubeconfig
    key: kubeconfig
  transformations:
    - patch:
        target: {kind: Deployment, name: ipam-capi-controller-manager}
        strategicMerge:
          spec:
            template:
              spec:
                initContainers:
                  - name: webhook-injector
                    image: "..."
                    args: [--webhook-config-name=ipam-capi-remote-webhook-config]
                    volumeMounts: [...]
                volumes:
                  - {name: webhook-certs, emptyDir: {}}
    - rewriteWebhookURL: {urlPrefix: "https://ipam-capi-remote-webhook-service:443"}
    - filterKinds: {kinds: [Service]}
    - packageWebhookConfigsForInjector:
        configMapName: ipam-capi-remote-webhook-config
```

For simpler operators (boot / argora / khalkeon), transformations reduce to a single filter:

```yaml
spec:
  source:
    helm:
      repo, name, version
      values: {...}
      hostValues:
        boot-operator-core:
          controllerManager: {enable: true}
      remoteValues:
        boot-operator-core:
          rbac: {enable: true}
          crd:  {enable: true}
  remoteKubeconfig: {secretName, key}
  transformations:
    - filterKinds: {kinds: [Service]}
```

The CR is the entire configuration surface. Per-cluster differences live in `spec.source.helm.values` (Helm) or are baked into the pinned kustomize source (kustomize; per-cluster overrides via different pinned refs, or via optional future patches — see §9.4). Mode-specific settings (which upstream subchart parts to enable per mode for Helm; which overlay directory for kustomize) live in mode-specific fields under the source discriminator. Shoot kubeconfig comes from Gardener's token-requestor per standard practice (see §3.6).

### 3.3 Source discriminator

The operator supports two source types, exactly one of which must be set. Each source declares its own fields — no shared "values" at `source` level.

**`spec.source.helm`**:
- `repo` — OCI or HTTP Helm repo URL
- `name` — chart name
- `version` — semver constraint (must resolve deterministically)
- `values` — common values, applied to both renders (map, passed to `helm template -f`)
- `hostValues` — host-render-only overrides (map, merged on top of `values` for the host render)
- `remoteValues` — remote-render-only overrides (map, merged on top of `values` for the remote render)

Implementation: `helm.sh/helm/v3`. For each render:
```
renderValues = merge(chart.values.yaml, spec.values, spec.hostValues or spec.remoteValues, {mode: "host" or "remote"})
manifestStream = helm template chart with renderValues
```

Chart's own `values.yaml` provides defaults for everything not overridden by the CR (image repos/tags, resource limits, default annotations, etc.). Chart's templates use `{{ if eq .Values.mode "host" }}` / `{{ if eq .Values.mode "remote" }}` guards to include/exclude resources per mode.

**`spec.source.kustomize`**:
- `url` — kustomize root URL. Format: `https://github.com/{org}/{repo}//{path}?ref={sha|tag}`. `ref` is required — floating references are rejected at CR admission.
- `hostPath` — subpath under `url` for the host overlay root (required; no default). Must be set explicitly per CR.
- `remotePath` — subpath under `url` for the remote overlay root (required; no default). Must be set explicitly per CR.

Implementation: `sigs.k8s.io/kustomize/api/krusty`. For each render:
```
hostRoot   = url + "/" + hostPath
remoteRoot = url + "/" + remotePath
manifestStream = krusty.Build(hostRoot or remoteRoot)
```

The kustomize source is expected to have `host/` and `remote/` (or the paths configured in CR) subdirectories, each with its own `kustomization.yaml` selecting the appropriate resources for that mode. See §4.2 for the ipam-capi layout.

Kustomize has no Helm-values equivalent; per-cluster overrides are baked into the pinned kustomize source. If a chart needs per-cluster differences (image tags, apiserver URL), it either uses different pinned refs per environment or adopts a future CR-side patch mechanism (§9.4). For today's five candidate charts, ipam-capi's needs can be met with a pinned image tag in its kustomize source per release cycle.

**Origin tagging**. Both source types produce a stream of unstructured Kubernetes manifests per render. The operator tags every manifest with `dual-deployment-operator.cc.sap/origin: upstream` if it originated from the upstream subchart (Helm) or upstream reference (kustomize), and expects the chart's own templates to carry `dual-deployment-operator.cc.sap/origin: additions` via `_helpers.tpl`. This tag lets selective transformations distinguish upstream from our additions — used by `patch: target: {origin: upstream}` and, optionally, `filterKinds: source: upstream`. Neither candidate operator relies on the origin distinction for `filterKinds` (they filter all Services regardless of origin); the tag's load-bearing consumer is `patch` targeting.


### 3.4 Transformation menu

The operator has a small, bounded set of transformation types. Most are Go structs with strict schemas; one (`patch`) uses Kubernetes-native patch formats (strategic-merge, JSON Patch) as an embedded DSL. Adding a new transformation type requires an operator release.

**Two scopes**:

- **Per-render transformations** (3 in v1): `patch`, `rewriteWebhookURL`, `filterKinds`. Applied independently to the host render and the remote render. Each transformation naturally affects only resources present in the render it's applied to.
- **Cross-stream transformations** (1 in v1): `packageWebhookConfigsForInjector`. Operates on both renders together, after all per-render transformations have completed.

Ordering. The reconciler groups declared transformations by scope: all per-render transformations run first (in declaration order, on both renders independently), then all cross-stream transformations run (in declaration order, on paired renders).

Per-render ordering convention:

1. Structural changes (`patch` for sidecar injection, labels, etc.) first
2. Complex rewrites (`rewriteWebhookURL`) after structural changes
3. Filters (`filterKinds`) last

Cross-stream ordering: `packageWebhookConfigsForInjector` runs after per-render (specifically after `rewriteWebhookURL` since WebhookConfigs are URL-rewritten before packaging).

#### 3.4.1 `patch`

Applies a Kubernetes-native patch (strategic merge or JSON Patch) to resources matching a target selector. Replaces the historical `injectInitContainer` and `addLabels` typed transformations — for patch-shaped operations, the patch content in the CR is more transparent than a typed wrapper that hides the same content.

Uses **typed variants** rather than a single opaque string. Exactly one of `strategicMerge` (an object) or `jsonPatch` (an array of ops) must be set — this lets CRD schema validate the patch shape at admission time (object vs array; JSON Patch op enum, required fields).

```yaml
- patch:
    target:
      kind: Deployment            # required
      name: metal-operator-controller-manager   # optional
      namespace: "..."            # optional
      origin: upstream            # optional; "upstream" | "additions"

    # Exactly one of the two variants:

    # Variant 1 — strategic merge patch (Kubernetes-aware list merging)
    strategicMerge:
      spec:
        template:
          spec:
            initContainers:
              - name: webhook-injector
                image: "..."
                args: [...]
                volumeMounts: [...]
            volumes:
              - name: webhook-certs
                emptyDir: {}

    # Variant 2 — JSON Patch (RFC 6902 ops list)
    # jsonPatch:
    #   - op: add
    #     path: /metadata/labels/some-key
    #     value: "true"
```

Semantics:
- `strategicMerge` — arbitrary JSON object, applied as a strategic-merge patch (Kubernetes list-key aware). Implemented via `k8s.io/apimachinery/pkg/util/strategicpatch`.
- `jsonPatch` — list of `{op, path, from?, value?}` operations following RFC 6902. `op` is one of `add`, `remove`, `replace`, `move`, `copy`, `test` (validated by CRD enum). Implemented via `github.com/evanphx/json-patch`.
- Applied to every resource matching the selector. If zero matches, error (fail-loud: mismatched selector is a misconfiguration).
- Idempotent: byte-identical inputs produce byte-identical outputs.

**Admission-time validation** (via CRD OpenAPI schema + a CEL rule):
- Exactly one of `strategicMerge` / `jsonPatch` set (CEL: `has(self.strategicMerge) != has(self.jsonPatch)`)
- `strategicMerge` must be a JSON object (not string, not array)
- `jsonPatch` must be an array of objects
- Each `jsonPatch` entry has required `op` (enum) and `path` fields

**Runtime validation** (at reconcile time, when the patch is applied):
- Well-formed patch content (structural sanity beyond what CRD catches)
- Whether the patch applies cleanly to matched resources (e.g., path exists for JSON Patch `replace`)
- Content-level correctness inside `strategicMerge` object (image field format, container name uniqueness, etc.) — CRD marks the object body as `x-kubernetes-preserve-unknown-fields: true`, so its inner contents are not admission-validated

Common use cases:
- **Sidecar injection**: `strategicMerge` with `spec.template.spec.initContainers` and `.volumes` (replaces the old `injectInitContainer` transformation)
- **Adding labels**: `strategicMerge` with `metadata.labels` (replaces the old `addLabels` transformation), or `jsonPatch` with `op: add` at `/metadata/labels/<key>`
- **Field overrides**: any field on any resource that matches the selector

**Planned enhancement**: v2 will add a validating admission webhook that parses `strategicMerge`/`jsonPatch` content against the target kind's OpenAPI schema. This will catch content-level errors (wrong field types, unknown fields) at admission instead of at reconcile. Deferred to v2 to keep v1 scope small; webhook infrastructure adds cert management and deployment complexity. See §9.9.

#### 3.4.2 `rewriteWebhookURL`

Rewrites `.webhooks[].clientConfig.service` → `.webhooks[].clientConfig.url` on `ValidatingWebhookConfiguration` and `MutatingWebhookConfiguration`.

```yaml
- rewriteWebhookURL:
    urlPrefix: "https://metal-operator-remote-webhook-service:443"
```

For each webhook whose `.clientConfig.service` is set: replace with `.clientConfig.url = urlPrefix + service.path`. Existing `.url` is left alone (idempotent). `.caBundle` is preserved.

**Stays typed** (not expressed as `patch`) because the operation iterates over an array (`.webhooks[]`) with a conditional (only replace if `.service` is set), constructs the new value from parts (`urlPrefix + service.path`), and preserves other fields. Not naturally expressible as a strategic-merge or JSON patch.

#### 3.4.3 `filterKinds`

Drops resources of listed kinds from the manifest stream (neither host nor remote — discarded).

```yaml
- filterKinds:
    kinds: [Service, ConfigMap]
```

`source` (optional) restricts the filter to resources of a given origin (`upstream` or `additions`). Omit it — as every candidate operator does — to drop all resources of the listed kinds regardless of origin. In practice the chart already suppresses unwanted upstream resources at render time via mode values / overlays, so an unqualified `filterKinds` is sufficient. `source` exists as an escape hatch for the rare case where an upstream render hardcodes a resource you cannot disable via values *and* your own additions emit a resource of the same kind in the same render that must survive the filter. It relies on the `origin` annotation (see §3.3).

**Stays typed** because it's a stream filter (removes resources from the manifest list), not a patch on a resource. JSON Patch and strategic-merge operate within a single resource; they can't remove a resource from the stream.

#### 3.4.4 `packageWebhookConfigsForInjector` (cross-stream)

**Scope**: cross-stream. Runs after all per-render transformations complete. Reads from the remote render, emits into the host render.

Packages Validating/MutatingWebhookConfigurations from the remote render into a ConfigMap on the host, so the webhook-injector companion controller can consume them via its source-ConfigMap contract.

**Why it exists**: the current webhook-injector reads WebhookConfigurations from a source ConfigMap on the seed and cannot be scoped by label (see §3.8 for injector behavior details). To avoid a `make build-` step producing pre-rendered `webhooks.yaml`, the operator packages live-rendered WebhookConfigurations into the ConfigMap the injector expects.

```yaml
- packageWebhookConfigsForInjector:
    configMapName: metal-operator-remote-webhook-config
    dataKey: webhooks.yaml           # optional, default "webhooks.yaml"
```

Semantics:
1. Removes every `ValidatingWebhookConfiguration` and `MutatingWebhookConfiguration` from the remote-render output (they will be delivered by the injector, not by the operator's direct apply to the shoot)
2. Serializes the removed resources as a multi-document YAML string
3. Emits a new `ConfigMap` into the host-render output:
   ```yaml
   apiVersion: v1
   kind: ConfigMap
   metadata:
     name: <configMapName>
     namespace: <CR namespace>
     annotations:
       dual-deployment-operator.cc.sap/origin: additions
   data:
     <dataKey>: |
       <serialized WebhookConfigurations>
   ```

The injector, running as a sidecar per operator Pod in the seed, watches this ConfigMap, applies the WebhookConfigurations to the shoot cluster, and manages their `caBundle` alongside cert rotation.

**Ordering**: must run AFTER `rewriteWebhookURL` (URLs should be rewritten before packaging) and any other per-render transformations that touch WebhookConfigurations. The reconciler enforces this by running all per-render transformations first, then cross-stream transformations in declaration order.

**Applicability**: charts that use the webhook-injector (metal-operator, ipam-capi). Charts without webhooks (boot, argora, khalkeon) do not include this transformation.

#### 3.4.5 Menu extensibility and design stance

New transformation types are added by operator releases. Not extensible at runtime.

**Hybrid stance: typed for structurally-complex operations, embedded DSL (`patch`) for patch-shaped operations.**

The four current transformations reflect this:
- `patch` — DSL. Applies strategic-merge or JSON Patch. Replaces the historical `injectInitContainer` and `addLabels` typed types (both were thin wrappers around patches; the DSL makes the operation visible in the CR).
- `rewriteWebhookURL` — typed. Iteration + conditional replacement; not naturally expressible as a patch.
- `filterKinds` — typed. Stream-level filter (removes resources); not a per-resource patch.
- `packageWebhookConfigsForInjector` — typed cross-stream. Moves resources between renders; not a per-resource patch.

Rationale for the hybrid:
- Patches make the operation transparent in the CR — reader sees exactly what the transformation does, not a name that hides the same content
- Complex/stream operations don't map cleanly to patches; forcing them would lose clarity, not gain it
- Typed transformations get admission-time schema validation (Kubernetes-native Container spec, kind lists, etc.); the `patch` type validates the patch string at runtime (patch application errors surface as reconcile errors, not admission errors)

Design history (r5 → r6):
- r5 had 5 typed per-render + 1 typed cross-stream. `injectInitContainer`, `addLabels`, `renameKind` were separate typed types.
- r6 (current): `renameKind` removed (was an artifact of ManagedResource-based delivery; direct-apply doesn't require the Role→ClusterRole conversion). `injectInitContainer` and `addLabels` collapsed into a single `patch` DSL — both were patch-shaped, and the typed wrapper hid the same content that a patch would show explicitly.

Candidate future types (not implemented in v1, listed to document the extension path):

- `setImageTag` — override container image tag by selector; specifically useful for ipam-capi's per-cluster image tag override (see §9.4). Could be added if kustomize's `images:` transformer doesn't cover the use case.

**Not on the roadmap**:
- Routing/split-related transformations (`setTarget`, kind-based routing, target annotations). Under two-render (§3.5), routing is determined by what each render emits, not by post-render classification.
- Runtime plugin loading. Extensibility is via operator releases.
- ConfigMap-loaded named patch templates. Considered as compromise between typed and DSL; rejected because `patch` covers the common case without templating engine complexity, and complex operations stay typed.

### 3.5 Two-render pattern

Instead of rendering the source once and splitting the output by kind or annotation, the operator renders the source **twice per reconcile** — once for host, once for remote — using mode-specific configuration to control what each render emits. Each render's output goes entirely to its target cluster.

#### 3.5.1 Rationale

Two-render is preferred over post-render split because:

1. **No routing decisions in the operator or CR.** The chart/kustomization decides what belongs to each cluster via values or overlay paths. The operator applies what each render produces, unmodified in destination.
2. **Correctness for symmetric topologies.** If a chart legitimately needs, say, a Deployment on both clusters, two-render produces two independent Deployments in the two renders. Post-render split by kind would collapse them.
3. **Familiar Helm/kustomize idiom.** Values-based mode switching is how charts routinely support multi-environment deploys. Two overlay directories is how kustomize expresses variant renders. Chart maintainers are on familiar ground.
4. **No hidden operator rules.** No hardcoded kind → target table; no CR-side routing rules to duplicate across shoots.

The alternative (single render + operator-side split by kind or annotation) was rejected — see §3.5.5.

#### 3.5.2 How chart controls each render (Helm)

Chart's `_helpers.tpl` defines a mode-aware guard:

```yaml
{{- define "dual.host" -}}{{ eq .Values.mode "host" }}{{- end }}
{{- define "dual.remote" -}}{{ eq .Values.mode "remote" }}{{- end }}
```

Chart templates use these guards:

```yaml
# templates/webhook-service.yaml
{{- if eq .Values.mode "host" }}
apiVersion: v1
kind: Service
metadata:
  name: metal-operator-remote-webhook-service
  namespace: {{ .Release.Namespace }}
  annotations:
    dual-deployment-operator.cc.sap/origin: additions
spec:
  # ...
{{- end }}
```

```yaml
# templates/namespace.yaml
{{- if eq .Values.mode "remote" }}
apiVersion: v1
kind: Namespace
metadata:
  name: metal-servers
  annotations:
    dual-deployment-operator.cc.sap/origin: additions
{{- end }}
```

The upstream subchart is enabled selectively via `hostValues` / `remoteValues` passing `.enable: true` for the appropriate parts per mode:

```yaml
# CR's spec.source.helm.hostValues:
metal-operator-core:
  controllerManager: {enable: true}   # only host renders the Deployment

# CR's spec.source.helm.remoteValues:
metal-operator-core:
  rbac:    {enable: true}             # only remote renders RBAC
  crd:     {enable: true}             # only remote renders CRDs
  webhook: {enable: true}             # only remote renders WebhookConfigs
```

Operator sets `.Values.mode` per render (not user-settable via `values`; CR admission rejects `values.mode`).

#### 3.5.3 How kustomize source controls each render

Two overlay directories under the kustomize source URL, one per mode:

```
system/kustomize/ipam-capi-remote/
├── host/
│   └── kustomization.yaml    # resources: [../manager, ../additions/host]
├── remote/
│   └── kustomization.yaml    # resources: [../managedresources, ../webhooks, ../additions/remote]
├── manager/                   # upstream Deployment kustomize root
├── managedresources/          # upstream CRDs+RBAC
├── webhooks/                  # upstream WebhookConfigs
└── additions/
    ├── host/                  # our host-side manifests
    └── remote/                # our remote-side manifests
```

Operator renders `${url}/${hostPath}` for the host render and `${url}/${remotePath}` for the remote render. Each overlay's `kustomization.yaml` selects which resources to include via `resources:`.

Our custom manifests under `additions/host/` and `additions/remote/` carry `dual-deployment-operator.cc.sap/origin: additions` annotation (added via kustomize `commonAnnotations` in each additions/ subdir's kustomization.yaml).

#### 3.5.4 Concrete walk-through (metal-operator)

Two renders per reconcile:

**Host render** — operator calls `helm template metal-operator-remote` with values `{...common values..., mode: host, metal-operator-core.controllerManager.enable: true}`:

Rendered manifests:
| Resource | From | Target |
|---|---|---|
| `Deployment/metal-operator-controller-manager` | upstream (controllerManager.enable=true) | host |
| `Service/metal-operator-remote-webhook-service` | additions (mode=host guard) | host |
| `Service/metal-registry-service` | additions | host |
| `Ingress/metal-operator-ingress` | additions | host |
| `NetworkPolicy/...` | additions | host |
| `ConfigMap/remote-kubeconfig` | additions | host |
| `ServiceAccount/metal-operator-webhook-injector` | additions | host |
| `ConfigMap/macdb` | additions | host |
| `Service/metal-operator-webhook-service` | upstream (if webhook.enable were true — it's false here, so not emitted) | — |

After transformations: `patch` injects the sidecar into the Deployment. `filterKinds {kinds: [Service]}` drops any Services (none emitted in host mode since `webhook.enable: false`, but the transformation runs harmlessly).

Result: apply all resources in this render to the seed cluster (host).

**Remote render** — operator calls `helm template metal-operator-remote` with values `{...common values..., mode: remote, metal-operator-core.rbac.enable: true, .crd.enable: true, .webhook.enable: true}`:

Rendered manifests:
| Resource | From | Target |
|---|---|---|
| `CustomResourceDefinition/endpoints.metal.ironcore.dev` (and others) | upstream (crd.enable=true) | remote |
| `ClusterRole/manager-role` | upstream (rbac.enable=true) | remote |
| `ClusterRoleBinding/manager-rolebinding` | upstream | remote |
| `Role/leader-election-role` | upstream | remote (renamed to ClusterRole below) |
| `RoleBinding/leader-election-rolebinding` | upstream | remote |
| `ServiceAccount/metal-operator-controller-manager` | upstream | remote |
| `ValidatingWebhookConfiguration/metal-operator-validating-webhook-configuration` | upstream (webhook.enable=true) | remote |
| `MutatingWebhookConfiguration/metal-operator-mutating-webhook-configuration` | upstream | remote |
| `Service/metal-operator-webhook-service` | upstream (webhook.enable=true) | dropped |
| `Namespace/metal-servers` | additions (mode=remote guard) | remote |
| `ClusterRoleBinding/cc:oidc-ias-admin` | additions | remote |
| `ServiceAccount/metal-token-rotate` | additions | remote |
| `Role/metal-token-rotate` (namespaced, stays a Role) | additions | remote |
| `RoleBinding/metal-token-rotate` | additions | remote |
| ... | | |

After transformations:
- `rewriteWebhookURL` — WebhookConfigurations get URL-based `clientConfig` (service replaced by url).
- `filterKinds {kinds: [Service]}` — drops the webhook Service.
- `packageWebhookConfigsForInjector` (cross-stream) — removes WebhookConfigurations from remote render and emits them wrapped in a ConfigMap on host render.

Note: upstream's Role (leader-election) stays a Role — no renaming under direct-apply. Our `metal-token-rotate` Role also stays a Role. Both apply cleanly to the shoot with their original namespace scope.

Result: apply the remaining resources (CRDs, ClusterRoles, ClusterRoleBindings, Roles/RoleBindings, SA, our namespace, our RBAC) to the shoot cluster. WebhookConfigurations arrive via the injector's ConfigMap consumption.

Each render is a natural, coherent set. No annotation-based classification. No kind rules. No routing decisions. The chart's mode-guards and the CR's `hostValues`/`remoteValues` determine what each render produces.

#### 3.5.5 Why not single render + split

Considered and rejected. Single-render-plus-split requires:

- Either **hardcoded kind rules in the operator** (rejected: doesn't handle multi-Deployment topologies; new operators would need operator releases for new routing) —
- Or **routing annotations on every resource** (rejected: upstream resources can't be annotated by Helm subcharts; would require operator-time annotation-adding transformations, redundant with what Helm can already express via values) —
- Or **routing config emitted by the chart as a sentinel resource** (rejected: still requires the operator to consult chart-provided data at split time, doesn't handle multi-Deployment cases cleanly).

Two-render sidesteps all these problems. Chart/kustomization owns what goes where via native mechanisms (values, overlays). Operator just renders and applies.

**Cost of two-render**: two Helm/krusty renders per reconcile. Helm SDK rendering is fast (~50-100ms per render for typical charts). At 10min reconcile intervals per CR, this is negligible operator load. Rendering happens in the operator process, no chart repo pulls beyond the once-per-CR (cached).


### 3.6 Delivery

Operator holds two Kubernetes clients per CR:

- **Host client**: seed cluster in-cluster access via the operator's ServiceAccount (RBAC scoped to the CR's namespace)
- **Shoot client**: constructed from a kubeconfig held in a Secret in the CR's namespace. `spec.remoteKubeconfig.secretName` points at this Secret (typically named `<operator>-remote-kubeconfig`, provisioned by Gardener's token-requestor as today, mounted in the existing operator Pod as `/var/run/remote-kubeconfig/kubeconfig`)

Every resource in the **host render** output → apply to host client (seed).
Every resource in the **remote render** output → apply to shoot client (shoot).

No split step. Each render's output goes entirely to its target cluster.

#### 3.6.1 Server-side apply

All resources are applied with server-side apply (`k8s.io/client-go` `Apply` verb), field manager `dual-deployment-operator`. This provides:

- **Idempotency**: byte-identical inputs produce byte-identical apply calls with no-op semantics if state is already correct
- **Field-level ownership**: SSA tracks which manager owns which field, so if a human or another controller edits a field the operator does not own, the operator's re-apply leaves it alone
- **Conflict handling**: on field conflicts with other managers, operator uses `force: true` for fields it owns

Note: the operator does not share any shoot resource with the webhook-injector — the injector writes only WebhookConfigurations (which the operator does not deliver), and it uses `Create`/`Update`, not SSA. SSA is used here for the operator's own idempotency and drift correction, not for co-ownership with the injector.

#### 3.6.2 Drift correction

Every reconcile (default: every 10 minutes plus event-driven), the operator re-applies every resource to its target cluster. Because SSA is idempotent, a resource unchanged since the last apply produces no writes.

If a resource has been mutated externally (drift), the next reconcile re-applies the operator's version, restoring the fields the operator owns.

#### 3.6.3 Health tracking

For each applied resource, the operator computes a `status: {Healthy | Progressing | Degraded | Unknown}` value using standard resource-type-aware heuristics:

- `Deployment`: `.status.availableReplicas == .spec.replicas` and all Conditions positive → Healthy
- `StatefulSet`: same
- `CustomResourceDefinition`: `Established` and `NamesAccepted` conditions → Healthy
- `ValidatingWebhookConfiguration` / `MutatingWebhookConfiguration`: existence + caBundle set (present, non-empty) → Healthy
- `Service`, `Ingress`, `ConfigMap`, `Secret`, `Namespace`, `ClusterRole`, `ClusterRoleBinding`, `Role`, `RoleBinding`, `ServiceAccount`: existence → Healthy
- All others: existence → Healthy (may be refined per type as needed)

Per-resource status is written to `CR.status.hostResources` and `CR.status.remoteResources` lists.

Overall CR conditions:
- `HostReconciled`: all host resources Healthy
- `RemoteReconciled`: all remote resources Healthy
- `Ready`: both HostReconciled and RemoteReconciled

#### 3.6.4 Ownership and cleanup

Host resources:
- Set `metadata.ownerReferences` pointing at the CR
- Kubernetes garbage collection removes them on CR deletion

Remote resources:
- Cannot use ownerReferences (owner CR is in seed, not shoot — cross-cluster ownerRef isn't supported)
- Operator maintains an inventory in `CR.status.remoteResources` and issues explicit deletes on CR deletion
- **`keepObjects` semantic for CRDs**: on CR deletion, CRDs on shoot are preserved by default (they may hold user data). Operator only deletes CRDs if `CR.spec.deletionPolicy.crds == "Delete"` (default: `Retain`). Non-CRD remote resources are deleted normally.

Deletion is finalizer-driven:
- Operator adds finalizer `dual-deployment-operator.cc.sap/cleanup` on first reconcile
- CR deletion sets `deletionTimestamp` but finalizer keeps it around
- Operator handles cleanup, then removes finalizer, CR fully deletes

#### 3.6.5 Reconciliation triggers

The operator watches:
- `DualDeploymentOperator` CRs (primary)
- Applied resources on both clusters (for drift detection via informer notifications — same-cluster resources are cheap to watch; cross-cluster resources on the shoot use the shoot client to watch)

Reconciliation is triggered by:
- CR create/update/delete
- CR values change
- CR source version bump (chart version, kustomize ref)
- Applied resource mutation on either cluster (drift trigger)
- Periodic re-sync (default 10min)

### 3.7 CR deletion

Finalizer-driven cleanup:

1. `deletionTimestamp` set on CR
2. Operator handler runs cleanup:
   - Delete host resources via `ownerReferences` cascade (kubelet garbage-collects) or explicit deletion of top-level owner objects
   - Delete remote resources explicitly (iterate `CR.status.remoteResources`, delete via shoot client) — respecting `deletionPolicy.crds` for CRDs
3. Finalizer removed
4. CR deletes

CRDs retention (`deletionPolicy.crds: Retain`) is the default because CRDs hold user domain data; removing them cascades to user CRs. Explicit `Delete` is available as an opt-in for teardown scenarios.

### 3.8 Coexistence with webhook-injector

The webhook-injector runs as a sidecar of each operator Pod (unchanged from today). Its role is:

1. **Watch cert Secret** in the operator's namespace
2. **Generate initial TLS certs** and populate the cert Secret
3. **Rotate certs** on expiration with configured overlap window
4. **Watch source ConfigMap** — the one produced by the operator's `packageWebhookConfigsForInjector` transformation (see §3.4.6). This ConfigMap contains the WebhookConfigurations (`webhooks.yaml`) that should exist on the shoot.
5. **Apply WebhookConfigurations to the shoot** — reads from the source ConfigMap (`--webhook-config-name`), injects the current caBundle into each webhook's `clientConfig`, and applies them to the shoot via `Create`/`Update` on its mounted shoot kubeconfig. On cert rotation it re-applies with the fresh caBundle.

Injector's behavior is **unchanged from today** — same `--webhook-config-name` source-ConfigMap contract, same delivery, same caBundle injection, same cert lifecycle. The one difference is upstream of the injector: today the chart bakes the source ConfigMap from a pre-rendered `webhooks.yaml`; under the new design the operator produces it from live-rendered WebhookConfigurations. The injector requires **no code change**.

**The operator does not do any of the injector's work.** It does not deliver WebhookConfigurations, does not inject or stamp caBundle into any resource, and does not manage certs. Its only interaction with the injector is producing the source ConfigMap the injector already knows how to consume.

**No shared-resource field ownership.** On the shoot, the operator and injector write **disjoint** resource sets:
- Operator (field manager `dual-deployment-operator`, server-side apply): CRDs, ClusterRoles, ClusterRoleBindings, Roles, RoleBindings, ServiceAccounts, and the operator's own additions.
- Injector (`Create`/`Update`, not SSA): ValidatingWebhookConfigurations and MutatingWebhookConfigurations only.

Because no resource is written by both, there is no field-ownership coordination, no SSA co-ownership discipline, and no risk of caBundle ping-pong. The `packageWebhookConfigsForInjector` transformation removes WebhookConfigurations from the operator's remote render precisely so the operator never applies them to the shoot.

**CRD conversion-webhook caBundle — open limitation.** The injector's only mechanism for stamping caBundle into a CRD's `.spec.conversion.webhook.clientConfig` is its `ManagedResourceReconciler`, which selects Gardener `ManagedResource` objects on the **seed** by `--managed-resource-label` and stamps caBundle into the CRDs embedded in those ManagedResources' referenced Secrets. This design deliberately does **not** produce `ManagedResource` objects (eliminating GRM is a core goal — §2.2.1), so that reconciler never fires for CRDs delivered by this operator. Consequently, for an operator whose CRDs carry a conversion webhook (metal-operator today), **caBundle on those CRDs is not managed under this design as written**. The operator does not take on this responsibility — that would be doing the injector's work, which is explicitly out of scope. Candidate resolutions are recorded in §9.2; the decision is deferred and must be resolved before any operator with conversion-webhook CRDs is migrated.

**Ordering during initial deploy**:
1. Operator applies CRDs, RBAC, SA, and additions to the shoot.
2. Operator's `packageWebhookConfigsForInjector` emits the source ConfigMap to the seed.
3. Injector observes the cert Secret + source ConfigMap, generates certs, and applies WebhookConfigurations to the shoot with the caBundle injected.
4. Webhook calls succeed once the WebhookConfigurations are present with a valid caBundle.

Same bootstrap behavior as today for WebhookConfigurations.

---


## 4. Artifact restructure

### 4.1 Helm-upstream operators (metal, boot, argora, khalkeon)

**Before** (metal-operator-remote):

```
system/metal-operator-remote/
├── Chart.yaml                                # dep: metal-operator (disabled at install)
├── Chart.lock
├── charts/metal-operator-*.tgz               # cached upstream
├── values.yaml                                # upstream disabled, our stuff
├── values-overrides.yaml                      # for make build-
├── values-managed-resources.yaml              # for make build-
├── webhooks.yaml                    ← pre-rendered upstream webhooks + URL rewrite
├── templates/
│   ├── _helpers.tpl
│   ├── _webhook-injector-sidecar.tpl
│   ├── controller-manager.yaml       ← pre-baked upstream Deployment + sidecar
│   ├── managedresource.yaml          ← wraps managedresources/*.yaml as MR+Secret
│   ├── webhook-config.yaml           ← ConfigMap with webhooks.yaml embedded
│   ├── ingress.yaml, macdb.yaml, metal-registry-service.yaml, networkpolicy.yaml
│   ├── remote-kubeconfig-configmap.yaml, remote-kubeconfig.yaml
│   ├── rotate-kubeconfig.yaml
│   ├── webhook-injector-rbac.yaml
│   └── webhook-service.yaml
└── managedresources/
    ├── crds-and-rbac.yaml            ← 5542 lines of pre-rendered upstream
    ├── namespace.yaml
    └── rbac.yaml
```

**After** (two-render pattern; all templates in one directory, mode-guarded):

```
system/metal-operator-remote/
├── Chart.yaml                                # dep: metal-operator (subchart, controlled per mode)
├── Chart.lock
├── charts/metal-operator-*.tgz               # cached upstream
├── values.yaml                                # defaults: subchart all-disabled, our defaults
└── templates/
    ├── _helpers.tpl                          # stamps origin: additions on every resource
    ├── ingress.yaml                          # {{ if eq .Values.mode "host" }} ... {{ end }}
    ├── macdb.yaml                            # host-guarded
    ├── metal-registry-service.yaml           # host-guarded
    ├── networkpolicy.yaml                    # host-guarded
    ├── remote-kubeconfig-configmap.yaml      # host-guarded
    ├── remote-kubeconfig.yaml                # host-guarded
    ├── rotate-kubeconfig.yaml                # host-guarded
    ├── webhook-injector-rbac.yaml            # host-guarded
    ├── webhook-service.yaml                  # host-guarded
    ├── namespace.yaml                        # {{ if eq .Values.mode "remote" }} ... {{ end }}
    └── extra-rbac.yaml                       # remote-guarded (was managedresources/rbac.yaml)
```

**Deleted**:
- `webhooks.yaml` — upstream renders live via Helm dep (in remote mode)
- `templates/controller-manager.yaml` — upstream renders live (in host mode), sidecar via CR's `injectInitContainer` transformation
- `templates/managedresource.yaml` — operator applies remote resources directly, no MR wrapping
- `templates/webhook-config.yaml` — no longer needed; operator applies WebhookConfigs directly
- `templates/_webhook-injector-sidecar.tpl` — sidecar spec moves to CR
- `managedresources/crds-and-rbac.yaml` — upstream renders live in remote render
- `managedresources/` directory itself — content moves to `templates/` with `mode == "remote"` guard
- `values-overrides.yaml`, `values-managed-resources.yaml` — only needed by `make build-`; replaced by CR's `hostValues`/`remoteValues`

**Values structure change**. Chart's own `values.yaml` sets upstream subchart to all-disabled defaults; CR enables specific parts per mode via `hostValues` / `remoteValues`:

```yaml
# metal-operator-remote/values.yaml (chart defaults)
mode: ""    # required — set by operator per render, not by user

# Common defaults for our chart (image tags, resource limits, etc.)
webhookInjector:
  repository: keppel.global.cloud.sap/.../webhook-injector
  tag: latest

# Upstream subchart — everything OFF by default. CR turns things on per mode.
metal-operator-core:
  rbac:              {enable: false}
  crd:               {enable: false}
  webhook:           {enable: false}
  controllerManager: {enable: false}
```

The operator injects `.Values.mode` per render (values `{mode: "host"}` for host render, `{mode: "remote"}` for remote render). CR admission rejects any user attempt to set `values.mode` — mode is not user-configurable.

**Chart size**: 820 chart lines + 5542 baked lines = ~6360 → ~400 lines (a bit larger than a naive `.host/.remote/` split due to guards, but no baked YAML).

`_helpers.tpl` addition — stamps origin annotation on every resource in this chart:

```yaml
{{- /* helpers for the dual-deployment-operator pattern */ -}}

{{- define "dual.additionsAnnotation" -}}
annotations:
  dual-deployment-operator.cc.sap/origin: additions
{{- end }}

{{- define "dual.isHost" -}}{{ eq .Values.mode "host" }}{{- end }}
{{- define "dual.isRemote" -}}{{ eq .Values.mode "remote" }}{{- end }}
```

Templates use the guards:

```yaml
# templates/webhook-service.yaml
{{- if eq .Values.mode "host" }}
apiVersion: v1
kind: Service
metadata:
  name: metal-operator-remote-webhook-service
  namespace: {{ .Release.Namespace }}
  {{- include "dual.additionsAnnotation" . | nindent 2 }}
spec:
  # ...
{{- end }}
```

Note: no `target` annotation. The template's mode guard determines which render it appears in; that render goes entirely to its target cluster. No further routing decision needed.


### 4.2 Ipam-capi

**Before**:

```
system/ipam-capi-remote/            # helmify output, generated by make build-
├── (Helm chart with baked kustomize output)

system/kustomize/ipam-capi-remote/   # source of truth
├── manager/                         # upstream Deployment kustomize root
├── managedresources/                # upstream CRDs+RBAC kustomize root
├── webhooks/                        # upstream WebhookConfig kustomize root
├── templates/                       # our custom manifests (Helm-templated today)
├── netpol-labels.yaml
└── values-override.yaml
```

**After** (two-render pattern with kustomize overlays):

```
# system/ipam-capi-remote/ is DELETED (was helmify output, no longer needed)

system/kustomize/ipam-capi-remote/   # sole source of truth, consumed by operator directly
├── host/                             # NEW: host-mode overlay
│   └── kustomization.yaml            # resources: [../manager, ../additions/host]
├── remote/                           # NEW: remote-mode overlay
│   └── kustomization.yaml            # resources: [../managedresources, ../webhooks, ../additions/remote]
├── manager/                          # upstream Deployment kustomize root (refs pinned)
│   └── kustomization.yaml            # references upstream at ?ref=v0.1.0
├── managedresources/                 # upstream CRDs+RBAC (refs pinned)
│   └── kustomization.yaml
├── webhooks/                         # upstream WebhookConfigs (refs pinned)
│   └── kustomization.yaml
├── additions/                        # NEW: our custom manifests as kustomize resources
│   ├── host/
│   │   ├── kustomization.yaml        # commonAnnotations: dual-deployment-operator.cc.sap/origin=additions
│   │   ├── webhook-service.yaml      # our webhook Service
│   │   ├── remote-kubeconfig-configmap.yaml
│   │   ├── network-policy.yaml
│   │   └── ...
│   └── remote/
│       ├── kustomization.yaml        # commonAnnotations: dual-deployment-operator.cc.sap/origin=additions
│       └── extra-rbac.yaml           # our custom RBAC that goes in the shoot
└── netpol-labels.yaml
```

**Changes**:
- Top-level directory removed; two mode-specific overlays (`host/`, `remote/`) at the root
- Our custom manifests split into `additions/host/` and `additions/remote/` subdirs, referenced by the corresponding mode overlay
- `additions/host/kustomization.yaml` and `additions/remote/kustomization.yaml` use `commonAnnotations` to stamp `dual-deployment-operator.cc.sap/origin: additions` on every resource
- Upstream refs in `manager/`, `managedresources/`, `webhooks/` kustomization.yaml files are pinned to specific tags/SHAs (they're currently unpinned — see §9.1)
- `_webhook-injector-sidecar.tpl` deleted (sidecar spec moves to CR's `injectInitContainer`)
- Helm-templated values in the old `templates/` are converted to plain kustomize resources (no `.Values.*` in kustomize source)

**Deleted**:
- `system/ipam-capi-remote/` (helmify output)
- `make build-ipam-capi-remote`
- Old `templates/` directory (Helm-templated manifests) — content moves to `additions/host/` and `additions/remote/` as plain kustomize resources
- `values-override.yaml` (Helm-values-style; not applicable in kustomize)


### 4.3 Makefile

**Deleted targets**:
- `build-metal-operator-remote`
- `build-boot-operator-remote`
- `build-argora-operator-remote`
- `build-khalkeon-remote`
- `build-ipam-capi-remote`

### 4.4 Total LOC change

| Chart | Before | After |
|---|---|---|
| metal-operator-remote | 820 chart + 5542 baked = 6362 | ~350 |
| boot-operator-remote | ~200 + 761 = ~961 | ~120 |
| argora-operator-remote | ~150 + 621 = ~771 | ~100 |
| khalkeon-remote | ~200 + 968 = ~1168 | ~130 |
| ipam-capi (kustomize source) | ~600 + generated chart | ~500 (cleaned up) |
| Makefile targets | ~90 lines | 0 |
| **Total** | **~9,350** | **~1,200** |

**~87% reduction** in artifact LOC.

---

## 5. Operator implementation

### 5.1 Scaffold

Kubebuilder v3, Go 1.22+. Repo structure:

```
dual-deployment-operator/
├── api/v1alpha1/
│   ├── zz_generated.deepcopy.go
│   ├── dualdeploymentoperator_types.go        # CRD types + status
│   └── groupversion_info.go
├── config/
│   ├── crd/
│   ├── manager/
│   ├── rbac/
│   └── default/
├── internal/
│   ├── controller/
│   │   └── dualdeploymentoperator_controller.go
│   ├── source/
│   │   ├── helm.go                             # Helm SDK renderer
│   │   ├── kustomize.go                        # krusty renderer
│   │   └── source.go
│   ├── transform/
│   │   ├── patch.go                            # strategic-merge + JSON Patch
│   │   ├── rewrite_webhook_url.go
│   │   ├── filter_kinds.go
│   │   ├── package_webhook_configs_for_injector.go   # cross-stream
│   │   └── transform.go
│   ├── deliver/
│   │   ├── apply.go                            # SSA against a Kubernetes client
│   │   ├── health.go                           # per-resource health computation
│   │   └── deliver.go
│   ├── clients/
│   │   ├── host.go                             # in-cluster client
│   │   └── shoot.go                            # kubeconfig-from-Secret client
│   ├── webhook/                                # validating admission webhook
│   │   ├── validator.go                        # webhook.CustomValidator implementation
│   │   ├── patch_validator.go                  # patch structural + content validation
│   │   └── suite_test.go                       # envtest-based webhook tests
│   └── manifest/
│       └── manifest.go
├── go.mod
├── main.go
├── Makefile
└── Dockerfile
```

### 5.2 Interfaces

```go
// Source renders manifests for a specific mode (host or remote).
type Source interface {
    Render(ctx context.Context, mode Mode) ([]Manifest, error)
}

type Mode string
const (
    ModeHost   Mode = "host"
    ModeRemote Mode = "remote"
)

// Two transformation interfaces, distinguished by scope.

// PerRenderTransformation operates on a single render's manifest stream.
// The reconciler applies it to each render independently.
type PerRenderTransformation interface {
    Type() string
    Apply(manifests []Manifest) ([]Manifest, error)
}

// CrossStreamTransformation operates on both renders together, potentially
// moving resources between them. Runs after all PerRenderTransformations.
type CrossStreamTransformation interface {
    Type() string
    ApplyCrossStream(host, remote []Manifest) (newHost, newRemote []Manifest, err error)
}

// transform.Group parses spec.Transformations and separates entries into
// the two categories based on the concrete type (packageWebhookConfigsForInjector
// is currently the only cross-stream type).
// Preserves declaration order within each category.

type Applier interface {
    Apply(ctx context.Context, resource Manifest) (Status, error)
    Delete(ctx context.Context, resource Manifest) error
}

type Manifest struct {
    Unstructured *unstructured.Unstructured
    Origin       Origin   // upstream | additions
    // No Target — target is implicit from which render produced it
}
```

### 5.3 Reconcile loop

```go
func (r *DualDeploymentOperatorReconciler) Reconcile(ctx, req) (ctrl.Result, error) {
    cr := &v1alpha1.DualDeploymentOperator{}
    if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Handle deletion
    if !cr.DeletionTimestamp.IsZero() {
        return r.reconcileDelete(ctx, cr)
    }

    // Add finalizer
    if !controllerutil.ContainsFinalizer(cr, FinalizerName) {
        controllerutil.AddFinalizer(cr, FinalizerName)
        return ctrl.Result{}, r.Update(ctx, cr)
    }

    // 1. Build source renderer
    src, err := source.From(cr.Spec.Source)
    if err != nil { return r.errStatus(ctx, cr, "InvalidSource", err) }

    // 2. Render TWICE — one per mode
    hostManifests, err := src.Render(ctx, source.ModeHost)
    if err != nil { return r.errStatus(ctx, cr, "HostRenderFailed", err) }

    remoteManifests, err := src.Render(ctx, source.ModeRemote)
    if err != nil { return r.errStatus(ctx, cr, "RemoteRenderFailed", err) }

    // 3. Group transformations by scope (per-render vs cross-stream)
    perRender, crossStream, err := transform.Group(cr.Spec.Transformations)
    if err != nil { return r.errStatus(ctx, cr, "InvalidTransformation", err) }

    // 4. Apply per-render transformations to each render independently.
    //    Same list runs on both; each transformation naturally affects
    //    only resources present in the render it runs on.
    for _, t := range perRender {
        hostManifests, err = t.Apply(hostManifests)
        if err != nil { return r.errStatus(ctx, cr, "HostTransformFailed", err) }
        remoteManifests, err = t.Apply(remoteManifests)
        if err != nil { return r.errStatus(ctx, cr, "RemoteTransformFailed", err) }
    }

    // 5. Apply cross-stream transformations. Each may move resources
    //    between renders, add resources, or remove them.
    for _, t := range crossStream {
        hostManifests, remoteManifests, err = t.ApplyCrossStream(hostManifests, remoteManifests)
        if err != nil { return r.errStatus(ctx, cr, "CrossStreamTransformFailed", err) }
    }

    // 6. Get clients
    hostApplier := r.HostApplier
    shootApplier, err := r.buildShootApplier(ctx, cr)
    if err != nil { return r.errStatus(ctx, cr, "ShootClientFailed", err) }

    // 7. Apply each render to its target cluster
    hostStatus := r.applyAll(ctx, hostApplier, hostManifests)
    remoteStatus := r.applyAll(ctx, shootApplier, remoteManifests)

    // 8. Update inventory + status
    cr.Status.HostResources = hostStatus
    cr.Status.RemoteResources = remoteStatus
    cr.Status.Conditions = computeConditions(hostStatus, remoteStatus)
    cr.Status.LastReconcile = &metav1.Time{Time: time.Now()}

    if err := r.Status().Update(ctx, cr); err != nil {
        return ctrl.Result{}, err
    }

    return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}
```

**Key differences from a single-render architecture**:
- Source is rendered twice with mode-specific parameters (Helm: different values maps; kustomize: different overlay paths).
- No `split` step — each render's output is a coherent bucket for its target cluster.
- Per-render transformations apply to each render independently. `filterKinds {kinds: [Service]}` runs on both renders, dropping Services from whichever render emits them.
- Cross-stream transformations run in a separate phase after per-render. Currently one type: `packageWebhookConfigsForInjector` moves WebhookConfigurations from remote render into a ConfigMap in host render.
- Only `origin` matters for transformation targeting; there is no `target` on manifests (implicit from the render pipeline).

### 5.4 Shoot client construction

```go
func (r *Reconciler) getShootApplier(ctx context.Context, ref RemoteKubeconfigRef) (Applier, error) {
    secret := &corev1.Secret{}
    if err := r.Get(ctx, types.NamespacedName{Namespace: r.cr.Namespace, Name: ref.SecretName}, secret); err != nil {
        return nil, fmt.Errorf("shoot kubeconfig secret not found: %w", err)
    }
    kubeconfigBytes := secret.Data[ref.Key]
    config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
    if err != nil {
        return nil, err
    }
    client, err := client.New(config, client.Options{})
    if err != nil {
        return nil, err
    }
    return &SSAApplier{Client: client, FieldManager: "dual-deployment-operator"}, nil
}
```

The kubeconfig Secret is provisioned by Gardener's token-requestor (existing mechanism). Token rotation updates the Secret contents; operator picks up new tokens on next reconcile (or via a Secret informer trigger).

### 5.5 Cleanup on deletion

```go
func (r *Reconciler) reconcileDelete(ctx, cr) (ctrl.Result, error) {
    hostApplier := r.HostApplier
    shootApplier, _ := r.getShootApplier(ctx, cr.Spec.RemoteKubeconfig)

    // Host: ownerReferences cascade, but explicit delete top-level owners for determinism
    for _, m := range cr.Status.HostResources {
        _ = hostApplier.Delete(ctx, m.AsManifest())
    }

    // Remote: explicit delete for each, respecting deletionPolicy for CRDs
    for _, m := range cr.Status.RemoteResources {
        if m.Kind == "CustomResourceDefinition" && cr.Spec.DeletionPolicy.CRDs == "Retain" {
            continue
        }
        _ = shootApplier.Delete(ctx, m.AsManifest())
    }

    controllerutil.RemoveFinalizer(cr, FinalizerName)
    return ctrl.Result{}, r.Update(ctx, cr)
}
```

### 5.6 Dependencies

- `helm.sh/helm/v3` — Helm SDK
- `sigs.k8s.io/kustomize/api` — kustomize (krusty)
- `sigs.k8s.io/controller-runtime` — controller-runtime
- `k8s.io/client-go` — SSA and dynamic client

No dependency on Gardener CRDs (no more ManagedResource emission). Operator is Gardener-agnostic at the Kubernetes API level; it just consumes a kubeconfig Secret that Gardener provides.

### 5.7 Deployment topology

**Per-shoot**. One `dual-deployment-operator` Deployment per shoot-cp namespace, watching CRs in its own namespace only.

Deployed via a per-shoot Flux HelmRelease as part of the shoot's control-plane bootstrap (alongside the existing operator deployments). RBAC is namespace-scoped:

- Watch: `DualDeploymentOperator` in namespace
- Read: Secret with kubeconfig in namespace
- Apply: any resource in namespace (host) + any resource in shoot cluster (remote, via mounted kubeconfig)

No cross-namespace RBAC. No cluster-wide seed RBAC. Each operator instance is scoped to its shoot.

**Multiplication factor**: one instance per shoot. For ~50 shoots × 1 operator = 50 instances. Each is tiny (~200Mi RAM, minimal CPU). Total footprint comparable to the current webhook-injector sidecar count (already per-shoot × per-operator).

### 5.8 Testing

Three layers:

1. **Unit tests** per transformation, table-driven with fixture manifests
2. **Integration tests** with `envtest`:
   - Real CRD, real reconciler
   - Fake source yields fixture manifests
   - Verify: two-render correctness (host render produces host resources, remote render produces remote resources), apply to (mock) both clients, deletion cascade, drift correction via re-reconcile
3. **Equivalence tests** vs. today's charts:
   - Test fixture per operator × representative shoot
   - Render today's chart with `helm template` → capture full manifest stream (host + remote)
   - Render new operator (mocked apply layer captures manifests + target destination) → assert byte-identical manifest streams

Additionally:
4. **Drift tests**: apply resource, mutate externally, verify next reconcile re-applies
5. **Disjoint-write tests**: verify the operator's remote render contains no ValidatingWebhookConfiguration or MutatingWebhookConfiguration after `packageWebhookConfigsForInjector` runs (they are removed from the operator's apply set and packaged into the host ConfigMap), so the operator and injector never write the same shoot resource
6. **Deletion tests**: verify finalizer cleanup on host + remote, verify CRDs retained by default

---

## 6. Migration plan

Eight phases:

### Phase 1: Build operator (~2-3 weeks)

- Scaffold kubebuilder
- Implement source (Helm + kustomize) with two-render Render(ctx, mode) interface
- Implement 5 transformations
- Implement dual-cluster SSA + drift + health + deletion
- Write unit + integration tests
- Write equivalence tests for metal-operator

**Success criterion**: Equivalence test passes for metal-operator against a QA shoot's expected output; drift + health + deletion tests pass; two-render pipeline produces disjoint host/remote manifest sets.

### Phase 2: Restructure metal-operator-remote (~1 week)

- Add `.Values.mode` guards to templates (`{{ if eq .Values.mode "host" }}` / `remote`)
- Move managedresources/*.yaml content to templates/ with `mode: remote` guard
- Update chart values.yaml: upstream subchart defaults all-disabled (CR turns on per mode); add `mode: ""` placeholder
- Update `_helpers.tpl` to stamp `dual-deployment-operator.cc.sap/origin: additions` on every resource
- Delete `webhooks.yaml`, `controller-manager.yaml`, `managedresource.yaml`, `webhook-config.yaml`, `_webhook-injector-sidecar.tpl`, `managedresources/`, `values-overrides.yaml`, `values-managed-resources.yaml`
- Delete `make build-metal-operator-remote`
- Chart to `0.7.0` (breaking bump)

**Success criterion**: `helm template metal-operator-remote --values <shoot-values>` produces the expected upstream + additions stream (verified via equivalence test).

### Phase 3: Verify webhook-injector ConfigMap contract (~1-2 days)

The injector requires **no code change** under this design — it keeps its existing `--webhook-config-name` source-ConfigMap contract and its `Create`/`Update` delivery. This phase only verifies the contract:

- Confirm the operator's `packageWebhookConfigsForInjector` ConfigMap (name, `webhooks.yaml` data key, YAML shape) exactly matches what the injector's `--webhook-config-name` reader expects
- Confirm the injector and operator write disjoint resource sets on the shoot (operator: CRDs/RBAC/SA/additions; injector: WebhookConfigurations) — no shared resource, so no field-ownership coordination to test
- Resolve §9.2 (CRD conversion-webhook caBundle) before migrating any operator whose CRDs carry a conversion webhook (metal-operator)

**Success criterion**: injector consumes the operator-produced ConfigMap unchanged; WebhookConfigurations appear on the shoot with a valid caBundle; operator and injector touch no common resource. §9.2 resolved for metal-operator.

### Phase 4: Deploy operator to one QA shoot (~1 week)

- Deploy operator to `a-qa-de-200`'s shoot-cp namespace
- Create `DualDeploymentOperator` CR for metal-operator
- Verify operator reconciles; both host and remote resources are applied
- Diff current state vs. operator-produced state — must be identical modulo transient fields
- Monitor for 48h — verify drift correction, health tracking, no injector conflicts

**Success criterion**: metal-operator running in QA via the operator, functionally equivalent to today's HelmRelease + injector setup.

### Phase 5: Migrate metal-operator across production seeds (~1-2 weeks)

- For each production seed running metal-operator: deploy operator to shoot-cp namespaces, replace HelmRelease with CR
- Monitor for drift alerts, webhook failures
- Old wrapper chart HelmReleases decommissioned after 1 week of clean operation

**Success criterion**: All production metal-operator instances migrated.

### Phase 6: Repeat for boot / argora / khalkeon (~1 week each)

Same pattern per operator. Can parallelize.

### Phase 7: Migrate ipam-capi (~2 weeks)

- Restructure `system/kustomize/ipam-capi-remote/` (top-level kustomization, plain kustomize resources)
- Resolve egress question for kustomize source pull
- Delete `system/ipam-capi-remote/` (helmify output)
- Delete `make build-ipam-capi-remote`
- Deploy CR
- Verify equivalence

**Success criterion**: ipam-capi migrated.

### Phase 8: Cleanup + docs

- Delete `system/*-remote/` old wrapper chart artifacts from git
- Delete `system/Makefile` obsolete targets
- Publish onboarding doc

---

## 7. Verification

### 7.1 Unit tests

Per transformation, table-driven over fixtures.

### 7.2 Integration tests

`envtest`-based with fake source. Verify:
- CR reconcile applies host + remote resources
- Deletion cascades correctly per policy
- Transformation errors surface in CR status
- Drift correction re-applies mutated resources
- Operator's remote apply set excludes WebhookConfigurations (they are packaged for the injector, not applied by the operator), so operator and injector write disjoint shoot resources

### 7.3 Equivalence tests

For each of five operators:

1. Fixture: CR + values for a representative shoot
2. Render today's chart + capture applied state (helm template + Flux dry-run)
3. Render new operator + capture manifest streams (mock apply layer)
4. Assert: streams equivalent (modulo canonical ordering, comment/whitespace, chart-provenance labels)

Equivalence tests run in CI on every operator PR.

### 7.4 In-cluster equivalence

Before Phase 4 rollout, on a QA shoot:

1. Snapshot current applied state for target operator
2. Deploy operator with matching CR
3. Diff operator-applied state vs. snapshot — must match

### 7.5 Drift + health verification

- Manually mutate a live resource (e.g., delete a label from CRD, change replica count on Deployment)
- Verify operator's next reconcile re-applies
- Verify CR status reflects Progressing → Healthy after re-apply

### 7.6 Operator/injector separation on QA

- Deploy operator + injector on QA
- Verify the operator produces the injector's source ConfigMap (`packageWebhookConfigsForInjector`) in the expected name/shape
- Verify the injector reads that ConfigMap and applies WebhookConfigurations to the shoot with a valid caBundle
- Verify the operator's remote apply set contains no WebhookConfigurations (operator and injector touch disjoint shoot resources)
- Verify §9.2 (CRD conversion-webhook caBundle) is resolved for metal-operator before relying on conversion webhooks

---

## 8. Rollback plan

Per-shoot rollback:

1. Delete `DualDeploymentOperator` CR
2. Operator handles cleanup (respecting deletionPolicy for CRDs — default Retain, so shoot CRDs preserved)
3. Redeploy old HelmRelease with today's wrapper chart
4. State converges within one Flux + injector reconcile cycle

Rollback window: Phase 4-7 (before old wrapper chart artifacts are deleted). After Phase 8, rollback requires git-revert to restore chart files.

Risk mitigation: postpone Phase 8 until 2 weeks of clean operation post-Phase 7.

---

## 9. Open questions

### 9.1 Egress from seeds to kustomize sources

For ipam-capi migration: operator needs to fetch kustomize source. Currently references `github.com/sapcc/helm-charts` and transitively `github.com/kubernetes-sigs`. Seed network policies may block this.

**Assumed for design**: github.com egress allowed. Fallback options if not:
- Mirror kustomize sources to keppel-hosted git
- Bundle as OCI kustomization artifacts in keppel
- Package as ConfigMap read by operator

To be resolved before Phase 7.

### 9.2 CRD conversion-webhook caBundle management

The webhook-injector's only path for stamping caBundle into a CRD's `.spec.conversion.webhook.clientConfig.caBundle` is its `ManagedResourceReconciler`: it selects Gardener `ManagedResource` objects on the seed by `--managed-resource-label`, walks their referenced Secrets, finds embedded CRDs with `conversion.strategy=Webhook`, and stamps caBundle. This design produces **no** `ManagedResource` objects (GRM elimination is a core goal, §2.2.1), so that path never fires for CRDs delivered by this operator.

Result: for an operator whose CRDs carry a conversion webhook (metal-operator today), caBundle on those CRDs is **not managed** under this design as written. The operator must NOT take this on — doing the injector's caBundle work is out of scope (operator/injector separation, §3.8).

Candidate resolutions (decision required before migrating any conversion-webhook operator):
- **A. Emit a minimal ManagedResource for CRDs only.** Wrap the remote CRDs in a `ManagedResource` carrying `--managed-resource-label`, keeping the injector's existing CRD-stamping path. Reintroduces GRM for one resource class — a targeted, documented exception to the GRM-elimination goal, not a general regression.
- **B. Extend the injector with a ConfigMap-driven CRD-stamping mode.** Teach the injector to stamp caBundle into CRDs named in a ConfigMap (analogous to its WebhookConfig path), so the operator can feed it via a cross-stream transformation without ManagedResources. Requires an injector code change.
- **C. Confirm no target operator actually needs it.** Verify whether metal-operator's CRDs genuinely require conversion-webhook caBundle in the remote (virtual) cluster. If conversion webhooks are unused there, the whole concern is moot.

To be resolved before Phase 5 (metal-operator production migration). Does not block the scaffold/CRD-types change.

### 9.3 Per-CR transformation config duplication

`injectInitContainer.container` is ~30 lines. If duplicated across all shoots for the same operator, this is verbose.

Options:
- Accept duplication (explicit, override-friendly)
- Reference from a ConfigMap: `injectInitContainer: {from: {configMap: sidecar-spec}}`
- Provide as chart value default; CR overrides

Deferred to Phase 1.

### 9.4 Kustomize source parameterization for ipam-capi

`spec.source.kustomize` has `url` + `hostPath` + `remotePath`, but no equivalent of Helm's `values` map (kustomize has no Helm-values equivalent — see §3.3). Per-cluster parameterization for kustomize sources must come from elsewhere.

Open question for ipam-capi's specific needs: today's `make build-ipam-capi-remote` sets `.controllerManager.manager.image.tag=$(PROVIDER_IPAM_VERSION)` on the generated Helm chart's values. Under the new design (two-render + direct kustomize), this variability must come from somewhere:

- **Option A**: image tag is baked into the kustomize source's `images:` transformer at a pinned ref; different environments use different refs of the kustomize source (via CR's `spec.source.kustomize.url?ref=...`)
- **Option B**: operator gains a `setImageTag` transformation type (currently listed as a candidate future transformation in §3.4.6); CR carries the tag override
- **Option C**: add CR-side `spec.source.kustomize.images` and/or `spec.source.kustomize.patches` — kustomize-native overrides that the operator applies to both renders before krusty build

Deferred to Phase 7. Preference is A if upstream release cadence aligns with kustomize ref bumps, otherwise C (kustomize-native, additive schema).

### 9.5 CRD versioning strategy

v1alpha1 for initial release. Progression:
- v1alpha1 → v1beta1 after 3 operators migrated and stable
- v1beta1 → v1 after all 5 migrated for 6+ months

### 9.6 CR reconciliation trigger scope

Beyond spec/status changes and periodic re-sync (10min), watch:
- Applied resources on both clients (drift detection via informer)
- Kubeconfig Secret in CR namespace (token rotation)

Confirm scope during Phase 1.

### 9.7 Operator's own deployment

Operator is a Deployment in shoot-cp namespace. Deployed by:
- Same Flux mechanism as other seed-shoot workloads (per-shoot HelmRelease)
- No self-management in v1 (avoid chicken-and-egg during upgrades)

### 9.8 Deletion policy default for CRDs

CRDs on shoot are retained by default (`spec.deletionPolicy.crds: Retain`) to avoid catastrophic data loss on CR delete. Confirm this default is right, or make explicit configuration required.

### 9.9 Validating admission webhook for `patch` content

**Planned for v2, deferred from v1**.

Under v1, CRD OpenAPI schema validates the shape of `PatchSpec` (`strategicMerge` is an object, `jsonPatch` is an array of ops with valid enum values), but the **inner content** of `strategicMerge` and `jsonPatch[].value` is marked `x-kubernetes-preserve-unknown-fields: true` — Kubernetes doesn't validate it against any schema. Content-level errors (wrong field types inside the patch, unknown fields on the target kind) surface at reconcile time.

A validating admission webhook would parse the patch content against the target kind's OpenAPI schema at admission time. Would catch:
- Type errors inside strategic-merge patches (e.g., `spec.replicas: "three"` when integer expected)
- Unknown field references in `jsonPatch` `path` values (e.g., `/spec/repliacs` typo)
- Structural violations that CRD schema can't express

**Why not v1**:
- Webhook infrastructure adds cert management, webhook Service, ValidatingWebhookConfiguration
- Per-shoot deployment topology means either one webhook per operator instance (many endpoints) or a centralized webhook (extra deployment)
- Runtime reconcile is fast enough that content errors surface within seconds — acceptable delay for v1

**What v1 provides**:
- Structural admission validation (typed variant selection, `jsonPatch.op` enum, required fields, object-vs-array shape checking)
- Runtime validation with CR status conditions surfacing errors
- Operator's `internal/webhook/` package is scaffolded in v1 (via `kubebuilder create webhook`) but not wired into a running webhook — deferred to v2 when we implement content validation

**v2 implementation** (deferred):
- Register operator as a `ValidatingWebhookConfiguration` targeting `DualDeploymentOperator` CRs
- Webhook handler parses `patch.strategicMerge` / `patch.jsonPatch` and validates against target kind's OpenAPI schema (fetched from the API server's OpenAPI endpoint at operator startup)
- Reject CRs with content-level errors at admission
- Cert management: cert-manager or a self-signed cert rotated by the operator itself

Track in v2 planning issue.

---

## 10. Glossary

- **Host** — the seed cluster; where the operator Pod runs
- **Remote** — the virtual (shoot) cluster; workerless, holds domain CRDs and CRs
- **Additions** — our custom manifests that don't come from upstream (Services, Ingress, NetPol, kubeconfig, injector RBAC, etc.)
- **Upstream** — the operator's original Helm chart or kustomize source (e.g., `ironcore-dev/metal-operator`)
- **Wrapper chart** — historical name for `system/<operator>-remote/` Helm charts. Under this design, only helm-upstream operators have one.
- **SSA** — Server-Side Apply, Kubernetes API primitive for declarative apply with field ownership
- **Field manager** — identifier for the client that owns a set of fields in a resource (per-field granularity via SSA)
- **webhook-injector** — companion controller ([SAP-cloud-infrastructure/webhook-injector](https://github.com/SAP-cloud-infrastructure/webhook-injector)) that manages the TLS cert lifecycle and delivers WebhookConfigurations to the shoot from a source ConfigMap (`--webhook-config-name`), injecting the current caBundle. Runs unchanged under this design: the operator produces the source ConfigMap (via `packageWebhookConfigsForInjector`) instead of the chart baking it, but the injector's contract, delivery, and caBundle handling are unmodified. The operator does not deliver WebhookConfigurations, stamp caBundle, or manage certs. (CRD conversion-webhook caBundle: see §9.2 open limitation.)
- **Transformation** — a typed Go struct implementing `Apply(manifests) → manifests`, opt-in per CR. Applied to each render independently under the two-render pattern.
- **Source discriminator** — the `spec.source.{helm,kustomize}` choice in the CR. Each source discriminator has its own nested fields (Helm has `values`/`hostValues`/`remoteValues`; kustomize has `url`/`hostPath`/`remotePath`).
- **Two-render pattern** — the operator renders the source twice per reconcile, once per mode, producing disjoint host and remote manifest sets. See §3.5.
- **Mode** — one of `host` or `remote`. For Helm sources, injected as `.Values.mode`. For kustomize, selected via `hostPath` / `remotePath`.
- **Origin annotation** — `dual-deployment-operator.cc.sap/origin: {upstream|additions}` on a manifest. Chart authors stamp `additions` on their own templates via `_helpers.tpl` (Helm) or `commonAnnotations` (kustomize). Operator tags unlabeled manifests as `upstream`.
- **Gardener token-requestor** — Gardener component that provisions shoot API tokens as Secrets in the seed
- **GRM** — gardener-resource-manager; per-shoot Gardener component that reconciles `ManagedResource` objects. Not used under this design (operator applies directly).

---

*Design doc revision 6 — hybrid patch DSL adopted; renameKind removed. Predecessor revisions: r5 (typed-only with 6 transformations), r4 (two-render pattern), r3 (single-render + split, superseded), r2 (MR-based remote delivery, superseded), r1 (two-chart CR, superseded). All archived in git history.*
