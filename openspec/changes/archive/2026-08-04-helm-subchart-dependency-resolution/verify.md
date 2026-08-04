# Verification Report: helm-subchart-dependency-resolution

Schema: `sdd-plus-superpowers`. Verified against `internal/source/helmloader.go` and its tests at HEAD `6a42d6d`, after a 4-round final code review that ended `APPROVE`.

## Summary

| Dimension    | Status |
|--------------|--------|
| Completeness | 7/7 tasks complete (0 unchecked plan steps); 4/4 requirements implemented |
| Correctness  | 4/4 requirements mapped to code; 8/8 scenarios covered by tests |
| Coherence    | Follows design decisions (OCI-only, Build()-only+Chart.lock, credential reuse, no cache change, scratch-dir); code review APPROVE |

Gate at verification time: `go build ./...` OK · `go test ./internal/source/... -skip Online` ok · `make run-golangci-lint` 0 issues · `make manifests generate` no diff.

## Completeness

**Tasks**: all plan steps checked (`grep -c '- [ ] **Step'` = 0). Tasks 1–7 complete: shared OCI registry-client builder, `Chart.lock` fail-closed pre-check, wire expand+Build+load into `Load()`, hermetic coverage tests, online test (removed under the OCI-only scope revision), scratch-dir/`readOnlyRootFilesystem` wiring, codegen+gate.

**Spec coverage** (`specs/helm-chart-loader/spec.md`): 1 MODIFIED + 3 ADDED requirements, all implemented:
- *Pull to a temporary directory and clean up* → [`helmloader.go` `Load`](../../../internal/source/helmloader.go) (`newScratchDir` → temp dir, `defer os.RemoveAll`, `expandAndResolveDeps` → `loader.Load(chartDir)`).
- *Subchart dependency resolution at pull time* → `expandAndResolveDeps` (`chartutil.Expand` → `resolveDepsPlan` → `downloader.Manager.Build()`).
- *Deterministic dependency resolution requires a committed Chart.lock* → `resolveDepsPlan` (Build()-only; missing lock in the fetch regime = fail-closed error).
- *Subchart dependencies MUST use OCI repositories* → `resolveDepsPlan` OCI/`file://`-only guard when a Build is required.

## Correctness

Requirement → implementation → test evidence:

| Requirement | Code | Test |
|---|---|---|
| Pull to temp dir + cleanup (+ cache can skip Load) | `Load`, `expandAndResolveDeps` | `TestHelmLoaderResolvesUnvendoredDependency`; existing render-cache tests unchanged |
| Subchart resolution at pull time — unvendored resolves | `m.Build()` in `expandAndResolveDeps` | `TestHelmLoaderResolvesUnvendoredDependency` (hermetic OCI; `assertNoVendoredSubcharts` invariant proves a real fetch) + `TestHelmLoaderOnlineOCIAnonymous` |
| — vendored subchart still renders | `resolveDepsPlan` needsBuild=false path | `TestHelmLoaderVendoredSubchartUnchanged` |
| — no-dependency chart unaffected | `resolveDepsPlan` len(Dependencies)==0 | `TestHelmLoaderNoDependenciesNoOp` |
| Chart.lock required (fail-closed); dep-less needs no lock | `resolveDepsPlan` | `TestHelmLoaderMissingLockFailsClosed`, `TestResolveDepsPlan` |
| OCI subchart resolves with parent credential | `credFor` → `ociRegistryClient` reuse | `TestHelmLoaderResolvesUnvendoredDependency` (authed hermetic registry) |
| HTTP(S)-repo subchart dep rejected fail-closed | `resolveDepsPlan` OCI guard | `TestResolveDepsPlan` (unvendored-HTTP, mixed partial-vendoring, empty-repo packed) |

All 8 spec scenarios have covering tests. `TestResolveDepsPlan` additionally covers three edges surfaced during review (vendored-HTTP accepted with no Build; mixed unvendored-OCI + vendored-HTTP rejected; empty-repo packed-only rejected in the Build regime).

## Coherence

Design decisions in `design.md` are followed:
- **Resolve at pull time via `downloader.Manager.Build()`** — implemented in `expandAndResolveDeps`.
- **Build()-only + Chart.lock pre-check** — `resolveDepsPlan` never invokes Update(); `SkipUpdate: true` on the Manager.
- **OCI-only subchart repos; HTTP(S) rejected fail-closed** — matches the design Decision + Non-Goal; two-regime rule (fully-vendored accepts any scheme; Build-required requires OCI/`file://` for every dep, incl. unpacked-dir requirement for empty-repo deps) closes the reprocess-whole-lock edge.
- **Single-registry auth reuse** — `credFor(ctx,"")` → `ociRegistryClient`; no per-dependency creds.
- **No render-cache change** — cache key/layer untouched; `Build()` runs inside `Load()` so it is transparently covered per resolved parent id.
- **Scratch dir for read-only rootfs** — `helmLoader.scratchDir` + `NewHelmLoader(scratchDir)` + `--source-scratch-dir` flag; `config/manager/manager.yaml` mounts a writable `emptyDir`; chart-1 equivalent recorded as a Phase 9 requirement in `docs/implementation.md`.
- **Kustomize untouched** — no change to `kustomize.go`/`gitresolver.go`.

Code patterns consistent with `internal/source` (constructor style, error-wrapping `source:` prefix, hermetic in-process OCI test harness reuse). `make manifests generate` produces no diff (no CRD/RBAC/type change), consistent with "loader-only, no CRD change".

## Issues

- **CRITICAL**: none.
- **WARNING**: none.
- **SUGGESTION**: none blocking. The `TestHelmLoaderResolvesUnvendoredDependency` online-equivalent OCI fetch is exercised hermetically; a live public-OCI-subchart online test was intentionally dropped under the OCI-only scope revision (no suitable stable public unvendored-OCI-subchart target; hermetic coverage is authoritative).

## Final Assessment

All checks passed. No critical or warning issues. Ready for archive.
