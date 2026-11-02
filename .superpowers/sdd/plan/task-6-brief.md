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

