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
k8s.io/apimachinery                    # for unstructured + strategic-merge patch
github.com/evanphx/json-patch          # for JSON Patch (RFC 6902) in patch transformation
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
│   │   ├── transform.go                        # Transformation interface + Build()
│   │   ├── patch.go                            # strategic-merge + JSON Patch
│   │   ├── rewrite_webhook_url.go
│   │   └── filter_kinds.go
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
    Repo         string                `json:"repo"`
    Name         string                `json:"name"`
    Version      string                `json:"version"`
    // Values applied to BOTH host and remote renders (common per-cluster stuff).
    Values       *apiextensionsv1.JSON `json:"values,omitempty"`
    // Values applied ONLY to the host render (typically enables the host-side
    // parts of the upstream subchart).
    HostValues   *apiextensionsv1.JSON `json:"hostValues,omitempty"`
    // Values applied ONLY to the remote render (typically enables the remote-side
    // parts of the upstream subchart).
    RemoteValues *apiextensionsv1.JSON `json:"remoteValues,omitempty"`
}

// KustomizeSource references a kustomize root plus its two overlay subpaths.
// +kubebuilder:validation:XValidation:rule="self.url.matches('.*[?&]ref=.+')",message="kustomize url must include a pinned ref= query parameter"
type KustomizeSource struct {
    // +kubebuilder:validation:MinLength=1
    URL        string `json:"url"`
    // +kubebuilder:validation:MinLength=1
    HostPath   string `json:"hostPath"`
    // +kubebuilder:validation:MinLength=1
    RemotePath string `json:"remotePath"`
}

type RemoteKubeconfigRef struct {
    SecretName string `json:"secretName"`
    Key        string `json:"key"`
}

// Transformation is a discriminated union — exactly one field set.
// All three types are per-render (single scope, r7).
type Transformation struct {
    Patch             *PatchSpec             `json:"patch,omitempty"`
    RewriteWebhookURL *RewriteWebhookURLSpec `json:"rewriteWebhookURL,omitempty"`
    FilterKinds       *FilterKindsSpec       `json:"filterKinds,omitempty"`
}

type PatchSpec struct {
    Target Selector `json:"target"`

    // Exactly one of the two must be set. Validated by a CEL rule on the CRD:
    //   has(self.strategicMerge) != has(self.jsonPatch)
    //
    // strategicMerge is applied as a Kubernetes strategic-merge patch
    // (list-key aware; merges arrays by identity fields).
    // Marked +kubebuilder:pruning:PreserveUnknownFields so arbitrary
    // patch content is accepted (structural validation only).
    // +kubebuilder:validation:Schemaless
    // +kubebuilder:pruning:PreserveUnknownFields
    StrategicMerge *apiextensionsv1.JSON `json:"strategicMerge,omitempty"`

    // jsonPatch is a list of RFC 6902 operations.
    // Each op is admission-validated (op enum, required fields).
    JSONPatch []JSONPatchOp `json:"jsonPatch,omitempty"`
}

// JSONPatchOp is one operation in a JSON Patch (RFC 6902).
type JSONPatchOp struct {
    // Op is the operation kind.
    // +kubebuilder:validation:Enum=add;remove;replace;move;copy;test
    Op string `json:"op"`

    // Path is a JSON Pointer identifying the target location.
    // +kubebuilder:validation:MinLength=1
    Path string `json:"path"`

    // From is used by move and copy operations.
    From string `json:"from,omitempty"`

    // Value is the value for add/replace/test operations.
    // +kubebuilder:validation:Schemaless
    // +kubebuilder:pruning:PreserveUnknownFields
    Value *apiextensionsv1.JSON `json:"value,omitempty"`
}

type RewriteWebhookURLSpec struct {
    URLPrefix string `json:"urlPrefix"`
}

type FilterKindsSpec struct {
    Kinds  []string `json:"kinds"`
    Source string   `json:"source,omitempty"`
}

