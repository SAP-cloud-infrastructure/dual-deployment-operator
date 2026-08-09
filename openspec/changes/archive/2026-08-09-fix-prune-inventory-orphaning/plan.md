# Fix Prune Inventory Orphaning — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking. Trailing `- [x]` checkboxes mark task-group completion — check them off AFTER all steps complete.

**Goal:** Make orphan pruning run before every status write on every terminating reconcile path, and preserve prior inventory verbatim for any render not applied this cycle, so a resource that leaves the render is deleted the same cycle instead of being permanently dropped from the status inventory.

**Architecture:** The fix is confined to `Reconcile` in [`internal/controller/dualdeploymentoperator_controller.go`](../../../internal/controller/dualdeploymentoperator_controller.go). Two early-return paths currently persist a current-render-only inventory before prune runs: (1) the `shootFirst` degraded-shoot return (L227-236) — the shoot render WAS applied, so it must be pruned before status is written; (2) the `credsNotReady`/`clientFailed` + `shootFirst` paths (L197-218) — seed is deferred, so its prior inventory must be preserved (currently emptied). The single invariant: a render applied this cycle is pruned before its status write; a render not applied this cycle keeps its prior inventory verbatim and is never pruned.

**Tech Stack:** Go, controller-runtime, Ginkgo/Gomega BDD tests, envtest (real K8s API + etcd). Test command: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/controller/... -run TestControllers -v` (or `make test`). Lint: `make run-golangci-lint`.

---

## Task 1: Test fake that applies normally but marks one resource Degraded

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller_test.go` (append near the existing `recordingApplier`, after line 711)

The existing `recordingApplier` (L695-711) always returns Healthy. The orphaning bug only triggers when `anyFailed(shootStatuses)` is true on the shoot render, so the RED tests need a fake applier that (a) applies to the real envtest cluster for most resources so prune's `Get`/`Delete` work, and (b) forces `Health=Degraded` for one named resource to trip the degraded early-return. Wrapping a real `SSAApplier` keeps `Get`/`Delete` behavior intact for prune.

- [x] **Step 1: Add the `degradingApplier` fake wrapping a real SSAApplier**

Append to `internal/controller/dualdeploymentoperator_controller_test.go`:

```go
// degradingApplier delegates to an inner Applier but forces Health=Degraded
// for the resource whose name matches degradeName. Used to exercise the
// ShootFirst degraded early-return path while keeping real Get/Delete behavior
// (so prune can actually delete orphans on the envtest cluster).
type degradingApplier struct {
	inner       deliver.Applier
	degradeName string
}

func (a *degradingApplier) Apply(ctx context.Context, m manifest.Manifest, ownedBy string) (ddov1alpha1.ResourceStatus, error) {
	st, err := a.inner.Apply(ctx, m, ownedBy)
	if m.Unstructured.GetName() == a.degradeName {
		st.Health = ddov1alpha1.HealthDegraded
		st.Message = "forced degraded by test"
	}
	return st, err
}

func (a *degradingApplier) Delete(ctx context.Context, m manifest.Manifest, ownedBy string) error {
	return a.inner.Delete(ctx, m, ownedBy)
}

func (a *degradingApplier) Get(ctx context.Context, m manifest.Manifest) (*unstructured.Unstructured, error) {
	return a.inner.Get(ctx, m)
}
```

- [x] **Step 2: Verify it compiles (no test run yet)**

Run: `go build ./internal/controller/...`
Expected: PASS (compiles; `degradingApplier` unused warning is fine — Go allows unused types).

- [x] **Step 3: Commit**

```bash
git add internal/controller/dualdeploymentoperator_controller_test.go
git commit -m "test(controller): add degradingApplier fake for degraded-shoot path"
```

- [x] Task 1 complete

---

