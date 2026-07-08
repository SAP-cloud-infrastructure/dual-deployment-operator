# Context and Design Decisions

This document captures the reasoning behind the design choices in [`design.md`](design.md), including alternatives considered and rejected, and the evolution of the design through discussion.

Read this if you're implementing the operator and want to understand **why** something is the way it is, not just what it is.

---

## The problem being solved

Today, five operators (`metal-operator`, `boot-operator`, `argora-operator`, `khalkeon`, `ipam-capi`) are deployed via wrapper Helm charts in `sapcc/helm-charts/system/<operator>-remote/`. Each wrapper:

1. Depends on the upstream operator's Helm chart (or in ipam-capi's case, a kustomize source)
2. Has upstream **disabled** at install time via values
3. Uses a `make build-<operator>-remote` target to pre-render upstream via `helm template` (or `kubectl kustomize | helmify`), transforming the output via `sed`/`yq`
4. Commits the pre-rendered YAML into the chart directory as `managedresources/*.yaml`, `webhooks.yaml`, `templates/controller-manager.yaml`
5. Delivers the pre-rendered YAML at chart-install time via three different mechanisms

The pain:

- **Silent drift**: `make build-` isn't in CI, runs off engineers' laptops with varying tool versions
- **30k lines** of committed generated YAML across the five operators pollutes PRs and reviews
- **Three delivery paths**: ManagedResource+gardener-resource-manager for CRDs/RBAC, webhook-injector ConfigMap+read+apply for WebhookConfigurations, webhook-injector cert-lifecycle for caBundles
- **Bespoke Makefile logic** per operator: adding a sixth means writing a sixth target from scratch
- **No install-time customization**: pre-rendered content can't reference values that vary per-cluster

