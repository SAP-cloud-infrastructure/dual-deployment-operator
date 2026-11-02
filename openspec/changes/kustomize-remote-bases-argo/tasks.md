<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## 1. Remote reference classification (TDD)

- [ ] 1.1 RED: write table tests for a `classifyKustomizeReference(raw string)` classifier — local path, remote git directory (HTTPS + GitHub shorthand, mandatory `?ref=`), remote single-file (`.yaml`/`.yml`/`.json`), and rejected forms (SSH, `user:token@` userinfo, URL fragment, ambiguous no-`//` multi-segment path). Assert rejections are sanitized (no raw URL/credential echo).
- [ ] 1.2 GREEN: implement the classifier in `internal/source` (new file, e.g. `remotepolicy.go`) returning a typed category + extracted ref; no behavior wired into Render yet.
- [ ] 1.3 RED: write tests for a reachable-tree walk that visits every recognized kustomization under the resolved root, classifies each `resources`/`components`/`bases` entry, detects reference cycles, and stays within the root boundary (reject path/symlink escape).
- [ ] 1.4 GREEN: implement the tree walk reusing the classifier; return the ordered set of remote references discovered.

## 2. SHA-pin enforcement preflight (TDD)

- [x] 2.1 RED: tests for ref extraction — git base `?ref=`, `raw.githubusercontent.com` path segment after `<org>/<repo>`, generic URL `?ref=` — and for the 40-char lowercase-hex SHA predicate.
- [x] 2.2 GREEN: implement ref extraction + SHA predicate.
- [x] 2.3 RED: tests for the preflight gate — strict mode rejects a tag/branch/undeterminable ref before any fetch; strict mode accepts a 40-hex SHA; permissive (default) accepts a tag. Assert strict rejection happens with no network/git invocation.
- [x] 2.4 GREEN: implement the preflight gate taking a `requireSHAPinnedRemoteBases bool`; wire it to run after the tree walk, before krusty.

## 3. Fail-closed sanitized fetch wrapper (TDD)

- [x] 3.1 RED: tests asserting a krusty run whose remote fetch fails (unreachable host, git error, non-2xx file) returns a render error — never a silent partial render — and that the error is sanitized (no credentials, no full remote URL).
- [x] 3.2 GREEN: implement the sanitizing error wrapper around `kust.Run`; map git-exec / loader fetch failures to a stable fail-closed error.

## 4. Render wiring (TDD)

- [ ] 4.1 RED: `kustomize_test.go` cases — Render runs classification + preflight before `kust.Run`; a disallowed reference (strict mode, unpinned) fails before krusty; an allowed tree renders via krusty live-fetch (injected/local fixture resolver).
- [ ] 4.2 GREEN: add `requireSHAPinnedRemoteBases` to `kustomizeSource`; call tree-walk → preflight → wrapped `kust.Run` in `Render`; preserve existing `LoadRestrictionsNone` + `manifest.Parse(OriginUpstream)` + `ApplyNamespace` behavior.
- [ ] 4.3 REFACTOR: deduplicate classification grammar shared with existing resolver URL parsing where safe; keep public seams unchanged.

## 5. Operator flag + manager wiring

- [ ] 5.1 Add operator-level flag `--kustomize-require-sha-pinned-remote-bases` (default `false`) in `cmd/main.go`; thread it into the kustomize source constructor. Unit-test the default and the plumb-through.

## 6. Git binary in the operator image

- [ ] 6.1 Add a `git` binary to the runtime image (`Dockerfile`), minimal install; document why (krusty `ClonerUsingGitExec` shells out to `git`). Keep the distroless/base posture as close as possible.
- [ ] 6.2 Verify the built image resolves a remote base end-to-end in the e2e/kind path OR document the manual verification if CI cannot reach the remote; add a fail-closed assertion that a missing `git` yields the sanitized error.

## 7. Docs

- [ ] 7.1 Add a new phase (after the last existing phase) in `docs/implementation.md` describing the Argo/Flux-aligned model; do NOT edit completed/ongoing phases. Cross-reference the egress NetworkPolicy prerequisite.
- [ ] 7.2 Write/extend the kustomize remote-bases authoring guide for the aligned model: live fetch each reconcile, egress-gated, pinning recommended (enforced only under the flag), credentials ambient/process-global, no hermetic offline or SHA cache. State the reproducibility caveat (cite Argo Best Practices).

## 8. ipam-capi equivalence fixture (partial — shoot overlay only)

- [ ] 8.1 Add a fifth `RUN_EQUIVALENCE=1`-gated fixture under
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
  that is explicitly out of scope for this change (see Global Constraints —
  no `RootResolver`/CRD/API changes) and stays a standalone future
  follow-up per `docs/implementation.md`'s existing deferral note.
- 8.3 Record in the fixture's README/comment why the seed overlay is
  skipped and link back to `docs/implementation.md`'s ipam-capi deferral
  note, so the follow-up change has a ready pointer.
- Commit: `test(equivalence): add ipam-capi shoot-overlay fixture (seed overlay deferred)`

- [ ] Task 8 complete

## 9. Final gate

- [ ] 9.1 `go build ./...`
- [ ] 9.2 `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./...`
- [ ] 9.3 `make run-golangci-lint`
- [ ] 9.4 `openspec validate kustomize-remote-bases-argo --strict`
- [ ] 9.5 `git diff --check`
- [ ] 9.6 `code-reviewer` subagent review (model `openai/gpt-6-astra`), address findings.

- [ ] Task 9 complete