## Task 2: RED test — ShootFirst degraded shoot render orphans a removed resource

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller_test.go` (add an `It` inside the existing `Describe`/`Context` that holds the ready-phase reconcile tests, near L363 after the "ShootFirst success path" test)

This test reproduces the bug: on a ShootFirst reconcile where the shoot render both (a) contains a degraded resource and (b) has had a resource removed since the last cycle, the removed resource must be pruned THIS cycle, not dropped from status. It uses the same envtest-backed `readyReconciler` pattern (L291-303) but swaps the shoot applier for `degradingApplier`, and seeds `cr.Status.ShootResources` with a prior inventory containing an extra resource that is absent from the current render.

The demo chart renders a fixed set; the "removed" resource is one we inject into prior status that the chart does NOT render, so it is an orphan by construction. Its identity must be a kind the shoot applier can `Get`/`Delete` on the envtest cluster (use a `ConfigMap` in the shoot namespace, created directly so prune finds it live and owned).

- [x] **Step 1: Write the failing test**

Add inside the ready-phase `Context` (after the ShootFirst success test at ~L363):

```go
It("prunes a removed shoot resource the same cycle even when the shoot render is degraded (ShootFirst)", func() {
	cr := newCR("test-degraded-prune")
	cr.Spec.ApplyOrder = "ShootFirst"

	// Create an owned orphan ConfigMap in the shoot namespace that the demo
	// chart does NOT render — it must be pruned once it leaves the inventory.
	orphan := &unstructured.Unstructured{}
	orphan.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})
	orphan.SetName("orphan-cm")
	orphan.SetNamespace(cr.Spec.ShootNamespace)
	orphan.SetLabels(map[string]string{manifest.OwnedByLabel: manifest.OwnedByValue(cr.Namespace, cr.Name)})
	Expect(k8sClient.Create(ctx, orphan)).To(Succeed())

	// Reconciler whose shoot applier forces one rendered resource Degraded,
	// so the ShootFirst degraded early-return path is taken.
	r := &DualDeploymentOperatorReconciler{
		Client: k8sClient,
		Scheme: k8sClient.Scheme(),
		SeedApplier: &deliver.SSAApplier{
			Client: k8sClient, FieldManager: FieldManagerName, Cluster: "seed",
		},
		SourceDeps: source.Deps{ChartLoader: envtestFakeChartLoader{dir: demoChartDir}},
		shootApplierFor: func(_ context.Context, _ *ddov1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
			return &degradingApplier{
				inner:       &deliver.SSAApplier{Client: k8sClient, FieldManager: FieldManagerName, Cluster: "shoot"},
				degradeName: "demo", // the demo chart's Deployment name — forces anyFailed()
			}, nil
		},
	}

	// Clear the shared demo CRD so this CR owns a fresh render.
	staleCRD := &unstructured.Unstructured{}
	staleCRD.SetGroupVersionKind(schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"})
	staleCRD.SetName("demos.demo.cc.sap")
	Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, staleCRD))).To(Succeed())

	Expect(k8sClient.Create(ctx, cr)).To(Succeed())
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}}

	// First reconcile: adds finalizer.
	_, _ = r.Reconcile(ctx, req)

	// Seed prior status with the orphan so it is in prevShootResources.
	got := &ddov1alpha1.DualDeploymentOperator{}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
	got.Status.ShootResources = []ddov1alpha1.ResourceStatus{
		{Kind: "ConfigMap", APIVersion: "v1", Namespace: cr.Spec.ShootNamespace, Name: "orphan-cm"},
	}
	Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())

	// Reconcile: shoot render is degraded (demo Deployment) AND orphan-cm left
	// the render. The bug: reconcile returns before prune, persists a status
	// without orphan-cm, and never deletes it. The fix: prune runs first.
	_, _ = r.Reconcile(ctx, req)

	// Assert the orphan was actually deleted from the cluster this cycle.
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})
	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: cr.Spec.ShootNamespace, Name: "orphan-cm"}, live)
		return apierrors.IsNotFound(err)
	}, 5*time.Second, 200*time.Millisecond).Should(BeTrue(), "orphan-cm must be pruned the same cycle despite degraded shoot render")
})
```

- [x] **Step 2: Run the test to verify it FAILS**

Run: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/controller/... -run TestControllers -v 2>&1 | grep -A5 "same cycle even when"`
Expected: FAIL — `orphan-cm` is still present (NotFound never true) because the degraded early-return at L235 persists status and returns before `pruneShoot()` runs.

- [x] **Step 3: Commit the failing test**

```bash
git add internal/controller/dualdeploymentoperator_controller_test.go
git commit -m "test(controller): RED — ShootFirst degraded render orphans removed resource"
```

- [x] Task 2 complete

---

