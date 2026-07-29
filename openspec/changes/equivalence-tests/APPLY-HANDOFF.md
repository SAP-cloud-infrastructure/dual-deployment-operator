# Apply Handoff — equivalence-tests (paused mid-Task 7)

**Branch:** `feat/equivalence-tests`
**Worktree:** `/Users/D065300/IdeaProjects/sapcc/dual-deployment-operator/.worktrees/equivalence-tests`
**Schema:** `sdd-plus-superpowers` — apply sequence (Step 2a subagent-driven-development).
**Paused:** 2026-07-29, mid Task 7.

## Progress: 6 of 12 tasks complete (committed + independently verified)

| Task | Commit | State |
|---|---|---|
| 1 set.go (resource identity key) | `ee78bd7` | ✅ done, verified |
| 2 normalize.go | `840d5a2` | ✅ done, verified |
| 3 allowlist.go | `9767a7b` | ✅ done, verified |
| 4 compare.go (+ field-path fix `cc069da`) | `382b206` | ✅ done, reviewed, fixed, verified |
| 5 golden_classify.go (+ robust-YAML fix `a1303ed`) | `253ef0a` | ✅ done, reviewed, fixed, verified |
| 6 UnwrapManagedResources | `d019187` | ✅ done, verified |
| plan.md checkboxes 1-6 | `2f6dbf1` | ✅ committed |
| 7 golden_render.go (GHCR pull + helm template) | — | 🔶 PARTIAL, uncommitted |
| 8-12 | — | ⬜ not started |

Offline suite (Tasks 1-6): **11 tests PASS**. Full build/gofmt/vet clean.

## Task 7 — where it stopped and the ONE blocker to resolve

The implementer timed out (30m — it was a network task on the experimental model). It left **uncommitted, buildable, gofmt-clean** files:
- `internal/equivalence/golden_render.go`
- `internal/equivalence/golden_render_test.go`

The code is correct and reuses the proven helm-SDK pattern from `internal/source/helmloader.go`. Running the test reveals a **real finding, not a code bug**:

```
golden: pull oci://ghcr.io/sapcc/helm-charts/charts/metal-operator-remote@0.6.30:
  FetchReference ... metal-operator-remote:0.6.30: not found
```

**Network egress to ghcr.io WORKS** (it reached the registry and got a definitive `not found`). So the OCIRef path and/or version pin is wrong. **RESOLVE FIRST on resume:**
1. Find the real published OCI path + tag for the `-remote` charts. The `sapcc/helm-charts` `helm-push.yaml` workflow pushes `helm push <pkg> oci://ghcr.io/${{ github.repository }}/` — `github.repository` is `sapcc/helm-charts`, so the path is likely `oci://ghcr.io/sapcc/helm-charts/<chart-name>` (NOT `/charts/<name>`), OR the chart tag differs from the `Chart.yaml` `version`. Anonymous `ghcr.io/v2/.../tags/list` token probing was DENIED in the paused session — try `helm pull oci://ghcr.io/sapcc/helm-charts/metal-operator-remote --version 0.6.30` and `.../charts/metal-operator-remote` directly, or `crane ls`/`skopeo list-tags`, or check the workflow's actual push path in `.github/workflows/helm-push.yaml` on `sapcc/helm-charts@master`.
2. Once the correct ref/version is known, update `TestGoldenRenderMetalOperatorPulls` (and later the fixture in Task 9), confirm PASS, then commit Task 7 with:
   `git add internal/equivalence/golden_render.go internal/equivalence/golden_render_test.go && git commit -m "test(equivalence): golden GHCR pull + helm template render"`
3. Then dispatch a code-quality reviewer for Task 7 (network/integration task — review warranted), fix any findings, mark Task 7 `[x]` in plan.md.

Possibility to weigh on resume: if the `-remote` charts turn out NOT to be anonymously pullable from GHCR (private packages), the brainstorm's "GHCR pull, gates every PR, no auth" decision needs revisiting — that would be a design-level escalation, not an implementer fix.

## Remaining tasks (plan.md has full TDD steps for each)
- **7** (partial): golden render — resolve the OCI ref/version blocker above.
- **8**: `operator_capture.go` — drive `source.From → Render → transform.Build → Apply`, capture 2 ObjectSets (offline-ish, uses fake ChartLoader in test).
- **9**: fixture model + metal-operator fixture (`testdata/fixtures/metal-operator/`).
- **10**: end-to-end harness + metal-operator gating subtest — the **run-and-triage loop** (calibrate allowlist/fixture against the live diff).
- **11**: fan out to boot/argora/khalkeon/ipam-capi fixtures.
- **12**: verify full suite + `make check`, confirm no build-tag/env-gate.

## After all 12 tasks (schema apply steps 3-6, do NOT skip)
3. Final holistic code-reviewer subagent over `origin/main..HEAD`.
4. `verify` artifact via `openspec-verify-change`.
5. `@docs` subagent (README/AGENTS.md/CLAUDE.md).
6. `superpowers:finishing-a-development-branch` → single PR `feat/equivalence-tests` → `main` (per user's PR-workflow preference: ONE feature branch → PR to main, never a stacked PR).

## How to resume
`/opsx-apply equivalence-tests` (it will re-read status + this handoff), or directly continue dispatching Task 7 in the worktree. Executor discipline reminders that held this session:
- ONE task per implementer subagent; independently verify every "DONE" against disk (files exist, commit landed, re-run tests yourself) — the experimental model returns a success wrapper even on timeout.
- Use `category=quick` for mechanical tasks (Sisyphus-Junior); `subagent_type=code-reviewer` for reviews.
- NOTE the tool quirk observed: `edit` on `plan.md` checkboxes did not always persist in the worktree; prefer committing checkbox flips (as commit `2f6dbf1` did) rather than trusting the edit alone.
