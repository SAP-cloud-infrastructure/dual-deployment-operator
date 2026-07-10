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

### Revision 3: Single-chart CR, direct dual-cluster apply, single-render+split (superseded)

**Shape**: Same CR as revision 2, plus `spec.remoteKubeconfig`. Operator applies directly to both host and shoot via server-side apply with two Kubernetes clients. Rendered once, then split by kind rules or target annotation.

```yaml
spec:
  source:
    helm: {repo, name, version, values}       # xor kustomize
  remoteKubeconfig:
    secretName: <operator>-remote-kubeconfig
    key: kubeconfig
  transformations: [...]
```

**Direct-apply framing (still chosen)**:
- Single remote-delivery mechanism
- Operator's drift/health machinery serves both targets uniformly
- No MR wrapper templates in the chart
- No `webhook-config` ConfigMap indirection
- webhook-injector shrinks to its actual specialty (cert lifecycle only)

**Superseded because** of the routing/split question: single-render architecture required either hardcoded kind-based routing rules (rejected — inflexible, tied to operator releases), or per-CR `setTarget` transformations (rejected — annotation-adder redundant with what Helm can already express), or a chart-emitted routing config ConfigMap (rejected — still doesn't handle multi-Deployment topologies cleanly). Revision 4 avoids the routing question entirely.

### Revision 4: Two-render pattern (base)

**Shape**: Same direct-apply model as r3, but source is rendered **twice per reconcile** — once with host-mode configuration, once with remote-mode configuration. Each render's output goes entirely to its target cluster. No split step, no routing decisions in operator or CR.

```yaml
spec:
  source:
    helm:
      repo, name, version
      values:       # common to both renders
      hostValues:   # host-only overrides
      remoteValues: # remote-only overrides
    # OR
    kustomize:
      url:          # base
      hostPath:     # subpath for host overlay (default "host")
      remotePath:   # subpath for remote overlay (default "remote")
  remoteKubeconfig:
    secretName: <operator>-remote-kubeconfig
    key: kubeconfig
  transformations: [...]
```

**Chosen because**:
- Chart/kustomization decides what goes where via values or overlay paths — no routing decisions in operator or CR
- Handles multi-Deployment topologies (e.g., a Deployment in each cluster) correctly — each render produces its own set
- Familiar Helm/kustomize idiom (values-based mode switching, overlay directories)
- No hardcoded kind rules; no `setTarget` transformation; no routing config
- 2× render per reconcile is a small cost (~100-200ms total; reconcile is 10-min-scale)

`hostValues`/`remoteValues` fields are Helm-specific, nested under `spec.source.helm`. Kustomize uses `hostPath`/`remotePath` nested under `spec.source.kustomize`. Each source discriminator declares its own fields; no shared `values` at the `source` level.

### Revision 5: Cross-stream transformation for webhook-injector constraint (superseded)

**Shape**: r4 + a cross-stream transformation phase after per-render transformations.

**Motivation**: The current webhook-injector implementation reads WebhookConfigurations from a source ConfigMap on the seed and cannot be scoped narrowly by label. Pure Option 2 (operator applies WebhookConfigs directly to shoot, injector patches only caBundle) would require injector code changes we can't get. To keep the injector operational without changing it, the operator must produce the source ConfigMap it expects.

**Change**: Add `packageWebhookConfigsForInjector` as a **cross-stream transformation** (v1's only one). Runs after per-render transformations. Reads WebhookConfigurations from the remote render, serializes them, emits a ConfigMap into the host render. Injector then delivers those WebhookConfigs to the shoot (same as today's behavior).

Result: injector's role is preserved (source ConfigMap → deliver → caBundle rotate). Operator applies CRDs, ClusterRoles, RoleBindings, ServiceAccount directly to shoot (Option 2 style), but not WebhookConfigurations. Two delivery paths for remote resources: operator direct-apply (CRDs/RBAC) and injector-via-ConfigMap (WebhookConfigs).

**Trade-off vs. pure Option 2 (r4)**:
- Retained: zero `make build-` targets (operator packages live-rendered WebhookConfigs), Option 2 model for non-WebhookConfig remote resources, direct-apply drift correction and health tracking
- Sacrificed: single remote-delivery path (injector remains as a delivery mechanism for WebhookConfigs, not just for certs)

**Alternative rejected**: reintroduce a tiny `make build-webhooks` target that pre-renders `webhooks.yaml` and lets the chart wrap it in a ConfigMap via `.Files.Get`. Rejected because it fragments the "zero make targets" goal and requires manual regeneration on upstream webhook changes.

**r5 transformation menu** had two scopes:
- Per-render (5 types): `injectInitContainer`, `renameKind`, `rewriteWebhookURL`, `addLabels`, `filterKinds`
- Cross-stream (1 type): `packageWebhookConfigsForInjector`

r5 confirmed the typed-only stance after considering and rejecting DSL alternatives.

### Revision 6: renameKind removed, `patch` DSL adopted with typed variants (chosen)

**Shape**: r5 with two changes to the transformation menu, plus a refinement in how patches are structurally validated:

1. `renameKind` removed entirely (was an artifact of ManagedResource delivery; under direct-apply, upstream Roles work as-is in the shoot cluster)
2. `injectInitContainer` and `addLabels` collapsed into a single `patch` transformation
3. `patch` uses **typed variants** (`strategicMerge` object OR `jsonPatch` array of typed ops) rather than an opaque string — so CRD admission validates the patch shape

**Motivation**:
- **`renameKind` removal**: The Role → ClusterRole conversion in today's `make build-` was needed because ManagedResource delivery had constraints that made namespaced Roles awkward. Under direct-apply to the shoot cluster, the operator preserves namespace and applies Roles as Roles. No conversion needed. Removes a transformation type AND removes the main reason `origin: additions` was load-bearing (protecting our custom Roles from being renamed).
- **`patch` for 2 typed types**: `injectInitContainer` and `addLabels` were thin typed wrappers around what are fundamentally strategic-merge patches. The typed name (`injectInitContainer`) hid the same content that a patch would show explicitly. Making the patch content visible in the CR is more transparent — reader sees exactly what happens, not a name that stands in for it. `patch` remains bounded (Kubernetes-native patch types, target selector), it's not an arbitrary scripting DSL.
- **Typed variants for `patch` structure**: Initial r6 draft used `patch: {type: <enum>, patch: <string>}` — an opaque string. Refined to `patch: {strategicMerge: <object> XOR jsonPatch: <[op]>}` so CRD admission validates the outer shape (object vs array, JSON Patch op enum, required fields per op) rather than seeing the content as a string blob. See §3.4.1 and §9.9 in design.md.

**r6 transformation menu**:
- Per-render (3 types): `patch` (DSL: strategic-merge object OR JSON Patch array), `rewriteWebhookURL` (typed), `filterKinds` (typed)
- Cross-stream (1 type): `packageWebhookConfigsForInjector` (typed)

**Why the remaining typed types stay typed**:
- `rewriteWebhookURL` iterates over `.webhooks[]` with conditional replacement (only if `.service` is set) and constructs the new value from parts (`urlPrefix + service.path`). Not naturally expressible as a strategic-merge or JSON patch.
- `filterKinds` is a stream-level filter (removes resources); JSON Patch and strategic-merge operate within a single resource, not across a stream.
- `packageWebhookConfigsForInjector` moves resources between renders; also not a per-resource patch.

**Admission validation under r6**:
- **What CRD schema catches at admission**: `strategicMerge` must be an object; `jsonPatch` must be an array of ops each with valid enum `op`, required `path`; exactly one of the two variants set (via CEL rule); `PatchSpec.target` is a valid Selector.
- **What runtime catches**: content-level errors inside the patch (field type mismatches, unknown fields, paths that don't exist on the target). Surfaced as CR status conditions.
- **v2 planned**: validating admission webhook that parses patch content against the target kind's OpenAPI schema — catches content errors at admission instead of at reconcile. Deferred to keep v1 scope small; webhook scaffolding (`internal/webhook/`) is included in v1 via kubebuilder but not wired up.

**Trade-off**:
- CR is comparable in verbosity to r5's typed transformations (`strategicMerge` block replaces `container:` + `additionalVolumes:` block; similar line count)
- Adding a new patch-shaped transformation = writing a new patch entry in the CR, no operator release
- Adding a new non-patch transformation type = still requires operator release (typed struct + reconciler switch)
- Structural admission validation preserved (via typed variants); content admission validation deferred to v2 (webhook)

**Total transformation types**: 4 (down from r5's 6). Simpler menu, more transparent CR, admission validation preserved for the outer patch structure.

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

- ✅ Single remote-delivery path for CRDs/RBAC/SA/additions (operator applies these directly)
- ✅ Chart emits no delivery-shaped resources
- ✅ Drift/health uniformly serves host + remote via one code path
- ✅ webhook-injector runs unchanged (delivers WebhookConfigs from the operator-produced ConfigMap + cert lifecycle)
- ✅ Operator and injector write disjoint shoot resources — no shared-resource field ownership, no SSA co-ownership discipline (injector uses Create/Update, not SSA)
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

## Operator/injector separation (no shared-resource field ownership)

An earlier revision (r3) had the operator apply WebhookConfigurations directly to the shoot and co-own them with the webhook-injector via SSA field managers (operator owns everything except `.clientConfig.caBundle`; injector owns caBundle). This was **abandoned** for two reasons discovered later:

1. **The real injector does not use server-side apply.** It uses `Create`/`Update` and has no field-manager co-ownership model. The SSA co-ownership story could not have worked against the injector as it actually exists.
2. **The real injector delivers WebhookConfigurations itself**, reading them from a source ConfigMap (`--webhook-config-name`) and injecting caBundle before applying. There is no benefit to the operator also delivering them.

Under the current design (r5+), the operator and injector write **disjoint** resources on the shoot, so no field-ownership coordination is needed:

- **Operator** (field manager `dual-deployment-operator`, server-side apply): CRDs, ClusterRoles, ClusterRoleBindings, Roles, RoleBindings, ServiceAccounts, and the operator's own additions. It never writes WebhookConfigurations and never touches caBundle.
- **Injector** (`Create`/`Update`, not SSA): ValidatingWebhookConfigurations and MutatingWebhookConfigurations, from the operator-produced source ConfigMap, with caBundle injected. Plus cert generation/rotation.

The `packageWebhookConfigsForInjector` cross-stream transformation removes WebhookConfigurations from the operator's remote render and packages them into the injector's source ConfigMap on the host — so the operator never applies a WebhookConfiguration to the shoot, guaranteeing the disjoint-write property.

**CRD conversion-webhook caBundle — open limitation.** The injector's only CRD caBundle-stamping path is its `ManagedResourceReconciler`, which selects seed-side Gardener `ManagedResource` objects by `--managed-resource-label` and stamps caBundle into their embedded CRDs. This design produces no ManagedResources, so that path never fires. For operators whose CRDs carry a conversion webhook (metal-operator), caBundle on those CRDs is **not managed** as the design currently stands. The operator does **not** take this on — it would be doing the injector's work. Candidate resolutions (emit a minimal ManagedResource for CRDs only; extend the injector with a ConfigMap-driven CRD-stamping mode; or confirm the conversion webhook is unused in the virtual cluster) are tracked in `design.md` §9.2 and must be resolved before migrating metal-operator to production.

**Bootstrap ordering**: operator applies CRDs/RBAC/SA/additions; operator emits the injector's source ConfigMap; injector generates certs and applies WebhookConfigurations with caBundle. Webhook calls succeed once the WebhookConfigurations are present. Same as today for WebhookConfigurations.

---

## Transformation menu design

Small, bounded set of transformation types. Hybrid stance: typed Go structs for structurally-complex operations, embedded DSL (`patch`) for patch-shaped operations.

Four transformations in v1, in two scopes:

**Per-render (3)** — applied to each render independently:

| Transformation | Type | Replaces (in today's `make build-`) |
|---|---|---|
| `patch` | DSL (strategic-merge or JSON Patch) with target selector | Sidecar injection: `sed -e '/containers:/i\ initContainers:\n {{ include ... }}'`  |
| | | Label addition: `yq eval '(select(.kind == "CustomResourceDefinition") \| .metadata.labels."...") = "true"'` |
| `rewriteWebhookURL` | Typed Go | `yq eval '(.webhooks[].clientConfig \| select(.service)) \|= ({"url": ..." + .service.path})'` |
| `filterKinds` | Typed Go | `yq eval 'select(.kind != "Service" and ...)'` |

**Cross-stream (1)** — operates on both renders after per-render completes:

| Transformation | Type | Purpose |
|---|---|---|
| `packageWebhookConfigsForInjector` | Typed Go | Reads WebhookConfigurations from remote render, serializes them into a ConfigMap on host render. Enables the webhook-injector to consume its expected source ConfigMap without requiring `make build-` regeneration or code changes to the injector. |

Adding a new typed transformation type requires an operator release; existing CRs unaffected. Adding new `patch`-shaped transformations does NOT require an operator release — write a new `patch` entry in the CR.

**Notable removals from earlier revisions**:
- **`renameKind`** (was in r5): removed in r6. Was an artifact of ManagedResource-based delivery — under direct-apply the operator preserves namespace and applies Roles as Roles. No conversion needed.
- **`injectInitContainer`, `addLabels`** (typed in r5): removed in r6, replaced by `patch`. Their typed wrappers hid the same content that a patch would show explicitly.

**Deliberately deferred**:
- `setImageTag` — override image tag by selector; specifically for ipam-capi's per-cluster image tag override
- Others: rejected — see rejected framings section

**Ordering**. Reconciler groups declared transformations by scope: per-render first (in declaration order, applied to both renders independently), then cross-stream (in declaration order, applied to paired renders).

Per-render ordering convention:
1. Structural changes (`patch`) first
2. Rewrites (`rewriteWebhookURL`) after structural changes
3. Filters (`filterKinds`) last

Cross-stream ordering: `packageWebhookConfigsForInjector` after per-render (specifically after `rewriteWebhookURL` since WebhookConfigs should be URL-rewritten before packaging).

---

## Two-render pattern (routing question)

Under the current design (revision 4), routing is not decided by the operator or the CR. Chart/kustomization decides via mode-specific configuration:

- **Helm**: `spec.source.helm.hostValues` and `spec.source.helm.remoteValues` selectively enable parts of the upstream subchart per mode. Chart's own templates use `{{ if eq .Values.mode "host" }}` / `remote` guards. Operator injects `.Values.mode` per render.
- **Kustomize**: `spec.source.kustomize.hostPath` and `remotePath` point at two overlay directories in the source. Each overlay's `kustomization.yaml` selects the resources for that mode.

Each render produces only the resources for its target cluster. The operator applies each render's output entirely to that target — no post-render split, no target annotations to consult.

The **`origin: additions` annotation** on chart/kustomization-authored resources (via `_helpers.tpl` in Helm or `commonAnnotations` in kustomize) is still meaningful — it lets selective transformations distinguish upstream from our additions within a render. Its load-bearing consumer under r6 is `patch: target: {origin: upstream}` (patch only upstream resources, not a same-named additions resource). `filterKinds: source: upstream` can also use it, but no candidate operator does: the chart already suppresses unwanted upstream resources at render time (mode values / overlays), so an unqualified `filterKinds: {kinds: [Service]}` suffices. Under r6 the annotation is much less load-bearing than under earlier revisions (renameKind is gone, which was the primary consumer); `filterKinds: source` remains only as an escape hatch for a hypothetical un-disableable upstream resource colliding with a same-kind addition in one render.

**Rejected alternatives** for routing (all considered before adopting two-render):

- **Hardcoded kind-based rules in the operator** (e.g., CRDs → remote, Deployments → host): rejected because it embeds routing decisions in the operator binary, doesn't handle multi-Deployment topologies, and requires operator releases when adding operators with different routing needs.
- **Target annotation on every resource, added by chart helper**: works for chart-owned templates but Helm subcharts can't post-process each other's output, so upstream resources come out un-annotated. Requires operator-time annotation-adding transformations for upstream, which are redundant with what Helm can already express via values.
- **`setTarget` / `addAnnotations` transformation in CR**: syntactic sugar for annotation-adding. Same fundamental issue as above — inelegant workaround for Helm subchart limitation.
- **Chart-emitted routing config ConfigMap**: chart provides routing rules as a sentinel resource; operator reads and applies. Chart-owned but still doesn't handle multi-Deployment topologies (two Deployments of same kind can't be routed differently by kind-based rules).

Two-render sidesteps all these problems because the chart/kustomization decides via native mechanisms what to emit per mode, and each mode's output is the answer.

---

## Per-chart transformation profiles

Not every operator needs every transformation. Menu is opt-in per CR:

| Operator | Transformations |
|---|---|
| metal-operator | `patch` (sidecar), `rewriteWebhookURL`, `patch` (label CRDs), `filterKinds` (Service), `packageWebhookConfigsForInjector` |
| ipam-capi | `patch` (sidecar), `rewriteWebhookURL`, `filterKinds` (Service), `packageWebhookConfigsForInjector` |
| boot-operator | `filterKinds` (Service) |
| argora-operator | `filterKinds` (Service, ConfigMap, Secret) |
| khalkeon | `filterKinds` (Service) |

Notes:
- Ipam-capi does not need CRD labelling — its CRDs don't have conversion webhooks, so the webhook-injector doesn't need CRD-level marker labels for ipam-capi.
- Only metal-operator and ipam-capi use `packageWebhookConfigsForInjector` — the three simpler operators don't have webhooks.
- The 3 simpler operators (boot, argora, khalkeon) now use just ONE transformation each — a `filterKinds`. Massive reduction from r5's 2 (which included `renameKind`).
- No `renameKind` in any CR under r6 — direct-apply preserves Roles as Roles across all operators.

---

## Open questions (deferred)

Full list in `design.md` §9. Highlights:

1. **Egress from seeds to kustomize sources** (ipam-capi migration blocker). Assumed feasible for design purposes. Verify infra before Phase 7.

2. **CRD conversion-webhook caBundle management**. The injector's only CRD caBundle-stamping path selects seed-side Gardener `ManagedResource` objects by `--managed-resource-label`; this design produces no ManagedResources, so caBundle on conversion-webhook CRDs (metal-operator) is unmanaged as the design stands. The operator does NOT take this on. Candidate resolutions in `design.md` §9.2; resolve before metal-operator production migration.

3. **Per-CR transformation config duplication**. `injectInitContainer.container` is ~30 lines. If duplicated across N shoots per operator, consider ConfigMap-referenced spec. Deferred to Phase 1.

4. **Kustomize source parameterization for ipam-capi**. `spec.source.kustomize` has no `values` field (kustomize has no Helm-values equivalent). Today's `make build-ipam-capi-remote` sets image tag via yq on the helmified chart's values.yaml. Under the new design, this variability comes from either baking the image tag into the kustomize source's `images:` transformer at a pinned ref, or adding a `setImageTag` transformation to the operator menu. Deferred to Phase 7.

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

- **"Operator is a general YAML pipeline processor"** — no. It's a domain-specific tool with 4 transformation types (3 per-render + 1 cross-stream), one of which is a bounded DSL (`patch` for strategic-merge / JSON Patch). Adding new typed types is deliberate; open-ended composition/scripting is not supported.
- **"Chart is minimal, operator does everything"** — no. Chart handles anything Helm can do well (templating, values, conditionals in resources). Operator only does what Helm/kustomize can't (cross-subchart patches, kind-specific rewrites, cross-render packaging).
- **"Additions chart separate from wrapper chart"** — no. Per user requirement, one artifact per operator, self-contained. The wrapper chart contains both upstream (as Helm dep) and our custom manifests.
- **"Operator per-seed watching many shoots"** — considered, rejected. Per-shoot is simpler once direct remote apply is chosen.
- **"Operator has hardcoded routing rules by kind"** — rejected in favor of two-render pattern. Chart/kustomization decides what goes where via mode-specific configuration; operator makes no routing decisions.
- **"Every resource must carry a `target` annotation, operator errors on missing"** — considered under single-render designs, rejected. Under two-render, target is implicit from which render produces the resource; no per-resource `target` annotation needed.
- **"CR carries the routing table"** — considered, rejected. Duplicates chart-scoped knowledge across every CR; under two-render, chart/kustomization owns this.
- **"Chart emits a routing config ConfigMap that operator consumes"** — considered, rejected. Doesn't solve multi-Deployment topologies; adds indirection without capability.
- **"Kustomize source has a `values` map like Helm"** — no. Kustomize has no Helm-values equivalent. Per-CR parameterization for kustomize sources is via `hostPath`/`remotePath` (mode selection) plus, if needed, future kustomize-native fields (`images`, `patches`). Not a values map.
- **"Replace ALL typed transformations with generic JSON manipulation (DSL)"** — considered, rejected. Loss of self-describing names for stream-level operations, complex iteration/conditional operations, and cross-stream operations. However, r6 adopted `patch` DSL specifically for the 2 patch-shaped operations (`injectInitContainer`, `addLabels`) where typed wrapping hid the same content that patch would show. Non-patch-shaped operations stay typed (`rewriteWebhookURL`, `filterKinds`, `packageWebhookConfigsForInjector`).
- **"ConfigMap-based patch library that operator resolves"** — considered as middle-ground between typed and DSL. Rejected: adds templating engine complexity, ConfigMap versioning, and runtime failure surface. `patch` DSL inline in the CR is simpler and equally flexible.
- **"webhook-injector should be scoped narrowly via labels so operator can apply WebhookConfigs directly (pure Option 2)"** — the injector implementation cannot be modified. Preserve its source-ConfigMap contract via `packageWebhookConfigsForInjector` cross-stream transformation. See revision 5.
- **"`renameKind Role→ClusterRole` is needed on all upstream Roles"** — was true under ManagedResource-based delivery in r2. Under direct-apply (r3+) the operator preserves namespace and applies Roles as Roles. `renameKind` removed in r6.
