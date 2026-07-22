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
    RemoteAccess      RemoteAccessRef           `json:"remoteAccess"`
    // RemoteNamespace is the target namespace for the remote (shoot) render and
    // delivery. Namespaced resources in the remote render that omit an explicit
    // metadata.namespace are placed here; cluster-scoped resources are unaffected.
    // The host render/delivery uses the CR's own metadata.namespace.
    RemoteNamespace   string                    `json:"remoteNamespace"`
    Transformations   []Transformation          `json:"transformations,omitempty"`
    RetentionPolicy   RetentionPolicy           `json:"retentionPolicy,omitempty"`
    // ApplyOrder controls which cluster's render is applied first each reconcile.
    // Deletion and prune run the reverse order. Default: RemoteFirst.
    // +kubebuilder:validation:Enum=HostFirst;RemoteFirst
    // +kubebuilder:default=RemoteFirst
    ApplyOrder        string                    `json:"applyOrder,omitempty"`
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

type RemoteAccessRef struct {
    // SecretName names a Gardener token-requestor Secret (token + bundle.crt) in the CR's namespace.
    SecretName string `json:"secretName"`
    // Server is the shoot API server URL (required; the operator runs in the seed and cannot infer it).
    Server     string `json:"server"`
    // TokenKey / CAKey default to Gardener's conventions when empty.
    TokenKey   string `json:"tokenKey,omitempty"`  // default "token"
    CAKey      string `json:"caKey,omitempty"`      // default "bundle.crt"
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

type RetentionPolicy struct {
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
- `+kubebuilder:validation:Enum=Retain;Delete` on `RetentionPolicy.CRDs`
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
- OCI registry access — charts are served **anonymously** from `keppel.global.cloud.sap/ccloud-helm/...` (verified); no credentials needed for the current operators. See Phase 7 for the (optional, off-by-default) auth seam.
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

> **Why strip before apply.** The `dual-deployment-operator.cc.sap/origin` annotation
> (see [`internal/manifest`](../internal/manifest/manifest.go)) is a chart/kustomization
> **authorship** signal, consumed once at parse time (`originOf`) to set `Manifest.Origin`
> and used only in-memory by `patch`/`filterKinds` transformations. It is **not** a
> routing/destination signal (destination is implicit from which render produced the
> resource) and has no meaning inside the target cluster. Because the operator applies
> with server-side apply, any annotation it writes becomes operator-**owned** and sticks
> across reconciles — so it must be stripped before `Patch`, otherwise every managed
> workload carries a dangling internal annotation forever. `StripInternalAnnotations` must
> also drop `metadata.annotations` to `nil` if it becomes empty after removal, to avoid
> emitting an empty `annotations: {}` on the object.

```go
func (a *SSAApplier) Apply(ctx context.Context, m manifest.Manifest, ownedBy string) (ResourceStatus, error) {
    // Strip internal (origin) annotations before applying — they are parse-time
    // authorship signals, not something that should live in the target cluster.
    m.StripInternalAnnotations()

    // Stamp the CR-identity ownership label (key dual-deployment-operator.cc.sap/owned-by).
    // ownedBy is a fixed-length hash from manifest.OwnedByValue(cr.Namespace, cr.Name) —
    // NOT a raw "<ns>_<name>" (that can exceed the 63-char label-value limit and is
    // non-injective). Passed as an ARGUMENT, not a struct field, so the applier stays
    // stateless and is safely shared across concurrent reconciles. Reused by prune
    // safety AND the cluster-scoped conflict guard.
    m.SetOwnedByLabel(ownedBy)

    status := ResourceStatus{
        Kind:       m.GetKind(),
        APIVersion: m.GetAPIVersion(),
        Namespace:  m.GetNamespace(),
        Name:       m.GetName(),
        LastApplied: &metav1.Time{Time: time.Now()},
    }

    // Defensive cluster-scoped conflict guard. ForceOwnership would silently seize a
    // same-named cluster-scoped object from another CR (single-install-per-seed
    // violation → last-writer-wins corruption). For cluster-scoped kinds only, GET
    // first and refuse if the live object is owned by a DIFFERENT CR. Absent,
    // self-owned, or unlabeled → safe to proceed (unlabeled = adopt). Namespaced
    // objects skip this — namespace isolation already prevents collision.
    if manifest.IsClusterScoped(m.GetKind()) {
        if owner, conflict := a.foreignOwner(ctx, m, ownedBy); conflict {
            status.Health = HealthDegraded
            status.Message = fmt.Sprintf("cluster-scoped %s %q already owned by a different CR %q; refusing to overwrite (single-install-per-seed)",
                m.GetKind(), m.GetName(), owner)
            return status, fmt.Errorf("%s", status.Message)
        }
    }

    err := a.Client.Patch(ctx, m.Unstructured, client.Apply,
        client.FieldOwner(a.FieldManager),
        client.ForceOwnership)
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

// foreignOwner GETs the live cluster-scoped object and reports whether it carries an
// owned-by label naming a CR OTHER than ownedBy. NotFound / no label / our own label =>
// not a conflict (nil object or unlabeled is safe to adopt).
func (a *SSAApplier) foreignOwner(ctx context.Context, m manifest.Manifest, ownedBy string) (string, bool) {
    live, err := a.get(ctx, m)
    if err != nil || live == nil {
        return "", false // absent (or unreadable) → let the apply proceed/report
    }
    owner := live.GetLabels()["dual-deployment-operator.cc.sap/owned-by"]
    return owner, owner != "" && owner != ownedBy
}
```

> **`SSAApplier` stays stateless** — it holds only `Client`, `FieldManager`, and `Cluster`; the per-CR ownership value is passed as the `ownedBy` argument to `Apply`, not stored on the struct, so one host/shoot applier instance is safely reused across concurrent reconciles of different CRs. The reconciler derives `ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)` once per reconcile and threads it through every `Apply` call. `manifest.IsClusterScoped(kind)` already exists (used by `ApplyNamespace`); reuse it here. The guard costs one extra GET **only** for cluster-scoped kinds.

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
    // ValidatingWebhookConfiguration / MutatingWebhookConfiguration are intentionally
    // NOT special-cased: the operator does not own caBundle (§3.8), so it must not grade
    // health on it. They fall through to the default (exists => Healthy). The injector owns
    // caBundle population and its own observability.
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
```

> **No `webhookConfigHealth`.** The operator does not compute health from a WebhookConfiguration's `caBundle` — it does not own that field (§3.8), so grading health on it would be inconsistent and would flap `Progressing` during the normal bootstrap window for a condition the operator cannot fix. WebhookConfigs (and conversion-webhook CRDs, whose health is `crdHealth`/`Established` only) report `Healthy` on existence. Populating and observing caBundle is the webhook-injector's responsibility. (A future, non-gating "caBundle stamped" signal is a possible enhancement — see design.md Open Questions.)

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

**The strip must be leaf-only and unconditional.** `stripCABundleFromWebhooks` removes **only** the `clientConfig.caBundle` leaf on each `webhooks[]` entry (`unstructured.RemoveNestedField(wh, "clientConfig", "caBundle")`); `stripCABundleFromCRDConversion` removes only `spec.conversion.webhook.clientConfig.caBundle`. Neither ever removes the parent `clientConfig` map (which holds the operator-owned `url`) nor the webhook entry itself. The strip runs on **every** apply, including the first — never conditionally "only if present". Rationale: if the operator applied `caBundle` even once (even `""`), SSA would record `dual-deployment-operator` as its field manager; a later apply omitting it under `ForceOwnership` would then **delete** the injector's caBundle, opening a webhook-outage window until the injector re-patched. A discriminating unit test asserts the operator's `managedFields` entry never contains a `caBundle` path.

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

    // 5. Get clients. shootPhase captures the remote availability outcome:
    //    ready | credsNotReady | clientFailed. Only `ready` means the remote
    //    render can be attempted.
    hostApplier := r.HostApplier   // preconstructed at startup
    shootApplier, err := r.buildShootApplier(ctx, cr)
    var shootPhase string
    switch {
    case err == nil:
        shootPhase = "ready"
    case errors.Is(err, errShootCredentialsNotReady):
        shootPhase = "credsNotReady" // benign bootstrap wait (Gardener not done)
    default:
        shootPhase = "clientFailed"  // missing Secret / bad server / unreachable
    }

    // 6. Sort each render by the fixed intra-render kind-priority
    //    (Namespace -> CRD -> RBAC -> workloads -> webhooks) so the first apply
    //    never fails on a missing CRD or Namespace.
    hostManifests = deliver.SortForApply(hostManifests)
    remoteManifests = deliver.SortForApply(remoteManifests)

    // 7. Apply per spec.applyOrder, respecting remote-failure severity.
    //    RemoteFirst gates the host render on remote availability (host depends on
    //    the remote coming up first); HostFirst never gates host on the remote.
    //    Three remote outcomes:
    //      - complete failure (client unbuildable, or 0 of N remote applied)
    //          RemoteFirst  -> STOP before host; Ready=False RemoteApplyFailed
    //          HostFirst    -> apply host first; flag RemoteApplyFailed (non-blocking)
    //      - credentials not ready (benign bootstrap wait)
    //          RemoteFirst  -> DEFER host too; Ready=False WaitingForShootCredentials
    //          HostFirst    -> apply host first; flag WaitingForShootCredentials
    //      - ready -> apply remote, then gate host per applyOrder. Under RemoteFirst
    //          ANY remote failure (partial ResourcesDegraded or complete
    //          RemoteApplyFailed) stops before host — the workless shoot's render is
    //          all structural deps host consumes. Under HostFirst host applies first.
    //    WebhookConfigs/conversion CRDs are applied with caBundle stripped.
    remoteFirst := cr.Spec.ApplyOrder != "HostFirst"
    var hostStatuses, remoteStatuses []v1alpha1.ResourceStatus
    remoteStatuses = cr.Status.RemoteResources // preserve prior remote status by default

    // Derive the ownership value once; thread it through every apply (the appliers
    // are stateless/shared, so ownedBy is an argument, never a struct field).
    ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

    applyHost := func() { hostStatuses = r.applyAll(ctx, hostApplier, hostManifests, ownedBy) }

    switch shootPhase {
    case "credsNotReady":
        if !remoteFirst {
            applyHost() // HostFirst: host does not wait on remote
        }
        // RemoteFirst: defer host until credentials populate.
        r.setCondition(cr, metav1.ConditionFalse, "WaitingForShootCredentials",
            "shoot token/CA not yet populated by Gardener; remote render deferred")
        return r.finishNotReady(ctx, cr, hostStatuses, remoteStatuses, 30*time.Second)

    case "clientFailed":
        if !remoteFirst {
            applyHost() // HostFirst: host proceeds; remote failure only flagged
        }
        // RemoteFirst: stop before host — host must not start without the remote.
        r.setCondition(cr, metav1.ConditionFalse, "RemoteApplyFailed",
            fmt.Sprintf("remote render could not be applied: %v", err))
        return r.finishNotReady(ctx, cr, hostStatuses, remoteStatuses, 0) // requeue w/ backoff via returned err
    }

    // shootPhase == "ready": apply the remote render, then gate host per applyOrder.
    if remoteFirst {
        remoteStatuses = r.applyAll(ctx, shootApplier, remoteManifests, ownedBy)
        // Under RemoteFirst the shoot is a workless cluster: every remote resource
        // (CRDs/RBAC/webhooks) is a structural dependency the host consumes. So ANY
        // remote failure — partial or complete — gates the host render this cycle;
        // starting host against a missing CRD/RBAC/webhook would crash-loop it.
        if anyFailed(remoteStatuses) {
            reason := "ResourcesDegraded" // partial: some applied, some failed
            msg := "some remote resources failed to apply; host render deferred until remote converges"
            if allFailed(remoteStatuses) {
                reason = "RemoteApplyFailed" // complete: 0 of N applied
                msg = "every resource in the remote render failed to apply"
            }
            r.setCondition(cr, metav1.ConditionFalse, reason, msg)
            return r.finishNotReady(ctx, cr, hostStatuses, remoteStatuses, 0)
        }
        applyHost() // full remote success → proceed to host
    } else {
        applyHost()
        remoteStatuses = r.applyAll(ctx, shootApplier, remoteManifests, ownedBy)
    }

    // 8. Prune orphans (only for renders that were actually applied this cycle).
    //    Identity key is version-independent (group/kind/namespace/name). Within a
    //    render, delete in reverse of the fixed intra-render kind priority; across
    //    renders, prune in the reverse of spec.applyOrder. CRDs skipped under
    //    retentionPolicy.crds: Retain.
    if remoteFirst { // reverse of RemoteFirst apply = prune host first, then remote
        r.prune(ctx, hostApplier, cr.Status.HostResources, hostManifests, cr)
        r.prune(ctx, shootApplier, cr.Status.RemoteResources, remoteManifests, cr)
    } else {
        r.prune(ctx, shootApplier, cr.Status.RemoteResources, remoteManifests, cr)
        r.prune(ctx, hostApplier, cr.Status.HostResources, hostManifests, cr)
    }

    // 9. Update status. Recorded applied-set = the render just applied.
    cr.Status.HostResources = hostStatuses
    cr.Status.RemoteResources = remoteStatuses
    cr.Status.Conditions = computeConditions(hostStatuses, remoteStatuses)
    cr.Status.LastReconcile = &metav1.Time{Time: time.Now()}
    if err := r.Status().Update(ctx, cr); err != nil {
        return ctrl.Result{}, err
    }

    // Requeue for periodic drift correction.
    return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

// finishNotReady writes status (host/remote statuses + the already-set condition)
// and requeues. A backoff of 0 means "return an error so controller-runtime
// requeues with exponential backoff" (used for RemoteApplyFailed); a non-zero
// backoff is a fixed RequeueAfter (used for WaitingForShootCredentials).
func (r *DualDeploymentOperatorReconciler) finishNotReady(ctx context.Context, cr *v1alpha1.DualDeploymentOperator,
    host, remote []v1alpha1.ResourceStatus, backoff time.Duration) (ctrl.Result, error) {
    cr.Status.HostResources = host
    cr.Status.RemoteResources = remote
    cr.Status.LastReconcile = &metav1.Time{Time: time.Now()}
    if e := r.Status().Update(ctx, cr); e != nil {
        return ctrl.Result{}, e
    }
    if backoff == 0 {
        return ctrl.Result{}, fmt.Errorf("remote render unavailable; requeuing")
    }
    return ctrl.Result{RequeueAfter: backoff}, nil
}
```

Helpers used above:
- `r.setCondition(cr, status, reason, message)` — sets the `Ready` condition via `meta.SetStatusCondition` (does not write; the caller's status update persists it).
- `anyFailed(statuses)` — reports whether **any** `ResourceStatus` in the slice has `Health=Degraded` — the partial-or-complete remote-failure test used under `RemoteFirst` to gate the host render (the workless shoot's render is entirely structural deps host consumes, so any failure gates host).
- `allFailed(statuses)` — reports whether **every** `ResourceStatus` in the slice has `Health=Degraded` (i.e. zero of N applied) — distinguishes the "complete remote failure" (`RemoteApplyFailed`) case from partial (`ResourcesDegraded`) when choosing the condition reason.
- `r.errStatus`, `r.applyAll`, `r.prune`, `computeConditions` — as defined elsewhere in this phase.

**Key differences from a single-render architecture**:
- Source is rendered twice with mode-specific parameters.
- No `split` step — each render's output is a coherent set for its target cluster.
- Transformations apply to each render independently. `filterKinds {kinds: [Service]}` runs on both, dropping Services from whichever render emits them.
- Only `origin` matters for transformation targeting; there is no `target` on manifests.
- Cross-render apply order follows `spec.applyOrder` (default `RemoteFirst`); deletion and prune run the reverse. Ordering *within* a render is the fixed built-in kind-priority, not consumer-configurable.
- Each reconcile prunes orphans (resources that left the render) via applied-set tracking against `status.*Resources`.

### Shoot client construction

```go
// errShootCredentialsNotReady signals that the shoot-access Secret exists but
// Gardener's token-requestor has not yet populated token/CA (absent or empty).
// This is a benign bootstrap state, NOT a fatal error: the caller maps it to a
// WaitingForShootCredentials condition, skips the remote render this cycle, and
// requeues. Distinct from a missing Secret (a misconfiguration → fatal).
var errShootCredentialsNotReady = errors.New("shoot credentials not yet populated")

func (r *DualDeploymentOperatorReconciler) buildShootApplier(ctx context.Context, cr *v1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
    ref := cr.Spec.RemoteAccess

    secret := &corev1.Secret{}
    if err := r.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: ref.SecretName}, secret); err != nil {
        // Missing Secret is a misconfiguration (wrong secretName / RBAC gap) → fatal.
        return nil, fmt.Errorf("failed to get shoot access secret: %w", err)
    }

    tokenKey, caKey := ref.TokenKey, ref.CAKey
    if tokenKey == "" {
        tokenKey = "token"
    }
    if caKey == "" {
        caKey = "bundle.crt"
    }

    // Readiness gate: token AND CA must be present AND non-empty. Gardener seeds
    // the Secret with token:"" / bundle.crt:"" and fills them asynchronously, so a
    // present-but-empty value means "not ready yet", never a usable credential
    // (an empty token would authenticate as nobody). Treat as not-ready, not fatal.
    token := secret.Data[tokenKey]
    caData := secret.Data[caKey]
    if len(token) == 0 || len(caData) == 0 {
        return nil, errShootCredentialsNotReady
    }

    // Build rest.Config directly from the Gardener token-requestor Secret —
    // token + CA bundle, no kubeconfig blob. spec.remoteAccess.server is required.
    config := &rest.Config{
        Host:        ref.Server,
        BearerToken: string(token),
        TLSClientConfig: rest.TLSClientConfig{
            CAData: caData,
        },
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
    // Teardown runs the REVERSE of spec.applyOrder. Under the default
    // RemoteFirst, deletion is host-first so the controller stops before its
    // CRDs are removed; under HostFirst, deletion is remote-first.
    shootApplier, err := r.buildShootApplier(ctx, cr)
    if err != nil {
        // Shoot unreachable: do NOT remove the finalizer and do NOT assume the
        // remote resources are gone. "Unreachable" is indistinguishable from
        // "transiently down" and must not be read as "deleted" — removing the
        // finalizer here would silently orphan live shoot resources. Surface it
        // and requeue; the CR stays in Terminating until the shoot returns (then
        // cleanup completes) or an operator manually removes the finalizer.
        r.Recorder.Event(cr, corev1.EventTypeWarning, "ShootUnreachable",
            "Shoot API server unreachable during deletion; retaining finalizer and retrying")
        meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
            Type: "Ready", Status: metav1.ConditionFalse, Reason: "ShootUnreachable",
            Message: fmt.Sprintf("shoot unreachable during deletion: %v", err),
        })
        _ = r.Status().Update(ctx, cr)
        return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
    }

    // Remote cleanup (reverse-delete order), respecting retentionPolicy for CRDs.
    orphans := deliver.SortStatusForDelete(cr.Status.RemoteResources)
    var errs []error
    for _, rs := range orphans {
        if rs.Kind == "CustomResourceDefinition" && cr.Spec.RetentionPolicy.CRDs == "Retain" {
            continue
        }
        if e := shootApplier.Delete(ctx, rs.AsManifest()); e != nil {
            errs = append(errs, e) // Delete ignores NotFound, so these are real failures
        }
    }

    // Host resources are removed via ownerReferences cascade (kubelet GC);
    // explicit host deletion is not strictly necessary.

    if err := kerrors.NewAggregate(errs); err != nil {
        // Reachable shoot but some deletes failed: keep the finalizer so orphans
        // are not leaked; requeue and retry.
        return ctrl.Result{}, err
    }

    controllerutil.RemoveFinalizer(cr, FinalizerName)
    return ctrl.Result{}, r.Update(ctx, cr)
}
```

**Finalizer safety**: the finalizer is removed **only** when remote cleanup is confirmed complete (every delete succeeded or returned NotFound). Two failure modes both keep the finalizer and requeue rather than leak:

- **Reachable shoot, some deletes failed** → keep finalizer, requeue, retry.
- **Unreachable shoot** → keep finalizer, set a `ShootUnreachable` condition + Event, requeue. The operator does **not** treat "unreachable" as "gone", because that guess — when the shoot is only transiently down — would silently orphan live resources with no finalizer left to clean them. The accepted cost is that a CR whose shoot is genuinely gone stays in `Terminating` until an operator manually removes the finalizer; this is surfaced (condition + Event), not auto-resolved.

---

## Phase 7: Production source loaders (~3-4 days)

Phase 2 built the Helm and kustomize renderers against the injected `ChartLoader` / `RootResolver` seams ([`internal/source/source.go`](../internal/source/source.go)) but shipped **only test fakes** (a `ChartLoader` that loads a chart from a local directory; a `RootResolver` that resolves to a local overlay directory). Phase 6 wired the reconciler with a **mocked source**. Neither pulls a real chart or fetches a real overlay, so `cmd/main.go` still carries a `TODO(production-loaders)` and live rendering fails until this phase lands.

This phase implements the two production fetchers so the operator renders real sources end-to-end. It is a prerequisite for Phase 8 (equivalence tests compare the operator's render of the **real** chart against today's chart output) and Phase 9 (a live in-cluster run).

> **Online testing is accepted from this phase on.** Phase 2's offline-only constraint (fakes, no network) applied to the renderer logic. The production loaders cannot be meaningfully verified without real I/O, so their tests **may** hit the network: pull real (small) charts and fetch real pinned git overlays. Gate anything slow or credential-dependent behind an env var (e.g. `DDO_ONLINE_TESTS=1`) or a build tag so `make test` on a laptop without registry/git credentials still passes, while CI runs the online tier.

### Production `ChartLoader` (Helm — OCI + HTTP)

```go
package source

// helmLoader is the production ChartLoader. It pulls from OCI (oci://) and
// classic HTTP Helm repositories and caches pulled charts by (repo, name, version).
// keppel.global.cloud.sap serves the operators' charts anonymously (verified), so
// the default path needs no credentials; the optional creds resolver exists only
// for a future private repo that requires auth.
type helmLoader struct {
    settings *cli.EnvSettings
    cache    *chartCache        // LRU keyed by repo+name+version
    creds    RegistryCredentials // OPTIONAL — nil/no-op for anonymous keppel; only for a future authed repo
}

func (l *helmLoader) Load(ctx context.Context, repo, name, version string) (*chart.Chart, error) {
    key := repo + "|" + name + "|" + version
    if c, ok := l.cache.get(key); ok {
        return c, nil
    }

    // OCI vs HTTP dispatch on the repo scheme.
    var chartPath string
    switch {
    case strings.HasPrefix(repo, "oci://"):
        // Anonymous by default; registry client only logs in if creds resolve a
        // non-empty auth for this host (future authed repo).
        regClient, err := l.newRegistryClient(ctx, repo)
        if err != nil { return nil, err }
        pull := action.NewPullWithOpts(action.WithConfig(l.actionConfig(regClient)))
        pull.Settings = l.settings
        pull.Version = version
        pull.DestDir = tmpDir
        // ref = oci://<repo>/<name> ; pull writes <name>-<version>.tgz to DestDir
        // ...
    default: // classic HTTP repo index
        pull := action.NewPullWithOpts(action.WithConfig(l.actionConfig(nil)))
        pull.RepoURL = repo
        pull.Username, pull.Password = l.creds.BasicAuth(repo) // empty for anonymous
        pull.Version = version
        pull.DestDir = tmpDir
        // ...
    }

    ch, err := loader.Load(chartPath)
    if err != nil { return nil, err }
    l.cache.put(key, ch)
    return ch, nil
}
```

**Authentication (keppel) — VERIFIED ANONYMOUS, no credentials needed for v1.** The operators' charts live in keppel at `oci://keppel.global.cloud.sap/ccloud-helm/<operator>-remote` (e.g. `ccloud-helm/metal-operator-remote`). Confirmed against the live registry from a laptop with **no auth configured**:

- keppel's token endpoint (`/keppel/v1/auth`) issues an **anonymous** bearer token for `repository:ccloud-helm/<repo>:pull` — the token payload carries `"kea":{"anon":true}`.
- With that token, `GET /v2/ccloud-helm/metal-operator-remote/tags/list` returns the real tag list and the manifest reports the Helm media types (`application/vnd.cncf.helm.config.v1+json` + `.../chart.content.v1.tar+gzip`).
- A plain `helm pull oci://keppel.global.cloud.sap/ccloud-helm/metal-operator-remote --version 0.6.2` with no login succeeds and writes `metal-operator-remote-0.6.2.tgz`.

So the production `ChartLoader` needs **no credential material** for the current five operators. The `RegistryCredentials` resolver is retained as an **optional, off-by-default** seam: it returns empty auth for keppel (anonymous), and only supplies real auth if a future source points at a private repo that challenges. Never assume auth is required; never log a token; never write auth into the cache key.

> **Correction to an earlier draft of this doc.** A previous version claimed the loader would "reuse the operator's in-cluster imagePullSecret" for `keppel.eu-de-1.cloud.sap`. That was wrong on three counts, verified against `rt-qa-de-1` (`shoot--cp--m-qa-de-1`): (1) no `imagePullSecrets` are set on the `-remote` operator Deployments or their ServiceAccounts, and no `dockerconfigjson`/`dockercfg` Secret exists in that namespace — image pulls authenticate at the **node/kubelet** level, which is not reachable in-process for a Helm chart pull anyway; (2) the registry host is `keppel.global.cloud.sap` (mirror/chart paths like `ccloud-ghcr-io-mirror/...` and `ccloud-helm/...`), not `keppel.eu-de-1.cloud.sap`; (3) chart pulls are anonymous, so no secret is needed at all. The imagePullSecret-reuse path is removed.

Use `helm.sh/helm/v3/pkg/registry` (`registry.NewClient`; call `registryClient.Login` **only** when creds resolve non-empty) for OCI, and `action.Pull`'s `Username`/`Password`/`CertFile` fields for HTTP.

**Caching.** LRU keyed by `repo|name|version`, shared across both renders of a reconcile (host + remote pull the same chart once) and across reconciles until evicted. Charts are immutable at a pinned version, so cache-by-version is safe; a version bump is a new key. Bound the cache (size + optional TTL) so long-lived operators don't grow unbounded.

### Production `RootResolver` (kustomize — git, pinned ref)

```go
package source

// gitResolver is the production RootResolver. It fetches a ?ref=-pinned remote
// (git) kustomize root into a temp dir and returns the path to the mode subpath.
type gitResolver struct {
    creds GitCredentials // resolves auth per git host (token / ssh / basic)
}

func (r *gitResolver) Resolve(ctx context.Context, url, subPath string) (string, func(), error) {
    // url is validated at admission to contain a pinned ?ref=<sha|tag> (KustomizeSource CEL rule).
    // Fetch the repo at that ref into a temp dir; return <tmp>/<subPath> + a cleanup func.
    dir, err := os.MkdirTemp("", "ddo-kustomize-")
    if err != nil { return "", nil, err }
    cleanup := func() { _ = os.RemoveAll(dir) }

    if err := r.fetch(ctx, url, dir); err != nil { // clone --depth 1, checkout ref
        cleanup()
        return "", nil, err
    }
    return filepath.Join(dir, subPath), cleanup, nil
}
```

**Authentication — VERIFIED ANONYMOUS, no credentials needed for v1.** The ipam-capi kustomize source and every remote base it pulls are **public**. Verified anonymously (no git auth configured):

- **Root source**: `github.com/sapcc/helm-charts//system/kustomize/ipam-capi-remote/...` — public (`git ls-remote` and the GitHub contents API both succeed unauthenticated).
- **Transitive remote bases** — this is the important part: the kustomization does **not** vendor upstream manifests, it references them by URL, so the resolver/krusty must fetch these too:
  - `github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster//config/{manager,webhook,crd,rbac}?ref=v1.1.0` — public (all HTTP 200 at the pinned tag).
  - `raw.githubusercontent.com/kubernetes-sigs/cluster-api/v1.13.4/config/crd/bases/*.yaml` — public (HTTP 200).

So the production `RootResolver` needs **no credential material** for the current five operators. Retain a `GitCredentials` resolver only as an **optional, off-by-default** seam for a hypothetical future source on a private host:

1. **Anonymous** — public repos (the current, verified path; no credentials).
2. **Token from a mounted Secret** (future) — a PAT / GitHub App token, injected into the clone URL or an `Authorization` header.
3. **SSH deploy key** (future) — for hosts that prefer SSH.

Do not assume auth is required; the resolver returns empty auth for public github. Never log a token.

> **The real open item for kustomize is egress, not auth.** Because the kustomization fetches transitive bases from `github.com` and `raw.githubusercontent.com` at build time, the operator Pod (running in the seed) must have **egress to github.com** — not just to `sapcc/helm-charts` but to the upstream `kubernetes-sigs` repos the source references. This is `design.md` §9 open question 1 (seed→kustomize-source egress), and it is a **network-reachability** prerequisite, distinct from credentials. Verify seed egress to github.com/raw.githubusercontent.com before the ipam-capi migration; if blocked, the resolver would need an in-landscape git mirror (which could reintroduce an auth question against that mirror).

**krusty fetches transitive bases itself — the resolver only supplies the ROOT.** The renderer already (Phase 2) calls `krusty.MakeKustomizer(...).Run(fSys, root)` with `LoadRestrictionsNone`. Critically, the ipam-capi kustomization references upstream manifests by **remote URL** (not vendored): its `manager/`, `webhook/`, `managedresources/` kustomizations pull `github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster//config/*?ref=v1.1.0` and `raw.githubusercontent.com/kubernetes-sigs/cluster-api/.../*.yaml`. **krusty resolves those remote bases during the build** — the `RootResolver` does NOT need to (and should not try to) pre-fetch them; it only needs to make the pinned **root** available as a local path. So the seam stays: resolver fetches the root, krusty (in the renderer) fetches the transitive remote bases. `LoadRestrictionsNone` is what permits krusty to follow those remote references. The interface seam from Phase 2 is unchanged.

**Library choice.** For the **root** fetch, prefer a pure-Go fetcher (`go-git` or `hashicorp/go-getter` with the git detector) over shelling to `git`, so the operator image needs no `git` binary and any future credentials never touch a subprocess argv. It MUST honor the pinned `?ref=` (checkout of the exact sha/tag) and MUST fail closed if the ref cannot be resolved (never silently build `HEAD`). Note that krusty's own remote-base fetching (for the transitive `kubernetes-sigs` URLs above) is internal to `krusty.Run` and uses its own getter — the operator does not control that transport, which is another reason **seed→github.com egress** (not auth) is the gating concern; see the egress note above.

### Wiring

Replace the `TODO(production-loaders)` in [`cmd/main.go`](../cmd/main.go): construct `helmLoader` and `gitResolver` and pass them via `source.Deps{ChartLoader: ..., RootResolver: ...}` to the reconciler. Both default to **anonymous** (no credential wiring needed for the current five operators); pass a non-nil `RegistryCredentials`/`GitCredentials` resolver only if/when a source targets a repo that challenges. The reconciler's `source.From(cr.Spec.Source, deps)` call (Phase 6) is unchanged.

### Tests (online tier)

- **Helm OCI pull, anonymous (primary)** — `Load` a real small chart from `oci://keppel.global.cloud.sap/ccloud-helm/<op>-remote` at a pinned version with **no credentials** and assert the returned `*chart.Chart` parses. This is the real path for all five operators. Runs under `DDO_ONLINE_TESTS=1` (hits keppel).
- **Helm OCI pull, authenticated (optional/future)** — push a tiny fixture chart to a throwaway OCI registry with a credential, then `Load` it with creds and assert. Exercises the creds seam even though no current operator needs it.
- **Auth failure** — a repo that challenges with wrong/absent credential returns a clear, non-leaking error (assert the error does NOT contain the token).
- **kustomize git resolve** — fetch a pinned-ref overlay from the real public source (`https://github.com/sapcc/helm-charts//system/kustomize/ipam-capi-remote/?ref=<tag>`) and assert the resolved path builds the expected overlay; assert a bad `?ref=` fails closed. A private-repo + token variant covers the creds seam.
- **Cache** — two `Load` calls for the same `repo|name|version` pull once (assert one network hit); a different version pulls again.
- **Offline still green** — `make test` without `DDO_ONLINE_TESTS` skips the network tests; the existing Phase 2 fake-based renderer tests continue to pass unchanged.

**Success criterion**: the operator pulls a real (small) chart **anonymously** from `oci://keppel.global.cloud.sap/ccloud-helm/...` and fetches a real pinned-ref kustomize overlay from the public `github.com/sapcc/helm-charts` source, rendering both to manifests; the optional credential seam is exercised by a self-hosted authed-registry test but is off by default; the cache pulls once per version; `make test` stays green offline while the online tier passes in CI.

### Follow-up: migrate `GetEventRecorderFor` → `GetEventRecorder`

While wiring the production loaders in [`cmd/main.go`](../cmd/main.go), also migrate the event recorder off the deprecated manager method. `mgr.GetEventRecorderFor(...)` is deprecated (staticcheck `SA1019`: old events API) and currently carries a `//nolint:staticcheck` in `cmd/main.go`. The non-deprecated `mgr.GetEventRecorder()` returns a differently-typed `events.EventRecorder` (method `Eventf`), not the `record.EventRecorder` (method `Event`) the reconciler field expects, so this is a field-type + call-site change, not a drop-in rename. Scope:

- Change the reconciler's `Recorder` field type (or introduce an adapter) to the events API recorder, updating every `Recorder.Event(...)` call site accordingly.
- Update the controller unit tests' fake recorder to the events-API equivalent.
- Remove the `//nolint:staticcheck` from the `recorder :=` line in `cmd/main.go` and confirm lint is clean without it.

**Success criterion**: `cmd/main.go` no longer calls `GetEventRecorderFor` and carries no `//nolint:staticcheck` for it; `golangci-lint run` is clean; events are still emitted on reconcile (verified by the controller suite).

---

## Phase 8: Equivalence tests (~1 week)

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

## Phase 9: Deployment charts (~2 days)

The operator ships as **two charts in two repos** (design.md §9.7), following the fleet's upstream-chart + wrapper-chart pattern (`ironcore-dev/metal-operator` publishes its own chart; `sapcc/helm-charts`'s `metal-operator-remote` wraps it):

**Chart 1 — `dual-deployment-operator` (the upstream controller chart, lives in THIS repo):**

Generated/maintained via the kubebuilder helm plugin, written to the repo-root `chart/` directory via the plugin's `--output-dir` flag (`kubebuilder edit --plugins=helm/v2-alpha --output-dir=.`), published as an OCI chart (e.g. `oci://keppel.global.cloud.sap/ccloud-helm/dual-deployment-operator`) versioned with the operator image.

```
chart/                                 # in THIS repo (dual-deployment-operator), repo root
├── Chart.yaml
├── values.yaml
├── crds/
│   └── dualdeploymentoperator.yaml   # CRD definition (installed once, not templated)
└── templates/
    ├── deployment.yaml            # --leader-elect=true
    ├── serviceaccount.yaml
    ├── clusterrole.yaml           # broad host applier grant + CR watch (see below)
    ├── clusterrolebinding.yaml
    ├── leader-election-role.yaml  # Lease in own namespace (leader election)
    └── networkpolicy.yaml
```

> **`chart/` is generated from `config/*`, not hand-written.** The kubebuilder kustomize scaffold under `config/` (CRD in `config/crd/`, RBAC in `config/rbac/`, manager in `config/manager/`, etc. — present since Phase 0, used by `make deploy`/`make run`/envtest) is the **source**; the helm plugin reads it and emits `chart/`. Do not author `chart/` by hand — run the plugin and re-run it (`--force`, same `--output-dir=.`) after changing markers/manifests, then commit the regenerated chart. `config/*` and `chart/` are complementary (dev/CI kustomize vs. published Helm chart), not competing.

**Chart 2 — `system/dual-deployment-operator-remote` (the wrapper + CR instances, lives in `sapcc/helm-charts`, replaces the per-operator `<operator>-remote` wrapper charts):**

```
system/dual-deployment-operator-remote/          # in sapcc/helm-charts
├── Chart.yaml                    # dependencies: [{name: dual-deployment-operator, repo: oci://…/ccloud-helm, version: <pinned>}]
├── values.yaml                   # defaults for source/transformations per managed operator
└── templates/
    ├── cr.yaml                   # one DualDeploymentOperator CR per managed operator, templated from .Values
    ├── remote-access.yaml        # Gardener token-requestor Secret (token + bundle.crt) — sapcc/Gardener glue
    └── shoot-rbac-bootstrap.yaml # minimal ManagedResource: shoot SA + apply-scoped ClusterRole/Binding — sapcc/Gardener glue
```

Chart 2 declares chart 1 as a Helm **`dependency`** (pinned by version, pulled from the OCI repo), so installing the wrapper brings the controller + CRD with it. `cr.yaml` templates each managed operator's CR (`ipam-capi-remote`, `metal-operator-remote`, …) with per-cluster values overridden from `cc/kube-secrets` (`values/helm/…/dual-deployment-operator-remote.yaml`) via the existing Concourse `helm-chart-pipeline` — the same GitOps delivery used today. Per-cluster config (e.g. ipam-capi's image tag + `kubernetesServiceHost`, design.md §9.4) enters the CR as `spec.source.helm.values` (Helm sources) or `patch` transforms (kustomize sources) templated from chart values. The shoot kubeconfig stays out of the CR (it is the `remote-access` token-requestor Secret).

The sapcc/Gardener-specific resources (`remote-access` Secret, `shoot-rbac-bootstrap` ManagedResource) live in chart 2, not chart 1 — chart 1 stays a clean, reusable upstream artifact (controller + RBAC + CRD only), exactly like `metal-operator`'s own chart carries no `-remote` glue.

Because chart 2 depends on chart 1, the controller + CRD (via chart 1's `crds/`) install before chart 2's `templates/` render the CR instances — a CR is never applied before its CRD/controller exist.

**Seed-side (host) RBAC scope** (the operator's own ServiceAccount on the seed, provisioned by **chart 1**) — a **broad applier grant provisioned by the controller chart**, not a namespace-only Role. The host render is **not** namespace-local: the candidate wrapper charts emit host-side `ClusterRole`/`ClusterRoleBinding` (metal-operator, ipam-capi both do), and the operator's own `patch`/rename transforms can produce `ClusterRole`s. So the host applier must be able to create cluster-scoped host-render kinds too, and — where the host render creates RBAC — must itself hold those powers (privilege-escalation prevention). This aligns to gardener-resource-manager (broad `cluster-admin`-equivalent target ClusterRole) and Flux (appliers bound to `cluster-admin`, tenants scoped via per-object SA impersonation, never by narrowing the applier). The grant covers:
- Watch `DualDeploymentOperator` CRs (cluster-wide read of the CR type)
- Get/List Secrets (for the shoot token-requestor Secret referenced by `spec.remoteAccess`)
- Create/update/delete/get/list/watch the host-render kinds — **namespaced and cluster-scoped** (Deployment, Service, Ingress, NetworkPolicy, ConfigMap, ServiceAccount, Role/RoleBinding, ClusterRole/ClusterRoleBinding, …); set ownerReferences to the CR so host resources GC on CR deletion. Where the host render creates RBAC, this grant holds the powers it confers.
- Create/get/update Leases in own namespace (leader election)

Scope the grant no broader than the host render needs, but do not force it namespace-only — the candidate charts prove cluster-scoped host resources exist.

**RBAC differs by *provisioning*, not breadth — both appliers are broad:**
- **Host (seed):** broad applier grant (above), provisioned by **this deployment chart**.
- **Remote (shoot):** a broad cluster-scoped `ClusterRole` (CRDs, ClusterRoles/Bindings, Roles/Bindings, ServiceAccounts, Validating/Mutating WebhookConfigurations, + additions) carrying the privilege-escalation set, seeded by the `shoot-rbac-bootstrap.yaml` ManagedResource (GRM-applied) — resolving the SA-can't-grant-itself-RBAC chicken-and-egg. See the shoot RBAC bootstrap section below and design.md §3.6.7.

**Single-install-per-seed constraint (host cluster-scoped names are seed-global).** The host render can contain cluster-scoped objects (`ClusterRole`/`ClusterRoleBinding`) whose names are **seed-global** — the operator applies them under their rendered upstream names and does **not** per-namespace-qualify them (upstream `roleRef`/subject references assume the fixed names). So at most **one** operator-managed install of a given operator may run per seed: two CRs on one seed emitting the same-named cluster-scoped object would fight over SSA field ownership and have ambiguous prune/GC (a cluster-scoped object cannot be owner-referenced by a namespaced CR). This matches current production — on `rt-qa-de-1`/`rt-eu-de-1` the `-remote` operators run in only the `m-<region>` workload shoot-cp namespace, and the host-side ClusterRole/Binding carry static seed-global names with a single `{{ .Release.Namespace }}` subject. It is an install-time contract (documented, not runtime-validated in v1); a per-name uniquifier or admission guard is a possible future enhancement. See design.md §3.6.7.

**Leader election (required).** Run with `--leader-elect=true` so at most one instance is active cluster-wide — required even at `replicas: 1` because a rolling update transiently runs two pods. Set `LeaderElectionReleaseOnCancel: true` so the outgoing leader releases the lease on graceful shutdown (near-instant failover instead of a ~15 s `LeaseDuration` wait). `replicas: 1` is the recommended default for this low-load per-shoot operator (matches cert-manager's default; `replicas: 2` is an optional HA upgrade — active-passive failover, not horizontal scale). See design.md §3.6.6.

**`cmd/main.go` production readiness (verify, don't build).** The kubebuilder scaffold already wires the manager's metrics server (`Metrics.BindAddress`, `--metrics-bind-address`, default `:8443` bound behind auth), the health-probe server (`HealthProbeBindAddress`, `--health-probe-bind-address` `:8081`), the `healthz`/`readyz` checks (`mgr.AddHealthzCheck`/`AddReadyzCheck`, `healthz.Ping`), and leader election. This phase's job is to **confirm they stay wired and are surfaced in chart 1**, not to add them:
- Uncomment `LeaderElectionReleaseOnCancel: true` in `cmd/main.go` (design.md §3.6.6).
- Ensure chart 1's `deployment.yaml` sets the container's `livenessProbe` (`GET /healthz` on the probe port) and `readinessProbe` (`GET /readyz`), passes `--leader-elect=true` and `--health-probe-bind-address`/`--metrics-bind-address`, and exposes the metrics port (the kubebuilder helm plugin scaffolds these from `config/`; verify they survived and point at the right ports).
- Chart 1 already includes the metrics `ServiceMonitor`/`metrics-reader` scaffolding via `config/prometheus/` + `config/rbac/`; keep or drop per whether the seed scrapes it, but do not silently lose the probes. A Deployment without probes is the gap this note closes.

**Shoot RBAC bootstrap (install-time prerequisite — the operator cannot self-bootstrap).** The operator applies CRDs/RBAC/ServiceAccounts/WebhookConfigurations to the shoot **as the ServiceAccount its `spec.remoteAccess` token-requestor Secret is minted for**. That SA cannot create those resources unless it already holds the rights, and Kubernetes privilege-escalation prevention forbids an applier from creating a ClusterRole granting powers it does not already hold. Therefore a **minimal, static** Gardener `ManagedResource` — applied by the privileged gardener-resource-manager (GRM, effectively cluster-admin on the shoot) — must seed, before the operator runs:
- the shoot ServiceAccount the operator authenticates as, and
- a ClusterRole + ClusterRoleBinding granting that SA create/update/delete on the kinds the operator delivers (`customresourcedefinitions`, `clusterroles`, `clusterrolebindings`, `roles`, `rolebindings`, `serviceaccounts`, `validatingwebhookconfigurations`, `mutatingwebhookconfigurations`, plus the operator's additions).

This does **not** reintroduce GRM into the runtime delivery path (the operator still delivers all content directly via SSA — no ManagedResource for content); the bootstrap MR is one-time install plumbing, the same category as the token-requestor Secret. Without it, the operator's remote applies fail with forbidden/privilege-escalation errors, surfaced per-resource as `Degraded` (continue-on-error). See design.md §3.6.7.

Example bootstrap ManagedResource (per operator, ~30 lines, static):

```yaml
# Applied by GRM (privileged). Seeds ONLY the SA + apply-scoped RBAC on the shoot.
apiVersion: resources.gardener.cloud/v1alpha1
kind: ManagedResource
metadata:
  name: <operator>-shoot-rbac-bootstrap
spec:
  secretRefs:
    - name: <operator>-shoot-rbac-bootstrap
---
# ...Secret with objects.yaml containing: ServiceAccount <operator>-controller-manager,
# ClusterRole (create/update/delete on CRDs/RBAC/SAs/webhookconfigs), ClusterRoleBinding.
```

> **This bootstrap MR is a per-operator Phase 9 deliverable, not just documentation.** `chart/shoot-rbac-bootstrap.yaml` (chart 2) must be authored **once per managed operator** — the SA name and the exact apply-scoped kinds differ per operator (e.g. ipam-capi needs conversion-webhook CRD verbs; boot/argora/khalkeon need no webhook verbs). Templated from chart-2 values so `cc/kube-secrets` can vary the SA name/namespace per cluster. Without the correct per-operator MR, that operator's first remote reconcile fails `forbidden`/privilege-escalation on every RBAC/CRD apply (surfaced per-resource as `Degraded`), so the operator is non-functional for that operator until it exists. Treat "author + verify the bootstrap MR" as a required step when onboarding each operator, alongside its CR template.

---

## Phase 9.5: Operator image build + publish (~1 day)

Chart 1 deploys a container image (`manager.image.repository` + `tag`), but nothing in the plan builds or publishes it — the same runtime-prerequisite gap as the production loaders. This phase closes it. The pieces are mostly scaffolded; the work is adding the publish workflow.

**Publish to GHCR; keppel mirrors it — do NOT push to keppel directly.** Verified against the sibling repo [`SAP-cloud-infrastructure/webhook-injector`](https://github.com/SAP-cloud-infrastructure/webhook-injector): its CI publishes the image to **`ghcr.io/<org>/<repo>`** via a GitHub Actions workflow (`.github/workflows/container-registry-ghcr.yaml`, auto-generated by [`sapcc/go-makefile-maker`](https://github.com/sapcc/go-makefile-maker)), authenticating with the built-in `GITHUB_TOKEN` (`packages: write`). Keppel then **mirrors** ghcr.io — confirmed on the live cluster, where the running images resolve to `keppel.global.cloud.sap/ccloud-ghcr-io-mirror/<org>/<repo>:...` (the `ccloud-ghcr-io-mirror` account is a keppel replica of ghcr.io). So the operator publishes to ghcr like every other SAP-cloud-infrastructure operator; workloads pull the keppel-mirrored path. No direct keppel push credential is needed.

**Already present:**
- `Dockerfile` (multi-stage: build the manager binary, ship a distroless runtime image) — Phase 0 layout.
- `make docker-build docker-push IMG=<ref>` and `make build-installer IMG=<ref>` targets in the `Makefile` (default `IMG ?= controller:latest`).

**Steps:**
- Add the GHCR publish workflow the same way the fleet does — via `sapcc/go-makefile-maker` (add a `githubWorkflow.pushContainerToGhcr` stanza to `Makefile.maker.yaml` and regenerate), producing `.github/workflows/container-registry-ghcr.yaml`. It builds and pushes `ghcr.io/${{ github.repository }}` on push to the default branch (and on tags), tagged `type=sha,format=long` + semver + `latest`, authenticating with `GITHUB_TOKEN` (`packages: write`) — no external registry secret.
- Image ref for deployment is the **keppel-mirrored** path: `keppel.global.cloud.sap/ccloud-ghcr-io-mirror/<org>/dual-deployment-operator:<tag>` (verify the exact mirror account with the team). Chart 1's `values.yaml` (`manager.image.repository`/`tag`) references that mirrored path so workloads pull via keppel; the operator never pushes to keppel.
- The published image tag (git sha / semver) is what chart 1's `values.yaml` pins, and what chart 2's dependency version tracks — so an operator release = new image tag + chart 1 version bump.
- No laptop builds — the drift lesson from today's `make build-` targets. CI (GitHub Actions) owns the publish, exactly as webhook-injector does.

**Ordering:** this must land before Phase 4/5 (any live deploy) — chart 1 has no image to run until it does. It has no dependency on Phases 7–9, so it can be done any time after Phase 0; listed here next to the chart work because they ship together (image + chart 1 + chart 2 are one release unit).

**Success criterion**: the GHCR workflow publishes a pullable image on merge (visible at `ghcr.io/<org>/dual-deployment-operator` and, once mirrored, at the keppel `ccloud-ghcr-io-mirror` path); chart 1's `values.yaml` references the keppel-mirrored repo; a `helm install` of chart 1 with the published tag starts a running manager Pod (probes green).

---

## Phase 10 (v1 scaffolding only): Validating admission webhook

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
| 7 | Operator pulls a real (small) chart **anonymously** from `oci://keppel.global.cloud.sap/ccloud-helm/...` and fetches a real pinned-ref kustomize overlay from the public `github.com/sapcc/helm-charts` source, rendering both to manifests; the optional credential seam is exercised by a self-hosted authed test but off by default; cache pulls once per version; `make test` stays green offline while the online tier passes in CI |
| 8 | Equivalence test passes for at least metal-operator vs. today's chart output (host render matches host-side output; remote render matches remote-side output) |
| 9 | Chart 1 (`dual-deployment-operator`, this repo, via kubebuilder helm plugin) builds/publishes with CRD in `crds/`; chart 2 (`dual-deployment-operator-remote`, `sapcc/helm-charts`) depends on it and installs in a QA shoot-cp namespace — controller + CRD come up first (dependency), then the CR instances (templated from `cc/kube-secrets` values); the operator reconciles the applied CRs. `cmd/main.go` probes/metrics/leader-election confirmed wired and reflected in chart 1's `deployment.yaml`; the per-operator `shoot-rbac-bootstrap` MR is authored in chart 2 |
| 9.5 | GHCR publish workflow (via `sapcc/go-makefile-maker`) publishes the operator image to `ghcr.io/<org>/dual-deployment-operator` on merge/tag with `GITHUB_TOKEN` (no keppel push secret); keppel mirrors it to `ccloud-ghcr-io-mirror`; chart 1's `values.yaml` references the keppel-mirrored path; `helm install` of chart 1 with the published tag starts a running manager Pod with green probes |
| 10 | Webhook scaffold builds; no-op ValidateCreate/ValidateUpdate methods pass tests; ValidatingWebhookConfiguration NOT deployed in v1 (deferred to v2) |

Each phase should merge to `main` with tests before moving to the next.

---

## Notes on chart-side changes (out of this repo's scope)

These changes happen in `sapcc/helm-charts`, not in this operator repo, but they gate the operator's usefulness:

- Create `system/dual-deployment-operator-remote/` (chart 2, §9.7 / Phase 9): declares chart 1 (`dual-deployment-operator`, published from this repo) as a Helm `dependency`, templates one `DualDeploymentOperator` CR per managed operator, and carries the `remote-access` Secret + `shoot-rbac-bootstrap` ManagedResource. Replaces the per-operator `<operator>-remote` wrapper charts. Per-cluster values overridden from `cc/kube-secrets`.
- Restructure `system/metal-operator-remote/` (and other wrapper charts) per `design.md` §4.1: split templates into `templates/host/` and `templates/remote/`, invert upstream enable values, add annotation helpers, delete pre-rendered files
- Restructure `system/kustomize/ipam-capi-remote/` per §4.2: top-level kustomization, additions/ subdir, pin refs
- Verify webhook-injector uses distinct SSA field manager (small injector code change if not)

Do the operator work first (Phases 0-8, plus chart 1 in this repo at Phase 9); chart 2 and the source restructures in `sapcc/helm-charts` follow once the operator is validated against today's chart output in equivalence tests.
