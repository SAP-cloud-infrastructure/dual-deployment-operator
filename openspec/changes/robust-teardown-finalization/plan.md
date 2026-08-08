<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Robust Teardown Finalization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `DualDeploymentOperator` CR deletion robust to an unreachable shoot — never deadlock, never silently orphan — and make the reconcile pipeline observable via structured logging, per-resource apply status, and CRD status printer-columns.

**Architecture:** A localized control-flow change in `reconcileDelete` (stop hard-returning on shoot-client build failure; block-until-clean with a `force-delete` annotation override), plus additive structured logging in `internal/controller` and `internal/source`, plus marker-only `+kubebuilder:printcolumn` additions on the CRD type. `spec.applyOrder` and reverse-order teardown are unchanged. No `internal/deliver` change, no CRD spec/status schema change.

**Tech Stack:** Go, controller-runtime (`sigs.k8s.io/controller-runtime`), Ginkgo/Gomega (BDD, envtest), kubebuilder markers + `controller-gen`, `logr` via `log.FromContext(ctx)`.

**Build / test / lint commands (SAP go-makefile-maker project — use these, NOT `make test`/`make build`):**
- build: `go build ./...`
- unit test (controller/webhook suites need envtest): `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/controller/... -v`
- non-envtest package test: `go test ./internal/source/... -v`
- codegen: `make manifests generate`
- lint: `make run-golangci-lint`
- full gate: `make check`

**Key files:**
- Modify: `internal/controller/dualdeploymentoperator_controller.go` (`reconcileDelete` ~L262-326; `applyAll` ~L368; add signal helpers + `ForceDeleteAnnotation` const)
- Modify: `api/v1alpha1/dualdeploymentoperator_types.go` (printcolumn markers on the `DualDeploymentOperator` type ~L207)
- Modify (generated): `config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml` (via `make manifests`)
- Modify: `internal/source/*.go` (source-pull + render-cache hit/miss logging)
- Modify: `docs/design.md` (new "Deleting a DualDeploymentOperator" section)
- Test: `internal/controller/dualdeploymentoperator_controller_test.go` (replace the ShootUnreachable deletion test; add new cases)

---

## Task 1: Add `ForceDeleteAnnotation` const

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller.go` (const block ~L36-44)

- [ ] **Step 1: Add the annotation const**

In the existing `const (...)` block alongside `FinalizerName`, add:

```go
	// ForceDeleteAnnotation, when set to "true" on a CR being deleted, tells the
	// reconciler to remove the finalizer even though shoot cleanup is incomplete —
	// an explicit, auditable operator consent to orphaning remaining shoot resources.
	// It is the supported alternative to a raw `kubectl patch ... finalizers:[]`.
	ForceDeleteAnnotation = "dual-deployment-operator.cc.sap/force-delete"
```

- [ ] **Step 2: Build to verify it compiles**

Run: `go build ./...`
Expected: exit 0.

- [ ] **Step 3: Commit**

```bash
git add internal/controller/dualdeploymentoperator_controller.go
git commit -m "feat(controller): add ForceDeleteAnnotation const"
```

- [x] Task 1 complete

---

## Task 2: Add teardown signal helpers (ShootCleanupBlocked / ShootCleanupForceDeleted)

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller.go` (add helpers near the existing `setCondition` ~L383)

The existing code sets `ShootUnreachable` inline in `reconcileDelete`. Extract/add three helpers so the delete path is readable and the new signals exist.

- [ ] **Step 1: Add `recordShootUnreachable`, `recordShootCleanupBlocked`, `recordShootCleanupForceDeleted`**

Add these methods (they only mutate `cr` in memory + emit events; the caller persists status via `r.Status().Update`):

