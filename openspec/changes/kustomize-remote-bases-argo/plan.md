<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# kustomize-remote-bases-argo Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development to
> implement this plan task-by-task. Trailing `- [ ]` checkboxes mark task
> group completion — check them off AFTER all steps complete.
>
> This plan is a 1:1 restructuring of this change's `tasks.md` into the
> `## Task N` heading format `subagent-driven-development` tooling expects.
> `tasks.md` remains the OpenSpec-tracked source checklist; this file is the
> execution plan. Keep both in sync (check off both when a task completes).

**Goal:** Let krusty fetch Kustomize remote bases live (Argo/Flux-aligned),
gated by a DDO-side classification + optional SHA-pin-enforcement preflight
and a fail-closed sanitized error wrapper, backed by a `git` binary in the
operator image.

**Architecture:** `internal/source/kustomizeSource.Render` gains a pre-krusty
pass: walk the reachable kustomization tree, classify every remote
reference, optionally enforce 40-hex SHA pins under a new operator flag, then
call the existing `krusty.MakeKustomizer(...).Run()` wrapped so fetch
failures return a sanitized, fail-closed error. The root `RootResolver`,
Helm, render cache, and deployment egress defaults are unchanged.

**Tech Stack:** Go, `sigs.k8s.io/kustomize/api` (krusty), Ginkgo/Gomega +
`go test`, envtest, `golangci-lint`, Dockerfile (add `git` binary).

**Spec:** `openspec/changes/kustomize-remote-bases-argo/design.md` and
`openspec/changes/kustomize-remote-bases-argo/specs/*/spec.md` are the
binding authority. `proposal.md` has the why/what-changes summary.

## Global Constraints

- Do NOT touch `RootResolver`, the go-git root resolver, Helm, the render
  cache, or deployment egress policy defaults (explicitly out of scope,
  see proposal.md "Not changing").
- `requireSHAPinnedRemoteBases` is an **operator-level flag**
  (`--kustomize-require-sha-pinned-remote-bases`, default `false`), NOT a
  CRD field — CEL can only gate the root URL, never transitive refs (design.md D2).
- Preflight runs classification → tree walk → SHA-pin gate, strictly
  **before** any krusty fetch / network call. Strict-mode rejection must
  produce **zero** network or git invocations.
- All rejection/error messages must be **sanitized**: never echo raw
  credentials, userinfo, or full remote URLs.
- Preserve existing `Render` behavior: `LoadRestrictionsNone`,
  `manifest.Parse(OriginUpstream)`, `ApplyNamespace` semantics stay intact.
- `docs/implementation.md`: add a **new phase after the current last
  phase**; do not edit completed/ongoing phases (phase-scoping rule, D6).
- TDD throughout: RED (failing test) → GREEN (minimum code) → REFACTOR,
  per task, with a real test run demonstrating FAIL then PASS.
- Every task's trailing checkbox in this plan.md AND the corresponding
  checkbox(es) in `tasks.md` get marked complete together.

---

## Task 1: Remote reference classification (TDD)

