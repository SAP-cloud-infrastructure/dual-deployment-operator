## Context

The production Helm `ChartLoader` (`internal/source/helmloader.go`, ~435 LOC) currently supports two chart-repo transports, dispatching on the `repo` scheme:

- **`oci://…`** — pull via the Helm registry client (`action.Pull`, OCI ref, pinned version). This is what the entire fleet uses (`oci://keppel.global.cloud.sap/ccloud-helm/…`, verified anonymous in Phase 7).
- **`http(s)://…`** — pull from a classic `index.yaml` Helm repository via `action.Pull` with `RepoURL`, plus a parallel `resolveHTTPID` path that fetches `index.yaml` to derive a render-cache id.

Phase 7.6 (shipped, `#16` / `508334f`) added subchart dependency resolution via `downloader.Manager.Build()` but **restricted it to OCI** — HTTP(S)-repo subchart deps are rejected fail-closed, because `Build()` needs HTTP repos pre-registered in a `repositories.yaml` with a cached index, which the operator does not do at reconcile time. That turned the HTTP(S) **parent**-chart path into a near-dead end: an HTTP(S) parent chart works only when it has no dependencies or all-vendored dependencies (any declared dependency would, in practice, live on the same HTTP(S) repo and be rejected). No fleet operator relies on this narrow case.

**Constraints:**
- The validating webhook (`internal/webhook/v1alpha1/dualdeploymentoperator_webhook.go`) is intentionally a pass-through stub; the project's v1 convention is that **all admission validation is expressed as CEL rules on the CRD** (see `cel-admission-validation` spec). The kustomize `url` `ref=` requirement is the established CEL precedent for a scheme/format constraint.
- Auto-generated files (`config/crd/bases/*`, `zz_generated.*`) MUST be regenerated via `make manifests generate`, never hand-edited.
- The kustomize source path (krusty git/HTTP remote bases) is a separate transport and is unaffected.

**Stakeholders:** operator maintainers (reduced maintenance/test surface); the five fleet operators (all already `oci://`, no behavior change for them).

## Goals / Non-Goals

**Goals:**
- Make the Helm `ChartLoader` **OCI-only** end to end: delete `pullHTTP` and `resolveHTTPID` and the `http(s)://` dispatch arms in `Load`, `ResolveID`, and `repoScope`.
- Reject a non-`oci://` Helm `repo` at **admission** with a clear message (CEL rule on `HelmSource.repo`), with the loader "unsupported scheme" error retained as a runtime backstop.
- Adapt **all** affected tests (full inventory in Migration Plan): remove the now-dead Helm HTTP-repo tests, remove the OCI-vs-HTTP cache-key collision test, keep the git/kustomize HTTPS-auth test (different transport), and add a test proving a non-`oci://` Helm `repo` is rejected.
- Keep `go build ./...`, `make run-golangci-lint`, and the offline `internal/source` suite green; `make manifests generate` produces no diff beyond the tightened CRD rule.
- Update docs (spec deltas, README, AGENTS, design.md §3.3, context.md) to reflect OCI-only.

**Non-Goals:**
- Changing the kustomize source path (git/HTTP remote bases via krusty) — explicitly out of scope.
- Removing `hostOf` / `rejectURLCredentials` — still used by the OCI path.
- Actively querying live clusters for stray `http(s)://` CRs — the operator is not deployed live yet (image publish is a future phase); "fleet is OCI-only" is recorded as a documented assumption.
- Adding custom Go logic to the validating webhook — it stays a pass-through stub.

## Capabilities

**Modified Capabilities:**

