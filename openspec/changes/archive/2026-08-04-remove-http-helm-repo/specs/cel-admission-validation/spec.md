<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## ADDED Requirements

### Requirement: Helm repo OCI-scheme enforced by CEL

The CRD schema for `spec.source.helm.repo` MUST carry a CEL rule that rejects any `repo` value that is not an OCI reference. The rule MUST require the value to begin with the `oci://` scheme prefix:

```
self.repo.startsWith('oci://')
```

with the message `"helm repo must be an oci:// reference"`. This enforces the OCI-only Helm chart transport at admission (analogous to the kustomize `url` ref-pinning rule), rejecting a misconfigured CR at `kubectl apply` time rather than failing opaquely at reconcile. The loader's unsupported-scheme error remains as a runtime backstop for callers that bypass admission.

#### Scenario: http(s) Helm repo is rejected

- **WHEN** a CR is applied with `spec.source.helm.repo: "https://charts.example.com"`
- **THEN** the API server rejects the request

#### Scenario: non-oci scheme Helm repo is rejected

- **WHEN** a CR is applied with `spec.source.helm.repo: "charts.example.com"` (no `oci://` prefix)
- **THEN** the API server rejects the request

#### Scenario: oci Helm repo is accepted

- **WHEN** a CR is applied with `spec.source.helm.repo: "oci://keppel.global.cloud.sap/ccloud-helm"`
- **THEN** the API server accepts the request
