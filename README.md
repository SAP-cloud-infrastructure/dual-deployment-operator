[![REUSE status](https://api.reuse.software/badge/github.com/SAP-cloud-infrastructure/dual-deployment-operator)](https://api.reuse.software/info/github.com/SAP-cloud-infrastructure/dual-deployment-operator)

# dual-deployment-operator

A Kubernetes operator that manages the deployment of split seed/shoot controllers in Gardener environments. Consumes a Helm chart or kustomize source per operator, renders it, applies typed Go transformations, and applies each half to its target cluster (seed and shoot) via server-side apply.

**Status**: Phase 0–8 complete (design revision 7 — two-render + hybrid patch DSL). Kubebuilder scaffold, `v1alpha1` CRD types with CEL admission validation are in place. Source rendering (`internal/manifest` — multi-doc YAML parser + origin tagging; `internal/source` — production OCI-only Helm ChartLoader (a non-`oci://` Helm `repo` is rejected at admission via CEL) and go-git kustomize RootResolver, two-render per reconcile, per-source `authSecretRef` credentials; in-memory rendered-manifest cache keyed on resolved content id (git SHA / OCI digest) via `Deps.RenderCache`; Helm loader resolves OCI subchart dependencies at pull time via `downloader.Manager.Build()` — `Chart.lock` required, HTTP(S)-repo subchart deps rejected fail-closed, `--source-scratch-dir` flag + emptyDir for read-only rootfs) is implemented and unit-tested. Manifest transformation (`internal/transform` — `patch` strategic-merge/JSON Patch, `rewriteWebhookURL`, `filterKinds`) is implemented and unit-tested. Dual-cluster SSA delivery (`internal/deliver` — SSAApplier with ForceOwnership, per-kind health, owned-by-guarded prune) and seed/shoot client factories (`internal/clients`) are implemented. The reconciler performs the full render→transform→apply→prune→status pipeline with finalizer-driven deletion. Equivalence tests (`internal/equivalence`) compare the operator's rendered+transformed output against a golden render from `sapcc/helm-charts` at a pinned SHA for four Helm operators (metal-operator, boot-operator, argora-operator, khalkeon); these subtests are opt-in behind `RUN_EQUIVALENCE=1` (the golden render needs the internal-only keppel OCI registry, unreachable from public CI runners), while the comparator's normalization/allowlist unit tests always run offline; ipam-capi is deferred pending kustomize source extensions.

## Purpose

Replaces the current pattern where each operator (`metal-operator`, `boot-operator`, `argora-operator`, `khalkeon`, `ipam-capi`) has:

- A wrapper Helm chart in `sapcc/helm-charts/system/<operator>-remote/` with baked-in generated content (~30k lines of committed rendered YAML across five operators)
- A `make build-<operator>-remote` Makefile target with bespoke `sed`/`yq` transformations
- Three separate remote-delivery mechanisms: ManagedResource + gardener-resource-manager (for CRDs/RBAC), webhook-injector reading a ConfigMap (for WebhookConfigurations), webhook-injector patching caBundles (for cert rotation)

Goals:
- Zero `make build-` targets
- Zero committed generated YAML
- Single remote-delivery mechanism (operator applies directly)
- One artifact per operator, self-contained
- Uniform pattern across all 5 operators regardless of upstream format (Helm or kustomize)

## Design summary

**Model C, Option 2 delivery, two-render pattern**. Full design in [`docs/design.md`](docs/design.md).

- Operator watches `DualDeploymentOperator` CRs
- **Renders the source twice per reconcile** — once for seed, once for shoot — using mode-specific configuration (Helm: `seedValues`/`shootValues`; kustomize: `seedPath`/`shootPath` selecting overlay directories)
- Applies 3 per-render transformations to each render independently: `patch` (strategic-merge or JSON Patch DSL), `rewriteWebhookURL` (typed; rewrites webhook and conversion-webhook URLs, also rewrites `.spec.conversion.webhook.clientConfig` on CRDs), `filterKinds` (typed)
- No cross-stream scope: WebhookConfigurations are applied directly to the shoot with `caBundle` unset; the webhook-injector patches `caBundle` in place via target patch mode ([webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14)), with disjoint SSA field ownership
- No split step, no routing rules — each render goes entirely to its target cluster
- Applies seed render's output to the seed cluster (in-cluster client)
- Applies shoot render's output to the shoot cluster (kubeconfig from a Gardener token-requestor Secret)
- Tracks per-resource health, drift-corrects on periodic reconcile

## Architecture

The following diagrams reflect the current design (revision 7). Both are editable in [draw.io / diagrams.net](https://app.diagrams.net).

![dual-deployment-operator reconcile dataflow (two-render, r7)](assets/architecture-dataflow.drawio.svg)

*Reconcile dataflow: the operator renders the source twice (seed + shoot), applies three per-render transforms, and applies each render directly to its target cluster via server-side apply. The webhook-injector sidecar (target patch mode) patches only `.caBundle` on labeled objects on the shoot — it is not a delivery path for WebhookConfigurations.*

![dual-deployment-operator per-shoot deployment topology](assets/architecture-topology.drawio.svg)

*Deployment topology: one operator Pod per `shoot--cp--*` namespace, with two Kubernetes clients (seed in-cluster + shoot via Gardener token-requestor kubeconfig). The webhook-injector sidecar is present only for `metal-operator` and `ipam-capi` (the two operators with webhooks). See [`docs/design.md`](docs/design.md) for full design detail.*

## Deployment topology

Per-shoot. One operator Pod per shoot-cp namespace (`shoot--cp--*`), watching CRs in its own namespace, with two Kubernetes clients (seed in-cluster + shoot via kubeconfig).

## Quick start

The scaffold, CRD types, and reconciler are implemented. Build and run unit tests:

```bash
make build
make test
```

The scaffold was initialized with:

```bash
kubebuilder init --domain cc.sap --repo github.com/SAP-cloud-infrastructure/dual-deployment-operator
kubebuilder create api --group dual-deployment-operator --version v1alpha1 --kind DualDeploymentOperator
```

See [`docs/implementation.md`](docs/implementation.md) for phase-by-phase steps.

## Documentation

| File | Purpose |
|---|---|
| [`docs/design.md`](docs/design.md) | Full design (revision 7, two-render + hybrid patch DSL). Architecture, CRD, transformations, delivery, migration plan, verification. |
| [`docs/context.md`](docs/context.md) | Conversation history and design-decision rationale. Why Model C over B; why Option 2 over Option 1 or 3; why per-shoot topology; why two-render over single-render+split. |
| [`docs/implementation.md`](docs/implementation.md) | Phase-by-phase implementation guide with concrete Go interfaces, file layout, and dependency list. |
| [`docs/related-artifacts.md`](docs/related-artifacts.md) | Links: tracking issue, archived POC, current wrapper charts, upstream repos. |

## Key references

- Tracking issue: [cc/unified-kubernetes#1294](https://github.wdf.sap.corp/cc/unified-kubernetes/issues/1294)
- Original problem: [cc/unified-kubernetes#1169](https://github.wdf.sap.corp/cc/unified-kubernetes/issues/1169)
- Archived kustomize POC: [sapcc/helm-charts#11633](https://github.com/sapcc/helm-charts/pull/11633)
- Design doc branch: [`docs/dualdeploymentoperator-design`](https://github.com/sapcc/helm-charts/tree/docs/dualdeploymentoperator-design/docs/dual-deployment-operator) in `sapcc/helm-charts`

## Naming conventions

- **Project/operator name**: `dual-deployment-operator` (kebab-case)
- **Kubernetes kind**: `DualDeploymentOperator` (CamelCase, per K8s convention)
- **API group**: `dual-deployment-operator.cc.sap`
- **Version**: `v1alpha1` initially; progression documented in design §9.5

## Support, Feedback, Contributing

This project is open to feature requests/suggestions, bug reports etc. via [GitHub issues](https://github.com/SAP-cloud-infrastructure/dual-deployment-operator/issues). Contribution and feedback are encouraged and always welcome. For more information about how to contribute, the project structure, as well as additional contribution information, see our [Contribution Guidelines](CONTRIBUTING.md).

## Security / Disclosure
If you find any bug that may be a security problem, please follow our instructions at [in our security policy](https://github.com/SAP-cloud-infrastructure/dual-deployment-operator/security/policy) on how to report it. Please do not create GitHub issues for security-related doubts or problems.

## Code of Conduct

We as members, contributors, and leaders pledge to make participation in our community a harassment-free experience for everyone. By participating in this project, you agree to abide by its [Code of Conduct](https://github.com/SAP/.github/blob/main/CODE_OF_CONDUCT.md) at all times.

## Licensing

Copyright 2026 SAP SE or an SAP affiliate company and dual-deployment-operator contributors. Please see our [LICENSE](LICENSE) for copyright and license information. Detailed information including third-party components and their licensing/copyright information is available [via the REUSE tool](https://api.reuse.software/info/github.com/SAP-cloud-infrastructure/dual-deployment-operator).