The kustomize POC ([sapcc/helm-charts#11633](https://github.com/sapcc/helm-charts/pull/11633)) confirmed that swapping tooling doesn't help — kustomize produced **+79% LOC, +130% files** vs. the current approach. The real problem is the pre-render-and-commit model, not the tooling.

---

## Design evolution across the design conversation

The design went through three revisions before settling. Understanding what was rejected and why helps avoid regressing.

### Revision 1: Two-chart CR (rejected)

**Shape**: CR referenced two charts — `spec.upstreamChart` (external, upstream operator's Helm chart) and `spec.host.chart` (our custom additions).

```yaml
spec:
  upstreamChart: {repo, name, version}      # external
  host:
    chart: {repo, name, version}             # our own additions
    upstreamValues: {...}
    injectSidecar: {...}
    values: {...}
  remote:
    upstreamValues: {...}
    rewriteWebhookClientConfig: {...}
    labelCRDs: {...}
    drop: {...}
    values: {...}
  shared: {...}
```

**Rejected because**:
- Exposed the two-chart split as CR-level concerns; the split is a chart-maintainer concern, not a per-cluster concern
- Transformation fields (`injectSidecar`, `rewriteWebhookClientConfig`, `labelCRDs`, `drop.kinds`) as top-level CR fields exposed transformation mechanism as configuration
- CR schema would need to grow every time a new transformation type became necessary
- Values were split into `host`/`remote`/`shared` in the CR when they're just values passed to the chart

### Revision 2: Single-chart CR, MR-based remote delivery (rejected)

**Shape**: CR references one chart; operator renders it with transformations; splits by target annotation; delivers remote resources as ManagedResource + Secret pairs.

```yaml
spec:
  chart: {repo, name, version}
  values: {...}
  transformations:
    - injectInitContainer: {...}
    - renameKind: {...}
    - rewriteWebhookURL: {...}
    - addLabels: {...}
    - filterKinds: {...}
  # remote: managedResourceClass: shoot-core   # (this field was invented, then dropped)
```

**Rejected because**: GRM (gardener-resource-manager) provides drift correction, health tracking, keepObjects, token rotation. But — and this is the critical point — **the operator must implement drift correction and health tracking for host resources anyway** (Flux HelmRelease doesn't drift-correct at resource level, doesn't track per-resource health). Once these mechanisms exist for host, applying remote via a second Kubernetes client uses the same code. GRM's differential benefit is illusory.

MR wrapping added a layer of abstraction (chart emits MR → operator applies MR → GRM reads MR → GRM applies actual resource → observe MR status → operator surfaces to CR status) without capability gain.

### Revision 3: Single-chart CR, direct dual-cluster apply (chosen — Option 2)

**Shape**: Same CR as revision 2, plus `spec.remoteKubeconfig`. Operator applies directly to both host and shoot via server-side apply with two Kubernetes clients.

```yaml
spec:
  source:
    helm: {repo, name, version, values}       # xor kustomize
  remoteKubeconfig:
    secretName: <operator>-remote-kubeconfig
    key: kubeconfig
  transformations: [...]
```

**Chosen because**:
- Single remote-delivery mechanism
- Operator's drift/health machinery serves both targets uniformly
- No MR wrapper templates in the chart
- No `webhook-config` ConfigMap indirection
- Chart is purely a manifest source; operator does all delivery
- webhook-injector shrinks to its actual specialty (cert lifecycle only)

---

## Alternatives considered — rendering approach

Nine options were surveyed. Summary of rejections:

| Option | Why rejected |
|---|---|
| Keep current (Helm + Makefile sed/yq) | Fragile; committed generated files; not CI-addressable |
| Dual-kustomization POC | +79% LOC, +130% files (measured, not speculated) |
| Pure Helm chart, no upstream fork | Webhook clientConfig transformation can't be expressed in Helm |
| Flux `HelmRelease.postRenderers` | postRenderers are kustomize-only; can't do string-prefix URL |
| Gardener extension | Extensions tied to shoot lifecycle; our trigger is manual deployment |
| Pipeline operator with DSL | Reinvents kustomize with 3-5 transforms that don't justify a DSL |
| Chart-baked with operator for delivery only | Doesn't eliminate `make build-` |
| Chart-with-upstream-dep + operator for transforms (Helm only, no kustomize) | Works for 4/5. ipam-capi (kustomize upstream) is asymmetric |
| Operator with source discriminator (Helm + kustomize) | **Chosen** |

Full analysis in `design.md` §2.1.

---

## Alternatives considered — delivery mechanism

Four options were considered for how rendered resources get to their target clusters:

### Option A: Baseline (MR + GRM + injector) — rejected

Preserves today's three-path remote delivery.
- ✅ Gardener-idiomatic
- ❌ Three delivery paths; two are the source of debug complexity today
- ❌ GRM benefits are illusory (see revision 2 rationale)

### Option 1: Direct apply for CRDs/RBAC only — rejected (this is what the "Option 1 vs Option 2" comparison in design.md §2.2.2 is about)

- ✅ Removes MR wrapping for CRDs/RBAC
- ❌ WebhookConfigurations still delivered via `webhook-config` ConfigMap + webhook-injector read-and-apply loop
- ❌ Under the new design there is no pre-rendered `webhooks.yaml` to put in the ConfigMap — Chart would need to emit a ConfigMap containing rendered WebhookConfigs, which the injector re-renders and applies. Duplicative and clumsy.
- ❌ Two remote-delivery paths remain
- ❌ Chart still tied to injector's ConfigMap contract

### Option 2: Direct apply for CRDs/RBAC + WebhookConfigs, injector for certs only — CHOSEN

- ✅ Single remote-delivery path (operator applies everything)
- ✅ Chart emits no delivery-shaped resources
- ✅ Drift/health uniformly serves host + remote via one code path
- ✅ webhook-injector's role clarifies to certs only
- ✅ SSA field managers cleanly separate operator writes (everything except caBundle) from injector writes (caBundle only)
- ⚠️ Requires shoot kubeconfig in operator — same credential model as injector today, via Gardener token-requestor

### Option 3: Operator absorbs webhook-injector entirely — rejected for v1

- ✅ Simplest topology (one Deployment instead of two)
- ❌ Cert generation, rotation with overlap windows, caBundle propagation is well-worn code in webhook-injector today
- ❌ Reimplementing in operator adds real work with regression risk
- ❌ Marginal benefit (removing one Deployment) doesn't justify the risk in v1
- Deferred as valid future consolidation if injector becomes a maintenance burden

Full comparison in `design.md` §2.2 with a comparison table.

---

## Delivery model: why Option 2 over Option 1

This was the load-bearing discussion. Six concrete criteria:

| Criterion | Option 1 | Option 2 |
|---|---|---|
| Remote delivery paths | 2 (operator + injector) | 1 (operator only) |
| Chart delivery contracts | Chart emits `webhook-config` ConfigMap for injector | Chart emits pure manifest resources |
| WebhookConfig update flow | Chart → ConfigMap → injector reads → injector applies | Chart → operator applies directly |
| Injector's role scope | Full delivery + certs + caBundle | Certs + caBundle only |
| Operator scope | Host + partial remote | Host + full remote |
| Debug when webhook fails to appear | "Which path is stuck? Check injector + CR status" | "Check CR status" |
| Adding new remote resource kind | Depends on kind, needs routing decision per kind | Everything through operator, kind-agnostic |

Option 1 is a partial simplification that retains the asymmetry between delivery paths for CRDs/RBAC vs. WebhookConfigs. Option 2 achieves delivery uniformity we'd need anyway to reason about remote state coherently, at the cost of teaching the operator to apply one additional resource kind (which isn't special from a Kubernetes-client perspective).

---

## Deployment topology: why per-shoot, not per-seed

Original design proposed per-seed operator (one instance per seed watching all shoot-cp-* namespaces). Changed to per-shoot after Option 2 was chosen.

**Reasoning**:

Under Option 2, operator needs a shoot kubeconfig to apply remote resources. Two placements:

- **Per-seed**: operator needs to juggle N kubeconfigs (one per shoot in its seed). Credential handling non-trivial. Token rotation must be coordinated across all shoots.
- **Per-shoot**: operator runs in each shoot-cp namespace with the shoot's kubeconfig mounted from a Gardener token-requestor Secret in the same namespace. Direct apply is trivial. RBAC scoped to that namespace.

Per-shoot wins on simplicity. Multiplication factor: ~50 shoots × 1 operator = 50 instances, but each is tiny (~200Mi RAM, minimal CPU). Total footprint comparable to today's webhook-injector sidecars (already per-shoot × per-operator).

Per-shoot matches the webhook-injector's existing topology, making colocation natural.

---

## SSA field-manager coexistence with webhook-injector

Under Option 2, both operator and webhook-injector write to WebhookConfigurations in the shoot cluster. Server-side apply's field-ownership model handles this:

- Operator's SSA field manager: `dual-deployment-operator`
- Injector's SSA field manager: `webhook-injector` (or its existing name — verify)

On a `ValidatingWebhookConfiguration`:
- Operator owns: everything **except** `.webhooks[*].clientConfig.caBundle`
- Injector owns: `.webhooks[*].clientConfig.caBundle` only

Same for `MutatingWebhookConfiguration` and CRDs with `.spec.conversion.webhook.clientConfig`.

**Operator behavior**:
- On apply, omit the `caBundle` field entirely (or explicitly set to empty and force field ownership relinquish)
- On re-apply (drift correction), continue omitting `caBundle` — injector's writes to that field are preserved

**Injector behavior** (small change if not already the case):
- Ensure all writes to shoot resources use a distinct, stable SSA field manager
- This is likely the case — verify during Phase 3

**Bootstrap ordering**: operator applies WebhookConfig first (no caBundle). Between then and injector's first caBundle patch, webhooks are inert (calls fail). This is the same as today's bootstrap behavior.

---

## Transformation menu design

Bounded, typed Go structs. Not a DSL. Not extensible at runtime.

Five transformations in v1:

| Transformation | Replaces (in today's `make build-`) |
|---|---|
| `injectInitContainer` | `sed -e '/containers:/i\      initContainers:\n {{ include ... }}'` |
| `renameKind` | `sed 's/kind: Role/kind: ClusterRole/g'` |
| `rewriteWebhookURL` | `yq eval '(.webhooks[].clientConfig \| select(.service)) \|= ({"url": ..." + .service.path})'` |
| `addLabels` | `yq eval '(select(.kind == "CustomResourceDefinition") \| .metadata.labels."...") = "true"'` |
| `filterKinds` | `yq eval 'select(.kind != "Service" and ...)'` |

Each is a Go struct with strict schema. Adding a new type requires an operator release; existing CRs unaffected.

**Deliberately deferred**:
- `patch` — strategic-merge patch by selector; too close to DSL escape hatch. Add only if concretely needed.
- `addAnnotations` — mirror of `addLabels`; add if needed.
- `setImageTag` — override image tag by selector; adds risk of drift with values-based image config.

**Ordering matters**. Transformations run top-to-bottom as declared. Convention:
1. Structural patches (`injectInitContainer`, `addLabels`) first
2. Renames (`renameKind`) before consumers
3. Rewrites (`rewriteWebhookURL`) after structural changes
4. Filters (`filterKinds`) last

---

## Split rules design

After transformations, resources are bucketed as host/remote/drop.

**Precedence**: explicit target annotation on the resource wins over kind-based defaults.

**Kind-based defaults** (first match wins):

| Kind | Bucket |
|---|---|
| `CustomResourceDefinition` | remote |
| `ValidatingWebhookConfiguration`, `MutatingWebhookConfiguration` | remote |
| `ClusterRole`, `ClusterRoleBinding` | remote |
| `Role`, `RoleBinding` (post-rename should be rare) | remote |
| `ServiceAccount` with `origin: upstream` | remote |
| `ServiceAccount` with `origin: additions` | host |
| `Deployment`, `StatefulSet`, `DaemonSet` | host |
| `Service`, `ConfigMap`, `Secret`, `Ingress`, `NetworkPolicy` | host |
| `Namespace` | either — must have explicit annotation |
| everything else | host (with warning to encourage annotation) |

**Chart-side origin annotation** (`dual-deployment-operator.cc.sap/origin: additions`) lets the operator distinguish our additions from upstream's output, which matters for:
- Filter transformations with `source: upstream` (drop upstream Services but keep ours)
- ServiceAccount routing (upstream's SA goes remote for shoot use; injector's SA stays on host)

The target annotation (`dual-deployment-operator.cc.sap/target`) is chart-side override. Stripped by operator before applying (routing hint, not persisted state).

---

## Per-chart transformation profiles

Not every operator needs every transformation. Menu is opt-in per CR:

| Operator | Transformations |
|---|---|
| metal-operator | `injectInitContainer`, `renameKind`, `rewriteWebhookURL`, `addLabels` (CRDs), `filterKinds` (Service) |
| ipam-capi | `injectInitContainer`, `renameKind`, `rewriteWebhookURL`, `filterKinds` (Service) |
| boot-operator | `renameKind`, `filterKinds` (Service) |
| argora-operator | `renameKind`, `filterKinds` (Service, ConfigMap, Secret) |
| khalkeon | `renameKind`, `filterKinds` (Service) |

Note: ipam-capi does not need `addLabels` — its CRDs don't have conversion webhooks, so webhook-injector doesn't need CRD-level marker labels for ipam-capi.

---

## Open questions (deferred)

Full list in `design.md` §9. Highlights:

1. **Egress from seeds to kustomize sources** (ipam-capi migration blocker). Assumed feasible for design purposes. Verify infra before Phase 7.

2. **webhook-injector SSA field-manager verification**. Does it already write with a distinct field manager? Small change if not. Verify in Phase 3.

3. **Per-CR transformation config duplication**. `injectInitContainer.container` is ~30 lines. If duplicated across N shoots per operator, consider ConfigMap-referenced spec. Deferred to Phase 1.

4. **Kustomize values projection**. How `spec.source.kustomize.values` maps to kustomize configMapGenerator overlays or replacements. Deferred to Phase 7.

5. **CRD deletion policy default**. Default to `Retain` (avoid catastrophic data loss); explicit `Delete` for teardown scenarios. Confirm this is right.

6. **CRD versioning strategy**. v1alpha1 → v1beta1 (after 3 operators stable) → v1 (after all 5 for 6+ months).

---

## Non-goals and future consolidation

**Not in v1 scope**:
- Operator absorbing webhook-injector's cert-lifecycle role (Option 3). Deferred; valid future consolidation if injector becomes a burden.
- Self-management of the dual-deployment-operator (operator managing its own deployment via a `DualDeploymentOperator` CR for itself). Chicken-and-egg during upgrades; bootstrap via Flux HelmRelease.
- Multi-cluster / cross-region operator instances. Per-shoot topology is deliberately simple.
- Runtime-extensible transformation types (would require plugin loader; DSL creep risk).

**Deliberate simplicity choices**:
- No transformation composition (transformations don't produce transformations)
- No conditional transformations (`if kind == X then Y`)
- No cross-resource references in transformations (e.g., "inject sidecar into whatever Deployment matches this label")

If any of these become concretely needed, they can be added — but they're rejected as speculative complexity for v1.

---

## Rejected framings (avoid regressing)

- **"Operator is a general YAML pipeline processor"** — no. It's a domain-specific tool with 5 typed transformations. Adding transformation types is deliberate; open-ended composition is not supported.
- **"Chart is minimal, operator does everything"** — no. Chart handles anything Helm can do well (templating, values, conditionals in resources). Operator only does what Helm/kustomize can't (cross-subchart patches, kind-specific rewrites, cluster-boundary routing).
- **"Additions chart separate from wrapper chart"** — no. Per user requirement, one artifact per operator, self-contained. The wrapper chart contains both upstream (as Helm dep) and our custom manifests.
- **"Operator per-seed watching many shoots"** — considered, rejected. Per-shoot is simpler once direct remote apply is chosen.
