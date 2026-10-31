<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Spec Delta: cel-admission-validation

## MODIFIED Requirements

### Requirement: Kustomize URL ref-pinning enforced by CEL

The CRD schema for `spec.source.kustomize.url` MUST carry a CEL rule that rejects URLs without a `ref=` query parameter. The rule MUST match a `?ref=<value>` or `&ref=<value>` fragment with a non-empty value:

```
self.matches('.*[?&]ref=.+')
```

with the message `"kustomize url must include a pinned ref= query parameter"`.

The root-URL CEL rule enforces that the **root** is pinned. Remote bases referenced transitively inside the fetched tree cannot be validated by CEL (their content is not visible at admission); their pinning is enforced at render time by the kustomize-remote-bases prefetch step, which resolves each remote base to an immutable commit SHA and fails closed on a missing or unresolvable `?ref=`. The admission rule therefore remains the root-level gate, and the resolver remains the transitive-base gate.

#### Scenario: URL without ref parameter is rejected

- **WHEN** a CR is applied with `spec.source.kustomize.url: "https://github.com/sapcc/helm-charts//system/kustomize/ipam-capi-remote/"`
- **THEN** the API server rejects the request

#### Scenario: URL with empty ref value is rejected

- **WHEN** a CR is applied with `spec.source.kustomize.url: "https://github.com/sapcc/helm-charts//system/kustomize/ipam-capi-remote/?ref="`
- **THEN** the API server rejects the request

#### Scenario: URL with ref tag is accepted

- **WHEN** a CR is applied with `spec.source.kustomize.url: "https://github.com/sapcc/helm-charts//system/kustomize/ipam-capi-remote/?ref=v1.2.31"`
- **THEN** the API server accepts the request

#### Scenario: URL with ref commit SHA is accepted

- **WHEN** a CR is applied with `spec.source.kustomize.url: "https://github.com/sapcc/helm-charts//system/kustomize/ipam-capi-remote/?ref=035c7c229d1234567890abcdef1234567890abcd"`
- **THEN** the API server accepts the request

#### Scenario: an unpinned transitive remote base fails closed at render time

- **WHEN** an admitted root (with a pinned root `?ref=`) transitively references a remote base whose `?ref=` is missing or unresolvable
- **THEN** admission still accepts the CR (the root is pinned)
- **AND** the render fails closed at reconcile time with an error naming the offending remote base
