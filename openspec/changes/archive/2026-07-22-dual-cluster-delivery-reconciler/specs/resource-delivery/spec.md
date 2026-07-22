<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## ADDED Requirements

### Requirement: Applier interface

The `internal/deliver` package SHALL define an `Applier` interface that abstracts writing a single manifest to a target Kubernetes cluster and deleting it. The interface MUST expose an `Apply` operation that takes the manifest plus the owning CR's ownership value (see the CR-identity ownership label requirement) and returns the resource's observed `ResourceStatus`, and a `Delete` operation that is idempotent. The ownership value is passed as an argument to `Apply`, not held on the applier, so the applier remains stateless and one instance is safely reused across concurrent reconciles of different CRs.

#### Scenario: Apply returns per-resource status

- **WHEN** `Apply(ctx, m, ownedBy)` is called with a manifest `m` and an ownership value
- **THEN** it returns a `v1alpha1.ResourceStatus` carrying the resource's `Kind`, `APIVersion`, `Namespace`, `Name`, computed `Health`, and `LastApplied` timestamp
- **AND** a non-nil error only when the apply itself failed

#### Scenario: Interface is implemented by a stateless SSA applier

- **WHEN** the delivery layer is constructed
- **THEN** the concrete implementation `SSAApplier` holds only a `client.Client`, a `FieldManager` string, and a `Cluster` label (`"host"` or `"remote"`)
- **AND** it holds no per-reconcile mutable state, so one instance MAY be reused across reconciles for the same cluster

### Requirement: Server-side apply with forced ownership

`SSAApplier.Apply` SHALL write the manifest using Kubernetes Server-Side Apply (`client.Apply`) with a fixed field manager `dual-deployment-operator` and `client.ForceOwnership`, so the operator authoritatively owns every field it submits and reclaims fields from stale managers without a read-modify-write loop.

#### Scenario: Apply uses SSA with the operator field manager

- **WHEN** `Apply` writes a resource to the target cluster
- **THEN** the request is a Server-Side Apply patch with field owner `dual-deployment-operator`
- **AND** `ForceOwnership` is set so conflicts on operator-owned fields are resolved in the operator's favor

#### Scenario: Internal annotations are stripped before apply

- **WHEN** a manifest carrying the internal origin annotation (`dual-deployment-operator.cc.sap/origin`) is applied
- **THEN** that annotation is removed from the submitted object before the apply
- **AND** it is never persisted on the live target object

### Requirement: CR-identity ownership label stamped on every applied object

Before applying any manifest, `SSAApplier` SHALL stamp a **CR-identity ownership label** on the submitted object identifying the `DualDeploymentOperator` CR that owns it. The label key is `dual-deployment-operator.cc.sap/owned-by` and its value is a **fixed-length hash** derived from the owning CR's namespace and name — the first 16 hex characters of `sha256("<namespace>/<name>")`. A hash is used rather than a raw `<namespace>_<name>` join because a Kubernetes label value is limited to 63 characters (which a shoot-cp namespace plus operator name can exceed, and an over-length value would make the SSA apply itself fail) and because the hash is injective across the namespace/name boundary. The value is deterministic, so it is stable across operator restarts, re-reconciles, and same-name CR re-creation (enabling re-adoption of retained objects). This label is the ownership signal reused by both the prune safety check (skip deleting an object whose label does not match) and the cluster-scoped conflict guard (below). Because it is applied as an operator-owned field via SSA, it persists across reconciles.

#### Scenario: Applied object carries the owning CR's identity label

- **WHEN** any manifest is applied on behalf of a CR named `n` in namespace `ns`
- **THEN** the submitted object carries the label `dual-deployment-operator.cc.sap/owned-by` whose value is the fixed-length hash of `ns/n` (first 16 hex chars of `sha256("ns/n")`)
- **AND** the value is ≤ 63 characters (a valid Kubernetes label value)