// NOTE (r7): PackageWebhookConfigsForInjectorSpec was scaffolded in the archived
// Phase 0+1 change and still exists in api/v1alpha1/dualdeploymentoperator_types.go
// (+ zz_generated.deepcopy.go + the CEL union rule + two test files). Under r7 the
// cross-stream transformation is removed (design.md §2.2.4), so this struct, its
// Transformation field, its DeepCopy, its CEL term, and its test references must be
// DELETED as a Phase-3 (or dedicated CRD) follow-up, then `make manifests generate`
// re-run. See "r7 CRD cleanup" below.

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
- `+kubebuilder:validation:Enum=add;remove;replace;move;copy;test` on `JSONPatchOp.Op`
- `+kubebuilder:pruning:PreserveUnknownFields` on `PatchSpec.StrategicMerge` and `JSONPatchOp.Value` (arbitrary content allowed inside the object)

Discriminator constraints via CEL (Kubernetes 1.29+, cleaner than validating webhook):

```go
// On Source:
// +kubebuilder:validation:XValidation:rule="has(self.helm) != has(self.kustomize)",message="exactly one of source.helm or source.kustomize must be set"

// On PatchSpec:
// +kubebuilder:validation:XValidation:rule="has(self.strategicMerge) != has(self.jsonPatch)",message="exactly one of patch.strategicMerge or patch.jsonPatch must be set"

// On Transformation (r7 — 3 per-render types; packageWebhookConfigsForInjector term removed):
// +kubebuilder:validation:XValidation:rule="(has(self.patch) ? 1 : 0) + (has(self.rewriteWebhookURL) ? 1 : 0) + (has(self.filterKinds) ? 1 : 0) == 1",message="exactly one transformation type must be set per entry"

// On KustomizeSource.URL:
// +kubebuilder:validation:XValidation:rule="self.matches('.*[?&]ref=.+')",message="kustomize url must include a pinned ref= parameter"

// On HelmSource.Version:
// +kubebuilder:validation:MinLength=1
// (semver-strict validation deferred to webhook — CEL can't validate semver format cleanly)
```

**Admission-time validation gained by typed variants** (r6 refinement):
- `strategicMerge` must be an object (not string, not array) — enforced by JSON Schema type
- `jsonPatch` must be an array of ops with valid `op` enum and required `path` — enforced by JSON Schema type + kubebuilder enum
- CEL rule enforces exactly-one-of

