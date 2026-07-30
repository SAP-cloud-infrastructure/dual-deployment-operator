# Verification Report: equivalence-tests

Schema: `sdd-plus-superpowers`. Verified 2026-07 against the implementation on
branch `feat/equivalence-tests`.

## Summary

| Dimension    | Status |
|--------------|--------|
| Completeness | 12/12 plan tasks complete; 4 of 5 operators wired (ipam-capi deferred, documented) |
| Correctness  | All spec requirements mapped to code + tests; 4-operator network suite + offline suite green |
| Coherence    | Follows design (Decision B scoped equivalence, git@SHA golden); 1 WARNING (stale spec title), 1 accepted deferral |

Evidence gates (re-run during verification):
- `golangci-lint run` (whole module) → **0 issues**
- `go build ./...`, `go vet ./...` → clean; no codegen drift
- `go test ./internal/equivalence/` offline subset → PASS
- `TestEquivalence` (metal-operator, khalkeon, boot-operator, argora-operator) network suite → PASS (~197s)
- `internal/source` suite incl. `TestGitResolverResolvesHistoricalSHA` → PASS
- Final code review (holistic) + re-review of fixes → **APPROVED**

## Completeness

- **Plan tasks**: 12/12 checkboxes `[x]` in `plan.md`.
- **Capabilities implemented** (spec → code):
  - `equivalence-golden-render` → `golden_render.go` (`RenderGolden`), `golden_classify.go` (`ClassifyGolden`, `UnwrapManagedResources`).
  - `equivalence-operator-capture` → `operator_capture.go` (`CaptureOperator`), `fixture.go` (`LoadFixture`).
  - `equivalence-comparator` → `compare.go` (`Compare`), `scope.go` (`Scope`, `CompareScoped`), `normalize.go`, `allowlist.go`, `set.go`.
  - `kustomize-root-resolver` (MODIFIED delta) → `internal/source/gitresolver.go` (`fetchSHA` full-depth fix).
- **Fixtures**: `testdata/fixtures/{metal-operator,boot-operator,argora-operator,khalkeon}/`. ipam-capi intentionally absent (deferred).

## Correctness

Requirement → implementation + test mapping (all covered):

| Requirement | Code | Test |
|---|---|---|
| Golden chart render (git@SHA — see WARNING) | `RenderGolden` | `TestGoldenRenderMetalOperatorFromGit` (network) |
| Seed/shoot classification | `ClassifyGolden` | `TestClassify*` |
| Shoot-stream unwrapping to bare objects | `UnwrapManagedResources` | `TestUnwrapManagedResources*` (incl. fail-closed) |
| Enumerated exclusion of replaced plumbing | `GoldenOpts.Exclusions` + `ClassifyGolden` | `TestClassifyExcludesByKindAndName` |
| Identity-gated, no-op-safe manipulation | `ClassifyGolden`/`UnwrapManagedResources` | `TestClassifyNoInjectorConfigMapIsNoOp`, `TestUnwrapManagedResourcesPassesThroughUnrelated` |
| Operator render+transform capture | `CaptureOperator` | `TestCaptureOperatorReturnsSets` |
| Fixture-driven capture inputs | `fixture.go` + fixtures | `TestLoadFixtureMetalOperator`, `TestEquivalence/*` |
| Scoped equivalence + known divergences | `Scope`, `CompareScoped` | `TestScope*`, `TestCompareScoped*` |
| Canonical normalization + per-resource compare | `Normalize`, `Compare` | `TestNormalize*`, `TestCompareReports*` |
| Allowlist limited to provenance/incidental | `StripAllowlist` | `TestStripAllowlistRemovesProvenanceAndInternalLabels` |
| caBundle parity | `Compare` + allowlist | `TestCompareCABundleAbsentBothSidesEqual` |
| Per-operator subtests gate CI (no build tag/env gate) | `equivalence_test.go` `operators` | `TestEquivalence/<op>` + verified no `//go:build`/`t.Skip`/`os.Getenv` |
| Resolver historical-SHA fail-closed | `fetchSHA` | `TestGitResolverResolvesHistoricalSHA` |

Scenario coverage confirmed for the load-bearing false-green guards (final review + fixes):
- Known-divergence cannot hide a both-sided field mismatch — `TestCompareScopedDoesNotHideBothSidedFieldMismatch`.
- Non-vacuity floor (scoped shoot sets non-empty) in the harness.

## Coherence

- **Design adherence**: matches the settled design — Decision B (scoped equivalence over delivered kinds + per-fixture known-divergences), golden rendered from `sapcc/helm-charts` git at a pinned SHA (GHCR proven non-viable), operator side via `source.From → Render → transform.Build → Apply`, delivery out of scope. `design.md` and the `equivalence-comparator` spec were reconciled to Decision B.
- **Pattern consistency**: SPDX headers on all new files; reuses `internal/source`/`internal/transform`/`internal/manifest`; TDD throughout; lint clean.

### WARNING (RESOLVED during verification)

1. **Stale spec title vs implementation** — `specs/equivalence-golden-render/spec.md`
   requirement was titled "Golden chart render from GHCR" and described an OCI/GHCR
   pull, but the implementation renders from git at a pinned SHA (Decision A, after
   GHCR was proven non-pullable). **RESOLVED**: the requirement was rewritten to
   "Golden chart render from git at a pinned SHA" (clone + `helm dependency build` +
   `helm template`), with the GHCR-non-viability recorded as rationale and the
   scenarios updated to match. `openspec validate` passes.

### Accepted deferral (not an issue)

2. **ipam-capi equivalence deferred** — the 5th operator (kustomize source) is intentionally out of scope for this change: two blockers surfaced, one fixed here (resolver historical-SHA), one deferred (kustomize repo-root-relative patch paths need a `KustomizeSource` clone-root-vs-build-dir extension — a CRD/source change). Documented in `docs/implementation.md` Phase 8 and `APPLY-HANDOFF.md`. This is a scoped, recorded follow-up, not a silent omission.

## Final Assessment

**No critical issues. The one WARNING (stale golden-render spec title) was reconciled
during verification; 1 accepted, documented deferral (ipam-capi).** The four
Helm-operator equivalence subtests gate every PR and genuinely assert equivalence
(false-green risks closed and reviewer-approved). Ready for archive.
