# Rename seed/shoot Terminology Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Trailing `- [ ]` checkboxes mark task-group completion — check them off AFTER all steps in the group complete.

**Goal:** Rename the operator's two-cluster vocabulary from `host`/`remote` to `seed`/`shoot` everywhere — Go identifiers, CRD API surface, generated artifacts, tests, and docs — with zero behavior change.

**Architecture:** A pure mechanical rename across three layers: the `v1alpha1` CRD types (public JSON fields + CEL enum), the internal packages (`clients`, `source`, `deliver`, `controller`, `cmd`), and their tests, followed by regeneration of derived artifacts (`make manifests generate`) and doc alignment. The existing unit/envtest suite is the regression guard: renaming a public field breaks compilation and the CEL-validation tests until the rename is complete and internally consistent, so a green `make test` after each layer proves coverage. One upstream field (`rest.Config.Host`) and all historical/external proper nouns in `docs/related-artifacts.md` are explicitly preserved.

**Tech Stack:** Go, Kubebuilder/controller-runtime, controller-tools (`make manifests generate`), Ginkgo/Gomega + envtest, golangci-lint.

**Rename map (authoritative):**
| host/remote | seed/shoot |
|---|---|
| `HostValues` / `hostValues` | `SeedValues` / `seedValues` |
| `RemoteValues` / `remoteValues` | `ShootValues` / `shootValues` |
| `HostPath` / `hostPath` | `SeedPath` / `seedPath` |
| `RemotePath` / `remotePath` | `ShootPath` / `shootPath` |
| `RemoteAccess` / `remoteAccess` | `ShootAccess` / `shootAccess` |
| `RemoteAccessRef` | `ShootAccessRef` |
| `RemoteNamespace` / `remoteNamespace` | `ShootNamespace` / `shootNamespace` |
| enum `HostFirst;RemoteFirst`, default `RemoteFirst` | `SeedFirst;ShootFirst`, default `ShootFirst` |
| `HostResources` / `hostResources` | `SeedResources` / `seedResources` |
| `RemoteResources` / `remoteResources` | `ShootResources` / `shootResources` |
| `ModeHost` (`"host"`) / `ModeRemote` (`"remote"`) | `ModeSeed` (`"seed"`) / `ModeShoot` (`"shoot"`) |
| `HostApplier` / `HostClient` | `SeedApplier` / `SeedClient` |
| reasons `HostRenderFailed` / `RemoteRenderFailed` / `HostTransformFailed` / `RemoteTransformFailed` / `RemoteApplyFailed` | `SeedRenderFailed` / `ShootRenderFailed` / `SeedTransformFailed` / `ShootTransformFailed` / `ShootApplyFailed` |
| `Cluster` label literals `"host"` / `"remote"` | `"seed"` / `"shoot"` |

