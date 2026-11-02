# ipam-capi equivalence fixture

This is the fifth equivalence fixture in the suite. It proves that the operator's
new kustomize render for the **ipam-capi-remote DDO overlay** (`system/kustomize/ipam-capi-remote-ddo/`)
produces identity-equivalent output to today's **ipam-capi-remote Helm chart**
(`system/ipam-capi-remote/`) for the shoot/resource kinds that are not subject to
intentional r7 migration deltas.

Golden side: `git clone` + `helm dependency build` + `helm template system/ipam-capi-remote/`,
then unwrap ManagedResources → shoot object set.

Operator side: `source.From` → `Render` (KustomizeSource, shoot mode only via
`CaptureShootOnly`) → `transform.Build` pipeline.

## About the DDO overlay (PR #13218)

The operator-side kustomize source is `system/kustomize/ipam-capi-remote-ddo/`,
the real DDO migration overlay from **sapcc/helm-charts PR #13218**
(`feat/ipam-capi-remote-ddo-m-qa-de-200` branch, SHA `726f6ccee8eef46152f8abc7cc0b107e6e7f9596`).

**PR #13218 is currently a DRAFT (as of 2026-11-02).** The overlay exists on the PR
branch but NOT on `master`. Both `system/ipam-capi-remote/` (Helm golden) and
`system/kustomize/ipam-capi-remote-ddo/` (DDO overlay) coexist at the PR branch SHA,
which is why both fixture sides use the same pinned commit.

**Once PR #13218 is merged**, update `sha:` in `fixture.yaml` and `url ?ref=` in
`cr.yaml` to a post-merge commit on `main` where both paths are present.

## Scope: shoot overlay only

**Only the `shoot/` overlay is tested.** The `seed/` overlay is NOT covered by this fixture.

Unlike the original ipam-capi-remote source (deferred in `docs/implementation.md`),
the new DDO overlay's `seed/` overlay IS buildable with `KustomizeSource` — it uses only
`../base` and `../additions/seed`, both vendored, with no repo-root-relative patches.
The original deferral reason no longer applies to `ipam-capi-remote-ddo`.

The shoot-only scope matches the task-8 brief. A follow-up fixture can add seed coverage.

## Migration deltas: what the fixture does and does not check

This fixture compares the new DDO overlay's output against the legacy Helm chart.
The DDO overlay intentionally changes some fields as part of the r7 migration:

| Object | Change | Handling |
|--------|--------|----------|
| Pool CRDs (4x) | `spec.conversion`: `service`→`null`, explicit webhook URL | `migrationExclusions` (not compared) |
| Core CRDs (3x) | `spec.conversion`: `{strategy: None}` patch added | `migrationExclusions` (not compared) |
| Webhook configurations | `dual-deployment-operator.cc.sap/webhook-injector` label added | `ignoreLabels` (stripped before compare) |
| Pool CRDs | Same webhook-injector label | `ignoreLabels` |
| `ipam-capi-remote-webhook-injector` ClusterRole | RBAC rules updated for r7 injector | `migrationExclusions` (not compared) |
| Leader-election RBAC (4 objects) | Dropped (manager runs `--leader-elect=false`) | `knownDivergences` (golden-only, expected missing) |

Everything else (CRD schemas, remaining RBAC, ServiceAccounts, WebhookConfiguration
policies other than labels) is compared normally and must match.

`migrationExclusions` objects are NOT regression-guarded by this fixture. The PR's own
test suite (`tests/crd_diff_test.sh`, `tests/migration_contract.py`) covers those fields
independently.

## Network requirements

This test is gated on `RUN_EQUIVALENCE=1` because it requires outbound network access:

- **Golden side (Helm)**: git clone of `github.com/sapcc/helm-charts` + `helm dependency build`
  pulling subcharts from the internal keppel OCI registry. Same requirement as the other
  four Helm fixtures.
- **Operator side (kustomize)**: git clone of `github.com/sapcc/helm-charts` (same SHA) +
  krusty fetching the provider directory base
  (`cluster-api-ipam-provider-in-cluster//config/crd?ref=<SHA>`, a git directory base)
  and three core CRDs via SHA-pinned `raw.githubusercontent.com` HTTP file URLs.
  The DDO operator code handles both `refKindRemoteGitDir` and `refKindRemoteFile`
  correctly per `internal/source/remotepolicy.go`. All kustomize sources are public/anonymous.

## Files

| File | Purpose |
|------|---------|
| `fixture.yaml` | Pinned SHA (PR branch), chart path, kustomize overlay path, scope config, migration exclusions |
| `cr.yaml` | `DualDeploymentOperator` CR targeting the real DDO kustomize overlay |
| `overlay-values.yaml` | Per-cluster Helm overlay values for the golden render |
| `README.md` | This file |
