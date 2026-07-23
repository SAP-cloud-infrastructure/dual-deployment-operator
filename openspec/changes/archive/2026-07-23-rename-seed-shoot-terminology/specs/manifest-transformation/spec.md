<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## MODIFIED Requirements

### Requirement: Transformations do not mutate input in place

Every transformation's `Apply` SHALL return fresh `[]manifest.Manifest` and MUST NOT mutate the caller's input slice or the underlying `unstructured.Unstructured` objects. The reconciler threads the same ordered `[]Transformation` through the seed render and the shoot render independently, so mutating shared state would corrupt the second render.

#### Scenario: Input slice and objects unchanged after Apply

- **WHEN** any transformation's `Apply` is called on a manifest stream
- **THEN** the caller's input slice is unchanged after the call
- **AND** the underlying `Unstructured` objects referenced by the input are unchanged
