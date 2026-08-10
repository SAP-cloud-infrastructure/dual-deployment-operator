# Knowledge Base Index

## CRD Status Reference

**File**: `crd-status-reference.md`

**Purpose**: Centralized reference for DualDeploymentOperator CRD Status struct, HealthState enum, condition types, printcolumns, and deletion semantics. Designed for Phase 8–10 reuse (equivalence tests, deployment charts, admission webhooks).

**Sections**:
- **Status Struct Definition**: Fields, types, ownership semantics
- **HealthState Enum**: Values (Unknown, Healthy, Unhealthy) with operational meaning
- **ResourceStatus**: Per-resource health tracking (name, namespace, kind, health, lastUpdateTime)
- **Condition Types & Reasons**: Table of all condition types (Ready, SourcesReady, DeliveryReady, ShootCleanup) with reason codes and semantics
- **Printcolumns**: Kubebuilder markers for kubectl output (Status, SourcesReady, DeliveryReady, ShootCleanup, Age)
- **Deletion Flow**: Reconciliation behavior when DualDeploymentOperator is deleted (finalizer, ShootCleanup condition, force-delete annotation override)
- **Phase 8–10 Cross-References**: Links to equivalence test scenarios, chart pod template requirements, and admission webhook validation rules

**Use Cases**:
- Phase 8 (Equivalence Tests): Validate condition types and HealthState enum match golden render expectations
- Phase 9 (Deployment Charts): Reference printcolumns and Status struct for chart values.yaml defaults and pod template annotations
- Phase 9.5 (Operator Image Build): Verify Status struct marshaling in operator binary
- Phase 10 (Validating Admission Webhook): Enforce condition type/reason validation and deletion semantics

---

**Last Updated**: 2026-08-10
**Source**: `api/v1alpha1/dualdeploymentoperator_types.go` (lines 172–217), `internal/controller/dualdeploymentoperator_controller_test.go` (lines 523–547)
