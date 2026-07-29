# Apply Handoff — equivalence-tests

**Branch:** `feat/equivalence-tests`
**Worktree:** `/Users/D065300/IdeaProjects/sapcc/dual-deployment-operator/.worktrees/equivalence-tests`
**Schema:** `sdd-plus-superpowers` — apply sequence (Step 2a subagent-driven-development).

## UPDATE 2026-07-29 (resume 3): 4/5 operators green; resolver fixed; ipam-capi blocked on a 2nd finding

Decision B taken (fix production resolver). Progress:
- gitresolver historical-SHA bug FIXED + regression test — commit `476bbb5`; documented (spec delta + design note) — commit `3019b6f`. `fetchSHA` no longer shallow; arbitrary historical SHAs resolve.
- 4 of 5 operators GREEN + committed: metal-operator (`f6c0187`), khalkeon (`5c1077e`), boot-operator (`0f08b7f`), argora-operator (`7b8f88d`). Offline suite clean; full 4-op network suite passed (191s).
- Scoped-equivalence machinery complete: `Scope{ComparedKinds, KnownDivergences, IgnoreLabels, CanonicalNamespace}`; global allowlist incl. helm.sh/resource-policy + app.kubernetes.io/instance.

### ⛔ ipam-capi SECOND blocker (kustomize root-vs-subpath) — needs decision
With the resolver fixed, the ipam feasibility probe got further and hit a NEW structural incompatibility: ipam-capi's kustomize overlays reference patch files by **repo-root-relative** paths, e.g. `manager/kustomization.yaml`:
```
patches:
- path: kustomize/ipam-capi-remote/manager/manager-remote-patch.yaml
```
Today's `make build-ipam-capi-remote` runs kustomize **from the repo root**. But the operator's `KustomizeSource` model resolves the root to the `seedPath`/`shootPath` SUBDIR, so krusty looks for the patch at `<subpath>/kustomize/ipam-capi-remote/manager/...` — a doubled path → `no such file or directory`.
- The `managedresources` (shoot) overlay IS self-contained (only remote `resources:` URLs, no local paths) — the shoot side alone would likely render.
- The `manager` (seed) overlay is the blocker (repo-root-relative `patches[].path`).
This is a design-level incompatibility between ipam-capi's kustomize layout and the operator's `KustomizeSource` `url`+`seedPath`/`shootPath` contract (design §9.4) — NOT a fixture-tuning or quick-fix item. Options: (i) extend KustomizeSource so the clone root and the kustomize-build dir can differ (build from repo root, target overlay dir) — a CRD/source change, real scope; (ii) shoot-only ipam equivalence (skip the seed/manager overlay) with a documented divergence; (iii) defer ipam-capi as a scoped follow-up and ship 4 operators now.

## UPDATE 2026-07-29 (resume 2): Tasks 7-9 done (option A), Task 10 in progress

Decision A adopted: golden = render wrapper chart from git@SHA (clone + `helm dependency build` + `helm template`). VERIFIED working (82s network run).
- Task 7 `golden_render.go` (git-source render) — commit `31822a4`, reviewed APPROVED.
- SPDX headers added to all internal/equivalence/*.go — commits `cd1e9a8` + `9eb8378`.
- Task 8 `operator_capture.go` — commit `2b97ddd`, verified.
- Task 9 fixture model + metal-operator fixture (git-source shape) — commit `d4ceed4`, verified.
- Task 10 harness `equivalence_test.go` WIP: fixed a STRUCTURAL bug found in triage — MR-unwrapped payloads were routed to SEED but must be SHOOT (design §3.5: managedresources/* = shoot-destined). `ClassifyGolden(docs, shootFromMR, opts)` + `UnwrapManagedResources -> (fromMR, passthrough, err)` now route MR payloads to shoot. Offline suite green; network TestEquivalence/metal-operator NOT yet green — needs fixture CALIBRATION (see below).

### ⚠️ Task 10 calibration finding (needs decision)
Triage pass 3 (seed/shoot inversion fixed) shows the operator (renders UPSTREAM metal-operator-core chart directly) and the golden (renders the -remote WRAPPER which disables most of upstream + substitutes pre-rendered managedresources/ + sapcc additions) emit substantially different object sets:
- Operator EXTRA (upstream-only, not in wrapper): per-CRD `*-admin/editor/viewer-role` ClusterRoles, cert-manager Issuer/Certificate, metrics services/roles, upstream manager-role — all in shoot--cp--m-qa-de-1 ns.
- Golden MISSING-on-operator (wrapper additions upstream doesn't emit): Ingress, NetworkPolicies, metal-registry Service, webhook-injector RBAC, remote-kubeconfig ConfigMap, macdb Secret, owner-info, token-rotate SA/RBAC.
- Namespace mismatch: operator emits in shoot--cp--m-qa-de-1; golden additions are namespace-less or kube-system.
Calibrating this to green = reproducing the wrapper's exact enable/disable value matrix + additions in the fixture CR, per operator. This is large and is the crux of the equivalence work — needs a decision on approach before burning more 90s network cycles.

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

## ⛔ ESCALATION (2026-07-29 resume): GHCR golden-source assumption is FALSE

Investigated the Task 7 `not found` conclusively. The brainstorm/design decision
"pull the published `<op>-remote` wrapper chart from GHCR at a pinned chart version"
**does not hold against reality**:

- SDK sanity check PASSES: `oci://ghcr.io/ironcore-dev/charts/metal-operator@0.6.2-crds` pulls fine — so the helm-SDK code + egress work.
- ALL `sapcc/helm-charts` `-remote` charts fail across every path/tag variant tried:
  - `oci://ghcr.io/sapcc/helm-charts/charts/metal-operator-remote@0.6.30` → not found
  - `@v0.6.30` → not found; `.../charts/khalkeon-remote@0.1.1` → not found; `.../charts/boot-operator-remote@0.4.21` → not found
  - `.../charts/metal-operator-remote/tags/list` → HTTP 404 "repository name not known to registry"
- `helm-push.yaml` only publishes charts `ct list-changed` detects in that push, under the Chart.yaml version at push time. Last run: 2026-07-15. metal-operator-remote bumped 2026-07-24 (9 days later, so 0.6.30 was never published). The published tag for any given chart is whatever version it had the last time it happened to be in a changed-set — NOT a stable, predictable pin.

**Conclusion:** GHCR is not a reliable golden source for these wrapper charts (unpredictable/absent tags; possibly private repos too). This is a DESIGN-LEVEL decision to revisit with the user, not an implementer fix. Options presented to user (awaiting decision):
- A. Golden source = render the wrapper chart from `sapcc/helm-charts` git **source** at a pinned SHA (git clone + `helm dependency build` + `helm template`). Robust, but needs git egress + transitive dep pulls (owner-info from keppel, upstream subchart from ghcr).
- B. Golden source = vendor a pinned snapshot of each `-remote` chart into `testdata/` and `helm template` the vendored copy. Fully offline/deterministic; re-vendor is a deliberate step. (This is brainstorm Option A, originally deprioritized.)
- C. Keep GHCR but drop the per-PR gate: resolve each chart's actually-published tag dynamically (list tags, pick latest) — fragile, and tags may still be absent.

Task 7 `golden_render.go` (GHCR `helm pull`) stays uncommitted pending this decision. If B is chosen, `RenderGolden` changes from pull-by-ref to load-from-vendored-path; if A, it becomes clone+depbuild+template.

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
