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

