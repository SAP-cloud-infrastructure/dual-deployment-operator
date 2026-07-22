<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Brainstorm: dual-cluster-delivery-reconciler

## Design Summary

This change implements Phases 5 (Delivery) and 6 (Reconciler) of `dual-deployment-operator`: the dual-cluster direct-apply mechanism plus the reconcile loop that ties rendering, transformation, and delivery together. Today the reconciler is a no-op stub; the source renderers (Helm/kustomize) and typed transformations (`patch`, `rewriteWebhookURL`, `filterKinds`) are complete. This change adds `internal/deliver` (a stateless server-side-apply applier with per-resource health) and `internal/clients` (host in-cluster + shoot token+CA-from-Secret factories), and rewires the controller to render twice, transform each render, apply each render to its target cluster, prune orphans, and report status. The design is heavily pre-constrained by `docs/design.md` (r7) and `docs/implementation.md`; this brainstorm validates that documented approach for the delivery+reconciler scope and resolves the open questions the docs left unsettled (prune, apply sequencing, partial-failure semantics, health read strategy, helper placement, and the caBundle-strip downtime concern).

## Alternatives Considered

### Option A: Applier-per-cluster + thin reconciler orchestration
- **Approach**: `internal/deliver` owns a stateless `SSAApplier` (one instance per cluster: host preconstructed at startup, shoot built per-CR from the token-requestor Secret's token+CA plus `spec.remoteAccess.server`) plus pure functions for ordering, health, and caBundle stripping. `internal/clients` owns only the two client factories. The reconciler orchestrates: render×2 → transform → sort → apply-all (per `applyOrder`) → prune → status.
- **Pros**: Clean unit boundaries — the applier is testable against a fake client with zero reconciler; ordering/health/strip are pure functions testable in isolation. Matches the documented Phase 5/6 structure exactly, so no design drift. Files stay small and focused.
- **Cons**: The reconciler holds the orchestration sequence, so it is the largest single file — inherent to reconciler responsibility.
- **Why not chosen**: It IS chosen. Best isolation, direct testability, aligns with the documented interfaces.

### Option B: Fat "Deliverer" facade
- **Approach**: A single `deliver.Deliverer` object swallows both clusters, ordering, apply, prune, and health; the reconciler calls `Deliverer.Sync(ctx, cr, hostManifests, remoteManifests)`.
- **Pros**: Reconciler shrinks to a few lines; one seam to mock.
- **Cons**: The facade becomes a god-object mixing two clusters + prune state + status shaping. The sub-behaviors cannot be unit-tested independently; the CR-shaped signature couples `deliver` to the API type it should not need to know about.
- **Why not chosen**: Trades away the isolation that makes each behavior independently verifiable — the exact thing the brainstorming discipline warns against.

### Option C: Reconciler-embedded delivery (no `deliver` package)
- **Approach**: Fold apply/health/prune helpers directly into the controller package; skip `internal/deliver` entirely.
- **Pros**: Fewest files.
- **Cons**: The controller package balloons; apply/health logic cannot be tested without the reconciler harness; contradicts the documented `internal/deliver` layout.
- **Why not chosen**: Poor testability and diverges from the established project structure.

## Agreed Approach

**Option A** — applier-per-cluster with a thin reconciler. The reconcile loop:

1. Get CR; handle deletion via finalizer; ensure finalizer on first reconcile.
2. Build source renderer from `spec.source`; render twice (`ModeHost` with `cr.Namespace`, `ModeRemote` with `spec.remoteNamespace`).
3. Build the ordered transformation list; apply each transform to both renders independently, in declaration order.
4. Build the shoot applier from the token-requestor Secret's token+CA plus `spec.remoteAccess.server` (host applier is preconstructed at startup).
5. Sort each render by a **fixed built-in intra-render kind-priority** (Namespace → CRD → RBAC → workloads → webhooks).
6. Apply the two renders in the order set by `spec.applyOrder` (enum `HostFirst`/`RemoteFirst`, default `RemoteFirst`). Both renders are always applied — cross-render sequencing is a best-effort preference, not a hard gate.
7. Prune orphans: diff the previous applied-set (from `status.HostResources`/`status.RemoteResources`) against the new render; delete resources present before but absent now, in reverse-delete order, respecting `retentionPolicy.crds=Retain`.
8. Update status (per-resource `ResourceStatus`, aggregate `Ready` condition, `LastReconcile`); requeue after 10m for periodic drift correction.

Delivery uses server-side apply (`FieldOwner=dual-deployment-operator`, `ForceOwnership`). For `Validating/MutatingWebhookConfiguration` and conversion-webhook CRDs, the operator strips **only the `caBundle` leaf** before every apply, unconditionally, so it never becomes the SSA field manager of `caBundle` — the webhook-injector owns that field exclusively (r7 disjoint-field coexistence). Health is computed from a GET-after-apply read. Per-resource apply failures are aggregated (continue-on-error); render/transform/client-build failures are fatal to the reconcile.

## Key Decisions

- **Prune via applied-set tracking**: reuse `status.HostResources`/`status.RemoteResources` as the previous applied-set; diff against the new render by `APIVersion/Kind/Namespace/Name`; delete orphans. Rationale: matches GRM/Flux drift behavior, needs no new status shape, closes the orphan gap left by apply-only reconciliation.
- **Consumer-specified cross-render sequencing (`spec.applyOrder`)**: enum `HostFirst`/`RemoteFirst`, default `RemoteFirst`; deletion and prune run the reverse. Rationale: CRDs/RBAC/webhooks live on the remote and must exist before the host controller starts; on teardown the controller should stop before its CRDs are removed. Intra-render kind ordering stays a fixed built-in — only the cluster sequence is consumer-configurable.
- **Cross-render gating depends on ordering**: under `HostFirst` the remote render always follows regardless of host per-resource failures (best-effort — clusters decoupled). Under `RemoteFirst` the host render is gated on the remote render fully converging — *any* remote failure (partial or complete) or credentials-not-ready stops/defers host. Rationale: the shoot is a workless cluster whose remote render is entirely structural deps (CRDs/RBAC/webhooks) the host consumes, so starting host against a failed remote resource would crash-loop it.
- **Continue-on-error, aggregate**: attempt every resource in a batch; collect per-resource errors into `ResourceStatus.Health=Degraded`+`Message`; reconcile returns a `kerrors.NewAggregate` so it requeues. Rationale: one bad resource must not block the rest; matches the per-resource `ResourceStatus` already in the CRD.
- **GET-after-apply health**: after SSA apply, one GET reads observed status (availableReplicas, CRD `Established`); GET failure → `Unknown` (non-fatal). WebhookConfigurations are not special-cased — the operator does not inspect `caBundle` (it does not own that field; populating it is the injector's job), so they report `Healthy` on existence. Rationale: the apply response reflects desired spec, not observed status.
- **Helpers in `internal/manifest`**: add `StripInternalAnnotations()` and `ResourceStatus.AsManifest()` alongside the existing `manifest.Manifest`. Rationale: keeps origin/annotation-key knowledge in one package.
- **caBundle strip must be leaf-only and unconditional**: strip only `clientConfig.caBundle` (never the parent `clientConfig` map or the webhook entry), on every apply including the first. Rationale: prevents the operator from ever owning `caBundle` in SSA `managedFields`. If it owned it once and later omitted it, `ForceOwnership` would delete the injector's value, causing a webhook-outage window until the injector re-patches. A discriminating test asserts the operator's `managedFields` never lists a `caBundle` path.
- **Idempotent Delete**: `client.IgnoreNotFound` on delete — already-gone is success, needed for prune retries and partial-shoot-teardown during CR deletion.
- **Finalizer-deletion safety**: do not remove the finalizer until remote deletion is confirmed complete. On a *reachable* shoot with failing deletes, keep it and requeue (else orphans leak). On an *unreachable* shoot, do NOT assume "shoot gone" and do NOT remove the finalizer — "unreachable" is indistinguishable from "transiently down"; surface a `ShootUnreachable` condition + Event and requeue, leaving the CR in `Terminating` until the shoot returns or an operator manually removes the finalizer.

## Open Questions

- [x] RESOLVED: `docs/design.md` + `docs/context.md` inverted the metal-operator ↔ ipam-capi webhook/CRD characteristics. Verified against `sapcc/helm-charts` rendered YAML (commit `1b1e010`): metal-operator = 1 VWC, 0 MWC, 0 conversion CRDs (of 17); ipam-capi = 1 VWC, 1 MWC, 4 conversion CRDs (of 7). The docs attributed the conversion-webhook caBundle migration blocker (and a fabricated MWC) to metal-operator; corrected to ipam-capi. Fixed in both docs in this change.
