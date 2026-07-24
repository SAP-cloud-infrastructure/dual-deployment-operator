<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Verification Report: production-source-loaders

## Summary

| Dimension    | Status                                             |
|--------------|----------------------------------------------------|
| Completeness | 9/9 plan tasks complete; 13/13 requirements implemented |
| Correctness  | 13/13 requirements mapped to code; scenarios covered by tests |
| Coherence    | Design decisions followed; no scope leakage        |

**Final assessment:** All checks passed. No CRITICAL or WARNING issues. Ready for archive.

Gate evidence (re-run on HEAD `c73379d` in the worktree):
- `go build ./...` → exit 0
- `make run-golangci-lint` → `0 issues.`
- `make manifests generate` → no drift (tree clean)
- Full suite `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./...` → all 8 packages `ok`
- Online tier `DDO_ONLINE_TESTS=1 go test -tags online` → real anonymous keppel OCI pull PASS, git tag+SHA resolve PASS, authed-OCI SKIP (needs `DDO_TEST_OCI_*`)
- `grep TODO(production-loaders)\|GetEventRecorderFor cmd/ internal/` → CLEAN (no stub)

---

## Completeness

### Task completion
All 9 plan tasks marked `- [x]` in `plan.md`; 0 incomplete.

### Spec coverage (13 requirements across 4 capabilities)

**crd-types** (3 requirements, MODIFIED + ADDED)
- HelmSource `authSecretRef` field → [`api/v1alpha1/dualdeploymentoperator_types.go`](../../../api/v1alpha1/dualdeploymentoperator_types.go) (+ generated CRD schema + deepcopy)
- KustomizeSource `authSecretRef` field → same file
- `SecretReference` type (name-only, `MinLength=1`, no namespace) → same file

**helm-chart-loader** (3 requirements)
- Scheme dispatch (oci:// + http(s)://; unsupported → error) → [`internal/source/helmloader.go`](../../../internal/source/helmloader.go) `Load`
- Non-nil registry client for anonymous OCI → `pullOCI` (`registry.NewClient` always constructed)
- Pull-to-temp-dir + cleanup, no caching → `Load` (`os.MkdirTemp` + `defer os.RemoveAll`)

**kustomize-root-resolver** (3 requirements)
- Production go-git RootResolver → [`internal/source/gitresolver.go`](../../../internal/source/gitresolver.go) `Resolve`
- Fail-closed pinned ref (tag/branch + SHA) → `fetchPinned` / `fetchSHA`
- Anonymous clone of public repos → `authFor` returns `nil, nil` when no resolver

**source-credentials** (4 requirements)
- Per-source `authSecretRef` resolution → [`internal/source/credentials.go`](../../../internal/source/credentials.go) `CredentialResolver.Resolve` + `credsFromSecretData`
- OCI + HTTP Helm auth via basic creds → `helmloader.go` `pullOCI` (inline `ClientOptBasicAuth`) / `pullHTTP` (`Username`/`Password`)
- git HTTPS auth via basic creds; SSH deferred → `gitresolver.go` `authFor` (`http.BasicAuth`)
- Credential material never leaked → resolve errors wrap only k8s Get errors; no token in logs/errors/cache-key (no cache key exists); no-leak assertions in tests

---

## Correctness

### Requirement → implementation (spot-checked)
- Fixed Secret keys `username`/`password`/`token`; token wins; token-only → user `"git"`; **username-only → anonymous** (`ok: pass != ""`) — `credsFromSecretData` (`credentials.go`), covered by `TestCredentialsFromSecretData`.
- Explicit `authSecretRef` whose resolve fails **surfaces an error** (no silent anonymous fallback) — `credFor`/`authFor` return errors, propagated as `source: resolve credentials: %w`; covered by `TestHelmLoaderSurfacesCredentialError`, `TestGitResolverSurfacesCredentialError`, and the anonymous-guard tests.
- `From` binds per-source creds via **fresh copied loader** (`withHelmCreds`/`withGitCreds`), never mutating shared singletons — `source.go`; concurrency-safe (verified `go test -race ./internal/source/...`).
- Reconciler injects per-CR `CredentialResolver{Client, cr.Namespace}` on a copied `Deps` — [`internal/controller/dualdeploymentoperator_controller.go`](../../../internal/controller/dualdeploymentoperator_controller.go).
- Recorder migrated to events API (`Eventf`) with `events.k8s.io` RBAC — controller + `config/rbac/role.yaml`.

### Scenario coverage (30 scenarios total)
- helm scheme dispatch + non-nil client + anonymous OCI → `TestHelmLoaderRejectsUnknownScheme`, `TestHelmLoaderOnlineOCIAnonymous` (online).
- git tag/branch + SHA fail-closed → `TestGitResolverTagAndSHA`, `TestGitResolverFailsClosedOnBadRef`.
- git HTTPS auth header emission + no-leak → `TestGitResolverHTTPSBasicAuth`.
- credential threading to concrete loader → `TestFromThreadsHelmCredsToLoader`.
- authed OCI (gated) → `TestHelmLoaderOCIAuthed` (`//go:build online`, skips without `DDO_TEST_OCI_*`).
- CRD `authSecretRef` optional + SecretReference validation → `TestAuthSecretRefFields` + CEL/CRD schema.

---

## Coherence

### Design decisions followed
- **No caching in Phase 7** — confirmed no LRU/emptyDir/chart-cache code; both loaders fetch fresh each reconcile. (`registry.ClientOptEnableCache(true)` is Helm's auth-token cache, not a chart cache. Caching deferred to Phase 7.5 per design.)
- **Classic HTTP(S) repos are a first-class tested path** — `pullHTTP` implemented + scheme-dispatched.
- **go-git for root fetch, fail-closed** — implemented; SHA path documented Depth:1 limitation (non-tip ancestor → fail-closed error, acceptable for pinned sources).
- **git HTTPS-only auth, SSH deferred** — only `http.BasicAuth`; no SSH.
- **Per-source `authSecretRef` on CRD (not operator-global)** — implemented, mirrors `shootAccess.secretName`.
- **Interfaces unchanged** — `ChartLoader.Load`, `RootResolver.Resolve`, `Source.Render` signatures intact; `Deps` gained optional `CredentialResolver`.
- **Recorder migration as a distinct task** — done separately (Task 8) with its own commit + RBAC regen.

### Pattern consistency
- New files follow repo SPDX header + `internal/source` package conventions.
- Generated artifacts (`zz_generated.deepcopy.go`, `config/crd/bases/*`, `config/rbac/role.yaml`) regenerated via `make manifests generate`, not hand-edited.
- go-git promoted to a direct dependency at `v5.19.1`; go.sum churn limited to go-git transitives.

---

## Accepted non-blocking follow-ups (documented, not gating)
- `gitResolver.fetchSHA` uses `Depth:1` fetch of branch+tag refs + `Checkout{Hash}`: a non-tip ancestor SHA may not be reachable → fails closed (error, never mis-render). Acceptable for pinned kustomize sources (tags/tips). Deepen only if deep-ancestor SHA pinning is ever needed.
- Authed OCI online test requires `DDO_TEST_OCI_*` env; skips cleanly otherwise. The anonymous keppel online pull already exercises the real OCI path.
- Optional: a dedicated branch-ref (vs tag/SHA) gitResolver test could be added for completeness.
