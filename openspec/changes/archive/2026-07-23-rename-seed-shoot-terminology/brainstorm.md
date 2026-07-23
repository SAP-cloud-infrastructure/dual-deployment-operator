## Design Summary

Unify the codebase onto a single Gardener vocabulary — `host → seed`, `remote → shoot` — everywhere: Go identifiers, comments, tests, the `v1alpha1` CRD API surface, generated artifacts, `README.md`, all `docs/*`, and sample CRs. The codebase currently mixes two axes: `host`/`remote` in code + CRD API, and `seed`/`shoot` as the physical Gardener cluster names (README states "host = seed, remote = shoot"). `shoot` is already used consistently (~36 occurrences) and stays; `host` (~68) and `remote` (~50) are renamed. This is a pure terminology refactor with **no behavior change**, but it is a **breaking `v1alpha1` CRD API change** — acceptable because the operator is pre-release with no deployed consumers. The result is one clean vocabulary with the code matching the physical Gardener topology it deploys to.

## Alternatives Considered

### Option A: Full unify — `host → seed`, `remote → shoot` (chosen)
- **Approach**: Rename both naming axes onto Gardener terms across all layers — Go internals, tests, comments, the public CRD JSON fields + CEL enum, generated CRD/RBAC/deepcopy, README, `docs/*`, and samples. Leave upstream `rest.Config.Host` and historical proper nouns untouched.
- **Pros**: Single coherent vocabulary; code names match the physical clusters they target; eliminates the `HostClient returns a client for the seed` self-contradiction; symmetric (`seed`/`shoot` pairs cleanly like `host`/`remote` did).
- **Cons**: Breaking CRD API change; large mechanical diff touching 18 Go files + docs; requires regenerating manifests.
- **Why chosen**: Pre-release status makes the breaking change free, and this is the only option that actually satisfies "seed and shoot everywhere".

### Option B: Internal only — rename Go internals, keep CRD field names
- **Approach**: Rename all Go identifiers, comments, and tests to seed/shoot, but leave the user-facing CRD JSON fields (`hostValues`, `remotePath`, …) and the CEL enum unchanged.
- **Pros**: Non-breaking to the API; smaller, safer diff.
- **Cons**: Leaves `host`/`remote` in the public CRD spec — so it is NOT truly "seed and shoot everywhere". Creates a new inconsistency (Go says `seed`, YAML says `host`).
- **Why not chosen**: Directly contradicts the stated goal; trades one inconsistency for another.

### Option C: `host → seed` only, leave `remote` as-is
- **Approach**: Fix only the host/seed contradiction; keep `remote` for the shoot side.
- **Pros**: Smallest diff; non-breaking on the `remote*` fields.
- **Cons**: Asymmetric vocabulary — seed side named `seed`, shoot side named `remote`. Still not "seed and shoot everywhere".
- **Why not chosen**: Leaves the codebase half-converted; the `remote`/`shoot` split is exactly the kind of inconsistency this change exists to remove.

## Agreed Approach

Option A. Perform a complete, mechanical rename in one change: `host → seed` and `remote → shoot` across every layer. Because the rename reaches the public CRD surface, regenerate all derived artifacts (`make manifests generate`) and verify coherence through the existing test gate — the CEL-validation tests in [`internal/controller/cel_validation_test.go`](../../../internal/controller/cel_validation_test.go) and the CRD unit tests exercise the renamed enum + fields, so a green `make test` proves the API rename is internally consistent. Verification closes with a repo-wide grep asserting no stray `host`/`remote` terminology remains, excluding the documented exceptions.

## Key Decisions

- **Both axes collapse onto Gardener terms**: `host → seed`, `remote → shoot`. Rationale: the README already defines this equivalence ("host = seed, remote = shoot"); the code should name the physical clusters directly.
- **CRD API is renamed despite being breaking**: `hostValues→seedValues`, `remoteValues→shootValues`, `hostPath→seedPath`, `remotePath→shootPath`, `remoteNamespace→shootNamespace`, `remoteAccess→shootAccess` (`RemoteAccessRef→ShootAccessRef`), enum `HostFirst;RemoteFirst→SeedFirst;ShootFirst` (default `RemoteFirst→ShootFirst`), status `hostResources→seedResources` / `remoteResources→shootResources`. Rationale: pre-release `v1alpha1`, no deployed users, so a clean break beats carrying legacy field names forever.
- **`rest.Config.Host` is NOT renamed**: it is an upstream Kubernetes struct field in [`internal/clients/clients.go`](../../../internal/clients/clients.go), not our terminology. Renaming it would break compilation.
- **Historical proper nouns and external names in `docs/related-artifacts.md` are NOT renamed**: archived revision descriptions (`spec.host.chart` from r1, "host mode + remote mode" describing past designs), external chart/repo names (`metal-operator-remote`, `boot-operator-remote`, `argora-operator-remote`, etc. — real artifacts in `sapcc/helm-charts`), and quoted upstream issue titles ("simplify remote operator deployment"). Rationale: these describe history or name things owned elsewhere; renaming them falsifies the record or breaks references. Only *current-design* prose that describes this operator's seed/shoot roles is updated.
- **All `docs/*` stay aligned**: `docs/design.md` (211 hits), `docs/context.md` (42), `docs/implementation.md` (37), and current-design portions of `docs/related-artifacts.md` (17) are updated to the seed/shoot vocabulary alongside the code, plus `README.md`. Rationale: docs describing the operator's own architecture must not drift from the renamed code. (Explicit user directive: "keep doc/* aligned".)
- **Generated artifacts are regenerated, never hand-edited**: `config/crd/bases/*`, `config/rbac/role.yaml`, `api/v1alpha1/zz_generated.deepcopy.go` come from `make manifests generate`.
- **No behavior change**: component boundaries, reconcile flow, and delivery paths are identical — only names change.
- **Standalone not-started change**: scoped as its own OpenSpec change, not retrofitted into completed/ongoing Phases 0–6 (project scoping rule).

## Open Questions

- [ ] None blocking. Condition-reason and event-reason string renames (e.g. `HostRenderFailed→SeedRenderFailed`) will be enumerated exhaustively during the specs/plan phase from a full `Host*`/`Remote*` identifier grep — resolved during design/specs, not a design-level unknown.
