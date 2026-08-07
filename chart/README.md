<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# dual-deployment-operator (controller chart)

Chart 1 — the upstream controller Helm chart for `dual-deployment-operator`. It ships the
operator Deployment, ServiceAccount, seed-side RBAC, leader-election Role, NetworkPolicy,
metrics, and the `DualDeploymentOperator` CRD. It is registry-agnostic and per-shoot
deployable (one Helm release per `shoot--cp--*` namespace via `--namespace`).

A downstream wrapper (`dual-deployment-operator-remote`, in `sapcc/helm-charts`) consumes
this chart as a subchart, overrides the image to the keppel mirror, and templates the
`DualDeploymentOperator` CR instances plus the Gardener glue. See `docs/design.md` §9.7.

## Generation — do not hand-author

This chart is generated from the `config/*` kustomize scaffold via the kubebuilder Helm
plugin:

```sh
KUSTOMIZE="$PWD/bin/kustomize" kubebuilder edit --plugins=helm/v2-alpha --output-dir=.
```

The `KUSTOMIZE` override points at a prebuilt kustomize v5 CLI because the plugin runs
`make build-installer` internally and the Makefile's default `KUSTOMIZE ?= go run
sigs.k8s.io/kustomize/kustomize/v5` has no resolvable go.sum entry. Install it once with
`GOBIN="$PWD/bin" go install sigs.k8s.io/kustomize/kustomize/v5@v5.8.1`.

### Post-generation customizations (re-apply after any `--force` regen)

The plugin owns `chart/**`; a `--force` regeneration overwrites the files below, so these
edits MUST be re-applied afterward (verify with `helm template`):

- **Image** (`values.yaml`): `manager.image.repository = ghcr.io/SAP-cloud-infrastructure/dual-deployment-operator` (keppel-free; the wrapper overrides to the mirror).
- **Gardener egress labels** (`values.yaml` `manager.pod.labels`): `networking.gardener.cloud/to-dns`, `to-public-networks`, `to-private-networks`, `to-runtime-apiserver`, and `networking.resources.gardener.cloud/to-all-istio-ingresses-istio-ingressgateway-tcp-9443: allowed` — required for pod egress under the seed's deny-all NetworkPolicy (`to-runtime-apiserver` lets the operator reach the seed kube-apiserver for leader election; `to-all-istio-ingresses-istio-ingressgateway-tcp-9443` lets it reach the shoot kube-apiserver via its external host, routed through the istio ingress LB by SNI, to apply the shoot render — mirrors the prod metal-operator-remote label set; without either, the respective calls time out).
- **Manager binding** (`templates/rbac/manager-rolebinding.yaml`): renders a `ClusterRoleBinding` at `rbac.namespaced=false` (default) and a `RoleBinding` at `rbac.namespaced=true`.
- **Broad seed-applier RBAC**: driven by `+kubebuilder:rbac` markers in `internal/controller/` → `config/rbac/role.yaml`; regenerate with `make manifests` before re-running the plugin.
- **Shoot-applier bootstrap + token-requestor Secret** (`templates/shoot-rbac/`, `values.yaml` `shootRbac`): hand-authored, NOT plugin-generated — re-add after any `--force` regen. Gated `shootRbac.enabled: false` by default; when enabled (by the wrapper chart, with `serviceAccountName`/`serviceAccountNamespace` supplied) it emits (1) a GRM `ManagedResource` that seeds the broad apply-scoped shoot ClusterRole+binding for the operator's shoot SA, and (2) the Gardener token-requestor `Secret` the operator reads for shoot credentials (`spec.shootAccess.secretName`, default `<release>-remote-kubeconfig`) — the Secret mints the token for the same SA the bootstrap binds (design.md §3.6, §3.6.7). The webhook-injector shoot RBAC stays in the workload chart, not here.

## Single-install-per-seed

The chart's cluster-scoped objects (CRD, applier `ClusterRole`/`ClusterRoleBinding`) carry
static, release-independent (seed-global) names. Installing two releases of this chart into
different namespaces on the **same seed** collides on those cluster-scoped names. This is the
documented single-install-per-seed contract (`docs/design.md` §3.6.7), matching current
production. Set `crd.enabled=false` on additional releases if the CRD is managed once
separately.
