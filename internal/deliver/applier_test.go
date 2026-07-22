// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package deliver

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
)

// clusterRole builds a test ClusterRole. ownedBy is an OPAQUE ownership value (in
// production it is manifest.OwnedByValue(ns,name), a hash) — these tests only need two
// distinct owner strings, so "owner-a"/"owner-b" stand in as opaque identities; the
// applier compares them as strings and never parses or hashes them.
func clusterRole(name, ownedBy string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"})
	u.SetName(name)
	if ownedBy != "" {
		u.SetLabels(map[string]string{manifest.OwnedByLabel: ownedBy})
	}
	return u
}

func TestApplyStampsOwnedByAndReturnsStatus(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	a := &SSAApplier{Client: c, FieldManager: "dual-deployment-operator", Cluster: "seed"}
	m := manifest.Manifest{Unstructured: clusterRole("cr", "")}
	st, err := a.Apply(context.Background(), m, "owner-a")
	if err != nil {
		t.Fatalf("Apply err = %v", err)
	}
	if st.Kind != "ClusterRole" || st.Name != "cr" {
		t.Errorf("status = %+v", st)
	}
}

func TestApplyRefusesForeignOwnedClusterScoped(t *testing.T) {
	existing := clusterRole("cr", "owner-b") // owned by a DIFFERENT CR
	c := fake.NewClientBuilder().WithObjects(existing).Build()
	a := &SSAApplier{Client: c, FieldManager: "dual-deployment-operator", Cluster: "seed"}
	m := manifest.Manifest{Unstructured: clusterRole("cr", "")}
	st, err := a.Apply(context.Background(), m, "owner-a")
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if st.Health != ddov1alpha1.HealthDegraded {
		t.Errorf("health = %q, want Degraded", st.Health)
	}
}

func TestDeleteIgnoresNotFound(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	a := &SSAApplier{Client: c, FieldManager: "dual-deployment-operator", Cluster: "seed"}
	if err := a.Delete(context.Background(), manifest.Manifest{Unstructured: clusterRole("gone", "")}, "owner-a"); err != nil {
		t.Errorf("Delete of absent object = %v, want nil", err)
	}
}

func TestDeleteSkipsForeignOwned(t *testing.T) {
	existing := clusterRole("cr", "owner-b") // owned by a DIFFERENT CR
	c := fake.NewClientBuilder().WithObjects(existing).Build()
	a := &SSAApplier{Client: c, FieldManager: "dual-deployment-operator", Cluster: "seed"}
	m := manifest.Manifest{Unstructured: clusterRole("cr", "")}
	if err := a.Delete(context.Background(), m, "owner-a"); err != nil {
		t.Fatalf("Delete = %v, want nil (foreign-owned skip is not an error)", err)
	}
	if _, err := a.Get(context.Background(), m); err != nil {
		t.Fatalf("foreign-owned object must be retained, but Get failed: %v", err)
	}
}

func TestDeleteRemovesOwned(t *testing.T) {
	existing := clusterRole("cr", "owner-a")
	c := fake.NewClientBuilder().WithObjects(existing).Build()
	a := &SSAApplier{Client: c, FieldManager: "dual-deployment-operator", Cluster: "seed"}
	m := manifest.Manifest{Unstructured: clusterRole("cr", "")}
	if err := a.Delete(context.Background(), m, "owner-a"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}
	if _, err := a.Get(context.Background(), m); err == nil {
		t.Fatal("owned object must be deleted, but it still exists")
	}
}

func TestDeleteSkipsUnlabeled(t *testing.T) {
	existing := clusterRole("cr", "") // no owned-by label
	c := fake.NewClientBuilder().WithObjects(existing).Build()
	a := &SSAApplier{Client: c, FieldManager: "dual-deployment-operator", Cluster: "seed"}
	m := manifest.Manifest{Unstructured: clusterRole("cr", "")}
	if err := a.Delete(context.Background(), m, "owner-a"); err != nil {
		t.Fatalf("Delete = %v, want nil (unlabeled skip is not an error)", err)
	}
	if _, err := a.Get(context.Background(), m); err != nil {
		t.Fatalf("unlabeled object must be retained, but Get failed: %v", err)
	}
}

func TestGetReturnsNotFoundError(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	a := &SSAApplier{Client: c, FieldManager: "dual-deployment-operator", Cluster: "seed"}
	_, err := a.Get(context.Background(), manifest.Manifest{Unstructured: clusterRole("absent", "")})
	if err == nil {
		t.Fatal("Get of absent object should return error")
	}
}

func TestGetReturnsLiveObject(t *testing.T) {
	existing := clusterRole("cr", "owner-a")
	c := fake.NewClientBuilder().WithObjects(existing).Build()
	a := &SSAApplier{Client: c, FieldManager: "dual-deployment-operator", Cluster: "seed"}
	live, err := a.Get(context.Background(), manifest.Manifest{Unstructured: clusterRole("cr", "")})
	if err != nil {
		t.Fatalf("Get = %v, want nil err", err)
	}
	if live.GetName() != "cr" {
		t.Errorf("live.Name = %q, want cr", live.GetName())
	}
}
