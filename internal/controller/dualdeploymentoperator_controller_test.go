// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"strings"
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
	"k8s.io/client-go/tools/events"
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
		SeedApplier: &deliver.SSAApplier{Client: c, FieldManager: FieldManagerName, Cluster: "seed"},
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
// chart renders a CRD (cluster-scoped) for ModeShoot and a CRD + ConfigMap
// (addition) + optional Deployment for ModeSeed. We use ApplyOrder=SeedFirst
// and point ShootAccess at a Secret with an EMPTY token so the shoot phase
// is "credsNotReady" — seed applies cleanly, conditions are written, and
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
				ShootAccess: ddov1alpha1.ShootAccessRef{
					SecretName: remoteSecretName,
					Server:     "https://shoot-api.example:6443",
				},
				ShootNamespace: "kube-system",
				// SeedFirst so that even with credsNotReady for the shoot, the
				// seed render is applied and status/conditions are populated.
				ApplyOrder: "SeedFirst",
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
			SeedApplier: &deliver.SSAApplier{
				Client:       k8sClient,
				FieldManager: FieldManagerName,
				Cluster:      "seed",
			},
			SourceDeps: source.Deps{
				ChartLoader: envtestFakeChartLoader{dir: demoChartDir},
			},
		}
	})

	It("delivers seed render and populates status conditions and LastReconcile", func() {
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

	It("prunes orphaned seed resources and respects retentionPolicy and owned-by guard", func() {
		cr := newCR("test-prune")
		ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

		loader := &switchableChartLoader{pruneRound: false}
		reconciler := &DualDeploymentOperatorReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			SeedApplier: &deliver.SSAApplier{
				Client:       k8sClient,
				FieldManager: FieldManagerName,
				Cluster:      "seed",
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
			g.Expect(got.Status.SeedResources).ToNot(BeEmpty())
		}, 5*time.Second, 100*time.Millisecond).Should(Succeed())

		// Track foreign-cm in status as if it were previously applied, so prune
		// considers it an orphan on the next round. Because the live object carries a
		// DIFFERENT owner label, the owned-by guard must skip the delete.
		Eventually(func(g Gomega) {
			got := &ddov1alpha1.DualDeploymentOperator{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
			got.Status.SeedResources = append(got.Status.SeedResources, ddov1alpha1.ResourceStatus{
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
		for _, rs := range got.Status.SeedResources {
			Expect(rs.Name).NotTo(Equal("orphan-cm"), "orphan-cm must not appear in status after prune")
		}

		_ = ownedBy
	})

	// Ready-phase reconcile: inject a shoot applier backed by the envtest cluster
	// (via shootApplierFor) so the full success path runs — shoot apply, seed
	// apply, prune, and a Ready=True status — for both apply orders.
	readyReconciler := func() *DualDeploymentOperatorReconciler {
		return &DualDeploymentOperatorReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			SeedApplier: &deliver.SSAApplier{
				Client: k8sClient, FieldManager: FieldManagerName, Cluster: "seed",
			},
			SourceDeps: source.Deps{ChartLoader: envtestFakeChartLoader{dir: demoChartDir}},
			shootApplierFor: func(_ context.Context, _ *ddov1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
				return &deliver.SSAApplier{Client: k8sClient, FieldManager: FieldManagerName, Cluster: "shoot"}, nil
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

	It("reaches Ready=True on the SeedFirst success path", func() {
		cr := newCR("test-ready-seedfirst")
		cr.Spec.ApplyOrder = "SeedFirst"
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
			g.Expect(got.Status.SeedResources).ToNot(BeEmpty())
			g.Expect(got.Status.ShootResources).ToNot(BeEmpty())
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())
	})

	It("reaches Ready=True on the ShootFirst success path", func() {
		cr := newCR("test-ready-shootfirst")
		cr.Spec.ApplyOrder = "ShootFirst"
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
			g.Expect(got.Status.ShootResources).ToNot(BeEmpty())
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())
	})

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
					degradeName: "demos.demo.cc.sap", // the only resource shoot mode renders — forces anyFailed()
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
			{Kind: "ConfigMap", APIVersion: "v1", Namespace: cr.Spec.ShootNamespace, Name: "orphan-cm", Health: ddov1alpha1.HealthHealthy},
		}
		Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())

		// Reconcile: shoot render is degraded (demos.demo.cc.sap CRD) AND orphan-cm left
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

	// -------------------------------------------------------------------------
	// Task 10: finalizer-driven deletion with ShootUnreachable safety
	//
	// Test approach:
	//   (b) Unreachable shoot: Secret missing → buildShootApplier fails →
	//       finalizer NOT removed, ShootUnreachable condition set, requeue 30s.
	//   (c) Reachable shoot: Secret populated with real envtest token+CA so
	//       buildShootApplier builds a client pointing at the envtest cluster.
	//       Non-CRD shoot resources deleted; CRD retained (retentionPolicy=Retain
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
					ShootAccess: ddov1alpha1.ShootAccessRef{
						SecretName: "shoot-secret-del",
						Server:     cfg.Host,
					},
					ShootNamespace:  "kube-system",
					RetentionPolicy: ddov1alpha1.RetentionPolicy{CRDs: "Retain"},
				},
			}
		}

		It("proceeds with seed cleanup, blocks the finalizer, and sets ShootCleanupBlocked when the shoot is unreachable", func() {
			// Given: a CR being deleted with a seed resource to clean up and a shoot
			// resource pending, but no shootAccess Secret (shoot client build fails).
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
					Client:       k8sClient,
					FieldManager: FieldManagerName,
					Cluster:      "seed",
				},
			}

			// When: delete the CR (sets DeletionTimestamp) and reconcile.
			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			result, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
			})

			// Then: no error, requeue 30s (blocking model).
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Seed resource was deleted despite the unreachable shoot (no hard-return).
			seedGet := &unstructured.Unstructured{}
			seedGet.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
			getErr := k8sClient.Get(ctx, types.NamespacedName{Namespace: deleteNS, Name: "seed-cm-del-blocked"}, seedGet)
			Expect(apierrors.IsNotFound(getErr)).To(BeTrue(), "seed resource must be deleted even when shoot unreachable")

			// Finalizer retained; ShootUnreachable + ShootCleanup=Blocked set.
			after := &ddov1alpha1.DualDeploymentOperator{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), after)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(after, FinalizerName)).To(BeTrue(),
				"finalizer must NOT be removed while shoot cleanup is blocked")
			blocked := meta.FindStatusCondition(after.Status.Conditions, "ShootCleanup")
			Expect(blocked).NotTo(BeNil(), "ShootCleanup condition must be set")
			Expect(blocked.Status).To(Equal(metav1.ConditionFalse))
			Expect(blocked.Reason).To(Equal("Blocked"))
			Expect(fakeRecorder.Events).To(Receive(ContainSubstring("ShootUnreachable")))
		})

		It("removes the finalizer and sets ShootCleanupForceDeleted when the force-delete annotation is set", func() {
			// Given: a CR being deleted with a shoot resource pending, unreachable shoot,
			// and the force-delete annotation set.
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
					Client:       k8sClient,
					FieldManager: FieldManagerName,
					Cluster:      "seed",
				},
			}

			// When: delete the CR and reconcile.
			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Finalizer removed (CR either gone, or present without the finalizer).
			after := &ddov1alpha1.DualDeploymentOperator{}
			getErr := k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), after)
			if getErr == nil {
				Expect(controllerutil.ContainsFinalizer(after, FinalizerName)).To(BeFalse(),
					"finalizer must be removed under force-delete")
			} else {
				Expect(apierrors.IsNotFound(getErr)).To(BeTrue())
			}
			var forceDeletedSeen bool
			for len(fakeRecorder.Events) > 0 {
				if strings.Contains(<-fakeRecorder.Events, "ShootCleanupForceDeleted") {
					forceDeletedSeen = true
				}
			}
			Expect(forceDeletedSeen).To(BeTrue(), "a ShootCleanupForceDeleted event must be emitted")
		})

		It("deletes non-CRD shoot resources, retains CRDs, removes finalizer on success", func() {
			// Given: a CR with finalizer whose shoot applier is injected via
			// shootApplierFor to point at the envtest cluster (deterministic —
			// independent of envtest auth mode), and shoot status recording a
			// ConfigMap (to delete) and a CRD (to retain under Retain).
			cr := newDeleteCR("test-del-reachable")
			ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

			shootConfigMap := &unstructured.Unstructured{}
			shootConfigMap.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
			shootConfigMap.SetName("shoot-cm-to-delete")
			shootConfigMap.SetNamespace(deleteNS)
			shootConfigMap.SetLabels(map[string]string{manifest.OwnedByLabel: ownedBy})
			Expect(k8sClient.Create(ctx, shootConfigMap)).To(Succeed())

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

			cr.Status.ShootResources = []ddov1alpha1.ResourceStatus{
				{Kind: "ConfigMap", APIVersion: "v1", Namespace: deleteNS, Name: "shoot-cm-to-delete", Health: ddov1alpha1.HealthHealthy},
				{Kind: "CustomResourceDefinition", APIVersion: "apiextensions.k8s.io/v1", Name: "demos.demo.cc.sap", Health: ddov1alpha1.HealthHealthy},
			}
			Expect(k8sClient.Status().Update(ctx, cr)).To(Succeed())

			fakeRecorder := events.NewFakeRecorder(10)
			r := &DualDeploymentOperatorReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: fakeRecorder,
				SeedApplier: &deliver.SSAApplier{
					Client:       k8sClient,
					FieldManager: FieldManagerName,
					Cluster:      "seed",
				},
				shootApplierFor: func(_ context.Context, _ *ddov1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
					return &deliver.SSAApplier{Client: k8sClient, FieldManager: FieldManagerName, Cluster: "shoot"}, nil
				},
			}

			// When: delete the CR.
			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Then: the non-CRD shoot resource is deleted.
			deletedCM := &unstructured.Unstructured{}
			deletedCM.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
			deletedCM.SetName("shoot-cm-to-delete")
			deletedCM.SetNamespace(deleteNS)
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deletedCM), deletedCM)).
				NotTo(Succeed(), "non-CRD shoot resource must be deleted")

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
					"finalizer must be removed after successful shoot cleanup")
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
			cr.Status.ShootResources = []ddov1alpha1.ResourceStatus{
				{Kind: "ConfigMap", APIVersion: "v1", Namespace: deleteNS, Name: "foreign-owned-cm", Health: ddov1alpha1.HealthHealthy},
			}
			Expect(k8sClient.Status().Update(ctx, cr)).To(Succeed())

			r := &DualDeploymentOperatorReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: events.NewFakeRecorder(10),
				SeedApplier: &deliver.SSAApplier{
					Client: k8sClient, FieldManager: FieldManagerName, Cluster: "seed",
				},
				shootApplierFor: func(_ context.Context, _ *ddov1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
					return &deliver.SSAApplier{Client: k8sClient, FieldManager: FieldManagerName, Cluster: "shoot"}, nil
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

		It("tears down in the reverse of spec.applyOrder", func() {
			assertOrder := func(applyOrder string, wantFirst string) {
				cr := newDeleteCR("test-del-order-" + strings.ToLower(applyOrder))
				cr.Spec.ApplyOrder = applyOrder
				Expect(k8sClient.Create(ctx, cr)).To(Succeed())
				got := &ddov1alpha1.DualDeploymentOperator{}
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cr), got)).To(Succeed())
				got.Status.SeedResources = []ddov1alpha1.ResourceStatus{
					{Kind: "ConfigMap", APIVersion: "v1", Namespace: deleteNS, Name: "seed-order-cm", Health: ddov1alpha1.HealthHealthy},
				}
				got.Status.ShootResources = []ddov1alpha1.ResourceStatus{
					{Kind: "ConfigMap", APIVersion: "v1", Namespace: "kube-system", Name: "shoot-order-cm", Health: ddov1alpha1.HealthHealthy},
				}
				Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())

				var order []string
				r := &DualDeploymentOperatorReconciler{
					Client:      k8sClient,
					Scheme:      k8sClient.Scheme(),
					Recorder:    events.NewFakeRecorder(10),
					SeedApplier: &recordingApplier{cluster: "seed", order: &order},
					shootApplierFor: func(_ context.Context, _ *ddov1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
						return &recordingApplier{cluster: "shoot", order: &order}, nil
					},
				}

				Expect(k8sClient.Delete(ctx, cr)).To(Succeed())
				_, err := r.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace},
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(order).To(HaveLen(2))
				Expect(order[0]).To(Equal(wantFirst),
					"deletion must process renders in the reverse of applyOrder=%s", applyOrder)
			}

			assertOrder("SeedFirst", "shoot")
			assertOrder("ShootFirst", "seed")
		})
	})
})

// recordingApplier records the order in which each cluster's Delete is invoked.
type recordingApplier struct {
	cluster string
	order   *[]string
}

func (a *recordingApplier) Apply(_ context.Context, m manifest.Manifest, _ string) (ddov1alpha1.ResourceStatus, error) {
	return ddov1alpha1.ResourceStatus{Kind: m.Unstructured.GetKind(), Name: m.Unstructured.GetName()}, nil
}

func (a *recordingApplier) Delete(_ context.Context, _ manifest.Manifest, _ string) error {
	*a.order = append(*a.order, a.cluster)
	return nil
}

func (a *recordingApplier) Get(_ context.Context, _ manifest.Manifest) (*unstructured.Unstructured, error) {
    return nil, apierrors.NewNotFound(schema.GroupResource{}, "")
}

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