## Task 3: GREEN — prune the degraded shoot render before its status write

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller.go:220-236` (the `shootFirst` degraded early-return block)

The `pruneShoot` closure is currently defined AFTER this block (L249). Move the prune closures (`pruneSeed` already exists at L189; add `pruneShoot`) so they are defined before the ready-phase branches, then call `pruneShoot()` before `finishNotReady` on the degraded path. Aggregate the prune error into the returned error but keep the degraded condition.

- [x] **Step 1: Define `pruneShoot` before the shootPhase switch**

Move the `pruneShoot` closure definition up. Locate its current definition (L249-254) and relocate it to just after `pruneSeed` (after L194), so both prune closures exist before any branch uses them. The relocated closure (identical body):

```go
	pruneShoot := func() error {
		retained, err := r.prune(ctx, shootApplier, prevShootResources, shootManifests, cr, ownedBy)
		shootStatuses = append(shootStatuses, retained...)
		logger.Info("Pruned orphans", "cluster", "shoot", "retained", len(retained))
		return err
	}
```

Then delete the duplicate definition at the old location (L249-254), leaving the `pruneErrs` accumulation block (L255-262) referencing the now-hoisted closures.

- [x] **Step 2: Prune before the degraded early-return**

Replace the degraded-return block (currently L227-236):

```go
		if anyFailed(shootStatuses) {
			reason := "ResourcesDegraded"
			msg := "some shoot resources failed to apply; seed render deferred until shoot converges"
			if allFailed(shootStatuses) {
				reason = "ShootApplyFailed"
				msg = "every resource in the shoot render failed to apply"
			}
			r.setCondition(cr, reason, msg)
			return r.finishNotReady(ctx, cr, seedStatuses, shootStatuses, 0)
		}
```

with (prune the applied shoot render before persisting its trimmed inventory; seed was NOT applied on this path so its prior inventory is preserved):

```go
		if anyFailed(shootStatuses) {
			reason := "ResourcesDegraded"
			msg := "some shoot resources failed to apply; seed render deferred until shoot converges"
			if allFailed(shootStatuses) {
				reason = "ShootApplyFailed"
				msg = "every resource in the shoot render failed to apply"
			}
			// The shoot render WAS applied this cycle, so prune its orphans before
			// writing status — otherwise a resource that left the render is dropped
			// from the inventory permanently (never re-enters prevShootResources).
			// Seed was NOT applied on this path; prevSeedResources is preserved via
			// seedStatuses below (see finishNotReady preservation).
			if err := pruneShoot(); err != nil {
				logger.Error(err, "Failed to prune shoot orphans on degraded path")
			}
			r.setCondition(cr, reason, msg)
			return r.finishNotReady(ctx, cr, prevSeedResources, shootStatuses, 0)
		}
```

Note: `seedStatuses` is nil here (seed never applied), so pass `prevSeedResources` to preserve the prior seed inventory instead of emptying it. This is the seed-preservation fix folded into the same block.

- [x] **Step 3: Run the Task 2 test to verify it now PASSES**

Run: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/controller/... -run TestControllers -v 2>&1 | grep -A5 "same cycle even when"`
Expected: PASS — `orphan-cm` is deleted this cycle.

- [x] **Step 4: Run the full controller suite to check for regressions**

Run: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/controller/... -run TestControllers`
Expected: PASS (all existing tests still green; the hoisted closure changes nothing for the happy path).

- [x] **Step 5: Commit**

```bash
git add internal/controller/dualdeploymentoperator_controller.go
git commit -m "fix(controller): prune degraded shoot render before status write

On the ShootFirst degraded early-return, the shoot render was applied but
prune ran only in the post-apply section that this path returns before,
so a resource that left the render was persisted out of status and never
pruned (unbounded orphaning). Hoist pruneShoot and call it before
finishNotReady; preserve prevSeedResources (seed not applied on this path)."
```

- [x] Task 3 complete

---

## Task 4: RED test — ShootFirst credsNotReady/clientFailed must preserve seed inventory

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller_test.go` (add an `It` near the existing credsNotReady/clientFailed tests)

