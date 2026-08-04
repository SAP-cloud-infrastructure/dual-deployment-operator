<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Verification Report: remove-http-helm-repo

Phase 7.7 — make the Helm `ChartLoader` OCI-only. Verified against the three delta specs (`helm-chart-loader`, `source-id-resolution`, `cel-admission-validation`), `design.md`, and `plan.md`.

## Summary

| Dimension | Status |
|---|---|
| Completeness | 5/5 tasks complete; 3/3 modified capabilities implemented |
| Correctness | 3/3 requirements implemented; 6/6 spec scenarios covered by tests |
| Coherence | All design decisions followed; no pattern deviations |

**Final assessment: All checks passed. Ready for archive.**

## Completeness

**Task completion** — all 5 plan task-groups marked `[x]` and independently verified against disk:
- Task 1 (CEL rule) — commit `c05cbf0`
- Task 2 (loader OCI-only) — commit `1697571`
- Task 3 (test cleanup + rejection tests) — commit `ed3de68`
- Task 4 (docs) — commit `e23ec29`
- Task 5 (full gate) — build exit 0, full envtest suite `ok`, golangci-lint 0 issues, codegen clean, `openspec validate` valid

**Spec coverage** — all three modified capabilities implemented (evidence below).

## Correctness

### `cel-admission-validation` — ADDED "Helm repo OCI-scheme enforced by CEL"
- Rule present in source: [`api/v1alpha1/dualdeploymentoperator_types.go:46`](../../../api/v1alpha1/dualdeploymentoperator_types.go) — `self.repo.startsWith('oci://')`, message "helm repo must be an oci:// reference".
- Regenerated into CRD: `config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml:126`.
- Scenarios covered (envtest, `internal/webhook/v1alpha1/dualdeploymentoperator_webhook_test.go`): http(s):// rejected (:79), scheme-less rejected (:86), oci:// accepted (:93). All pass under the full suite.

### `helm-chart-loader` — MODIFIED "Production Helm ChartLoader with scheme dispatch" (OCI-only)
- `Load` dispatch OCI-only + reworded default: `internal/source/helmloader.go:65,68`.
- `pullHTTP` deleted (grep clean). `hostOf`, `rejectURLCredentials`, `httpClient` field retained and still used by the OCI path (confirmed by final review at `helmloader.go:35-37,120-122,185-187`).
- Scenarios covered: OCI pull path intact (existing OCI tests pass); non-OCI rejected with clear error — `TestHelmLoader_Load_RejectsHTTPScheme` (`helmloader_test.go:467`) asserts the error contains `oci://`.

### `source-id-resolution` — MODIFIED (Helm ResolveID OCI-only) + REMOVED "Resolve classic HTTP Helm repos via the index"
- `resolveHTTPID` deleted (grep clean); `resolveOCIDigest` retained (`helmloader.go:175`); `ResolveID` dispatch OCI-only + reworded default (`helmloader.go:168,171`); `repoScope` OCI-only + reworded default (`helmloader.go:154,157`).
- Scenario covered: `TestHelmLoader_ResolveID_RejectsHTTPScheme` (`helmloader_test.go:475`) asserts the error contains `oci://`.

## Coherence

**Design decisions followed:**
- Enforcement at admission via CEL (not the pass-through webhook stub) — implemented as a CEL `XValidation`, matching the kustomize `url` rule style. ✓
- `oci://`-prefix allowlist (not http blocklist). ✓
- Loader unsupported-scheme error kept as backstop (all three `default` arms reworded to name oci://). ✓
- Compiler-driven import cleanup — only genuinely-unused imports removed (`helm.sh/helm/v3/pkg/repo`, `sigs.k8s.io/yaml`); build + lint green. ✓
- Render-cache key struct intentionally NOT collapsed — only `repoScope` lost its `http:` arm; `keyParts` unchanged. ✓

**Scope discipline (out-of-scope surfaces untouched):**
- Kustomize git transport unchanged — `TestGitResolverHTTPSBasicAuth` preserved (`gitresolver_authed_test.go`). ✓
- Phase 7.6 subchart-dependency HTTP(S)-repo fail-closed rejection intact — `helmloader_deps_test.go:80` still present and passing; this is a distinct, still-valid feature. ✓
- Docs (README/AGENTS/design.md §3.3/context.md Revision 9) state OCI-only without contradicting the still-true Phase 7.6 subchart-dep HTTP rejection note. ✓

**Independent reviews:** per-task spec-compliance + code-quality reviews passed for Tasks 1–3; final holistic `code-reviewer` over `origin/main..HEAD` returned APPROVED with no findings.

## Issues

None. No CRITICAL, WARNING, or SUGGESTION issues.
