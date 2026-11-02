<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Tasks: kustomize-remote-bases

## Global constraints (approved apply corrections, 2026-10-31)

Use the corrected design and delta specs. "Pinned" means a required ref resolved
to a full SHA before fetch; tags/branches remain allowed. CEL checks root only.
"Classifier" means the documented HTTPS/GitHub-shorthand subset, with unsupported
remote forms rejected; no internal Kustomize imports. All three recognized filenames
and all supported loading fields/configurations must be checked, even for local-only
trees. This is a dependency-version-tested preflight, not a network sandbox.
Bind root Secret to root HTTPS origin; other origins anonymous. Base and kustomize
render cache identities include repository and current authorization context; preserve
remote-identity/SHA associations, invalidate on credential rotation, bound snapshots,
and do not cache scratch paths. Guard cycles, path traversal and symlink escapes.
Helm behavior and RootResolver interface stay unchanged. Work on the current branch.
Implementers own focused RED/GREEN checks; orchestrator owns the full final gate.

## 1. Remote-reference discovery (transitive, typed)

- [x] 1.1 Write failing classifier tests for local relative/absolute paths, HTTPS repository URLs with optional //subdir, GitHub shorthand, tag/SHA refs, deceptive local :// filenames, and unsupported remote forms; do not use a naive substring test
- [x] 1.2 Implement the documented conservative URL subset using public APIs only; make 1.1 green
- [x] 1.3 Write failing tests for a transitive depth-first walker that parses a `kustomization.yaml`, yields its remote entries across `resources`/`components`/`bases`, recurses into local dirs that contain a `kustomization.yaml`, and recurses into each fetched remote base's own kustomization (use a fake fetcher returning staged fixtures); assert depth-first order and that a nested remote base is discovered
- [x] 1.4 Implement the walker against the stable boundary (typed `Kustomization`; no unexported loader/git-URL internals); make 1.3 green
- [x] 1.5 Add a regression test that fails loudly if a kustomize version bump breaks remote-target classification (guards the API-churn risk in design Risks)

## 2. Per-base pin-to-SHA fetch (reuse gitResolver)

- [x] 2.1 Write failing tests that each discovered remote base is resolved to an immutable commit SHA via the source-id-resolution machinery (`ResolveID`: peeled annotated-tag → lightweight tag → branch head), and that a bare 40-char SHA ref is used as-is (table-driven against a fake remote lister)
- [x] 2.2 Write failing tests that fetch uses the full-depth fail-closed clone (`fetchSHA`) so an arbitrary historical SHA resolves, and that an unresolvable/unreachable base fails closed (error names the base; NO default-branch fallback, NO krusty unpinned fetch)
- [x] 2.3 Implement per-base resolve+fetch by reusing `gitResolver` (`ResolveID`, `fetchSHA`, `splitRef`, `repoScope`); make 2.1–2.2 green
- [x] 2.4 Write failing tests for root-HTTPS-origin-scoped authSecretRef credentials (token/basic), anonymous other origins, and redacted errors; prove root credentials never reach a different origin
- [x] 2.5 Implement origin-scoped credentials for base fetches; make 2.4 green

## 3. SHA-keyed base cache

- [x] 3.1 Write failing bounded base-cache tests keyed on canonical repository + full SHA + authorization context: sharing within a context avoids content fetch, distinct SHAs/context/rotated credentials miss; cached snapshots survive per-render cleanup
- [x] 3.2 Implement the base cache composing with (not replacing) the existing `Deps.RenderCache`; make 3.1 green
- [x] 3.3 Write failing tests that preparation precedes kustomize render-cache lookup and keys include associated transitive identities/SHAs plus repository/current authorization context; moved tags and rotated credentials cannot serve stale content
- [x] 3.4 Implement the `inputHash` extension for kustomize; make 3.3 green

## 4. Staging tree + rewrite-to-local

- [x] 4.1 Write failing tests that a staging tree is materialized: kustomizations + their local files copied, each fetched base dropped into a deterministic SHA-keyed subdir, and each remote reference rewritten to a local path RELATIVE to the rewritten kustomization's own directory, pointing at the exact selected subdir (not repo root)
- [x] 4.2 Write a failing test that an operand with NO remote bases produces a byte-identical render to the pre-change behavior (nothing to rewrite)
- [x] 4.3 Implement staging-tree materialization + rewrite (no in-place mutation of the resolver temp dir); make 4.1–4.2 green