On `credsNotReady`/`clientFailed` with `shootFirst`, the `if !shootFirst` guard (L198, L209) skips `applySeed()`, leaving `seedStatuses` nil, and `finishNotReady` persists an empty `SeedResources`, dropping the entire seed inventory. This test seeds a prior `SeedResources` and asserts it survives a credsNotReady reconcile.

- [x] **Step 1: Write the failing test**

Add near the credsNotReady tests (the existing suite already has a credsNotReady reconciler pattern — reuse a reconciler whose `shootApplierFor` returns `errShootCredentialsNotReady`):

```go
It("preserves prior seed inventory on ShootFirst credsNotReady (does not empty it)", func() {
	cr := newCR("test-preserve-seed-creds")
	cr.Spec.ApplyOrder = "ShootFirst"

	r := &DualDeploymentOperatorReconciler{
		Client: k8sClient,
		Scheme: k8sClient.Scheme(),
		SeedApplier: &deliver.SSAApplier{
			Client: k8sClient, FieldManager: FieldManagerName, Cluster: "seed",
		},
		SourceDeps: source.Deps{ChartLoader: envtestFakeChartLoader{dir: demoChartDir}},
		shootApplierFor: func(_ context.Context, _ *ddov1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
			return nil, errShootCredentialsNotReady
		},
	}

	Expect(k8sClient.Create(ctx, cr)).To(Succeed())
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}}
	_, _ = r.Reconcile(ctx, req) // adds finalizer

	// Seed a prior seed inventory.
	got := &ddov1alpha1.DualDeploymentOperator{}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
	got.Status.SeedResources = []ddov1alpha1.ResourceStatus{
		{Kind: "ServiceAccount", APIVersion: "v1", Namespace: cr.Namespace, Name: "prior-sa"},
	}
	Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())

	// Reconcile on credsNotReady + ShootFirst: seed is deferred. Prior seed
	// inventory MUST be preserved, not emptied.
	_, _ = r.Reconcile(ctx, req)

	after := &ddov1alpha1.DualDeploymentOperator{}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), after)).To(Succeed())
	Expect(after.Status.SeedResources).To(HaveLen(1), "prior seed inventory must be preserved on credsNotReady")
	Expect(after.Status.SeedResources[0].Name).To(Equal("prior-sa"))
})
```

- [x] **Step 2: Run the test to verify it FAILS**

Run: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/controller/... -run TestControllers -v 2>&1 | grep -A5 "preserves prior seed inventory"`
Expected: FAIL — `SeedResources` is empty (len 0), because `finishNotReady` at L206 was passed nil `seedStatuses`.

- [x] **Step 3: Commit the failing test**

```bash
git add internal/controller/dualdeploymentoperator_controller_test.go
git commit -m "test(controller): RED — ShootFirst credsNotReady drops seed inventory"
```

- [x] Task 4 complete

---

## Task 5: GREEN — preserve seed inventory on credsNotReady/clientFailed shootFirst paths

**Files:**
- Modify: `internal/controller/dualdeploymentoperator_controller.go:196-218` (the credsNotReady and clientFailed switch cases)

On both paths, when `shootFirst` (seed not applied), pass `prevSeedResources` to `finishNotReady` instead of the nil `seedStatuses`. When `!shootFirst`, seed WAS applied and pruned, so `seedStatuses` is correct — keep it. The simplest correct form: compute the seed argument per branch.

- [x] **Step 1: Fix the credsNotReady case**

Replace (currently L197-206):

```go
	case "credsNotReady":
		if !shootFirst {
			applySeed() // SeedFirst: seed does not wait on shoot
			if err := pruneSeed(); err != nil {
				logger.Error(err, "Failed to prune seed orphans")
			}
		}
		r.setCondition(cr, "WaitingForShootCredentials",
			"shoot token/CA not yet populated by Gardener; shoot render deferred")
		return r.finishNotReady(ctx, cr, seedStatuses, shootStatuses, 30*time.Second)
```

with:

```go
	case "credsNotReady":
		seedOut := prevSeedResources // ShootFirst: seed deferred, preserve prior inventory
		if !shootFirst {
			applySeed() // SeedFirst: seed does not wait on shoot
			if err := pruneSeed(); err != nil {
				logger.Error(err, "Failed to prune seed orphans")
			}
			seedOut = seedStatuses
		}
		r.setCondition(cr, "WaitingForShootCredentials",
			"shoot token/CA not yet populated by Gardener; shoot render deferred")
		return r.finishNotReady(ctx, cr, seedOut, shootStatuses, 30*time.Second)