### Requirement: Cluster-scoped apply is guarded against cross-CR conflict

Because cluster-scoped objects share a single seed-global namespace, and `SSAApplier.Apply` uses `client.ForceOwnership` (which would otherwise silently seize a same-named object from another owner), `SSAApplier` SHALL guard every **cluster-scoped** apply with a GET-before-apply conflict check. Before applying a cluster-scoped object, it SHALL GET the live object and inspect its `dual-deployment-operator.cc.sap/owned-by` label:

- **Absent live object** — safe; apply proceeds (the operator becomes the owner and stamps its label).
- **Present, label matches this CR** (or the object carries no `owned-by` label at all — e.g. a pre-existing object from the current Helm chart during migration, owned by no operator-managed CR) — safe; apply proceeds. An unlabeled object is adopted by the plain apply, which stamps this CR's label.
- **Present, label is a *different* CR's identity** — CONFLICT. The applier SHALL NOT apply the object (it MUST NOT `ForceOwnership`-clobber another CR's cluster-scoped object), SHALL return the object's `ResourceStatus` with `Health=Degraded` and a `Message` naming the conflicting owner, and SHALL surface the conflict as a per-resource error so the reconcile records it and requeues (continue-on-error — other resources still apply).

This guard applies ONLY to cluster-scoped kinds; namespaced objects are naturally isolated by namespace and are applied without the extra GET. The guard turns the silent last-writer-wins corruption of a single-install-per-seed violation into a visible, non-destructive failure.

#### Scenario: Cluster-scoped apply proceeds when unowned or self-owned

- **WHEN** a cluster-scoped object (e.g. a `ClusterRole`) does not exist, or exists carrying this CR's `owned-by` label, or exists with no `owned-by` label
- **THEN** the apply proceeds and the object ends up carrying this CR's `owned-by` label

#### Scenario: Cluster-scoped apply refuses a foreign-owned object

- **WHEN** a cluster-scoped object already exists carrying a `dual-deployment-operator.cc.sap/owned-by` label whose value is a *different* CR's identity
- **THEN** the object is NOT applied (no `ForceOwnership` clobber)
- **AND** its `ResourceStatus.Health` is `Degraded` with a `Message` naming the conflicting owner
- **AND** the apply surfaces a per-resource error so the reconcile requeues while other resources still apply

#### Scenario: Namespaced apply skips the conflict GET

- **WHEN** a namespaced object is applied
- **THEN** no cluster-scoped conflict GET is performed for it (namespace isolation already prevents cross-CR collision)

### Requirement: caBundle strip is leaf-only and unconditional

Before applying any `ValidatingWebhookConfiguration`, `MutatingWebhookConfiguration`, or conversion-webhook `CustomResourceDefinition`, `SSAApplier` SHALL remove only the `caBundle` leaf from the submitted object — never the parent `clientConfig` map and never the webhook entry — and SHALL do so on every apply including the first. This guarantees the operator never becomes the SSA field manager of `caBundle`, so its periodic re-apply never prunes the webhook-injector's caBundle value.

#### Scenario: caBundle leaf removed from webhook configurations

- **WHEN** a `ValidatingWebhookConfiguration` or `MutatingWebhookConfiguration` is applied
- **THEN** each `webhooks[].clientConfig.caBundle` leaf is removed from the submitted object
- **AND** `webhooks[].clientConfig.url` and all other operator-owned fields are preserved in the submitted object

#### Scenario: caBundle leaf removed from conversion-webhook CRD

- **WHEN** a `CustomResourceDefinition` with `spec.conversion.strategy: Webhook` is applied
- **THEN** `spec.conversion.webhook.clientConfig.caBundle` is removed from the submitted object
- **AND** the rest of `spec.conversion.webhook.clientConfig` (including `url`) is preserved

#### Scenario: Strip runs unconditionally even when caBundle is absent

