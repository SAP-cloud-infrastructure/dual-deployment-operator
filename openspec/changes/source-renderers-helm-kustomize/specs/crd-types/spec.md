# Spec Delta: crd-types (source-renderers-helm-kustomize)

This change adds a required `spec.remoteNamespace` field to `DualDeploymentOperatorSpec`, extending the CRD types capability originally defined by the scaffold change.

## MODIFIED Requirements

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

## ADDED Requirements

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
