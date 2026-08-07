# Related Artifacts

Pointers to everything needed to reason about the operator's design and implementation.

---

## Tracking issues

| Issue | Purpose | Status |
|---|---|---|
| [cc/unified-kubernetes#1294](https://github.wdf.sap.corp/cc/unified-kubernetes/issues/1294) | Feature tracking for dual-deployment-operator | Open, has ToDo checklist mirroring the design's migration plan |
| [cc/unified-kubernetes#1169](https://github.wdf.sap.corp/cc/unified-kubernetes/issues/1169) | Original problem statement ("simplify remote operator deployment") | Closed / referenced by #1294 |
| [cc/unified-kubernetes#914](https://github.wdf.sap.corp/cc/unified-kubernetes/issues/914) | Webhook deployments for remote operators (context on why webhook-injector exists) | Older |
| [cc/unified-kubernetes#831](https://github.wdf.sap.corp/cc/unified-kubernetes/issues/831) | Epic — remote-operator umbrella | Older |

---

## Related PRs

| PR | Purpose | Status |
|---|---|---|
| [sapcc/helm-charts#11633](https://github.com/sapcc/helm-charts/pull/11633) | Kustomize-based POC (predecessor). Confirmed pure-tooling swap doesn't help. | Draft / archived. Kept as reference for what doesn't work. |

---

## Design doc location

Primary source of truth (mirrored in this repo):

- **In `sapcc/helm-charts`**: `docs/dual-deployment-operator/design.md` on branch `docs/dualdeploymentoperator-design`
  - [View on GitHub](https://github.com/sapcc/helm-charts/blob/docs/dualdeploymentoperator-design/docs/dual-deployment-operator/design.md)
- **In this repo**: `docs/design.md` (copy)

Design doc revisions:
- **r1** (archived in git): Two-chart CR (`spec.upstreamChart` + `spec.host.chart`) with mandatory transformation fields
- **r2** (archived in git): Single-chart CR, ManagedResource-based remote delivery
- **r3** (archived in git): Single-chart CR, direct dual-cluster apply with single-render + split; superseded by routing question
- **r4** (archived in git): Two-render pattern — source renders twice per reconcile (host mode + remote mode), each render's output applied directly to its target cluster. No split step. Pure Option 2 delivery (operator applies WebhookConfigs directly to shoot).
- **r5** (archived in git): Two-render + cross-stream transformation phase. Adds `packageWebhookConfigsForInjector` to accommodate the webhook-injector's inability to be scoped narrowly. Operator applies CRDs/RBAC directly to shoot, but WebhookConfigurations are packaged into a ConfigMap on host for the injector to deliver.
- **r6** (current): r5 + `renameKind` removed (was an artifact of ManagedResource-based delivery, not needed under direct-apply) + `injectInitContainer` and `addLabels` collapsed into a single `patch` DSL transformation (both were patch-shaped; the typed wrapper hid the same content that a patch would show explicitly). Menu simplified to 4 transformation types: `patch`, `rewriteWebhookURL`, `filterKinds`, `packageWebhookConfigsForInjector`.

Git commits in `sapcc/helm-charts` on branch `docs/dualdeploymentoperator-design`:
```
035c7c229d docs: switch to Option 2 delivery (direct dual-cluster apply)
4270ac4fae docs: rewrite dual-deployment-operator design for Model C
a810b701a5 docs: add dual-deployment-operator design document
```

---

## Current wrapper charts and their upstream

Repository: [`sapcc/helm-charts`](https://github.com/sapcc/helm-charts)

| Operator wrapper | Location | Upstream source | Upstream type |
|---|---|---|---|
| metal-operator-remote | `system/metal-operator-remote/` | [ironcore-dev/metal-operator](https://github.com/ironcore-dev/metal-operator) → OCI chart at `oci://ghcr.io/ironcore-dev/charts` | Helm |
| boot-operator-remote | `system/boot-operator-remote/` | [ironcore-dev/boot-operator](https://github.com/ironcore-dev/boot-operator) → OCI chart at `oci://ghcr.io/ironcore-dev/charts` | Helm |
| argora-operator-remote | `system/argora-operator-remote/` | [SAP-cloud-infrastructure/argora](https://github.com/SAP-cloud-infrastructure/argora) → OCI chart at `oci://ghcr.io/sapcc/charts` | Helm |
| khalkeon-remote | `system/khalkeon-remote/` | [cobaltcore-dev/khalkeon](https://github.com/cobaltcore-dev/khalkeon) → OCI chart at `oci://ghcr.io/cobaltcore-dev/charts` | Helm |
| ipam-capi-remote | `system/ipam-capi-remote/` (helmify output) + `system/kustomize/ipam-capi-remote/` (source) | [kubernetes-sigs/cluster-api-ipam-provider-in-cluster](https://github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster) | Kustomize |

Each wrapper chart's structure and Makefile target is analyzed in `design.md` §1.2 and §4.

### Per-shoot deployment pipeline (verified mechanics)

How these `-remote` wrapper charts get installed into different `shoot--cp--*` namespaces —
the Concourse `helm-chart-pipeline` + `cc/kube-secrets` structure (namespace template,
`filter` fan-out, `cluster_settings` overrides, and the
`values/helm/<class>/<region>/<shoot>/<release>.yaml` path convention) — is documented in
[`deployment-pipeline.md`](deployment-pipeline.md). This is the model `chart 2`
(`dual-deployment-operator-remote`) plugs into unchanged, and the reason `chart 1` is
namespace-agnostic. Sources: `github.wdf.sap.corp/cc/kube-secrets` (branch `master`) +
`sapcc/helm-charts`.

---

## webhook-injector (companion controller)

Repository: [SAP-cloud-infrastructure/webhook-injector](https://github.com/SAP-cloud-infrastructure/webhook-injector)

Current role today:
1. Reads WebhookConfiguration YAML from a ConfigMap in seed (`<operator>-remote-webhook-config`)
2. Applies WebhookConfigurations to the shoot cluster via mounted kubeconfig
3. Generates + rotates TLS certs (stores in seed cert Secret)
4. Patches `caBundle` on shoot's WebhookConfigurations and CRDs (labeled `<operator>-remote-webhook-injector: true`)

Role under this operator's design (Option 2):
- (1) and (2) removed — operator handles WebhookConfig delivery
- (3) and (4) retained — injector remains the cert lifecycle owner

Coexistence: SSA field managers separate operator (owns everything except `caBundle`) from injector (owns `caBundle` only). Injector code change (small): ensure a distinct field manager is set for its writes.

Deployment: runs as a **sidecar** in each operator's Pod today. Under the new design, the sidecar spec moves into the CR's `injectInitContainer` transformation. Chart no longer emits the sidecar template.

---

## Gardener components involved

Docs: [gardener/gardener](https://github.com/gardener/gardener)

| Component | Role in this design |
|---|---|
| **gardener-resource-manager (GRM)** | **Not used.** Design deliberately avoids MR-based delivery. See `design.md` §2.2.1 and `context.md` for rationale. |
| **token-requestor** | Provisions shoot API tokens as Secrets in the seed. Operator consumes these Secrets to build its shoot Kubernetes client. Existing mechanism, unchanged. |
| **gardenlet** | Manages the shoot's control plane (kube-apiserver, etcd, etc.) in the seed. Not directly interacted with. |
| **Shoot** (CR) | Not directly created or modified. Operator's CRs live in the shoot-cp namespace but don't touch the Shoot itself. |

---

## Kubernetes SIG projects referenced

| Project | Role |
|---|---|
| [helm/helm](https://github.com/helm/helm) | Helm SDK, used for chart pull + template. `helm.sh/helm/v3` module. |
| [kubernetes-sigs/kustomize](https://github.com/kubernetes-sigs/kustomize) | Krusty library for kustomize source rendering. `sigs.k8s.io/kustomize/api/krusty` module. |
| [kubernetes-sigs/controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) | Operator framework (via kubebuilder scaffold). |
| [kubernetes-sigs/kubebuilder](https://github.com/kubernetes-sigs/kubebuilder) | Scaffolding tool for the operator. |
| [kubernetes/client-go](https://github.com/kubernetes/client-go) | Kubernetes client library, used for SSA. |
| [flux/flux2](https://github.com/fluxcd/flux2) | Reference for how a similar controller uses krusty + helm SDK. |
| [argoproj/argo-cd](https://github.com/argoproj/argo-cd) | Reference for split-apply-status patterns and health computation logic. |

---

## Documentation branch state

Design doc branch in `sapcc/helm-charts`:

```
docs/dualdeploymentoperator-design (3 commits ahead of master, not pushed)
├── docs/dual-deployment-operator/design.md     # ~1620 lines, revision 6 (two-render + cross-stream + hybrid patch DSL)
```

To push and open a PR:
```bash
cd sapcc/helm-charts
git push -u origin docs/dualdeploymentoperator-design
gh pr create --base master --head docs/dualdeploymentoperator-design \
  --title "docs: dual-deployment-operator design (rev 3, Option 2)" \
  --body "See docs/dual-deployment-operator/design.md and cc/unified-kubernetes#1294"
```

Not pushed at time of writing — user has not requested push.

---

## Migration ordering

Operator repo builds independently of chart restructures. Rough ordering across repos:

1. **Operator repo (this repo)**: Phases 0-9 from `implementation.md` (through Phase 8 equivalence tests, plus Phase 9 **chart 1** — the `dual-deployment-operator` controller+CRD upstream chart, generated via the kubebuilder helm plugin and published as an OCI chart). Culminates in equivalence tests passing against today's chart output for metal-operator.
2. **Operator wrapper chart** (in `sapcc/helm-charts`): Phase 9 **chart 2** — `system/dual-deployment-operator-remote/`, which depends on chart 1 (pulled from the OCI repo), templates the `DualDeploymentOperator` CR instances, and carries the `shoot-access` Secret + `shoot-rbac-bootstrap` ManagedResource. Per-cluster values from `cc/kube-secrets`. Replaces the per-operator `<operator>-remote` wrapper charts.
3. **webhook-injector**: verify or add SSA field-manager discipline.
4. **Chart restructures** (in `sapcc/helm-charts`): per `design.md` §4. Done per-operator, first metal-operator (Phase 2 in design's migration plan), then boot/argora/khalkeon, then ipam-capi.
5. **Per-shoot rollout**: deploy operator + CR to one QA shoot first (Phase 4 of design), then production seeds (Phase 5-7).

Chart restructure must happen after the operator can render + apply today's chart correctly — that's what the equivalence tests verify.

---

## Contacts

Team CC-CoInf-CI:
- @d062284
- @d051408
- @d065300

Owners of related upstream operators:
- metal-operator: ironcore-dev community
- boot-operator: ironcore-dev community
- argora: SAP-cloud-infrastructure community
- khalkeon: cobaltcore-dev community
- ipam-capi: kubernetes-sigs / cluster-api community

Owner of webhook-injector: SAP-cloud-infrastructure community

---

## Historical notes

**Why the design went through six revisions**:

The conversation evolved through progressive refinement:

1. **r1** proposed a rich CRD with per-transformation fields at the top level (`injectSidecar`, `rewriteWebhookClientConfig`, etc.). Too much configuration surface.

2. **r2** simplified to a bounded transformation menu but kept MR-based remote delivery, because "MR is idiomatic in Gardener". Critique that surfaced: MR+GRM's benefits (drift, health) are things we must implement for host anyway. Once implemented, applying to remote via a second client is nearly free.

3. **r3** removed MR wrapping entirely. Operator applies directly to both host and shoot. webhook-injector's role narrowed to cert lifecycle. Chart is a pure manifest source. Rendered once, then split into host/remote by kind rules or target annotations.

4. **r4** removed the split step. Source is rendered twice per reconcile — once per mode. Chart/kustomization decides via mode-specific configuration what each render emits. Each render's output goes entirely to its target cluster. No routing decisions in operator or CR. Handles multi-Deployment topologies correctly.

5. **r5** added a cross-stream transformation phase to accommodate the real-world webhook-injector constraint (can't be narrowly scoped, must have source ConfigMap). Cross-stream transformation `packageWebhookConfigsForInjector` moves WebhookConfigurations from remote render into a ConfigMap on host for the injector to consume. Preserves zero-make-targets goal.

6. **r6** (final) removed `renameKind` (was needed under MR-based delivery, not under direct-apply) and collapsed `injectInitContainer` + `addLabels` into a single `patch` transformation that takes strategic-merge or JSON Patch content. Result: 4 transformation types instead of 6, CR content is more transparent (patches visible instead of typed wrappers hiding the same content), and 3 of the 5 candidate charts (boot, argora, khalkeon) reduce to a single `filterKinds` transformation.

Each revision was a real refinement in response to constraints or clarity concerns (r2 simplification, r3 dropped MR abstraction, r4 sidestepped routing, r5 accommodated injector constraint, r6 simplified the menu and adopted DSL for patch-shaped operations). The final design has ~87% fewer lines of chart artifact than today; the operator itself is ~1000-1300 lines of Go.

**The kustomize POC's real contribution**: it proved that swapping tooling without changing the model (pre-render at chart source time, commit generated files) doesn't help. That framing let the design conversation move directly to "eliminate pre-rendering entirely, do it at operator time" without wasting cycles on further tooling comparisons.