- **`helm-chart-loader`** — Narrow "Production Helm ChartLoader with scheme dispatch" to OCI-only: the `Load` scheme dispatch supports only `oci://`; the "classic HTTP(S) repo reference is pulled via RepoURL" scenario is REMOVED; a non-`oci://` Helm `repo` MUST be rejected with a clear error naming OCI.
- **`source-id-resolution`** — REMOVE the "Resolve classic HTTP Helm repos via the index, degrade gracefully" requirement (and its scenarios); `ResolveID` for the Helm loader resolves only `oci://` (manifest digest). The git and OCI requirements are unchanged.
- **`cel-admission-validation`** — ADD a requirement that `spec.source.helm.repo` MUST begin with `oci://`, enforced by a CEL `XValidation` rule on `HelmSource`, rejected at admission with a clear message (analogous to the existing kustomize `url` `ref=` rule).

No new capabilities.

## Decisions

**Decision: Enforce OCI-only at admission via CEL, not the validating webhook**
- Chosen: Add `+kubebuilder:validation:XValidation` on `HelmSource` requiring `self.repo.startsWith('oci://')`, with a clear message.
- Reason: The webhook is a pass-through stub; the project convention is CEL-only admission validation. CEL rejects at `kubectl apply` time with no new webhook/cert serving surface, and mirrors the existing kustomize `url` `ref=` rule.
- Alternatives considered: (B) loader-only enforcement — rejected because a bad CR would be accepted at admission and fail opaquely at reconcile, contradicting the phase spec's "validate, do not silently narrow". (C) custom webhook logic — rejected because it violates the CEL-only convention and reintroduces webhook code for a check CEL handles natively.

**Decision: `oci://`-prefix allowlist, not an `http(s)://` blocklist**
- Chosen: CEL requires the `oci://` prefix.
- Reason: Matches the phase spec ("only `oci://` accepted") and rejects every non-OCI scheme uniformly at admission, leaving no gap for the loader to cover.
- Alternatives considered: blocklist only `http://`/`https://` — rejected: leaves other schemes (e.g. `file://`) to fall through to the loader, a weaker admission guarantee.

**Decision: Keep the loader "unsupported scheme" error as a backstop**
- Chosen: Reword the `Load`/`ResolveID`/`repoScope` `default` arm to name OCI explicitly; do not rely solely on CEL.
- Reason: Defense in depth for callers that bypass admission (direct loader use in tests/tools).
- Alternatives considered: remove the loader check entirely — rejected: loses the backstop.

**Decision: Compiler-driven dead-code cleanup**
- Chosen: Remove `pullHTTP`, `resolveHTTPID`, and follow the compiler + `make run-golangci-lint` to drop now-unused imports/helpers (`net/http`, `io`, `repo.IndexFile` handling).
- Reason: Avoids guessing which symbols become unused; the toolchain is authoritative.

**Decision: Live-CR pre-check = documented assumption**
- Chosen: Record "all fleet CRs use `oci://`" as an assumption; do not query clusters.
- Reason: The operator is not deployed live yet (no live CRs); the fleet publishes to keppel OCI (verified Phase 7). The new CEL rule + loader error catch any future stray.

**Decision: Render-cache key structure kept as-is (NOT simplified in this change)**
- Chosen: Leave the `keyParts` struct (`internal/source/cachekey.go`) unchanged. The only key-related edit is mechanical: `helmLoader.repoScope` loses its `http:` arm and thereafter returns only `oci:<host>` for Helm (git resolver returns `git:<host>` unchanged). `TestCacheKey_OCIvsHTTPNoCollision` is removed (no `http:` Helm scope remains to collide with OCI).
- Reason: After OCI-only, `sourceKind` ("helm"/"kustomize") becomes 1:1 correlated with `repoScope`'s scheme (`oci:`/`git:`) — the struct comment already notes `sourceKind` is "redundant with repoScope's scheme today". Collapsing them would save one field but is a `render-cache` refactor, out of Phase 7.7 scope (which removes a transport, not the cache-key model), and it removes a deliberate extensibility seam: the correlation holds only because there are currently two source kinds each with one transport. A future third combination (OCI-based kustomize, a re-added HTTP Helm repo, a non-git kustomize fetch) would break the correlation, and a merged field would become a silent cache-collision risk. Keeping the dimensions independent follows standard composite-cache-key practice (independent conceptual dimensions stay independent even when they correlate today).
- Alternatives considered: collapse `sourceKind` into `repoScope`'s scheme now — rejected as out-of-scope and as removing the extensibility seam for a negligible (~one field / ~6 bytes per key) gain.

