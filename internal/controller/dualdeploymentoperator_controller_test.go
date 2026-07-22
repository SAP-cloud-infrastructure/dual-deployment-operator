// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ddov1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/deliver"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/manifest"
	"github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source"
)

// ---------------------------------------------------------------------------
// Unit tests (plain testing.T, fake client)
// ---------------------------------------------------------------------------

func newTestReconciler(objs ...client.Object) *DualDeploymentOperatorReconciler {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(ddov1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&ddov1alpha1.DualDeploymentOperator{}).
		Build()
	return &DualDeploymentOperatorReconciler{
		Client:      c,
		Scheme:      scheme,
		HostApplier: &deliver.SSAApplier{Client: c, FieldManager: FieldManagerName, Cluster: "host"},
	}
}

func TestReconcileMissingCRReturnsNoError(t *testing.T) {
	r := newTestReconciler()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "nope", Namespace: "ns"}})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected empty result for missing CR, got %+v", res)
	}
}

// ---------------------------------------------------------------------------
// Envtest (Ginkgo) — Task 8: two-render reconcile populates status
//
// Source strategy: inject a fakeChartLoader (same as internal/source tests)
// via SourceDeps.ChartLoader pointing at the demo testdata chart. The demo
// chart renders a CRD (cluster-scoped) for ModeRemote and a CRD + ConfigMap
// (addition) + optional Deployment for ModeHost. We use ApplyOrder=HostFirst
// and point RemoteAccess at a Secret with an EMPTY token so the shoot phase
// is "credsNotReady" — host applies cleanly, conditions are written, and
// LastReconcile is set. This exercises the full pipeline up to the status
// write without needing a real shoot API server.
//
// Note: fakeChartLoader is defined in fakes_envtest_test.go (same package).
// ---------------------------------------------------------------------------

