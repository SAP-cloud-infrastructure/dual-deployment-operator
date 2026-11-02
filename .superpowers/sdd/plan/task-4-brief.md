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

