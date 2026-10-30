// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// noMatchApplier is a stub deliver.Applier whose Get always returns a REST no-match
// error (the target cluster has no REST mapping for the orphan's kind — e.g. a
// PrometheusRule orphan on a shoot with no Prometheus-operator CRDs). It records any
// Delete call so the test can assert Delete is never attempted for a kind that cannot
// exist on the target.
type noMatchApplier struct {
	getErr      error
	deleteCalls int
}

func (a *noMatchApplier) Apply(context.Context, manifest.Manifest, string) (ddov1alpha1.ResourceStatus, error) {
	return ddov1alpha1.ResourceStatus{}, nil
}

func (a *noMatchApplier) Delete(context.Context, manifest.Manifest, string) error {
	a.deleteCalls++
	return nil
}

func (a *noMatchApplier) Get(context.Context, manifest.Manifest) (*unstructured.Unstructured, error) {
	return nil, a.getErr
}

// TestPruneDropsOrphanWhenKindHasNoRESTMapping reproduces Gap 3: an orphan whose kind
// no longer exists on the target (no CRD / no REST mapping) must be dropped from status,
// not retained as a degraded error forever. Get returns a *meta.NoKindMatchError, which
// is NOT apierrors.IsNotFound; prune must treat it like NotFound.
func TestPruneDropsOrphanWhenKindHasNoRESTMapping(t *testing.T) {
	noMatch := &meta.NoKindMatchError{
		GroupKind:        schema.GroupKind{Group: "monitoring.coreos.com", Kind: "PrometheusRule"},
		SearchedVersions: []string{"v1"},
	}
	applier := &noMatchApplier{getErr: noMatch}

	r := &DualDeploymentOperatorReconciler{}
	cr := &ddov1alpha1.DualDeploymentOperator{}

	prev := []ddov1alpha1.ResourceStatus{{
		APIVersion: "monitoring.coreos.com/v1",
		Kind:       "PrometheusRule",
		Namespace:  "shoot-ns",
		Name:       "orphan-rule",
	}}

	retained, err := r.prune(context.Background(), applier, prev, nil, cr, "owner")

	if err != nil {
		t.Fatalf("prune returned error for a no-REST-mapping orphan, want nil: %v", err)
	}
	if len(retained) != 0 {
		t.Fatalf("orphan with no REST mapping was retained in status %+v, want it dropped", retained)
	}
	if applier.deleteCalls != 0 {
		t.Fatalf("prune called Delete %d times for a kind with no REST mapping, want 0", applier.deleteCalls)
	}
}

// TestPruneRetainsOrphanOnGenuineGetError guards the fix: a genuine, non-NotFound,
// non-no-match Get error must still be aggregated and the orphan retained for retry.
func TestPruneRetainsOrphanOnGenuineGetError(t *testing.T) {
	applier := &noMatchApplier{getErr: &apiStatusError{}}

	r := &DualDeploymentOperatorReconciler{}
	cr := &ddov1alpha1.DualDeploymentOperator{}

	prev := []ddov1alpha1.ResourceStatus{{
		APIVersion: "v1",
		Kind:       "ConfigMap",
		Namespace:  "shoot-ns",
		Name:       "orphan-cm",
	}}

	retained, err := r.prune(context.Background(), applier, prev, nil, cr, "owner")

	if err == nil {
		t.Fatalf("prune swallowed a genuine Get error, want it aggregated")
	}
	if len(retained) != 1 {
		t.Fatalf("orphan dropped on a genuine Get error, want it retained for retry; got %+v", retained)
	}
}

// apiStatusError is a plain error that is neither IsNotFound nor a no-match error.
type apiStatusError struct{}

func (apiStatusError) Error() string { return "internal server error" }
