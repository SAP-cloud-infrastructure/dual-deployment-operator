<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## MODIFIED Requirements

### Requirement: DualDeploymentOperator CRD registration

The `dual-deployment-operator` module SHALL register a Custom Resource Definition named `dualdeploymentoperators.dual-deployment-operator.cc.sap` at API group `dual-deployment-operator.cc.sap`, version `v1alpha1`, kind `DualDeploymentOperator`, scope `Namespaced`, short name `ddo`, singular `dualdeploymentoperator`, and plural `dualdeploymentoperators`. The CRD SHALL be generated from Go source under `api/v1alpha1/` via `controller-gen` and MUST NOT be hand-authored.

The CRD SHALL declare `additionalPrinterColumns` so that reconcile status is visible in `kubectl get` and list-based UIs (e.g. Lens/Freelens) without inspecting `status.conditions[]`. The columns SHALL be `Ready` (JSONPath `.status.conditions[?(@.type=="Ready")].status`), `Reason` (JSONPath `.status.conditions[?(@.type=="Ready")].reason`), and `Age` (JSONPath `.metadata.creationTimestamp`, type `date`). These columns are declared via `+kubebuilder:printcolumn` markers on the `DualDeploymentOperator` type; they are marker-only and MUST NOT change the CRD's spec or status schema.

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

#### Scenario: Status is visible in list-view via printer-columns

- **WHEN** a maintainer runs `make manifests`
- **THEN** the generated CRD's `spec.versions[0].additionalPrinterColumns` declares columns named `Ready`, `Reason`, and `Age`
- **AND** the `Ready` column's `jsonPath` is `.status.conditions[?(@.type=="Ready")].status`
- **AND** the `Reason` column's `jsonPath` is `.status.conditions[?(@.type=="Ready")].reason`
- **AND** the `Age` column's `jsonPath` is `.metadata.creationTimestamp` with type `date`

#### Scenario: kubectl get surfaces Ready status

- **WHEN** a `DualDeploymentOperator` with a populated `Ready` condition exists and `kubectl get ddo` is run
- **THEN** the output includes a `READY` column reflecting the `Ready` condition status
- **AND** a `REASON` column reflecting the `Ready` condition reason
