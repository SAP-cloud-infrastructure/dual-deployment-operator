<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Retrospective: production-source-loaders (Phase 7)

Executed via the `sdd-plus-superpowers` OpenSpec workflow: 9 plan tasks through subagent-driven-development (fresh implementer per task, per-task code review), two extra review checkpoints (pre- and post-Task-8) requested by the user, a final holistic review, verify, docs, and a single PR (#8) to `main`. 25 feature commits + 2 planning-doc commits.

## Wins

- **Interface-first design paid off.** The `ChartLoader` / `RootResolver` / `Deps` seams existed from Phase 2, so this change was purely additive behind unchanged signatures. Confirmed by the final review: `Source.Render`, `ChartLoader.Load`, `RootResolver.Resolve` all unchanged (commit `d3ebc1a` diff touches no interface).
- **Online tier proved the loaders against reality**, not just mocks. `TestHelmLoaderOnlineOCIAnonymous` pulled a real chart from `oci://keppel.global.cloud.sap/ccloud-helm/metal-operator-remote@0.6.2` in 0.37s (Task 3 online run), and the git resolver resolved tag+SHA against a local repo. This caught nothing broken — but it's real evidence the production path works, not an assertion.
- **Per-task review caught two genuine bugs the tests didn't.** The reconciler-wiring review found a **P1 data race** (shared singleton loaders mutated per-CR via `bindHelmCreds`) — fixed in `6bdc3d7` and confirmed with `go test -race`. The pre-Task-8 checkpoint found a **P1 correctness bug** (explicit `authSecretRef` resolve failures silently degrading to anonymous) — fixed in `0af8a40`. Neither would have surfaced from a green test suite.
- **The gate held at every step.** `make run-golangci-lint` → 0 issues, `make manifests generate` → no drift, full envtest suite green across all 8 packages, re-verified independently on final HEAD before push.

## Misses

- **A subagent mischaracterized new-code lint failures as "pre-existing."** After Task 8, the implementer reported "7 pre-existing lint issues remain unchanged" — but all 7 were in files this change introduced (Tasks 1/3/4/7: `errcheck` on `os.ReadDir`/`Worktree`, `gocritic`, `perfsprint`, `govet unusedwrite`). Caught only because the orchestrator re-ran lint independently rather than trusting the report. Fixed in `db58b1a`. Lesson: "pre-existing" claims from subagents must be verified against the actual diff, not accepted.
- **A dispatch returned a misleading transport error.** The Task 2 dispatch surfaced `Unauthorized: Jwt is expired`, implying failure — but the work had actually committed successfully (`8693299` + `312c58d`). Only a disk check revealed the truth. The "never trust the completion wrapper" gate cut both ways here: a *failure* wrapper over *successful* work.
- **Stray untracked Task-3 files appeared before Task 3 ran.** After the Task 2 confusion, `helmloader.go`/`online_test.go` existed untracked (likely a prior partial/overreach). Discarded and re-run cleanly through Task 3's own TDD+review rather than adopting unreviewed code.

## Plan deviations

- **`gitResolver.fetchSHA` changed strategy** (Task 4): the plan specified a bare-SHA refspec (`<sha>:refs/heads/ddo-pin`, Depth:1), but git's `file://` dumb transport rejects it (`server does not support exact SHA1 refspec`). Implementer switched to fetch-all-branch+tag-refs (Depth:1) + `Checkout{Hash}` — still fail-closed (errors if SHA absent). Documented limitation: a non-tip ancestor SHA may not be reachable at Depth:1 (fails closed, never mis-renders). Accepted for pinned kustomize sources.
- **`credsFromSecretData` semantics hardened** beyond the original plan (`4aefb9b`): raised by Task 2 + Task 3 reviews, folded in at the Task 9 gate. `ok` now requires a non-empty password/token (`ok: pass != ""`), so a username-only Secret resolves anonymous instead of attempting empty-password basic auth.
- **Command vocabulary corrected at dispatch time.** The plan referenced `make test`/`make lint`/`make build`, but this SAP `go-makefile-maker` project uses `make check` / `make run-golangci-lint` / `go build` + `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./...`. Corrected in every subagent dispatch; the plan text itself was not rewritten (would be a good pre-apply fix next time).
- **Two extra reviews inserted** (pre- and post-Task-8) at user request; the pre-Task-8 one produced the migration guidance (`GetEventRecorder` takes a name arg in controller-runtime v0.24.1; `events.NewFakeRecorder`; the `events.k8s.io` RBAC gotcha) that made Task 8 mechanical.

## Skill / workflow compliance

- **Followed the schema apply sequence exactly:** pre-flight commit → worktree → subagent-driven-development → final review → verify → docs → finishing-a-development-branch. No step skipped.
- **Superpowers skills invoked via the Skill tool** (not paraphrased): `writing-plans`, `using-git-worktrees`, `subagent-driven-development`, `finishing-a-development-branch`; TDD + code-review enforced transitively per task.
- **"Never trust the completion wrapper" gate exercised every task** — each subagent's DONE was verified against disk (files exist, commit landed, tests/build re-run by the orchestrator). This directly caught the lint-mischaracterization and the JWT-error-over-success cases.
- **No scope leakage:** caching stayed out (deferred to Phase 7.5) — the final review confirmed `registry.ClientOptEnableCache(true)` is Helm's auth-token cache, not a chart cache.

## Surprises

- **Local `main` looked behind `origin/main` but wasn't a problem.** Pre-flight committed planning docs on local `main` (`0936f00`), which sat *on top of* `origin/main` (`e810690`), so the branch already contained the remote tip — `git rebase origin/main` was a no-op and `origin/main` was an ancestor of HEAD. The PR base was clean without any rebase. (Initial read that a rebase was needed was wrong; verified with `git merge-base --is-ancestor`.)
- **The events-API `FakeRecorder` exists and is drop-in enough.** `k8s.io/client-go/tools/events.NewFakeRecorder(n)` has the same `Events chan string` shape as the old `record.FakeRecorder`, so the `ShootUnreachable` assertion migrated with a one-line type change — the migration was smaller than feared.

## Promote candidates

- **→ project memory (already partially captured):** this project's real test/lint commands are `make check` / `make run-golangci-lint` / `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./...` — NOT `make test`/`make lint`. Plans and dispatches must use these. Worth encoding so future plans don't repeat the `make test` mistake.
- **→ workflow practice:** subagent "pre-existing issue" claims (lint, test failures) must be re-checked against the change's own diff before acceptance — they were wrong once this session in a way that would have shipped 7 lint violations.
- **→ Phase 7.5 (already scoped):** the carried-forward caching design (resolve-then-key against immutable digest/SHA, emptyDir, render-cache with transitive-tag caveat) is preserved in `brainstorm.md`'s "Deferred Decisions" section — pick it up there.
- **→ possible schema/skill note:** the events-API migration recipe (arg-taking `GetEventRecorder`, `events.NewFakeRecorder`, `events.k8s.io` RBAC) is a reusable snippet for any controller-runtime deprecation cleanup.
