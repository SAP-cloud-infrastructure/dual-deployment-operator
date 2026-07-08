# dual-deployment-operator

A Kubernetes operator that manages the deployment of split host/remote controllers in Gardener environments. Consumes a Helm chart or kustomize source per operator, renders it, applies typed Go transformations, and applies each half to its target cluster (host = seed, remote = shoot) via server-side apply.

**Status**: pre-implementation. Design finalized (revision 3). Scaffold pending.

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

**Model C with Option 2 delivery**. Full design in [`docs/design.md`](docs/design.md).

- Operator watches `DualDeploymentOperator` CRs
- Renders the CR's chart (Helm SDK) or kustomize source (krusty)
- Applies typed Go transformations in order: `injectInitContainer`, `renameKind`, `rewriteWebhookURL`, `addLabels`, `filterKinds`
- Splits by kind rules + optional target annotation into host / remote / drop buckets
- Applies host resources to the seed cluster (in-cluster client)
- Applies remote resources to the shoot cluster (kubeconfig from a Gardener token-requestor Secret)
- Tracks per-resource health, drift-corrects on periodic reconcile
- webhook-injector retained for cert lifecycle only (SSA field-manager coexistence)

## Deployment topology

Per-shoot. One operator Pod per shoot-cp namespace (`shoot--cp--*`), watching CRs in its own namespace, with two Kubernetes clients (seed in-cluster + shoot via kubeconfig).

## Quick start

Not yet scaffolded. Planned scaffold:

```bash
kubebuilder init --domain cc.sap --repo github.tools.sap/D065300/dual-deployment-operator
kubebuilder create api --group dual-deployment-operator --version v1alpha1 --kind DualDeploymentOperator
```

See [`docs/implementation.md`](docs/implementation.md) for phase-by-phase steps.

## Documentation

| File | Purpose |
|---|---|
| [`docs/design.md`](docs/design.md) | Full design (revision 3). Architecture, CRD, transformations, delivery, migration plan, verification. |
| [`docs/context.md`](docs/context.md) | Conversation history and design-decision rationale. Why Model C over B; why Option 2 over Option 1 or 3; why per-shoot topology. |
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
