# Spec: CRD Types

## Purpose

Defines the Go type shapes for the `DualDeploymentOperator` CRD, including the API group registration, spec/status structures, discriminated unions for source and transformation variants, and all leaf field types. Generated CRD manifests and deepcopy code are produced from these types via `controller-gen`.
## Requirements
### Requirement: DualDeploymentOperator CRD registration

The `dual-deployment-operator` module SHALL register a Custom Resource Definition named `dualdeploymentoperators.dual-deployment-operator.cc.sap` at API group `dual-deployment-operator.cc.sap`, version `v1alpha1`, kind `DualDeploymentOperator`, scope `Namespaced`, short name `ddo`, singular `dualdeploymentoperator`, and plural `dualdeploymentoperators`. The CRD SHALL be generated from Go source under `api/v1alpha1/` via `controller-gen` and MUST NOT be hand-authored.

#### Scenario: CRD manifest is generated from Go types

- **WHEN** a maintainer runs `make manifests`
- **THEN** a file `config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml` is created or updated
- **AND** the file's `spec.group` is `dual-deployment-operator.cc.sap`
- **AND** the file's `spec.versions[0].name` is `v1alpha1`
- **AND** the file's `spec.names.kind` is `DualDeploymentOperator`
- **AND** the file's `spec.scope` is `Namespaced`

#### Scenario: CRD installs into envtest

- **WHEN** the envtest fixture applies `config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml`
- **THEN** the API server reports the CRD as Established within 30 seconds
- **AND** `kubectl api-resources` lists `dualdeploymentoperators` in the target API group

---

### Requirement: DualDeploymentOperatorSpec top-level fields

`DualDeploymentOperatorSpec` MUST declare exactly five top-level fields with the following Go types and JSON tags:

| Go field | JSON tag | Required | Type |
|---|---|---|---|
| `Source` | `source` | yes | `Source` |
| `RemoteKubeconfig` | `remoteKubeconfig` | yes | `RemoteKubeconfigRef` |
| `RemoteNamespace` | `remoteNamespace` | yes | `string` |
| `Transformations` | `transformations,omitempty` | no | `[]Transformation` |
| `DeletionPolicy` | `deletionPolicy,omitempty` | no | `DeletionPolicy` |

No other top-level fields SHALL be present in `DualDeploymentOperatorSpec`.

#### Scenario: Required fields reject empty spec

- **WHEN** a CR is applied with `spec: {}`
- **THEN** the API server rejects the request
- **AND** the rejection message identifies `spec.source`, `spec.remoteKubeconfig`, and `spec.remoteNamespace` as required

#### Scenario: Optional fields default to empty

- **WHEN** a CR is applied with only `spec.source`, `spec.remoteKubeconfig`, and `spec.remoteNamespace` set
- **THEN** the API server accepts the request
- **AND** `spec.transformations` is stored as an empty list
- **AND** `spec.deletionPolicy` is stored as `{crds: Retain}` via the object-level default (see the DeletionPolicy requirement)

### Requirement: Source discriminated union

The `Source` Go type SHALL be a discriminated union with exactly two pointer fields:

```go
type Source struct {
    Helm      *HelmSource      `json:"helm,omitempty"`
    Kustomize *KustomizeSource `json:"kustomize,omitempty"`
}
```

Callers SHALL set exactly one of `Helm` or `Kustomize`; the "exactly one" invariant is enforced by CEL (see the `cel-admission-validation` spec).

#### Scenario: Helm source variant compiles and marshals

- **WHEN** a Go program constructs `Source{Helm: &HelmSource{Repo: "oci://x", Name: "y", Version: "1.0.0"}}`
- **THEN** `json.Marshal` produces `{"helm":{"repo":"oci://x","name":"y","version":"1.0.0"}}`
- **AND** `json.Unmarshal` of that output round-trips to an equal `Source` value

#### Scenario: Kustomize source variant compiles and marshals

- **WHEN** a Go program constructs `Source{Kustomize: &KustomizeSource{URL: "https://github.com/x/y//p?ref=v1", HostPath: "host", RemotePath: "remote"}}`
- **THEN** `json.Marshal` produces `{"kustomize":{"url":"https://github.com/x/y//p?ref=v1","hostPath":"host","remotePath":"remote"}}`
- **AND** `json.Unmarshal` of that output round-trips to an equal `Source` value

---

### Requirement: HelmSource fields for two-render pattern

`HelmSource` MUST declare the following fields to support the two-render pattern where the operator renders the chart once per mode with mode-specific values merged on top of common values:

| Go field | JSON tag | Required | Type |
|---|---|---|---|
| `Repo` | `repo` | yes (`MinLength=1`) | `string` |
| `Name` | `name` | yes (`MinLength=1`) | `string` |
| `Version` | `version` | yes (`MinLength=1`) | `string` |
| `Values` | `values,omitempty` | no | `*apiextensionsv1.JSON` |
| `HostValues` | `hostValues,omitempty` | no | `*apiextensionsv1.JSON` |
| `RemoteValues` | `remoteValues,omitempty` | no | `*apiextensionsv1.JSON` |

Semantics: `Values` applies to both renders; `HostValues` overrides `Values` for the host render; `RemoteValues` overrides `Values` for the remote render. This change lands the type shape only; value merging is executed by Phase 2's Helm renderer.

The `Values`, `HostValues`, and `RemoteValues` fields MUST be marked `+kubebuilder:validation:Schemaless` and `+kubebuilder:pruning:PreserveUnknownFields` so arbitrary chart values are accepted without structural validation.

#### Scenario: Helm version accepts semver constraint strings

- **WHEN** a CR is applied with `spec.source.helm.version: "0.7.x"`
- **THEN** the API server accepts the request
- **AND** strict semver validation is deferred to a future validating webhook

#### Scenario: Nested values are preserved through round-trip

- **WHEN** a CR is applied with `spec.source.helm.values: {metal-operator-core: {controllerManager: {manager: {args: ["--foo"]}}}}`
- **THEN** the stored value is byte-identical to the input after JSON canonicalization
- **AND** the operator can read the nested structure via `unstructured` accessors

---

### Requirement: KustomizeSource fields with required overlay paths

`KustomizeSource` MUST declare exactly three fields, all required (no defaults):

| Go field | JSON tag | Required | Type | Constraint |
|---|---|---|---|---|
| `URL` | `url` | yes | `string` | `MinLength=1`, must include `?ref=<value>` (CEL) |
| `HostPath` | `hostPath` | yes | `string` | `MinLength=1` |
| `RemotePath` | `remotePath` | yes | `string` | `MinLength=1` |

`HostPath` and `RemotePath` are subpaths under `URL`; they name the two overlay directories the operator renders (host and remote respectively). No `+kubebuilder:default` markers are set on either field. This deviates from `docs/design.md` §3.3 and `docs/implementation.md` §Phase 1, which describe both fields as optional with defaults `"host"` and `"remote"` — the deviation is deliberate per the `design.md` decision "KustomizeSource.HostPath and RemotePath are both required, no defaults."

#### Scenario: Missing HostPath is rejected

- **WHEN** a CR is applied with `spec.source.kustomize.url: "https://github.com/x/y//p?ref=v1"` and `remotePath: "remote"` but no `hostPath`
- **THEN** the API server rejects the request
- **AND** the rejection message identifies `spec.source.kustomize.hostPath` as required

#### Scenario: Empty HostPath is rejected

- **WHEN** a CR is applied with `spec.source.kustomize.hostPath: ""`
- **THEN** the API server rejects the request via the `MinLength=1` constraint

#### Scenario: Explicit paths are accepted

- **WHEN** a CR is applied with `spec.source.kustomize: {url: "https://github.com/x/y//p?ref=v1", hostPath: "overlays/host", remotePath: "overlays/remote"}`
- **THEN** the API server accepts the request
- **AND** the stored `hostPath` and `remotePath` values are exactly as submitted

---

### Requirement: RemoteKubeconfigRef fields

`RemoteKubeconfigRef` MUST declare exactly two required fields:

```go
type RemoteKubeconfigRef struct {
    SecretName string `json:"secretName"`
    Key        string `json:"key"`
}
```

Both fields are `MinLength=1`. `SecretName` names a Secret in the same namespace as the CR; `Key` names the entry within that Secret that contains kubeconfig bytes.

#### Scenario: Both fields required

- **WHEN** a CR is applied with `spec.remoteKubeconfig: {secretName: "kc"}` and no `key`
- **THEN** the API server rejects the request

---

### Requirement: Transformation discriminated union with 4 types across 2 scopes

`Transformation` MUST declare exactly four pointer fields, forming a discriminated union where exactly one is set per entry:

```go
type Transformation struct {
    // Per-render (3)
    Patch             *PatchSpec             `json:"patch,omitempty"`
    RewriteWebhookURL *RewriteWebhookURLSpec `json:"rewriteWebhookURL,omitempty"`
    FilterKinds       *FilterKindsSpec       `json:"filterKinds,omitempty"`
    // Cross-stream (1)
    PackageWebhookConfigsForInjector *PackageWebhookConfigsForInjectorSpec `json:"packageWebhookConfigsForInjector,omitempty"`
}
```

The "exactly one" invariant is enforced by CEL (see `cel-admission-validation` spec).

#### Scenario: Round-trip for each variant

- **WHEN** a Go program constructs a `Transformation` with each of the four fields (one at a time)
- **THEN** each `json.Marshal`/`json.Unmarshal` cycle round-trips without loss

---

### Requirement: PatchSpec typed variants

