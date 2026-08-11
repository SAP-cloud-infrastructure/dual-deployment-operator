[![REUSE status](https://api.reuse.software/badge/github.com/SAP-cloud-infrastructure/dual-deployment-operator)](https://api.reuse.software/info/github.com/SAP-cloud-infrastructure/dual-deployment-operator)

# dual-deployment-operator

A Kubernetes operator for **Gardener** environments that deploys a single Helm chart or
kustomize source across **two clusters at once** — the seed (in-cluster) and its shoot
(remote) — from one `DualDeploymentOperator` custom resource.

You point it at a source, tell it which namespace to use on the shoot, and give it
credentials to reach the shoot. On every reconcile it renders the source **twice** (once
per cluster), applies your transformations, and server-side-applies each render to its
target cluster. It tracks per-resource health, drift-corrects on a periodic reconcile, and
cleans both clusters up on deletion.

## Why

Splitting an operator's manifests into a seed half and a shoot half traditionally meant
committed generated YAML, bespoke `sed`/`yq` build targets, and multiple separate
remote-delivery mechanisms per operator. `dual-deployment-operator` replaces all of that
with one declarative CR and one delivery path: the operator applies directly to both
clusters — no committed rendered YAML, no `make build-` targets, one artifact per operator.

## How it works

```
                          ┌─────────────────────────────────────────┐
  DualDeploymentOperator  │  render(seed)  → transform → SSA apply ──┼──▶ seed cluster
  (one CR)  ──────────────┤                                          │   (in-cluster)
                          │  render(shoot) → transform → SSA apply ──┼──▶ shoot cluster
                          └─────────────────────────────────────────┘   (kubeconfig from
                                                                          Gardener Secret)
```

- **Two renders per reconcile.** Helm sources use `seedValues`/`shootValues`; kustomize
  sources use `seedPath`/`shootPath` to select overlay directories.
- **Per-render transformations** (optional): `patch` (strategic-merge or JSON Patch),
  `rewriteWebhookURL`, and `filterKinds`.
- **Server-side apply** to each cluster with drift correction and owned-by-guarded pruning.
- **Safe teardown.** On CR deletion a finalizer blocks until shoot cleanup succeeds (or you
  set an explicit force-delete annotation), so deletion is safe even when the shoot is
  temporarily unreachable.

### Architecture

![dual-deployment-operator reconcile dataflow](assets/architecture-dataflow.drawio.svg)

*Reconcile dataflow: the operator renders the source twice (seed + shoot), applies the
per-render transforms, and applies each render directly to its target cluster via
server-side apply.*

![dual-deployment-operator per-shoot deployment topology](assets/architecture-topology.drawio.svg)

*Deployment topology: one operator Pod per `shoot--cp--*` namespace, with two Kubernetes
clients — seed (in-cluster) and shoot (via a Gardener token-requestor kubeconfig).*

Full architecture, rationale, and the transformation DSL are in
[`docs/design.md`](docs/design.md).

## Install

The operator is deployed **per shoot** — one Helm release per `shoot--cp--<name>` namespace
on the seed.

```bash
helm install dual-deployment-operator ./chart \
  --namespace shoot--cp--<name> --create-namespace \
  [--set manager.image.tag=<version>]
```

The image defaults to `ghcr.io/SAP-cloud-infrastructure/dual-deployment-operator`. See
[`chart/README.md`](chart/README.md) for all values, the required Gardener egress labels,
and the single-install-per-seed contract.

## Usage

Create a `DualDeploymentOperator` CR describing your source, the shoot namespace, and how to
reach the shoot.

### Helm source

```yaml
apiVersion: dual-deployment-operator.cc.sap/v1alpha1
kind: DualDeploymentOperator
metadata:
  name: my-operator
  namespace: shoot--cp--example        # seed render/delivery uses this namespace
spec:
  source:
    helm:
      repo: oci://registry.example.com/charts   # MUST be an oci:// reference
      name: my-operator
      version: 1.2.3
      seedValues:                                # values applied to the seed render
        mode: seed
      shootValues:                               # values applied to the shoot render
        mode: shoot
  shootNamespace: kube-system          # target namespace for the shoot render
  shootAccess:
    secretName: my-operator-shoot-access   # Gardener token-requestor Secret (this namespace)
    server: https://api.shoot.example.com
  applyOrder: ShootFirst               # ShootFirst (default) | SeedFirst
```

### Kustomize source

```yaml
spec:
  source:
    kustomize:
      url: https://github.com/org/repo//base?ref=<pinned-sha>   # ref= is required
      seedPath: overlays/seed
      shootPath: overlays/shoot
  shootNamespace: kube-system
  shootAccess:
    secretName: my-operator-shoot-access
    server: https://api.shoot.example.com
```

### Transformations (optional)

Applied to each render independently, in declaration order. All are silent no-ops when they
match nothing, so a transform targeting a kind absent from one render does not fail it.

```yaml
spec:
  transformations:
    - patch:                          # strategicMerge OR jsonPatch
        target: { kind: Deployment, name: my-operator }
        strategicMerge:
          spec: { replicas: 2 }
    - rewriteWebhookURL:
        urlPrefix: https://webhook.shoot.example.com
    - filterKinds:
        kinds: [ValidatingWebhookConfiguration]
```

### Private sources

For an authenticated OCI registry or git HTTPS repo, add `authSecretRef` under the source
and create a Secret (in the CR's namespace) with `username`/`password` or `token` keys:

```yaml
spec:
  source:
    helm:
      repo: oci://registry.example.com/charts
      name: my-operator
      version: 1.2.3
      authSecretRef: { name: my-registry-creds }
```

## Checking status

The CRD exposes printer columns, so `kubectl get` shows the current state at a glance:

```console
$ kubectl get ddo -n shoot--cp--example
NAME          READY   REASON             AGE
my-operator   True    ReconcileSuccess   5m
```

The `Ready` condition always reflects the situation right now:

| Ready | Reason | Meaning |
|---|---|---|
| `True` | `ReconcileSuccess` | All managed resources applied and healthy |
| `False` | `Progressing` | One or more resources still coming up |
| `False` | `ResourcesDegraded` | One or more resources failed to apply / are unhealthy |
| `False` | `WaitingForShootCredentials` | Shoot token/CA not yet populated by Gardener (benign bootstrap wait) |
| `False` | `ShootApplyFailed` / `ShootUnreachable` | The shoot render could not be applied / the shoot is unreachable |
| `False` | `Deleting` | The CR is being deleted; teardown is in progress |

Full status (`kubectl get ddo my-operator -o yaml`) also lists per-resource health under
`status.seedResources` / `status.shootResources`, and each condition carries an
`observedGeneration` so you can tell whether the status reflects your latest spec change.

## Deleting

`kubectl delete ddo my-operator` triggers a finalizer that tears down both renders. If the
shoot is unreachable, deletion **blocks** (the CR shows a `ShootCleanup=Blocked` condition)
and retries rather than orphaning shoot resources. To force removal and accept orphaned
shoot resources:

```bash
kubectl annotate ddo my-operator dual-deployment-operator.cc.sap/force-delete=true
```

By default CRDs in the render are retained on delete; set `spec.retentionPolicy.crds: Delete`
to remove them too.

## Local development

Run the operator against your current kubeconfig. The **seed** client uses your local
kubeconfig; the **shoot** client is still built from the `shootAccess` Secret referenced by
each CR.

```bash
make install                      # install the CRD into the current cluster
make run                          # run the operator locally (uses current kubeconfig)

# point at a specific kubeconfig:
go run ./cmd/main.go --kubeconfig ~/.kube/other-config
# or:
KUBECONFIG=~/.kube/other-config make run
```

Kubeconfig resolution order: `--kubeconfig` flag → `KUBECONFIG` env → `$HOME/.kube/config` →
in-cluster config.

Build and test:

```bash
make build        # build the binary
make test         # run unit + envtest suites (Ginkgo/Gomega)
make lint-fix     # auto-fix code style
```

> E2E tests expect a dedicated [Kind](https://kind.sigs.k8s.io/) cluster, not your real
> dev/prod cluster.

## Documentation

| File | Purpose |
|---|---|
| [`docs/design.md`](docs/design.md) | Full architecture, CRD reference, transformation DSL, delivery model, migration plan |
| [`docs/implementation.md`](docs/implementation.md) | Phase-by-phase implementation guide |
| [`docs/context.md`](docs/context.md) | Design-decision rationale |
| [`chart/README.md`](chart/README.md) | Controller Helm chart: values, RBAC, egress labels, install contract |

## Status

`v1alpha1`. The operator is functionally complete — source rendering (OCI Helm + git
kustomize), per-render transformations, dual-cluster server-side apply, finalizer-driven
teardown, and CEL admission validation are implemented and tested. Image publish and
production rollout are in progress.

## Support, Feedback, Contributing

This project is open to feature requests, suggestions, and bug reports via
[GitHub issues](https://github.com/SAP-cloud-infrastructure/dual-deployment-operator/issues).
For more on how to contribute and the project structure, see our
[Contribution Guidelines](CONTRIBUTING.md).

## Security / Disclosure

If you find a bug that may be a security problem, please follow the instructions in our
[security policy](https://github.com/SAP-cloud-infrastructure/dual-deployment-operator/security/policy).
Please do not open GitHub issues for security-related concerns.

## Code of Conduct

By participating in this project, you agree to abide by its
[Code of Conduct](https://github.com/SAP/.github/blob/main/CODE_OF_CONDUCT.md) at all times.

## Licensing

Copyright 2026 SAP SE or an SAP affiliate company and dual-deployment-operator contributors.
Please see our [LICENSE](LICENSE) for copyright and license information. Detailed information
including third-party components and their licensing/copyright information is available
[via the REUSE tool](https://api.reuse.software/info/github.com/SAP-cloud-infrastructure/dual-deployment-operator).