- **WHEN** a webhook configuration is applied whose rendered form does not contain a `caBundle` field
- **THEN** the strip step still runs and is a no-op
- **AND** no error is produced

#### Scenario: Operator never owns caBundle in managedFields

- **WHEN** a webhook configuration is applied and then read back
- **THEN** the `metadata.managedFields` entry for field manager `dual-deployment-operator` does not list any `caBundle` path

### Requirement: GET-after-apply health computation

After a successful apply, `SSAApplier` SHALL read the live object back with a GET and compute per-kind health from observed state. Health MUST be `Healthy`/`Progressing` for `Deployment` and `StatefulSet` based on available/ready replicas, `Healthy` for a `CustomResourceDefinition` whose `Established` condition is true, and `Healthy` on existence for all other kinds. The operator MUST NOT inspect `caBundle` when computing WebhookConfiguration health. A failed GET MUST yield health `Unknown` and MUST NOT fail the apply.

#### Scenario: Deployment health from observed replicas

- **WHEN** a `Deployment` is applied and its live `status.availableReplicas` is greater than or equal to `spec.replicas`
- **THEN** its `ResourceStatus.Health` is `Healthy`
- **AND** otherwise its health is `Progressing`

#### Scenario: CRD health from Established condition

- **WHEN** a `CustomResourceDefinition` is applied and its `Established` condition is `True`
- **THEN** its `ResourceStatus.Health` is `Healthy`

#### Scenario: WebhookConfiguration health does not depend on caBundle

- **WHEN** a `ValidatingWebhookConfiguration` is applied and exists on the target, but its `caBundle` has not yet been stamped by the webhook-injector
- **THEN** its `ResourceStatus.Health` is `Healthy` (existence-only)
- **AND** the health computation does not read the `caBundle` field

#### Scenario: GET failure degrades to Unknown, not an error

- **WHEN** the apply succeeds but the follow-up GET fails (e.g. transient API error)
- **THEN** the `ResourceStatus.Health` is `Unknown` and `Message` records the read error
- **AND** `Apply` returns a nil error (the apply itself succeeded)

### Requirement: Idempotent delete

`SSAApplier.Delete` SHALL delete the target object and MUST treat a not-found result as success, so that prune retries and partial-teardown deletion never fail on an already-removed object.

#### Scenario: Deleting an existing object succeeds

- **WHEN** `Delete(ctx, m)` is called for an object present on the target cluster
- **THEN** the object is deleted and a nil error is returned

#### Scenario: Deleting an absent object is a no-op

- **WHEN** `Delete(ctx, m)` is called for an object that does not exist on the target cluster
- **THEN** a nil error is returned (not-found is ignored)

### Requirement: Fixed intra-render apply and delete ordering

The delivery layer SHALL provide pure ordering functions that sort a manifest set by a fixed kind priority for apply (Namespace → CustomResourceDefinition → RBAC (ClusterRole, ClusterRoleBinding, Role, RoleBinding, ServiceAccount) → other kinds → webhook configurations) and the exact reverse for delete. This ordering MUST NOT be consumer-configurable; only the cross-render (host-vs-remote) sequence is configurable (see the reconcile-loop capability).

#### Scenario: Apply order places CRDs and namespaces before dependents

- **WHEN** a render containing a Namespace, a CustomResourceDefinition, a ClusterRole, a Deployment, and a ValidatingWebhookConfiguration is sorted for apply
- **THEN** the Namespace and CustomResourceDefinition sort before the ClusterRole and Deployment
- **AND** the ValidatingWebhookConfiguration sorts last

#### Scenario: Delete order is the reverse of apply order

- **WHEN** the same resource set is sorted for delete
- **THEN** the resulting order is the exact reverse of the apply order

#### Scenario: Unlisted kinds retain stable relative order

- **WHEN** a render contains kinds not named in the priority list
- **THEN** those kinds are ordered among the "other kinds" tier in their original stable emit order