var _ = Describe("DualDeploymentOperator controller", func() {
	const (
		crNamespace      = "default"
		remoteSecretName = "shoot-access"
		demoChartDir     = "../../internal/source/testdata/charts/demo"
	)

	// newCR builds a valid CR backed by the demo Helm chart.
	newCR := func(name string) *ddov1alpha1.DualDeploymentOperator {
		return &ddov1alpha1.DualDeploymentOperator{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: crNamespace,
			},
			Spec: ddov1alpha1.DualDeploymentOperatorSpec{
				Source: ddov1alpha1.Source{
					Helm: &ddov1alpha1.HelmSource{
						Repo:    "oci://unused",
						Name:    "demo",
						Version: "0.1.0",
					},
				},
				RemoteAccess: ddov1alpha1.RemoteAccessRef{
					SecretName: remoteSecretName,
					Server:     "https://shoot-api.example:6443",
				},
				RemoteNamespace: "kube-system",
				// HostFirst so that even with credsNotReady for the shoot, the
				// host render is applied and status/conditions are populated.
				ApplyOrder: "HostFirst",
			},
		}
	}

	var (
		testReconciler *DualDeploymentOperatorReconciler
		ns             *corev1.Namespace
	)

	BeforeEach(func() {
		// Ensure the test namespace exists in envtest.
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: crNamespace}}
		if err := k8sClient.Create(ctx, ns); client.IgnoreAlreadyExists(err) != nil {
			Expect(err).NotTo(HaveOccurred())
		}

		// Create a shoot-access Secret with EMPTY token — triggers credsNotReady
		// (benign bootstrap wait) so we don't need a real shoot API server.
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      remoteSecretName,
				Namespace: crNamespace,
			},
			Data: map[string][]byte{
				"token":      {}, // deliberately empty
				"bundle.crt": []byte("FAKE-CA"),
			},
		}
		// Delete in case left over from a previous run, then recreate.
		if err := k8sClient.Delete(ctx, secret); client.IgnoreNotFound(err) != nil {
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		// Build a reconciler wired directly against the envtest API server.
		// SourceDeps uses fakeChartLoader (same testdata used by source unit tests).
		testReconciler = &DualDeploymentOperatorReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			HostApplier: &deliver.SSAApplier{
				Client:       k8sClient,
				FieldManager: FieldManagerName,
				Cluster:      "host",
			},
			SourceDeps: source.Deps{
				ChartLoader: envtestFakeChartLoader{dir: demoChartDir},
			},
		}
	})

	It("delivers host render and populates status conditions and LastReconcile", func() {
		cr := newCR("test-deliver")
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())

		// First reconcile: adds the finalizer and returns (no status write yet).
		res, err := testReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
		})
		Expect(err).NotTo(HaveOccurred())
		// After finalizer-add reconcile returns zero result so controller requeues.
		_ = res

		// Fetch updated CR (now has finalizer).
		updated := &ddov1alpha1.DualDeploymentOperator{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), updated)).To(Succeed())
		Expect(updated.Finalizers).To(ContainElement(FinalizerName))

		// Second reconcile: runs the full pipeline.
		_, err = testReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		// Status must be written: conditions non-empty, LastReconcile set.
		Eventually(func(g Gomega) {
			got := &ddov1alpha1.DualDeploymentOperator{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
			g.Expect(got.Status.Conditions).ToNot(BeEmpty(), "status.conditions must be set")
			g.Expect(got.Status.LastReconcile).ToNot(BeNil(), "status.lastReconcile must be set")
		}, 5*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("prunes orphaned host resources and respects retentionPolicy and owned-by guard", func() {
		cr := newCR("test-prune")
		ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

		loader := &switchableChartLoader{pruneRound: false}
		reconciler := &DualDeploymentOperatorReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			HostApplier: &deliver.SSAApplier{
				Client:       k8sClient,
				FieldManager: FieldManagerName,
				Cluster:      "host",
			},
			SourceDeps: source.Deps{ChartLoader: loader},
		}

		foreignCM := &unstructured.Unstructured{}
		foreignCM.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
		foreignCM.SetName("foreign-cm")
		foreignCM.SetNamespace(cr.Namespace)
		foreignCM.SetLabels(map[string]string{manifest.OwnedByLabel: "other-owner-value"})
		Expect(k8sClient.Create(ctx, foreignCM)).To(Succeed())

		Expect(k8sClient.Create(ctx, cr)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		_, err = reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			got := &ddov1alpha1.DualDeploymentOperator{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
			g.Expect(got.Status.HostResources).ToNot(BeEmpty())
		}, 5*time.Second, 100*time.Millisecond).Should(Succeed())

		// Track foreign-cm in status as if it were previously applied, so prune
		// considers it an orphan on the next round. Because the live object carries a
		// DIFFERENT owner label, the owned-by guard must skip the delete.
		Eventually(func(g Gomega) {
			got := &ddov1alpha1.DualDeploymentOperator{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
			got.Status.HostResources = append(got.Status.HostResources, ddov1alpha1.ResourceStatus{
				Kind: "ConfigMap", APIVersion: "v1", Namespace: cr.Namespace, Name: "foreign-cm",
				Health: ddov1alpha1.HealthHealthy,
			})
			g.Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())
		}, 5*time.Second, 100*time.Millisecond).Should(Succeed())

		loader.pruneRound = true

		_, err = reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
		})
		Expect(err).NotTo(HaveOccurred())

		orphanCM := &unstructured.Unstructured{}
		orphanCM.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
		orphanCM.SetName("orphan-cm")
		orphanCM.SetNamespace(cr.Namespace)
		Eventually(func() bool {
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(orphanCM), orphanCM.DeepCopy()) != nil
		}, 5*time.Second, 100*time.Millisecond).Should(BeTrue(), "orphan-cm should be deleted")

		demoCRD := &unstructured.Unstructured{}
		demoCRD.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "apiextensions.k8s.io",
			Version: "v1",
			Kind:    "CustomResourceDefinition",
		})
		demoCRD.SetName("demos.demo.cc.sap")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(demoCRD), demoCRD)).To(Succeed())

		liveForeignCM := &unstructured.Unstructured{}
		liveForeignCM.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
		liveForeignCM.SetName("foreign-cm")
		liveForeignCM.SetNamespace(cr.Namespace)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(liveForeignCM), liveForeignCM)).To(Succeed())

		got := &ddov1alpha1.DualDeploymentOperator{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
		for _, rs := range got.Status.HostResources {
			Expect(rs.Name).NotTo(Equal("orphan-cm"), "orphan-cm must not appear in status after prune")
		}

		_ = ownedBy
	})

	// Ready-phase reconcile: inject a shoot applier backed by the envtest cluster
	// (via shootApplierFor) so the full success path runs — remote apply, host
	// apply, prune, and a Ready=True status — for both apply orders.
	readyReconciler := func() *DualDeploymentOperatorReconciler {
		return &DualDeploymentOperatorReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			HostApplier: &deliver.SSAApplier{
				Client: k8sClient, FieldManager: FieldManagerName, Cluster: "host",
			},
			SourceDeps: source.Deps{ChartLoader: envtestFakeChartLoader{dir: demoChartDir}},
			shootApplierFor: func(_ context.Context, _ *ddov1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
				return &deliver.SSAApplier{Client: k8sClient, FieldManager: FieldManagerName, Cluster: "remote"}, nil
			},
		}
	}

	reconcileTwice := func(r *DualDeploymentOperatorReconciler, cr *ddov1alpha1.DualDeploymentOperator) {
		// The demo chart renders a cluster-scoped CRD (demos.demo.cc.sap) shared
		// across specs; delete it first so this CR becomes its fresh owner and the
		// single-install-per-seed guard does not fire against a prior spec's owner.
		staleCRD := &unstructured.Unstructured{}
		staleCRD.SetGroupVersionKind(schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"})
		staleCRD.SetName("demos.demo.cc.sap")
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, staleCRD))).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(staleCRD), staleCRD.DeepCopy()))
		}, 5*time.Second, 100*time.Millisecond).Should(BeTrue(), "shared demo CRD must be fully deleted before reconcile")

		Expect(k8sClient.Create(ctx, cr)).To(Succeed())
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}}
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
	}

	It("reaches Ready=True on the HostFirst success path", func() {
		cr := newCR("test-ready-hostfirst")
		cr.Spec.ApplyOrder = "HostFirst"
		r := readyReconciler()
		reconcileTwice(r, cr)

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}}
		Eventually(func(g Gomega) {
			_, err := r.Reconcile(ctx, req)
			g.Expect(err).NotTo(HaveOccurred())
			got := &ddov1alpha1.DualDeploymentOperator{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
			cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			g.Expect(cond).ToNot(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(cond.Reason).To(Equal("ReconcileSuccess"))
			g.Expect(got.Status.HostResources).ToNot(BeEmpty())
			g.Expect(got.Status.RemoteResources).ToNot(BeEmpty())
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())
	})

	It("reaches Ready=True on the RemoteFirst success path", func() {
		cr := newCR("test-ready-remotefirst")
		cr.Spec.ApplyOrder = "RemoteFirst"
		r := readyReconciler()
		reconcileTwice(r, cr)

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}}
		Eventually(func(g Gomega) {
			_, err := r.Reconcile(ctx, req)
			g.Expect(err).NotTo(HaveOccurred())
			got := &ddov1alpha1.DualDeploymentOperator{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
			cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			g.Expect(cond).ToNot(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(got.Status.RemoteResources).ToNot(BeEmpty())
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())
	})

	// -------------------------------------------------------------------------
	// Task 10: finalizer-driven deletion with ShootUnreachable safety
	//
	// Test approach:
	//   (b) Unreachable shoot: Secret missing → buildShootApplier fails →
	//       finalizer NOT removed, ShootUnreachable condition set, requeue 30s.
	//   (c) Reachable shoot: Secret populated with real envtest token+CA so
	//       buildShootApplier builds a client pointing at the envtest cluster.
	//       Non-CRD remote resources deleted; CRD retained (retentionPolicy=Retain
	//       default); finalizer removed on success.
	// -------------------------------------------------------------------------

	Describe("reconcileDelete", func() {
		const deleteNS = "default"

		newDeleteCR := func(name string) *ddov1alpha1.DualDeploymentOperator {
			return &ddov1alpha1.DualDeploymentOperator{
				ObjectMeta: metav1.ObjectMeta{
					Name:       name,
					Namespace:  deleteNS,
					Finalizers: []string{FinalizerName},
				},
				Spec: ddov1alpha1.DualDeploymentOperatorSpec{
					Source: ddov1alpha1.Source{
						Helm: &ddov1alpha1.HelmSource{
							Repo:    "oci://unused",
							Name:    "demo",
							Version: "0.1.0",
						},
					},
					RemoteAccess: ddov1alpha1.RemoteAccessRef{
						SecretName: "shoot-secret-del",
						Server:     cfg.Host,
					},
					RemoteNamespace: "kube-system",
					RetentionPolicy: ddov1alpha1.RetentionPolicy{CRDs: "Retain"},
				},
			}
		}

		It("retains finalizer and sets ShootUnreachable when Secret is missing", func() {
			// Given: CR with finalizer and deletion timestamp, no Secret in cluster.
			cr := newDeleteCR("test-del-unreachable")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			fakeRecorder := record.NewFakeRecorder(10)
			r := &DualDeploymentOperatorReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: fakeRecorder,
				HostApplier: &deliver.SSAApplier{
					Client:       k8sClient,
					FieldManager: FieldManagerName,
					Cluster:      "host",
				},
			}

			// When: delete the CR (sets DeletionTimestamp).
			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())

			// Trigger reconcileDelete directly via Reconcile.
			result, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
			})

			// Then: no error returned (requeue via RequeueAfter), requeue 30s.
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Finalizer must still be present.
			got := &ddov1alpha1.DualDeploymentOperator{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(got, FinalizerName)).To(BeTrue(),
				"finalizer must NOT be removed when shoot is unreachable")

			// ShootUnreachable condition must be set.
			cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil(), "Ready condition must be set")
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("ShootUnreachable"))

			// A warning Event must have been emitted.
			Expect(fakeRecorder.Events).To(Receive(ContainSubstring("ShootUnreachable")))
		})

		It("deletes non-CRD remote resources, retains CRDs, removes finalizer on success", func() {
			// Given: a CR with finalizer whose shoot applier is injected via
			// shootApplierFor to point at the envtest cluster (deterministic —
			// independent of envtest auth mode), and remote status recording a
			// ConfigMap (to delete) and a CRD (to retain under Retain).
			cr := newDeleteCR("test-del-reachable")
			ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

			remoteConfigMap := &unstructured.Unstructured{}
			remoteConfigMap.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
			remoteConfigMap.SetName("remote-cm-to-delete")
			remoteConfigMap.SetNamespace(deleteNS)
			remoteConfigMap.SetLabels(map[string]string{manifest.OwnedByLabel: ownedBy})
			Expect(k8sClient.Create(ctx, remoteConfigMap)).To(Succeed())

			// Create the CRD so the retention assertion is self-contained (not reliant
			// on another spec having rendered it into the shared envtest cluster).
			retainCRD := &unstructured.Unstructured{}
			retainCRD.SetUnstructuredContent(map[string]any{
				"apiVersion": "apiextensions.k8s.io/v1",
				"kind":       "CustomResourceDefinition",
				"metadata":   map[string]any{"name": "demos.demo.cc.sap"},
				"spec": map[string]any{
					"group": "demo.cc.sap",
					"names": map[string]any{"kind": "Demo", "listKind": "DemoList", "plural": "demos", "singular": "demo"},
					"scope": "Namespaced",
					"versions": []any{map[string]any{
						"name": "v1alpha1", "served": true, "storage": true,
						"schema": map[string]any{"openAPIV3Schema": map[string]any{"type": "object"}},
					}},
				},
			})
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, retainCRD))).To(Succeed())

			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			cr.Status.RemoteResources = []ddov1alpha1.ResourceStatus{
				{Kind: "ConfigMap", APIVersion: "v1", Namespace: deleteNS, Name: "remote-cm-to-delete", Health: ddov1alpha1.HealthHealthy},
				{Kind: "CustomResourceDefinition", APIVersion: "apiextensions.k8s.io/v1", Name: "demos.demo.cc.sap", Health: ddov1alpha1.HealthHealthy},
			}
			Expect(k8sClient.Status().Update(ctx, cr)).To(Succeed())

			fakeRecorder := record.NewFakeRecorder(10)
			r := &DualDeploymentOperatorReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: fakeRecorder,
				HostApplier: &deliver.SSAApplier{
					Client:       k8sClient,
					FieldManager: FieldManagerName,
					Cluster:      "host",
				},
				shootApplierFor: func(_ context.Context, _ *ddov1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
					return &deliver.SSAApplier{Client: k8sClient, FieldManager: FieldManagerName, Cluster: "remote"}, nil
				},
			}

			// When: delete the CR.
			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Then: the non-CRD remote resource is deleted.
			deletedCM := &unstructured.Unstructured{}
			deletedCM.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
			deletedCM.SetName("remote-cm-to-delete")
			deletedCM.SetNamespace(deleteNS)
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deletedCM), deletedCM)).
				NotTo(Succeed(), "non-CRD remote resource must be deleted")

			// The CRD is retained (retentionPolicy.crds=Retain default).
			retainedCRD := &unstructured.Unstructured{}
			retainedCRD.SetGroupVersionKind(schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"})
			retainedCRD.SetName("demos.demo.cc.sap")
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(retainedCRD), retainedCRD)).
				To(Succeed(), "CRD must be retained under retentionPolicy.crds=Retain")

			// The finalizer is removed on success (CR then GC'd, or finalizer gone).
			got := &ddov1alpha1.DualDeploymentOperator{}
			if getErr := k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got); getErr == nil {
				Expect(controllerutil.ContainsFinalizer(got, FinalizerName)).To(BeFalse(),
					"finalizer must be removed after successful remote cleanup")
			}
		})

		It("does not delete a foreign-owned resource recorded in status during teardown", func() {
			cr := newDeleteCR("test-del-foreign")

			foreignCM := &unstructured.Unstructured{}
			foreignCM.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
			foreignCM.SetName("foreign-owned-cm")
			foreignCM.SetNamespace(deleteNS)
			foreignCM.SetLabels(map[string]string{manifest.OwnedByLabel: "some-other-cr-owner"})
			Expect(k8sClient.Create(ctx, foreignCM)).To(Succeed())

			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			cr.Status.RemoteResources = []ddov1alpha1.ResourceStatus{
				{Kind: "ConfigMap", APIVersion: "v1", Namespace: deleteNS, Name: "foreign-owned-cm", Health: ddov1alpha1.HealthHealthy},
			}
			Expect(k8sClient.Status().Update(ctx, cr)).To(Succeed())

			r := &DualDeploymentOperatorReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
				HostApplier: &deliver.SSAApplier{
					Client: k8sClient, FieldManager: FieldManagerName, Cluster: "host",
				},
				shootApplierFor: func(_ context.Context, _ *ddov1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
					return &deliver.SSAApplier{Client: k8sClient, FieldManager: FieldManagerName, Cluster: "remote"}, nil
				},
			}

			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// The foreign-owned resource must survive teardown — the CR never owned it.
			survivor := &unstructured.Unstructured{}
			survivor.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
			survivor.SetName("foreign-owned-cm")
			survivor.SetNamespace(deleteNS)
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(survivor), survivor)).
				To(Succeed(), "foreign-owned resource must NOT be deleted during teardown")
		})
	})
})
