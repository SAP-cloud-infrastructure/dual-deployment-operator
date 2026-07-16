# dual-deployment-operator — Design Document

**Status:** Proposal
**Tracking issue:** [cc/unified-kubernetes#1294](https://github.wdf.sap.corp/cc/unified-kubernetes/issues/1294)
**Predecessor POC:** [sapcc/helm-charts#11633](https://github.com/sapcc/helm-charts/pull/11633) (kustomize-based, archived)
**Original problem:** [cc/unified-kubernetes#1169](https://github.wdf.sap.corp/cc/unified-kubernetes/issues/1169)
**Enabling change (r7):** [webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14) — adds a label-scoped, direct-apply, caBundle-only "target patch mode" to the injector. This removes the two constraints that shaped r5/r6 (the injector's source-ConfigMap-only delivery and its ManagedResource-only CRD caBundle stamping), collapsing the cross-stream transformation scope and closing the §9.2 open limitation. See §2.2.4 and CONTEXT.md revision 7.

---

## TL;DR

A kubebuilder operator (`dual-deployment-operator`) that pulls **one artifact per operator** (Helm chart or kustomize source) from `sapcc/helm-charts`, **renders it twice with mode-specific configuration** (once for host, once for remote), applies typed Go transformations to each render independently, and **applies each render directly to its target Kubernetes API** — host resources to the seed via the operator's own service account, remote resources (including WebhookConfigurations) to the shoot via a Gardener-provided kubeconfig.

Key characteristics:

- **Two-render, no split.** Chart/kustomization determines what belongs to host vs remote via mode-specific configuration (Helm: `hostValues`/`remoteValues`; kustomize: `hostPath`/`remotePath` selecting overlay directories). Each render produces only the resources for its target cluster. Operator does not decide routing; the source decides via what it emits per mode.
- **One artifact per operator, self-contained.** No wrapper + additions split.
- **Operator supports both Helm and kustomize** via a CR source discriminator (`spec.source.helm` xor `spec.source.kustomize`).
- **Small transformation menu:** 3 types — `patch` (DSL: strategic-merge or JSON Patch), `rewriteWebhookURL`, `filterKinds`. All per-render (a single scope). No general-purpose DSL, no scripting. `patch` replaces the historical `injectInitContainer` and `addLabels` typed types for patch-shaped operations. `renameKind` is not needed (was an artifact of ManagedResource-based delivery). The r5/r6 cross-stream `packageWebhookConfigsForInjector` type is **removed** in r7 — the operator now applies WebhookConfigurations directly to the shoot rather than packaging them for the injector (see §2.2.4).
- **Dual-cluster direct apply** — operator holds two Kubernetes clients per CR, applies host + remote uniformly with server-side apply + drift correction + per-resource health tracking. No `ManagedResource` wrapping. WebhookConfigurations and CRDs are applied by the operator with `caBundle` left unset (unowned), so the injector can own that one field.
- **webhook-injector retained for caBundle + cert lifecycle only.** Under r7 it runs in **target patch mode** ([webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14)): it watches labeled CRD/Validating/Mutating WebhookConfiguration objects **on the shoot** (`--target-label`) and keeps their `.caBundle` in sync as certs rotate, patching **only** `caBundle` — it does not create, delete, or rewrite `clientConfig`. It does NOT deliver WebhookConfigurations from a source ConfigMap anymore (`--webhook-config-name` is left unset). The operator and injector write the same objects but **disjoint fields**: the operator owns everything except `caBundle`; the injector owns `caBundle` only. SSA field ownership keeps them from clobbering each other.
- **Zero `make build-` targets remain.** All pre-rendered YAML deleted.
- **Estimated size:** ~900-1200 lines of Go (slightly less than r6 — cross-stream scope removed), shared across all five current remote operators.
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
| **C. Option 2, r5/r6 form** (superseded by r7) | Operator directly (SSA) | Operator directly (second Kubernetes client) | webhook-injector, from a source ConfigMap the operator produces (`packageWebhookConfigsForInjector`) | webhook-injector unchanged (certs + caBundle injection into the WebhookConfigs it delivers) |
| **C′. Option 2, r7 form (chosen)** | Operator directly (SSA) | Operator directly (second Kubernetes client) | **Operator directly (SSA), `caBundle` left unset** | **webhook-injector target patch mode** — watches labeled WebhookConfigs/CRDs on the shoot, patches `caBundle` only; generates + rotates certs ([webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14)) |
| **D. Option 3** | Operator directly (SSA) | Operator directly (second Kubernetes client) | Operator directly | **Operator absorbs cert lifecycle** — webhook-injector deleted |

The GRM-based baseline (A) is the current architecture. Options B/C/C′/D progressively fold delivery mechanisms into the operator. Revision 7 adopts **C′**: the operator is the single delivery path for *all* remote resources (CRDs, RBAC, SA, additions, **and** WebhookConfigurations); the injector shrinks to caBundle + cert lifecycle only, coexisting via disjoint SSA field ownership rather than via a source ConfigMap. This is the "pure Option 2" that r5 wanted but could not have while the injector was ConfigMap-only and unmodifiable; [webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14) removed that constraint (§2.2.4).

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

This subsection describes Option 2 as it stood in r5/r6 (form **C**). Revision 7 refines it to form **C′** — see §2.2.4. Both share the same core (operator applies CRDs/RBAC/SA/additions directly; injector handles caBundle + certs); they differ only in *how* WebhookConfigurations reach the shoot.

- ✅ **Operator is the single delivery path for CRDs, RBAC, ServiceAccounts, and the operator's own additions** — applied directly via one Kubernetes client, no `ManagedResource` wrapping
- ✅ Chart emits **no delivery-shaped resources** — no MR wrapper template, no hand-authored `webhook-config` ConfigMap, no `.Files.Get` indirection. Chart is purely a manifest source.
- ✅ Drift correction and health tracking apply uniformly to host and remote via the same operator machinery (which we build anyway for host)
- ✅ Deployment topology becomes crisper: one operator per shoot-cp namespace with two Kubernetes clients (seed + shoot)
- ✅ CR status is the single source of truth for the resources the operator delivers
- ⚠️ (r5/r6 / form C) WebhookConfigurations were still delivered by the injector, not the operator — the operator produced the injector's source ConfigMap via the `packageWebhookConfigsForInjector` cross-stream transformation. This preserved the injector's ConfigMap contract without a `make build-` step, but left **two** remote-delivery paths (operator direct-apply + injector-via-ConfigMap). **Superseded in r7** — see §2.2.4.
- ⚠️ Requires the operator to hold a shoot kubeconfig — same credential model as the webhook-injector today, via Gardener token-requestor. No new mechanism.

**Preferred over Option 1 because** (comparison unchanged from r5/r6):

| Aspect | Option 1 | Option 2 (r5/r6 form C) |
|---|---|---|
| Remote delivery paths | 2 (operator + injector) | 2 (operator + injector-via-ConfigMap) |
| Chart complexity | Still has `webhook-config` ConfigMap contract with injector | Chart emits pure manifest resources; operator produces the ConfigMap |
| WebhookConfig update flow | Chart → ConfigMap → injector reads → injector applies | render → operator packages → ConfigMap → injector reads → injector applies |
| Injector's role | Full delivery + certs + caBundle | Delivery of WebhookConfigs (from operator-produced ConfigMap) + certs + caBundle injection |
| Operator scope | Applies to host + partial remote | Applies to host + full remote except WebhookConfigs |

Option 1 is a partial simplification that leaves an asymmetry between delivery paths for CRDs and delivery paths for WebhookConfigs. Option 2 (form C) achieved delivery uniformity for everything except WebhookConfigs; r7 (form C′) closes that last gap — see below.

**Preferred over Option 3 (absorbing injector entirely) because**:

Option 3 would require the operator to own TLS cert generation, rotation timing with overlap windows, and caBundle propagation. This is well-worn code in the webhook-injector today with known behavior. Reimplementing it in the operator is real work with real regression risk, for the marginal benefit of removing one Kubernetes Deployment (the injector). Not worth the trade in v1.

Option 3 remains a valid future consolidation if the injector becomes a maintenance burden or if we want a strictly single-component deployment. It is not a v1 goal.

#### 2.2.4 Chosen (r7): Option 2 form C′ — operator delivers WebhookConfigs directly, injector patches caBundle only

**What changed since r6.** Forms A–C and the r5/r6 design were all constrained by two properties of the webhook-injector *as it existed*:

1. It delivered WebhookConfigurations only by reading them from a **source ConfigMap** (`--webhook-config-name`) and could not be scoped to specific shoot objects by label. This is why r5 introduced `packageWebhookConfigsForInjector` (a cross-stream transformation) to produce that ConfigMap — the operator could not simply apply the WebhookConfigs itself and let the injector top up caBundle, because the injector would then *also* deliver them from the ConfigMap, double-writing.
2. Its only path to stamp `caBundle` into a **CRD conversion webhook** was its seed-side `ManagedResourceReconciler`, keyed on `--managed-resource-label`. Since this design emits no ManagedResources, that path never fired — leaving §9.2 as an open limitation blocking metal-operator's production migration.

[webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14) adds an opt-in **target patch mode** that removes both constraints:

- **`--target-label=<key>=<value>`** — the injector watches labeled `CustomResourceDefinition`, `ValidatingWebhookConfiguration`, and `MutatingWebhookConfiguration` objects **directly on the shoot** and keeps their `.caBundle` in sync as certs rotate. It patches **only** `caBundle` (per-webhook merge by name via `StrategicMergeFrom` for MWC/VWC; `MergeFrom` for CRD conversion), never creates/deletes, and never rewrites `clientConfig` Service→URL.
- **`--webhook-config-name` becomes optional** — with only `--target-label` + `--cert-sans`, the injector runs a cert-only + patch mode: it owns the cert Secret and rotation state machine, applies no WebhookConfigs from any ConfigMap.
- Labeled CRDs participate in the injector's rotation gate, so cert promotion waits for shoot propagation (zero-downtime).

**r7's delivery (form C′):**

- ✅ **Operator is the single delivery path for *all* remote resources** — CRDs, RBAC, SA, additions, **and** WebhookConfigurations — applied via one Kubernetes client with SSA. One remote-delivery path, kind-agnostic.
- ✅ **No cross-stream transformation.** `packageWebhookConfigsForInjector` is removed; the operator no longer moves WebhookConfigs between renders or emits a source ConfigMap. The transformation menu is a single per-render scope (§3.4).
- ✅ **Disjoint SSA *field* ownership** replaces disjoint *resource* ownership. The operator applies WebhookConfigs (and conversion-webhook CRDs) with `caBundle` **left unset**, so it never owns that field; the injector's target patch mode owns `caBundle` only. Two managers, same object, non-overlapping fields — SSA keeps them from clobbering each other, and there is no caBundle ping-pong.
- ✅ **§9.2 closed** — conversion-webhook CRD caBundle is now stamped on the shoot CRD directly by the injector's target patch mode; no ManagedResource needed. (§9.2 records this resolution.)
- ⚠️ **New coupling: a label contract.** The operator's WebhookConfig/CRD output must carry the injector's `--target-label` so the injector adopts them. This is a one-line annotation/label on those resources (added by the chart or a `patch`), and is strictly simpler than the ConfigMap name/dataKey/YAML-shape contract it replaces.
- ⚠️ **New deployment requirement: injector flags.** The injector sidecar must be started with `--target-label` (+ `--cert-sans`) and **without** `--webhook-config-name`. This is a deployment-manifest change, tracked as a migration step (§6).

**Preferred over form C because** it removes the last remote-delivery asymmetry (WebhookConfigs now flow through the operator like everything else), deletes an entire transformation scope and interface (§5.2), and closes the only open production blocker (§9.2) — at the cost of a label contract that is cheaper than the ConfigMap contract it replaces. The injector shrinks to exactly its specialty (caBundle + certs), which is the shape r5 originally wanted.

**Still preferred over Option 3** for the same reason as before: the operator does not absorb cert generation/rotation. The injector remains, only smaller.

---

## 3. Design

### 3.1 Architecture

![dual-deployment-operator reconcile dataflow (two-render, r7)](../assets/architecture-dataflow.drawio.svg)

*Reconcile dataflow: CR → operator → host render + remote render → per-render transforms (patch, rewriteWebhookURL, filterKinds) → server-side apply to seed / shoot → unified drift correction + health → CR status. Editable in [draw.io / diagrams.net](https://app.diagrams.net).*

![dual-deployment-operator per-shoot deployment topology](../assets/architecture-topology.drawio.svg)

*Deployment topology: one operator Pod per `shoot--cp--*` namespace, two Kubernetes clients (seed in-cluster + shoot via Gardener token-requestor kubeconfig). Webhook-injector sidecar present only for `metal-operator` and `ipam-capi`. Editable in [draw.io / diagrams.net](https://app.diagrams.net).*

Two independent renders per reconcile. Each render produces only the resources for its target cluster — the chart/kustomization is responsible for emitting the right set per mode via values or overlay path. Operator does not decide routing; the source decides.

**Single transformation scope (r7).** All transformations are per-render (`patch`, `rewriteWebhookURL`, `filterKinds`), applied independently to each render. The r5/r6 cross-stream phase and its sole type `packageWebhookConfigsForInjector` are removed: the operator applies WebhookConfigurations directly to the shoot (in the remote render) rather than moving them into a host-side ConfigMap for the injector to deliver (§2.2.4).

**Coexistence with webhook-injector** (target patch mode, [webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14)):

```
webhook-injector (sidecar of each operator Pod, per shoot)
     │
     ├── Watches cert Secret in seed
     ├── Generates + rotates TLS certs (writes to cert Secret)
     ├── Watches LABELED CRD/Validating/Mutating WebhookConfiguration
     │   objects ON THE SHOOT (--target-label), applied there by
     │   the operator
     └── Patches ONLY .caBundle on those objects, keeping it current
         as certs rotate (never creates/deletes, never rewrites
         clientConfig). --webhook-config-name is left unset.
```

The injector no longer delivers WebhookConfigurations from a source ConfigMap; the operator delivers them directly. The injector's job narrows to exactly caBundle + cert lifecycle. This requires the injector sidecar to be started in target patch mode (`--target-label` + `--cert-sans`, no `--webhook-config-name`); see §6.

The operator and injector write the **same** shoot objects (WebhookConfigurations, conversion-webhook CRDs) but **disjoint fields**:
- Operator (field manager `dual-deployment-operator`, server-side apply) owns every field **except** `.caBundle` on WebhookConfigurations and `.spec.conversion.webhook.clientConfig.caBundle` on CRDs. It applies these resources with `caBundle` **unset**, so it never owns or writes that field.
- Injector owns `.caBundle` only, patched via `StrategicMergeFrom` (MWC/VWC, per-webhook merge by name) / `MergeFrom` (CRD conversion).

Because the two managers own non-overlapping fields on the same objects, SSA prevents either from clobbering the other, and there is no caBundle ping-pong. (Contrast r5/r6, which achieved separation by making the operator not write WebhookConfigs at all; r7 achieves it by field-level ownership instead.)

**CRD conversion-webhook caBundle (resolved in r7)**: the injector's target patch mode stamps `.spec.conversion.webhook.clientConfig.caBundle` directly onto labeled CRDs on the shoot — no `ManagedResource` and no seed-side `ManagedResourceReconciler` required. This closes the r5/r6 open limitation that blocked metal-operator's production migration. See §9.2.

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

  # Target namespace for the remote (shoot) render + delivery (required).
  # Namespaced resources in the remote render that omit metadata.namespace are
  # placed here; cluster-scoped resources are unaffected. The host render/delivery
  # uses the CR's own metadata.namespace.
  remoteNamespace: metal-operator

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
                    # Target patch mode: watch labeled webhook objects on the shoot
                    # and keep their caBundle current. No --webhook-config-name.
                    args:
                      - --target-label=dual-deployment-operator.cc.sap/webhook-injector=metal-operator
                      - --cert-sans=metal-operator-remote-webhook-service.metal-operator.svc
                    volumeMounts:
                      - {name: webhook-certs, mountPath: /tmp/k8s-webhook-server/serving-certs, readOnly: true}
                volumes:
                  - name: webhook-certs
                    emptyDir: {}

    - rewriteWebhookURL:
        urlPrefix: "https://metal-operator-remote-webhook-service:443"

    - filterKinds:
        kinds: [Service]

    # Label the remote-render WebhookConfigurations (and conversion-webhook CRDs)
    # so the webhook-injector's target patch mode adopts them on the shoot and
    # keeps their caBundle in sync. The operator applies them directly (r7);
    # it omits the caBundle field so the injector owns it. Replaces the r5/r6
    # cross-stream packageWebhookConfigsForInjector transformation.
    - patch:
        target: {kind: ValidatingWebhookConfiguration}
        strategicMerge:
          metadata:
            labels:
              dual-deployment-operator.cc.sap/webhook-injector: metal-operator
    - patch:
        target: {kind: MutatingWebhookConfiguration}
        strategicMerge:
          metadata:
            labels:
              dual-deployment-operator.cc.sap/webhook-injector: metal-operator
    - patch:
        target: {kind: CustomResourceDefinition}
        strategicMerge:
          metadata:
            labels:
              dual-deployment-operator.cc.sap/webhook-injector: metal-operator


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
                    args:
                      - --target-label=dual-deployment-operator.cc.sap/webhook-injector=ipam-capi
                      - --cert-sans=ipam-capi-remote-webhook-service.ipam-capi.svc
                    volumeMounts: [...]
                volumes:
                  - {name: webhook-certs, emptyDir: {}}
    - rewriteWebhookURL: {urlPrefix: "https://ipam-capi-remote-webhook-service:443"}
    - filterKinds: {kinds: [Service]}
    # Label webhook objects for the injector's target patch mode (r7).
    - patch:
        target: {kind: ValidatingWebhookConfiguration}
        strategicMerge:
          metadata: {labels: {dual-deployment-operator.cc.sap/webhook-injector: ipam-capi}}
    - patch:
        target: {kind: MutatingWebhookConfiguration}
        strategicMerge:
          metadata: {labels: {dual-deployment-operator.cc.sap/webhook-injector: ipam-capi}}
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

**Single scope (r7): all transformations are per-render** (3 in v1): `patch`, `rewriteWebhookURL`, `filterKinds`. Each is applied independently to the host render and the remote render, in declaration order. Each transformation naturally affects only resources present in the render it's applied to. (The r5/r6 cross-stream scope and its sole type `packageWebhookConfigsForInjector` were removed in r7 — §2.2.4.)

Ordering. The reconciler applies the declared transformations to each render in declaration order. Recommended convention:

1. Structural changes (`patch` for sidecar injection, labels, etc.) first
2. Complex rewrites (`rewriteWebhookURL`) after structural changes
3. Filters (`filterKinds`) last

Labeling WebhookConfigurations/CRDs for the injector's target patch mode is done with `patch` (adding `metadata.labels`), and — since it only adds a label — is order-insensitive relative to the others.

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

Rewrites service-based webhook `clientConfig` to URL-based `clientConfig` on three kinds:
- `ValidatingWebhookConfiguration` — each `.webhooks[].clientConfig`
- `MutatingWebhookConfiguration` — each `.webhooks[].clientConfig`
- `CustomResourceDefinition` — the single `.spec.conversion.webhook.clientConfig` (only when `.spec.conversion.strategy == "Webhook"`)

```yaml
- rewriteWebhookURL:
    urlPrefix: "https://metal-operator-remote-webhook-service:443"
```

For each `clientConfig` whose `.service` is set: replace it with `.url = urlPrefix + service.path` (using `service.path`, or `""` if absent). An existing `.url` is left alone (idempotent). `.caBundle` is preserved. The transformation targets **both** WebhookConfiguration kinds and CRD conversion webhooks unconditionally in v1 — there is no per-kind narrowing field. A CRD without a webhook conversion strategy, or a `clientConfig` already using `.url`, is a no-op.

Rationale for including CRDs: a CRD conversion webhook's `clientConfig` also references a seed-local Service that the virtual (shoot) cluster cannot route to, so it needs the same Service→URL rewrite as WebhookConfigurations. The two live at different paths (`.webhooks[].clientConfig` vs `.spec.conversion.webhook.clientConfig`), so a single transformation walks both. (caBundle on CRD conversion webhooks is handled separately by the injector's target patch mode — §3.8, §9.2.)

**Stays typed** (not expressed as `patch`) because the operation iterates over `.webhooks[]` (WebhookConfigs) and inspects `.spec.conversion.webhook` (CRDs) with a conditional (only replace if `.service` is set), constructs the new value from parts (`urlPrefix + service.path`), and preserves other fields. Not naturally expressible as a strategic-merge or JSON patch.

#### 3.4.3 `filterKinds`

Drops resources of listed kinds from the manifest stream (neither host nor remote — discarded).

```yaml
- filterKinds:
    kinds: [Service, ConfigMap]
```

`source` (optional) restricts the filter to resources of a given origin (`upstream` or `additions`). Omit it — as every candidate operator does — to drop all resources of the listed kinds regardless of origin. In practice the chart already suppresses unwanted upstream resources at render time via mode values / overlays, so an unqualified `filterKinds` is sufficient. `source` exists as an escape hatch for the rare case where an upstream render hardcodes a resource you cannot disable via values *and* your own additions emit a resource of the same kind in the same render that must survive the filter. It relies on the `origin` annotation (see §3.3).

**Stays typed** because it's a stream filter (removes resources from the manifest list), not a patch on a resource. JSON Patch and strategic-merge operate within a single resource; they can't remove a resource from the stream.

#### 3.4.4 Labeling webhook objects for the injector (r7)

There is **no** dedicated transformation for the webhook-injector under r7. The operator applies WebhookConfigurations (and conversion-webhook CRDs) directly to the shoot as part of the normal remote render; the only requirement is that those objects carry the injector's `--target-label` so the injector's target patch mode adopts them and keeps their `.caBundle` current.

This label is added with the existing `patch` transformation (or, equivalently, stamped by the chart/kustomization on the upstream webhook objects). Example (metal-operator):

```yaml
- patch:
    target: {kind: ValidatingWebhookConfiguration}
    strategicMerge:
      metadata:
        labels:
          dual-deployment-operator.cc.sap/webhook-injector: metal-operator
- patch:
    target: {kind: MutatingWebhookConfiguration}
    strategicMerge:
      metadata:
        labels:
          dual-deployment-operator.cc.sap/webhook-injector: metal-operator
- patch:
    target: {kind: CustomResourceDefinition}      # only conversion-webhook CRDs need it;
    strategicMerge:                                # the injector skips non-webhook CRDs
      metadata:
        labels:
          dual-deployment-operator.cc.sap/webhook-injector: metal-operator
```

**caBundle ownership**: the operator applies these objects via SSA with the `caBundle` field **unset**, so it does not own that field. The injector's target patch mode owns `caBundle` only (§3.8). This is the r7 replacement for the r5/r6 cross-stream `packageWebhookConfigsForInjector` transformation, which packaged WebhookConfigurations into a host-side ConfigMap for the injector to read — no longer needed now that the injector patches labeled shoot objects directly (§2.2.4).

**Injector deployment**: the sidecar must run in target patch mode — `--target-label=<key>=<value>` matching the label above, `--cert-sans=<webhook service DNS name>`, and **no** `--webhook-config-name` (§6). The label key/value is a per-operator convention; the example uses `dual-deployment-operator.cc.sap/webhook-injector: <operator>`.

**Applicability**: charts that use the webhook-injector (metal-operator, ipam-capi). Charts without webhooks (boot, argora, khalkeon) add no label and run the injector-free.

#### 3.4.5 Menu extensibility and design stance

New transformation types are added by operator releases. Not extensible at runtime.

**Hybrid stance: typed for structurally-complex operations, embedded DSL (`patch`) for patch-shaped operations.**

The three current transformations reflect this:
- `patch` — DSL. Applies strategic-merge or JSON Patch. Replaces the historical `injectInitContainer` and `addLabels` typed types (both were thin wrappers around patches; the DSL makes the operation visible in the CR). Also used to stamp the injector's target-patch-mode label onto webhook objects (§3.4.4).
- `rewriteWebhookURL` — typed. Iteration + conditional replacement across `.webhooks[]` (WebhookConfigs) and `.spec.conversion.webhook.clientConfig` (CRDs); not naturally expressible as a patch.
- `filterKinds` — typed. Stream-level filter (removes resources); not a per-resource patch.

Rationale for the hybrid:
- Patches make the operation transparent in the CR — reader sees exactly what the transformation does, not a name that hides the same content
- Complex/stream operations don't map cleanly to patches; forcing them would lose clarity, not gain it
- Typed transformations get admission-time schema validation (Kubernetes-native Container spec, kind lists, etc.); the `patch` type validates the patch string at runtime (patch application errors surface as reconcile errors, not admission errors)

Design history (r5 → r6 → r7):
- r5 had 5 typed per-render + 1 typed cross-stream. `injectInitContainer`, `addLabels`, `renameKind` were separate typed types.
- r6: `renameKind` removed (was an artifact of ManagedResource-based delivery; direct-apply doesn't require the Role→ClusterRole conversion). `injectInitContainer` and `addLabels` collapsed into a single `patch` DSL — both were patch-shaped, and the typed wrapper hid the same content that a patch would show explicitly.
- r7: cross-stream scope and `packageWebhookConfigsForInjector` removed entirely — the operator applies WebhookConfigurations directly and the injector patches their caBundle in place (§2.2.4). The menu is now 3 per-render types, one scope.

Candidate future types (not implemented in v1, listed to document the extension path):

- `setImageTag` — override container image tag by selector; specifically useful for ipam-capi's per-cluster image tag override (see §9.4). Could be added if kustomize's `images:` transformer doesn't cover the use case.

**Not on the roadmap**:
- Routing/split-related transformations (`setTarget`, kind-based routing, target annotations). Under two-render (§3.5), routing is determined by what each render emits, not by post-render classification.
- Runtime plugin loading. Extensibility is via operator releases.
- ConfigMap-loaded named patch templates. Considered as compromise between typed and DSL; rejected because `patch` covers the common case without templating engine complexity, and complex operations stay typed.
- Cross-stream transformations. Removed in r7; no current use case remains (the one that existed, webhook packaging, is obsolete under the injector's target patch mode). If a genuine cross-render operation reappears, the scope can be reintroduced.

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
- `rewriteWebhookURL` — WebhookConfigurations get URL-based `clientConfig` (service replaced by url); conversion-webhook CRDs likewise get their `.spec.conversion.webhook.clientConfig` rewritten service→url (§3.4.2).
- `filterKinds {kinds: [Service]}` — drops the webhook Service.
- `patch` (label) — stamps the injector's `--target-label` onto the Validating/Mutating WebhookConfigurations (and conversion-webhook CRDs) so the injector adopts them on the shoot and keeps their caBundle current (§3.4.4).

Note: upstream's Role (leader-election) stays a Role — no renaming under direct-apply. Our `metal-token-rotate` Role also stays a Role. Both apply cleanly to the shoot with their original namespace scope.

Result: apply **all** remote-render resources — CRDs, ClusterRoles, ClusterRoleBindings, Roles/RoleBindings, SA, our namespace, our RBAC, **and the labeled WebhookConfigurations** — to the shoot cluster (r7). The operator applies the WebhookConfigurations with `caBundle` unset; the injector's target patch mode stamps their caBundle in place and keeps it current as certs rotate (§3.8).

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

Under r7 the webhook-injector runs as a sidecar of each operator Pod in **target patch mode** ([webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14)). Its role is:

1. **Watch cert Secret** in the operator's namespace
2. **Generate initial TLS certs** and populate the cert Secret (SANs from `--cert-sans`)
3. **Rotate certs** on expiration with configured overlap window
4. **Watch labeled webhook objects on the shoot** — Validating/Mutating WebhookConfigurations and conversion-webhook CRDs carrying `--target-label`, applied there by the operator. It watches them via a label-scoped cache on the shoot cluster.
5. **Patch `.caBundle` on those objects** — for each labeled object, keep `caBundle` in sync with the current cert (per-webhook `StrategicMergeFrom` for MWC/VWC; `MergeFrom` on `.spec.conversion.webhook.clientConfig` for CRDs). It patches **only** `caBundle` — it never creates or deletes objects and never rewrites `clientConfig` Service→URL. Labeled CRDs join the rotation gate so cert promotion waits for shoot propagation.

The injector is **no longer a delivery mechanism**. It does not read a source ConfigMap (`--webhook-config-name` is unset) and does not apply WebhookConfigurations. It touches exactly one field on objects someone else created.

**The operator delivers WebhookConfigurations; the injector owns their caBundle.** The operator applies WebhookConfigurations (and conversion-webhook CRDs) to the shoot via SSA as part of the normal remote render, with the `caBundle` field **unset** so it never owns that field. It stamps the injector's `--target-label` on those objects (via `patch`, §3.4.4) so the injector adopts them. The operator does not generate or rotate certs.

**Disjoint SSA *field* ownership** (not disjoint resources). On the shoot, the operator and injector write the **same** WebhookConfiguration/CRD objects but **non-overlapping fields**:
- Operator (field manager `dual-deployment-operator`, server-side apply): every field **except** `caBundle`. Plus CRDs (non-conversion), ClusterRoles, ClusterRoleBindings, Roles, RoleBindings, ServiceAccounts, and the operator's own additions — those are operator-only.
- Injector: `.caBundle` on labeled Validating/Mutating WebhookConfigurations and `.spec.conversion.webhook.clientConfig.caBundle` on labeled conversion-webhook CRDs — nothing else.

Because the two managers own non-overlapping fields, SSA prevents either from clobbering the other: the operator's periodic re-apply omits `caBundle` (so it never reverts the injector's write), and the injector patches only `caBundle` (so it never disturbs the operator's fields). No caBundle ping-pong, no `force` conflicts on shared fields.

**CRD conversion-webhook caBundle — resolved (r7).** The injector's target patch mode stamps caBundle directly onto labeled conversion-webhook CRDs on the shoot. No `ManagedResource`, no seed-side `ManagedResourceReconciler`, no GRM. This closes the r5/r6 open limitation (§9.2) that blocked metal-operator's production migration. The only requirement is that the operator label the conversion-webhook CRDs with `--target-label` (non-conversion CRDs may carry the label too — the injector skips CRDs without a webhook conversion strategy).

**Ordering during initial deploy**:
1. Operator applies CRDs, RBAC, SA, additions, and the labeled WebhookConfigurations (caBundle unset) to the shoot.
2. Injector observes the cert Secret (generates certs if absent) and the newly-labeled shoot objects, then patches their caBundle.
3. Webhook calls succeed once the WebhookConfigurations carry a valid caBundle.

The bootstrap has one fewer hop than r5/r6 (no host-side source ConfigMap to produce and consume); the injector reacts directly to the labeled shoot objects the operator applied.

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
    Render(ctx context.Context, mode Mode, namespace string) ([]Manifest, error)
}

type Mode string
const (
    ModeHost   Mode = "host"
    ModeRemote Mode = "remote"
)

// A single transformation interface (r7 — cross-stream scope removed).

// Transformation operates on a single render's manifest stream. The reconciler
// applies each declared transformation to the host render and the remote render
// independently, in declaration order.
type Transformation interface {
    Type() string
    Apply(manifests []Manifest) ([]Manifest, error)
}

// transform.Build parses spec.Transformations into an ordered []Transformation,
// preserving declaration order. (No scope split — every type is per-render.
// The r5/r6 CrossStreamTransformation interface and Group() splitter are gone.)

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

    // 2. Render TWICE — one per mode. Host uses the CR's own namespace;
    //    remote uses spec.remoteNamespace.
    hostManifests, err := src.Render(ctx, source.ModeHost, cr.Namespace)
    if err != nil { return r.errStatus(ctx, cr, "HostRenderFailed", err) }

    remoteManifests, err := src.Render(ctx, source.ModeRemote, cr.Spec.RemoteNamespace)
    if err != nil { return r.errStatus(ctx, cr, "RemoteRenderFailed", err) }

    // 3. Build the ordered transformation list (single per-render scope, r7).
    transforms, err := transform.Build(cr.Spec.Transformations)
    if err != nil { return r.errStatus(ctx, cr, "InvalidTransformation", err) }

    // 4. Apply each transformation to both renders independently, in
    //    declaration order. The same list runs on host and remote; each
    //    transformation naturally affects only resources present in the
    //    render it runs on. (No cross-stream phase in r7.)
    for _, t := range transforms {
        hostManifests, err = t.Apply(hostManifests)
        if err != nil { return r.errStatus(ctx, cr, "HostTransformFailed", err) }
        remoteManifests, err = t.Apply(remoteManifests)
        if err != nil { return r.errStatus(ctx, cr, "RemoteTransformFailed", err) }
    }

    // 5. Get clients
    hostApplier := r.HostApplier
    shootApplier, err := r.buildShootApplier(ctx, cr)
    if err != nil { return r.errStatus(ctx, cr, "ShootClientFailed", err) }

    // 6. Apply each render to its target cluster. WebhookConfigurations and
    //    conversion-webhook CRDs are applied with caBundle unset (the injector
    //    owns that field via its target patch mode).
    hostStatus := r.applyAll(ctx, hostApplier, hostManifests)
    remoteStatus := r.applyAll(ctx, shootApplier, remoteManifests)

    // 7. Update inventory + status
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
- Transformations apply to each render independently. `filterKinds {kinds: [Service]}` runs on both renders, dropping Services from whichever render emits them.
- Single transformation scope (r7): no cross-stream phase. WebhookConfigurations are applied to the shoot by the operator (in the remote render), not packaged into a host-side ConfigMap.
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
5. **Disjoint-field tests**: verify the operator applies Validating/Mutating WebhookConfigurations (and conversion-webhook CRDs) to the remote render with the `caBundle` field **unset**, and stamps the injector's `--target-label` on them — so the injector's target patch mode can own `caBundle` without the operator reverting it on re-apply. (Under SSA, the operator's field manager must not appear as owner of `caBundle`.)
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

### Phase 3: Configure webhook-injector target patch mode (~1-2 days)

The injector requires **the r7 code** ([webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14)) — target patch mode. This phase configures and verifies its coexistence contract with the operator:

- Configure the injector sidecar (in the operator's `patch`-injected sidecar spec, §3.4.4) with `--target-label=<key>=<value>`, `--cert-sans=<webhook service DNS>`, and **no** `--webhook-config-name`.
- Confirm the operator stamps the same `--target-label` on the Validating/Mutating WebhookConfigurations (and conversion-webhook CRDs) it applies to the shoot, and applies them with `caBundle` unset.
- Confirm disjoint SSA **field** ownership: the operator owns every field except `caBundle`; the injector owns `caBundle` only. Re-apply by the operator must not revert the injector's caBundle write, and the injector's patch must not disturb operator-owned fields.

**Success criterion**: with the injector in target patch mode, the operator-applied WebhookConfigurations and conversion-webhook CRDs on the shoot receive a valid caBundle from the injector and keep it across a simulated cert rotation; the operator's periodic re-apply does not revert caBundle. §9.2 is resolved by the injector's direct CRD stamping (no per-operator resolution needed).

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
- Operator's remote apply set **includes** WebhookConfigurations, applied with `caBundle` unset and carrying the injector's `--target-label` (operator and injector share these objects but own disjoint fields — §3.8)

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

### 7.6 Operator/injector coexistence on QA

- Deploy operator + injector (target patch mode) on QA
- Verify the operator applies the labeled Validating/Mutating WebhookConfigurations (and conversion-webhook CRDs) directly to the shoot with `caBundle` unset
- Verify the injector adopts the labeled shoot objects and stamps a valid `caBundle`, keeping it current across a simulated cert rotation
- Verify disjoint SSA field ownership: the operator's periodic re-apply does not revert the injector's caBundle; the injector's patch does not disturb operator-owned fields
- Verify conversion-webhook CRD caBundle is stamped on the shoot CRD by the injector (§9.2 resolved) — no ManagedResource involved

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

### 9.2 CRD conversion-webhook caBundle + URL rewrite — RESOLVED (r7)

**Status: resolved by [webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14) (caBundle) and by extending `rewriteWebhookURL` to CRDs (URL rewrite).**

A CRD carrying a conversion webhook (`.spec.conversion.strategy == "Webhook"`) has two seed-local references that must be handled for the virtual (shoot) cluster, both at the path `.spec.conversion.webhook.clientConfig`: the `.service` (which the shoot cannot route to) and the `.caBundle` (which must track the current cert). Both were unhandled in r5/r6.

*caBundle — original problem (r5/r6):* the webhook-injector's only path for stamping caBundle into `.spec.conversion.webhook.clientConfig.caBundle` was its `ManagedResourceReconciler`, which selects Gardener `ManagedResource` objects on the seed by `--managed-resource-label` and stamps caBundle into their embedded CRDs. This design produces **no** `ManagedResource` objects (GRM elimination is a core goal, §2.2.1), so that path never fired — leaving conversion-webhook CRD caBundle (metal-operator) unmanaged, and blocking metal-operator's production migration.

*caBundle — resolution:* the injector's new **target patch mode** (`--target-label`) stamps caBundle directly onto labeled conversion-webhook CRDs **on the shoot**, with no `ManagedResource` involved (it uses `MergeFrom` on `.spec.conversion.webhook.clientConfig`, patches only `caBundle`, and skips CRDs that don't use webhook conversion). The operator labels its conversion-webhook CRDs with the same `--target-label` it uses for WebhookConfigurations (§3.4.4). This is candidate resolution "B" from the r6 draft, realized upstream — but keyed on the shoot CRD object's label rather than a ConfigMap, and without any operator-side cross-stream transformation.

*Service→URL rewrite — original gap:* `rewriteWebhookURL` in r5/r6 iterated only `.webhooks[].clientConfig` (Validating/Mutating WebhookConfigurations) and never touched a CRD's `.spec.conversion.webhook.clientConfig`, so a conversion-webhook CRD's Service reference was left pointing at a seed Service the shoot cannot route to. The injector deliberately does **not** rewrite `clientConfig` Service→URL (it patches only caBundle), so nothing covered this.

*Service→URL rewrite — resolution:* `rewriteWebhookURL` is extended (§3.4.2) to also rewrite the CRD conversion webhook's `.spec.conversion.webhook.clientConfig.service → .url` (only when `.spec.conversion.strategy == "Webhook"` and `.service` is set), using the same `urlPrefix + service.path` construction and idempotency rule as for WebhookConfigurations. No new CRD field — the transformation simply walks the CRD path in addition to `.webhooks[]`.

Together these close the limitation: for a labeled conversion-webhook CRD applied by the operator, the operator rewrites its conversion webhook to a URL clientConfig (with caBundle unset) and the injector stamps caBundle in place. No `ManagedResource`, no GRM, no per-operator decision required before migration.

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
- **Option B**: operator gains a `setImageTag` transformation type (currently listed as a candidate future transformation in §3.4.5); CR carries the tag override
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
- **webhook-injector** — companion controller ([SAP-cloud-infrastructure/webhook-injector](https://github.com/SAP-cloud-infrastructure/webhook-injector)) that manages the TLS cert lifecycle and, under r7, runs in **target patch mode** ([webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14)): it watches labeled Validating/Mutating WebhookConfigurations and conversion-webhook CRDs **on the shoot** (`--target-label`) and keeps their `.caBundle` in sync as certs rotate, patching **only** `caBundle`. It does **not** deliver WebhookConfigurations (no `--webhook-config-name`), does not rewrite `clientConfig`, and does not create/delete objects. The operator delivers WebhookConfigurations (caBundle unset); the injector owns caBundle via disjoint SSA field ownership (§3.8). (CRD conversion-webhook caBundle: resolved in r7 — see §9.2.)
- **target patch mode** — the webhook-injector run mode that keeps `caBundle` current on labeled shoot objects applied by another writer (the operator), rather than delivering WebhookConfigurations from a source ConfigMap. Enabled by `--target-label` + `--cert-sans`, with `--webhook-config-name` unset. Introduced in [webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14).
- **Transformation** — a typed Go struct implementing `Apply(manifests) → manifests`, opt-in per CR. Applied to each render independently under the two-render pattern.
- **Source discriminator** — the `spec.source.{helm,kustomize}` choice in the CR. Each source discriminator has its own nested fields (Helm has `values`/`hostValues`/`remoteValues`; kustomize has `url`/`hostPath`/`remotePath`).
- **Two-render pattern** — the operator renders the source twice per reconcile, once per mode, producing disjoint host and remote manifest sets. See §3.5.
- **Mode** — one of `host` or `remote`. For Helm sources, injected as `.Values.mode`. For kustomize, selected via `hostPath` / `remotePath`.
- **Origin annotation** — `dual-deployment-operator.cc.sap/origin: {upstream|additions}` on a manifest. Chart authors stamp `additions` on their own templates via `_helpers.tpl` (Helm) or `commonAnnotations` (kustomize). Operator tags unlabeled manifests as `upstream`.
- **Gardener token-requestor** — Gardener component that provisions shoot API tokens as Secrets in the seed
- **GRM** — gardener-resource-manager; per-shoot Gardener component that reconciles `ManagedResource` objects. Not used under this design (operator applies directly).

---

*Design doc revision 7 — cross-stream scope removed; operator applies WebhookConfigurations directly to the shoot; webhook-injector runs in label-scoped target patch mode (caBundle-only), enabled by [webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14); §9.2 CRD conversion-webhook caBundle limitation closed. Predecessor revisions: r6 (hybrid patch DSL, 4 transformations incl. cross-stream `packageWebhookConfigsForInjector`), r5 (typed-only with 6 transformations), r4 (two-render pattern), r3 (single-render + split, superseded), r2 (MR-based remote delivery, superseded), r1 (two-chart CR, superseded). All archived in git history.*
