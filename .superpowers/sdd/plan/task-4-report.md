# Task 4 Report: Render wiring — classify→tree-walk→SHA-gate pipeline

**Commit:** `61aa386`  
**Date:** 2026-11-02  
**Branch:** `feat/kustomize-remote-bases-argo`

---

## Summary

Task 4 closes the gap identified in the task-4-brief: classification + tree-walk must run in **ALL modes** (including permissive/default), not just when the SHA gate is strict. Tasks 2 and 3 had already wired `preflightSHAGate` and `runKrusty`/`sanitizeKrustyError` into `Render()` — this task extends the pipeline to always exercise `treeWalkKustomize` for classification.

---

## Changes Made

### `internal/source/remotepolicy.go`

**`preflightSHAGate()`** — restructured into two explicit phases:

**Phase 1 (always runs):** `treeWalkKustomize()` is called unconditionally before krusty. This classifies every `resources`/`components`/`bases` entry and rejects:
- SSH refs (`git@host:repo`, `ssh://`)
- Embedded-credential URLs (`https://user:token@host`)
- URL fragments (`#`)
- Ambiguous multi-segment paths without `//` boundary
- Local path cycles
- Symlink boundary escapes

**Phase 2 (strict mode only):** SHA-pin checking. Every discovered remote ref must be pinned to a full 40-hex SHA. This gate is still gated by `requireSHAPinnedRemoteBases`.

**Before:** Permissive mode returned `nil` immediately, skipping both tree-walk and SHA checks. SSH refs, embedded credentials, etc. would pass through to krusty unchecked.

**After:** Permissive mode runs Phase 1 (classification) but skips Phase 2 (SHA check). Security invariant holds in both modes.

---

### `internal/source/kustomize.go`

**`preflightIsolationRoot(root, subPath)`** — new helper function.

**Problem discovered:** `Render()` passes the overlay subpath root (e.g. `testdata/kustomize/seed`) to `preflightSHAGate`. The tree-walk uses this path as its isolation boundary. A standard kustomize overlay references sibling directories via `../base`, which escapes the overlay subpath root — this caused the existing testdata fixtures to fail boundary checks with the new always-run tree-walk.

**Root cause:** The RootResolver interface returns `filepath.Join(cloneDir, subPath)`, not `cloneDir`. In production, `../base` from within `seed/` stays within the clone directory. In the testdata case, `../base` escapes the `testdata/kustomize/seed` boundary incorrectly.

**Fix:** Compute the "isolation root" by stripping the subPath suffix from the resolved root. This recovers `cloneDir` (the correct boundary). krusty still receives the original overlay path for rendering.

```
preflightRoot = preflightIsolationRoot(root, subPath)
// root   = /tmp/clone-X/seed
// subPath = "seed"
// result  = /tmp/clone-X  (correct isolation boundary)
```

Fallback: if root doesn't end with the expected subPath suffix, use root itself (safe — may reject valid `../` refs in misconfigured test setups but never fails production).

**`Render()` now calls:** `preflightSHAGate(preflightIsolationRoot(root, subPath), k.requireSHAPinnedRemoteBases)`.

---

### `internal/source/kustomize_test.go`

**TDD RED→GREEN→REFACTOR cycle:**

**4.1 RED (3 failing tests confirmed the gap):**
1. `TestTask4_SSHRefRejectedInPermissiveMode` — FAIL (SSH ref passes through to krusty in permissive mode)
2. `TestTask4_EmbeddedCredentialRefRejectedInPermissiveMode` — FAIL (credential URL passes through)
3. `TestTask4_ClassificationRunsBeforeKrustyInPermissiveMode` — FAIL (cycle detection skipped in permissive mode)

Additional GREEN/passing tests written at RED stage (already correct before the fix):
- `TestTask4_StrictModeUnpinnedFailsBeforeKrusty` — already PASS (covered by Task 2)
- `TestTask4_PermissiveModeUnpinnedTagAllowed` — already PASS
- `TestTask4_LocalOnlyTreeUnchanged` — became FAIL after Phase 1 was added (fixed by `preflightIsolationRoot`)
- `TestTask4_StrictModeLocalOnlyTreeUnchanged` — already PASS

**4.2 GREEN:** All 7 Task 4 tests pass after the `remotepolicy.go` + `kustomize.go` changes.

**4.3 REFACTOR:** 
- Added `TestPreflightIsolationRoot` (5 sub-cases) to unit-test the new helper.
- Investigated dedup opportunities between `remotepolicy.go` and `gitresolver.go`:
  - `shaRe` (gitresolver, 7-40 chars) vs `reSHA40` (remotepolicy, exactly 40 chars) — intentionally different semantics; dedup would be wrong.
  - URL parsing (`url.Parse`, userinfo check) — used in different validation contexts, different inputs; dedup would reduce clarity without benefit.
  - No safe dedup opportunity identified. Classification grammar in `remotepolicy.go` is self-contained.

---

## Test Results

```
go test ./internal/source/... -count=1 -timeout 60s
ok  github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source  13.162s
```

100% pass (all tests including all Task 2, 3, 4 tests).

Pre-existing `internal/webhook/v1alpha1` failure (missing etcd binary at `/usr/local/kubebuilder/bin/etcd`) confirmed unrelated.

---

## Invariants Preserved

| Invariant | Status |
|-----------|--------|
| Local-only trees (no remote refs) render unchanged | ✓ |
| Strict mode zero-fetch: `kustomizeFetchHook` not reached on rejection | ✓ |
| Permissive mode: tag/branch refs pass through to krusty | ✓ |
| Error messages never echo raw URLs or credentials | ✓ |
| `LoadRestrictionsNone` + `manifest.Parse(OriginUpstream)` + `ApplyNamespace` unchanged | ✓ |
| `RootResolver` interface unchanged | ✓ |
| Helm, render cache, deployment egress policy untouched | ✓ |

---

## Security Invariant: Classification in ALL Modes

The key security property established by Task 4:

**Before Task 4:** In permissive mode, an attacker who controls a kustomization.yaml could include an SSH ref (`git@github.com:org/repo`) or embedded-credential URL. These would pass through `preflightSHAGate` (which was a no-op) and reach krusty, which would attempt to contact the SSH endpoint or use the embedded credentials. The authSecretRef bypass would be complete.

**After Task 4:** Both SSH refs and embedded-credential URLs are rejected by classification (Phase 1) in ALL modes, before krusty is ever invoked. The `kustomizeFetchHook` never fires for these cases, proving zero-fetch semantics.