## 5. Offline preflight + renderer wiring

- [x] 5.1 Write failing preflight tests covering all recognized filenames and supported path-bearing fields, supplemental built-in configurations, HTTP resources/patches/OpenAPI/configurations/generator inputs, unsupported remote forms, cycles and escapes; assert rejection before any endpoint receives a request
- [x] 5.2 Implement the preflight rejector; make 5.1 green
- [x] 5.3 Write failing renderer tests: prefetch/stage when Git bases exist, preflight always, plugins/Helm execution disabled; accepted localized trees cause no krusty requests and supported local-only renders remain byte-identical
- [x] 5.4 Implement renderer wiring in `internal/source/kustomize.go` per source-rendering MODIFIED spec; make 5.3 green; confirm plugins stay disabled (no alpha/exec plugins)

## 6. Scratch directory + chart (read-only rootfs)

- [x] 6.1 Write a failing test that `NewGitResolver(scratchDir)` roots the root checkout under `scratchDir` (and falls back to `os.TempDir()` when empty), mirroring `helmLoader.newScratchDir()`
- [x] 6.2 Add a `scratchDir` field to `gitResolver`, change `NewGitResolver()` → `NewGitResolver(scratchDir string)`, base every `os.MkdirTemp` on it, and wire `sourceScratchDir` at the `cmd/main.go` construction site (line ~206); update all `NewGitResolver()` callers/tests; make 6.1 green
- [x] 6.3 Write failing tests that per-base checkouts and the staging tree are created under the configured scratch dir (not `os.TempDir()`) and are removed by `cleanup`; make green via the prefetch engine from groups 2 & 4
- [x] 6.4 Reuse the existing `source-scratch` emptyDir for the kustomize path (no new volume); re-evaluate its `sizeLimit` (currently `256Mi`) for the transitive-base + staging-tree footprint and update the chart `values.yaml` mount comment to cover the kustomize loader; verify `make manifests`/chart regen leaves no unexpected diff

## 7. CEL + CRD regeneration

- [x] 7.1 Confirm (with a test/CR fixture) the existing root-URL `?ref=` CEL rule still gates the root; document that transitive-base pinning is a render-time (resolver) gate per the cel-admission-validation MODIFIED spec
- [x] 7.2 If any marker changed, run `make manifests generate` and verify only generated CRD/RBAC files changed as expected (no marker changed; conditional regeneration not needed)

## 8. Equivalence / integration coverage

- [x] 8.1 Add an integration test using the `ipam-capi-remote` worked example (remote base pinned to a SHA) that renders fully offline through prefetch→stage→preflight→krusty and matches the expected manifest set
- [x] 8.2 Verify the shared-base and moved-tag cache scenarios end-to-end against the render path

## 9. Docs + phase wiring

- [x] 9.1 Document supported URL syntax, required ref with runtime SHA resolution (recommend full SHAs), rejected HTTP loading paths, credential/cache boundaries and preflight limitations; document upstream==deployed authoring gate and ipam-capi example
- [x] 9.2 Add a NEW phase AFTER Phase 10 in `docs/implementation.md` capturing this change (do NOT edit completed/ongoing phases); add additive cross-reference notes to Phase 9 (egress labels AND the shared `source-scratch` emptyDir sizing) without editing Phase 9

## 10. Full gate

- [x] 10.1 Run `go build ./...`, `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./...`, and `make run-golangci-lint`; fix new-code findings (re-verify any "pre-existing" claim against this change's own diff)
- [x] 10.2 Run `openspec validate kustomize-remote-bases --strict` and resolve any issues

## 11. PR follow-up — actual ipam equivalence (user requested)

- [x] 11.1 Add a provenance-pinned `ipam-capi-remote` equivalence case comparing the existing vendored operand delivery with DDO's remote-base render/transform output for both seed and shoot, including CRD schemas, conversion webhook configuration and RBAC; keep expected divergences explicit and narrow
- [x] 11.2 Run the new case (not merely its skipped gate), demonstrate detection of a meaningful mismatch, complete scoped review and repository validation, and push the follow-up to PR #59