```

- [x] **Step 2: Fix the clientFailed case**

Replace (currently L208-217):

```go
	case "clientFailed":
		if !shootFirst {
			applySeed() // SeedFirst: seed proceeds; shoot failure only flagged
			if err := pruneSeed(); err != nil {
				logger.Error(err, "Failed to prune seed orphans")
			}
		}
		r.setCondition(cr, "ShootApplyFailed",
			fmt.Sprintf("shoot render could not be applied: %v", shootErr))
		return r.finishNotReady(ctx, cr, seedStatuses, shootStatuses, 0)
```

with:

```go
	case "clientFailed":
		seedOut := prevSeedResources // ShootFirst: seed deferred, preserve prior inventory
		if !shootFirst {
			applySeed() // SeedFirst: seed proceeds; shoot failure only flagged
			if err := pruneSeed(); err != nil {
				logger.Error(err, "Failed to prune seed orphans")
			}
			seedOut = seedStatuses
		}
		r.setCondition(cr, "ShootApplyFailed",
			fmt.Sprintf("shoot render could not be applied: %v", shootErr))
		return r.finishNotReady(ctx, cr, seedOut, shootStatuses, 0)
```

- [x] **Step 3: Run the Task 4 test to verify it now PASSES**

Run: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/controller/... -run TestControllers -v 2>&1 | grep -A5 "preserves prior seed inventory"`
Expected: PASS — `SeedResources` retains `prior-sa`.

- [x] **Step 4: Run the full controller suite**

Run: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./internal/controller/...`
Expected: PASS (all tests green, including the Task 2 degraded-prune test and existing credsNotReady/clientFailed tests).

- [x] **Step 5: Commit**

```bash
git add internal/controller/dualdeploymentoperator_controller.go
git commit -m "fix(controller): preserve seed inventory on ShootFirst credsNotReady/clientFailed

Under ShootFirst on the credsNotReady/clientFailed paths the seed render is
deferred (not applied), but finishNotReady was passed the nil seedStatuses,
emptying status.seedResources and orphaning every seed resource. Pass
prevSeedResources when seed was not applied this cycle; keep the applied
seedStatuses on SeedFirst."
```

- [x] Task 5 complete

---

## Task 6: Full verification — lint, whole test suite, spec re-validate

**Files:** none (verification only)

- [x] **Step 1: Run the full test suite (all packages)**

Run: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.36 -p path) go test ./...`
Expected: PASS across all packages. Note any pre-existing failures unrelated to this change.

- [x] **Step 2: Run the linter**

Run: `make run-golangci-lint`
Expected: no new violations in `dualdeploymentoperator_controller.go` or the test file. Re-verify any "pre-existing" claim against `git diff main -- internal/controller/`.

- [x] **Step 3: Build**

Run: `go build ./...`
Expected: exit 0.

- [x] **Step 4: Re-validate the OpenSpec change (base spec may have shifted after rebase)**

Run: `openspec validate fix-prune-inventory-orphaning`
Expected: `Change 'fix-prune-inventory-orphaning' is valid`. If the MODIFIED delta headers no longer match the base `openspec/specs/reconcile-loop/spec.md` (e.g. reworded by a merged change), re-sync the delta headers to exact matches.

- [x] **Step 5: Confirm no unrelated files changed**

Run: `git diff --stat main -- . ':!openspec'`
Expected: only `internal/controller/dualdeploymentoperator_controller.go` and `internal/controller/dualdeploymentoperator_controller_test.go` in the code diff.

- [x] Task 6 complete

---

## Task 7: Update tasks/progress markers in the OpenSpec change

**Files:**
- This file (`plan.md`) — trailing checkboxes already tracked per task.

- [x] **Step 1: Confirm all task-group checkboxes above are checked**

Verify every `- [x] Task N complete` is now `- [x]`.

- [x] **Step 2: Commit any remaining plan/progress updates**

```bash
git add openspec/changes/fix-prune-inventory-orphaning/plan.md
git commit -m "docs(openspec): mark fix-prune-inventory-orphaning plan tasks complete"
```

- [x] Task 7 complete
