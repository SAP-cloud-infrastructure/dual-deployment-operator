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
