## Context

The `dual-deployment-operator` deploys split controllers across a Gardener seed (in-cluster) and shoot (via token-requestor kubeconfig). The codebase carries **two overlapping naming axes** for the same two clusters:

- `host` / `remote` — used in Go code, the `v1alpha1` CRD API surface, tests, and most docs.
- `seed` / `shoot` — the physical Gardener cluster names, used in a handful of comments and the README's own mapping ("host = seed, remote = shoot").

`shoot` is already used consistently (~36 occurrences) and is correct. `host` (~68) and `remote` (~50) are the inconsistency: e.g. [`clients.go`](../../../internal/clients/clients.go) literally documents `HostClient returns a client for the seed`. This split forces every reader to hold a translation table and invites drift between code and topology.

**Constraints:** The operator is pre-release (`v1alpha1`, no deployed consumers), so a breaking CRD field rename is acceptable. Generated artifacts (CRD YAML, RBAC, deepcopy) must be regenerated via `make manifests generate`, never hand-edited. Upstream Kubernetes field names (`rest.Config.Host`) and historical/external proper nouns in docs must not be renamed.

**Stakeholders:** operator developers (all future code reads one vocabulary); any CR authors during pre-release (must adopt the new field names).

## Goals / Non-Goals

**Goals:**
- Every reference to the seed side is named `seed`; every reference to the shoot side is named `shoot` — across Go identifiers, comments, tests, the CRD API, generated artifacts, `README.md`, and all `docs/*`.
- Rename the public CRD surface: `hostValues→seedValues`, `remoteValues→shootValues`, `hostPath→seedPath`, `remotePath→shootPath`, `remoteNamespace→shootNamespace`, `remoteAccess→shootAccess` (`RemoteAccessRef→ShootAccessRef`), enum `HostFirst;RemoteFirst→SeedFirst;ShootFirst` (default `RemoteFirst→ShootFirst`), status `hostResources→seedResources` / `remoteResources→shootResources`.
- Zero behavior change: reconcile flow, delivery paths, and component boundaries are byte-for-byte equivalent in logic.
- Verification proves no stray `host`/`remote` terminology remains (excluding documented exceptions), and `make manifests generate` + `make lint` + `make test` all pass.

**Non-Goals:**
- No behavioral, structural, or API-shape changes beyond names (no new fields, no field-type changes, no reorganized packages).
- Not renaming `rest.Config.Host` (upstream K8s struct field).
- Not renaming historical revision descriptions, external chart/repo names, or quoted upstream issue titles in `docs/related-artifacts.md`.
- No CRD conversion webhook or version bump for backward compatibility (pre-release; clean break instead).
- Not touching completed/ongoing Phases 0–6 as phases — this is a standalone change.

## Capabilities

Modified capabilities (this change alters existing behavior/surface; each becomes a spec under `specs/<name>/spec.md`):

- **seed-shoot-terminology** — the single normative capability: the operator's CRD API, Go implementation, tests, generated artifacts, and documentation use the `seed`/`shoot` vocabulary exclusively for the two target clusters, with the enumerated exceptions preserved.

The change is one coherent rename with a single acceptance surface, so it is modeled as one capability rather than fragmented per-file. The spec will enumerate the concrete rename map and the exclusion list as testable requirements + scenarios.

## Decisions

**Decision: Collapse both axes onto Gardener terms**
- Chosen: `host → seed`, `remote → shoot` everywhere.
- Reason: The README already defines this equivalence; naming the physical clusters directly removes the mental translation layer and the `HostClient`-returns-seed contradiction.
- Alternatives considered: (A) internal-only rename keeping CRD field names — rejected, leaves `host`/`remote` in the public spec, so not "everywhere" and creates a Go-vs-YAML mismatch. (B) `host→seed` only, keep `remote` — rejected, asymmetric vocabulary (`seed` vs `remote`).

**Decision: Rename the breaking CRD surface rather than preserve it**
- Chosen: Rename public JSON fields + CEL enum; regenerate CRD.
- Reason: `v1alpha1`, pre-release, no deployed users — a clean break is free and avoids carrying legacy field aliases forever.
- Alternatives considered: dual-serving old+new field names via a conversion webhook — rejected as unnecessary complexity for a zero-user API.

**Decision: Preserve documented exceptions**
- Chosen: Do not rename `rest.Config.Host`, nor historical/external proper nouns in `docs/related-artifacts.md` (e.g. `spec.host.chart` from archived r1, "host mode + remote mode" describing past designs, `metal-operator-remote`/`boot-operator-remote`/`argora-operator-remote` chart names, quoted issue titles).
- Reason: `rest.Config.Host` is upstream API (renaming breaks compilation); the doc proper nouns name history or externally-owned artifacts, so renaming them falsifies the record or breaks references.
- Alternatives considered: blanket find-replace — rejected, would corrupt history and break external references.

**Decision: Verify via test gate + terminology grep**
- Chosen: `make manifests generate` (no diff surprises), `make lint`, `make test`, plus a repo-wide grep asserting no residual `host`/`remote` terminology outside the exclusion list.
- Reason: The CEL-validation and CRD unit tests exercise the renamed enum + fields, so a green suite proves the API rename is internally coherent; the grep proves completeness.
- Alternatives considered: manual spot-check — rejected, not exhaustive for a 100+-occurrence rename.

## Risks / Trade-offs

- [Breaking CRD change invalidates any existing CRs] → Acceptable: pre-release, no deployed consumers; sample CRs are updated in the same change.
- [Missed occurrence leaves the vocabulary half-converted] → Mitigation: exhaustive `Host*`/`host`/`Remote*`/`remote` grep enumerated during specs/plan; verify step re-greps and fails on any residual outside the exclusion list.
- [Over-renaming an excluded proper noun / upstream field falsifies docs or breaks build] → Mitigation: explicit exclusion list in the spec; `rest.Config.Host` and `docs/related-artifacts.md` historical/external names are called out as MUST-NOT-rename, verified by build + review.
- [Generated artifacts drift from source markers] → Mitigation: regenerate with `make manifests generate`; CI/build gate catches uncommitted generated diffs.
- [Condition/event reason strings are consumed by external tooling] → Low risk pre-release; reasons like `HostRenderFailed→SeedRenderFailed` are internal status strings with no external contract yet.

## Migration Plan

Deployment steps:
1. Rename Go identifiers, comments, and test names (`host→seed`, `remote→shoot`) across the 18 affected files, excluding `rest.Config.Host`.
2. Rename the CRD API markers/fields/enum in [`dualdeploymentoperator_types.go`](../../../api/v1alpha1/dualdeploymentoperator_types.go).
3. Run `make manifests generate` to regenerate `config/crd/bases/*`, `config/rbac/role.yaml`, and `zz_generated.deepcopy.go`.
4. Update `README.md`, `docs/design.md`, `docs/context.md`, `docs/implementation.md`, current-design prose in `docs/related-artifacts.md`, and `config/samples/*`.
5. Run `make lint` and `make test`; run the terminology grep.

Rollback:
- Pure rename on an isolated feature branch; rollback = discard the branch. No data or state migration involved.

## Open Questions

- [ ] None blocking. The exhaustive list of `Host*`/`Remote*` identifier and reason-string renames is derived mechanically during the specs/plan phase from a full grep — a resolvable enumeration task, not a design unknown.
