# Retrospective: transformations (Phase 3 — `internal/transform`)

Evidence-first. Every claim cites a commit, file, or measurable fact from this change.

## Wins

- **TDD via subagent-driven-development held up.** Each of Tasks 1–6 was a fresh implementer with a single-task scope guard; every task committed RED→GREEN in order (`3188dcd` → `b4b3a5e` → `81ce458` → `cdc60f1` → `e72c412` → `e7c5ce9`). Final `go test ./internal/transform/...` = 14 test functions PASS, `golangci-lint` = 0 issues.
- **Empirical grounding prevented speculative surface.** Two "do we need instance selection?" questions (filterKinds, and the injector-label patch) were answered by reading the real `sapcc/helm-charts` `system/Makefile` and rendered `webhooks.yaml`/`crds-and-rbac.yaml` for all five operators — every filter/label is whole-kind, so no `name`-selector was added (design.md Open Questions, resolved).
- **Code review caught a real correctness bug.** The reviewer flagged that `strategicMerge` implemented as RFC 7386 JSON merge patch would wipe existing `containers` on sidecar injection — the primary metal-operator use case. Fixed in `fab1b80` (GVK→typed-struct via client-go scheme + `strategicpatch.StrategicMergePatch`, JSON-merge fallback for CRs), proven by `TestPatchStrategicMergeMergesContainersByName` (`10addc5`).

## Misses

- **The plan pre-selected the wrong strategicMerge implementation.** plan.md Task 5 offered `jsonpatch.MergePatch` OR `strategicpatch.StrategicMergePatch` and the dispatched implementer chose MergePatch — behaviorally wrong for merge-keyed lists. The plan's own "note" hedged instead of mandating the schema-aware path. Cost: one extra review→fix→re-review cycle.
- **golangci-lint was not run per-task.** Tasks 1–6 verified `gofmt`/`vet`/`go test` but not `golangci-lint`; the errcheck/gocritic issues surfaced only at the Task 7 gate, requiring a dedicated cleanup commit (`34fd5e4`). Running the repo linter inside each task would have caught them earlier.
- **Experimental implementer model needed constant disk-verification.** Every subagent ran on `google/gemini-3.5-flash` (flagged unstable) and returned a "SUCCESS" wrapper; each required independent `git log`/`go test` verification against disk before proceeding. No false success occurred, but the overhead was real.

## Plan deviations

- **Task 7 (gates) expanded.** Planned as a pass/fail verification; became an actual fix pass (`34fd5e4`) because golangci-lint found 11 errcheck + 1 gocritic issues. Handled directly by the orchestrator (small, mechanical) rather than a subagent.
- **`Build` exactly-one enforcement added mid-review.** The plan/spec required rejecting empty entries; the reviewer correctly noted multi-field entries were silently accepted (priority switch). Tightened in `fab1b80` + `TestBuildMultipleFieldsErrors`.
- **`go.mod` touched.** `sigs.k8s.io/yaml` promoted to direct (test helper) in Task 1; `client-go/kubernetes/scheme` already present, so the strategic-merge fix added no new dependency.

## Skill / workflow compliance

- Invoked as mandated: `using-git-worktrees` (worktree `.worktrees/transformations`, branch `feat-transformations`), `subagent-driven-development` (Step 2a), `writing-plans` (plan artifact), `openspec-verify-change` (Step 4), `finishing-a-development-branch` (Step 6).
- Superpowers precedence honored in the SDD lane: TDD + code-review ran transitively via subagent-driven-development; `code-reviewer` agent used for both per-change and final holistic review (`ses_090e3cd7…`, APPROVE).
- Scope guards enforced: no implementer wrote verify.md, synced specs, archived, or ran branch finishing; all used `git add <specific files>`.

## Surprises

- **repo errcheck flags `_ =`-discarded errors.** The initial `_ = unstructured.SetNestedSlice(...)` still tripped errcheck, forcing real error propagation (`rewriteWebhookList`/`rewriteConversion` now return `error`). Good outcome — the code is genuinely more correct.
- **YAML→map numbers are `float64`.** `sigs.k8s.io/yaml` round-trips through JSON, so `replicas: 1` became `float64(1)`; a strict `nestedInt64` helper `t.Fatalf`'d on it, exposing that the original no-mutation test asserted nothing. Fixed by comparing via `reflect.DeepEqual` (`TestApplyDoesNotMutateInput`).

## Promote candidates

- **→ plan-writing guidance:** when a plan step has a correctness-critical implementation choice (e.g. strategic-merge vs JSON-merge), MANDATE the correct option with a failing test that discriminates them — never offer an either/or the implementer can get wrong. (Classify: writing-plans skill improvement.)
- **→ subagent-driven-development / per-task gate:** run the repo's actual linter (`golangci-lint`, not just `gofmt`/`vet`) inside each task's GREEN step when the repo's `check` target uses it. (Classify: workflow rule.)
- **→ CLAUDE.md/AGENTS.md note (docs-sync, separate change):** `docs/design.md` + `docs/context.md` invert metal-operator ↔ ipam-capi webhook/CRD characteristics (metal has 0 conversion CRDs + 1 VWC; ipam-capi has 4 conversion CRDs + MWC + VWC). Recorded in design.md Open Questions; fix in the docs-sync pass. (Classify: doc correction.)
