# Capability: manifest-transformation

## Purpose

Defines the typed Go transformation pipeline applied to each render's manifest stream in the dual-deployment-operator. The `internal/transform` package provides a `Transformation` interface and three concrete implementations (`patch`, `rewriteWebhookURL`, `filterKinds`) that are composed and applied independently to the seed render and the shoot render.

## Requirements

### Requirement: Transformation interface

The `internal/transform` package SHALL define a single `Transformation` interface with exactly two methods: `Type() string` (returning a stable identifier for the transformation kind) and `Apply(manifests []manifest.Manifest) ([]manifest.Manifest, error)`. `Apply` MUST operate on a single render's manifest stream and MUST return the transformed stream. There SHALL be no cross-stream or cross-render interface (no `CrossStreamTransformation`, no `ApplyCrossStream`), and no scope-splitting `Group()` — every transformation is per-render.

#### Scenario: Interface exposes Type and Apply

- **WHEN** any concrete transformation is used through the `Transformation` interface
- **THEN** `Type()` returns a stable non-empty identifier
- **AND** `Apply` accepts a `[]manifest.Manifest` and returns a `[]manifest.Manifest` and an error

#### Scenario: No cross-stream interface exists

- **WHEN** the package API is inspected
- **THEN** it exposes only the single per-render `Transformation` interface
- **AND** exposes no `CrossStreamTransformation` type, no `ApplyCrossStream` method, and no `Group()` scope-splitter

---

### Requirement: Build parses the CR transformation list preserving order

The package SHALL provide `Build(specs []v1alpha1.Transformation) ([]Transformation, error)` that walks the CR's discriminated-union entries and returns an ordered `[]Transformation`, preserving declaration order. For each entry, `Build` MUST select the transformation by the single set discriminator field (`patch`, `rewriteWebhookURL`, or `filterKinds`). An entry with no field set MUST cause `Build` to return an error. `Build` MUST NOT reorder, deduplicate, or merge entries.

#### Scenario: Declaration order preserved

- **WHEN** `Build` receives a list of entries in a given order
- **THEN** the returned `[]Transformation` is in the same order as the input entries

#### Scenario: Discriminator selects the transformation type

- **WHEN** `Build` receives an entry with exactly `patch` set
- **THEN** the corresponding element's `Type()` identifies it as a patch transformation
- **AND** likewise `rewriteWebhookURL` and `filterKinds` entries map to their respective transformations

#### Scenario: Empty entry is rejected

- **WHEN** `Build` receives an entry with none of `patch`, `rewriteWebhookURL`, or `filterKinds` set
- **THEN** it returns an error indicating no transformation type was set in the entry

---

### Requirement: Shared Match selector

The package SHALL provide a shared `Match(m manifest.Manifest, sel v1alpha1.Selector) bool` used by `patch` (and available to `filterKinds` for its `source` restriction). `Match` MUST return true only when every set selector field matches: `Kind` matches the manifest's kind exactly when set; `Name` matches the manifest's name by glob when set (a `*` pattern matching any name); `Origin` matches the manifest's `Origin` exactly when set. An empty selector field MUST NOT constrain the match. A `Selector` with no fields set MUST match every manifest.

#### Scenario: Kind matches exactly

- **WHEN** a selector sets `Kind: "Deployment"` and a manifest's kind is `Deployment`
- **THEN** `Match` returns true
- **AND** a manifest whose kind is `Service` does not match

#### Scenario: Name matches by glob

- **WHEN** a selector sets `Name: "metal-*"` and a manifest is named `metal-operator-controller-manager`
- **THEN** `Match` returns true
- **AND** a manifest named `other-controller` does not match

#### Scenario: Origin restricts by manifest origin

- **WHEN** a selector sets `Origin: "upstream"`
- **THEN** only manifests carrying `OriginUpstream` match
- **AND** manifests carrying `OriginAdditions` do not match

#### Scenario: Empty selector matches everything

- **WHEN** a selector has no `Kind`, `Name`, or `Origin` set
- **THEN** `Match` returns true for every manifest

---

### Requirement: patch transformation

The `patch` transformation SHALL apply a Kubernetes-native patch to every manifest matching its target `Selector`. Exactly one of `strategicMerge` (a JSON object) or `jsonPatch` (an array of RFC 6902 ops) MUST be set; the transformation MUST re-validate this exactly-one invariant at runtime and return an error if neither or both are set. A `strategicMerge` patch MUST be applied via `k8s.io/apimachinery/pkg/util/strategicpatch`; a `jsonPatch` MUST be applied via `github.com/evanphx/json-patch` following RFC 6902 semantics. If the selector matches zero manifests, `patch` MUST return an error (fail-loud: a mismatched selector is a misconfiguration). Patching MUST be idempotent for byte-identical inputs.

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

#### Scenario: Zero matches fails loud

- **WHEN** the `patch` target selector matches no manifest in the stream
- **THEN** `Apply` returns an error indicating no matching resource was found

---

### Requirement: patch stamps the injector target label

