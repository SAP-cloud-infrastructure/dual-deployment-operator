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
│   │   ├── seed.go                             # in-cluster (seed) client factory
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
    ShootAccess      ShootAccessRef           `json:"shootAccess"`
    // ShootNamespace is the target namespace for the shoot render and
    // delivery. Namespaced resources in the shoot render that omit an explicit
    // metadata.namespace are placed here; cluster-scoped resources are unaffected.
    // The seed render/delivery uses the CR's own metadata.namespace.
    ShootNamespace   string                    `json:"shootNamespace"`
    Transformations   []Transformation          `json:"transformations,omitempty"`
    RetentionPolicy   RetentionPolicy           `json:"retentionPolicy,omitempty"`
    // ApplyOrder controls which cluster's render is applied first each reconcile.
    // Deletion and prune run the reverse order. Default: ShootFirst.
    // +kubebuilder:validation:Enum=SeedFirst;ShootFirst
    // +kubebuilder:default=ShootFirst
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
    // Values applied to BOTH seed and shoot renders (common per-cluster stuff).
    Values       *apiextensionsv1.JSON `json:"values,omitempty"`
    // Values applied ONLY to the seed render (typically enables the seed-side
    // parts of the upstream subchart).
    SeedValues   *apiextensionsv1.JSON `json:"seedValues,omitempty"`
    // Values applied ONLY to the shoot render (typically enables the shoot-side
    // parts of the upstream subchart).
    ShootValues *apiextensionsv1.JSON `json:"shootValues,omitempty"`
}

// KustomizeSource references a kustomize root plus its two overlay subpaths.
// +kubebuilder:validation:XValidation:rule="self.url.matches('.*[?&]ref=.+')",message="kustomize url must include a pinned ref= query parameter"
type KustomizeSource struct {
    // +kubebuilder:validation:MinLength=1
    URL        string `json:"url"`
    // +kubebuilder:validation:MinLength=1
    SeedPath   string `json:"seedPath"`
    // +kubebuilder:validation:MinLength=1
    ShootPath string `json:"shootPath"`
}

