<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## RENAMED Requirements

- FROM: `### Requirement: RemoteAccessRef fields`
- TO: `### Requirement: ShootAccessRef fields`
- FROM: `### Requirement: RemoteNamespace field`
- TO: `### Requirement: ShootNamespace field`

---

## MODIFIED Requirements

### Requirement: DualDeploymentOperatorSpec top-level fields

`DualDeploymentOperatorSpec` MUST declare exactly six top-level fields with the following Go types and JSON tags:

| Go field | JSON tag | Required | Type |
|---|---|---|---|
| `Source` | `source` | yes | `Source` |
| `ShootAccess` | `shootAccess` | yes | `ShootAccessRef` |
| `ShootNamespace` | `shootNamespace` | yes | `string` |
| `Transformations` | `transformations,omitempty` | no | `[]Transformation` |
| `RetentionPolicy` | `retentionPolicy,omitempty` | no | `RetentionPolicy` |
| `ApplyOrder` | `applyOrder,omitempty` | no | `string` |

`ApplyOrder` MUST carry `+kubebuilder:validation:Enum=SeedFirst;ShootFirst` and `+kubebuilder:default=ShootFirst`. No other top-level fields SHALL be present in `DualDeploymentOperatorSpec`.

#### Scenario: Required fields reject empty spec

- **WHEN** a CR is applied with `spec: {}`
- **THEN** the API server rejects the request
- **AND** the rejection message identifies `spec.source`, `spec.shootAccess`, and `spec.shootNamespace` as required

#### Scenario: Optional fields default to empty

- **WHEN** a CR is applied with only `spec.source`, `spec.shootAccess`, and `spec.shootNamespace` set
- **THEN** the API server accepts the request
- **AND** `spec.transformations` is stored as an empty list
- **AND** `spec.retentionPolicy` is stored as `{crds: Retain}` via the object-level default (see the RetentionPolicy requirement)
- **AND** `spec.applyOrder` is stored as `ShootFirst` via its field-level default

#### Scenario: ApplyOrder rejects invalid value

- **WHEN** a CR is applied with `spec.applyOrder: "HostFirst"`
- **THEN** the API server rejects the request

#### Scenario: ApplyOrder accepts SeedFirst

- **WHEN** a CR is applied with `spec.applyOrder: "SeedFirst"`
- **THEN** the API server accepts the request and stores `spec.applyOrder: "SeedFirst"`

### Requirement: DualDeploymentOperatorStatus with health tracking types

`DualDeploymentOperatorStatus` MUST declare all fields that later phases will populate, so the CRD status subresource shape is stable from v1:

```go
type DualDeploymentOperatorStatus struct {
    SeedResources  []ResourceStatus   `json:"seedResources,omitempty"`
    ShootResources []ResourceStatus   `json:"shootResources,omitempty"`
    Conditions     []metav1.Condition `json:"conditions,omitempty"`
    LastReconcile  *metav1.Time       `json:"lastReconcile,omitempty"`
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
- **THEN** `status.seedResources`, `status.shootResources`, `status.conditions`, and `status.lastReconcile` remain unset

### Requirement: Source discriminated union

`Source` MUST be a discriminated union with exactly one of two optional pointer fields set: `Helm *HelmSource` (JSON `helm,omitempty`) or `Kustomize *KustomizeSource` (JSON `kustomize,omitempty`). Enforcement of "exactly one" is a CEL validation concern (see the cel-admission-validation capability); the type shape here only declares the two variants.

#### Scenario: Helm source variant compiles and marshals

- **WHEN** a Go program constructs `Source{Helm: &HelmSource{Repo: "oci://x", Name: "y", Version: "1.0.0"}}`
- **THEN** `json.Marshal` produces `{"helm":{"repo":"oci://x","name":"y","version":"1.0.0"}}`
- **AND** `json.Unmarshal` of that output round-trips to an equal `Source` value

#### Scenario: Kustomize source variant compiles and marshals

- **WHEN** a Go program constructs `Source{Kustomize: &KustomizeSource{URL: "https://github.com/x/y//p?ref=v1", SeedPath: "seed", ShootPath: "shoot"}}`
- **THEN** `json.Marshal` produces `{"kustomize":{"url":"https://github.com/x/y//p?ref=v1","seedPath":"seed","shootPath":"shoot"}}`
- **AND** `json.Unmarshal` of that output round-trips to an equal `Source` value

### Requirement: HelmSource fields for two-render pattern

`HelmSource` MUST declare the following fields to support the two-render pattern where the operator renders the chart once per mode with mode-specific values merged on top of common values:

| Go field | JSON tag | Required | Type |
|---|---|---|---|
| `Repo` | `repo` | yes (`MinLength=1`) | `string` |
| `Name` | `name` | yes (`MinLength=1`) | `string` |
| `Version` | `version` | yes (`MinLength=1`) | `string` |
| `Values` | `values,omitempty` | no | `*apiextensionsv1.JSON` |
| `SeedValues` | `seedValues,omitempty` | no | `*apiextensionsv1.JSON` |
| `ShootValues` | `shootValues,omitempty` | no | `*apiextensionsv1.JSON` |

Semantics: `Values` applies to both renders; `SeedValues` overrides `Values` for the seed render; `ShootValues` overrides `Values` for the shoot render. This change lands the type shape only; value merging is executed by Phase 2's Helm renderer.

The `Values`, `SeedValues`, and `ShootValues` fields MUST be marked `+kubebuilder:validation:Schemaless` and `+kubebuilder:pruning:PreserveUnknownFields` so arbitrary chart values are accepted without structural validation.

#### Scenario: Helm version accepts semver constraint strings

- **WHEN** a CR is applied with `spec.source.helm.version: "0.7.x"`
- **THEN** the API server accepts the request
- **AND** strict semver validation is deferred to a future validating webhook

#### Scenario: Nested values are preserved through round-trip

- **WHEN** a CR is applied with `spec.source.helm.values: {metal-operator-core: {controllerManager: {manager: {args: ["--foo"]}}}}`
- **THEN** the stored value is byte-identical to the input after JSON canonicalization
- **AND** the operator can read the nested structure via `unstructured` accessors

### Requirement: KustomizeSource fields with required overlay paths

`KustomizeSource` MUST declare exactly three fields, all required (no defaults):

| Go field | JSON tag | Required | Type | Constraint |
|---|---|---|---|---|
| `URL` | `url` | yes | `string` | `MinLength=1`, must include `?ref=<value>` (CEL) |
| `SeedPath` | `seedPath` | yes | `string` | `MinLength=1` |
| `ShootPath` | `shootPath` | yes | `string` | `MinLength=1` |

`SeedPath` and `ShootPath` are subpaths under `URL`; they name the two overlay directories the operator renders (seed and shoot respectively). No `+kubebuilder:default` markers are set on either field. This deviates from `docs/design.md` §3.3 and `docs/implementation.md` §Phase 1, which describe both fields as optional with defaults `"seed"` and `"shoot"` — the deviation is deliberate per the `design.md` decision "KustomizeSource.SeedPath and ShootPath are both required, no defaults."

#### Scenario: Missing SeedPath is rejected

- **WHEN** a CR is applied with `spec.source.kustomize.url: "https://github.com/x/y//p?ref=v1"` and `shootPath: "shoot"` but no `seedPath`
- **THEN** the API server rejects the request
- **AND** the rejection message identifies `spec.source.kustomize.seedPath` as required

#### Scenario: Empty SeedPath is rejected

- **WHEN** a CR is applied with `spec.source.kustomize.seedPath: ""`
- **THEN** the API server rejects the request via the `MinLength=1` constraint

#### Scenario: Explicit paths are accepted

- **WHEN** a CR is applied with `spec.source.kustomize: {url: "https://github.com/x/y//p?ref=v1", seedPath: "overlays/seed", shootPath: "overlays/shoot"}`
- **THEN** the API server accepts the request
- **AND** the stored `seedPath` and `shootPath` values are exactly as submitted

### Requirement: ShootAccessRef fields

`ShootAccessRef` MUST declare two required fields and two optional fields:

```go
type ShootAccessRef struct {
    SecretName string `json:"secretName"`
    Server     string `json:"server"`
    TokenKey   string `json:"tokenKey,omitempty"`
    CAKey      string `json:"caKey,omitempty"`
}
```

`SecretName` and `Server` are `MinLength=1` and required. `SecretName` names a Secret in the same namespace as the CR that holds the shoot ServiceAccount credentials produced by Gardener's token-requestor: a token entry (the bearer token) and a CA-bundle entry (the shoot CA). `Server` is the shoot API server URL (for example `https://kube-apiserver.<shoot-cp-namespace>.svc.cluster.local:443`). `TokenKey` and `CAKey` are optional and select which keys within the Secret hold the token and CA bundle; they MUST default to `token` and `bundle.crt` respectively (Gardener's token-requestor conventions), so a standard Gardener Secret needs neither field set.

The operator builds the shoot REST config directly from these — `{Host: Server, BearerToken: <secret[tokenKey]>, CAData: <secret[caKey]>}` — rather than parsing a kubeconfig blob, because the Gardener token-requestor Secret contains a rotating token plus CA, not a self-contained kubeconfig. `Server` is required (no derived default) because the operator Pod runs in the seed, so it cannot infer the shoot API server address from its own environment; the value is supplied per CR (mirroring how the wrapper charts already pass the shoot apiserver URL).

#### Scenario: SecretName and Server required

- **WHEN** a CR is applied with `spec.shootAccess: {secretName: "kc"}` and no `server`
- **THEN** the API server rejects the request

#### Scenario: Server URL required

- **WHEN** a CR is applied with `spec.shootAccess: {server: "https://api.example:443"}` and no `secretName`
- **THEN** the API server rejects the request

#### Scenario: Token and CA keys default to Gardener conventions

- **WHEN** a CR is applied with `spec.shootAccess: {secretName: "kc", server: "https://api.example:443"}` and no `tokenKey` or `caKey`
- **THEN** the operator reads the bearer token from the Secret key `token` and the CA bundle from the Secret key `bundle.crt`

### Requirement: ShootNamespace field

`DualDeploymentOperatorSpec.ShootNamespace` (JSON tag `shootNamespace`) MUST be a required string naming the target namespace for the shoot render and delivery. It MUST be validated as a DNS-1123 label (`+kubebuilder:validation:MinLength=1` and `+kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$``). It carries no `omitempty` and no default — a value must be supplied. The seed render/delivery does NOT use this field; the seed target namespace is the CR's own `metadata.namespace`.

#### Scenario: Missing shootNamespace is rejected

- **WHEN** a CR is applied with `spec.source` and `spec.shootAccess` set but no `spec.shootNamespace`
- **THEN** the API server rejects the request
- **AND** the rejection identifies `spec.shootNamespace` as required

#### Scenario: Invalid namespace value is rejected

- **WHEN** a CR is applied with `spec.shootNamespace` set to a value that is not a valid DNS-1123 label (for example `Invalid_NS` or an empty string)
- **THEN** the API server rejects the request

#### Scenario: Valid shootNamespace is accepted

- **WHEN** a CR is applied with `spec.shootNamespace` set to a valid DNS-1123 label (for example `metal-operator`)
- **THEN** the API server accepts the request
- **AND** the stored object preserves `spec.shootNamespace`