- 1.1 RED: write table tests for a `classifyKustomizeReference(raw string)`
  classifier — local path, remote git directory (HTTPS + GitHub shorthand,
  mandatory `?ref=`), remote single-file (`.yaml`/`.yml`/`.json`), and
  rejected forms (SSH, `user:token@` userinfo, URL fragment, ambiguous
  no-`//` multi-segment path). Assert rejections are sanitized (no raw
  URL/credential echo). Run tests — expect FAIL (classifier doesn't exist yet).
- 1.2 GREEN: implement the classifier in `internal/source` (new file, e.g.
  `remotepolicy.go`) returning a typed category + extracted ref; no behavior
  wired into Render yet. Run tests — expect PASS.
- 1.3 RED: write tests for a reachable-tree walk that visits every
  recognized kustomization under the resolved root, classifies each
  `resources`/`components`/`bases` entry, detects reference cycles, and
  stays within the root boundary (reject path/symlink escape). Run tests —
  expect FAIL.
- 1.4 GREEN: implement the tree walk reusing the classifier; return the
  ordered set of remote references discovered. Run tests — expect PASS.
- Commit: `feat(source): classify kustomize remote references + tree walk`

- [ ] Task 1 complete

---

## Task 2: SHA-pin enforcement preflight (TDD)

- 2.1 RED: tests for ref extraction — git base `?ref=`,
  `raw.githubusercontent.com` path segment after `<org>/<repo>`, generic URL
  `?ref=` — and for the 40-char lowercase-hex SHA predicate. Run — expect FAIL.
- 2.2 GREEN: implement ref extraction + SHA predicate. Run — expect PASS.
- 2.3 RED: tests for the preflight gate — strict mode rejects a
  tag/branch/undeterminable ref before any fetch; strict mode accepts a
  40-hex SHA; permissive (default) accepts a tag. Assert strict rejection
  happens with no network/git invocation. Run — expect FAIL.
- 2.4 GREEN: implement the preflight gate taking a
  `requireSHAPinnedRemoteBases bool`; wire it to run after the tree walk,
  before krusty. Run — expect PASS.
- Commit: `feat(source): SHA-pin enforcement preflight for remote bases`

- [x] Task 2 complete

---

## Task 3: Fail-closed sanitized fetch wrapper (TDD)

- 3.1 RED: tests asserting a krusty run whose remote fetch fails
  (unreachable host, git error, non-2xx file) returns a render error — never
  a silent partial render — and that the error is sanitized (no
  credentials, no full remote URL). Run — expect FAIL.
- 3.2 GREEN: implement the sanitizing error wrapper around `kust.Run`; map
  git-exec / loader fetch failures to a stable fail-closed error. Run —
  expect PASS.
- Commit: `feat(source): fail-closed sanitized wrapper around krusty Run`

- [x] Task 3 complete

---

## Task 4: Render wiring (TDD)

> **Partial progress note (added after Task 2, commit a5eb10d):** Task 2's
> fix round already wired `preflightSHAGate` into `kustomizeSource.Render()`
> immediately before `kust.Run`, and added the `requireSHAPinnedRemoteBases`
> field. What remains for this task: (a) make sure classification +
> tree-walk (not just the SHA gate) run before `kust.Run` in ALL modes,
> including permissive (currently the preflight skips classify/tree-walk
> in permissive mode per cod-7's review — confirm whether this is correct
> per spec or a gap); (b) the 4.1 RED/4.2 GREEN test cases below, if not
> already covered by Task 2's tests; (c) the 4.3 REFACTOR dedup pass.
> Do not re-wire what's already wired — verify and extend only.

- 4.1 RED: `kustomize_test.go` cases — Render runs classification +
  preflight before `kust.Run`; a disallowed reference (strict mode,
  unpinned) fails before krusty; an allowed tree renders via krusty
  live-fetch (injected/local fixture resolver). Run — expect FAIL.
- 4.2 GREEN: add `requireSHAPinnedRemoteBases` to `kustomizeSource`; call
  tree-walk → preflight → wrapped `kust.Run` in `Render`; preserve existing
  `LoadRestrictionsNone` + `manifest.Parse(OriginUpstream)` +
  `ApplyNamespace` behavior. Run — expect PASS.
- 4.3 REFACTOR: deduplicate classification grammar shared with existing
  resolver URL parsing where safe; keep public seams unchanged. Run full
  package tests — expect PASS.
- Commit: `feat(source): wire remote-base policy preflight into Render`

- [x] Task 4 complete

---

## Task 5: Operator flag + manager wiring

> **Partial progress note (added after Task 2, commit a5eb10d):** Task 2's
> fix round already added `Deps.RequireSHAPinnedRemoteBases` and threaded it
> through `source.From()`. What remains: `cmd/main.go` still needs the
> actual `--kustomize-require-sha-pinned-remote-bases` CLI flag definition
> and the assignment of its value into `Deps.RequireSHAPinnedRemoteBases`
> (per cod-7's review, `cmd/main.go:204-208` is the relevant area). Strict
> mode is currently unreachable from the operator until this flag exists.

- 5.1 Add operator-level flag
  `--kustomize-require-sha-pinned-remote-bases` (default `false`) in
  `cmd/main.go`; thread it into the kustomize source constructor.
  Unit-test the default and the plumb-through (RED: test asserting default
  `false` and constructor wiring → FAIL; GREEN: implement → PASS).
- Commit: `feat(cmd): add --kustomize-require-sha-pinned-remote-bases flag`

- [x] Task 5 complete

---

## Task 6: Git binary in the operator image

- 6.1 Add a `git` binary to the runtime image (`Dockerfile`), minimal
  install; document why (krusty `ClonerUsingGitExec` shells out to `git`).
  Keep the distroless/base posture as close as possible.
- 6.2 Verify the built image resolves a remote base end-to-end in the
  e2e/kind path OR document the manual verification if CI cannot reach the
  remote; add a fail-closed assertion that a missing `git` yields the
  sanitized error.
- Commit: `build: add git binary to operator image for krusty remote bases`

- [x] Task 6 complete

---

## Task 7: Docs

- 7.1 Add a new phase (after the last existing phase) in
  `docs/implementation.md` describing the Argo/Flux-aligned model; do NOT
  edit completed/ongoing phases. Cross-reference the egress NetworkPolicy
  prerequisite.
- 7.2 Write/extend the kustomize remote-bases authoring guide for the
  aligned model: live fetch each reconcile, egress-gated, pinning
  recommended (enforced only under the flag), credentials
  ambient/process-global, no hermetic offline or SHA cache. State the
  reproducibility caveat (cite Argo Best Practices).
- Commit: `docs: document Argo/Flux-aligned kustomize remote-base model`

- [x] Task 7 complete

---

## Task 8: ipam-capi equivalence fixture (partial — shoot overlay only)

- 8.1 Add a fifth `RUN_EQUIVALENCE=1`-gated fixture under
  `testdata/fixtures/ipam-capi/` (golden: `sapcc/helm-charts` at a pinned
  SHA via `git clone` + `helm dependency build` + `helm template` of
  `system/ipam-capi-remote/`; operator side:
  `source.From → Render → transform.Build → Apply` against
  `system/kustomize/ipam-capi-remote/` at the same SHA), scoped to the
  `managedresources`/shoot overlay only — it is self-contained and renders
  today, unlike the `manager`/seed overlay.
- 8.2 Do NOT attempt the seed overlay fixture: it is blocked by
  `kustomize/ipam-capi-remote/manager`'s repo-root-relative `patches[].path`
  values, which only resolve when kustomize builds from the repo root —
  `KustomizeSource` builds from the overlay subdir instead. Closing it
  requires extending `KustomizeSource` so the clone root and the
  kustomize-build dir can differ (CRD + source change, design.md §9.4);
  that is explicitly out of scope for this change (Global Constraints — no
  `RootResolver`/CRD/API changes) and stays a standalone future follow-up
  per `docs/implementation.md`'s existing deferral note.
- 8.3 Record in the fixture's README/comment why the seed overlay is
  skipped and link back to `docs/implementation.md`'s ipam-capi deferral
  note, so the follow-up change has a ready pointer.
- Commit: `test(equivalence): add ipam-capi shoot-overlay fixture (seed overlay deferred)`

- [x] Task 8 complete

---

## Task 9: Final gate

- 9.1 `go build ./...`
- 9.2 `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./...`
- 9.3 `make run-golangci-lint`
- 9.4 `openspec validate kustomize-remote-bases-argo --strict`
- 9.5 `git diff --check`
- 9.6 `code-reviewer` subagent review (model `openai/gpt-6-astra`), address findings.

- [x] Task 9 complete
