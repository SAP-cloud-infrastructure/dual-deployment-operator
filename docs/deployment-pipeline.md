<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Deployment Pipeline Reference: per-shoot install of the `-remote` wrapper charts

**Purpose.** Concrete, verified mechanics of how the fleet installs a `<operator>-remote`
wrapper chart into different `shoot--cp--*` namespaces from the pipeline side. This
complements `design.md` §9.7 (chart structure) and §9.4 (per-cluster CR values) with the
*actual* Concourse `helm-chart-pipeline` + `cc/kube-secrets` structure that `chart 2`
(`dual-deployment-operator-remote`) must plug into.

Verified 2026-07 against `cc/kube-secrets` (`github.wdf.sap.corp/cc/kube-secrets`, branch
`master`) and `sapcc/helm-charts`. This is the model `chart 2` follows unchanged.

> **Scope.** `chart 2`, the deploy pipelines, and `cc/kube-secrets` values live **outside**
> this operator repo (in `sapcc/helm-charts` + `cc/kube-secrets`). `chart 1` (this repo,
> `chart/`) is deliberately namespace-agnostic so a single chart drops into any
> `shoot--cp--*` the pipeline points it at. Nothing here is built in this repo; it is
> recorded so `chart 1`'s namespace/RBAC design stays aligned and the `chart 2` authors
> have the exact contract.

---

## The three layers

Installing a `-remote` subchart into different shoot namespaces is a **pipeline concern**,
external to both charts. Three layers combine:

### 1. Pipeline definition — chart + release + namespace *template* + fan-out filter

`cc/kube-secrets/pipelines/<release>-<class>/pipeline.rb` instantiates the shared
`HelmChartPipeline` template. Verified example
(`pipelines/metal-operator-remote-runtime/pipeline.rb`):

```ruby
require "../_templates/helm-chart-pipeline"

puts HelmChartPipeline.new(
    chart:     "system/metal-operator-remote",   # wrapper chart in sapcc/helm-charts
    release:   "metal-operator-remote",
    namespace: "shoot--cp--m-$REGION",           # namespace TEMPLATE, $REGION per cluster
    vault_enabled: true,
    filter: {
        types:    "runtime",                       # selects the cluster set (fan-out)
        clusters: "*",
        regions:  "*",
        triggers: "NOMATCH",
        gates:    "NOMATCH",
    },
    individual_deploy_buttons: "all",
    auth: :kubelogon,
).render
```

Key facts:

- **Namespace is a per-cluster template, not a value.** `shoot--cp--m-$REGION` expands per
  matched cluster → `shoot--cp--m-qa-de-1` on `rt-qa-de-1`, `shoot--cp--m-eu-de-1` on
  `rt-eu-de-1`, etc. Helm installs with `--namespace <that>`; the wrapper's namespaced
  resources inherit it (they set no hardcoded `metadata.namespace`). This is exactly why
  `chart 1` must stay namespace-agnostic.
- **`filter` is the fan-out.** `types`/`clusters`/`regions` select the cluster set from the
  fleet inventory; the pipeline renders **one deploy job per matched cluster**. That is how
  a single pipeline file installs into many shoot namespaces — not a Helm loop.
- **Per-cluster namespace overrides** use `cluster_settings` when the `$REGION` template
  does not fit. Verified in `pipelines/metal-operator-remote-admin-k3s/pipeline.rb`:

  ```ruby
  namespace: "shoot--ccloud--metal-operator",
  cluster_settings: {
      "a-qa-de-100" => ClusterSettings.new(namespace_override: "shoot--cp--met-op-qa-de-100"),
      "a-qa-de-200" => ClusterSettings.new(namespace_override: "shoot--cp--m-qa-de-200"),
  },
  ```

  So the target namespace comes from the `$REGION` template **or** a per-cluster
  `namespace_override` — never from the chart or its values.

### 2. `cc/kube-secrets` per-shoot values — path-addressed by class/region/shoot

The pipeline resolves per-cluster Helm values from a conventional path where the **file
name == release name** and the **directory == class/region/shoot**:

```
values/helm/<class>/<region>/<shoot>/<release>.yaml
```

Verified for `metal-operator-remote`:

