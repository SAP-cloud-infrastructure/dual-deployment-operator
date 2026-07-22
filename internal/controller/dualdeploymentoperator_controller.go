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
	"k8s.io/client-go/tools/record"
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
	// FinalizerName is the finalizer added on first reconcile to guard remote teardown.
	FinalizerName = "dual-deployment-operator.cc.sap/finalizer"

	// FieldManagerName is the SSA field manager used for all applies.
	FieldManagerName = "dual-deployment-operator"

	requeueInterval = 10 * time.Minute
)

// DualDeploymentOperatorReconciler reconciles a DualDeploymentOperator object.
type DualDeploymentOperatorReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	HostApplier deliver.Applier
	Recorder    record.EventRecorder
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
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

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
	src, err := source.From(cr.Spec.Source, r.SourceDeps)
	if err != nil {
		return r.errStatus(ctx, cr, "InvalidSource", err)
	}

	// 2. Render TWICE — once per mode.
	hostManifests, err := src.Render(ctx, source.ModeHost, cr.Namespace)
	if err != nil {
		return r.errStatus(ctx, cr, "HostRenderFailed", err)
	}

	remoteManifests, err := src.Render(ctx, source.ModeRemote, cr.Spec.RemoteNamespace)
	if err != nil {
		return r.errStatus(ctx, cr, "RemoteRenderFailed", err)
	}

	// 3. Build the ordered transformation list.
	transforms, err := transform.Build(cr.Spec.Transformations)
	if err != nil {
		return r.errStatus(ctx, cr, "InvalidTransformation", err)
	}

	// 4. Apply each transformation to both renders independently, in declaration order.
	for _, t := range transforms {
		hostManifests, err = t.Apply(hostManifests)
		if err != nil {
			return r.errStatus(ctx, cr, "HostTransformFailed", err)
		}
		remoteManifests, err = t.Apply(remoteManifests)
		if err != nil {
			return r.errStatus(ctx, cr, "RemoteTransformFailed", err)
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
	hostManifests = deliver.SortForApply(hostManifests)
	remoteManifests = deliver.SortForApply(remoteManifests)

	// 7. Derive the ownership value once; thread it through every apply call.
	ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

	// Preserve prior remote status by default (not overwritten if remote is skipped).
	var hostStatuses, remoteStatuses []ddov1alpha1.ResourceStatus
	remoteStatuses = cr.Status.RemoteResources

	remoteFirst := cr.Spec.ApplyOrder != "HostFirst"

	prevHostResources := cr.Status.HostResources
	prevRemoteResources := cr.Status.RemoteResources

	var applyErrs []error

	applyHost := func() {
		var err error
		hostStatuses, err = r.applyAll(ctx, r.HostApplier, hostManifests, ownedBy)
		if err != nil {
			applyErrs = append(applyErrs, err)
		}
	}

	pruneHost := func() error {
		retained, err := r.prune(ctx, r.HostApplier, prevHostResources, hostManifests, cr, ownedBy)
		hostStatuses = append(hostStatuses, retained...)
		return err
	}

	switch shootPhase {
	case "credsNotReady":
		if !remoteFirst {
			applyHost() // HostFirst: host does not wait on remote
			if err := pruneHost(); err != nil {
				logger.Error(err, "Failed to prune host orphans")
			}
		}
		r.setCondition(cr, "WaitingForShootCredentials",
			"shoot token/CA not yet populated by Gardener; remote render deferred")
		return r.finishNotReady(ctx, cr, hostStatuses, remoteStatuses, 30*time.Second)

	case "clientFailed":
		if !remoteFirst {
			applyHost() // HostFirst: host proceeds; remote failure only flagged
			if err := pruneHost(); err != nil {
				logger.Error(err, "Failed to prune host orphans")
			}
		}
		r.setCondition(cr, "RemoteApplyFailed",
			fmt.Sprintf("remote render could not be applied: %v", shootErr))
		return r.finishNotReady(ctx, cr, hostStatuses, remoteStatuses, 0)
	}

	// shootPhase == "ready": apply the remote render, then gate host per applyOrder.
	if remoteFirst {
		var remoteErr error
		remoteStatuses, remoteErr = r.applyAll(ctx, shootApplier, remoteManifests, ownedBy)
		if remoteErr != nil {
			applyErrs = append(applyErrs, remoteErr)
		}
		if anyFailed(remoteStatuses) {
			reason := "ResourcesDegraded"
			msg := "some remote resources failed to apply; host render deferred until remote converges"
			if allFailed(remoteStatuses) {
				reason = "RemoteApplyFailed"
				msg = "every resource in the remote render failed to apply"
			}
			r.setCondition(cr, reason, msg)
			return r.finishNotReady(ctx, cr, hostStatuses, remoteStatuses, 0)
		}
		applyHost()
	} else {
		applyHost()
		var remoteErr error
		remoteStatuses, remoteErr = r.applyAll(ctx, shootApplier, remoteManifests, ownedBy)
		if remoteErr != nil {
			applyErrs = append(applyErrs, remoteErr)
		}
	}

	// 8. Prune orphans in reverse of apply order, only for renders applied this cycle.
	var pruneErrs []error
	pruneRemote := func() error {
		retained, err := r.prune(ctx, shootApplier, prevRemoteResources, remoteManifests, cr, ownedBy)
		remoteStatuses = append(remoteStatuses, retained...)
		return err
	}
	if remoteFirst {
		pruneErrs = append(pruneErrs, pruneHost())
		pruneErrs = append(pruneErrs, pruneRemote())
	} else {
		pruneErrs = append(pruneErrs, pruneRemote())
		pruneErrs = append(pruneErrs, pruneHost())
	}
	pruneErr := kerrors.NewAggregate(pruneErrs)

	// 9. Write status.
	cr.Status.HostResources = hostStatuses
	cr.Status.RemoteResources = remoteStatuses
	cr.Status.Conditions = computeConditions(hostStatuses, remoteStatuses)
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
// Event are set, and the CR is requeued. Host and remote resources are both deleted
// explicitly (the applier sets no owner references).
func (r *DualDeploymentOperatorReconciler) reconcileDelete(ctx context.Context, cr *ddov1alpha1.DualDeploymentOperator) (ctrl.Result, error) {
	shootApplier, err := r.buildShootApplierOrDefault(ctx, cr)
	if err != nil {
		if r.Recorder != nil {
			r.Recorder.Event(cr, corev1.EventTypeWarning, "ShootUnreachable",
				"Shoot API server unreachable during deletion; retaining finalizer and retrying")
		}
		meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "ShootUnreachable",
			Message: fmt.Sprintf("shoot unreachable during deletion: %v", err),
		})
		if uErr := r.Status().Update(ctx, cr); uErr != nil {
			log.FromContext(ctx).Error(uErr, "Failed to update status after ShootUnreachable")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

	deleteRender := func(applier deliver.Applier, statuses []ddov1alpha1.ResourceStatus) []error {
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

	// Tear down in the reverse of spec.applyOrder. Host resources are deleted
	// explicitly (not via owner-reference GC — the applier sets no ownerRefs, and
	// cluster-scoped host resources cannot be owned by a namespaced CR anyway).
	var errs []error
	if cr.Spec.ApplyOrder == "HostFirst" {
		errs = append(errs, deleteRender(shootApplier, cr.Status.RemoteResources)...)
		errs = append(errs, deleteRender(r.HostApplier, cr.Status.HostResources)...)
	} else {
		errs = append(errs, deleteRender(r.HostApplier, cr.Status.HostResources)...)
		errs = append(errs, deleteRender(shootApplier, cr.Status.RemoteResources)...)
	}

	if agg := kerrors.NewAggregate(errs); agg != nil {
		return ctrl.Result{}, agg
	}

	controllerutil.RemoveFinalizer(cr, FinalizerName)
	return ctrl.Result{}, r.Update(ctx, cr)
}

// buildShootApplier constructs a shoot SSAApplier from the Gardener token-requestor
// Secret referenced by cr.Spec.RemoteAccess. Returns errShootCredentialsNotReady
// when the Secret exists but token/CA are not yet populated (benign bootstrap wait).
// A missing Secret is a misconfiguration error.
func (r *DualDeploymentOperatorReconciler) buildShootApplier(ctx context.Context, cr *ddov1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
	ref := cr.Spec.RemoteAccess

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
		Cluster:      "remote",
	}, nil
}

// errShootCredentialsNotReady signals that the shoot-access Secret exists but
// Gardener's token-requestor has not yet populated token/CA (absent or empty).
// This is a benign bootstrap state — NOT a fatal error. The caller maps it to a
// WaitingForShootCredentials condition, skips the remote render this cycle, and requeues.
var errShootCredentialsNotReady = errors.New("shoot credentials not yet populated")

// applyAll calls applier.Apply for every manifest in ms, continues on error,
// aggregates results, and returns the []ResourceStatus for all of them.
func (r *DualDeploymentOperatorReconciler) applyAll(ctx context.Context, applier deliver.Applier, ms []manifest.Manifest, ownedBy string) ([]ddov1alpha1.ResourceStatus, error) {
	out := make([]ddov1alpha1.ResourceStatus, 0, len(ms))
	var errs []error
	for _, m := range ms {
		st, err := applier.Apply(ctx, m, ownedBy)
		if err != nil {
			errs = append(errs, err)
		}
		out = append(out, st)
	}
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

// finishNotReady writes status (host/remote statuses + the already-set condition)
// and requeues. A backoff of 0 means return an error so controller-runtime requeues
// with exponential backoff (used for hard failures); non-zero is a fixed RequeueAfter
// (used for benign waits like WaitingForShootCredentials).
func (r *DualDeploymentOperatorReconciler) finishNotReady(ctx context.Context, cr *ddov1alpha1.DualDeploymentOperator,
	host, remote []ddov1alpha1.ResourceStatus, backoff time.Duration) (ctrl.Result, error) {

	cr.Status.HostResources = host
	cr.Status.RemoteResources = remote
	cr.Status.LastReconcile = &metav1.Time{Time: time.Now()}
	if e := r.Status().Update(ctx, cr); e != nil {
		return ctrl.Result{}, e
	}
	if backoff == 0 {
		return ctrl.Result{}, errors.New("remote render unavailable; requeuing")
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

// computeConditions derives the Ready condition from the aggregated host and remote
// resource statuses. Ready=True only when no resource is Degraded.
func computeConditions(host, remote []ddov1alpha1.ResourceStatus) []metav1.Condition {
	if anyFailed(host) || anyFailed(remote) {
		return []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "ResourcesDegraded",
			Message:            "one or more managed resources are in a Degraded state",
			LastTransitionTime: metav1.Now(),
		}}
	}
	if anyProgressing(host) || anyProgressing(remote) {
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