```go
// recordShootUnreachable records the non-fatal "shoot could not be reached/built during
// deletion" signal: a Warning event + Ready=False. Deletion still proceeds to seed cleanup.
func (r *DualDeploymentOperatorReconciler) recordShootUnreachable(cr *ddov1alpha1.DualDeploymentOperator, err error) {
	if r.Recorder != nil {
		r.Recorder.Eventf(cr, nil, corev1.EventTypeWarning, "ShootUnreachable",
			"RetainFinalizer", "%s", "Shoot API server unreachable during deletion; proceeding with seed cleanup and retaining finalizer")
	}
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  "ShootUnreachable",
		Message: fmt.Sprintf("shoot unreachable during deletion: %v", err),
	})
}

// recordShootCleanupBlocked records that CR deletion is waiting on shoot cleanup: a Warning
// event + a dedicated ShootCleanup=False/Blocked condition naming the override annotation.
func (r *DualDeploymentOperatorReconciler) recordShootCleanupBlocked(cr *ddov1alpha1.DualDeploymentOperator, cause error) {
	if r.Recorder != nil {
		r.Recorder.Eventf(cr, nil, corev1.EventTypeWarning, "ShootCleanupBlocked",
			"RetainFinalizer", "Shoot cleanup incomplete; retaining finalizer. Set annotation %s=true to force deletion (orphans remaining shoot resources)", ForceDeleteAnnotation)
	}
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
		Type:    "ShootCleanup",
		Status:  metav1.ConditionFalse,
		Reason:  "Blocked",
		Message: fmt.Sprintf("waiting on shoot cleanup; set %s=true to force: %v", ForceDeleteAnnotation, cause),
	})
}

// recordShootCleanupForceDeleted records that the operator explicitly consented to abandoning
// shoot cleanup: a distinct Warning event + ShootCleanup=False/ForceDeleted condition.
func (r *DualDeploymentOperatorReconciler) recordShootCleanupForceDeleted(cr *ddov1alpha1.DualDeploymentOperator, cause error) {
	if r.Recorder != nil {
		r.Recorder.Eventf(cr, nil, corev1.EventTypeWarning, "ShootCleanupForceDeleted",
			"ForceDelete", "%s", "Finalizer removed via force-delete annotation; remaining shoot resources may be orphaned")
	}
	msg := "shoot cleanup abandoned by operator force-delete; shoot resources may be orphaned"
	if cause != nil {
		msg = fmt.Sprintf("%s (last error: %v)", msg, cause)
	}
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
		Type:    "ShootCleanup",
		Status:  metav1.ConditionFalse,
		Reason:  "ForceDeleted",
		Message: msg,
	})
}
```

- [ ] **Step 2: Build to verify it compiles**

Run: `go build ./...`
Expected: exit 0 (helpers unused until Task 4 — Go allows unused methods).

- [ ] **Step 3: Commit**

```bash
git add internal/controller/dualdeploymentoperator_controller.go
git commit -m "feat(controller): add ShootCleanup signal helpers for teardown"
```

- [x] Task 2 complete

---

