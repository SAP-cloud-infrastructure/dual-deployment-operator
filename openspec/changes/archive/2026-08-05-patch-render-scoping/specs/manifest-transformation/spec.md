<!-- SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

## MODIFIED Requirements

### Requirement: patch transformation

The `patch` transformation SHALL apply a Kubernetes-native patch to every manifest matching its target `Selector`. Exactly one of `strategicMerge` (a JSON object) or `jsonPatch` (an array of RFC 6902 ops) MUST be set; the transformation MUST re-validate this exactly-one invariant at runtime and return an error if neither or both are set. A `strategicMerge` patch MUST be applied via `k8s.io/apimachinery/pkg/util/strategicpatch`; a `jsonPatch` MUST be applied via `github.com/evanphx/json-patch` following RFC 6902 semantics. If the selector matches zero manifests, `patch` MUST return the input manifests unchanged with no error (a clean no-op), consistent with the zero-match behavior of `rewriteWebhookURL` and `filterKinds`; it MUST NOT fail loud and MUST NOT stop the reconcile. Patching MUST be idempotent for byte-identical inputs.

#### Scenario: strategicMerge patches matching manifests

- **WHEN** `patch` with a `strategicMerge` object targets a `Deployment` present in the stream
- **THEN** the matched `Deployment` is returned with the strategic-merge patch applied
- **AND** non-matching manifests are returned unchanged

#### Scenario: jsonPatch applies operations by path

- **WHEN** `patch` with a `jsonPatch` op list targets a matching manifest
- **THEN** the RFC 6902 operations are applied to the matched manifest

#### Scenario: Both variants set is rejected at runtime

- **WHEN** `patch` has both `strategicMerge` and `jsonPatch` set
- **THEN** `Apply` returns an error stating exactly one of strategicMerge or jsonPatch must be set

#### Scenario: Neither variant set is rejected at runtime

- **WHEN** `patch` has neither `strategicMerge` nor `jsonPatch` set
- **THEN** `Apply` returns an error stating exactly one of strategicMerge or jsonPatch must be set

#### Scenario: Zero matches is a clean no-op

- **WHEN** the `patch` target selector matches no manifest in the stream
- **THEN** `Apply` returns the input manifests unchanged
- **AND** returns no error
- **AND** the returned manifests are byte-identical to the input

#### Scenario: Single-render-scoped patch no-ops on the non-matching render

- **WHEN** a `patch` targets a kind that exists in only one render (e.g. a shoot-only `ValidatingWebhookConfiguration`) and is applied to both the seed and shoot renders
- **THEN** the shoot render's matching object is patched
- **AND** the seed render is returned byte-identical with no error
- **AND** the reconcile is not stopped
