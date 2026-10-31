// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// noMatchApplier records deletion attempts after a failed inventory lookup.
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

// NoMatch is not authoritative absence; preserve inventory for a later retry.
func TestPruneRetainsOrphanWhenKindHasNoRESTMapping(t *testing.T) {
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

	if !errors.Is(err, noMatch) {
		t.Fatalf("prune lost no-match error: %v", err)
	}
	if !reflect.DeepEqual(retained, prev) {
		t.Fatalf("ambiguous absence changed inventory: got %+v, want %+v", retained, prev)
	}
	if applier.deleteCalls != 0 {
		t.Fatalf("prune called Delete %d times for a kind with no REST mapping, want 0", applier.deleteCalls)
	}
}

func TestPruneRetainsOrphanForUnservedVersion(t *testing.T) {
	gv := schema.GroupVersion{Group: "example.com", Version: "v1"}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gv})
	mapper.Add(gv.WithKind("Widget"), meta.RESTScopeNamespace)
	gk := schema.GroupKind{Group: gv.Group, Kind: "Widget"}
	_, lookupErr := mapper.RESTMapping(gk, "v1beta1")
	if !meta.IsNoMatchError(lookupErr) {
		t.Fatalf("expected version-specific NoMatch: %v", lookupErr)
	}
	if mapping, err := mapper.RESTMapping(gk); err != nil || mapping.GroupVersionKind != gv.WithKind("Widget") {
		t.Fatalf("GroupKind must still serve v1: %v, %v", mapping, err)
	}
	a := &noMatchApplier{getErr: lookupErr}
	r := &DualDeploymentOperatorReconciler{}
	prior := []ddov1alpha1.ResourceStatus{{APIVersion: "example.com/v1beta1", Kind: "Widget", Name: "still-stored", Namespace: "ns"}}
	retained, err := r.prune(context.Background(), a, prior, nil, &ddov1alpha1.DualDeploymentOperator{}, "owner")
	if !errors.Is(err, lookupErr) || !reflect.DeepEqual(retained, prior) || a.deleteCalls != 0 {
		t.Fatalf("must retain exact inventory and lookup error without deleting: retained=%+v, err=%v, deletes=%d", retained, err, a.deleteCalls)
	}
}

func TestPruneDropsAuthoritativelyNotFoundOrphan(t *testing.T) {
	a := &noMatchApplier{getErr: apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "gone")}
	r := &DualDeploymentOperatorReconciler{}
	prior := []ddov1alpha1.ResourceStatus{{APIVersion: "v1", Kind: "ConfigMap", Name: "gone", Namespace: "ns"}}
	retained, err := r.prune(context.Background(), a, prior, nil, &ddov1alpha1.DualDeploymentOperator{}, "owner")
	if err != nil || len(retained) != 0 || a.deleteCalls != 0 {
		t.Fatalf("NotFound must remain successful absence: retained=%+v, err=%v, deletes=%d", retained, err, a.deleteCalls)
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
