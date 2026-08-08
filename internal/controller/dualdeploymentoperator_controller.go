// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/clients"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/deliver"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/transform"
)

const (
	// FinalizerName is the finalizer added on first reconcile to guard shoot teardown.
	FinalizerName = "dual-deployment-operator.cc.sap/finalizer"

	// FieldManagerName is the SSA field manager used for all applies.
	FieldManagerName = "dual-deployment-operator"

	requeueInterval = 10 * time.Minute

	// ForceDeleteAnnotation, when set to "true" on a CR being deleted, tells the
	// reconciler to remove the finalizer even though shoot cleanup is incomplete —
	// an explicit, auditable operator consent to orphaning remaining shoot resources.
	// It is the supported alternative to a raw `kubectl patch ... finalizers:[]`.
	ForceDeleteAnnotation = "dual-deployment-operator.cc.sap/force-delete"
)

// DualDeploymentOperatorReconciler reconciles a DualDeploymentOperator object.
type DualDeploymentOperatorReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	SeedApplier deliver.Applier
	Recorder    events.EventRecorder
	SourceDeps  source.Deps

	// shootApplierFor builds the shoot applier for a CR. Defaults to buildShootApplier;
	// overridable in tests to inject a fake shoot applier without a real shoot cluster.
	shootApplierFor func(ctx context.Context, cr *ddov1alpha1.DualDeploymentOperator) (deliver.Applier, error)
}

func (r *DualDeploymentOperatorReconciler) buildShootApplierOrDefault(ctx context.Context, cr *ddov1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
	if r.shootApplierFor != nil {
		return r.shootApplierFor(ctx, cr)
	}
	return r.buildShootApplier(ctx, cr)
}

// +kubebuilder:rbac:groups=dual-deployment-operator.cc.sap,resources=dualdeploymentoperators,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=dual-deployment-operator.cc.sap,resources=dualdeploymentoperators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dual-deployment-operator.cc.sap,resources=dualdeploymentoperators/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Broad seed-applier grant: the operator applies each seed render via SSA, so its
// ServiceAccount must manage the seed-render kinds. secrets are included because a
// seed render can carry Secrets (e.g. the metal-operator-remote-kubeconfig
// token-requestor shell that shootAccess reads, and macdb). bind;escalate on RBAC are
// required because the render itself creates Roles/ClusterRoles (Kubernetes
// privilege-escalation prevention). See design.md 3.6.7 / controller-chart-rbac spec.
// +kubebuilder:rbac:groups="",resources=serviceaccounts;configmaps;services;secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets;daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies;ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete;bind;escalate
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations;mutatingwebhookconfigurations,verbs=get;list;watch;create;update;patch;delete

