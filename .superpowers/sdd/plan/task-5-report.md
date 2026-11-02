# Task 5 Report: Operator flag + manager wiring

## Status: DONE

## What was implemented

Added the `--kustomize-require-sha-pinned-remote-bases` CLI flag (default `false`) to
`cmd/main.go` and wired its value into `source.Deps.RequireSHAPinnedRemoteBases` so
that strict SHA-pin enforcement is now reachable from the operator binary.

## TDD cycle

### RED
`cmd/main_test.go::TestKustomizeRequireSHAPinnedFlagRegistered` was written first.
It calls `flag.Lookup("kustomize-require-sha-pinned-remote-bases")` and asserts the
flag exists with `DefValue == "false"`. Test FAILED because the flag was not yet
registered.

Additional tests in `internal/source/source_test.go`:
- `TestDepsRequireSHAPinnedDefaultFalse` – asserts `Deps{}` zero value is `false`
- `TestFromThreadsRequireSHAPinnedIntoKustomizeSource` – asserts `From()` threads
  `Deps.RequireSHAPinnedRemoteBases` (both `false` and `true`) into the constructed
  `kustomizeSource` (already wired by Task 2; tests pass from the start as expected
  per the partial-progress note).

### GREEN
Implementation:
1. Declared `var requireSHAPinnedRemoteBases bool` at package level in `cmd/main.go`
2. Registered the flag via `flag.BoolVar` in `init()` (package-level init, not inside
   `main()`) so that tests can observe it without invoking `main()`
3. Assigned `RequireSHAPinnedRemoteBases: requireSHAPinnedRemoteBases` in the
   `source.Deps` literal at `cmd/main.go:217`

### REFACTOR
No structural refactoring needed; the change is minimal and idiomatic.

## Files changed

| File | Change |
|------|--------|
| `cmd/main.go` | Added `var requireSHAPinnedRemoteBases bool` (pkg-level), registered flag in `init()`, added `RequireSHAPinnedRemoteBases: requireSHAPinnedRemoteBases` to `source.Deps` |
| `cmd/main_test.go` | New file: `TestKustomizeRequireSHAPinnedFlagRegistered` (RED→GREEN) |
| `internal/source/source_test.go` | Added `TestDepsRequireSHAPinnedDefaultFalse` and `TestFromThreadsRequireSHAPinnedIntoKustomizeSource` + `unwrapKustomizeSource` helper |

## Test results

All unit tests pass (envtest suites fail due to missing kubebuilder binaries in this
environment — pre-existing, unrelated to this task):

```
ok  github.com/SAP-cloud-infrastructure/dual-deployment-operator/cmd
ok  github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source
ok  github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1
ok  github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest
ok  github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/transform
ok  github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/deliver
ok  github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/clients
ok  github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/equivalence
FAIL github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/controller  (envtest: no etcd)
FAIL github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/webhook/v1alpha1 (envtest: no etcd)
```

golangci-lint: 0 issues on `./cmd/...`

## Concerns

None. The implementation is minimal and intentional. The flag is off by default, matching
the spec requirement. Strict mode is now reachable by passing
`--kustomize-require-sha-pinned-remote-bases=true` to the operator binary.

---

## Fix Round 1 (review finding: missing regression coverage on the assignment path)

### Finding

The initial `cmd/main_test.go` only checked flag registration metadata
(`flag.Lookup(...).DefValue`). The source-level tests injected `Deps` directly, bypassing
the `buildSourceDeps` / `main()` construction path entirely. Deleting
`RequireSHAPinnedRemoteBases: requireSHAPinned` from the Deps constructor would have left
every test green — the wiring was untested.

### Fix

Extracted the inline `source.Deps{...}` literal in `main()` into a package-level
`buildSourceDeps(scratchDir string, requireSHAPinned bool) source.Deps` function. This
makes the exact construction path testable without starting a manager.

Two new tests added to `cmd/main_test.go`:

- **`TestBuildSourceDepsWiresRequireSHAPinned`** — calls `buildSourceDeps("", false)` and
  `buildSourceDeps("", true)` directly and asserts `RequireSHAPinnedRemoteBases` matches
  the input for each.
- **`TestBuildSourceDepsFlagVarBinding`** — simulates CLI parsing by mutating the
  package-level `requireSHAPinnedRemoteBases` var (the one bound to the flag) then calling
  `buildSourceDeps("", requireSHAPinnedRemoteBases)`, asserting the value propagates. This
  is the same sequence `main()` performs after `flag.Parse()`.

### RED-GREEN demonstration

Temporarily removed `RequireSHAPinnedRemoteBases: requireSHAPinned,` from `buildSourceDeps`:

```
=== RED (assignment removed) ===
--- FAIL: TestBuildSourceDepsWiresRequireSHAPinned
    main_test.go:42: buildSourceDeps(true): RequireSHAPinnedRemoteBases = false, want true
--- FAIL: TestBuildSourceDepsFlagVarBinding
    main_test.go:60: after setting requireSHAPinnedRemoteBases=true, buildSourceDeps returned
        Deps with RequireSHAPinnedRemoteBases=false; assignment in buildSourceDeps is missing

=== GREEN (assignment restored) ===
--- PASS: TestBuildSourceDepsWiresRequireSHAPinned
--- PASS: TestBuildSourceDepsFlagVarBinding
```

### Files changed

| File | Change |
|------|--------|
| `cmd/main.go` | Extracted `buildSourceDeps()` from inline literal in `main()`; `main()` now calls `buildSourceDeps(sourceScratchDir, requireSHAPinnedRemoteBases)` |
| `cmd/main_test.go` | Added `TestBuildSourceDepsWiresRequireSHAPinned` and `TestBuildSourceDepsFlagVarBinding` |
