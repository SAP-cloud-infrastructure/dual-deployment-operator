# dual-deployment-operator — Design Document

**Status:** Proposal
**Tracking issue:** [cc/unified-kubernetes#1294](https://github.wdf.sap.corp/cc/unified-kubernetes/issues/1294)
**Predecessor POC:** [sapcc/helm-charts#11633](https://github.com/sapcc/helm-charts/pull/11633) (kustomize-based, archived)
**Original problem:** [cc/unified-kubernetes#1169](https://github.wdf.sap.corp/cc/unified-kubernetes/issues/1169)

---

## TL;DR

A kubebuilder operator (`dual-deployment-operator`) that pulls **one artifact per operator** (Helm chart or kustomize source) from `sapcc/helm-charts`, renders it, applies typed Go transformations, splits the manifest stream by target cluster, and **applies each half directly to its target Kubernetes API** — host resources to the seed via the operator's own service account, remote resources to the shoot via a Gardener-provided kubeconfig.

Key characteristics:

- **One artifact per operator, self-contained.** No wrapper + additions split.
- **Operator supports both Helm and kustomize** via a CR source discriminator (`spec.source.helm` xor `spec.source.kustomize`).
- **Bounded transformation menu:** 5 typed Go transforms (`injectInitContainer`, `renameKind`, `rewriteWebhookURL`, `addLabels`, `filterKinds`). No DSL.
- **Dual-cluster direct apply** — operator holds two Kubernetes clients per CR, applies host + remote uniformly with server-side apply + drift correction + per-resource health tracking. No `ManagedResource` wrapping.
- **webhook-injector retained** for its unique role (TLS cert generation + rotation + caBundle patching on shoot). Coexists cleanly with operator via SSA field managers.
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
   - Label CRDs (metal-operator only) with `metal-operator-remote-webhook-injector: "true"` so a companion webhook-injector controller manages their caBundles
   - Inject the webhook-injector sidecar as an `initContainer` into upstream's Deployment (metal + ipam-capi)
4. Commits the transformed YAML into the chart: `managedresources/*.yaml`, `webhooks.yaml`, `templates/controller-manager.yaml`
5. Delivers remote content via **three separate paths**:
   - **CRDs, RBAC, ServiceAccount**: `templates/managedresource.yaml` (chart Helm template) wraps each pre-baked YAML doc as a `ManagedResource + Secret` pair. Gardener's `gardener-resource-manager` (GRM) in each shoot's control plane picks up the MR and applies to the shoot.
   - **WebhookConfigurations**: `webhooks.yaml` (pre-rendered with URL rewrite) is packed into a `webhook-config` ConfigMap on the host via `.Files.Get`. The **webhook-injector sidecar** (mounted alongside each operator Pod) reads this ConfigMap and applies the WebhookConfigurations to the shoot cluster via a mounted shoot kubeconfig.
   - **caBundle rotation**: webhook-injector also generates and rotates TLS certs, storing them in a seed-side Secret and patching `.webhooks[].clientConfig.caBundle` on the shoot's WebhookConfigurations (and on CRDs with conversion webhooks) whenever certs rotate.

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
| **C. Option 2 (chosen)** | Operator directly (SSA) | Operator directly (second Kubernetes client) | **Operator directly** | webhook-injector unchanged (patches caBundle only; no longer delivers WebhookConfigs) |
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

- ✅ **Single remote-delivery path**: operator applies every remote resource (CRD, RBAC, WebhookConfiguration, etc.) directly via one Kubernetes client
- ✅ Chart emits **no delivery-shaped resources** — no MR wrapper template, no `webhook-config` ConfigMap, no `.Files.Get` indirection. Chart is purely a manifest source; operator does all delivery.
- ✅ Drift correction and health tracking apply uniformly to host and remote via the same operator machinery (which we build anyway for host)
- ✅ Deployment topology becomes crisper: one operator per shoot-cp namespace with two Kubernetes clients (seed + shoot)
- ✅ webhook-injector's scope shrinks to its actual specialty (cert generation, rotation, caBundle patching) — cleaner separation of concerns
- ✅ CR status becomes the single source of truth for remote state — no need to inspect MR status or injector ConfigMap contents
- ⚠️ Departs from Gardener MR/GRM idiom for remote delivery (but the webhook-injector already deviates, so we're not introducing a new deviation, just accepting the direct-apply pattern for CRDs/RBAC too)
- ⚠️ Requires the operator to hold a shoot kubeconfig — same credential model as the webhook-injector today, via Gardener token-requestor. No new mechanism.
- ⚠️ Requires SSA field-manager discipline between operator and webhook-injector on WebhookConfigurations (operator owns everything except `.webhooks[].clientConfig.caBundle`; injector owns caBundle). Standard SSA co-ownership pattern.

**Preferred over Option 1 because**:

| Aspect | Option 1 | Option 2 |
|---|---|---|
| Remote delivery paths | 2 (operator + injector) | 1 (operator only) |
| Chart complexity | Still has `webhook-config` ConfigMap contract with injector | Chart emits pure manifest resources; no delivery contracts |
| WebhookConfig update flow | Chart → ConfigMap → injector reads → injector applies | Chart → operator applies directly |
| Injector's role | Full delivery + certs + caBundle | Certs + caBundle only (delivery removed) |
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
        Helm SDK Pull              Krusty (kustomize)
        (charts, OCI/HTTP)         (kustomize sources)
              │                           │
              └─────────────┬─────────────┘
                            │
                    Rendered manifest stream
                            │
                            ▼
              Apply transformations in order
              (typed Go menu, per-CR opt-in)
                            │
                            ▼
              Split by target (host / remote / drop)
              (kind rules + optional annotation)
                            │
              ┌─────────────┴─────────────┐
              ▼                           ▼
      seed Kubernetes client       shoot Kubernetes client
      (in-cluster SA)              (kubeconfig from Gardener
              │                     token-requestor Secret)
              │                           │
              ▼                           ▼
   Server-side apply to seed    Server-side apply to shoot
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

**Coexistence with webhook-injector** (unchanged from today's role, but with reduced scope):

```
webhook-injector (sidecar of each operator Pod, per shoot)
     │
     ├── Watches cert Secret in seed
     ├── Generates + rotates TLS certs (writes to cert Secret)
     └── Applies caBundle updates to shoot's WebhookConfigurations
         and CRDs (via mounted shoot kubeconfig, SSA with its own
         field manager — coexists with operator writes)
```

The operator and injector write to overlapping resources (WebhookConfigurations) but distinct fields (operator owns everything except caBundle; injector owns caBundle). Server-side apply's field-ownership model handles this cleanly.

### 3.2 Custom Resource

Full example (metal-operator):

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
      values:
        metal-operator-core:            # upstream sub-chart values pass-through
          controllerManager:
            manager:
              args: [--mac-prefixes-file=/etc/macdb/macdb.yaml, ...]
              env: {KUBECONFIG: /var/run/remote-kubeconfig/kubeconfig, ...}
        macdb: {...}
        apiserverURL: "api.m-eu-de-1.cp..."
        webhookInjector: {image: "keppel.../webhook-injector", tag: "sha-..."}

  # Kubeconfig for the shoot cluster.
  # Mounted from a Gardener token-requestor-managed Secret in this namespace.
  remoteKubeconfig:
    secretName: <operator>-remote-kubeconfig
    key: kubeconfig

  transformations:
    - injectInitContainer:
        selector: {kind: Deployment, name: metal-operator-controller-manager}
        container:
          name: webhook-injector
          image: "keppel.global.cloud.sap/.../webhook-injector:sha-fd8a075..."
          args: [--webhook-config-name=metal-operator-remote-webhook-config]
          volumeMounts:
            - {name: webhook-certs, mountPath: /tmp/k8s-webhook-server/serving-certs, readOnly: true}
        additionalVolumes:
          - {name: webhook-certs, emptyDir: {}}

    - renameKind:
        from: Role
        to: ClusterRole
        source: upstream

    - rewriteWebhookURL:
        urlPrefix: "https://metal-operator-remote-webhook-service:443"

    - addLabels:
        selector: {kind: CustomResourceDefinition}
        labels:
          metal-operator-remote-webhook-injector: "true"

    - filterKinds:
        kinds: [Service]
        source: upstream

status:
  # Populated by the operator on each reconcile.
  hostResources:
    - {kind: Deployment, name: metal-operator-controller-manager, status: Healthy, lastApplied: ...}
    - ...
  remoteResources:
    - {kind: CustomResourceDefinition, name: endpoints.metal.ironcore.dev, status: Healthy, lastApplied: ...}
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
    - injectInitContainer: {...}
    - renameKind: {from: Role, to: ClusterRole, source: upstream}
    - rewriteWebhookURL: {urlPrefix: "https://ipam-capi-remote-webhook-service:443"}
    - filterKinds: {kinds: [Service], source: upstream}
```

For simpler operators (boot / argora / khalkeon):

```yaml
spec:
  source:
    helm: {repo, name, version, values}
  remoteKubeconfig: {secretName, key}
  transformations:
    - renameKind: {from: Role, to: ClusterRole, source: upstream}
    - filterKinds: {kinds: [Service], source: upstream}
```

The CR is the entire configuration surface. Per-cluster differences (apiserver URL, mac database, image tags) live in `spec.source.*.values`. Per-chart transformation profile lives in `spec.transformations`. Shoot kubeconfig comes from Gardener's token-requestor per standard practice (see §3.6).

### 3.3 Source discriminator

The operator supports two source types, exactly one of which must be set:

**`spec.source.helm`**:
- `repo` — OCI or HTTP Helm repo URL
- `name` — chart name
- `version` — semver constraint (must resolve deterministically)
- `values` — passed to `helm template` as `-f values.yaml`

Implementation: `helm.sh/helm/v3`. Standard `Pull` + `Template` action.

**`spec.source.kustomize`**:
- `url` — kustomize root URL. Format: `https://github.com/{org}/{repo}//{path}?ref={sha|tag}`. `ref` is required — floating references are rejected at CR admission.
- `hostPath` — subpath under `url` for the host overlay root (required; no default). Must be set explicitly per CR.
- `remotePath` — subpath under `url` for the remote overlay root (required; no default). Must be set explicitly per CR.

Implementation: `sigs.k8s.io/kustomize/api/krusty`. Battle-tested Go library used by Flux and Argo CD.

Both produce a stream of unstructured Kubernetes manifests. The operator tags every fetched manifest with `dual-deployment-operator.cc.sap/origin: upstream`. Manifests coming from the wrapper's own templates (our custom stuff) are tagged `dual-deployment-operator.cc.sap/origin: additions` by the chart itself via `_helpers.tpl`.

### 3.4 Transformation menu

The operator has a bounded set of typed transformation types. Each is a Go struct with strict schema; no DSL, no scripting, no free-form expressions. Adding a new type requires an operator release.

Ordering matters. Transformations run top-to-bottom as declared. Convention:

1. Structural patches (`injectInitContainer`, `addLabels`) before renames
2. Renames (`renameKind`) before other transforms that depend on the renamed kind
3. Rewrites (`rewriteWebhookURL`) after all structural changes
4. Filters (`filterKinds`) last

#### 3.4.1 `injectInitContainer`

Prepends an init container and optionally adds volumes to matching PodSpec-carrying resources.

```yaml
- injectInitContainer:
    selector:
      kind: Deployment            # required; also StatefulSet, DaemonSet, Job, CronJob
      name: metal-operator-controller-manager
    container:                    # standard k8s Container object, validated
      name: webhook-injector
      image: "..."
      args: [...]
      env: [...]
      volumeMounts: [...]
      ports: [...]
      resources: {...}
    additionalVolumes: [...]      # standard k8s Volume objects
```

Idempotent. If no matching resource found, error.

#### 3.4.2 `renameKind`

Rewrites the `kind` field on matching resources. Handles Role→ClusterRole ref updates in binding kinds.

```yaml
- renameKind:
    from: Role
    to: ClusterRole
    source: upstream
```

For `RoleBinding` → `ClusterRoleBinding` (if requested), the transformation also updates `.roleRef.kind` in matching bindings and strips `.metadata.namespace`.

#### 3.4.3 `rewriteWebhookURL`

Rewrites `.webhooks[].clientConfig.service` → `.webhooks[].clientConfig.url` on `ValidatingWebhookConfiguration` and `MutatingWebhookConfiguration`.

```yaml
- rewriteWebhookURL:
    urlPrefix: "https://metal-operator-remote-webhook-service:443"
```

For each webhook whose `.clientConfig.service` is set: replace with `.clientConfig.url = urlPrefix + service.path`. Existing `.url` is left alone (idempotent). `.caBundle` is preserved.

#### 3.4.4 `addLabels`

Adds labels to matching resources' `metadata.labels`. Additive.

```yaml
- addLabels:
    selector:
      kind: CustomResourceDefinition
    labels:
      metal-operator-remote-webhook-injector: "true"
```

#### 3.4.5 `filterKinds`

Drops resources of listed kinds from the manifest stream (neither host nor remote — discarded).

```yaml
- filterKinds:
    kinds: [Service, ConfigMap]
    source: upstream
```

#### 3.4.6 Menu extensibility

New transformation types are added by operator releases. Not extensible at runtime. Candidate future types (not implemented in v1):

- `patch` — strategic-merge patch by selector (deferred to avoid DSL creep)
- `addAnnotations` — mirror of `addLabels`
- `setImageTag` — override container image tag by selector

### 3.5 Split rules

After transformations, the manifest stream is split into three buckets: `host`, `remote`, `drop`.

Precedence (first match wins):

1. **Explicit annotation** `dual-deployment-operator.cc.sap/target: {host|remote|drop}` on the resource → use that
2. **Kind-based defaults**:

| Kind | Bucket |
|---|---|
| `CustomResourceDefinition` | remote |
| `ValidatingWebhookConfiguration`, `MutatingWebhookConfiguration` | remote |
| `ClusterRole`, `ClusterRoleBinding` | remote |
| `Role`, `RoleBinding` | remote (should be rare after `renameKind`) |
| `ServiceAccount` with `origin: upstream` | remote |
| `ServiceAccount` with `origin: additions` (webhook-injector SA) | host |
| `Deployment`, `StatefulSet`, `DaemonSet` | host |
| `Service`, `ConfigMap`, `Secret`, `Ingress`, `NetworkPolicy` | host |
| `Namespace` | either — must have annotation |
| anything else | host (default; log warning to prompt annotation) |

Chart authors override with the target annotation per-resource. The annotation is stripped by the operator before applying — it's a routing hint, not persisted state.

### 3.6 Delivery

Operator holds two Kubernetes clients per CR:

- **Host client**: seed cluster in-cluster access via the operator's ServiceAccount (RBAC scoped to the CR's namespace + relevant cluster-scoped resources for CRs that need them, though none should — we deliver cluster-scoped resources only to shoots)
- **Shoot client**: constructed from a kubeconfig held in a Secret in the CR's namespace. `spec.remoteKubeconfig.secretName` points at this Secret (typically named `<operator>-remote-kubeconfig`, provisioned by Gardener's token-requestor as today, mounted in the existing operator Pod as `/var/run/remote-kubeconfig/kubeconfig`)

For each resource in the `host` bucket → apply to host client.
For each resource in the `remote` bucket → apply to shoot client.

#### 3.6.1 Server-side apply

All resources are applied with server-side apply (`k8s.io/client-go` `Apply` verb), field manager `dual-deployment-operator`. This provides:

- **Idempotency**: byte-identical inputs produce byte-identical apply calls with no-op semantics if state is already correct
- **Field-level ownership**: SSA field managers make multi-writer scenarios (with webhook-injector) safe — see §3.8
- **Conflict handling**: on field conflicts with other managers, operator uses `force: true` for fields it owns

#### 3.6.2 Drift correction

Every reconcile (default: every 10 minutes plus event-driven), the operator re-applies every resource to its target cluster. Because SSA is idempotent, a resource unchanged since the last apply produces no writes.

If a resource has been mutated externally (drift), the next reconcile re-applies the operator's version. Field ownership prevents this from clobbering fields owned by other managers (e.g., caBundle owned by webhook-injector).

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

The webhook-injector runs as a sidecar of each operator Pod (unchanged). Under Option 2 its responsibilities are:

1. **Watch cert Secret** in the operator's namespace
2. **Generate initial TLS certs** and populate the cert Secret
3. **Rotate certs** on expiration with configured overlap window
4. **Patch caBundle** on the shoot's `ValidatingWebhookConfiguration`, `MutatingWebhookConfiguration`, and CRDs (that have `.spec.conversion.webhook.clientConfig`) whenever certs rotate

It **does not**:
- Read WebhookConfiguration definitions from a ConfigMap
- Apply WebhookConfigurations to the shoot (operator does this now)

The chart no longer emits the `webhook-config` ConfigMap. Injector's `--webhook-config-name` argument becomes unused; injector code path for reading from ConfigMap is disabled or removed.

**Field ownership on shared resources**:

Operator's SSA field manager: `dual-deployment-operator`
Injector's SSA field manager: `webhook-injector` (or its existing name; verify)

On a `ValidatingWebhookConfiguration`:
- Operator owns: all fields **except** `.webhooks[*].clientConfig.caBundle`
- Injector owns: `.webhooks[*].clientConfig.caBundle` only

SSA respects field ownership. Operator's re-applies (in reconcile) that omit caBundle (or set caBundle to empty) don't clobber injector's writes to caBundle. Injector's writes to caBundle don't clobber operator's writes to other fields.

Requires: injector applies with a stable, distinct field manager name. If injector currently doesn't set a field manager explicitly, add one. Small injector change.

**During cert rotation**: injector updates caBundle; operator's next drift-correction re-apply notices no drift on fields it owns, so no ping-pong.

**Ordering during initial deploy**: operator applies WebhookConfig first (with no caBundle); injector observes and patches caBundle. Between the two applies, webhooks are inert (calls fail because caBundle is empty) — normal bootstrap behavior, same as today.

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

**After**:

```
system/metal-operator-remote/
├── Chart.yaml                                # dep: metal-operator (ENABLED at install)
├── Chart.lock
├── charts/metal-operator-*.tgz               # cached upstream
├── values.yaml                                # upstream ENABLED, our stuff
└── templates/
    ├── _helpers.tpl                          # stamps target + origin annotations
    ├── host/
    │   ├── ingress.yaml
    │   ├── macdb.yaml
    │   ├── metal-registry-service.yaml
    │   ├── networkpolicy.yaml
    │   ├── remote-kubeconfig-configmap.yaml
    │   ├── remote-kubeconfig.yaml
    │   ├── rotate-kubeconfig.yaml
    │   ├── webhook-injector-rbac.yaml
    │   └── webhook-service.yaml
    └── remote/
        └── namespace.yaml                    # + any extra RBAC our chart adds in remote
```

**Deleted**:
- `webhooks.yaml` — upstream renders live via Helm dep
- `templates/controller-manager.yaml` — upstream renders live, sidecar via operator transformation
- `templates/managedresource.yaml` — operator applies remote resources directly
- `templates/webhook-config.yaml` — no longer needed; operator applies WebhookConfigs directly
- `templates/_webhook-injector-sidecar.tpl` — sidecar spec moves to CR
- `managedresources/crds-and-rbac.yaml` — upstream renders live
- `values-overrides.yaml`, `values-managed-resources.yaml` — only for `make build-`

**Values inversion**: upstream sub-chart values change from all-disabled to all-enabled:

```yaml
# before
metal-operator-core:
  rbac: {enable: false}
  crd: {enable: false}
  webhook: {enable: false}
  controllerManager: {enable: false}

# after
metal-operator-core:
  rbac: {enable: true}
  crd: {enable: true}
  webhook: {enable: true}
  controllerManager: {enable: true}
```

**Chart size**: 820 chart lines + 5542 baked lines = ~6360 → ~350 lines.

`_helpers.tpl` addition:

```yaml
{{- define "dual.hostAnnotations" -}}
dual-deployment-operator.cc.sap/target: host
dual-deployment-operator.cc.sap/origin: additions
{{- end }}

{{- define "dual.remoteAnnotations" -}}
dual-deployment-operator.cc.sap/target: remote
dual-deployment-operator.cc.sap/origin: additions
{{- end }}
```

### 4.2 Ipam-capi

**Before**:

```
system/ipam-capi-remote/            # helmify output, generated
├── (Helm chart with baked kustomize output)

system/kustomize/ipam-capi-remote/   # source of truth
├── manager/                         # upstream Deployment kustomize root
├── managedresources/                # upstream CRDs+RBAC kustomize root
├── webhooks/                        # upstream WebhookConfig kustomize root
├── templates/                       # our custom manifests (Helm templates today)
├── netpol-labels.yaml
└── values-override.yaml
```

**After**:

```
# system/ipam-capi-remote/ is DELETED

system/kustomize/ipam-capi-remote/   # sole source of truth, consumed by operator directly
├── kustomization.yaml               # NEW: top-level, combines sub-roots
├── manager/, managedresources/, webhooks/    # (unchanged structure, refs pinned)
├── additions/                       # NEW: our custom manifests as kustomize resources
│   ├── kustomization.yaml
│   ├── webhook-service.yaml
│   ├── remote-kubeconfig-configmap.yaml
│   ├── network-policy.yaml
│   └── ...
├── netpol-labels.yaml
└── values-override.yaml
```

**Changes**:
- Top-level `kustomization.yaml` combines all sub-roots
- Our custom manifests move from `templates/` (Helm templates) to `additions/` (plain kustomize resources); Helm-templated values become kustomize `configMapGenerator` refs or `patches`
- Upstream refs in `manager/kustomization.yaml`, `managedresources/kustomization.yaml`, `webhooks/kustomization.yaml` pinned to specific tags/SHAs (they're currently unpinned — see §9)
- `webhook-injector-sidecar.tpl` deleted (sidecar spec moves to CR)
- No `templates/managedresource.yaml` equivalent needed

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
│   │   ├── inject_init_container.go
│   │   ├── rename_kind.go
│   │   ├── rewrite_webhook_url.go
│   │   ├── add_labels.go
│   │   ├── filter_kinds.go
│   │   └── transform.go
│   ├── split/
│   │   └── split.go
│   ├── deliver/
│   │   ├── apply.go                            # SSA against a Kubernetes client
│   │   ├── health.go                           # per-resource health computation
│   │   └── deliver.go
│   ├── clients/
│   │   ├── host.go                             # in-cluster client
│   │   └── shoot.go                            # kubeconfig-from-Secret client
│   └── manifest/
│       └── manifest.go
├── go.mod
├── main.go
├── Makefile
└── Dockerfile
```

### 5.2 Interfaces

```go
type Source interface {
    Render(ctx context.Context, values map[string]any) ([]Manifest, error)
}

type Transformation interface {
    Apply(manifests []Manifest) ([]Manifest, error)
    Type() string
}

type Applier interface {
    Apply(ctx context.Context, resource Manifest) (Status, error)
    Delete(ctx context.Context, resource Manifest) error
}

type Manifest struct {
    Unstructured *unstructured.Unstructured
    Origin       Origin   // upstream | additions
    Target       Target   // host | remote | drop
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

    // 1. Fetch and render source
    src, err := source.From(cr.Spec.Source)
    manifests, err := src.Render(ctx, cr.Spec.Source.Values())

    // 2. Apply transformations
    for _, t := range cr.Spec.Transformations {
        transform, err := transform.From(t)
        manifests, err = transform.Apply(manifests)
    }

    // 3. Split by target
    host, remote, dropped := split.ByTarget(manifests)

    // 4. Get clients
    hostApplier := r.HostApplier
    shootApplier, err := r.getShootApplier(ctx, cr.Spec.RemoteKubeconfig)  // reads Secret in CR namespace

    // 5. Apply to targets, collect status
    hostStatus := make([]ResourceStatus, 0, len(host))
    for _, m := range host {
        s, err := hostApplier.Apply(ctx, m)
        hostStatus = append(hostStatus, s)
    }
    remoteStatus := make([]ResourceStatus, 0, len(remote))
    for _, m := range remote {
        s, err := shootApplier.Apply(ctx, m)
        remoteStatus = append(remoteStatus, s)
    }

    // 6. Update inventory + status
    cr.Status.HostResources = hostStatus
    cr.Status.RemoteResources = remoteStatus
    cr.Status.Conditions = computeConditions(hostStatus, remoteStatus)
    if err := r.Status().Update(ctx, cr); err != nil {
        return ctrl.Result{}, err
    }

    return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}
```

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
   - Verify: split correctness, apply to (mock) both clients, deletion cascade, drift correction via re-reconcile
3. **Equivalence tests** vs. today's charts:
   - Test fixture per operator × representative shoot
   - Render today's chart with `helm template` → capture full manifest stream (host + remote)
   - Render new operator (mocked apply layer captures manifests + target destination) → assert byte-identical manifest streams

Additionally:
4. **Drift tests**: apply resource, mutate externally, verify next reconcile re-applies
5. **SSA coexistence tests**: apply WebhookConfiguration; mock caBundle writer with distinct field manager; verify operator re-applies don't clobber caBundle
6. **Deletion tests**: verify finalizer cleanup on host + remote, verify CRDs retained by default

---

## 6. Migration plan

Eight phases:

### Phase 1: Build operator (~2-3 weeks)

- Scaffold kubebuilder
- Implement source (Helm + kustomize)
- Implement 5 transformations
- Implement split + dual-cluster SSA + drift + health + deletion
- Write unit + integration tests
- Write equivalence tests for metal-operator

**Success criterion**: Equivalence test passes for metal-operator against a QA shoot's expected output; drift + health + deletion tests pass.

### Phase 2: Restructure metal-operator-remote (~1 week)

- Move templates to `templates/host/` and `templates/remote/` with target annotations
- Invert upstream values to enable
- Add `_helpers.tpl` for annotations
- Delete `webhooks.yaml`, `controller-manager.yaml`, `managedresource.yaml`, `webhook-config.yaml`, `sidecar.tpl`, `managedresources/`, `values-overrides.yaml`, `values-managed-resources.yaml`
- Delete `make build-metal-operator-remote`
- Chart to `0.7.0` (breaking bump)

**Success criterion**: `helm template metal-operator-remote --values <shoot-values>` produces the expected upstream + additions stream (verified via equivalence test).

### Phase 3: Update webhook-injector for SSA field-manager discipline (~1 week)

- Verify webhook-injector currently sets a distinct SSA field manager on its writes
- If not, add flag `--field-manager=webhook-injector` (or similar) and ensure it's used for all writes
- Test coexistence: operator applies WebhookConfig without caBundle; injector patches caBundle; operator re-apply doesn't clobber

**Success criterion**: SSA coexistence test passes; injector reads no ConfigMap and only manages certs + caBundle in shoot.

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
- SSA field-manager coexistence with injector doesn't cause ping-pong

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

### 7.6 SSA coexistence with injector

- Deploy operator + injector on QA
- Verify WebhookConfig lifecycle: operator applies without caBundle → injector patches → operator re-apply preserves caBundle
- Rotate cert → injector updates caBundle → operator re-apply preserves new caBundle

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

### 9.2 Injector field-manager verification

Does webhook-injector currently write with a distinct SSA field manager? Needed for SSA coexistence.

To be verified during Phase 3. Small injector change if not already the case.

### 9.3 Per-CR transformation config duplication

`injectInitContainer.container` is ~30 lines. If duplicated across all shoots for the same operator, this is verbose.

Options:
- Accept duplication (explicit, override-friendly)
- Reference from a ConfigMap: `injectInitContainer: {from: {configMap: sidecar-spec}}`
- Provide as chart value default; CR overrides

Deferred to Phase 1.

### 9.4 Kustomize values projection

How are `spec.source.kustomize.values` projected into the kustomize root? Candidates:
- Generated `configMapGenerator` overlay
- kustomize `replacements`
- Convention: kustomize source declares expected literals

Deferred to Phase 7.

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

---

## 10. Glossary

- **Host** — the seed cluster; where the operator Pod runs
- **Remote** — the virtual (shoot) cluster; workerless, holds domain CRDs and CRs
- **Additions** — our custom manifests that don't come from upstream (Services, Ingress, NetPol, kubeconfig, injector RBAC, etc.)
- **Upstream** — the operator's original Helm chart or kustomize source (e.g., `ironcore-dev/metal-operator`)
- **Wrapper chart** — historical name for `system/<operator>-remote/` Helm charts. Under this design, only helm-upstream operators have one.
- **SSA** — Server-Side Apply, Kubernetes API primitive for declarative apply with field ownership
- **Field manager** — identifier for the client that owns a set of fields in a resource (per-field granularity via SSA)
- **webhook-injector** — companion controller ([SAP-cloud-infrastructure/webhook-injector](https://github.com/SAP-cloud-infrastructure/webhook-injector)) that manages TLS cert lifecycle and patches caBundles on WebhookConfigurations + CRDs. Under Option 2, its role narrows to certs/caBundle only; delivery of WebhookConfigurations moves to the operator.
- **Transformation** — a typed Go struct implementing `Apply(manifests) → manifests`, opt-in per CR
- **Source discriminator** — the `spec.source.{helm,kustomize}` choice in the CR
- **Target annotation** — `dual-deployment-operator.cc.sap/target: {host|remote|drop}` on a manifest, overrides kind-based split rules
- **Origin annotation** — `dual-deployment-operator.cc.sap/origin: {upstream|additions}` on a manifest, tagged by chart's `_helpers.tpl` (additions) or by operator on fetch (upstream)
- **Gardener token-requestor** — Gardener component that provisions shoot API tokens as Secrets in the seed
- **GRM** — gardener-resource-manager; per-shoot Gardener component that reconciles `ManagedResource` objects. Not used under this design (operator applies directly).

---

*Design doc revision 3 — Option 2 chosen. Predecessor revisions: r2 (Model C with MR-based remote delivery), r1 (Model A/B-ish with `spec.upstreamChart` + `spec.host.chart`). Both archived in git history.*