`PatchSpec` MUST declare exactly three fields:

```go
type PatchSpec struct {
    Target         Selector              `json:"target"`
    StrategicMerge *apiextensionsv1.JSON `json:"strategicMerge,omitempty"`
    JSONPatch      []JSONPatchOp         `json:"jsonPatch,omitempty"`
}
```

Exactly one of `StrategicMerge` or `JSONPatch` MUST be set per `PatchSpec` (enforced by CEL). `StrategicMerge` MUST be marked `+kubebuilder:validation:Schemaless` and `+kubebuilder:pruning:PreserveUnknownFields`.

#### Scenario: StrategicMerge variant accepts arbitrary object content

- **WHEN** a CR is applied with `spec.transformations[0].patch: {target: {kind: Deployment}, strategicMerge: {spec: {template: {spec: {initContainers: [{name: sidecar}]}}}}}`
- **THEN** the API server accepts the request
- **AND** the stored content is byte-identical after JSON canonicalization

#### Scenario: JSONPatch variant validates op enum

- **WHEN** a CR is applied with `spec.transformations[0].patch: {target: {kind: Deployment}, jsonPatch: [{op: "invalid-op", path: "/metadata/labels"}]}`
- **THEN** the API server rejects the request via the `Op` enum constraint

---

### Requirement: JSONPatchOp fields with RFC 6902 op enum

`JSONPatchOp` MUST declare exactly four fields:

```go
type JSONPatchOp struct {
    Op    string                `json:"op"`
    Path  string                `json:"path"`
    From  string                `json:"from,omitempty"`
    Value *apiextensionsv1.JSON `json:"value,omitempty"`
}
```

`Op` MUST have `+kubebuilder:validation:Enum=add;remove;replace;move;copy;test`. `Path` MUST have `+kubebuilder:validation:MinLength=1`. `Value` MUST be marked `+kubebuilder:validation:Schemaless` and `+kubebuilder:pruning:PreserveUnknownFields`.

#### Scenario: All six RFC 6902 ops are accepted

- **WHEN** a CR is applied with a `jsonPatch` list containing one op of each: `add`, `remove`, `replace`, `move`, `copy`, `test`
- **THEN** the API server accepts the request

---

### Requirement: RewriteWebhookURLSpec fields

`RewriteWebhookURLSpec` MUST declare exactly two fields:

```go
type RewriteWebhookURLSpec struct {
    URLPrefix   string   `json:"urlPrefix"`
    TargetKinds []string `json:"targetKinds,omitempty"`
}
```

`URLPrefix` is `MinLength=1`. `TargetKinds` is optional; when empty, the transformation applies to Validating and Mutating WebhookConfiguration kinds by default.

#### Scenario: Minimal spec is accepted

- **WHEN** a CR is applied with `spec.transformations[0].rewriteWebhookURL.urlPrefix: "https://example:443"`
- **THEN** the API server accepts the request

---

### Requirement: FilterKindsSpec fields

`FilterKindsSpec` MUST declare exactly two fields:

```go
type FilterKindsSpec struct {
    Kinds  []string `json:"kinds"`
    Source string   `json:"source,omitempty"`
}
```

`Kinds` MUST have `+kubebuilder:validation:MinItems=1`. `Source` is optional; valid values are `"upstream"`, `"additions"`, or empty (both).

#### Scenario: Empty kinds list is rejected

- **WHEN** a CR is applied with `spec.transformations[0].filterKinds: {kinds: []}`
- **THEN** the API server rejects the request

---

### Requirement: PackageWebhookConfigsForInjectorSpec fields

`PackageWebhookConfigsForInjectorSpec` MUST declare exactly two fields:

```go
type PackageWebhookConfigsForInjectorSpec struct {
    ConfigMapName string `json:"configMapName"`
    DataKey       string `json:"dataKey,omitempty"`
}
```

`ConfigMapName` is `MinLength=1`. `DataKey` is optional; the operator uses `"webhooks.yaml"` when empty.

#### Scenario: Only ConfigMapName is required

- **WHEN** a CR is applied with `spec.transformations[0].packageWebhookConfigsForInjector.configMapName: "webhook-config"`
- **THEN** the API server accepts the request

---

### Requirement: Selector fields with glob name matching

`Selector` MUST declare exactly three optional fields:

```go
type Selector struct {
    Kind   string `json:"kind,omitempty"`
    Name   string `json:"name,omitempty"`
    Origin string `json:"origin,omitempty"`
}
```

`Name` supports the glob `*` to match any name. `Origin` accepts `"upstream"` or `"additions"` or empty. Matching semantics (AND across set fields; unset fields match everything) are implemented by future phases; this change lands the type shape only.

#### Scenario: Empty selector is accepted

- **WHEN** a CR is applied with `spec.transformations[0].patch.target: {}`
- **THEN** the API server accepts the request

