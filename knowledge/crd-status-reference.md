# CRD Status Reference: DualDeploymentOperator

**Source**: `api/v1alpha1/dualdeploymentoperator_types.go` (lines 172–217)  
**Last Updated**: 2026-08-10  
**Scope**: Phase 8–10 (Equivalence tests, Deployment charts, Validating admission webhook)

## Status Struct

```go
type DualDeploymentOperatorStatus struct {
	// Conditions represent the latest available observations of the DualDeploymentOperator's state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed DualDeploymentOperator.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// HealthState represents the overall health of the DualDeploymentOperator.
	HealthState HealthState `json:"healthState,omitempty"`

	// ResourceStatus tracks the status of rendered resources.
	ResourceStatus []ResourceStatus `json:"resourceStatus,omitempty"`
}
```

## HealthState Enum

```go
type HealthState string

const (
	HealthStateHealthy   HealthState = "Healthy"
	HealthStateUnhealthy HealthState = "Unhealthy"
	HealthStateUnknown   HealthState = "Unknown"
)
```

**Semantics**:
- `Healthy`: All conditions are True; reconciliation succeeded; resources applied cleanly.
- `Unhealthy`: One or more conditions are False; reconciliation failed or resources are degraded.
- `Unknown`: Initial state or condition evaluation pending.

## ResourceStatus Struct

```go
type ResourceStatus struct {
	// Name is the name of the resource.
	Name string `json:"name,omitempty"`

	// Namespace is the namespace of the resource.
	Namespace string `json:"namespace,omitempty"`

	// Kind is the kind of the resource.
	Kind string `json:"kind,omitempty"`

	// Status is the status of the resource.
	Status string `json:"status,omitempty"`
}
```

**Usage**: Tracks per-resource health in the shoot cluster (e.g., Deployment, StatefulSet, DaemonSet).

## Condition Types & Reasons

| Type | Reason | Status | Meaning |
|------|--------|--------|---------|
| `Ready` | `ReconciliationSucceeded` | True | Reconciliation completed; all resources applied. |
| `Ready` | `ReconciliationFailed` | False | Reconciliation failed; check error logs. |
| `Ready` | `ShootClientNotReady` | False | Shoot cluster client unavailable (token/CA missing or invalid). |
| `Ready` | `SourceRenderFailed` | False | Helm/Kustomize render failed; check source spec. |
| `Ready` | `ManifestTransformFailed` | False | Patch/webhook-rewrite/filter transformation failed. |
| `Ready` | `ResourceDeliveryFailed` | False | SSA apply to shoot cluster failed. |
| `ShootCleanup` | `ShootCleanupSucceeded` | True | Finalizer cleanup completed; all resources pruned from shoot. |
| `ShootCleanup` | `ShootCleanupBlocked` | False | Shoot client unreachable; cleanup blocked (waiting for force-delete annotation or client recovery). |
| `ShootCleanup` | `ShootCleanupFailed` | False | Cleanup attempted but failed (e.g., prune error). |

**Condition Observation Rules**:
- `ObservedGeneration` increments on spec change; conditions reflect the latest generation.
- Conditions use `metav1.Condition` (standard K8s type with `type`, `status`, `reason`, `message`, `lastTransitionTime`, `observedGeneration`).

## PrintColumn Markers

```go
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="HealthState",type=string,JSONPath=".status.healthState"
// +kubebuilder:printcolumn:name="ObservedGeneration",type=integer,JSONPath=".status.observedGeneration"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].message"
```

**Display Order** (kubectl get dualdeploymentoperators):
1. NAME (implicit)
2. Ready (condition status: True/False/Unknown)
3. HealthState (Healthy/Unhealthy/Unknown)
4. ObservedGeneration (integer)
5. Age (duration since creation)
6. Message (latest Ready condition message)

## Deletion & Finalizer Behavior

**Finalizer**: `dual-deployment-operator.cc.sap/cleanup`

**Deletion Flow**:
1. User deletes DualDeploymentOperator CR.
2. Kubernetes sets `metadata.deletionTimestamp`; controller observes deletion.
3. Controller reconciles with `ShootCleanup` condition = False, reason = `ShootCleanupBlocked` (if shoot client unreachable).
4. Controller attempts to prune all rendered resources from shoot cluster.
5. On success: `ShootCleanup` condition = True, reason = `ShootCleanupSucceeded`; finalizer removed; CR deleted.
6. On failure or shoot client unreachable: `ShootCleanup` condition = False, reason = `ShootCleanupBlocked` or `ShootCleanupFailed`; finalizer retained; CR remains.
7. **Force-delete override**: If `dual-deployment-operator.cc.sap/force-delete: "true"` annotation is present, controller skips shoot cleanup and removes finalizer immediately (CR deleted without pruning).

**Test Assertions** (from `internal/controller/dualdeploymentoperator_controller_test.go`, lines 523–547):
- Deletion without force-delete: `ShootCleanup` condition observed; finalizer present until cleanup succeeds.
- Deletion with force-delete annotation: Finalizer removed immediately; CR deleted without shoot prune.
- Shoot client unreachable during deletion: `ShootCleanupBlocked` condition set; finalizer retained; CR remains until client recovers or force-delete is applied.

## Phase 8–10 Cross-References

### Phase 8: Equivalence Tests
- Use `HealthState` and `Ready` condition to validate operator-side render matches golden render from sapcc/helm-charts.
- Verify printcolumns render correctly in test fixtures.

### Phase 9: Deployment Charts
- Chart 1 pod template must carry Gardener egress labels: `networking.gardener.cloud/to-dns`, `to-public-networks`, `to-private-networks`.
- Status conditions and HealthState are read-only (set by controller); chart does not modify them.

### Phase 9.5: Operator Image Build & Publish
- No status changes; image build is orthogonal to CRD status.

### Phase 10: Validating Admission Webhook
- Webhook may validate `spec` changes; status is immutable (webhook does not modify status).
- Webhook may reject invalid source specs that would cause `SourceRenderFailed` condition.

---

**Maintainer Notes**:
- Status struct is auto-generated from markers; do NOT hand-edit `zz_generated.deepcopy.go`.
- Condition types and reasons are conventions; add new reasons as needed (e.g., `SourceCacheMiss` in Phase 7.5).
- HealthState is a convenience field; derive it from condition statuses in reconciliation logic.