**MUST NOT rename:** `rest.Config.Host` at [`internal/clients/clients.go:39`](../../../internal/clients/clients.go#L39) (`Host: server`) — upstream Kubernetes struct field. Existing reasons already using `Shoot` (`ShootClientFailed`, `WaitingForShootCredentials`, `ShootUnreachable`) stay unchanged. Historical revision descriptions, external chart/repo names (`metal-operator-remote`, `boot-operator-remote`, `argora-operator-remote`, `ipam-capi-remote`), and quoted upstream issue titles in `docs/related-artifacts.md` stay unchanged.

**File structure (what each task touches):**
- `api/v1alpha1/dualdeploymentoperator_types.go` — CRD field names, JSON tags, CEL enum markers, doc comments.
- `api/v1alpha1/zz_generated.deepcopy.go` + `config/crd/bases/*.yaml` + `config/rbac/role.yaml` — regenerated, never hand-edited.
- `internal/source/source.go`, `helm.go`, `kustomize.go` — `Mode` constants + field reads.
- `internal/clients/clients.go` — `SeedClient` factory (preserve `Host: server`).
- `internal/deliver/applier.go` — `Cluster` label comment.
- `internal/controller/dualdeploymentoperator_controller.go` — appliers, reasons, applyOrder logic, status field writes.
- `cmd/main.go` — `SeedApplier` construction + `"seed"` label.
- All `*_test.go` under `api/`, `internal/` — identifiers, literals, test descriptions.
- `README.md`, `docs/design.md`, `docs/context.md`, `docs/implementation.md`, current-design prose in `docs/related-artifacts.md`.

---

## Task 1: Rename CRD API types

**Files:**
- Modify: `api/v1alpha1/dualdeploymentoperator_types.go`

- [ ] **Step 1: Rename spec/status fields, JSON tags, and CEL markers**

In `api/v1alpha1/dualdeploymentoperator_types.go` apply the rename map to every identifier and JSON tag:
- `HelmSource`: `HostValues` → `SeedValues` (tag `hostValues` → `seedValues`), `RemoteValues` → `ShootValues` (tag `remoteValues` → `shootValues`).
- `KustomizeSource`: `HostPath` → `SeedPath` (tag `hostPath` → `seedPath`), `RemotePath` → `ShootPath` (tag `remotePath` → `shootPath`).
- `DualDeploymentOperatorSpec`: `RemoteAccess RemoteAccessRef` → `ShootAccess ShootAccessRef` (tag `remoteAccess` → `shootAccess`); `RemoteNamespace` → `ShootNamespace` (tag `remoteNamespace` → `shootNamespace`).
- Rename the type `RemoteAccessRef` → `ShootAccessRef` (struct + doc comment).
- `ApplyOrder` markers: `+kubebuilder:validation:Enum=HostFirst;RemoteFirst` → `+kubebuilder:validation:Enum=SeedFirst;ShootFirst`; `+kubebuilder:default=RemoteFirst` → `+kubebuilder:default=ShootFirst`.
- Status: `HostResources` → `SeedResources` (tag `hostResources` → `seedResources`), `RemoteResources` → `ShootResources` (tag `remoteResources` → `shootResources`).
- Update every doc comment referencing "host render"/"remote (shoot) render"/"host target namespace" to seed/shoot wording.

- [ ] **Step 2: Verify the types package compiles**

Run: `go build ./api/v1alpha1/...`
Expected: FAIL — `zz_generated.deepcopy.go` still references old names (`RemoteAccessRef`, `HostValues`, etc.). This confirms the deepcopy regen in Task 2 is required.

- [ ] **Step 3: Commit**

```bash
git add api/v1alpha1/dualdeploymentoperator_types.go
git commit -m "refactor(api): rename host/remote to seed/shoot in v1alpha1 types"
```

- [x] Task 1 complete

---

## Task 2: Regenerate CRD, RBAC, and deepcopy

**Files:**
- Regenerate (do NOT hand-edit): `api/v1alpha1/zz_generated.deepcopy.go`, `config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml`, `config/rbac/role.yaml`, `config/webhook/manifests.yaml`

- [ ] **Step 1: Run codegen**

Run: `make manifests generate`
Expected: PASS (exit 0). Regenerates `zz_generated.deepcopy.go` with `SeedValues`/`ShootValues`/`ShootAccessRef`/`SeedResources`/`ShootResources` methods, and rewrites the CRD YAML with `seedValues`/`shootValues`/`seedPath`/`shootPath`/`shootNamespace` fields and the `SeedFirst;ShootFirst` enum.

- [ ] **Step 2: Verify generated CRD reflects the rename**

Run: `rg -c 'seedValues|shootValues|seedPath|shootPath|shootNamespace|SeedFirst|ShootFirst' config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml`
Expected: non-zero count.

Run: `rg -c 'hostValues|remotePath|remoteNamespace|HostFirst|RemoteFirst' config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml`
Expected: `0`.

- [ ] **Step 3: Verify the api package now compiles**

Run: `go build ./api/v1alpha1/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add api/v1alpha1/zz_generated.deepcopy.go config/crd/bases config/rbac config/webhook
git commit -m "chore(api): regenerate CRD/RBAC/deepcopy for seed/shoot rename"
```

- [x] Task 2 complete

---

## Task 3: Rename source-rendering internals

**Files:**
- Modify: `internal/source/source.go` (Mode constants), `internal/source/helm.go` (`HostValues`/`RemoteValues` reads), `internal/source/kustomize.go` (`HostPath`/`RemotePath` reads)

- [ ] **Step 1: Rename Mode constants**

In `internal/source/source.go`: `ModeHost Mode = "host"` → `ModeSeed Mode = "seed"`; `ModeRemote Mode = "remote"` → `ModeShoot Mode = "shoot"`. Update any comments.

- [ ] **Step 2: Update field reads and Mode usages in renderers**

In `internal/source/helm.go`: `spec.HostValues` → `spec.SeedValues`, `spec.RemoteValues` → `spec.ShootValues`; `ModeHost`/`ModeRemote` → `ModeSeed`/`ModeShoot`; injected `{mode: "host"|"remote"}` values follow the constant values `"seed"`/`"shoot"`.
In `internal/source/kustomize.go`: `spec.HostPath` → `spec.SeedPath`, `spec.RemotePath` → `spec.ShootPath`; `ModeHost`/`ModeRemote` → `ModeSeed`/`ModeShoot`.

- [ ] **Step 3: Verify the source package compiles**

Run: `go build ./internal/source/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/source
git commit -m "refactor(source): rename ModeHost/ModeRemote to ModeSeed/ModeShoot and field reads"
```

- [x] Task 3 complete

---

## Task 4: Rename clients and deliver internals

**Files:**
- Modify: `internal/clients/clients.go` (`HostClient` → `SeedClient`; PRESERVE `Host: server`), `internal/deliver/applier.go` (`Cluster` label comment)

- [ ] **Step 1: Rename the seed client factory**

In `internal/clients/clients.go`: rename func `HostClient` → `SeedClient`; update the package doc comment `builds host (in-cluster) and shoot` → `builds seed (in-cluster) and shoot` and `HostClient returns a client for the seed` → `SeedClient returns a client for the seed`. **Do NOT change line 39 `Host: server`** — that is the upstream `rest.Config.Host` field. Update `spec.RemoteAccess*` reads to `spec.ShootAccess*` if present in this file.

- [ ] **Step 2: Update deliver Cluster-label comment**

In `internal/deliver/applier.go`: comment `Cluster string // "host" or "remote", for logging` → `Cluster string // "seed" or "shoot", for logging`.

- [ ] **Step 3: Verify both packages compile**

Run: `go build ./internal/clients/... ./internal/deliver/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/clients internal/deliver
git commit -m "refactor(clients,deliver): rename HostClient to SeedClient; seed/shoot labels"
```

- [x] Task 4 complete

---

## Task 5: Rename controller and cmd internals

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller.go`, `cmd/main.go`

- [ ] **Step 1: Rename controller identifiers, reasons, and applyOrder logic**

In `internal/controller/dualdeploymentoperator_controller.go`:
- Struct/local: `HostApplier` → `SeedApplier`, `RemoteResources`/`HostResources` status writes → `ShootResources`/`SeedResources`, `cr.Spec.RemoteAccess` → `cr.Spec.ShootAccess`, `cr.Spec.RemoteNamespace` → `cr.Spec.ShootNamespace`.
- Reason literals: `"HostRenderFailed"` → `"SeedRenderFailed"`, `"RemoteRenderFailed"` → `"ShootRenderFailed"`, `"HostTransformFailed"` → `"SeedTransformFailed"`, `"RemoteTransformFailed"` → `"ShootTransformFailed"`, `"RemoteApplyFailed"` → `"ShootApplyFailed"` (both occurrences, lines ~191 and ~207). Leave `ShootClientFailed`, `WaitingForShootCredentials`, `ShootUnreachable` unchanged.
- applyOrder: `remoteFirst := cr.Spec.ApplyOrder != "HostFirst"` → `shootFirst := cr.Spec.ApplyOrder != "SeedFirst"` (rename the local var everywhere it is used); `cr.Spec.ApplyOrder == "HostFirst"` → `== "SeedFirst"`; rename local `applyHost` → `applySeed` and update its call sites and the `// HostFirst:` / `// remote` comments to seed/shoot wording.
- `Cluster: "remote"` (line ~341) → `Cluster: "shoot"`; any `ModeHost`/`ModeRemote` → `ModeSeed`/`ModeShoot`.

- [ ] **Step 2: Rename cmd applier construction**

In `cmd/main.go`: `HostApplier` → `SeedApplier`; `Cluster: "host"` (line ~193) → `Cluster: "seed"`.

- [ ] **Step 3: Verify the whole module compiles**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/dualdeploymentoperator_controller.go cmd/main.go
git commit -m "refactor(controller,cmd): rename host/remote to seed/shoot (identifiers, reasons, applyOrder)"
```

- [x] Task 5 complete

---

## Task 6: Rename tests and make the suite green

**Files:**
- Modify: `api/v1alpha1/dualdeploymentoperator_types_test.go`, `internal/controller/dualdeploymentoperator_controller_test.go`, `internal/controller/cel_validation_test.go`, `internal/controller/status_helpers_test.go`, `internal/controller/fakes_envtest_test.go`, `internal/clients/clients_test.go`, `internal/deliver/applier_test.go`, `internal/source/helm_test.go`, `internal/source/kustomize_test.go`, `internal/source/source_test.go`, `internal/webhook/v1alpha1/webhook_suite_test.go`

- [ ] **Step 1: Run the suite to confirm it fails against renamed production code**

Run: `make test`
Expected: FAIL (compile errors) — tests still reference `HostValues`, `RemoteAccess`, `ModeHost`, `"HostFirst"`, `hostResources`, etc.

- [ ] **Step 2: Apply the rename map to all test files**

Apply the same rename map to every test file: Go identifiers (`HostValues`→`SeedValues`, `RemoteAccessRef`→`ShootAccessRef`, `ModeHost`→`ModeSeed`, `HostApplier`→`SeedApplier`, status `HostResources`→`SeedResources`, etc.), JSON/YAML literals in test fixtures (`hostValues`→`seedValues`, `remotePath`→`shootPath`, `remoteNamespace`→`shootNamespace`, `remoteAccess`→`shootAccess`), enum literals (`"HostFirst"`→`"SeedFirst"`, `"RemoteFirst"`→`"ShootFirst"`), Cluster labels (`"host"`→`"seed"`, `"remote"`→`"shoot"`), reason strings, and Ginkgo `Describe`/`It`/`When` descriptions containing "host"/"remote". In `cel_validation_test.go`, any case asserting `"ShootFirst"` is *rejected* must be re-pointed to a genuinely invalid value (e.g. `"HostFirst"` or `"Bogus"`) since `ShootFirst` is now valid; any case asserting `"HostFirst"` is *accepted* becomes `"SeedFirst"`. Preserve external chart-name URLs containing `ipam-capi-remote` verbatim.

- [ ] **Step 3: Run the suite to confirm green**

Run: `make test`
Expected: PASS (or only pre-existing unrelated failures; none should reference host/remote).

- [ ] **Step 4: Commit**

```bash
git add api/v1alpha1/dualdeploymentoperator_types_test.go internal/controller internal/clients internal/deliver internal/source internal/webhook
git commit -m "test: rename host/remote to seed/shoot across the suite"
```

- [x] Task 6 complete

---

## Task 7: Align docs and sample

**Files:**
- Modify: `README.md`, `docs/design.md`, `docs/context.md`, `docs/implementation.md`, `docs/related-artifacts.md` (current-design prose only), `config/samples/dual-deployment-operator_v1alpha1_dualdeploymentoperator.yaml`

- [ ] **Step 1: Update README and design/context/implementation docs**

In `README.md`: rewrite the "host = seed, remote = shoot" mapping and all subsequent host/remote prose (purpose, design summary, topology) to the single seed/shoot vocabulary; the diagram captions ("host + remote", "host mode + remote mode") become "seed + shoot", "seed mode + shoot mode".
In `docs/design.md`, `docs/context.md`, `docs/implementation.md`: rename all host/remote references describing *this operator's* seed/shoot roles, including CRD field names (`hostValues`→`seedValues`, etc.), enum values, and prose. Keep code-fence field names consistent with the renamed CRD.

- [ ] **Step 2: Update related-artifacts, preserving history and external names**

In `docs/related-artifacts.md`: update ONLY current-design prose. Do NOT rename: archived revision descriptions (`spec.host.chart` in r1, "host mode + remote mode" describing past r4/r5), external chart/repo names (`metal-operator-remote`, `boot-operator-remote`, `argora-operator-remote`, `ipam-capi-remote`), and quoted upstream issue titles ("simplify remote operator deployment"). When in doubt whether a token is history/external, leave it.

- [ ] **Step 3: Verify sample CR still applies**

The sample at `config/samples/dual-deployment-operator_v1alpha1_dualdeploymentoperator.yaml` has only a `# TODO(user)` spec stub (no host/remote fields), so no field rename is needed. Confirm no stray terminology:
Run: `rg -i 'host|remote' config/samples/`
Expected: no matches (other than none). If a field example is later added, it must use seed/shoot names.

- [ ] **Step 4: Commit**

```bash
git add README.md docs config/samples
git commit -m "docs: align README/docs/sample with seed/shoot terminology"
```

- [x] Task 7 complete

---

## Task 8: Final verification gate

**Files:** none (verification only)

- [ ] **Step 1: Lint**

Run: `make lint`
Expected: PASS (exit 0).

- [ ] **Step 2: Full test suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 3: Confirm no generated-artifact drift**

Run: `make manifests generate && git status --porcelain`
Expected: empty output (no uncommitted regenerated changes).

- [ ] **Step 4: Terminology grep — no residual host/remote outside the exclusion list**

Run:
```bash
rg -i '\b(host|remote)\b' --type go -g '!*_test.go' | rg -v 'Host: server'
```
Expected: no matches (the only allowed `Host` is `rest.Config.Host` at `internal/clients/clients.go`).

Run:
```bash
rg -i '\b(host|remote)\b' --type go -g '*_test.go'
```
Expected: no matches (test fixtures fully renamed).

Run:
```bash
rg -i 'hostValues|remoteValues|hostPath|remotePath|remoteAccess|remoteNamespace|hostResources|remoteResources|HostFirst|RemoteFirst' config/ README.md docs/design.md docs/context.md docs/implementation.md
```
Expected: no matches. (`docs/related-artifacts.md` is excluded here because it legitimately retains historical/external `*-remote` names.)

- [ ] **Step 5: Commit any final fixups**

```bash
git add -A
git commit -m "chore: final seed/shoot terminology cleanup" || echo "nothing to commit"
```

- [x] Task 8 complete
