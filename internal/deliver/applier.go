// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package deliver

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// Applier abstracts writing/deleting a single manifest on a target cluster. ownedBy is
// the caller's per-CR ownership value (manifest.OwnedByValue(cr.Namespace, cr.Name)),
// passed as an argument rather than held on the applier so the applier stays stateless
// and one instance is safely reused across concurrent reconciles of different CRs.
type Applier interface {
	Apply(ctx context.Context, m manifest.Manifest, ownedBy string) (ddov1alpha1.ResourceStatus, error)
	Delete(ctx context.Context, m manifest.Manifest, ownedBy string) error
	Get(ctx context.Context, m manifest.Manifest) (*unstructured.Unstructured, error)
}

// SSAApplier is a stateless server-side-apply Applier. It holds only the client, field
// manager, and cluster label — NO per-reconcile mutable state — so a single instance is
// reused across reconciles (and concurrent CRs) for the same cluster. The per-CR
// ownership value is passed to Apply as an argument, not stored on the struct.
type SSAApplier struct {
	Client       client.Client
	FieldManager string
	Cluster      string // "seed" or "shoot", for logging
}

var _ Applier = (*SSAApplier)(nil)

func (a *SSAApplier) Apply(ctx context.Context, m manifest.Manifest, ownedBy string) (ddov1alpha1.ResourceStatus, error) {
	m.StripInternalAnnotations()
	m.SetOwnedByLabel(ownedBy)
	prepareForApply(m.Unstructured)

	u := m.Unstructured
	status := ddov1alpha1.ResourceStatus{
		Kind: u.GetKind(), APIVersion: u.GetAPIVersion(),
		Namespace: u.GetNamespace(), Name: u.GetName(),
		LastApplied: &metav1.Time{Time: time.Now()},
	}

	// Defensive cluster-scoped conflict guard (cluster-scoped kinds only).
	if manifest.IsClusterScoped(u.GetKind()) {
		owner, conflict, err := a.foreignOwner(ctx, u, ownedBy)
		if err != nil {
			status.Health = ddov1alpha1.HealthDegraded
			status.Message = fmt.Sprintf("cluster-scoped conflict pre-check failed for %s %q: %v", u.GetKind(), u.GetName(), err)
			return status, err
		}
		if conflict {
			status.Health = ddov1alpha1.HealthDegraded
			status.Message = fmt.Sprintf("cluster-scoped %s %q already owned by CR %q; refusing to overwrite (single-install-per-seed)", u.GetKind(), u.GetName(), owner)
			return status, fmt.Errorf("%s", status.Message)
		}
	}

	//nolint:staticcheck // SA1019: client.Apply replacement unavailable for unstructured SSA in controller-runtime v0.24.1
	if err := a.Client.Patch(ctx, u, client.Apply, client.FieldOwner(a.FieldManager), client.ForceOwnership); err != nil {
		status.Health = ddov1alpha1.HealthDegraded
		status.Message = err.Error()
		return status, err
	}

	live, err := a.Get(ctx, m)
	if err != nil {
		status.Health = ddov1alpha1.HealthUnknown
		status.Message = fmt.Sprintf("apply succeeded but read-back GET failed: %v", err)
		return status, nil
	}
	status.Health = computeHealth(live)
	return status, nil
}

func (a *SSAApplier) Delete(ctx context.Context, m manifest.Manifest, ownedBy string) error {
	live, err := a.Get(ctx, m)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if live.GetLabels()[manifest.OwnedByLabel] != ownedBy {
		return nil
	}
	if err := a.Client.Delete(ctx, m.Unstructured); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// Get returns the live object from the cluster. Returns (*unstructured.Unstructured, error)
// so callers can distinguish NotFound (apierrors.IsNotFound) from other errors, unlike the
// unexported get which swallows errors.
func (a *SSAApplier) Get(ctx context.Context, m manifest.Manifest) (*unstructured.Unstructured, error) {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(m.Unstructured.GroupVersionKind())
	if err := a.Client.Get(ctx, client.ObjectKeyFromObject(m.Unstructured), live); err != nil {
		return nil, err
	}
	return live, nil
}

// foreignOwner reports whether the live cluster-scoped object is owned by a CR OTHER
// than ownedBy. A NotFound object is absent (safe to apply). Any other GET error is
// returned so the caller fails safe rather than clobbering a possibly foreign-owned
// object under ForceOwnership.
func (a *SSAApplier) foreignOwner(ctx context.Context, u *unstructured.Unstructured, ownedBy string) (owner string, conflict bool, err error) {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(u.GroupVersionKind())
	if getErr := a.Client.Get(ctx, client.ObjectKeyFromObject(u), live); getErr != nil {
		if apierrors.IsNotFound(getErr) {
			return "", false, nil
		}
		return "", false, getErr
	}
	owner = live.GetLabels()[manifest.OwnedByLabel]
	return owner, owner != "" && owner != ownedBy, nil
}