```
values/helm/runtime/qa-de-1/rt-qa-de-1/metal-operator-remote.yaml
values/helm/runtime/eu-de-1/rt-eu-de-1/metal-operator-remote.yaml
values/helm/runtime/eu-de-2/rt-eu-de-2/metal-operator-remote.yaml
values/helm/runtime/eu-de-3/rt-eu-de-3/metal-operator-remote.yaml
values/helm/runtime/na-us-2/rt-na-us-2/metal-operator-remote.yaml
values/helm/admin-k3s/qa-de-1/a-qa-de-200/metal-operator-remote.yaml
```

This file carries the shoot-specific overrides. Verified surface for
`runtime/qa-de-1/rt-qa-de-1/metal-operator-remote.yaml`:

- `env.KUBERNETES_SERVICE_HOST` — the shoot apiserver URL (the genuinely per-shoot value,
  e.g. `api.m-qa-de-1.cp.external.rt-qa-de-1.soil-garden.qa-de-1.cloud.sap`).
- `remote.ca` — the shoot CA bundle (base64).
- controller args (dns zone, registry URL, `--manager-namespace`, …).
- secret refs resolved at deploy via `{{ resolve "vault+kvv2:///secrets/<region>/…" }}`
  (`vault_enabled: true` in the pipeline).

This is the layer that differentiates one shoot from another — the same GitOps model
`design.md` §9.4/§9.7 describe, here with the exact path convention.

### 3. Shared template — assembles the `helm upgrade --install` per cluster

`cc/kube-secrets/pipelines/_templates/helm-chart-pipeline.rb` turns the definition + filter
+ values into one Concourse deploy job per matched cluster (with per-cluster `auth`,
optional `vault_enabled`, `cluster_settings` overrides, and `individual_deploy_buttons`).

---

## End-to-end chain

```
pipeline.rb  (chart + release + namespace template + filter)
  └─ filter selects clusters  →  one deploy job per shoot
       └─ namespace = "shoot--cp--m-$REGION"  (or cluster_settings namespace_override)
            └─ helm upgrade --install <release> <chart>
                 --namespace shoot--cp--m-<region>
                 -f values/helm/<class>/<region>/<shoot>/<release>.yaml
```

---

## What `chart 2` (`dual-deployment-operator-remote`) needs on the pipeline side

To install `chart 2` into different shoot namespaces, add the pipeline side (in
`cc/kube-secrets`), **not** chart logic:

1. **Pipeline**: `pipelines/dual-deployment-operator-remote-<class>/pipeline.rb` with
   `chart: "system/dual-deployment-operator-remote"`, `release:
   "dual-deployment-operator-remote"`, `namespace: "shoot--cp--m-$REGION"` (or
   `cluster_settings` overrides), and a `filter` selecting the target shoots. `auth:
   :kubelogon`, `vault_enabled: true` as needed.
2. **Per-shoot values**:
   `values/helm/<class>/<region>/<shoot>/dual-deployment-operator-remote.yaml` carrying that
   shoot's `spec.source` (chart version / kustomize ref), the `chart 1` image override
   (→ keppel mirror, under the subchart alias), `shootNamespace`, `shootAccess`, and any
   `vault+kvv2://` refs.
3. **Charts stay namespace-agnostic**: `chart 1` and `chart 2` never hardcode the namespace;
   it is injected by `--namespace` at deploy time. Fan-out (one release per shoot) is the
   pipeline's `filter`, never a Helm range.

### Alignment consequences already baked into `chart 1`

- **No hardcoded `metadata.namespace`** on namespaced resources → they inherit the Helm
  release namespace, so `chart 1` drops cleanly into any `shoot--cp--*`.
- **`ClusterRoleBinding` subject uses `{{ .Release.Namespace }}`** and cluster-scoped role
  names are **static/seed-global** → matches the verified `metal-operator-webhook-injector`
  RBAC shape and the single-install-per-seed contract (`design.md` §3.6.7 / §5.7).
- **Keppel-free image default** → `chart 2` overrides `manager.image.repository`/`tag` to the
  keppel mirror under the subchart alias; `chart 1` itself carries no keppel reference.

---

## Open design fork for `chart 2` (decide when building it in `sapcc/helm-charts`)

Whether `chart 2` is **one release with one CR** (a single managed operator per shoot-cp
namespace, mirroring today's one `<operator>-remote` chart per operator) or **one release
templating multiple CRs** (all managed operators in that namespace). The current fleet
topology is one `-remote` chart per operator, so the natural shape is **one release per
(operator × shoot-cp namespace)**. This decision belongs to the `chart 2` change, not to
`chart 1` in this repo.