## Risks / Trade-offs

- [A hypothetical future consumer wants a classic HTTP(S) Helm repo] → Documented capability narrowing, not a regression the fleet relies on; OCI is the sanctioned Helm transport. Re-add would be a new change. Mitigation: the removal is localized to the Helm loader + one CEL rule, straightforward to revert from git history.
- [CEL rule change alters the CRD admission surface (larger blast radius than a pure code change)] → Kept as its own OpenSpec change (not folded into 7.6); `make manifests generate` diff reviewed to confirm only the tightened `HelmSource.repo` rule changes.
- [A live CR somehow already uses `http(s)://`] → No live CRs exist yet (operator unpublished). If one appeared post-deploy, admission would now reject the update and the loader would error at reconcile with a clear message — fail-loud, not silent under-render.
- [Removing shared HTTP helpers accidentally breaks the OCI path] → `hostOf`/`rejectURLCredentials` explicitly retained; OCI online + hermetic authed tests stay green as the guard.

## Migration Plan

Deployment steps:
1. Add the `oci://`-prefix CEL rule to `HelmSource.repo` in `api/v1alpha1/dualdeploymentoperator_types.go`; run `make manifests generate`.
2. Delete `pullHTTP`/`resolveHTTPID`; drop the `http(s)://` arms in `Load`/`ResolveID`/`repoScope`; reword the `default` arm; let the compiler + `make run-golangci-lint` drive import cleanup.
3. Adapt **all** affected tests (ground-truthed inventory — `internal/source/*_test.go`):
   - **Remove (Helm HTTP-repo path, being deleted):**
     - `helmloader_online_test.go` → `TestHelmLoaderOnlineHTTPRepo` (entire file is the HTTP-online parent pull)
     - `helmloader_authed_test.go` → `TestHelmLoaderAuthedHTTPRepo`
     - `helmloader_test.go` → `TestHelmLoader_ResolveID_HTTPIndexDigest`, `TestHelmLoader_ResolveID_HTTPVersionFallback`, `TestHelmLoader_ResolveID_HTTPUnkeyable`, `TestHelmLoader_repoScope_HTTPPathDistinguishes`, `TestHelmLoader_ResolveID_RejectsURLCredentials_HTTP`, `TestHelmLoader_resolveHTTPID_HTTP4xx`, `TestHelmLoader_resolveHTTPID_BasicAuth`
   - **Remove (asserts a now-impossible scenario):** `cachekey_test.go` → `TestCacheKey_OCIvsHTTPNoCollision` — after removal there is no `http:` Helm repoScope to collide with OCI.
   - **Keep (out of scope — git/kustomize transport, NOT the Helm repo path):** `gitresolver_authed_test.go` → `TestGitResolverHTTPSBasicAuth` (git-over-HTTPS BasicAuth), and all other git/OCI/kustomize tests.
   - **Add:** a test asserting a non-`oci://` Helm `repo` is rejected — at admission via the CEL rule (envtest, in the CRD/webhook suite) and/or at `Load`/`ResolveID` with a clear OCI-naming error.
   - **Guard:** after the edits, `grep -ri http internal/source/*_test.go` should surface only the retained git/kustomize transport tests and the new rejection test — no lingering Helm-repo HTTP test remains.
4. Update the three spec deltas + README/AGENTS/design.md §3.3/context.md.
5. Gate: `go build ./...`, `make run-golangci-lint`, offline `internal/source` suite, `make manifests generate` (no unexpected diff).

Rollback:
- Single-branch change against `main`; revert the commit/PR to restore the HTTP path (code + CEL rule + tests) from git history. No data migration, no state.

## Open Questions

- (none blocking) — CEL shape (`oci://` prefix) and the live-CR check (documented assumption) were resolved during brainstorming.