The `patch` transformation SHALL be usable to add the webhook-injector's `--target-label` to WebhookConfigurations and conversion-webhook CustomResourceDefinitions by supplying a `strategicMerge` that sets `metadata.labels`. There SHALL be no dedicated injector transformation type; injector integration is expressed entirely as a `patch`. Because it only adds a label, this usage is order-insensitive relative to other transformations.

#### Scenario: Label stamped on a webhook object via patch

- **WHEN** a `patch` targets `ValidatingWebhookConfiguration` with a `strategicMerge` setting `metadata.labels["dual-deployment-operator.cc.sap/webhook-injector"]` to an operator name
- **THEN** the matched WebhookConfiguration is returned carrying that label
- **AND** its other fields are preserved

---

### Requirement: rewriteWebhookURL transformation

The `rewriteWebhookURL` transformation SHALL rewrite service-based webhook `clientConfig` to URL-based `clientConfig` on three kinds: `ValidatingWebhookConfiguration` and `MutatingWebhookConfiguration` (each `.webhooks[].clientConfig`), and `CustomResourceDefinition` (the `.spec.conversion.webhook.clientConfig`, only when `.spec.conversion.strategy == "Webhook"`). For each `clientConfig` whose `.service` is set, the transformation MUST replace it with `.url = urlPrefix + service.path` (using `service.path`, or `""` if absent). An existing `.url` MUST be left unchanged (idempotent). `.caBundle` MUST be preserved (the injector owns it). A manifest of any other kind, a non-webhook CRD, or a `clientConfig` already using `.url` MUST be a no-op. If no manifest matches, the transformation MUST no-op (not error).

#### Scenario: WebhookConfiguration service rewritten to URL

- **WHEN** `rewriteWebhookURL` with `urlPrefix` set processes a `ValidatingWebhookConfiguration` whose webhook `clientConfig.service` is set
- **THEN** that webhook's `clientConfig.url` becomes `urlPrefix + service.path`
- **AND** the webhook's `clientConfig.service` is removed
- **AND** the webhook's `clientConfig.caBundle` is preserved

#### Scenario: Conversion-webhook CRD service rewritten to URL

- **WHEN** `rewriteWebhookURL` processes a `CustomResourceDefinition` with `.spec.conversion.strategy == "Webhook"` and `.spec.conversion.webhook.clientConfig.service` set
- **THEN** `.spec.conversion.webhook.clientConfig.url` becomes `urlPrefix + service.path`
- **AND** its `caBundle` is preserved

#### Scenario: Non-webhook CRD untouched

- **WHEN** `rewriteWebhookURL` processes a `CustomResourceDefinition` without a `Webhook` conversion strategy
- **THEN** the CRD is returned unchanged

#### Scenario: Existing URL left alone

- **WHEN** a `clientConfig` already uses `.url` (no `.service`)
- **THEN** the manifest is returned unchanged

#### Scenario: Zero matches no-ops

- **WHEN** the stream contains no WebhookConfiguration and no conversion-webhook CRD
- **THEN** `Apply` returns the stream unchanged with no error

---

### Requirement: filterKinds transformation

The `filterKinds` transformation SHALL drop manifests whose kind is in its `kinds` list from the stream. An optional `source` field MUST restrict the filter to manifests of that origin (`upstream` or `additions`); when `source` is omitted, all manifests of the listed kinds MUST be dropped regardless of origin. Manifests not matching the filter MUST be returned unchanged and in their original relative order. If no manifest matches, the transformation MUST no-op (not error).

#### Scenario: Listed kinds dropped

- **WHEN** `filterKinds` with `kinds: [Service]` processes a stream containing a `Service` and a `Deployment`
- **THEN** the `Service` is removed
- **AND** the `Deployment` is returned unchanged

#### Scenario: Source restricts by origin

- **WHEN** `filterKinds` with `kinds: [ConfigMap]` and `source: upstream` processes an upstream `ConfigMap` and an additions `ConfigMap`
- **THEN** only the upstream `ConfigMap` is dropped
- **AND** the additions `ConfigMap` is retained

#### Scenario: Relative order preserved

- **WHEN** `filterKinds` removes some manifests from a stream
- **THEN** the surviving manifests keep their original relative order

#### Scenario: Zero matches no-ops

- **WHEN** no manifest in the stream has a kind in the `kinds` list
- **THEN** `Apply` returns the stream unchanged with no error

---

### Requirement: Transformations do not mutate input in place

Every transformation's `Apply` SHALL return fresh `[]manifest.Manifest` and MUST NOT mutate the caller's input slice or the underlying `unstructured.Unstructured` objects. The reconciler threads the same ordered `[]Transformation` through the seed render and the shoot render independently, so mutating shared state would corrupt the second render.

#### Scenario: Input slice and objects unchanged after Apply

- **WHEN** any transformation's `Apply` is called on a manifest stream
- **THEN** the caller's input slice is unchanged after the call
- **AND** the underlying `Unstructured` objects referenced by the input are unchanged
