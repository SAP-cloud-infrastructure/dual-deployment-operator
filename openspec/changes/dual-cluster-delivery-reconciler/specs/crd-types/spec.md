<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## MODIFIED Requirements

### Requirement: DualDeploymentOperatorSpec top-level fields

`DualDeploymentOperatorSpec` MUST declare exactly six top-level fields with the following Go types and JSON tags:

| Go field | JSON tag | Required | Type |
|---|---|---|---|
| `Source` | `source` | yes | `Source` |
| `RemoteAccess` | `remoteAccess` | yes | `RemoteAccessRef` |
| `RemoteNamespace` | `remoteNamespace` | yes | `string` |
| `Transformations` | `transformations,omitempty` | no | `[]Transformation` |
| `RetentionPolicy` | `retentionPolicy,omitempty` | no | `RetentionPolicy` |
| `ApplyOrder` | `applyOrder,omitempty` | no | `string` |

`ApplyOrder` MUST carry `+kubebuilder:validation:Enum=HostFirst;RemoteFirst` and `+kubebuilder:default=RemoteFirst`. No other top-level fields SHALL be present in `DualDeploymentOperatorSpec`.

#### Scenario: Required fields reject empty spec

- **WHEN** a CR is applied with `spec: {}`
- **THEN** the API server rejects the request
- **AND** the rejection message identifies `spec.source`, `spec.remoteAccess`, and `spec.remoteNamespace` as required

#### Scenario: Optional fields default to empty

- **WHEN** a CR is applied with only `spec.source`, `spec.remoteAccess`, and `spec.remoteNamespace` set
- **THEN** the API server accepts the request
- **AND** `spec.transformations` is stored as an empty list
- **AND** `spec.retentionPolicy` is stored as `{crds: Retain}` via the object-level default (see the RetentionPolicy requirement)
- **AND** `spec.applyOrder` is stored as `RemoteFirst` via its field-level default

#### Scenario: ApplyOrder rejects invalid value

- **WHEN** a CR is applied with `spec.applyOrder: "ShootFirst"`
- **THEN** the API server rejects the request

#### Scenario: ApplyOrder accepts HostFirst

- **WHEN** a CR is applied with `spec.applyOrder: "HostFirst"`
- **THEN** the API server accepts the request and stores `spec.applyOrder: "HostFirst"`

---

### Requirement: RetentionPolicy with CRDs enum

`RetentionPolicy` MUST declare exactly one field:

```go
type RetentionPolicy struct {
    CRDs string `json:"crds,omitempty"`
}
```

`CRDs` MUST have `+kubebuilder:validation:Enum=Retain;Delete` and `+kubebuilder:default=Retain`. Additionally, the `RetentionPolicy` field within `DualDeploymentOperatorSpec` MUST carry an object-level `+kubebuilder:default={crds:Retain}` marker. Both defaults are required because Kubernetes applies a nested field default only when the parent object is present in the submitted CR — so the field-level default on `CRDs` covers the case where `spec.retentionPolicy` is present but `crds` is omitted, and the object-level default on `RetentionPolicy` covers the case where `spec.retentionPolicy` is omitted entirely. The field is named `retentionPolicy` (not `deletionPolicy`) because it governs only whether CRDs are retained on removal; every other resource is always deleted, and no other field is protected.

#### Scenario: Default is Retain when retentionPolicy is omitted entirely

- **WHEN** a CR is applied with no `spec.retentionPolicy` field
- **THEN** the stored CR has `spec.retentionPolicy.crds: "Retain"` via the object-level default on the `RetentionPolicy` field

#### Scenario: Default is Retain when crds is omitted within retentionPolicy

- **WHEN** a CR is applied with `spec.retentionPolicy: {}` (present but empty)
- **THEN** the stored CR has `spec.retentionPolicy.crds: "Retain"` via the field-level default on `CRDs`

#### Scenario: Invalid enum value is rejected

- **WHEN** a CR is applied with `spec.retentionPolicy.crds: "Purge"`
- **THEN** the API server rejects the request

---

### Requirement: RemoteAccessRef fields

`RemoteAccessRef` MUST declare two required fields and two optional fields:

```go
type RemoteAccessRef struct {
    SecretName string `json:"secretName"`
    Server     string `json:"server"`
    TokenKey   string `json:"tokenKey,omitempty"`
    CAKey      string `json:"caKey,omitempty"`
}
```

`SecretName` and `Server` are `MinLength=1` and required. `SecretName` names a Secret in the same namespace as the CR that holds the shoot ServiceAccount credentials produced by Gardener's token-requestor: a token entry (the bearer token) and a CA-bundle entry (the shoot CA). `Server` is the shoot API server URL (for example `https://kube-apiserver.<shoot-cp-namespace>.svc.cluster.local:443`). `TokenKey` and `CAKey` are optional and select which keys within the Secret hold the token and CA bundle; they MUST default to `token` and `bundle.crt` respectively (Gardener's token-requestor conventions), so a standard Gardener Secret needs neither field set.

The operator builds the shoot REST config directly from these — `{Host: Server, BearerToken: <secret[tokenKey]>, CAData: <secret[caKey]>}` — rather than parsing a kubeconfig blob, because the Gardener token-requestor Secret contains a rotating token plus CA, not a self-contained kubeconfig. `Server` is required (no derived default) because the operator Pod runs in the seed, so it cannot infer the shoot API server address from its own environment; the value is supplied per CR (mirroring how the wrapper charts already pass the shoot apiserver URL).

#### Scenario: SecretName and Server required

- **WHEN** a CR is applied with `spec.remoteAccess: {secretName: "kc"}` and no `server`
- **THEN** the API server rejects the request

#### Scenario: Server URL required

- **WHEN** a CR is applied with `spec.remoteAccess: {server: "https://api.example:443"}` and no `secretName`
- **THEN** the API server rejects the request

#### Scenario: Token and CA keys default to Gardener conventions

- **WHEN** a CR is applied with `spec.remoteAccess: {secretName: "kc", server: "https://api.example:443"}` and no `tokenKey` or `caKey`
- **THEN** the operator reads the bearer token from the Secret key `token` and the CA bundle from the Secret key `bundle.crt`

#### Scenario: Custom token and CA keys are honored

- **WHEN** a CR is applied with `spec.remoteAccess.tokenKey: "access-token"` and `spec.remoteAccess.caKey: "ca.crt"`
- **THEN** the operator reads the bearer token from Secret key `access-token` and the CA bundle from Secret key `ca.crt`

---

## RENAMED Requirements

- FROM: `### Requirement: DeletionPolicy with CRDs enum`
- TO: `### Requirement: RetentionPolicy with CRDs enum`

- FROM: `### Requirement: RemoteKubeconfigRef fields`
- TO: `### Requirement: RemoteAccessRef fields`