---

### Requirement: DeletionPolicy with CRDs enum

`DeletionPolicy` MUST declare exactly one field:

```go
type DeletionPolicy struct {
    CRDs string `json:"crds,omitempty"`
}
```

`CRDs` MUST have `+kubebuilder:validation:Enum=Retain;Delete` and `+kubebuilder:default=Retain`. Additionally, the `DeletionPolicy` field within `DualDeploymentOperatorSpec` MUST carry an object-level `+kubebuilder:default={crds:Retain}` marker. Both defaults are required because Kubernetes applies a nested field default only when the parent object is present in the submitted CR — so the field-level default on `CRDs` covers the case where `spec.deletionPolicy` is present but `crds` is omitted, and the object-level default on `DeletionPolicy` covers the case where `spec.deletionPolicy` is omitted entirely.

#### Scenario: Default is Retain when deletionPolicy is omitted entirely

- **WHEN** a CR is applied with no `spec.deletionPolicy` field
- **THEN** the stored CR has `spec.deletionPolicy.crds: "Retain"` via the object-level default on the `DeletionPolicy` field

#### Scenario: Default is Retain when crds is omitted within deletionPolicy

- **WHEN** a CR is applied with `spec.deletionPolicy: {}` (present but empty)
- **THEN** the stored CR has `spec.deletionPolicy.crds: "Retain"` via the field-level default on `CRDs`

#### Scenario: Invalid enum value is rejected

- **WHEN** a CR is applied with `spec.deletionPolicy.crds: "Purge"`
- **THEN** the API server rejects the request

---

### Requirement: DualDeploymentOperatorStatus with health tracking types

`DualDeploymentOperatorStatus` MUST declare all fields that later phases will populate, so the CRD status subresource shape is stable from v1:

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

`HealthState` MUST have `+kubebuilder:validation:Enum=Healthy;Progressing;Degraded;Unknown`. The v1 reconciler MUST NOT populate any status field — Phase 6 activates population.

#### Scenario: Status subresource is enabled

- **WHEN** the CRD is applied to a cluster
- **THEN** the CRD's `spec.versions[0].subresources.status` is set to `{}`
- **AND** `kubectl edit ddo <name>` does not permit spec changes when only status is edited

#### Scenario: Empty status round-trips

- **WHEN** the no-op reconciler processes a CR
- **THEN** `status.hostResources`, `status.remoteResources`, `status.conditions`, and `status.lastReconcile` remain unset

---

### Requirement: Generated deepcopy code

The build MUST produce `api/v1alpha1/zz_generated.deepcopy.go` via `controller-gen object`, and this file MUST NOT be hand-edited.

#### Scenario: Deepcopy is regenerated by make

- **WHEN** a maintainer runs `make generate`
- **THEN** `api/v1alpha1/zz_generated.deepcopy.go` exists
- **AND** the file compiles with `go build ./api/v1alpha1/...`
- **AND** every type declared in `dualdeploymentoperator_types.go` has a `DeepCopy*` method

---

### Requirement: Group version registration

The package `api/v1alpha1` MUST declare a `GroupVersion` variable at `dual-deployment-operator.cc.sap/v1alpha1` and register `DualDeploymentOperator` and `DualDeploymentOperatorList` in the scheme.

#### Scenario: Scheme registration succeeds

- **WHEN** the operator's `cmd/main.go` calls `v1alpha1.AddToScheme(scheme)`
- **THEN** the call returns `nil`
- **AND** the scheme can encode and decode `DualDeploymentOperator` objects

### Requirement: RemoteNamespace field

`DualDeploymentOperatorSpec.RemoteNamespace` (JSON tag `remoteNamespace`) MUST be a required string naming the target namespace for the remote (shoot) render and delivery. It MUST be validated as a DNS-1123 label (`+kubebuilder:validation:MinLength=1` and `+kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$``). It carries no `omitempty` and no default — a value must be supplied. The host render/delivery does NOT use this field; the host target namespace is the CR's own `metadata.namespace`.

#### Scenario: Missing remoteNamespace is rejected

- **WHEN** a CR is applied with `spec.source` and `spec.remoteKubeconfig` set but no `spec.remoteNamespace`
- **THEN** the API server rejects the request
- **AND** the rejection identifies `spec.remoteNamespace` as required

#### Scenario: Invalid namespace value is rejected

- **WHEN** a CR is applied with `spec.remoteNamespace` set to a value that is not a valid DNS-1123 label (for example `Invalid_NS` or an empty string)
- **THEN** the API server rejects the request

#### Scenario: Valid remoteNamespace is accepted

- **WHEN** a CR is applied with `spec.remoteNamespace` set to a valid DNS-1123 label (for example `metal-operator`)
- **THEN** the API server accepts the request
- **AND** the stored object preserves `spec.remoteNamespace`

