<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## MODIFIED Requirements

### Requirement: Applier interface

The `internal/deliver` package SHALL define an `Applier` interface that abstracts writing a single manifest to a target Kubernetes cluster and deleting it. The interface MUST expose an `Apply` operation that takes the manifest plus the owning CR's ownership value (see the CR-identity ownership label requirement) and returns the resource's observed `ResourceStatus`, and a `Delete` operation that is idempotent. The ownership value is passed as an argument to `Apply`, not held on the applier, so the applier remains stateless and one instance is safely reused across concurrent reconciles of different CRs.

#### Scenario: Apply returns per-resource status

- **WHEN** `Apply(ctx, m, ownedBy)` is called with a manifest `m` and an ownership value
- **THEN** it returns a `v1alpha1.ResourceStatus` carrying the resource's `Kind`, `APIVersion`, `Namespace`, `Name`, computed `Health`, and `LastApplied` timestamp
- **AND** a non-nil error only when the apply itself failed

#### Scenario: Interface is implemented by a stateless SSA applier

- **WHEN** the delivery layer is constructed
- **THEN** the concrete implementation `SSAApplier` holds only a `client.Client`, a `FieldManager` string, and a `Cluster` label (`"seed"` or `"shoot"`)
- **AND** it holds no per-reconcile mutable state, so one instance MAY be reused across reconciles for the same cluster

### Requirement: Fixed intra-render apply and delete ordering

The delivery layer SHALL provide pure ordering functions that sort a manifest set by a fixed kind priority for apply (Namespace → CustomResourceDefinition → RBAC (ClusterRole, ClusterRoleBinding, Role, RoleBinding, ServiceAccount) → other kinds → webhook configurations) and the exact reverse for delete. This ordering MUST NOT be consumer-configurable; only the cross-render (seed-vs-shoot) sequence is configurable (see the reconcile-loop capability).

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
