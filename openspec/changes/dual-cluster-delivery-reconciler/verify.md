<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Verification Report: dual-cluster-delivery-reconciler

Schema: `sdd-plus-superpowers`. Verified against `1be3b11..HEAD` (24 commits) in worktree
`feat/dual-cluster-delivery-reconciler-impl`. Full suite green, lint 0 issues, `go vet` clean,
`go build ./...` exit 0. Two holistic code reviews (final review #1 → 7 P1 findings, all fixed;
re-review #2 → APPROVED).

## Summary

| Dimension    | Status |
|--------------|--------|
| Completeness | 11/11 plan task groups complete; 5 capabilities, all with implementing source |
| Correctness  | 32 spec requirements mapped to implementation + tests; key scenarios spot-checked |
| Coherence    | Follows design.md decisions (owned-by hash, stateless applier, single-install guard, RemoteFirst gating, caBundle leaf-only strip) |

**Final assessment: All checks passed. Ready for archive** (one design-deferred out-of-scope follow-up noted below).

## Completeness

- **Tasks**: `plan.md` shows 11/11 trailing checkboxes `[x]`, 0 incomplete.
- **Capabilities → source**:
  - `resource-delivery` → `internal/deliver/` (order.go, health.go, cabundle.go, applier.go)
  - `cluster-clients` → `internal/clients/clients.go` + reconciler `buildShootApplier`
  - `reconcile-loop` → `internal/controller/dualdeploymentoperator_controller.go`
  - `crd-types` → `api/v1alpha1/dualdeploymentoperator_types.go` (renames + `applyOrder`)
  - `noop-reconciler` (REMOVED) → superseded; the log-and-return stub is gone (0 occurrences of the old `"reconciling"` info line)

## Correctness (spec behavior → code, spot-checked)

- caBundle leaf-only unconditional strip — `internal/deliver/cabundle.go`
- SSA with `ForceOwnership` — `internal/deliver/applier.go:70`
- Cluster-scoped conflict guard (owned-by, fail-safe on non-NotFound GET) — `internal/deliver/applier.go` `foreignOwner`
- GET-after-apply health; existence-only for webhooks; nil→Unknown — `internal/deliver/health.go`
- Three-state shoot credentials gate (ready/credsNotReady/clientFailed) — `internal/clients/clients.go` + controller `buildShootApplier`
- RemoteFirst gates host on ANY remote failure (partial or complete) — `dualdeploymentoperator_controller.go` `anyFailed(remoteStatuses)`
- Status-diff prune, version-independent identity key, owned-by guard, retentionPolicy Retain — `internal/deliver/order.go` + controller `prune`
- Failed prune keeps orphan tracked (retained merged back into status) — controller `prune` + reconcile status write
- Continue-on-error aggregation → prompt requeue — controller `applyAll` returns aggregated error
- `Ready=False/Progressing` when any Progressing/Unknown — controller `computeConditions`/`anyProgressing`
- Finalizer-driven deletion, reverse-`applyOrder`, both renders deleted explicitly, ShootUnreachable never orphans — controller `reconcileDelete`
- crd-types renames (`RemoteAccessRef`, `RetentionPolicy`, `ApplyOrder`) — `api/v1alpha1`

## Coherence

Implementation follows the design.md decisions:
- Applier is stateless (`ownedBy` is a method argument, not struct state); one instance reused across reconciles.
- Ownership label value is a fixed-length `sha256("<ns>/<name>")[:16]` hash (≤63 char label limit, injective).
- Single-install-per-seed defended by the cluster-scoped conflict guard.
- Package layout matches documented Phase 5/6 structure (`internal/deliver`, `internal/clients`, thin reconciler).

## Test coverage

- Task 9 prune owned-by-skip: genuinely exercised — a foreign-owned orphan is seeded into `status.HostResources` so prune considers it and the guard skips the delete.
- Task 10 reachable-delete: deterministic via injected `shootApplierFor` fake; asserts BOTH non-CRD delete AND CRD retention (no longer self-skips under envtest client-cert auth).

## Warnings / accepted out-of-scope follow-ups

- **SUGGESTION (design-deferred, not blocking):** Host apply RBAC. The generated `config/rbac/role.yaml`
  grants the CR/status/finalizers, Secret read, and Event create/patch, but not broad host-render apply
  verbs. This is intentional per design.md §3.6.7 + Non-Goals: host apply RBAC is a **broad, chart-provisioned
  grant delivered by the operator's own deployment chart (Phase 8)**, which is out of scope for this change
  (Phases 5–6). Tracked for the Phase 8 deployment-chart work. Not a blocker for archiving Phases 5–6.
- **SUGGESTION (out-of-scope):** Production `source.Deps` (real OCI/HTTP `ChartLoader` / `RootResolver`)
  are not yet wired in `cmd/main.go` (only test fakes exist). `source.From` now fails cleanly with a
  controlled error (surfaced as an `InvalidSource`-style condition) rather than panicking. Wiring production
  loaders is a follow-up before live source rendering.
