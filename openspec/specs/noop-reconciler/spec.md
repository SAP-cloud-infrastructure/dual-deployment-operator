# Spec: No-op Reconciler

## Purpose

Defines the behavior of the v1 `DualDeploymentOperatorReconciler`: it watches `DualDeploymentOperator` CRs, logs one info line per reconcile, requeues at 10-minute intervals, and performs no mutations. Also covers manager health/readiness endpoints, metrics, structured logging, and leader election configuration.

## Requirements

### Requirement: Controller watches DualDeploymentOperator CRs

The `DualDeploymentOperatorReconciler` MUST register a controller with controller-runtime that watches `DualDeploymentOperator` CRs in its own namespace. The controller MUST be registered by the manager in `cmd/main.go` via `SetupWithManager`.

#### Scenario: Manager starts with the controller registered

- **WHEN** the operator binary is invoked with `manager --leader-elect=false`
- **THEN** the manager logs indicate the `DualDeploymentOperator` controller has started
- **AND** the manager watches the `DualDeploymentOperator` GVK

#### Scenario: SetupWithManager returns nil on valid setup

- **WHEN** a test invokes `(&DualDeploymentOperatorReconciler{}).SetupWithManager(mgr)` against a valid manager
- **THEN** the call returns `nil`
- **AND** the controller is registered in the manager's controller list

---

### Requirement: Reconcile handles missing CR gracefully

The `Reconcile` method MUST fetch the CR by name; if the CR does not exist (already deleted, not yet created), `Reconcile` MUST return `ctrl.Result{}, nil` and MUST NOT return an error. The fetch MUST use `client.IgnoreNotFound(err)` idiom.

#### Scenario: Missing CR returns no error

- **WHEN** the reconciler is invoked with a `ctrl.Request` for a CR that does not exist
- **THEN** `Reconcile` returns `(ctrl.Result{}, nil)`
- **AND** no log at level Error is emitted

---

### Requirement: Reconcile logs one info line and requeues at 10 minutes

When the reconciler successfully fetches a CR, it MUST emit exactly one log line at level Info containing the message `"reconciling"` and structured fields `name` and `namespace` matching the CR, then return `ctrl.Result{RequeueAfter: 10 * time.Minute}, nil`.

The requeue interval MUST be defined in code as `10 * time.Minute` and MUST match `docs/design.md` §3.6.2's drift-correction default so v1 lands with the same cadence Phase 6 will use for real work.

#### Scenario: Info log line is emitted per reconcile

- **WHEN** the reconciler is invoked with a `ctrl.Request` for an existing CR named `metal-operator` in namespace `shoot--cp--m-eu-de-1`
- **THEN** exactly one Info-level log line is emitted
- **AND** the log message is `"reconciling"`
- **AND** the log has structured fields `name=metal-operator` and `namespace=shoot--cp--m-eu-de-1`

#### Scenario: RequeueAfter is 10 minutes

- **WHEN** the reconciler is invoked with a valid CR
- **THEN** the returned `ctrl.Result.RequeueAfter` equals `10 * time.Minute`
- **AND** the returned `ctrl.Result.Requeue` is `false`
- **AND** the returned error is `nil`

---

### Requirement: Reconcile does not mutate the CR

The v1 reconciler MUST NOT write to any field of the CR — no `Spec` mutation, no `Status` update, no `Metadata` change (except that controller-runtime may set observed generation internally), no finalizer add/remove. This preserves the reconciler as a pure wiring stub; Phase 6 introduces status writes and finalizer management.

#### Scenario: CR is byte-identical after reconcile

- **WHEN** a CR is created, then the reconciler is invoked
- **THEN** the CR's `spec`, `status`, and `metadata.finalizers` fields are unchanged after reconcile
- **AND** no `Update` or `Patch` call is issued by the reconciler against the CR

#### Scenario: No status subresource writes

- **WHEN** a CR is reconciled multiple times over 30 minutes of envtest simulated time
- **THEN** `status.hostResources`, `status.remoteResources`, `status.conditions`, and `status.lastReconcile` remain unset

---

### Requirement: Manager health and readiness endpoints

The manager MUST expose `/healthz` (liveness) and `/readyz` (readiness) HTTP endpoints on the health-probe port configured by the kubebuilder scaffold (default `:8081`). Both endpoints MUST return HTTP 200 when the manager is running.

#### Scenario: Endpoints respond 200 when manager is healthy

- **WHEN** the manager is started and reaches leader-elected state
- **THEN** a GET request to `http://127.0.0.1:8081/healthz` returns HTTP 200
- **AND** a GET request to `http://127.0.0.1:8081/readyz` returns HTTP 200

---

### Requirement: Manager metrics endpoint

The manager MUST expose a Prometheus-format metrics endpoint on the metrics port configured by the kubebuilder scaffold (default `:8080`). The metrics MUST include the standard controller-runtime metrics: `controller_runtime_reconcile_total`, `controller_runtime_reconcile_time_seconds`, and process-level Go metrics.

Metrics endpoint authentication and authorization follow kubebuilder defaults (kube-rbac-proxy sidecar in the manager Deployment); v1 does not customize this.

#### Scenario: Metrics contain reconcile counter

- **WHEN** the reconciler processes a CR once
- **THEN** the metrics endpoint reports `controller_runtime_reconcile_total{controller="dualdeploymentoperator", result="success"} >= 1`

---

### Requirement: Structured logging via controller-runtime logger

All log lines emitted by the operator MUST use the controller-runtime `logr.Logger` accessible via `ctrl.LoggerFrom(ctx)`, not `fmt.Println` or `log.Println`. Log lines MUST include structured fields (`name`, `namespace`, `controller`) rather than message string interpolation.

#### Scenario: All log lines carry structured fields

- **WHEN** the reconciler is invoked with a valid CR
- **THEN** the emitted log line JSON contains `name`, `namespace`, and `controller` keys as separate fields
- **AND** the log message string does NOT include the CR name via string interpolation (e.g., no `"reconciling foo"` — must be `msg="reconciling"` with `name=foo` as a separate field)

---

### Requirement: Leader election configured but disabled by default

The manager MUST support leader election via the kubebuilder scaffold's `--leader-elect` flag. In v1 the default MUST be `false` so envtest and local development work without needing a leader-election ConfigMap.

Phase 8's deployment chart will set `--leader-elect=true` for cluster deployment; the flag exists and works, only its default value is off.

#### Scenario: Manager runs without leader election

- **WHEN** the operator is invoked as `manager --leader-elect=false` (the default)
- **THEN** the manager starts and reconciles CRs
- **AND** no leader-election ConfigMap or Lease is created in the operator's namespace

#### Scenario: Manager supports leader election when enabled

- **WHEN** the operator is invoked as `manager --leader-elect=true --leader-election-id=dual-deployment-operator`
- **THEN** the manager creates a Lease resource in its namespace
- **AND** the manager waits for leadership before reconciling