## Task 3: RED — write the new deletion tests

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller_test.go` (replace the `It("retains finalizer and sets ShootUnreachable when Secret is missing", ...)` block ~L404-447)

- [ ] **Step 1: Replace the buggy test with the block-until-clean test + force-delete test**

Replace the existing `It("retains finalizer and sets ShootUnreachable when Secret is missing", ...)` block with these two specs (same `Describe("reconcileDelete")`, same `newDeleteCR`/`deleteNS`):

```go
		It("proceeds with seed cleanup, blocks the finalizer, and sets ShootCleanupBlocked when the shoot is unreachable", func() {
			cr := newDeleteCR("test-del-blocked")
			ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

			// A seed resource owned by this CR that MUST be deleted even though the shoot is unreachable.
			seedCM := &unstructured.Unstructured{}
			seedCM.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
			seedCM.SetName("seed-cm-del-blocked")
			seedCM.SetNamespace(deleteNS)
			seedCM.SetLabels(map[string]string{manifest.OwnedByLabel: ownedBy})
			Expect(k8sClient.Create(ctx, seedCM)).To(Succeed())

			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			// Record the seed ConfigMap and one shoot resource in status so teardown attempts both.
			got := &ddov1alpha1.DualDeploymentOperator{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
			got.Status.SeedResources = []ddov1alpha1.ResourceStatus{
				{Kind: "ConfigMap", APIVersion: "v1", Namespace: deleteNS, Name: "seed-cm-del-blocked", Health: ddov1alpha1.HealthHealthy},
			}
			got.Status.ShootResources = []ddov1alpha1.ResourceStatus{
				{Kind: "ConfigMap", APIVersion: "v1", Namespace: "kube-system", Name: "shoot-cm", Health: ddov1alpha1.HealthHealthy},
			}
			Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())

			fakeRecorder := events.NewFakeRecorder(10)
			r := &DualDeploymentOperatorReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: fakeRecorder,
				SeedApplier: &deliver.SSAApplier{
					Client: k8sClient, FieldManager: FieldManagerName, Cluster: "seed",
				},
			}

			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			result, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Seed resource was deleted despite the unreachable shoot.
			seedGet := &unstructured.Unstructured{}
			seedGet.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
			err = k8sClient.Get(ctx, types.NamespacedName{Namespace: deleteNS, Name: "seed-cm-del-blocked"}, seedGet)
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "seed resource must be deleted even when shoot unreachable")

			// Finalizer retained; ShootCleanup=Blocked set.
			after := &ddov1alpha1.DualDeploymentOperator{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), after)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(after, FinalizerName)).To(BeTrue())
			blocked := meta.FindStatusCondition(after.Status.Conditions, "ShootCleanup")
			Expect(blocked).NotTo(BeNil())
			Expect(blocked.Status).To(Equal(metav1.ConditionFalse))
			Expect(blocked.Reason).To(Equal("Blocked"))
			Expect(fakeRecorder.Events).To(Receive(ContainSubstring("ShootUnreachable")))
		})

		It("removes the finalizer and sets ShootCleanupForceDeleted when the force-delete annotation is set", func() {
			cr := newDeleteCR("test-del-force")
			cr.Annotations = map[string]string{ForceDeleteAnnotation: "true"}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			got := &ddov1alpha1.DualDeploymentOperator{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
			got.Status.ShootResources = []ddov1alpha1.ResourceStatus{
				{Kind: "ConfigMap", APIVersion: "v1", Namespace: "kube-system", Name: "shoot-cm", Health: ddov1alpha1.HealthHealthy},
			}
			Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())

			fakeRecorder := events.NewFakeRecorder(10)
			r := &DualDeploymentOperatorReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: fakeRecorder,
				SeedApplier: &deliver.SSAApplier{
					Client: k8sClient, FieldManager: FieldManagerName, Cluster: "seed",
				},
			}

			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// CR is gone (finalizer removed) OR finalizer absent on re-get.
			after := &ddov1alpha1.DualDeploymentOperator{}
			getErr := k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), after)
			if getErr == nil {
				Expect(controllerutil.ContainsFinalizer(after, FinalizerName)).To(BeFalse())
			} else {
				Expect(apierrors.IsNotFound(getErr)).To(BeTrue())
			}
			Expect(fakeRecorder.Events).To(Receive(ContainSubstring("ShootCleanupForceDeleted")))
		})
```

- [ ] **Step 2: Ensure `apierrors` is imported**

Confirm the test file imports `apierrors "k8s.io/apimachinery/pkg/api/errors"`. If missing, add it to the import block.

- [ ] **Step 3: Run the new tests — expect FAIL**

Run: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/controller/... -run TestControllers -v`
Expected: FAIL — the current `reconcileDelete` hard-returns before seed cleanup (seed ConfigMap not deleted) and has no `ShootCleanup`/force-delete handling.

- [ ] **Step 4: Commit the failing tests**

```bash
git add internal/controller/dualdeploymentoperator_controller_test.go
git commit -m "test(controller): RED for block-until-clean teardown + force-delete override"
```

- [x] Task 3 complete

---