func (r *DualDeploymentOperatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	cr := &ddov1alpha1.DualDeploymentOperator{}
	if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion.
	if !cr.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, cr)
	}

	// Add finalizer on first reconcile.
	if !controllerutil.ContainsFinalizer(cr, FinalizerName) {
		controllerutil.AddFinalizer(cr, FinalizerName)
		return ctrl.Result{}, r.Update(ctx, cr)
	}

	logger.Info("Starting reconciliation", "name", cr.Name, "namespace", cr.Namespace)

	// 1. Build source renderer.
	deps := r.SourceDeps
	deps.CredentialResolver = &source.CredentialResolver{Client: r.Client, Namespace: cr.Namespace}
	src, err := source.From(cr.Spec.Source, deps)
	if err != nil {
		return r.errStatus(ctx, cr, "InvalidSource", err)
	}

	// 2. Render TWICE — once per mode.
	seedManifests, err := src.Render(ctx, source.ModeSeed, cr.Namespace)
	if err != nil {
		return r.errStatus(ctx, cr, "SeedRenderFailed", err)
	}

	shootManifests, err := src.Render(ctx, source.ModeShoot, cr.Spec.ShootNamespace)
	if err != nil {
		return r.errStatus(ctx, cr, "ShootRenderFailed", err)
	}

	// 3. Build the ordered transformation list.
	transforms, err := transform.Build(cr.Spec.Transformations)
	if err != nil {
		return r.errStatus(ctx, cr, "InvalidTransformation", err)
	}

	// 4. Apply each transformation to both renders independently, in declaration order.
	for _, t := range transforms {
		seedManifests, err = t.Apply(seedManifests)
		if err != nil {
			return r.errStatus(ctx, cr, "SeedTransformFailed", err)
		}
		shootManifests, err = t.Apply(shootManifests)
		if err != nil {
			return r.errStatus(ctx, cr, "ShootTransformFailed", err)
		}
	}

	// 5. Build shoot applier (three-state: ready / credsNotReady / clientFailed).
	shootApplier, shootErr := r.buildShootApplierOrDefault(ctx, cr)
	var shootPhase string
	switch {
	case shootErr == nil:
		shootPhase = "ready"
	case errors.Is(shootErr, errShootCredentialsNotReady):
		shootPhase = "credsNotReady"
	default:
		shootPhase = "clientFailed"
	}

	// 6. Sort each render by the fixed intra-render kind priority.
	seedManifests = deliver.SortForApply(seedManifests)
	shootManifests = deliver.SortForApply(shootManifests)

	// 7. Derive the ownership value once; thread it through every apply call.
	ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

	// Preserve prior shoot status by default (not overwritten if shoot is skipped).
	var seedStatuses, shootStatuses []ddov1alpha1.ResourceStatus
	shootStatuses = cr.Status.ShootResources

	shootFirst := cr.Spec.ApplyOrder != "SeedFirst"

	prevSeedResources := cr.Status.SeedResources
	prevShootResources := cr.Status.ShootResources

	var applyErrs []error

	applySeed := func() {
		var err error
		seedStatuses, err = r.applyAll(ctx, r.SeedApplier, seedManifests, ownedBy)
		if err != nil {
			applyErrs = append(applyErrs, err)
		}
	}

	pruneSeed := func() error {
		retained, err := r.prune(ctx, r.SeedApplier, prevSeedResources, seedManifests, cr, ownedBy)
		seedStatuses = append(seedStatuses, retained...)
		return err
	}

	switch shootPhase {
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
	}

	// shootPhase == "ready": apply the shoot render, then gate seed per applyOrder.
	if shootFirst {
		var shootErr error
		shootStatuses, shootErr = r.applyAll(ctx, shootApplier, shootManifests, ownedBy)
		if shootErr != nil {
			applyErrs = append(applyErrs, shootErr)
		}
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
		applySeed()
	} else {
		applySeed()
		var shootErr error
		shootStatuses, shootErr = r.applyAll(ctx, shootApplier, shootManifests, ownedBy)
		if shootErr != nil {
			applyErrs = append(applyErrs, shootErr)
		}
	}

	// 8. Prune orphans in reverse of apply order, only for renders applied this cycle.
	var pruneErrs []error
	pruneShoot := func() error {
		retained, err := r.prune(ctx, shootApplier, prevShootResources, shootManifests, cr, ownedBy)
		shootStatuses = append(shootStatuses, retained...)
		return err
	}
	if shootFirst {
		pruneErrs = append(pruneErrs, pruneSeed())
		pruneErrs = append(pruneErrs, pruneShoot())
	} else {
		pruneErrs = append(pruneErrs, pruneShoot())
		pruneErrs = append(pruneErrs, pruneSeed())
	}
	pruneErr := kerrors.NewAggregate(pruneErrs)

	// 9. Write status.
	cr.Status.SeedResources = seedStatuses
	cr.Status.ShootResources = shootStatuses
	cr.Status.Conditions = computeConditions(seedStatuses, shootStatuses)
	cr.Status.LastReconcile = &metav1.Time{Time: time.Now()}
	if err := r.Status().Update(ctx, cr); err != nil {
		return ctrl.Result{}, err
	}

	if reconcileErr := kerrors.NewAggregate(append(applyErrs, pruneErr)); reconcileErr != nil {
		return ctrl.Result{}, reconcileErr
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// reconcileDelete tears down both renders in the reverse of spec.applyOrder, retaining
// the finalizer until all deletes succeed. If the shoot is unreachable (any error
// building the shoot client), the finalizer is kept, a ShootUnreachable condition and
// Event are set, and the CR is requeued. Seed and shoot resources are both deleted
// explicitly (the applier sets no owner references).
func (r *DualDeploymentOperatorReconciler) reconcileDelete(ctx context.Context, cr *ddov1alpha1.DualDeploymentOperator) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

	shootApplier, buildErr := r.buildShootApplierOrDefault(ctx, cr)
	if buildErr != nil {
		r.recordShootUnreachable(cr, buildErr)
	}

	deleteRender := func(applier deliver.Applier, statuses []ddov1alpha1.ResourceStatus) []error {
		if applier == nil {
			return nil
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

	var shootErrs, seedErrs []error
	if cr.Spec.ApplyOrder == "SeedFirst" {
		shootErrs = deleteRender(shootApplier, cr.Status.ShootResources)
		seedErrs = deleteRender(r.SeedApplier, cr.Status.SeedResources)
	} else {
		seedErrs = deleteRender(r.SeedApplier, cr.Status.SeedResources)
		shootErrs = deleteRender(shootApplier, cr.Status.ShootResources)
	}

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

// buildShootApplier constructs a shoot SSAApplier from the Gardener token-requestor
// Secret referenced by cr.Spec.ShootAccess. Returns errShootCredentialsNotReady
// when the Secret exists but token/CA are not yet populated (benign bootstrap wait).
// A missing Secret is a misconfiguration error.
func (r *DualDeploymentOperatorReconciler) buildShootApplier(ctx context.Context, cr *ddov1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
	ref := cr.Spec.ShootAccess

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: ref.SecretName}, secret); err != nil {
		return nil, fmt.Errorf("failed to get shoot access secret: %w", err)
	}

	restCfg, err := clients.ShootRESTConfig(secret, ref.TokenKey, ref.CAKey, ref.Server)
	if errors.Is(err, clients.ErrShootCredentialsNotReady) {
		return nil, errShootCredentialsNotReady
	}
	if err != nil {
		return nil, err
	}

	shootClient, err := client.New(restCfg, client.Options{Scheme: r.Scheme})
	if err != nil {
		return nil, err
	}

	return &deliver.SSAApplier{
		Client:       shootClient,
		FieldManager: FieldManagerName,
		Cluster:      "shoot",
	}, nil
}

// errShootCredentialsNotReady signals that the shoot-access Secret exists but
// Gardener's token-requestor has not yet populated token/CA (absent or empty).
// This is a benign bootstrap state — NOT a fatal error. The caller maps it to a
// WaitingForShootCredentials condition, skips the shoot render this cycle, and requeues.
var errShootCredentialsNotReady = errors.New("shoot credentials not yet populated")

// applyAll calls applier.Apply for every manifest in ms, continues on error,
// aggregates results, and returns the []ResourceStatus for all of them.
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

// setCondition sets the Ready condition on cr (does not write to the API server;
// the caller's status update persists it).
func (r *DualDeploymentOperatorReconciler) setCondition(cr *ddov1alpha1.DualDeploymentOperator, reason, message string) {
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}

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

// errStatus sets a Ready=False condition with the given reason and returns a
// requeue-with-backoff result (controller-runtime exponential backoff via returned error).
func (r *DualDeploymentOperatorReconciler) errStatus(ctx context.Context, cr *ddov1alpha1.DualDeploymentOperator, reason string, err error) (ctrl.Result, error) {
	r.setCondition(cr, reason, err.Error())
	cr.Status.LastReconcile = &metav1.Time{Time: time.Now()}
	if uErr := r.Status().Update(ctx, cr); uErr != nil {
		log.FromContext(ctx).Error(uErr, "Failed to update status", "reason", reason)
	}
	return ctrl.Result{}, err
}

// finishNotReady writes status (seed/shoot statuses + the already-set condition)
// and requeues. A backoff of 0 means return an error so controller-runtime requeues
// with exponential backoff (used for hard failures); non-zero is a fixed RequeueAfter
// (used for benign waits like WaitingForShootCredentials).
func (r *DualDeploymentOperatorReconciler) finishNotReady(ctx context.Context, cr *ddov1alpha1.DualDeploymentOperator,
	seed, shoot []ddov1alpha1.ResourceStatus, backoff time.Duration) (ctrl.Result, error) {

	cr.Status.SeedResources = seed
	cr.Status.ShootResources = shoot
	cr.Status.LastReconcile = &metav1.Time{Time: time.Now()}
	if e := r.Status().Update(ctx, cr); e != nil {
		return ctrl.Result{}, e
	}
	if backoff == 0 {
		return ctrl.Result{}, errors.New("shoot render unavailable; requeuing")
	}
	return ctrl.Result{RequeueAfter: backoff}, nil
}

// anyFailed reports whether any ResourceStatus in the slice has Health=Degraded.
func anyFailed(statuses []ddov1alpha1.ResourceStatus) bool {
	for _, s := range statuses {
		if s.Health == ddov1alpha1.HealthDegraded {
			return true
		}
	}
	return false
}

// allFailed reports whether every ResourceStatus in the slice has Health=Degraded
// (i.e. zero of N applied successfully).
func allFailed(statuses []ddov1alpha1.ResourceStatus) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if s.Health != ddov1alpha1.HealthDegraded {
			return false
		}
	}
	return true
}

