<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Verification Report: deployment-chart-helm

Verifies the implementation against the change artifacts (specs, design, plan).
Evidence gathered against the implementation worktree
(`.worktrees/deployment-chart-helm-impl`, branch `feature/deployment-chart-helm-impl`;
commits `db11b6b`, `505fbdd`, `844d65f`). Helm renders use the prebuilt
`bin/kustomize` (`KUSTOMIZE` override) because the plugin runs `make build-installer`.

## Summary

| Dimension    | Status |
|--------------|--------|
| Completeness | 45/45 plan steps complete; 4/4 spec capabilities implemented |
| Correctness  | 4/4 capabilities' requirements satisfied by rendered chart + config |
| Coherence    | Design followed; helm↔kustomize install parity achieved; `openspec validate` PASS |

Final assessment: **All checks passed. Ready for archive.**

## Completeness

**Plan tasks**: all 45 checkboxes in `plan.md` complete. Tasks 1–7 were executed and
each verified against disk this session (build/lint/helm/test), then the trailing
completion checkboxes were marked.

**Spec coverage** — one delta capability per spec file, all implemented:

- `controller-helm-chart` — chart generated at `chart/` via `helm/v2-alpha`; `PROJECT`
  records the plugin (`output: .`); CRD ships as a toggled template under
  `chart/templates/crd/`; `helm lint` clean.
- `controller-chart-image` — `manager.image.repository = ghcr.io/SAP-cloud-infrastructure/dual-deployment-operator`, tag defaults to `.Chart.AppVersion` (`0.1.0`), keppel-free, overridable.
- `controller-chart-rbac` — broad seed-applier `ClusterRole` (from `+kubebuilder:rbac`
  markers) + `ClusterRoleBinding`; subject namespace `{{ .Release.Namespace }}`; static
  seed-global names.
- `controller-chart-deployment` — three Gardener egress pod labels; `/healthz` +
  `/readyz` probes; `--leader-elect=true`; `replicas: 1`; no chart-cache volume;
  namespace-agnostic (per-shoot); kustomize parity requirement.

## Correctness

Requirement→evidence mapping (all rendered/asserted):

- **CRD toggled template**: `chart/templates/crd/dualdeploymentoperators.dual-deployment-operator.cc.sap.yaml` gated by `{{- if .Values.crd.enabled }}`; default render = 1 CRD, `--set crd.enabled=false` = 0; `crd.keep` → `helm.sh/resource-policy: keep`.
- **Image**: rendered `image: "ghcr.io/SAP-cloud-infrastructure/dual-deployment-operator:0.1.0"`; override to keppel-mirror path + pinned tag verified; no `keppel` in chart manifests.
- **RBAC**: rendered `kind: ClusterRoleBinding` at default (`rbac.namespaced=false`), `RoleBinding` at `true`; subject in the release namespace; applier `ClusterRole` rules byte-identical to `config/rbac/role.yaml` (125 lines) covering deployments/services/configmaps/serviceaccounts/roles/rolebindings/clusterroles/clusterrolebindings/networkpolicies/CRDs/webhookconfigs + `bind;escalate`.
- **Deployment**: 3 `networking.gardener.cloud/to-*: allowed` labels; both probes; `--leader-elect=true`, `--health-probe-bind-address`, `--metrics-bind-address`; `replicas: 1`; no `source-cache` volume.
- **Leader election**: `cmd/main.go` sets `LeaderElectionReleaseOnCancel: true`.

Scenario coverage: rendered-manifest assertions cover the spec scenarios
(default/disabled CRD, image default/override, ClusterRoleBinding vs RoleBinding toggle,
egress-label presence, per-shoot namespace independence).

## Coherence

- **Design adherence**: A1 templated-CRD decision matches design.md §9.7 (amended);
  keppel-free image with wrapper override matches §9.7; broad applier RBAC + single-install-per-seed matches §3.6.7.
- **helm↔kustomize install parity** (user requirement + `controller-chart-deployment`
  spec): `config/default` is namespace-portable (no `Namespace` object); an overlay
  `namespace: shoot--cp--x` rewrites all 8 namespaced resources' `metadata.namespace`
  and both `ClusterRoleBinding` subjects, zero lingering `-system`. `config/dev` re-adds
  the `Namespace` for `make deploy`. `config/dev` and the chart render the same resource
  set modulo the dev-only `Namespace` object and the Helm release-name prefix; applier
  ClusterRole rules, manager args, probes, and egress labels are equivalent. Pattern is
  community-idiomatic (kustomize namespace transformer ≡ `helm --namespace`; `kubectl -n`
  is not the portability mechanism because it does not rewrite RBAC subject namespaces).
- **Writable source-scratch volume** (`controller-chart-deployment` spec): the manager runs
  `readOnlyRootFilesystem: true`, so the Helm loader's per-render `os.MkdirTemp` would fail
  without a writable mount. Both helm and kustomize render a `source-scratch` `emptyDir`
  (`sizeLimit: 256Mi`) mounted at `/tmp/ddo-source` plus `--source-scratch-dir=/tmp/ddo-source`;
  the two renders are byte-identical on those lines and `readOnlyRootFilesystem: true` is
  preserved. The operator side (`--source-scratch-dir` flag + `source.NewHelmLoader`) shipped
  in the Phase 7.6 `helm-subchart-dependency-resolution` change; this closes the chart-side gap
  it flagged. Distinct from the deferred Phase 7.5 `source-cache` volume.
- **Gates**: `go build ./...` exit 0; `helm lint` clean; `golangci-lint` 0 issues;
  controller+api tests pass; codegen drift = only intended `config/rbac/role.yaml` +
  `config/manager/manager.yaml`; REUSE compliant (`chart/**` covered);
  `openspec validate deployment-chart-helm` PASS.

## Issues

- CRITICAL: none.
- WARNING: none.
- SUGGESTION: `dist/install.yaml` generated from `config/default` is inherently
  single-namespace (static YAML cannot be namespace-portable); this is expected and the
  Helm chart / kustomize overlay are the portable paths. Pre-existing `testdata/fixtures/**`
  REUSE coverage is handled by the committed `REUSE.toml` (`testdata/**`), unrelated to
  this change.
