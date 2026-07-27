<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## MODIFIED Requirements

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
| `AuthSecretRef` | `authSecretRef,omitempty` | no | `*SecretReference` |

Semantics: `Values` applies to both renders; `SeedValues` overrides `Values` for the seed render; `ShootValues` overrides `Values` for the shoot render. `AuthSecretRef` is optional; when set, it names a Secret (in the CR's namespace) holding chart-pull credentials (keys `username`/`password`/`token`); when unset, the chart is pulled anonymously. Value merging is executed by the Helm renderer; credential resolution is executed by the source-credentials capability.

The `Values`, `SeedValues`, and `ShootValues` fields MUST be marked `+kubebuilder:validation:Schemaless` and `+kubebuilder:pruning:PreserveUnknownFields` so arbitrary chart values are accepted without structural validation.

#### Scenario: Helm version accepts semver constraint strings

- **WHEN** a CR is applied with `spec.source.helm.version: "0.7.x"`
- **THEN** the API server accepts the request
- **AND** strict semver validation is deferred to a future validating webhook

#### Scenario: Nested values are preserved through round-trip

- **WHEN** a CR is applied with `spec.source.helm.values: {metal-operator-core: {controllerManager: {manager: {args: ["--foo"]}}}}`
- **THEN** the stored value is byte-identical to the input after JSON canonicalization
- **AND** the operator can read the nested structure via `unstructured` accessors

#### Scenario: authSecretRef is optional

- **WHEN** a CR is applied with `spec.source.helm` set but no `authSecretRef`
- **THEN** the API server accepts the request
- **AND** the chart is pulled anonymously at reconcile time

#### Scenario: authSecretRef names a namespaced Secret

- **WHEN** a CR is applied with `spec.source.helm.authSecretRef.name: "keppel-pull"`
- **THEN** the API server accepts the request
- **AND** the stored `authSecretRef.name` is exactly `keppel-pull`

---

### Requirement: KustomizeSource fields with required overlay paths

`KustomizeSource` MUST declare the following fields:

| Go field | JSON tag | Required | Type | Constraint |
|---|---|---|---|---|
| `URL` | `url` | yes | `string` | `MinLength=1`, must include `?ref=<value>` (CEL) |
| `SeedPath` | `seedPath` | yes | `string` | `MinLength=1` |
| `ShootPath` | `shootPath` | yes | `string` | `MinLength=1` |
| `AuthSecretRef` | `authSecretRef,omitempty` | no | `*SecretReference` | — |

`SeedPath` and `ShootPath` are subpaths under `URL`; they name the two overlay directories the operator renders (seed and shoot respectively). No `+kubebuilder:default` markers are set on either field. This deviates from `docs/design.md` §3.3 and `docs/implementation.md` §Phase 1, which describe both fields as optional with defaults `"seed"` and `"shoot"` — the deviation is deliberate per the `design.md` decision "KustomizeSource.SeedPath and ShootPath are both required, no defaults."

`AuthSecretRef` is optional; when set, it names a Secret (in the CR's namespace) holding git HTTPS credentials (keys `username`/`password`/`token`); when unset, the root is fetched anonymously.

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

#### Scenario: authSecretRef is optional for kustomize

- **WHEN** a CR is applied with a valid `spec.source.kustomize` but no `authSecretRef`
- **THEN** the API server accepts the request
- **AND** the git root is fetched anonymously at reconcile time

---

## ADDED Requirements

### Requirement: SecretReference type for source authentication

A `SecretReference` type MUST be declared for use by `HelmSource.AuthSecretRef` and `KustomizeSource.AuthSecretRef`. It MUST carry at least a `Name` field (JSON tag `name`, `MinLength=1`) naming a Secret in the CR's own namespace. Cross-namespace references MUST NOT be supported (no `namespace` field), matching the `shootAccess` convention that credential Secrets live in the CR's namespace.

#### Scenario: SecretReference requires a name

- **WHEN** a CR is applied with `authSecretRef: {}` (no `name`)
- **THEN** the API server rejects the request via the `MinLength=1` constraint on `name`

#### Scenario: SecretReference has no namespace field

- **WHEN** the CRD schema for `authSecretRef` is inspected
- **THEN** it exposes only `name` (no `namespace`), so the Secret is always resolved in the CR's namespace
