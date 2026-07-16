# Spec Delta: cel-admission-validation (remove-cross-stream-transformation)

This change updates the transformation-union CEL rule to count three transformation types instead of four, following the removal of the cross-stream `packageWebhookConfigsForInjector` transformation in design revision 7 (`docs/design.md` §2.2.4). See the `crd-types` delta in this change for the corresponding Go-type removal.

## MODIFIED Requirements

### Requirement: Transformation union enforced by CEL

The CRD schema for each entry of `spec.transformations[]` MUST carry a CEL rule that rejects any entry where the number of set union fields is not exactly one. The rule MUST count the three union fields explicitly:

```
(has(self.patch) ? 1 : 0) +
(has(self.rewriteWebhookURL) ? 1 : 0) +
(has(self.filterKinds) ? 1 : 0) == 1
```

with the message `"exactly one transformation type must be set per entry"`.

#### Scenario: Empty transformation entry is rejected

- **WHEN** a CR is applied with `spec.transformations: [{}]`
- **THEN** the API server rejects the request

#### Scenario: Two union fields in one entry is rejected

- **WHEN** a CR is applied with `spec.transformations: [{patch: {...}, filterKinds: {...}}]`
- **THEN** the API server rejects the request

#### Scenario: Each of the three variants is accepted alone

- **WHEN** a CR is applied with `spec.transformations: [{patch: {target: {}, strategicMerge: {...}}}]`
- **THEN** the API server accepts the request

- **WHEN** a CR is applied with `spec.transformations: [{rewriteWebhookURL: {urlPrefix: "https://x:443"}}]`
- **THEN** the API server accepts the request

- **WHEN** a CR is applied with `spec.transformations: [{filterKinds: {kinds: ["Service"]}}]`
- **THEN** the API server accepts the request

#### Scenario: Removed packageWebhookConfigsForInjector variant is rejected

- **WHEN** a CR is applied with `spec.transformations: [{packageWebhookConfigsForInjector: {configMapName: "webhooks"}}]`
- **THEN** the API server rejects the request because `packageWebhookConfigsForInjector` is no longer a known field of `Transformation` (pruned by the structural schema)
