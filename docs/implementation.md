# Implementation Guide

Phase-by-phase concrete steps for building `dual-deployment-operator`. See [`design.md`](design.md) for the "what" and [`context.md`](context.md) for the "why".

---

## Phase 0: Scaffold (~1 day)

### Init

```bash
kubebuilder init \
  --domain cc.sap \
  --repo github.tools.sap/D065300/dual-deployment-operator \
  --project-name dual-deployment-operator

kubebuilder create api \
  --group dual-deployment-operator \
  --version v1alpha1 \
  --kind DualDeploymentOperator \
  --resource --controller
```

Expected `PROJECT` file group: `dual-deployment-operator.cc.sap`.

### Go version and dependencies

Go 1.22+. Key deps to add:

```
helm.sh/helm/v3                        # for Helm chart pull + render
sigs.k8s.io/kustomize/api/krusty       # for kustomize source render
sigs.k8s.io/controller-runtime         # already brought in by kubebuilder
k8s.io/client-go                       # for dynamic client + SSA
k8s.io/apimachinery                    # for unstructured
```

No Gardener imports needed (we don't emit ManagedResources).

### Layout

```
dual-deployment-operator/
├── api/v1alpha1/
│   ├── dualdeploymentoperator_types.go       # CRD Spec + Status types
│   ├── zz_generated.deepcopy.go
│   └── groupversion_info.go
├── cmd/
│   └── main.go
├── config/
│   ├── crd/                                    # kubebuilder-generated
│   ├── manager/
│   ├── rbac/
│   └── default/
├── internal/
│   ├── controller/
│   │   └── dualdeploymentoperator_controller.go
│   ├── source/
│   │   ├── source.go                           # Source interface + discriminator
│   │   ├── helm.go                             # Helm SDK renderer
│   │   └── kustomize.go                        # krusty renderer
│   ├── transform/
│   │   ├── transform.go                        # Transformation interface + registry
│   │   ├── inject_init_container.go
│   │   ├── rename_kind.go
│   │   ├── rewrite_webhook_url.go
│   │   ├── add_labels.go
│   │   └── filter_kinds.go
│   ├── split/
│   │   └── split.go                            # kind rules + annotation router
│   ├── deliver/
│   │   ├── deliver.go                          # Applier interface
│   │   ├── apply.go                            # SSA implementation
│   │   └── health.go                           # per-resource health computation
│   ├── clients/
│   │   ├── host.go                             # in-cluster client factory
│   │   └── shoot.go                            # kubeconfig-from-Secret client factory
│   └── manifest/
│       └── manifest.go                         # []unstructured wrapper with origin tag
├── testdata/
│   └── fixtures/                                # per-operator test fixtures
├── go.mod
├── go.sum
├── Makefile                                     # kubebuilder default + additions
└── Dockerfile
```

---

## Phase 1: CRD types (~2-3 days)

### Spec types

Define in `api/v1alpha1/dualdeploymentoperator_types.go`:

```go
type DualDeploymentOperatorSpec struct {
    Source            Source                    `json:"source"`
    RemoteKubeconfig  RemoteKubeconfigRef       `json:"remoteKubeconfig"`
    Transformations   []Transformation          `json:"transformations,omitempty"`
    DeletionPolicy    DeletionPolicy            `json:"deletionPolicy,omitempty"`
}

// Source is a discriminated union — exactly one of Helm, Kustomize.
type Source struct {
    Helm      *HelmSource      `json:"helm,omitempty"`
    Kustomize *KustomizeSource `json:"kustomize,omitempty"`
}

type HelmSource struct {
    Repo    string                       `json:"repo"`
    Name    string                       `json:"name"`
    Version string                       `json:"version"`
    Values  *apiextensionsv1.JSON        `json:"values,omitempty"`
}

type KustomizeSource struct {
    URL     string                       `json:"url"`
    Values  *apiextensionsv1.JSON        `json:"values,omitempty"`
}

type RemoteKubeconfigRef struct {
    SecretName string `json:"secretName"`
    Key        string `json:"key"`
}

// Transformation is a discriminated union — exactly one field set.
type Transformation struct {
    InjectInitContainer *InjectInitContainerSpec `json:"injectInitContainer,omitempty"`
    RenameKind          *RenameKindSpec          `json:"renameKind,omitempty"`
    RewriteWebhookURL   *RewriteWebhookURLSpec   `json:"rewriteWebhookURL,omitempty"`
    AddLabels           *AddLabelsSpec           `json:"addLabels,omitempty"`
    FilterKinds         *FilterKindsSpec         `json:"filterKinds,omitempty"`
}

type InjectInitContainerSpec struct {
    Selector            Selector        `json:"selector"`
    Container           corev1.Container `json:"container"`
    AdditionalVolumes   []corev1.Volume  `json:"additionalVolumes,omitempty"`
}

type RenameKindSpec struct {
    From            string `json:"from"`
    To              string `json:"to"`
    FromAPIVersion  string `json:"fromApiVersion,omitempty"`
    ToAPIVersion    string `json:"toApiVersion,omitempty"`
    Source          string `json:"source,omitempty"`   // "upstream" | "additions" | ""
}

type RewriteWebhookURLSpec struct {
    URLPrefix   string   `json:"urlPrefix"`
    TargetKinds []string `json:"targetKinds,omitempty"`
}

type AddLabelsSpec struct {
    Selector Selector          `json:"selector"`
    Labels   map[string]string `json:"labels"`
}

type FilterKindsSpec struct {
    Kinds  []string `json:"kinds"`
    Source string   `json:"source,omitempty"`
}

type Selector struct {
    Kind   string `json:"kind,omitempty"`
    Name   string `json:"name,omitempty"`      // supports glob "*"
    Origin string `json:"origin,omitempty"`    // "upstream" | "additions"
}

type DeletionPolicy struct {
    CRDs string `json:"crds,omitempty"` // "Retain" (default) | "Delete"
}
```

### Status types

```go
type DualDeploymentOperatorStatus struct {
    HostResources   []ResourceStatus   `json:"hostResources,omitempty"`
    RemoteResources []ResourceStatus   `json:"remoteResources,omitempty"`
    Conditions      []metav1.Condition `json:"conditions,omitempty"`
    LastReconcile   *metav1.Time       `json:"lastReconcile,omitempty"`
}

type ResourceStatus struct {
    Kind        string       `json:"kind"`
    APIVersion  string       `json:"apiVersion"`
    Namespace   string       `json:"namespace,omitempty"`
    Name        string       `json:"name"`
    Health      HealthState  `json:"health"`
    LastApplied *metav1.Time `json:"lastApplied,omitempty"`
    Message     string       `json:"message,omitempty"`
}

type HealthState string
const (
    HealthHealthy     HealthState = "Healthy"
    HealthProgressing HealthState = "Progressing"
    HealthDegraded    HealthState = "Degraded"
    HealthUnknown     HealthState = "Unknown"
)
```

### Validation

Use CRD OpenAPI validation via kubebuilder annotations:

- `+kubebuilder:validation:MinLength=1` on all string identifiers
- `+kubebuilder:validation:Enum=Retain;Delete` on `DeletionPolicy.CRDs`
- Custom webhook or admission-time validation to enforce:
  - Exactly one of `Source.Helm` / `Source.Kustomize`
  - Exactly one of the `Transformation` union fields
  - `KustomizeSource.URL` includes `?ref=` query parameter
  - `HelmSource.Version` is a valid semver constraint

Consider using CEL validation rules (Kubernetes 1.29+) for the discriminator constraints — cleaner than a validating webhook for simple discriminators.

---

## Phase 2: Source renderers (~3-4 days)

### Interface

```go
package source

import "context"

type Source interface {
    Render(ctx context.Context) ([]manifest.Manifest, error)
}

func From(spec v1alpha1.Source) (Source, error) {
    switch {
    case spec.Helm != nil && spec.Kustomize == nil:
        return NewHelm(spec.Helm), nil
    case spec.Kustomize != nil && spec.Helm == nil:
        return NewKustomize(spec.Kustomize), nil
    default:
        return nil, errors.New("exactly one of source.helm or source.kustomize must be set")
    }
}
```

### Helm renderer

```go
package source

type Helm struct {
    spec *v1alpha1.HelmSource
}

func (h *Helm) Render(ctx context.Context) ([]manifest.Manifest, error) {
    // 1. Set up registry client (OCI auth if needed)
    settings := cli.New()
    registryClient, _ := registry.NewClient(...)

    // 2. Pull chart to temp dir
    puller := action.NewPullWithOpts(action.WithConfig(...))
    puller.RepoURL = h.spec.Repo
    puller.Version = h.spec.Version
    puller.DestDir = tmpDir
    // ...

    // 3. Load chart
    chart, err := loader.Load(chartPath)

    // 4. Merge values
    values := parseValues(h.spec.Values)

    // 5. Render templates
    installer := action.NewInstall(cfg)
    installer.DryRun = true
    installer.ReleaseName = h.spec.Name
    installer.ClientOnly = true
    installer.IncludeCRDs = true
    installer.Namespace = "..."

    release, err := installer.Run(chart, values)

    // 6. Parse rendered YAML → []Manifest with origin: upstream
    return parseManifests(release.Manifest, OriginUpstream), nil
}
```

**Key concerns**:
- OCI registry auth for private repos (`keppel.eu-de-1.cloud.sap`) — reuse existing pull secrets
- Chart caching to avoid re-pulling on every reconcile (LRU cache keyed by repo+name+version)
- Values merging — CR values override chart's `values.yaml` per Helm precedence
- Post-render marking with `origin: upstream` on every manifest

### Kustomize renderer

```go
package source

type Kustomize struct {
    spec *v1alpha1.KustomizeSource
}

func (k *Kustomize) Render(ctx context.Context) ([]manifest.Manifest, error) {
    // 1. Set up krusty options
    opts := krusty.MakeDefaultOptions()
    opts.LoadRestrictions = types.LoadRestrictionsNone   // allow remote refs

    // 2. Init file system with remote support
    fSys := filesys.MakeFsOnDisk()  // krusty handles URL resolution

    // 3. Kustomize build
    k := krusty.MakeKustomizer(opts)
    resmap, err := k.Run(fSys, spec.URL)
    if err != nil { return nil, err }

    // 4. Serialize
    yaml, err := resmap.AsYaml()

    // 5. Parse → []Manifest with origin: upstream
    // Special handling: our additions/ subdir resources get origin: additions
    return parseManifests(yaml, ...), nil
}
```

**Key concerns**:
- Value projection into kustomize root — spec.Kustomize.Values need a mechanism. Options: generate a configMapGenerator patch on the fly, or convention (source declares expected literals; operator adds patches). Defer decision until Phase 6.
- Ref pinning — reject URLs without `?ref=<sha|tag>` at admission time
- Distinguishing origin: for ipam-capi, the additions live under `additions/` subdir of the kustomize root. Operator inspects the resource's source path (kustomize preserves `internal.config.kubernetes.io/kustomize-source` annotation) to tag origin.

---

## Phase 3: Transformations (~1 week)

### Interface

```go
package transform

type Transformation interface {
    Type() string
    Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error)
}

func From(spec v1alpha1.Transformation) (Transformation, error) {
    switch {
    case spec.InjectInitContainer != nil:
        return &injectInitContainer{spec: spec.InjectInitContainer}, nil
    case spec.RenameKind != nil:
        return &renameKind{spec: spec.RenameKind}, nil
    case spec.RewriteWebhookURL != nil:
        return &rewriteWebhookURL{spec: spec.RewriteWebhookURL}, nil
    case spec.AddLabels != nil:
        return &addLabels{spec: spec.AddLabels}, nil
    case spec.FilterKinds != nil:
        return &filterKinds{spec: spec.FilterKinds}, nil
    default:
        return nil, errors.New("no transformation type set")
    }
}
```

### Table-driven tests

For each transformation, structure tests as:

```go
func TestInjectInitContainer(t *testing.T) {
    tests := []struct {
        name    string
        input   []manifest.Manifest
        spec    *v1alpha1.InjectInitContainerSpec
        want    []manifest.Manifest
        wantErr string
    }{
        {
            name: "injects sidecar into matching Deployment",
            input: []manifest.Manifest{fixture("upstream-deployment.yaml")},
            spec: &v1alpha1.InjectInitContainerSpec{...},
            want: []manifest.Manifest{fixture("expected-deployment-with-sidecar.yaml")},
        },
        {
            name: "error when no matching resource",
            input: []manifest.Manifest{fixture("unrelated.yaml")},
            spec: &v1alpha1.InjectInitContainerSpec{Selector: Selector{Kind: "Deployment", Name: "does-not-exist"}},
            wantErr: "no matching resource",
        },
        // ...
    }
    // ...
}
```

Fixtures under `testdata/fixtures/transform/<transformation-name>/`.

### Selector matching

Extract to `internal/transform/selector.go`:

```go
func Match(m manifest.Manifest, sel v1alpha1.Selector) bool {
    if sel.Kind != "" && m.GetKind() != sel.Kind {
        return false
    }
    if sel.Name != "" && !globMatch(m.GetName(), sel.Name) {
        return false
    }
    if sel.Origin != "" && string(m.Origin) != sel.Origin {
        return false
    }
    return true
}
```

Shared across all transformations that use selectors.

---

## Phase 4: Split (~2-3 days)

```go
package split

type Buckets struct {
    Host    []manifest.Manifest
    Remote  []manifest.Manifest
    Dropped []manifest.Manifest
}

func ByTarget(manifests []manifest.Manifest) Buckets {
    var b Buckets
    for _, m := range manifests {
        target := resolveTarget(m)
        switch target {
        case TargetHost:
            b.Host = append(b.Host, m)
        case TargetRemote:
            b.Remote = append(b.Remote, m)
        case TargetDrop:
            b.Dropped = append(b.Dropped, m)
        }
    }
    return b
}

func resolveTarget(m manifest.Manifest) Target {
    // 1. Explicit annotation wins
    if t, ok := m.GetAnnotations()[TargetAnnotation]; ok {
        return Target(t)
    }
    // 2. Kind-based default
    return defaultTarget(m.GetKind(), m.Origin)
}

func defaultTarget(kind string, origin manifest.Origin) Target {
    switch kind {
    case "CustomResourceDefinition":
        return TargetRemote
    case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
        return TargetRemote
    case "ClusterRole", "ClusterRoleBinding":
        return TargetRemote
    case "Role", "RoleBinding":
        return TargetRemote  // should be rare post-renameKind
    case "ServiceAccount":
        if origin == manifest.OriginUpstream {
            return TargetRemote
        }
        return TargetHost
    case "Deployment", "StatefulSet", "DaemonSet":
        return TargetHost
    case "Service", "ConfigMap", "Secret", "Ingress", "NetworkPolicy":
        return TargetHost
    case "Namespace":
        // Must have explicit annotation
        return TargetHost  // fallback, log warning
    default:
        return TargetHost  // fallback, log warning
    }
}
```

Test with fixture manifests covering the full rule table.

---

## Phase 5: Delivery (~1 week)

### Applier interface

```go
package deliver

type Applier interface {
    Apply(ctx context.Context, m manifest.Manifest) (ResourceStatus, error)
    Delete(ctx context.Context, m manifest.Manifest) error
}

type SSAApplier struct {
    Client       client.Client
    FieldManager string
    Cluster      string  // "host" or "remote" for logging
}
```

### Implementation

```go
func (a *SSAApplier) Apply(ctx context.Context, m manifest.Manifest) (ResourceStatus, error) {
    // Strip routing annotations before applying
    m.StripInternalAnnotations()

    err := a.Client.Patch(ctx, m.Unstructured, client.Apply,
        client.FieldOwner(a.FieldManager),
        client.ForceOwnership)

    status := ResourceStatus{
        Kind:       m.GetKind(),
        APIVersion: m.GetAPIVersion(),
        Namespace:  m.GetNamespace(),
        Name:       m.GetName(),
        LastApplied: &metav1.Time{Time: time.Now()},
    }
    if err != nil {
        status.Health = HealthDegraded
        status.Message = err.Error()
        return status, err
    }

    // Fetch current state for health check
    current, err := a.get(ctx, m)
    status.Health = computeHealth(current)
    return status, nil
}
```

### Health computation

```go
func computeHealth(u *unstructured.Unstructured) HealthState {
    switch u.GetKind() {
    case "Deployment":
        return deploymentHealth(u)
    case "StatefulSet":
        return statefulSetHealth(u)
    case "CustomResourceDefinition":
        return crdHealth(u)
    case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
        return webhookConfigHealth(u)
    default:
        return HealthHealthy  // exists = healthy for other kinds
    }
}

func deploymentHealth(u *unstructured.Unstructured) HealthState {
    replicas, _, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
    available, _, _ := unstructured.NestedInt64(u.Object, "status", "availableReplicas")
    if available >= replicas {
        return HealthHealthy
    }
    return HealthProgressing
}

func webhookConfigHealth(u *unstructured.Unstructured) HealthState {
    webhooks, found, _ := unstructured.NestedSlice(u.Object, "webhooks")
    if !found || len(webhooks) == 0 {
        return HealthDegraded
    }
    // Check all webhooks have non-empty caBundle
    for _, wh := range webhooks {
        whMap := wh.(map[string]interface{})
        cc, _, _ := unstructured.NestedMap(whMap, "clientConfig")
        caBundle, _, _ := unstructured.NestedString(cc, "caBundle")
        if caBundle == "" {
            return HealthProgressing  // waiting for webhook-injector to patch
        }
    }
    return HealthHealthy
}
```

### SSA field-manager coexistence with webhook-injector

The operator **omits** `caBundle` when applying WebhookConfigurations and CRDs with conversion webhooks:

```go
func (a *SSAApplier) prepareForApply(m manifest.Manifest) {
    if m.GetKind() == "ValidatingWebhookConfiguration" || m.GetKind() == "MutatingWebhookConfiguration" {
        stripCABundleFromWebhooks(m.Unstructured)
    }
    if m.GetKind() == "CustomResourceDefinition" {
        stripCABundleFromCRDConversion(m.Unstructured)
    }
}
```

This lets webhook-injector own the `caBundle` field via its own SSA field manager. Operator's re-applies never touch caBundle, so injector's writes persist.

---

## Phase 6: Reconciler (~1 week)

### Main loop

```go
func (r *DualDeploymentOperatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    cr := &v1alpha1.DualDeploymentOperator{}
    if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Handle deletion
    if !cr.DeletionTimestamp.IsZero() {
        return r.reconcileDelete(ctx, cr)
    }

    // Add finalizer on first reconcile
    if !controllerutil.ContainsFinalizer(cr, FinalizerName) {
        controllerutil.AddFinalizer(cr, FinalizerName)
        return ctrl.Result{}, r.Update(ctx, cr)
    }

    // 1. Fetch and render source
    src, err := source.From(cr.Spec.Source)
    if err != nil {
        return r.errStatus(ctx, cr, "InvalidSource", err)
    }
    manifests, err := src.Render(ctx)
    if err != nil {
        return r.errStatus(ctx, cr, "RenderFailed", err)
    }

    // 2. Apply transformations
    for _, ts := range cr.Spec.Transformations {
        t, err := transform.From(ts)
        if err != nil { return r.errStatus(ctx, cr, "InvalidTransformation", err) }
        manifests, err = t.Apply(manifests)
        if err != nil { return r.errStatus(ctx, cr, "TransformFailed", err) }
    }

    // 3. Split
    buckets := split.ByTarget(manifests)

    // 4. Get clients
    hostApplier := r.HostApplier   // preconstructed at startup
    shootApplier, err := r.buildShootApplier(ctx, cr)
    if err != nil {
        return r.errStatus(ctx, cr, "ShootClientFailed", err)
    }

    // 5. Apply
    hostStatuses := r.applyAll(ctx, hostApplier, buckets.Host)
    remoteStatuses := r.applyAll(ctx, shootApplier, buckets.Remote)

    // 6. Update status
    cr.Status.HostResources = hostStatuses
    cr.Status.RemoteResources = remoteStatuses
    cr.Status.Conditions = computeConditions(hostStatuses, remoteStatuses)
    cr.Status.LastReconcile = &metav1.Time{Time: time.Now()}

    if err := r.Status().Update(ctx, cr); err != nil {
        return ctrl.Result{}, err
    }

    // Requeue for periodic drift correction
    return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}
```

### Shoot client construction

```go
func (r *DualDeploymentOperatorReconciler) buildShootApplier(ctx context.Context, cr *v1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
    secret := &corev1.Secret{}
    if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: cr.Spec.RemoteKubeconfig.SecretName}, secret); err != nil {
        return nil, fmt.Errorf("failed to get shoot kubeconfig secret: %w", err)
    }

    kubeconfigBytes, ok := secret.Data[cr.Spec.RemoteKubeconfig.Key]
    if !ok {
        return nil, fmt.Errorf("key %q not found in secret", cr.Spec.RemoteKubeconfig.Key)
    }

    config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
    if err != nil {
        return nil, err
    }

    shootClient, err := client.New(config, client.Options{})
    if err != nil {
        return nil, err
    }

    return &deliver.SSAApplier{
        Client:       shootClient,
        FieldManager: FieldManagerName,
        Cluster:      "remote",
    }, nil
}
```

### Deletion

```go
func (r *DualDeploymentOperatorReconciler) reconcileDelete(ctx context.Context, cr *v1alpha1.DualDeploymentOperator) (ctrl.Result, error) {
    shootApplier, err := r.buildShootApplier(ctx, cr)
    if err != nil {
        // Shoot may be unreachable during shoot deletion — log and continue with host cleanup
        r.Log.Info("shoot unreachable during deletion, proceeding with host cleanup only", "error", err)
    }

    // Delete remote resources
    if shootApplier != nil {
        for _, rs := range cr.Status.RemoteResources {
            if rs.Kind == "CustomResourceDefinition" && cr.Spec.DeletionPolicy.CRDs == "Retain" {
                continue
            }
            _ = shootApplier.Delete(ctx, rs.AsManifest())
        }
    }

    // Delete host resources via ownerReferences cascade (kubelet GC)
    // Explicit delete not strictly necessary; ownerReferences handle it

    controllerutil.RemoveFinalizer(cr, FinalizerName)
    return ctrl.Result{}, r.Update(ctx, cr)
}
```

---

## Phase 7: Equivalence tests (~1 week)

Fixtures directory:

```
testdata/fixtures/
├── metal-operator/
│   ├── cr.yaml                                # CR fixture
│   ├── shoot-values.yaml                      # values for target shoot
│   ├── today-chart-render.yaml                # captured helm template of today's chart
│   └── expected-operator-output/
│       ├── host/
│       │   ├── deployment.yaml
│       │   ├── service.yaml
│       │   └── ...
│       └── remote/
│           ├── crd-endpoints-metal.yaml
│           └── ...
├── boot-operator/
├── argora-operator/
├── khalkeon/
└── ipam-capi/
```

Test structure:

```go
func TestEquivalence(t *testing.T) {
    ops := []string{"metal-operator", "boot-operator", "argora-operator", "khalkeon", "ipam-capi"}
    for _, op := range ops {
        t.Run(op, func(t *testing.T) {
            cr := loadCR(fmt.Sprintf("testdata/fixtures/%s/cr.yaml", op))

            operatorOutput := renderThroughOperator(cr)   // with mocked apply layer capturing manifests

            todayOutput := loadTodayChartRender(op)

            requireEquivalent(t, operatorOutput.Host, todayOutput.Host,
                ignore("helm.sh/chart"),           // allowed to differ
                ignoreOrdering(),
                ignoreWhitespace())
            requireEquivalent(t, operatorOutput.Remote, todayOutput.Remote,
                sameIgnores)
        })
    }
}
```

Load bearing test. Failure means the operator produces different output than today's chart — must investigate before proceeding to migration.

---

## Phase 8: Deployment chart (~2 days)

The operator itself needs a chart to deploy per-shoot. In `sapcc/helm-charts`:

```
system/dual-deployment-operator/
├── Chart.yaml
├── values.yaml
└── templates/
    ├── deployment.yaml
    ├── serviceaccount.yaml
    ├── clusterrole.yaml         # via subject-access-reviewer for CR watches
    ├── role.yaml                # in-namespace resource applies
    ├── rolebinding.yaml
    └── networkpolicy.yaml
```

Deployed via a per-shoot Flux HelmRelease alongside the operators it manages.

RBAC scope:
- Watch `DualDeploymentOperator` CRs in own namespace
- Get/List Secrets in own namespace (for shoot kubeconfig)
- Apply arbitrary resources in own namespace (host resources — Deployment, Service, etc.)
- Apply arbitrary resources in shoot (remote — via kubeconfig, so RBAC is enforced by shoot)

The operator's cluster-side RBAC in seed should be minimal — only what it needs to apply to its own namespace + read Secrets.

---

## Concrete v1 dependencies

`go.mod` (illustrative):

```
module github.tools.sap/D065300/dual-deployment-operator

go 1.22

require (
    helm.sh/helm/v3 v3.15.0
    sigs.k8s.io/kustomize/api v0.17.2
    sigs.k8s.io/kustomize/kyaml v0.17.2
    sigs.k8s.io/controller-runtime v0.19.0
    k8s.io/apimachinery v0.31.0
    k8s.io/api v0.31.0
    k8s.io/client-go v0.31.0
    github.com/go-logr/logr v1.4.2
    github.com/onsi/ginkgo/v2 v2.20.0
    github.com/onsi/gomega v1.34.1
)
```

Verify versions against current stable at implementation time.

---

## Development quick reference

**Run tests**:
```bash
make test
```

**Run against QA shoot**:
```bash
# from a machine with kubectl access to QA seed
kubectl apply -f config/crd/bases/
make install-cr FIXTURE=metal-operator NAMESPACE=shoot--cp--m-qa-de-1
make run
```

**Local development loop**:
```bash
# Uses envtest via kubebuilder scaffolding
make test-integration
```

---

## Success criteria per phase

| Phase | Success criterion |
|---|---|
| 0 | Kubebuilder scaffold builds; CRD registers |
| 1 | CRD types compile; deepcopy generated; validation webhook (or CEL) rejects invalid discriminators |
| 2 | Both Helm and kustomize renderers produce parsable manifest streams for a metal-operator fixture; origin tags correct |
| 3 | Each transformation's unit tests pass with table-driven cases |
| 4 | Split correctly buckets a mixed manifest stream per rules and annotations |
| 5 | Applier applies a resource via SSA and returns Healthy status; can also delete |
| 6 | Reconciler successfully processes a CR end-to-end with mocked source; status populated |
| 7 | Equivalence test passes for at least metal-operator vs. today's chart output |
| 8 | Operator chart installs successfully in a QA shoot-cp namespace |

Each phase should merge to `main` with tests before moving to the next.

---

## Notes on chart-side changes (out of this repo's scope)

These changes happen in `sapcc/helm-charts`, not in this operator repo, but they gate the operator's usefulness:

- Restructure `system/metal-operator-remote/` (and other wrapper charts) per `design.md` §4.1: split templates into `templates/host/` and `templates/remote/`, invert upstream enable values, add annotation helpers, delete pre-rendered files
- Restructure `system/kustomize/ipam-capi-remote/` per §4.2: top-level kustomization, additions/ subdir, pin refs
- Verify webhook-injector uses distinct SSA field manager (small injector code change if not)

Do the operator work first; chart restructures follow once the operator is validated against today's chart output in equivalence tests.
