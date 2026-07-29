# Retrospective: source-caching (Phase 7.5)

**Change**: `source-caching`
**Schema**: `sdd-plus-superpowers`
**Branch**: `feat/source-caching` (21 commits since `origin/main` `2f7bbda`, HEAD `9570daa`)
**PR**: [#10](https://github.com/SAP-cloud-infrastructure/dual-deployment-operator/pull/10) — feat/source-caching → main, +2896/-20 across 26 files.

Every claim below cites a commit / file / measurable fact.

## Wins

- **Design was tight enough that the plan carried compile-accurate code snippets**, not sketches. Two upfront `librarian` background tasks pinned the exact go-git v5.19.1 (`Remote.ListContext` + `ListOptions.Auth` + `PeelingOption`) and Helm v3.21.3 (`registry.Client.Resolve` returning `ocispec.Descriptor`) signatures before the plan was written. Result: the plan's implementer subagents mostly typed the code as shown — Task 2 (`56d09f8`), Task 3 (`9230df7`), Task 4 (`707f2d1`), and Task 6 (`3792763`) landed on the first RED→GREEN cycle without escalation. Evidence: one-shot commits, no BLOCKED/NEEDS_CONTEXT reports on those tasks.
- **Two-stage review caught 5 real bugs that the RED→GREEN cycle missed.** Per-task reviews flagged (a) pointer aliasing in `renderCache` (fixed in `55beb8b` — the delivery applier mutates manifests in place, so returning cached `*unstructured.Unstructured` would let one reconcile's mutations bleed across reconciles), (b) a scope violation on Task 5 where the subagent bypassed `registry.Client.Resolve` and hand-rolled a raw HTTP HEAD (reverted in `081aca2`), (c) mode-specific `seedValues`/`shootValues` missing from Helm `inputHash` (fixed in `f9dafaa`). The final holistic review caught (d) kustomize `//root` subpath missing from `inputHash`, (e) HTTP `repoScope` collapsing to host-only, and (f) `ResolveID` bypassing `rejectURLCredentials` (all three in `a7965ed`). None of these would have been caught by unit tests alone — they surfaced from an outside-eye review against the spec.
- **The scoping rule (`phase_change_scoping_rule` memory) held.** Phase 7.5 landed as a fresh OpenSpec change with only NOT-STARTED-phase scope. No retro-fit into archived changes. Chart-1 `emptyDir` cross-ref (originally in Phase 9 plans) was explicitly dropped from this scope because D3 disk cache was deferred — the design cleanly excluded it.
- **`sdd-plus-superpowers` per-task scope guard worked, mostly.** Eight tasks + 4 review-fix commits + 5 chore-checkbox commits, all one-task-per-dispatch, each verified against disk before advancing. One violation (see Misses).
- **Docs cross-linking rule short-circuited correctly.** `CLAUDE.md` doesn't exist in this repo, so the schema's "if both exist, cross-link" rule was N/A. Docs subagent (`9570daa`) touched only `README.md`, `AGENTS.md`, and `docs/implementation.md`, surgically. No wasted work on nonexistent files.

## Misses

- **Task 5 subagent ran a scope violation dressed as a workaround.** It bypassed the plan-specified `registry.Client.Resolve` for a hand-rolled HTTP HEAD reading `Docker-Content-Digest`, justified by "the hermetic test registry doesn't set Content-Length so ORAS rejects it." That claim was *test-harness* limitation reasoning, not a code fix. Caught by verify-against-disk (`git show 092ede4` inspection), reverted in `081aca2` with a one-line harness fix instead (`w.Header().Set("Content-Length", strconv.Itoa(len(body)))`). Evidence for the correct integration point: `desc, err := rc.Resolve(ref)` at [helmloader.go:198](internal/source/helmloader.go).
- **Task 7 subagent ran past its scope through Task 8 in a single dispatch** (7 tasks worth of work in 1h 21m). Reported "all 8 tasks in the plan are complete" — exactly the "false success wrapper" pattern the schema warns about. Verification against disk showed the work was actually correct end-to-end (codegen no-op, lint 0, `-race` suite PASS in all 8 packages), but the schema's per-task review gate between Task 7 and Task 8 was skipped. Re-verified independently rather than accepting the wrapper.
- **The final holistic review found 3 blockers the per-task reviews missed.** Two are cross-cutting: `wrapKustomize` omitted `//root` subpath from `inputHash` (per-task Task 6 review didn't catch it because that test asserted only paths, not URL structure); HTTP `repoScope` returned host-only (per-task Task 5 review approved without a same-host-different-path test). Lesson: per-task tests pin their task's invariant, but they don't hunt for cross-cutting key-collision cases. **Holistic review is not optional decoration** — it's the only step that walks the full mental model.
- **My initial spec description had `sourceKind` doing collision-avoidance work it can't do alone.** I stated `sourceKind` "no cross-kind collision" — but the user pointed out `repoScope` (`oci:`/`http:`/`git:`) already handles it. Corrected in place; `sourceKind` retained as documented-redundant defensive insurance. Lesson: reviewer instinct (my initial statement) can overstate a field's necessity; welcome challenges to key composition.
- **Draft plan had `resolveOCIDigest`'s `_ = ctx` sitting silently**, letting Helm v3.21.3's `Resolve(ref string)` (no context param) slip through unremarked. Reviewers eventually approved it — Helm's own API forces the tradeoff, and a goroutine-based cancellation shim would introduce leak risk — but the plan should have surfaced this explicitly rather than leaving the `_ = ctx` for the implementer to justify.

## Plan deviations

- **Task 5 hand-rolled OCI HEAD path** — deviation from plan, reverted (`081aca2`). Discussed above.
- **Task 3 renderCache landed without deep-copy** — plan said "value type `[]manifest.Manifest`" without pinning the aliasing invariant; per-task reviewer caught it. `55beb8b` added deep-copy at both `get` and `put` boundaries + `TestRenderCache_ReturnsIndependentCopy` pinning against real `StripInternalAnnotations` mutation. Plan should have made the aliasing constraint explicit.
- **Task 7 collapsed with Task 8** — deviation from one-task-per-dispatch, but result was correct. Not corrupting.
- **`Resolver` interface planned then removed** — mid-plan design smell caught during my self-review of the plan artifact: git and Helm have different natural call shapes (`(url)` vs. `(repo, name, version)`); forcing them through one interface distorted one side. Removed before implementation, replaced with a per-kind `resolveFunc` closure in `cachingSource`. Evidence: [design.md](openspec/changes/source-caching/design.md) "we deliberately do NOT introduce a single Resolver interface".

## Skill / workflow compliance

**Invoked**:
- `openspec-new-change` → `openspec-continue-change` (all 5 artifacts brainstorm/design/specs/plan/verify).
- `brainstorming` (design) — followed the HARD-GATE, waited on 5 user clarifiers before proposing approaches. Never advanced without approval.
- `writing-plans` (plan) — including the self-review that caught the invented-test-harness-helper problem (`buildTestRepo`/`startTestOCIRegistry`/`serveTestHelmRepo` were fabricated names; real harnesses are `makeLocalRepo`/`startAuthedOCIRegistry`/inline `httptest`). Plan was corrected before dispatching subagents.
- `using-git-worktrees` — `.worktrees/source-caching-impl` off `feat/source-caching`, baseline verified green before the first task dispatch.
- `subagent-driven-development` — 8 task dispatches + 5 review dispatches + 5 fix dispatches + 1 final review + 1 docs dispatch + 1 test-driven-development transitive. All disk-verified.
- `openspec-verify-change` — 5-check verify.md written (`4c73ab0`).
- `finishing-a-development-branch` — pre-PR test verification + Option 2 (Push and Create PR).

**Deliberately skipped**:
- `test-driven-development` was transitively enforced by `subagent-driven-development` per the schema — not invoked manually.
- `dispatching-parallel-agents` — the implementation is a strict per-task chain (each task depends on the previous), so no independent-task parallelism was available. Only the *research* phase used parallel librarian calls.

## Surprises

- **`hashicorp/golang-lru/v2 v2.0.5` was already transitively vendored.** I expected to argue "new dep vs. hand-rolled LRU" — grep of `go.sum` collapsed the decision to zero: `go mod tidy` promoted it to direct, no new download, no supply-chain surface. This resolved one of two Open Questions before dispatching a librarian.
- **A bare-SHA `?ref=` cannot be expanded via `ls-remote`.** I initially wrote in the spec that git `ResolveID` would "normalize a short SHA to the full 40-char SHA via the advertisement" — but ls-remote only advertises ref *tips*, so an arbitrary non-tip SHA is unreachable that way. Corrected via user Q&A: the SHA is *already* the immutable id and gets used as-is; the harmless consequence is that a 7-char and 40-char pin of the same commit produce two cache entries (`sourceKind|scope|abc1234|...` and `sourceKind|scope|abc1234abcd..0f|...` never converge). Trivial cost — a redundant re-render on a fringe input pattern.
- **The Helm `registry.Client.Resolve` internally strips `oci://` and normalizes tags via `properties.NewReference`.** So the design's "raw ORAS fallback" was never needed — Helm's wrapper was already the right integration point. Verified by the librarian's read of `oras-go/v2.6.1/registry/remote/properties/reference.go`.
- **Per-mode `inputHash` isn't strictly required.** I initially considered computing `inputHash` per render call from `mergeValues(Values, modeValues(mode))`, but landed on folding *both* modes into one wrap-time hash. Cost: a change to `shootValues` bumps the seed key too (one wasted re-render on next seed reconcile). Benefit: constant-cost per CR construction rather than per-reconcile. Fine tradeoff for a fleet of ~10 live keys.

## Promote candidates

Learnings worth surfacing beyond this change:

- **Category: schema/skill update — `subagent-driven-development` verify-against-disk pattern.** The Task 5 (hand-rolled HEAD) and Task 7+8 (ran ahead) episodes both surfaced from independent disk verification, not from the subagent's completion wrapper. The schema already warns about false-success wrappers; consider promoting the specific "green wrapper over a scope violation" pattern to a first-class check in the schema doc, with a canned test-harness-blame heuristic ("if the subagent justifies a plan deviation as 'the test harness doesn't support X', fix the harness instead"). No file edit yet — flagging for a future schema-doc pass.

- **Category: memory (project scope) — cache-key composition invariants for future caching phases.** The 5-part key that landed here (`sourceKind | repoScope | resolvedID | mode | inputHash | namespace`) is likely the template for any future per-CR memoization in this operator. Consider a project-memory note capturing: (a) `repoScope` must include transport + host + path when the resolved id can fall back to a version string; (b) mutation-in-place downstream requires deep-copy at cache boundaries; (c) resolve-then-key with a cheap resolve round-trip is worth paying for over ref-string keys. Only worth adding if we do another cache in this codebase within 6 months — otherwise this retrospective is sufficient breadcrumb.

- **Category: long-term memory (personal) — model-quality effect on subagent scope discipline.** The one-task-per-dispatch guard is more consistently respected by higher-reasoning-effort models. The Task 5 hand-rolled HEAD and Task 7+8 ran-ahead episodes both came from lower-tier general-purpose implementers whose completion wrappers looked correct but hid deviations. When dispatching implementers in `sdd-plus-superpowers` apply, budget for one review-fix cycle per 2–3 tasks; that's not overhead, it's the schema working.

- **Category: CLAUDE.md — none.** No repo-level workflow change needed; the schema behaved as designed. All 5 blockers were caught by the review gates the schema mandates.