// computeConditions derives the Ready condition from the aggregated seed and shoot
// resource statuses. Ready=True only when no resource is Degraded.
func computeConditions(seed, shoot []ddov1alpha1.ResourceStatus) []metav1.Condition {
	if anyFailed(seed) || anyFailed(shoot) {
		return []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "ResourcesDegraded",
			Message:            "one or more managed resources are in a Degraded state",
			LastTransitionTime: metav1.Now(),
		}}
	}
	if anyProgressing(seed) || anyProgressing(shoot) {
		return []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "Progressing",
			Message:            "one or more managed resources are still Progressing or Unknown",
			LastTransitionTime: metav1.Now(),
		}}
	}
	return []metav1.Condition{{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "ReconcileSuccess",
		Message:            "all managed resources applied successfully",
		LastTransitionTime: metav1.Now(),
	}}
}

func anyProgressing(statuses []ddov1alpha1.ResourceStatus) bool {
	for _, s := range statuses {
		if s.Health == ddov1alpha1.HealthProgressing || s.Health == ddov1alpha1.HealthUnknown {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *DualDeploymentOperatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ddov1alpha1.DualDeploymentOperator{}).
		Named("dualdeploymentoperator").
		Complete(r)
}

// prune deletes resources that were in prev (last applied set) but are absent from
// current (this reconcile's render). Identity is version-independent (group/kind/ns/name).
// CRDs are retained when retentionPolicy.crds != "Delete". Objects not owned by ownedBy
// are skipped. Errors are aggregated; status is written by the caller after prune so a
// failed delete is retried next reconcile.
func (r *DualDeploymentOperatorReconciler) prune(
	ctx context.Context,
	applier deliver.Applier,
	prev []ddov1alpha1.ResourceStatus,
	current []manifest.Manifest,
	cr *ddov1alpha1.DualDeploymentOperator,
	ownedBy string,
) ([]ddov1alpha1.ResourceStatus, error) {

	currentKeys := make(map[string]struct{}, len(current))
	for _, m := range current {
		currentKeys[deliver.ManifestIdentityKey(m)] = struct{}{}
	}

	var errs []error
	var retained []ddov1alpha1.ResourceStatus
	for _, rs := range deliver.SortStatusForDelete(prev) {
		if _, inCurrent := currentKeys[deliver.StatusIdentityKey(rs)]; inCurrent {
			continue
		}
		if rs.Kind == "CustomResourceDefinition" && cr.Spec.RetentionPolicy.CRDs != "Delete" {
			continue
		}
		m := deliver.ManifestFromStatus(rs)
		live, err := applier.Get(ctx, m)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			errs = append(errs, err)
			retained = append(retained, rs)
			continue
		}
		if live.GetLabels()[manifest.OwnedByLabel] != ownedBy {
			continue
		}
		if err := applier.Delete(ctx, m, ownedBy); err != nil {
			errs = append(errs, err)
			retained = append(retained, rs)
		}
	}
	return retained, kerrors.NewAggregate(errs)
}
