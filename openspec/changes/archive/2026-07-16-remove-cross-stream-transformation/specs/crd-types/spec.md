# Spec Delta: crd-types (remove-cross-stream-transformation)

This change removes the r5/r6 cross-stream transformation `packageWebhookConfigsForInjector` from the CRD types, following design revision 7 (`docs/design.md` §2.2.4). The webhook-injector's target patch mode ([webhook-injector#14](https://github.com/SAP-cloud-infrastructure/webhook-injector/pull/14)) lets the operator apply WebhookConfigurations directly to the shoot with `caBundle` left unset, so the operator no longer packages them into a host-side ConfigMap. The transformation menu collapses to three per-render types in a single scope.

It also updates `rewriteWebhookURL` in two ways: (a) it removes the unused `RewriteWebhookURLSpec.TargetKinds` field — v1 targets its webhook kinds unconditionally, so no per-kind narrowing field is kept; and (b) it extends the transformation to rewrite the `clientConfig` of conversion-webhook CRDs (`.spec.conversion.webhook.clientConfig`) in addition to Validating/Mutating WebhookConfigurations, closing the CRD conversion-webhook Service→URL rewrite gap (`docs/design.md` §3.4.2, §9.2). Both are changes to an existing, still-live type — `TargetKinds` is dropped and no new field is added.

The `PackageWebhookConfigsForInjectorSpec` type was scaffolded by the archived scaffold change but was never used by any implementation (Phase 3 transformations were never built) and no `DualDeploymentOperator` CR in the fleet sets it, so its removal requires no data migration.

Implementation note: the `TargetKinds` field is already absent from the Go type (`api/v1alpha1/dualdeploymentoperator_types.go`), the generated deepcopy, and the generated CRD YAML — only the tracked live spec still described it. This change reconciles the spec to the code (and adds the CRD conversion-webhook extension, which IS new implementation work in Phase 3). `PackageWebhookConfigsForInjectorSpec`, by contrast, still exists in the Go type + deepcopy + CEL rule + tests and must be deleted during apply.

## MODIFIED Requirements

### Requirement: Transformation discriminated union with 4 types across 2 scopes

`Transformation` MUST declare exactly three pointer fields, forming a discriminated union where exactly one is set per entry. All three are per-render transformations (a single scope):

```go
type Transformation struct {
    // Per-render (3)
    Patch             *PatchSpec             `json:"patch,omitempty"`
    RewriteWebhookURL *RewriteWebhookURLSpec `json:"rewriteWebhookURL,omitempty"`
    FilterKinds       *FilterKindsSpec       `json:"filterKinds,omitempty"`
}
```

There is no cross-stream scope and no `PackageWebhookConfigsForInjector` field. The "exactly one" invariant is enforced by CEL (see `cel-admission-validation` spec).

#### Scenario: Round-trip for each variant

- **WHEN** a Go program constructs a `Transformation` with each of the three fields (one at a time)
- **THEN** each `json.Marshal`/`json.Unmarshal` cycle round-trips without loss

#### Scenario: No cross-stream field exists

- **WHEN** the `Transformation` struct is inspected via reflection
- **THEN** it declares exactly three fields (`Patch`, `RewriteWebhookURL`, `FilterKinds`)
- **AND** no `PackageWebhookConfigsForInjector` field is present

### Requirement: RewriteWebhookURLSpec fields

`RewriteWebhookURLSpec` MUST declare exactly one field:

```go
type RewriteWebhookURLSpec struct {
    URLPrefix string `json:"urlPrefix"`
}
```

The previously-declared optional `TargetKinds []string` field is REMOVED — v1 does not support per-kind narrowing. `URLPrefix` is `MinLength=1`. The transformation applies to three kinds — `ValidatingWebhookConfiguration` and `MutatingWebhookConfiguration` (rewriting each `.webhooks[].clientConfig`), and `CustomResourceDefinition` (rewriting `.spec.conversion.webhook.clientConfig`, only when `.spec.conversion.strategy == "Webhook"`). Targeting is unconditional across all three kinds.

#### Scenario: Minimal spec is accepted

- **WHEN** a CR is applied with `spec.transformations[0].rewriteWebhookURL.urlPrefix: "https://example:443"`
- **THEN** the API server accepts the request

## REMOVED Requirements

### Requirement: PackageWebhookConfigsForInjectorSpec fields

**Reason**: The cross-stream transformation `packageWebhookConfigsForInjector` is removed in design revision 7. Under r7 the operator applies WebhookConfigurations directly to the shoot (in the remote render) with the injector's `--target-crd-label` stamped and the `caBundle` field left unset; the webhook-injector's target patch mode keeps `caBundle` in sync in place. Packaging WebhookConfigurations into a host-side ConfigMap is therefore obsolete, and the `PackageWebhookConfigsForInjectorSpec` type is deleted along with its `Transformation` union field, its DeepCopy methods, and its CEL union term.

**Migration**: The type was scaffolded but never used by any reconciler or transformation (Phase 3 was never implemented), and no CR sets `spec.transformations[].packageWebhookConfigsForInjector`. Removal is a breaking CRD schema change with no data migration. After removal, run `make manifests generate` to regenerate the CRD YAML, RBAC, and `zz_generated.deepcopy.go`.