**Not caught at admission** (deferred to runtime + planned admission webhook in v2):
- Content-level errors inside `strategicMerge` (wrong field types, unknown fields against target's schema)
- `jsonPatch` paths that don't exist on target
- Whether the patch will apply cleanly

See design.md §9.9 for planned admission webhook.

---

## Phase 2: Source renderers (~3-4 days)

### Interface

```go
package source

import "context"

type Mode string
const (
    ModeHost   Mode = "host"
    ModeRemote Mode = "remote"
)

// Source renders the manifest stream for a specific mode into a target namespace.
type Source interface {
    Render(ctx context.Context, mode Mode, namespace string) ([]manifest.Manifest, error)
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

func (h *Helm) Render(ctx context.Context, mode Mode, namespace string) ([]manifest.Manifest, error) {
    // 1. Set up registry client (OCI auth if needed)
    settings := cli.New()
    registryClient, _ := registry.NewClient(...)

    // 2. Pull chart to temp dir (cached across renders of same repo+name+version)
    puller := action.NewPullWithOpts(action.WithConfig(...))
    puller.RepoURL = h.spec.Repo
    puller.Version = h.spec.Version
    puller.DestDir = tmpDir
    // ...

    // 3. Load chart
    chart, err := loader.Load(chartPath)

    // 4. Merge values in precedence order:
    //    chart.values.yaml (chart defaults)
    //      < spec.Values                     (common per-cluster)
    //      < spec.HostValues or spec.RemoteValues (mode-specific)
    //      < {mode: "host"|"remote"}         (operator-injected)
    values := mergeValues(
        chart.Values,          // chart defaults (already loaded)
        parseValues(h.spec.Values),
        parseValues(h.spec.modeValues(mode)),  // returns HostValues or RemoteValues
        map[string]any{"mode": string(mode)},
    )

    // Reject if user tried to set mode via values (only operator sets it)
    // ...admission validation preferred; runtime check as safeguard...

    // 5. Render templates
    installer := action.NewInstall(cfg)
    installer.DryRun = true
    installer.ReleaseName = h.spec.Name
    installer.ClientOnly = true
    installer.IncludeCRDs = true
    installer.Namespace = "..."

    release, err := installer.Run(chart, values)

    // 6. Parse rendered YAML → []Manifest, tagged with origin: upstream
    //    (chart's own templates already carry origin: additions via _helpers.tpl)
    return parseManifestsWithOriginFallback(release.Manifest, OriginUpstream), nil
}

func (spec *v1alpha1.HelmSource) modeValues(mode Mode) *apiextensionsv1.JSON {
    switch mode {
    case ModeHost:   return spec.HostValues
    case ModeRemote: return spec.RemoteValues
    }
    return nil
}
```

**Key concerns**:
- OCI registry auth for private repos (`keppel.eu-de-1.cloud.sap`) — reuse existing pull secrets
- Chart caching to avoid re-pulling on every reconcile AND every render (LRU cache keyed by repo+name+version; both renders in one reconcile share the cached chart)
- Values merging — mode-specific values override common values, per Helm precedence
- Post-render origin tagging: manifests carrying `dual-deployment-operator.cc.sap/origin: additions` (set by chart's `_helpers.tpl`) keep that origin; all others get `origin: upstream`

### Kustomize renderer

```go
package source

type Kustomize struct {
    spec *v1alpha1.KustomizeSource
}

func (k *Kustomize) Render(ctx context.Context, mode Mode, namespace string) ([]manifest.Manifest, error) {
    // Determine overlay path per mode
    var subPath string
    switch mode {
    case ModeHost:
        subPath = k.spec.HostPath
        if subPath == "" { subPath = "host" }
    case ModeRemote:
        subPath = k.spec.RemotePath
        if subPath == "" { subPath = "remote" }
    }

    // Construct root URL: base URL + subpath (preserving query string / ?ref=)
    rootURL := joinURLPath(k.spec.URL, subPath)

    // 1. Set up krusty options
    opts := krusty.MakeDefaultOptions()
    opts.LoadRestrictions = types.LoadRestrictionsNone   // allow remote refs

    // 2. Init file system with remote support
    fSys := filesys.MakeFsOnDisk()  // krusty handles URL resolution

    // 3. Kustomize build the mode-specific overlay
    kk := krusty.MakeKustomizer(opts)
    resmap, err := kk.Run(fSys, rootURL)
    if err != nil { return nil, err }

    // 4. Serialize
    yaml, err := resmap.AsYaml()

    // 5. Parse → []Manifest, respecting origin annotations set by
    //    additions/*/kustomization.yaml (commonAnnotations); default upstream.
    return parseManifestsWithOriginFallback(yaml, OriginUpstream), nil
}
```

**Key concerns**:
- No value projection. `spec.source.kustomize` has no `values` field. Per-CR parameterization for kustomize is via mode selection (`hostPath`/`remotePath`) or, if needed, future kustomize-native fields (`images`, `patches` — see design.md §9.4). Not a Helm values map.
- Ref pinning — reject URLs without `?ref=<sha|tag>` at admission time
- Overlay layout: kustomize source must have two subdirectories (`host/` and `remote/` by default), each with its own `kustomization.yaml` selecting the appropriate resources for that mode. See design.md §4.2 for the ipam-capi layout.
- Origin tagging: chart authors add `commonAnnotations: {dual-deployment-operator.cc.sap/origin: additions}` on the `additions/host/` and `additions/remote/` kustomization files. Other resources (upstream) default to `origin: upstream`.

---

## Phase 3: Transformations (~1 week)

### r7 CRD cleanup (do this first)

The archived Phase 0+1 scaffold shipped a `PackageWebhookConfigsForInjectorSpec` CRD type for the r5/r6 cross-stream transformation. r7 removes that transformation (design.md §2.2.4), so before implementing the transform package, delete the now-dead type and regenerate:

1. In [`api/v1alpha1/dualdeploymentoperator_types.go`](../api/v1alpha1/dualdeploymentoperator_types.go): remove the `PackageWebhookConfigsForInjector *PackageWebhookConfigsForInjectorSpec` field from `Transformation`, remove the `PackageWebhookConfigsForInjectorSpec` struct, and drop the `packageWebhookConfigsForInjector` term from the `Transformation` `XValidation` CEL rule (leaving the 3-way `patch`/`rewriteWebhookURL`/`filterKinds` count).
2. Remove the `PackageWebhookConfigsForInjector` references from the type tests ([`dualdeploymentoperator_types_test.go`](../api/v1alpha1/dualdeploymentoperator_types_test.go)) and any CEL test that constructs it.
3. Run `make manifests generate` to regenerate the CRD YAML, RBAC, and `zz_generated.deepcopy.go` (which currently has `PackageWebhookConfigsForInjectorSpec` DeepCopy methods).
4. `make build test lint-fix` green.

This is a breaking CRD change, but the field is unused (Phase 3 was never implemented) and no CR in the fleet sets it yet, so no data migration is needed.

### Interface

A single transformation interface (r7 — cross-stream scope removed). The reconciler applies each declared transformation to both renders independently, in declaration order.

```go
package transform

// Transformation operates on a single render's manifest stream.
type Transformation interface {
    Type() string
    Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error)
}

// Build parses spec.Transformations into an ordered []Transformation,
// preserving declaration order. (No scope split — every type is per-render.)
func Build(specs []v1alpha1.Transformation) ([]Transformation, error) {
    var out []Transformation
    for _, spec := range specs {
        switch {
        case spec.Patch != nil:
            out = append(out, &patch{spec: spec.Patch})
        case spec.RewriteWebhookURL != nil:
            out = append(out, &rewriteWebhookURL{spec: spec.RewriteWebhookURL})
        case spec.FilterKinds != nil:
            out = append(out, &filterKinds{spec: spec.FilterKinds})
        default:
            return nil, errors.New("no transformation type set in entry")
        }
    }
    return out, nil
}
```

### Webhook-injector integration (r7): label, don't package

There is **no** `packageWebhookConfigsForInjector` transformation in r7. The operator applies WebhookConfigurations (and conversion-webhook CRDs) directly to the shoot in the remote render; the only requirement is that those objects carry the webhook-injector's `--target-label` so its target patch mode adopts them and keeps `.caBundle` current (design.md §3.4.4, §3.8).

That labeling is done with the existing `patch` transformation — no new code:

```yaml
- patch:
    target: {kind: ValidatingWebhookConfiguration}
    strategicMerge:
      metadata:
        labels:
          dual-deployment-operator.cc.sap/webhook-injector: metal-operator
# (repeat for MutatingWebhookConfiguration and, if present, conversion-webhook CustomResourceDefinition)
```

Two consequences for the delivery layer (Phase 5):
- The operator applies these objects via SSA with the `caBundle` field **unset**, so its field manager never owns `caBundle` (the injector owns it). See the caBundle handling section below.
- No `manifest.SerializeMultiDoc` helper is needed for injector integration (it was only used to serialize WebhookConfigs into the packaged ConfigMap). If a later phase needs multi-doc serialization for another reason, add it then.

### rewriteWebhookURL walks three kinds (r7)

`rewriteWebhookURL.Apply` MUST rewrite service-based `clientConfig` to URL-based on:
- `ValidatingWebhookConfiguration` / `MutatingWebhookConfiguration`: iterate `.webhooks[]`, and for each entry whose `.clientConfig.service` is set, replace it with `.clientConfig.url = urlPrefix + service.path` (path defaults to `""` if absent).
- `CustomResourceDefinition`: only when `.spec.conversion.strategy == "Webhook"` and `.spec.conversion.webhook.clientConfig.service` is set, replace it with `.spec.conversion.webhook.clientConfig.url = urlPrefix + service.path`.

Rules for all three: an existing `.url` is left unchanged (idempotent); `.caBundle` is preserved (the injector owns it); a manifest of any other kind, or one already using `.url`, is a no-op. This closes the CRD conversion-webhook Service→URL gap (design.md §3.4.2, §9.2). Table-driven tests MUST include a conversion-webhook CRD fixture (service→url rewritten, caBundle preserved) and a non-webhook CRD fixture (untouched).

### Table-driven tests

For each transformation, structure tests as:

```go
func TestPatch(t *testing.T) {
    tests := []struct {
        name    string
        input   []manifest.Manifest
        spec    *v1alpha1.PatchSpec
        want    []manifest.Manifest
        wantErr string
    }{
        {
            name: "strategicMerge injects sidecar into matching Deployment",
            input: []manifest.Manifest{fixture("upstream-deployment.yaml")},
            spec: &v1alpha1.PatchSpec{
                Target: v1alpha1.Selector{Kind: "Deployment", Name: "controller-manager"},
                StrategicMerge: mustJSON(`{
                    "spec": {
                        "template": {
                            "spec": {
                                "initContainers": [
                                    {"name": "webhook-injector", "image": "keppel.../webhook-injector:sha-abc"}
                                ],
                                "volumes": [
                                    {"name": "webhook-certs", "emptyDir": {}}
                                ]
                            }
                        }
                    }
                }`),
            },
            want: []manifest.Manifest{fixture("expected-deployment-with-sidecar.yaml")},
        },
        {
            name: "strategicMerge adds label to matching CRDs",
            input: []manifest.Manifest{fixture("upstream-crd.yaml")},
            spec: &v1alpha1.PatchSpec{
                Target: v1alpha1.Selector{Kind: "CustomResourceDefinition"},
                StrategicMerge: mustJSON(`{
                    "metadata": {
                        "labels": {"metal-operator-remote-webhook-injector": "true"}
                    }
                }`),
            },
            want: []manifest.Manifest{fixture("expected-crd-with-label.yaml")},
        },
        {
            name: "jsonPatch applies operations by path",
            input: []manifest.Manifest{fixture("some-deployment.yaml")},
            spec: &v1alpha1.PatchSpec{
                Target: v1alpha1.Selector{Kind: "Deployment"},
                JSONPatch: []v1alpha1.JSONPatchOp{
                    {Op: "replace", Path: "/spec/replicas", Value: mustJSON(`3`)},
                },
            },
            want: []manifest.Manifest{fixture("expected-deployment-3-replicas.yaml")},
        },
        {
            name: "error when no matching resource",
            input: []manifest.Manifest{fixture("unrelated.yaml")},
            spec: &v1alpha1.PatchSpec{
                Target: v1alpha1.Selector{Kind: "Deployment", Name: "does-not-exist"},
                StrategicMerge: mustJSON(`{}`),
            },
            wantErr: "no matching resource",
        },
        {
            name: "error when both strategicMerge and jsonPatch set",
            input: []manifest.Manifest{fixture("some-deployment.yaml")},
            spec: &v1alpha1.PatchSpec{
                Target: v1alpha1.Selector{Kind: "Deployment"},
                StrategicMerge: mustJSON(`{}`),
                JSONPatch: []v1alpha1.JSONPatchOp{{Op: "test", Path: "/foo"}},
            },
            wantErr: "exactly one of strategicMerge or jsonPatch must be set",
        },
        {
            name: "error when neither variant set",
            input: []manifest.Manifest{fixture("some-deployment.yaml")},
            spec: &v1alpha1.PatchSpec{
                Target: v1alpha1.Selector{Kind: "Deployment"},
            },
            wantErr: "exactly one of strategicMerge or jsonPatch must be set",
        },
        // Additional cases: invalid JSON Patch path, strategic-merge conflict, etc.
    }
    // ...
}

// Helper for building apiextensionsv1.JSON values in tests
func mustJSON(s string) *apiextensionsv1.JSON {
    return &apiextensionsv1.JSON{Raw: []byte(s)}
}
```

Similar tests for `rewriteWebhookURL` and `filterKinds`. (No `packageWebhookConfigsForInjector` in r7 — injector integration is a `patch` that stamps `--target-label`, covered by the `patch` tests.)

### `patch` implementation

```go
package transform

import (
    "encoding/json"
    "k8s.io/apimachinery/pkg/util/strategicpatch"
    jsonpatch "github.com/evanphx/json-patch"
)

type patch struct {
    spec *v1alpha1.PatchSpec
}

func (p *patch) Type() string { return "patch" }

func (p *patch) Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error) {
    // Validate that exactly one variant is set. Should be enforced by CEL at
    // admission, but re-check here as a safety net for older API-server versions
    // or CRDs installed without CEL.
    if (p.spec.StrategicMerge == nil) == (p.spec.JSONPatch == nil) {
        return nil, fmt.Errorf("patch: exactly one of strategicMerge or jsonPatch must be set")
    }

    // Prepare the patch bytes once — same patch applied to all matched resources.
    var patchBytes []byte
    var isStrategic bool
    switch {
    case p.spec.StrategicMerge != nil:
        isStrategic = true
        patchBytes = p.spec.StrategicMerge.Raw   // already JSON via apiextensionsv1.JSON
    case p.spec.JSONPatch != nil:
        isStrategic = false
        // Convert []JSONPatchOp back to JSON array for the patch library
        var err error
        patchBytes, err = json.Marshal(p.spec.JSONPatch)
        if err != nil {
            return nil, fmt.Errorf("patch: failed to marshal jsonPatch: %w", err)
        }
    }

    matched := 0
    for i, m := range manifests {
        if !Match(m, p.spec.Target) {
            continue
        }
        matched++

        // Get current resource as JSON
        originalBytes, err := json.Marshal(m.Unstructured.Object)
        if err != nil { return nil, err }

        var mergedBytes []byte
        if isStrategic {
            // Strategic-merge patch, Kubernetes-list-key-aware.
            // Uses m.Unstructured.Object as the schema hint — for core types
            // this triggers the built-in openapi schema for list merging.
            mergedBytes, err = strategicpatch.StrategicMergePatch(originalBytes, patchBytes, m.Unstructured.Object)
        } else {
            jp, decodeErr := jsonpatch.DecodePatch(patchBytes)
            if decodeErr != nil {
                return nil, fmt.Errorf("patch: failed to decode json patch: %w", decodeErr)
            }
            mergedBytes, err = jp.Apply(originalBytes)
        }
        if err != nil {
            return nil, fmt.Errorf("patch: failed to apply to %s/%s: %w", m.GetKind(), m.GetName(), err)
        }

        // Deserialize back into unstructured
        var newObj map[string]interface{}
        if err := json.Unmarshal(mergedBytes, &newObj); err != nil { return nil, err }
        manifests[i].Unstructured.Object = newObj
    }

    if matched == 0 {
        return nil, fmt.Errorf("patch: no matching resource for target %+v", p.spec.Target)
    }
    return manifests, nil
}
```

Notes on strategic-merge patching:
- Kubernetes strategic merge requires a schema for merge-key resolution (which array items are "the same"). For core Kubernetes types, `strategicpatch.StrategicMergePatch` handles this via the built-in openapi schema.
- For custom types (CRDs), strategic-merge may fall back to JSON merge patch behavior. In practice, for our transformations (patching Deployment, adding labels to CRDs), the core-type behavior is what we need.
- If strategic-merge on a CRD becomes an issue, users can switch to `jsonPatch` with an explicit JSON Patch.



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

## Phase 4: (No split needed — two-render pattern)

Under the two-render architecture (design.md §3.5), there is no split step. Each render produces manifests destined entirely for one target cluster:

- `Render(ctx, ModeHost)` → all host resources
- `Render(ctx, ModeRemote)` → all remote resources

Transformations run on each render independently. Each transformation naturally affects only what's present in that render (e.g., a sidecar-injecting `patch` matches a Deployment only in the host render; `rewriteWebhookURL` matches Validating/Mutating WebhookConfigurations and conversion-webhook CRDs only in the remote render).

**No `internal/split/` package needed.** The reconcile loop (Phase 6) iterates over each render and applies the same transformations to both. Delivery (Phase 5) applies each render's output to its respective cluster.

Skip this phase's implementation. Time budget rolls into Phase 6.

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

### SSA field-manager coexistence with webhook-injector (r7)

Under r7 the operator **applies** WebhookConfigurations and conversion-webhook CRDs to the shoot itself (they are no longer packaged into a ConfigMap), but **omits the `caBundle` field** on apply so its SSA field manager never owns it. The webhook-injector's target patch mode owns `caBundle` (design.md §3.8). Disjoint fields, same object.

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

Because the operator applies with `caBundle` stripped, SSA does not record `dual-deployment-operator` as the field manager for `caBundle`; the injector's per-webhook strategic-merge patch (and `MergeFrom` on CRD conversion) sets it and keeps it. The operator's periodic re-apply omits `caBundle`, so it never reverts the injector's write; the injector patches only `caBundle`, so it never disturbs operator-owned fields. No caBundle ping-pong, no SSA `force` conflicts on shared fields.

The operator must also stamp the injector's `--target-label` on these objects (via the `patch` transformation, §Phase 3 above) so the injector's target patch mode adopts them.

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

    // 1. Build source renderer
    src, err := source.From(cr.Spec.Source)
    if err != nil {
        return r.errStatus(ctx, cr, "InvalidSource", err)
    }

    // 2. Render TWICE — one per mode. Host uses the CR's own namespace;
    //    remote uses spec.remoteNamespace.
    hostManifests, err := src.Render(ctx, source.ModeHost, cr.Namespace)
    if err != nil { return r.errStatus(ctx, cr, "HostRenderFailed", err) }

    remoteManifests, err := src.Render(ctx, source.ModeRemote, cr.Spec.RemoteNamespace)
    if err != nil { return r.errStatus(ctx, cr, "RemoteRenderFailed", err) }

    // 3. Build the ordered transformation list (single per-render scope, r7).
    transforms, err := transform.Build(cr.Spec.Transformations)
    if err != nil { return r.errStatus(ctx, cr, "InvalidTransformation", err) }

    // 4. Apply each transformation to both renders independently, in
    //    declaration order. Each transformation naturally affects only
    //    resources present in the render it runs on. (No cross-stream phase.)
    for _, t := range transforms {
        hostManifests, err = t.Apply(hostManifests)
        if err != nil { return r.errStatus(ctx, cr, "HostTransformFailed", err) }
        remoteManifests, err = t.Apply(remoteManifests)
        if err != nil { return r.errStatus(ctx, cr, "RemoteTransformFailed", err) }
    }

    // 5. Get clients
    hostApplier := r.HostApplier   // preconstructed at startup
    shootApplier, err := r.buildShootApplier(ctx, cr)
    if err != nil {
        return r.errStatus(ctx, cr, "ShootClientFailed", err)
    }

    // 6. Apply each render to its target cluster. WebhookConfigurations and
    //    conversion-webhook CRDs are applied with caBundle stripped (the
    //    webhook-injector owns that field via its target patch mode).
    hostStatuses := r.applyAll(ctx, hostApplier, hostManifests)
    remoteStatuses := r.applyAll(ctx, shootApplier, remoteManifests)

    // 7. Update status
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

**Key differences from a single-render architecture**:
- Source is rendered twice with mode-specific parameters.
- No `split` step — each render's output is a coherent set for its target cluster.
- Transformations apply to each render independently. `filterKinds {kinds: [Service]}` runs on both, dropping Services from whichever render emits them.
- Only `origin` matters for transformation targeting; there is no `target` on manifests.

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

## Phase 9 (v1 scaffolding only): Validating admission webhook

Kubebuilder scaffold created but not wired in v1. Structural validation is provided by CRD schema + CEL rules (§Validation above). Content-level validation of `patch` bodies is deferred to a v2 webhook.

**Scaffold in v1**:

```bash
kubebuilder create webhook \
  --group dual-deployment-operator \
  --version v1alpha1 \
  --kind DualDeploymentOperator \
  --programmatic-validation
```

This generates:
- `internal/webhook/dualdeploymentoperator_webhook.go` — CustomValidator stub
- `config/webhook/` — webhook Service + Configuration + patch overlay
- `config/certmanager/` — cert-manager Issuer + Certificate (or manual cert plumbing)

**v1**: leave the CustomValidator methods as no-op (return `nil` immediately). Do not deploy the webhook ValidatingWebhookConfiguration. CRD schema + CEL handle all v1 admission validation.

**v2** (deferred): implement `ValidateCreate` and `ValidateUpdate`:
- Parse `spec.transformations[].patch.strategicMerge` / `.jsonPatch`
- Look up target kind's OpenAPI schema via `discovery.DiscoveryClient` + `openapi.OpenAPISchema()`
- Validate patch content against the target schema (uses `k8s.io/apimachinery/pkg/util/validation` primitives)
- Reject with detailed field-path errors

Also v2: cert-manager or self-signed cert rotation, ValidatingWebhookConfiguration deployment via operator's own chart.

Failure mode if v1 CR contains invalid patch content: reconcile fails, CR status shows condition `PatchApplicationFailed` with the error. Same as any other transformation failure.

**Success criterion**: webhook scaffold builds; no-op ValidateCreate/ValidateUpdate methods pass tests; ValidatingWebhookConfiguration NOT deployed by v1's operator chart.

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
    github.com/evanphx/json-patch v5.9.0
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
| 1 | CRD types compile; deepcopy generated; validation webhook (or CEL) rejects invalid discriminators and unpinned kustomize URLs |
| 2 | Both Helm and kustomize renderers produce parsable manifest streams for host and remote modes on a metal-operator fixture; origin tags correct |
| 3 | Each transformation's unit tests pass with table-driven cases; a single per-render `Transformation` interface + `Build()` implemented (no cross-stream scope); a `patch` that stamps the injector's `--target-label` onto WebhookConfigurations/CRDs is covered. Also: the scaffolded `PackageWebhookConfigsForInjectorSpec` CRD type is removed (see "r7 CRD cleanup") and `make manifests generate` re-run |
| 4 | (skipped — no split step) |
| 5 | Applier applies a resource via SSA and returns Healthy status; can also delete; strips caBundle from WebhookConfigurations and conversion-webhook CRDs before apply (so the injector owns caBundle) |
| 6 | Reconciler successfully processes a CR end-to-end with mocked source; applies the ordered transformation list to both renders; produces host/remote manifest sets where the remote set includes WebhookConfigurations (caBundle stripped, injector-labeled); status populated |
| 7 | Equivalence test passes for at least metal-operator vs. today's chart output (host render matches host-side output; remote render matches remote-side output) |
| 8 | Operator chart installs successfully in a QA shoot-cp namespace |
| 9 | Webhook scaffold builds; no-op ValidateCreate/ValidateUpdate methods pass tests; ValidatingWebhookConfiguration NOT deployed in v1 (deferred to v2) |

Each phase should merge to `main` with tests before moving to the next.

---

## Notes on chart-side changes (out of this repo's scope)

These changes happen in `sapcc/helm-charts`, not in this operator repo, but they gate the operator's usefulness:

- Restructure `system/metal-operator-remote/` (and other wrapper charts) per `design.md` §4.1: split templates into `templates/host/` and `templates/remote/`, invert upstream enable values, add annotation helpers, delete pre-rendered files
- Restructure `system/kustomize/ipam-capi-remote/` per §4.2: top-level kustomization, additions/ subdir, pin refs
- Verify webhook-injector uses distinct SSA field manager (small injector code change if not)

Do the operator work first; chart restructures follow once the operator is validated against today's chart output in equivalence tests.
