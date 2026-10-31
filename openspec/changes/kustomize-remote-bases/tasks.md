<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Tasks: kustomize-remote-bases

## 1. Remote-reference discovery (transitive, typed)

- [ ] 1.1 Write failing tests for a `remoteref` classifier that, given a `resources`/`components`/`bases` entry string, returns whether it parses as a Kustomize git-URL remote target (cover: local relative path, local abs path, `repo//subdir?ref=sha`, tag ref, bare host without `//`, and a deceptive local path containing `://` in a filename) — assert classification uses Kustomize's git-URL grammar, not a naive `"://"` test
- [ ] 1.2 Implement the classifier over `sigs.k8s.io/kustomize/api/types.Kustomization` + the stable remote-target parse; make 1.1 green
- [ ] 1.3 Write failing tests for a transitive depth-first walker that parses a `kustomization.yaml`, yields its remote entries across `resources`/`components`/`bases`, recurses into local dirs that contain a `kustomization.yaml`, and recurses into each fetched remote base's own kustomization (use a fake fetcher returning staged fixtures); assert depth-first order and that a nested remote base is discovered
- [ ] 1.4 Implement the walker against the stable boundary (typed `Kustomization`; no unexported loader/git-URL internals); make 1.3 green
- [ ] 1.5 Add a regression test that fails loudly if a kustomize version bump breaks remote-target classification (guards the API-churn risk in design Risks)

## 2. Per-base pin-to-SHA fetch (reuse gitResolver)

- [ ] 2.1 Write failing tests that each discovered remote base is resolved to an immutable commit SHA via the source-id-resolution machinery (`ResolveID`: peeled annotated-tag → lightweight tag → branch head), and that a bare 40-char SHA ref is used as-is (table-driven against a fake remote lister)
- [ ] 2.2 Write failing tests that fetch uses the full-depth fail-closed clone (`fetchSHA`) so an arbitrary historical SHA resolves, and that an unresolvable/unreachable base fails closed (error names the base; NO default-branch fallback, NO krusty unpinned fetch)
- [ ] 2.3 Implement per-base resolve+fetch by reusing `gitResolver` (`ResolveID`, `fetchSHA`, `splitRef`, `repoScope`); make 2.1–2.2 green
- [ ] 2.4 Write failing tests that per-base fetch applies per-host credentials via `authFor`/`authSecretRef` (basic auth; `git`+token or username/password) with anonymous fallback for hosts with no creds; assert error paths never echo credential material
- [ ] 2.5 Implement per-host credential wiring for base fetches; make 2.4 green

## 3. SHA-keyed base cache

- [ ] 3.1 Write failing tests for a base cache keyed on `git:<host>` + resolved SHA: a base shared by two operands at the same SHA is fetched once (hit avoids egress); a different SHA is a distinct key and refetches
- [ ] 3.2 Implement the base cache composing with (not replacing) the existing `Deps.RenderCache`; make 3.1 green
- [ ] 3.3 Write failing tests that the render-cache `inputHash` incorporates the resolved SHA set of all transitive remote bases, so a moved base SHA under an unchanged root `resolvedID` yields a distinct render key (per render-cache MODIFIED spec)
- [ ] 3.4 Implement the `inputHash` extension for kustomize; make 3.3 green

## 4. Staging tree + rewrite-to-local

- [ ] 4.1 Write failing tests that a staging tree is materialized: kustomizations + their local files copied, each fetched base dropped into a deterministic SHA-keyed subdir, and each remote reference rewritten to a local path RELATIVE to the rewritten kustomization's own directory, pointing at the exact selected subdir (not repo root)
- [ ] 4.2 Write a failing test that an operand with NO remote bases produces a byte-identical render to the pre-change behavior (nothing to rewrite)
- [ ] 4.3 Implement staging-tree materialization + rewrite (no in-place mutation of the resolver temp dir); make 4.1–4.2 green

## 5. Offline preflight + renderer wiring

- [ ] 5.1 Write failing tests for the preflight: it recursively parses every staged kustomization and errors if any `resources`/`components`/`bases` value still parses as a remote git target; a fully-localized tree passes
- [ ] 5.2 Implement the preflight rejector; make 5.1 green
- [ ] 5.3 Write failing tests wiring the kustomize renderer to run prefetch→stage→preflight before `krusty` only when remote bases are present, build the staging tree with plugins disabled, and otherwise build the resolved root directly; assert no network egress on a localized tree and byte-identical output when no remote bases
- [ ] 5.4 Implement renderer wiring in `internal/source/kustomize.go` per source-rendering MODIFIED spec; make 5.3 green; confirm plugins stay disabled (no alpha/exec plugins)

## 6. CEL + CRD regeneration

- [ ] 6.1 Confirm (with a test/CR fixture) the existing root-URL `?ref=` CEL rule still gates the root; document that transitive-base pinning is a render-time (resolver) gate per the cel-admission-validation MODIFIED spec
- [ ] 6.2 If any marker changed, run `make manifests generate` and verify only generated CRD/RBAC files changed as expected

## 7. Equivalence / integration coverage

- [ ] 7.1 Add an integration test using the `ipam-capi-remote` worked example (remote base pinned to a SHA) that renders fully offline through prefetch→stage→preflight→krusty and matches the expected manifest set
- [ ] 7.2 Verify the shared-base and moved-tag cache scenarios end-to-end against the render path

## 8. Docs + phase wiring

- [ ] 8.1 Document `source.kustomize` remote-base authoring guidance, including the BREAKING requirement that remote bases carry an immutable pinned `?ref=`, and the live-vs-upstream divergence authoring gate (only point at upstream when upstream == deployed shape; reference the `ipam-capi-remote` example)
- [ ] 8.2 Add a NEW phase AFTER Phase 10 in `docs/implementation.md` capturing this change (do NOT edit completed/ongoing phases); add additive cross-reference notes to Phase 9 egress labels without editing Phase 9

## 9. Full gate

- [ ] 9.1 Run `go build ./...`, `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./...`, and `make run-golangci-lint`; fix new-code findings (re-verify any "pre-existing" claim against this change's own diff)
- [ ] 9.2 Run `openspec validate kustomize-remote-bases --strict` and resolve any issues