type ShootAccessRef struct {
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
    SeedResources   []ResourceStatus   `json:"seedResources,omitempty"`
    ShootResources []ResourceStatus   `json:"shootResources,omitempty"`
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
    ModeSeed   Mode = "seed"
    ModeShoot Mode = "shoot"
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
    //      < spec.SeedValues or spec.ShootValues (mode-specific)
    //      < {mode: "seed"|"shoot"}         (operator-injected)
    values := mergeValues(
        chart.Values,          // chart defaults (already loaded)
        parseValues(h.spec.Values),
        parseValues(h.spec.modeValues(mode)),  // returns SeedValues or ShootValues
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
    case ModeSeed:   return spec.SeedValues
    case ModeShoot: return spec.ShootValues
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
    case ModeSeed:
        subPath = k.spec.SeedPath
    case ModeShoot:
        subPath = k.spec.ShootPath
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
- No value projection. `spec.source.kustomize` has no `values` field. Per-CR parameterization for kustomize is via mode selection (`seedPath`/`shootPath`) or, if needed, future kustomize-native fields (`images`, `patches` — see design.md §9.4). Not a Helm values map.
- Ref pinning — reject URLs without `?ref=<sha|tag>` at admission time
- Overlay layout: kustomize source must have two subdirectories (`seed/` and `shoot/`), each with its own `kustomization.yaml` selecting the appropriate resources for that mode. See design.md §4.2 for the ipam-capi layout.
- Origin tagging: chart authors add `commonAnnotations: {dual-deployment-operator.cc.sap/origin: additions}` on the `additions/seed/` and `additions/shoot/` kustomization files. Other resources (upstream) default to `origin: upstream`.

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

There is **no** `packageWebhookConfigsForInjector` transformation in r7. The operator applies WebhookConfigurations (and conversion-webhook CRDs) directly to the shoot in the shoot render; the only requirement is that those objects carry the webhook-injector's `--target-label` so its target patch mode adopts them and keeps `.caBundle` current (design.md §3.4.4, §3.8).

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

- `Render(ctx, ModeSeed)` → all seed resources
- `Render(ctx, ModeShoot)` → all shoot resources

Transformations run on each render independently. Each transformation naturally affects only what's present in that render (e.g., a sidecar-injecting `patch` matches a Deployment only in the seed render; `rewriteWebhookURL` matches Validating/Mutating WebhookConfigurations and conversion-webhook CRDs only in the shoot render).

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
    Cluster      string  // "seed" or "shoot" for logging
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

> **`SSAApplier` stays stateless** — it holds only `Client`, `FieldManager`, and `Cluster`; the per-CR ownership value is passed as the `ownedBy` argument to `Apply`, not stored on the struct, so one seed/shoot applier instance is safely reused across concurrent reconciles of different CRs. The reconciler derives `ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)` once per reconcile and threads it through every `Apply` call. `manifest.IsClusterScoped(kind)` already exists (used by `ApplyNamespace`); reuse it here. The guard costs one extra GET **only** for cluster-scoped kinds.

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

    // 2. Render TWICE — one per mode. Seed uses the CR's own namespace;
    //    shoot uses spec.shootNamespace.
    seedManifests, err := src.Render(ctx, source.ModeSeed, cr.Namespace)
    if err != nil { return r.errStatus(ctx, cr, "SeedRenderFailed", err) }

    shootManifests, err := src.Render(ctx, source.ModeShoot, cr.Spec.ShootNamespace)
    if err != nil { return r.errStatus(ctx, cr, "ShootRenderFailed", err) }

    // 3. Build the ordered transformation list (single per-render scope, r7).
    transforms, err := transform.Build(cr.Spec.Transformations)
    if err != nil { return r.errStatus(ctx, cr, "InvalidTransformation", err) }

    // 4. Apply each transformation to both renders independently, in
    //    declaration order. Each transformation naturally affects only
    //    resources present in the render it runs on. (No cross-stream phase.)
    for _, t := range transforms {
        seedManifests, err = t.Apply(seedManifests)
        if err != nil { return r.errStatus(ctx, cr, "SeedTransformFailed", err) }
        shootManifests, err = t.Apply(shootManifests)
        if err != nil { return r.errStatus(ctx, cr, "ShootTransformFailed", err) }
    }

    // 5. Get clients. shootPhase captures the shoot availability outcome:
    //    ready | credsNotReady | clientFailed. Only `ready` means the shoot
    //    render can be attempted.
    seedApplier := r.SeedApplier   // preconstructed at startup
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
    seedManifests = deliver.SortForApply(seedManifests)
    shootManifests = deliver.SortForApply(shootManifests)

    // 7. Apply per spec.applyOrder, respecting shoot-failure severity.
    //    ShootFirst gates the seed render on shoot availability (seed depends on
    //    the shoot coming up first); SeedFirst never gates seed on the shoot.
    //    Three shoot outcomes:
    //      - complete failure (client unbuildable, or 0 of N shoot applied)
    //          ShootFirst  -> STOP before seed; Ready=False ShootApplyFailed
    //          SeedFirst    -> apply seed first; flag ShootApplyFailed (non-blocking)
    //      - credentials not ready (benign bootstrap wait)
    //          ShootFirst  -> DEFER seed too; Ready=False WaitingForShootCredentials
    //          SeedFirst    -> apply seed first; flag WaitingForShootCredentials
    //      - ready -> apply shoot, then gate seed per applyOrder. Under ShootFirst
    //          ANY shoot failure (partial ResourcesDegraded or complete
    //          ShootApplyFailed) stops before seed — the workless shoot's render is
    //          all structural deps seed consumes. Under SeedFirst seed applies first.
    //    WebhookConfigs/conversion CRDs are applied with caBundle stripped.
    shootFirst := cr.Spec.ApplyOrder != "SeedFirst"
    var seedStatuses, shootStatuses []v1alpha1.ResourceStatus
    shootStatuses = cr.Status.ShootResources // preserve prior shoot status by default

    // Derive the ownership value once; thread it through every apply (the appliers
    // are stateless/shared, so ownedBy is an argument, never a struct field).
    ownedBy := manifest.OwnedByValue(cr.Namespace, cr.Name)

    applySeed := func() { seedStatuses = r.applyAll(ctx, seedApplier, seedManifests, ownedBy) }

    switch shootPhase {
    case "credsNotReady":
        if !shootFirst {
            applySeed() // SeedFirst: seed does not wait on shoot
        }
        // ShootFirst: defer seed until credentials populate.
        r.setCondition(cr, metav1.ConditionFalse, "WaitingForShootCredentials",
            "shoot token/CA not yet populated by Gardener; shoot render deferred")
        return r.finishNotReady(ctx, cr, seedStatuses, shootStatuses, 30*time.Second)

    case "clientFailed":
        if !shootFirst {
            applySeed() // SeedFirst: seed proceeds; shoot failure only flagged
        }
        // ShootFirst: stop before seed — seed must not start without the shoot.
        r.setCondition(cr, metav1.ConditionFalse, "ShootApplyFailed",
            fmt.Sprintf("shoot render could not be applied: %v", err))
        return r.finishNotReady(ctx, cr, seedStatuses, shootStatuses, 0) // requeue w/ backoff via returned err
    }

    // shootPhase == "ready": apply the shoot render, then gate seed per applyOrder.
    if shootFirst {
        shootStatuses = r.applyAll(ctx, shootApplier, shootManifests, ownedBy)
        // Under ShootFirst the shoot is a workless cluster: every shoot resource
        // (CRDs/RBAC/webhooks) is a structural dependency the seed consumes. So ANY
        // shoot failure — partial or complete — gates the seed render this cycle;
        // starting seed against a missing CRD/RBAC/webhook would crash-loop it.
        if anyFailed(shootStatuses) {
            reason := "ResourcesDegraded" // partial: some applied, some failed
            msg := "some shoot resources failed to apply; seed render deferred until shoot converges"
            if allFailed(shootStatuses) {
                reason = "ShootApplyFailed" // complete: 0 of N applied
                msg = "every resource in the shoot render failed to apply"
            }
            r.setCondition(cr, metav1.ConditionFalse, reason, msg)
            return r.finishNotReady(ctx, cr, seedStatuses, shootStatuses, 0)
        }
        applySeed() // full shoot success → proceed to seed
    } else {
        applySeed()
        shootStatuses = r.applyAll(ctx, shootApplier, shootManifests, ownedBy)
    }

    // 8. Prune orphans (only for renders that were actually applied this cycle).
    //    Identity key is version-independent (group/kind/namespace/name). Within a
    //    render, delete in reverse of the fixed intra-render kind priority; across
    //    renders, prune in the reverse of spec.applyOrder. CRDs skipped under
    //    retentionPolicy.crds: Retain.
    if shootFirst { // reverse of ShootFirst apply = prune seed first, then shoot
        r.prune(ctx, seedApplier, cr.Status.SeedResources, seedManifests, cr)
        r.prune(ctx, shootApplier, cr.Status.ShootResources, shootManifests, cr)
    } else {
        r.prune(ctx, shootApplier, cr.Status.ShootResources, shootManifests, cr)
        r.prune(ctx, seedApplier, cr.Status.SeedResources, seedManifests, cr)
    }

    // 9. Update status. Recorded applied-set = the render just applied.
    cr.Status.SeedResources = seedStatuses
    cr.Status.ShootResources = shootStatuses
    cr.Status.Conditions = computeConditions(seedStatuses, shootStatuses)
    cr.Status.LastReconcile = &metav1.Time{Time: time.Now()}
    if err := r.Status().Update(ctx, cr); err != nil {
        return ctrl.Result{}, err
    }

    // Requeue for periodic drift correction.
    return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

// finishNotReady writes status (seed/shoot statuses + the already-set condition)
// and requeues. A backoff of 0 means "return an error so controller-runtime
// requeues with exponential backoff" (used for ShootApplyFailed); a non-zero
// backoff is a fixed RequeueAfter (used for WaitingForShootCredentials).
func (r *DualDeploymentOperatorReconciler) finishNotReady(ctx context.Context, cr *v1alpha1.DualDeploymentOperator,
    seed, shoot []v1alpha1.ResourceStatus, backoff time.Duration) (ctrl.Result, error) {
    cr.Status.SeedResources = seed
    cr.Status.ShootResources = shoot
    cr.Status.LastReconcile = &metav1.Time{Time: time.Now()}
    if e := r.Status().Update(ctx, cr); e != nil {
        return ctrl.Result{}, e
    }
    if backoff == 0 {
        return ctrl.Result{}, fmt.Errorf("shoot render unavailable; requeuing")
    }
    return ctrl.Result{RequeueAfter: backoff}, nil
}
```

Helpers used above:
- `r.setCondition(cr, status, reason, message)` — sets the `Ready` condition via `meta.SetStatusCondition` (does not write; the caller's status update persists it).
- `anyFailed(statuses)` — reports whether **any** `ResourceStatus` in the slice has `Health=Degraded` — the partial-or-complete shoot-failure test used under `ShootFirst` to gate the seed render (the workless shoot's render is entirely structural deps seed consumes, so any failure gates seed).
- `allFailed(statuses)` — reports whether **every** `ResourceStatus` in the slice has `Health=Degraded` (i.e. zero of N applied) — distinguishes the "complete shoot failure" (`ShootApplyFailed`) case from partial (`ResourcesDegraded`) when choosing the condition reason.
- `r.errStatus`, `r.applyAll`, `r.prune`, `computeConditions` — as defined elsewhere in this phase.

**Key differences from a single-render architecture**:
- Source is rendered twice with mode-specific parameters.
- No `split` step — each render's output is a coherent set for its target cluster.
- Transformations apply to each render independently. `filterKinds {kinds: [Service]}` runs on both, dropping Services from whichever render emits them.
- Only `origin` matters for transformation targeting; there is no `target` on manifests.
- Cross-render apply order follows `spec.applyOrder` (default `ShootFirst`); deletion and prune run the reverse. Ordering *within* a render is the fixed built-in kind-priority, not consumer-configurable.
- Each reconcile prunes orphans (resources that left the render) via applied-set tracking against `status.*Resources`.

### Shoot client construction

```go
// errShootCredentialsNotReady signals that the shoot-access Secret exists but
// Gardener's token-requestor has not yet populated token/CA (absent or empty).
// This is a benign bootstrap state, NOT a fatal error: the caller maps it to a
// WaitingForShootCredentials condition, skips the shoot render this cycle, and
// requeues. Distinct from a missing Secret (a misconfiguration → fatal).
var errShootCredentialsNotReady = errors.New("shoot credentials not yet populated")

func (r *DualDeploymentOperatorReconciler) buildShootApplier(ctx context.Context, cr *v1alpha1.DualDeploymentOperator) (deliver.Applier, error) {
    ref := cr.Spec.ShootAccess

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
    // token + CA bundle, no kubeconfig blob. spec.shootAccess.server is required.
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
        Cluster:      "shoot",
    }, nil
}
```

### Deletion

```go
func (r *DualDeploymentOperatorReconciler) reconcileDelete(ctx context.Context, cr *v1alpha1.DualDeploymentOperator) (ctrl.Result, error) {
    // Teardown runs the REVERSE of spec.applyOrder. Under the default
    // ShootFirst, deletion is seed-first so the controller stops before its
    // CRDs are removed; under SeedFirst, deletion is shoot-first.
    shootApplier, err := r.buildShootApplier(ctx, cr)
    if err != nil {
        // Shoot unreachable: do NOT remove the finalizer and do NOT assume the
        // shoot resources are gone. "Unreachable" is indistinguishable from
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

    // Shoot cleanup (reverse-delete order), respecting retentionPolicy for CRDs.
    orphans := deliver.SortStatusForDelete(cr.Status.ShootResources)
    var errs []error
    for _, rs := range orphans {
        if rs.Kind == "CustomResourceDefinition" && cr.Spec.RetentionPolicy.CRDs == "Retain" {
            continue
        }
        if e := shootApplier.Delete(ctx, rs.AsManifest()); e != nil {
            errs = append(errs, e) // Delete ignores NotFound, so these are real failures
        }
    }

    // Seed resources are removed via ownerReferences cascade (kubelet GC);
    // explicit seed deletion is not strictly necessary.

    if err := kerrors.NewAggregate(errs); err != nil {
        // Reachable shoot but some deletes failed: keep the finalizer so orphans
        // are not leaked; requeue and retry.
        return ctrl.Result{}, err
    }

    controllerutil.RemoveFinalizer(cr, FinalizerName)
    return ctrl.Result{}, r.Update(ctx, cr)
}
```

**Finalizer safety**: the finalizer is removed **only** when shoot cleanup is confirmed complete (every delete succeeded or returned NotFound). Two failure modes both keep the finalizer and requeue rather than leak:

- **Reachable shoot, some deletes failed** → keep finalizer, requeue, retry.
- **Unreachable shoot** → keep finalizer, set a `ShootUnreachable` condition + Event, requeue. The operator does **not** treat "unreachable" as "gone", because that guess — when the shoot is only transiently down — would silently orphan live resources with no finalizer left to clean them. The accepted cost is that a CR whose shoot is genuinely gone stays in `Terminating` until an operator manually removes the finalizer; this is surfaced (condition + Event), not auto-resolved.

---

## Phase 7: Production source loaders (~3-4 days)

Phase 2 built the Helm and kustomize renderers against the injected `ChartLoader` / `RootResolver` seams ([`internal/source/source.go`](../internal/source/source.go)) but shipped **only test fakes** (a `ChartLoader` that loads a chart from a local directory; a `RootResolver` that resolves to a local overlay directory). Phase 6 wired the reconciler with a **mocked source**. Neither pulls a real chart or fetches a real overlay, so `cmd/main.go` still carries a `TODO(production-loaders)` and live rendering fails until this phase lands.

This phase implements the two production fetchers so the operator renders real sources end-to-end. It is a prerequisite for Phase 8 (equivalence tests compare the operator's render of the **real** chart against today's chart output) and Phase 9 (a live in-cluster run).

> **Online testing is accepted from this phase on.** Phase 2's offline-only constraint (fakes, no network) applied to the renderer logic. The production loaders cannot be meaningfully verified without real I/O, so their tests **may** hit the network: pull real (small) charts and fetch real pinned git overlays. These run as ordinary tests in the default `Test` job (and any local `go test ./...`), so CI must have egress to the public registries/repos they target; there is no build tag or env gate. Credential-dependent paths are proven **hermetically** in-process instead (see below), so no real credentials are ever needed.

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

**Caching.** LRU keyed by `repo|name|version`, shared across both renders of a reconcile (seed + shoot pull the same chart once) and across reconciles until evicted. Charts are immutable at a pinned version, so cache-by-version is safe; a version bump is a new key. Bound the cache (size + optional TTL) so long-lived operators don't grow unbounded.

> **SCOPE UPDATE (2026-07): caching is DEFERRED to Phase 7.5.** The `production-source-loaders` OpenSpec change (which realizes this Phase 7) deliberately ships **no cache** — both loaders fetch fresh each reconcile (Helm: pull tgz to a temp dir, load, clean up; kustomize: re-fetch root each call). The code sketch above with `cache *chartCache` / `l.cache.get`/`put` and this "Caching" paragraph describe the **Phase 7.5** target, not Phase 7. See the new "Phase 7.5: Source caching" below for the actual (resolve-then-key, `emptyDir`-backed, symmetric) design. Treat the loader code above as illustrative of the eventual shape, not the Phase 7 deliverable.

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

> **The real open item for kustomize is egress, not auth — VERIFIED AVAILABLE on qa (conditional on Gardener labels).** Because the kustomization fetches transitive bases from `github.com` and `raw.githubusercontent.com` at build time, the operator Pod (running in the seed) must have **egress to github.com** — not just to `sapcc/helm-charts` but to the upstream `kubernetes-sigs` repos the source references. This is `design.md` §9 open question 1 (seed→kustomize-source egress), a **network-reachability** prerequisite distinct from credentials. **Smoke-tested 2026-07 on `a-qa-de-200` / `shoot--cp--m-qa-de-200`** (the seed namespace where the `-remote` operators actually run): from a Pod carrying the Gardener networking labels, `git-upload-pack` against `github.com/sapcc/helm-charts` and `github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster` both returned HTTP 200 with refs advertised, `raw.githubusercontent.com/.../cluster-api/v1.13.4/...` returned HTTP 200, and keppel served an anonymous token + real chart tags. **The gating factor is the Gardener `deny-all` NetworkPolicy, not the WAN path**: an *unlabeled* Pod could not even resolve DNS. So egress works iff the operator Pod carries `networking.gardener.cloud/to-dns`, `to-public-networks`, and `to-private-networks` — a **Phase 9 chart requirement** (see Phase 9 "Gardener egress labels on the operator Pod"), not operator code. No in-landscape git mirror is needed for qa-de-200; re-verify per landscape before each operator's migration.

**krusty fetches transitive bases itself — the resolver only supplies the ROOT.** The renderer already (Phase 2) calls `krusty.MakeKustomizer(...).Run(fSys, root)` with `LoadRestrictionsNone`. Critically, the ipam-capi kustomization references upstream manifests by **remote URL** (not vendored): its `manager/`, `webhook/`, `managedresources/` kustomizations pull `github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster//config/*?ref=v1.1.0` and `raw.githubusercontent.com/kubernetes-sigs/cluster-api/.../*.yaml`. **krusty resolves those remote bases during the build** — the `RootResolver` does NOT need to (and should not try to) pre-fetch them; it only needs to make the pinned **root** available as a local path. So the seam stays: resolver fetches the root, krusty (in the renderer) fetches the transitive remote bases. `LoadRestrictionsNone` is what permits krusty to follow those remote references. The interface seam from Phase 2 is unchanged.

**Library choice.** For the **root** fetch, prefer a pure-Go fetcher (`go-git` or `hashicorp/go-getter` with the git detector) over shelling to `git`, so the operator image needs no `git` binary and any future credentials never touch a subprocess argv. It MUST honor the pinned `?ref=` (checkout of the exact sha/tag) and MUST fail closed if the ref cannot be resolved (never silently build `HEAD`). Note that krusty's own remote-base fetching (for the transitive `kubernetes-sigs` URLs above) is internal to `krusty.Run` and uses its own getter — the operator does not control that transport, which is another reason **seed→github.com egress** (not auth) is the gating concern; see the egress note above.

### Wiring

Replace the `TODO(production-loaders)` in [`cmd/main.go`](../cmd/main.go): construct `helmLoader` and `gitResolver` and pass them via `source.Deps{ChartLoader: ..., RootResolver: ...}` to the reconciler. Both default to **anonymous** (no credential wiring needed for the current five operators); pass a non-nil `RegistryCredentials`/`GitCredentials` resolver only if/when a source targets a repo that challenges. The reconciler's `source.From(cr.Spec.Source, deps)` call (Phase 6) is unchanged.

### Tests (default job — real pulls + hermetic auth)

All of these run in the default `Test` job (and local `go test ./...`); none are build-tagged or env-gated. The real-pull tests need network egress to the listed public sources; the authenticated paths are proven hermetically in-process and need no credentials or external services.

- **Helm OCI pull, anonymous (primary)** — `Load` a real small chart from `oci://ghcr.io/stefanprodan/charts` at a pinned version with **no credentials** and assert the returned `*chart.Chart` parses. Real network pull.
- **Helm OCI pull, authenticated (hermetic)** — `TestHelmLoaderOCIAuthed` stands up a hand-rolled in-process OCI-distribution registry (a content-addressable byte store gated by HTTP Basic auth) behind a `httptest` **TLS** server, pushes a fixture chart via helm's own registry client, then pulls it back through the production `helmLoader`. Proves correct creds succeed, wrong creds fail without leaking, and anonymous is rejected. TLS is required because helm/ORAS refuse to forward Basic credentials over plain HTTP (ORAS GHSA-vh4v-2xq2-g5cg); the test injects a cert-trusting client through the loader's unexported `httpClient` seam (production leaves it nil). No `distribution/v3` dependency, no `registry:2`, no external service.
- **Auth failure** — wrong credentials return a clear, non-leaking error; asserted by `TestGitResolverHTTPSBasicAuth` (hermetic), `TestHelmLoaderOCIAuthed` (hermetic, wrong-password variant), and `TestHelmLoaderAuthedHTTPRepo` (hermetic, classic HTTP repo).
- **Authenticated classic HTTP(S) Helm repo (hermetic)** — `TestHelmLoaderAuthedHTTPRepo` runs a Basic-auth `httptest` Helm repo (plain HTTP — the classic-repo path has no credential-forwarding restriction) and proves `pullHTTP` sends credentials: correct creds succeed, wrong creds fail without leaking the password, anonymous gets 401.
- **Authenticated git clone (hermetic)** — `TestGitResolverAuthedCloneSucceeds` (`internal/source/gitresolver_authed_test.go`). Stands up an in-process `git-http-backend` CGI handler (via `net/http/cgi`) guarded by Basic-auth inside an `httptest.Server`. Proves correct creds → resolve succeeds + `seed/kustomization.yaml` exists; wrong creds → error (401). Skips gracefully when `git-http-backend` is absent.
- **kustomize git resolve, anonymous** — `TestGitResolverOnlineKustomizeRender` fetches a pinned-ref overlay from `github.com/kubernetes-sigs/kustomize` (public) and asserts the overlay renders. Real network fetch.

**CI shape**: `ci.yaml` / `checks.yaml` / `codeql.yaml` are autogenerated by go-makefile-maker (do NOT hand-edit). The real-pull tests run in the autogenerated `Test` job as ordinary tests — there is no separate online workflow. (An earlier iteration used a hand-authored `.github/workflows/online-tests.yaml` with a `registry:2` service for authed OCI; both are removed now that authed OCI is hermetic.)

**Success criterion**: the operator pulls a real (small) chart **anonymously** from a public OCI registry and fetches a real pinned-ref kustomize overlay from the public `github.com/kubernetes-sigs/kustomize` source; all three authenticated credential seams (OCI, classic HTTP repo, git clone) are proven by hermetic in-process servers with no external services or credentials; the whole suite runs in the default `Test` job.

### Follow-up: migrate `GetEventRecorderFor` → `GetEventRecorder`

While wiring the production loaders in [`cmd/main.go`](../cmd/main.go), also migrate the event recorder off the deprecated manager method. `mgr.GetEventRecorderFor(...)` is deprecated (staticcheck `SA1019`: old events API) and currently carries a `//nolint:staticcheck` in `cmd/main.go`. The non-deprecated `mgr.GetEventRecorder()` returns a differently-typed `events.EventRecorder` (method `Eventf`), not the `record.EventRecorder` (method `Event`) the reconciler field expects, so this is a field-type + call-site change, not a drop-in rename. Scope:

- Change the reconciler's `Recorder` field type (or introduce an adapter) to the events API recorder, updating every `Recorder.Event(...)` call site accordingly.
- Update the controller unit tests' fake recorder to the events-API equivalent.
- Remove the `//nolint:staticcheck` from the `recorder :=` line in `cmd/main.go` and confirm lint is clean without it.

**Success criterion**: `cmd/main.go` no longer calls `GetEventRecorderFor` and carries no `//nolint:staticcheck` for it; `golangci-lint run` is clean; events are still emitted on reconcile (verified by the controller suite).

---

## Phase 7.5: Source caching (resolve-then-key, emptyDir) (~3-4 days) — COMPLETE

Phase 7 ships both production loaders **without caching** — every reconcile pulls the Helm chart fresh (temp dir → load → cleanup) and re-fetches the kustomize root (krusty then re-fetches transitive bases). Reconcile is 10-minute-scale and the fleet is small, so this is correct and acceptable; caching was deliberately split out because a *sound* cache has its own design (mutable refs, transitive-tag immutability, Helm/kustomize symmetry) that is bigger than "make the loaders render". This phase adds that cache.

### Core idea: resolve-then-key

A cache is only sound when its key maps to immutable content. A CR's pinned `?ref=` (git) or `:version` (OCI tag) is **not** guaranteed immutable — `?ref=main` moves, a tag can be force-pushed. But git/OCI both let us **resolve a ref to its concrete content id cheaply, at fetch time**:

- **git**: `ls-remote` (the `info/refs` advertisement — a small round trip, no clone) resolves `main` / `v1.1.0` / a short SHA → the exact **commit SHA** it points at right now.
- **OCI**: a `HEAD`/manifest request resolves `repo:0.6.2` → the immutable **manifest digest** (`sha256:…`).

Key the cache on the **resolved id**, not the ref string. A push to `main` or a moved tag → new resolved id → cache miss → re-fetch. **Auto-invalidates, no TTL.** This is sound for any ref kind, unlike a `repo|name|version`-string key.

### Two cache layers (symmetric across both loaders)

1. **Helm chart-artifact cache (disk `.tgz`), keyed by resolved OCI digest.** On `Load`: resolve `repo:version` → digest; if `<digest>.tgz` is cached, load it; else pull, store under the digest. Disk-backed LRU, evict-before-write (on-disk usage ≤ cap by construction), **no TTL**, cache-write failure (`ENOSPC`) is **non-fatal** (log, return the pulled chart, re-pull next reconcile — the cache is an optimization, never the source of truth). For classic HTTP(S) repos with no digest, fall back to no-cache or a `repo|name|resolved-version` key (design decision for this phase).
2. **kustomize render cache, keyed by resolved root SHA.** On `Resolve`+render: `ls-remote` → root SHA; if the rendered `[]manifest` for `base|SHA|subPath` is cached, return it (skips clone **and** krusty **and** transitive fetches — the expensive part); else fetch@SHA, run krusty, cache the result. **Soundness caveat to design around:** the root SHA covers the transitive refs only insofar as they are pinned in the committed kustomization files (they are, for ipam-capi: `?ref=v1.1.0`, `v1.13.4`) — but those transitive refs are **tags**, so a force-moved upstream tag under the *same* root SHA would serve a stale render. Decide in this phase: (a) trust upstream release tags not to move (document it), or (b) resolve transitive refs to SHAs too (fully sound, more work). Start with (a) + a documented caveat; escalate to (b) only if needed.

### Chart-1 deployment cross-ref (the emptyDir moved here from Phase 9)

The disk caches need a volume. Chart 1's `deployment.yaml` mounts an `emptyDir` at the cache dir:
```yaml
# pod spec
volumes:
  - name: source-cache
    emptyDir:
      sizeLimit: 640Mi   # >= the loader's LRU cap (default 512Mi) + headroom
# manager container
volumeMounts:
  - name: source-cache
    mountPath: /cache/source   # match the loader's configured cache dir
```
Set `emptyDir.sizeLimit` ≥ the LRU cap so the cache is isolated from other ephemeral-storage consumers and cannot contribute to node disk pressure; the loader's evict-before-write LRU keeps on-disk usage under the cap by construction, so the two bounds agree. `emptyDir` (not a PVC) is correct — the cache is disposable; a Pod restart just re-populates. The loader falls back to an `os.TempDir()` subdir when no volume is mounted, so it runs correctly even before this chart change lands. (This is the block previously drafted under Phase 9; it belongs with the caching implementation, not the deployment-chart phase.)

### Optional (valuable regardless of caching): record the resolved id in status

Even without a cache, recording the **resolved commit SHA / OCI digest** in `status` (or an Event/log) per reconcile makes every render **auditable and reproducible** — "this reconcile rendered ipam-capi at `sapcc/helm-charts@a1b2c3` / metal-operator-remote@`sha256:…`" — even when the CR pins `?ref=main`. Consider landing this small piece first; it is independent of the cache and useful on its own.

### Cache flags (deferred from Phase 7)

The `--chart-cache-dir` / `--chart-cache-cap-mb` (or unified `--source-cache-*`) manager flags and their `cmd/main.go` wiring belong to this phase, not Phase 7.

**Success criterion**: repeated reconciles of an unchanged source pull/clone/render once (subsequent reconciles are cache hits keyed by resolved digest/SHA); a source change (new push to a tracked branch, tag move, or version bump) is picked up on the next reconcile via a changed resolved id (no TTL, no stale render); on-disk cache stays ≤ the configured cap; a cache-write failure never fails a render; `make test` stays green offline while an online tier proves resolve-then-key hit/miss behavior.

---

## Phase 7.6: Helm subchart dependency resolution (~1 day)

**Blocks production usage of subchart-wrapping charts.** Phase 7's Helm loader ([`internal/source/helmloader.go`](../internal/source/helmloader.go)) pulls the chart `.tgz` and calls `loader.Load()` directly — it does **not** run Helm dependency resolution. A chart that declares `dependencies:` in `Chart.yaml` under-renders **silently** unless its subcharts are vendored under `charts/` in the pulled archive. The operator-native `metal-operator-remote-v2` wrapper ([`sapcc/helm-charts` `system/metal-operator-remote-v2`](https://github.com/sapcc/helm-charts/tree/master/system/metal-operator-remote-v2)) wraps the upstream `metal-operator` chart as a subchart dependency; it works today only because it **vendors** that subchart. This phase adds dependency resolution so wrapper charts can declare a plain `dependencies:` entry instead. Full rationale: [`subchart-dependency-resolution.md`](subchart-dependency-resolution.md) + `context.md` Revision 8.

> **This is the community-standard approach.** Argo CD resolves chart dependencies at sync (reconcile) time via the Helm SDK exactly this way; Flux pre-resolves in a separate `source-controller` artifact step this operator has no analogue for. Reconcile-time `downloader.Manager.Build()` is the aligned choice; the Phase 7.5 render cache (keyed on the resolved OCI digest) makes it run once per chart version.

### Change: dependency-build step in `Load()`

`downloader.Manager` operates on an **unpacked chart directory**, not the in-memory `*chart.Chart`, so the pulled `.tgz` must be expanded first. Insert before `loader.Load()`:

```go
// after chartPath is the pulled .tgz (from pullOCI / pullHTTP):
unpackedDir := filepath.Join(tmp, "unpacked")
if err := os.MkdirAll(unpackedDir, 0o755); err != nil {
    return nil, err
}
f, err := os.Open(chartPath)
if err != nil {
    return nil, err
}
defer func() { _ = f.Close() }()
if err := chartutil.Expand(unpackedDir, f); err != nil {
    return nil, fmt.Errorf("source: expand chart: %w", err)
}
chartDir := filepath.Join(unpackedDir, name)

rc, err := registry.NewClient(opts...) // reuse the pullOCI option set (cache, optional basic-auth, test httpClient)
if err != nil {
    return nil, err
}
m := &downloader.Manager{
    Out:              io.Discard,
    ChartPath:        chartDir,
    Getters:          getter.All(l.settings),
    RepositoryConfig: l.settings.RepositoryConfig,
    RepositoryCache:  l.settings.RepositoryCache,
    RegistryClient:   rc,
}
if err := m.Build(); err != nil {
    return nil, fmt.Errorf("source: build dependencies: %w", err)
}
return loader.Load(chartDir)
```

Estimated size: **~35–40 lines**. `l.settings`, `getter.All`, and the OCI registry-client option set already exist in the Phase 7 pull path and are reused/refactored. `Build()` on a chart with no `dependencies:` is a cheap no-op, so it runs **unconditionally** — no `Chart.yaml`-presence guard.

### `Build()`-only, `Chart.lock` required (the determinism trap)

Verified against Helm v3.21.3 `pkg/downloader/manager.go`: `Manager.Build()` rebuilds `charts/` from a committed `Chart.lock` (exact pinned versions, no semver re-negotiation) — but if the lock is **absent**, `Build()` silently falls back to `Update()`, which re-negotiates semver ranges against the live repo index and can pull a newer subchart mid-reconcile. **Call `Build()` only, never `Update()`**; a missing/out-of-sync `Chart.lock` must surface as a fail-closed render error on CR status, not a silent re-resolve. Chart authors using dependencies **MUST commit `Chart.lock`** (accepted practice, like `package-lock.json` / `Cargo.lock`).

### Auth contract (Helm single-registry limitation)

Helm's registry client supports **one credential set per OCI hostname** and has **no per-dependency credential** ([helm/helm#11286](https://github.com/helm/helm/issues/11286)). The CR carries one `authSecretRef` for the parent pull. Contract for chart authors: subcharts must reside on the **same** (or a public) registry as the parent — reusing that `authSecretRef` — or stay vendored under `charts/` if they need distinct private credentials. Do not attempt per-dependency auth wiring; it does not exist in Helm.

### Kustomize side — no change needed (verified)

The analogous gap does **not** exist on the kustomize path. [`internal/source/kustomize.go`](../internal/source/kustomize.go) runs `krusty.Run` with `LoadRestrictionsNone`, which **fetches remote bases itself during the build**, and the git [`RootResolver`](../internal/source/gitresolver.go) is fail-closed on an unresolvable `?ref=`. Helm's `loader.Load()` is a pure unpack (no dependency fetch), which is why only the Helm loader needs this step. The one kustomize nuance — transitive-tag mutability — is already documented as a Phase 7.5 cache-soundness caveat, not a silent-under-render defect.

### Tests (default job — real pull + hermetic)

- **Vendored subchart still renders (regression guard)** — a chart with a pre-vendored `charts/<dep>.tgz` renders the subchart's objects; proves `Build()` does not break the existing vendored path.
- **Declared-but-unvendored dependency now resolves** — a fixture parent chart declaring a `dependencies:` entry (with a committed `Chart.lock`) whose subchart is served by an in-process registry/repo resolves and renders the subchart's objects through the production `helmLoader`. Reuse the hermetic OCI/HTTP server pattern from Phase 7's `TestHelmLoaderOCIAuthed` / `TestHelmLoaderAuthedHTTPRepo`.
- **No-dependency chart is a no-op** — a chart with no `dependencies:` loads unchanged (Build() no-op path).
- **Missing/out-of-sync `Chart.lock` fails closed** — a chart declaring `dependencies:` without a committed lock returns a clear render error (the design's `Build()`-only contract), not a silent under-render.

**Success criterion**: a chart declaring an unvendored `dependencies:` entry (with committed `Chart.lock`) renders its subchart's objects through the operator; a pre-vendored chart still renders unchanged; a no-dependency chart is unaffected; a missing/out-of-sync lock fails closed with a surfaced error; `make test` stays green offline while the hermetic subchart-resolution test proves the resolve path; once shipped, `metal-operator-remote-v2` can drop subchart vendoring in favor of `dependencies:` + `Chart.lock`.

---

## Phase 7.7: Remove classic HTTP(S) Helm-repo parent-chart support (OCI-only) (~1 day)

**Motivation.** Phase 7 shipped a classic HTTP(S) Helm-repo parent-chart path (`pullHTTP`, plus the `http(s)://` dispatch arms in `Load`/`ResolveID`/`repoScope` and `resolveHTTPID`) alongside the OCI path. Phase 7.6 then restricted **subchart dependency** resolution to OCI (HTTP(S)-repo subchart deps are rejected fail-closed, because `downloader.Manager.Build()` needs HTTP repos pre-registered/index-cached, which the operator does not do at reconcile time). This makes the HTTP(S) **parent** path a near-dead end: a chart pulled from an HTTP(S) repo that itself declares subchart dependencies will, in practice, declare those subcharts on the **same HTTP(S) repo** — which Phase 7.6 now rejects. So an HTTP(S) parent chart works only in the narrow case of "no dependencies, or all-vendored deps." The whole fleet publishes to the keppel **OCI** registry (verified in Phase 7: `oci://keppel.global.cloud.sap/ccloud-helm/...`, anonymous), so the HTTP(S) parent path carries maintenance + test surface for a case no operator uses. This phase removes it, making the Helm loader **OCI-only end to end**.

> **Scope stance / community-practice note.** OCI is the current, sanctioned Helm distribution transport (classic `index.yaml` HTTP repos are legacy); an operator that renders a fixed, internally-published fleet has no reason to keep the HTTP(S) transport. This is a deliberate narrowing, not a capability regression the fleet relies on. Confirm no in-flight consumer sets `spec.source.helm.repo` to an `http(s)://` value before removing (all current CRs use `oci://`).

### Removal scope (`internal/source/helmloader.go`)

- Delete `pullHTTP` and `resolveHTTPID`.
- In `Load`, `ResolveID`, and `repoScope`: drop the `http://` / `https://` dispatch arms; the `default` arm becomes "only `oci://` is supported" (the existing unsupported-scheme error already covers this — reword it to name OCI explicitly).
- Remove now-unused HTTP-only helpers/imports (e.g. `repo.IndexFile` handling, `net/http`/`io` usages that only served `resolveHTTPID`) — let the compiler + `make run-golangci-lint` drive the cleanup.
- Keep `hostOf`/`rejectURLCredentials` (still used by the OCI path).

### CRD / admission (validate, do not silently narrow)

- `spec.source.helm.repo` MUST now be an `oci://` URL. Add/extend the CEL validation (or the loader's `rejectURLCredentials`-adjacent guard) so a non-`oci://` Helm repo is rejected at admission with a clear message, rather than failing opaquely at reconcile. Check `api/v1alpha1/*_types.go` for an existing `repo` pattern/CEL rule and tighten it; run `make manifests generate`.

### Tests to remove / adjust

- Remove `TestHelmLoaderOnlineHTTPRepo` (online HTTP parent pull) and `TestHelmLoaderAuthedHTTPRepo` (hermetic authed HTTP repo) and any `resolveHTTPID`/`repoScope`-HTTP unit tests (`TestHelmLoader_ResolveID_HTTPIndexDigest`, `_HTTPVersionFallback`, `_HTTPUnkeyable`, `_repoScope_HTTPPathDistinguishes`, `_resolveHTTPID_*`, `TestHelmLoader_ResolveID_RejectsURLCredentials_HTTP`).
- Add a unit test asserting a `http(s)://` Helm `repo` is rejected (at admission via CEL and/or at `Load`/`ResolveID` with a clear error).
- The OCI online + hermetic authed tests stay; the kustomize git path is untouched (HTTP(S) there is a different transport — kustomize remote bases via krusty — and is NOT in scope here).

### Docs to update

- `helm-chart-loader` spec: the "Production Helm ChartLoader with scheme dispatch" requirement currently lists both `oci://` and `http(s)://`; narrow it to OCI-only and drop the "classic HTTP(S) repo reference is pulled via RepoURL" scenario (this is a MODIFIED + REMOVED delta in a NEW OpenSpec change for this phase).
- README + AGENTS: change "Production Helm ChartLoader (OCI + HTTP(S) repos)" → "OCI-only".
- design.md §3.3: drop the HTTP(S) parent-repo mention from the Helm source discriminator.
- context.md: add a short revision recording the OCI-only narrowing and its rationale (HTTP parent + Phase 7.6 OCI-only subchart rule = dead end).

**Success criterion**: the Helm loader accepts only `oci://` Helm sources; a `http(s)://` Helm `repo` is rejected with a clear error (admission + loader); `pullHTTP`/`resolveHTTPID` and their tests are gone; `go build ./...`, `make run-golangci-lint`, and the offline `internal/source` suite are green; `make manifests generate` produces no unexpected diff beyond the tightened CRD rule; kustomize path unchanged.

> **Do this as its own OpenSpec change** (schema `sdd-plus-superpowers`), not folded into the Phase 7.6 change (already archived) or PR #16. It touches the CRD validation surface, so it is a slightly larger blast radius than 7.6.

---

## Phase 8: Equivalence tests (~1 week)

> **Status (2026-07):** SHIPPED for the four Helm-sourced operators — `metal-operator`,
> `boot-operator`, `argora-operator`, `khalkeon` — via scoped equivalence (Decision B:
> compare the delivered kinds — CRDs/RBAC/WebhookConfigs — with a per-fixture
> known-divergence list, since the operator renders the upstream chart while the golden
> side renders the disabling `-remote` wrapper). Golden side renders the wrapper from
> `sapcc/helm-charts` git at a pinned SHA (`git clone` + `helm dependency build` +
> `helm template`); operator side drives `source.From → Render → transform.Build → Apply`.
> Harness + fixtures in `internal/equivalence/` and `testdata/fixtures/<op>/`. The
> per-operator subtests are **opt-in behind `RUN_EQUIVALENCE=1`** — the golden render's
> `helm dependency build` pulls subcharts from the internal-only keppel OCI registry, which
> public CI runners cannot reach, so they skip on public CI and run wherever that registry
> is reachable (local dev, internal runners). The comparator's normalization/allowlist unit
> tests always run offline.
>
> **`ipam-capi` equivalence is DEFERRED to a standalone future follow-up (not tied to any
> phase).** It is not sequenced within the phase plan because closing it depends on a
> `KustomizeSource` capability that is itself a separate CRD + source change; schedule it
> independently once that extension exists. Two ipam-capi-specific blockers surfaced (the
> first fixed here, the second deferred):
> 1. *Fixed in this change:* the production git `RootResolver` (`fetchSHA`) used a
>    `Depth: 1` shallow fetch and could not check out an arbitrary historical commit
>    SHA — see the `kustomize-root-resolver` spec delta + `TestGitResolverResolvesHistoricalSHA`.
> 2. *Deferred:* ipam-capi's kustomize overlays reference patch files by
>    **repo-root-relative** paths (`patches[].path: kustomize/ipam-capi-remote/manager/…`),
>    so today's `make build-` runs kustomize from the repo root. The operator's
>    `KustomizeSource` (`url` + `seedPath`/`shootPath`) resolves the build dir to the
>    overlay **subdir**, so those relative patch paths double and the seed/`manager`
>    overlay fails to build (`no such file`). Closing ipam-capi equivalence requires
>    extending `KustomizeSource` so the clone root and the kustomize-build dir can differ
>    (build from the repo root, target the overlay) — a CRD + source change (design §9.4),
>    hence its own follow-up rather than part of the equivalence-tests change. The
>    `managedresources`/shoot overlay is self-contained and would render; only the
>    seed overlay is blocked.

> **Subchart-wrapping equivalence fixture — FOLLOW-UP (Phase 7.6 regression guard).** Once
> Phase 7.6 (Helm subchart dependency resolution) shipped, add a fifth equivalence fixture
> for a chart that declares an **OCI subchart dependency** — the natural consumer is
> [`sapcc/helm-charts` `system/metal-operator-remote-v2`](https://github.com/sapcc/helm-charts/tree/master/system/metal-operator-remote-v2),
> which wraps upstream `metal-operator` as a subchart. This is a strong end-to-end
> regression guard: the golden side already runs `helm dependency build` (real Helm subchart
> resolution), and the operator side now runs `downloader.Manager.Build()` inside
> `helmLoader.Load` — the fixture proves the two produce equivalent rendered output for a
> subchart-wrapping chart (before Phase 7.6 the operator side under-rendered, so this is the
> exact bug the fixture catches). Not tied to a phase; runs behind `RUN_EQUIVALENCE=1` like
> the other four (both sides fetch the subchart from the internal keppel OCI registry).
> **Prerequisites to build it** (same shape as the existing fixtures): a pinned
> `sapcc/helm-charts` commit SHA where `metal-operator-remote-v2` exists as an OCI
> subchart-wrapping chart, its per-shoot overlay values, and the fixture `cr.yaml` under
> `testdata/fixtures/metal-operator-remote-v2/`. Independent of PR #16 (the Phase 7.6 loader
> change), which is covered by the hermetic `TestHelmLoaderResolvesUnvendoredDependency` and
> `TestResolveDepsPlan`; this fixture adds the render-equivalence dimension on top.

Fixtures directory:

```
testdata/fixtures/
├── metal-operator/
│   ├── cr.yaml                                # CR fixture
│   ├── shoot-values.yaml                      # values for target shoot
│   ├── today-chart-render.yaml                # captured helm template of today's chart
│   └── expected-operator-output/
│       ├── seed/
│       │   ├── deployment.yaml
│       │   ├── service.yaml
│       │   └── ...
│       └── shoot/
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

            requireEquivalent(t, operatorOutput.Seed, todayOutput.Seed,
                ignore("helm.sh/chart"),           // allowed to differ
                ignoreOrdering(),
                ignoreWhitespace())
            requireEquivalent(t, operatorOutput.Shoot, todayOutput.Shoot,
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
    ├── deployment.yaml            # --leader-elect=true; Gardener egress labels (see below); chart-cache emptyDir added in Phase 7.5
    ├── serviceaccount.yaml
    ├── clusterrole.yaml           # broad seed applier grant + CR watch (see below)
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

**Seed-side RBAC scope** (the operator's own ServiceAccount on the seed, provisioned by **chart 1**) — a **broad applier grant provisioned by the controller chart**, not a namespace-only Role. The seed render is **not** namespace-local: the candidate wrapper charts emit seed-side `ClusterRole`/`ClusterRoleBinding` (metal-operator, ipam-capi both do), and the operator's own `patch`/rename transforms can produce `ClusterRole`s. So the seed applier must be able to create cluster-scoped seed-render kinds too, and — where the seed render creates RBAC — must itself hold those powers (privilege-escalation prevention). This aligns to gardener-resource-manager (broad `cluster-admin`-equivalent target ClusterRole) and Flux (appliers bound to `cluster-admin`, tenants scoped via per-object SA impersonation, never by narrowing the applier). The grant covers:
- Watch `DualDeploymentOperator` CRs (cluster-wide read of the CR type)
- Get/List Secrets (for the shoot token-requestor Secret referenced by `spec.shootAccess`)
- Create/update/delete/get/list/watch the seed-render kinds — **namespaced and cluster-scoped** (Deployment, Service, Ingress, NetworkPolicy, ConfigMap, ServiceAccount, Role/RoleBinding, ClusterRole/ClusterRoleBinding, …); set ownerReferences to the CR so seed resources GC on CR deletion. Where the seed render creates RBAC, this grant holds the powers it confers.
- Create/get/update Leases in own namespace (leader election)

Scope the grant no broader than the seed render needs, but do not force it namespace-only — the candidate charts prove cluster-scoped seed resources exist.

**RBAC differs by *provisioning*, not breadth — both appliers are broad:**
- **Seed:** broad applier grant (above), provisioned by **this deployment chart**.
- **Remote (shoot):** a broad cluster-scoped `ClusterRole` (CRDs, ClusterRoles/Bindings, Roles/Bindings, ServiceAccounts, Validating/Mutating WebhookConfigurations, + additions) carrying the privilege-escalation set, seeded by the `shoot-rbac-bootstrap.yaml` ManagedResource (GRM-applied) — resolving the SA-can't-grant-itself-RBAC chicken-and-egg. See the shoot RBAC bootstrap section below and design.md §3.6.7.

**Single-install-per-seed constraint (seed cluster-scoped names are seed-global).** The seed render can contain cluster-scoped objects (`ClusterRole`/`ClusterRoleBinding`) whose names are **seed-global** — the operator applies them under their rendered upstream names and does **not** per-namespace-qualify them (upstream `roleRef`/subject references assume the fixed names). So at most **one** operator-managed install of a given operator may run per seed: two CRs on one seed emitting the same-named cluster-scoped object would fight over SSA field ownership and have ambiguous prune/GC (a cluster-scoped object cannot be owner-referenced by a namespaced CR). This matches current production — on `rt-qa-de-1`/`rt-eu-de-1` the `-remote` operators run in only the `m-<region>` workload shoot-cp namespace, and the seed-side ClusterRole/Binding carry static seed-global names with a single `{{ .Release.Namespace }}` subject. It is an install-time contract (documented, not runtime-validated in v1); a per-name uniquifier or admission guard is a possible future enhancement. See design.md §3.6.7.

**Leader election (required).** Run with `--leader-elect=true` so at most one instance is active cluster-wide — required even at `replicas: 1` because a rolling update transiently runs two pods. Set `LeaderElectionReleaseOnCancel: true` so the outgoing leader releases the lease on graceful shutdown (near-instant failover instead of a ~15 s `LeaseDuration` wait). `replicas: 1` is the recommended default for this low-load per-shoot operator (matches cert-manager's default; `replicas: 2` is an optional HA upgrade — active-passive failover, not horizontal scale). See design.md §3.6.6.

**`cmd/main.go` production readiness (verify, don't build).** The kubebuilder scaffold already wires the manager's metrics server (`Metrics.BindAddress`, `--metrics-bind-address`, default `:8443` bound behind auth), the health-probe server (`HealthProbeBindAddress`, `--health-probe-bind-address` `:8081`), the `healthz`/`readyz` checks (`mgr.AddHealthzCheck`/`AddReadyzCheck`, `healthz.Ping`), and leader election. This phase's job is to **confirm they stay wired and are surfaced in chart 1**, not to add them:
- Uncomment `LeaderElectionReleaseOnCancel: true` in `cmd/main.go` (design.md §3.6.6).
- Ensure chart 1's `deployment.yaml` sets the container's `livenessProbe` (`GET /healthz` on the probe port) and `readinessProbe` (`GET /readyz`), passes `--leader-elect=true` and `--health-probe-bind-address`/`--metrics-bind-address`, and exposes the metrics port (the kubebuilder helm plugin scaffolds these from `config/`; verify they survived and point at the right ports).
- Chart 1 already includes the metrics `ServiceMonitor`/`metrics-reader` scaffolding via `config/prometheus/` + `config/rbac/`; keep or drop per whether the seed scrapes it, but do not silently lose the probes. A Deployment without probes is the gap this note closes.

> **Chart-cache `emptyDir` volume moved to Phase 7.5.** Phase 7 does NOT cache (both loaders fetch fresh each reconcile), so no cache volume is needed for Phase 9. The `emptyDir` chart-cache mount is a **Phase 7.5** deliverable (see that phase), coupled with the caching implementation it supports.

> **Writable scratch `emptyDir` REQUIRED for Phase 7.6 subchart resolution (read-only rootfs).** Chart 1's manager container runs with `securityContext.readOnlyRootFilesystem: true` (`config/manager/manager.yaml`). Phase 7.6 (Helm subchart dependency resolution) expands the pulled chart and runs `downloader.Manager.Build()` on disk, and the loader already writes a per-render temp dir for every Helm render. With a read-only root filesystem and **no** writable volume, `os.MkdirTemp("")` (which writes under `/tmp` on the container root) **fails at runtime** — so chart 1's `deployment.yaml` MUST mount a writable `emptyDir` for the loader's scratch space and pass its path via the operator's `--source-scratch-dir` flag. Phase 7.6 already shipped the operator side: `helmLoader.scratchDir` (via `source.NewHelmLoader(scratchDir)`) + the `--source-scratch-dir` manager flag; empty preserves `os.TempDir()` behavior for local/dev runs. Wire it in the chart as:
> ```yaml
> # pod spec
> volumes:
>   - name: source-scratch
>     emptyDir:
>       sizeLimit: 256Mi   # transient: pulled .tgz + expanded chart + fetched subcharts, cleaned up per render
> # manager container
> volumeMounts:
>   - name: source-scratch
>     mountPath: /tmp/ddo-source   # or any writable path; pass the same via --source-scratch-dir
> args:
>   - --source-scratch-dir=/tmp/ddo-source
> ```
> If Phase 7.5's `source-cache` `emptyDir` lands first, this scratch space MAY be consolidated onto that same volume (point `--source-scratch-dir` at a subdir of the cache mount) rather than adding a second `emptyDir` — the design decision in the `helm-subchart-dependency-resolution` change ("reuse the Phase 7.5 cache emptyDir; no new volume") applies. Either way, a writable mount is **mandatory**, not optional, because of `readOnlyRootFilesystem: true`. Without it the operator cannot render ANY Helm source (not just subchart-wrapping ones), since every `Load` needs the scratch dir.

**Gardener egress labels on the operator Pod (Phase 7 cross-ref — REQUIRED for kustomize/OCI egress).** The operator runs per-shoot in a `shoot--cp--*` namespace **on the seed**, where Gardener enforces a `deny-all` NetworkPolicy plus label-gated allow policies. **Verified on `a-qa-de-200` / `shoot--cp--m-qa-de-200`** (2026-07): a Pod there has **no** egress — not even DNS — unless it carries the Gardener networking labels; the running `-remote` operators (ipam-capi, metal-operator, …) all carry them. A labeled smoke Pod reached `github.com` (git-upload-pack, HTTP 200 with refs advertised), `raw.githubusercontent.com` (HTTP 200), and `keppel.global.cloud.sap` (anonymous token + real chart tag list, HTTP 200); an unlabeled Pod failed DNS resolution entirely. So chart 1's `deployment.yaml` pod template **must** stamp these labels, or both the Helm OCI pull (keppel) and the kustomize root+transitive fetches (github) fail with DNS/connection errors:
```yaml
metadata:
  labels:
    networking.gardener.cloud/to-dns: allowed              # resolve github.com / keppel via cluster DNS
    networking.gardener.cloud/to-public-networks: allowed  # reach github.com / raw.githubusercontent.com / keppel
    networking.gardener.cloud/to-private-networks: allowed  # in-landscape hosts (e.g. an internal mirror), matches the -remote operators
```
This resolves `design.md` §9 open question 1 (seed→kustomize-source egress): egress **is** available on the qa landscape, **conditional on these labels** — no in-landscape git mirror is needed for qa-de-200. It is a chart requirement (a networking prerequisite), not operator code. See Phase 7 "Production `RootResolver`" egress note.

**Shoot RBAC bootstrap (install-time prerequisite — the operator cannot self-bootstrap).** The operator applies CRDs/RBAC/ServiceAccounts/WebhookConfigurations to the shoot **as the ServiceAccount its `spec.shootAccess` token-requestor Secret is minted for**. That SA cannot create those resources unless it already holds the rights, and Kubernetes privilege-escalation prevention forbids an applier from creating a ClusterRole granting powers it does not already hold. Therefore a **minimal, static** Gardener `ManagedResource` — applied by the privileged gardener-resource-manager (GRM, effectively cluster-admin on the shoot) — must seed, before the operator runs:
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

> **This bootstrap MR is a per-operator Phase 9 deliverable, not just documentation.** `chart/shoot-rbac-bootstrap.yaml` (chart 2) must be authored **once per managed operator** — the SA name and the exact apply-scoped kinds differ per operator (e.g. ipam-capi needs conversion-webhook CRD verbs; boot/argora/khalkeon need no webhook verbs). Templated from chart-2 values so `cc/kube-secrets` can vary the SA name/namespace per cluster. Without the correct per-operator MR, that operator's first shoot reconcile fails `forbidden`/privilege-escalation on every RBAC/CRD apply (surfaced per-resource as `Degraded`), so the operator is non-functional for that operator until it exists. Treat "author + verify the bootstrap MR" as a required step when onboarding each operator, alongside its CR template.

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
| 2 | Both Helm and kustomize renderers produce parsable manifest streams for seed and shoot modes on a metal-operator fixture; origin tags correct |
| 3 | Each transformation's unit tests pass with table-driven cases; a single per-render `Transformation` interface + `Build()` implemented (no cross-stream scope); a `patch` that stamps the injector's `--target-label` onto WebhookConfigurations/CRDs is covered. Also: the scaffolded `PackageWebhookConfigsForInjectorSpec` CRD type is removed (see "r7 CRD cleanup") and `make manifests generate` re-run |
| 4 | (skipped — no split step) |
| 5 | Applier applies a resource via SSA and returns Healthy status; can also delete; strips caBundle from WebhookConfigurations and conversion-webhook CRDs before apply (so the injector owns caBundle) |
| 6 | Reconciler successfully processes a CR end-to-end with mocked source; applies the ordered transformation list to both renders; produces seed/shoot manifest sets where the shoot set includes WebhookConfigurations (caBundle stripped, injector-labeled); status populated |
| 7 | Operator pulls a real (small) chart **anonymously** from `oci://keppel.global.cloud.sap/ccloud-helm/...` and fetches a real pinned-ref kustomize overlay from the public `github.com/sapcc/helm-charts` source, rendering both to manifests; the optional credential seam is exercised by a self-hosted authed test but off by default; cache pulls once per version; `make test` stays green offline while the online tier passes in CI |
| 7.6 | A chart declaring an **unvendored** `dependencies:` entry (with committed `Chart.lock`) renders its subchart's objects through the operator via `downloader.Manager.Build()`; a pre-vendored chart still renders unchanged; a no-dependency chart is unaffected; a missing/out-of-sync lock fails closed with a surfaced error; `make test` green offline with a hermetic subchart-resolution test; `metal-operator-remote-v2` can then drop vendoring |
| 8 | Equivalence test passes for at least metal-operator vs. today's chart output (seed render matches the prior seed-side output; shoot render matches the prior shoot-side output) |
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

## Phase 11: Chart-Side Publish Constraints & Workflow Considerations

**Finding (go-makefile-maker `1975bb0dd4cf`):** The `PushHelmChartToGhcr` task in go-makefile-maker lacks a path-filter field (e.g., `ignorePaths`). Chart republish is triggered **solely by versioning strategy** (`semver` vs `sha`). A semver version constraint prevents overwrite on non-version-bump pushes; `sha` versioning republishes the chart on every main push regardless of chart-only changes.

**Implication:** If this operator uses semver versioning (Recommended), chart-only main pushes skip republish by design—no action required. If using `sha` versioning and manual publish filtering is desired, a workflow-level gate (e.g., `push.paths` in `.github/workflows/publish-chart.yaml`) must be hand-authored; the go-makefile-maker toolchain cannot provide this.

**Recommendation:** Verify `Makefile.maker.yaml` versioning strategy (likely `semver`, which is safe). If `sha` versioning is chosen and chart-only filtering is critical for cost/frequency, document the manual workflow patch requirement in the chart distribution runbook.