## Task 4: GREEN — rewrite `reconcileDelete` control flow

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller.go` (`reconcileDelete` ~L262-326)

- [ ] **Step 1: Replace the body of `reconcileDelete`**

Replace the whole function body (keep the signature) with:

```go
func (r *DualDeploymentOperatorReconciler) reconcileDelete(ctx context.Context, cr *ddov1alpha1.DualDeploymentOperator) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

	// Build the shoot applier; on failure, record ShootUnreachable but DO NOT return.
	shootApplier, buildErr := r.buildShootApplierOrDefault(ctx, cr)
	if buildErr != nil {
		r.recordShootUnreachable(cr, buildErr)
	}

	deleteRender := func(applier deliver.Applier, statuses []ddov1alpha1.ResourceStatus) []error {
		if applier == nil {
			return nil // shoot unreachable this pass: skip, retry later
		}
		var errs []error
		for _, rs := range deliver.SortStatusForDelete(statuses) {
			if rs.Kind == "CustomResourceDefinition" && cr.Spec.RetentionPolicy.CRDs != "Delete" {
				continue
			}
			if e := applier.Delete(ctx, deliver.ManifestFromStatus(rs), ownedBy); e != nil {
				errs = append(errs, e)
			}
		}
		return errs
	}

	// Tear down in the reverse of spec.applyOrder — UNCHANGED generic behavior.
	var shootErrs, seedErrs []error
	if cr.Spec.ApplyOrder == "SeedFirst" {
		shootErrs = deleteRender(shootApplier, cr.Status.ShootResources)
		seedErrs = deleteRender(r.SeedApplier, cr.Status.SeedResources)
	} else {
		seedErrs = deleteRender(r.SeedApplier, cr.Status.SeedResources)
		shootErrs = deleteRender(shootApplier, cr.Status.ShootResources)
	}

	// Real seed-delete errors retry via controller-runtime backoff.
	if agg := kerrors.NewAggregate(seedErrs); agg != nil {
		return ctrl.Result{}, agg
	}

	shootDone := buildErr == nil && len(shootErrs) == 0
	forceDelete := cr.Annotations[ForceDeleteAnnotation] == "true"
	logger.Info("Processed CR deletion", "shootDone", shootDone, "forceDelete", forceDelete)

	switch {
	case shootDone:
		controllerutil.RemoveFinalizer(cr, FinalizerName)
		return ctrl.Result{}, r.Update(ctx, cr)
	case forceDelete:
		r.recordShootCleanupForceDeleted(cr, kerrors.NewAggregate(shootErrs))
		if uErr := r.Status().Update(ctx, cr); uErr != nil {
			logger.Error(uErr, "Failed to update status before force-delete finalizer removal")
		}
		controllerutil.RemoveFinalizer(cr, FinalizerName)
		return ctrl.Result{}, r.Update(ctx, cr)
	default:
		r.recordShootCleanupBlocked(cr, kerrors.NewAggregate(shootErrs))
		if uErr := r.Status().Update(ctx, cr); uErr != nil {
			logger.Error(uErr, "Failed to update status while blocked on shoot cleanup")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
}
```

- [ ] **Step 2: Run the Task 3 tests — expect PASS**

Run: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/controller/... -run TestControllers -v`
Expected: PASS (both new deletion specs green; the reachable-shoot happy-path spec still green).

- [ ] **Step 3: Run lint**

Run: `make run-golangci-lint`
Expected: no new violations in `internal/controller/`.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/dualdeploymentoperator_controller.go
git commit -m "fix(controller): block-until-clean teardown; no hard-return on unreachable shoot"
```

- [x] Task 4 complete

---

## Task 5: Per-resource apply-status logging in `applyAll`

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller.go` (`applyAll` ~L368-379)

- [ ] **Step 1: Add V(1) per-resource logging + a V(0) summary**

Replace the `applyAll` body with:

```go
func (r *DualDeploymentOperatorReconciler) applyAll(ctx context.Context, applier deliver.Applier, ms []manifest.Manifest, ownedBy string) ([]ddov1alpha1.ResourceStatus, error) {
	logger := log.FromContext(ctx)
	out := make([]ddov1alpha1.ResourceStatus, 0, len(ms))
	var errs []error
	var degraded int
	for _, m := range ms {
		st, err := applier.Apply(ctx, m, ownedBy)
		if err != nil {
			errs = append(errs, err)
		}
		if st.Health == ddov1alpha1.HealthDegraded {
			degraded++
		}
		logger.V(1).Info("Applied resource",
			"kind", st.Kind, "name", st.Name, "namespace", st.Namespace,
			"health", st.Health, "message", st.Message)
		out = append(out, st)
	}
	logger.Info("Applied render", "applied", len(out), "degraded", degraded)
	return out, kerrors.NewAggregate(errs)
}
```

- [ ] **Step 2: Build + run controller tests — expect PASS**

Run: `go build ./... && KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/controller/... -run TestControllers`
Expected: exit 0, PASS (behavior unchanged; logging is additive).

- [ ] **Step 3: Commit**

```bash
git add internal/controller/dualdeploymentoperator_controller.go
git commit -m "feat(controller): log per-resource apply status at V(1) + apply summary"
```

- [x] Task 5 complete

---

## Task 6: Reconcile-pipeline logging (delete progress, apply/prune summaries)

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller.go` (prune path ~L227-252; delete already logged in Task 4)

- [ ] **Step 1: Add a prune-summary log line**

In the reconcile prune section, after computing pruned/retained for a target, add (adapt variable names to the existing `prune` return):

```go
	logger.Info("Pruned orphans", "cluster", "seed", "pruned", len(prevSeedResources))
```
and the analogous line for shoot after `pruneShoot()`:
```go
	logger.Info("Pruned orphans", "cluster", "shoot", "pruned", len(prevShootResources))
```
(Place each immediately after the corresponding prune call; use the actual count of deleted orphans if the `prune` helper returns it, otherwise the previous-set length as an upper bound.)

- [ ] **Step 2: Build + run controller tests — expect PASS**

Run: `go build ./... && KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/controller/... -run TestControllers`
Expected: exit 0, PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/controller/dualdeploymentoperator_controller.go
git commit -m "feat(controller): log prune summaries per target"
```

- [x] Task 6 complete

---

## Task 7: Source-pull + render-cache hit/miss logging

**Files:**
- Modify: `internal/source/*.go` — the pull entry point (Helm `Load` / kustomize resolve) and the render-cache lookup (`Deps.RenderCache`)

- [ ] **Step 1: Locate the pull + cache-lookup sites**

Run: `grep -rn "func.*Load\|RenderCache\|Get(\|resolved" internal/source/*.go | grep -v _test`
Identify (a) where the chart/kustomization is pulled and the content id is resolved, and (b) where `RenderCache` is consulted.

- [ ] **Step 2: Add pull + cache log lines**

At the pull site, after the content id is resolved, add:
```go
	log.FromContext(ctx).Info("Pulled source", "sourceKind", sourceKind, "ref", ref, "resolvedID", resolvedID)
```
At the render-cache lookup, on hit:
```go
	log.FromContext(ctx).V(1).Info("Render cache lookup", "resolvedID", resolvedID, "cache", "hit")
```
and on miss:
```go
	log.FromContext(ctx).V(1).Info("Render cache lookup", "resolvedID", resolvedID, "cache", "miss")
```
Use the actual variable names present at each site. If the function has no `ctx`, thread the existing logger the package already uses; do NOT add a new logging dependency. Never log credentials or rendered values.

- [ ] **Step 3: Build + run source tests — expect PASS**

Run: `go build ./... && go test ./internal/source/...`
Expected: exit 0, PASS (logging additive; no behavior change).

- [ ] **Step 4: Commit**

```bash
git add internal/source/
git commit -m "feat(source): log source pull and render-cache hit/miss"
```

- [x] Task 7 complete

---

## Task 8: CRD status printer-columns (marker + regen)

**Files:**
- Modify: `api/v1alpha1/dualdeploymentoperator_types.go` (markers above `type DualDeploymentOperator struct` ~L207)
- Modify (generated): `config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml`

- [ ] **Step 1: Add printcolumn markers**

Immediately above the existing `// +kubebuilder:object:root=true` block on `DualDeploymentOperator`, add:

```go
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
```

- [ ] **Step 2: Regenerate manifests**

Run: `make manifests`
Expected: `config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml` now contains a `spec.versions[0].additionalPrinterColumns` list with `Ready`, `Reason`, `Age`.

- [ ] **Step 3: Verify the generated columns**

Run: `grep -A12 "additionalPrinterColumns" config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml`
Expected: three entries with `name: Ready` / `name: Reason` / `name: Age` and the JSONPaths from Step 1 (Age with `type: date`).

- [ ] **Step 4: Commit**

```bash
git add api/v1alpha1/dualdeploymentoperator_types.go config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml
git commit -m "feat(api): add Ready/Reason/Age printer-columns to DualDeploymentOperator CRD"
```

- [x] Task 8 complete

---

## Task 9: User documentation for deletion + force-delete

**Files:**
- Modify: `docs/design.md` (add a "Deleting a DualDeploymentOperator" section)

- [ ] **Step 1: Add the deletion section**

Append a section to `docs/design.md`:

```markdown
## Deleting a DualDeploymentOperator

Deletion is finalizer-driven. On `kubectl delete dualdeploymentoperator <name>`:

- **Normal:** the operator tears down shoot and seed resources in the reverse of
  `spec.applyOrder`, then removes its finalizer and the CR is deleted.
- **Shoot unreachable:** seed resources are still cleaned up, but the finalizer is
  **retained** and the CR stays `Terminating`. The `Ready` condition shows
  `ShootUnreachable`; a `ShootCleanup=Blocked` condition and a `ShootCleanupBlocked`
  event explain that deletion is waiting on shoot cleanup and name the escape hatch.
  The operator keeps retrying — a transient shoot outage self-heals with no action.
- **Force-delete (shoot permanently gone):** set the annotation to complete deletion,
  deliberately orphaning any remaining shoot resources:

      kubectl annotate dualdeploymentoperator <name> \
        dual-deployment-operator.cc.sap/force-delete=true

  The operator completes seed cleanup, removes the finalizer, and emits a
  `ShootCleanupForceDeleted` event/condition for audit. Prefer this over a raw
  `kubectl patch ... finalizers:[]`, which deletes the CR outright and skips seed cleanup.

There is **no timeout** that auto-removes the finalizer: this operator has no
post-finalizer retry agent, so orphaning only ever happens by the explicit annotation above.
```

- [ ] **Step 2: REUSE/lint check**

Run: `make run-golangci-lint` (docs-only change won't affect Go lint; ensures nothing else regressed) and confirm the file has the SPDX header conventions of neighboring docs (add the HTML SPDX comment at top if the section is a new file — here it's an append, so no header needed).

- [ ] **Step 3: Commit**

```bash
git add docs/design.md
git commit -m "docs: document DualDeploymentOperator deletion states and force-delete annotation"
```

- [x] Task 9 complete

---

## Task 10: Full gate + reverse-order regression guard

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller_test.go` (add the reverse-order assertion if not already covered by the happy-path spec)

- [ ] **Step 1: Add a reverse-order deletion assertion**

Ensure a spec asserts that on deletion the two renders are processed in reverse of `spec.applyOrder`. If the existing reachable-shoot spec does not assert order, extend it to record both a seed and a shoot resource in status and assert the shoot delete happens before the seed delete under default `ShootFirst` becomes seed-then-shoot — i.e. seed deleted first (reverse of `ShootFirst`). Use a fake applier that appends `cluster` to a slice to capture order, or assert via the injected `shootApplierFor` + `SeedApplier` call sequence.

- [ ] **Step 2: Run the full gate**

Run: `make check`
Expected: build, vet, lint, and `go test ./...` (with envtest) all green. Note any pre-existing failures unrelated to this change and confirm they predate it.

- [ ] **Step 3: Verify specs are satisfied (self-check against `openspec/changes/.../specs/`)**

Confirm: reconcile-loop scenarios (unreachable→seed cleanup, block, no-timeout, force-delete, reverse-order) map to Tasks 3–4 + this task; render-audit-logging scenarios map to Tasks 5–7; crd-types scenarios map to Task 8.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/dualdeploymentoperator_controller_test.go
git commit -m "test(controller): guard reverse-of-applyOrder teardown order"
```

- [x] Task 10 complete
