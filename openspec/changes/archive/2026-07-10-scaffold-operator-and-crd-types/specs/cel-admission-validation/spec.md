## ADDED Requirements

### Requirement: Source discriminator enforced by CEL

The CRD schema for `spec.source` MUST carry a CEL rule that rejects any CR where the number of set variants under `spec.source` is not exactly one. The rule MUST be:

```
has(self.helm) != has(self.kustomize)
```

with the message `"exactly one of source.helm or source.kustomize must be set"`.

#### Scenario: Neither variant set is rejected

- **WHEN** a CR is applied with `spec.source: {}`
- **THEN** the API server rejects the request with the configured message
- **AND** the rejection is emitted at admission time (before the CR reaches etcd)

#### Scenario: Both variants set is rejected

- **WHEN** a CR is applied with both `spec.source.helm` and `spec.source.kustomize` populated
- **THEN** the API server rejects the request with the configured message

#### Scenario: Exactly one variant is accepted

- **WHEN** a CR is applied with only `spec.source.helm` populated
- **THEN** the API server accepts the request

- **WHEN** a CR is applied with only `spec.source.kustomize` populated
- **THEN** the API server accepts the request

---

### Requirement: Transformation union enforced by CEL

The CRD schema for each entry of `spec.transformations[]` MUST carry a CEL rule that rejects any entry where the number of set union fields is not exactly one. The rule MUST count the four union fields explicitly:

```
(has(self.patch) ? 1 : 0) +
(has(self.rewriteWebhookURL) ? 1 : 0) +
(has(self.filterKinds) ? 1 : 0) +
(has(self.packageWebhookConfigsForInjector) ? 1 : 0) == 1
```

with the message `"exactly one transformation type must be set per entry"`.

#### Scenario: Empty transformation entry is rejected

- **WHEN** a CR is applied with `spec.transformations: [{}]`
- **THEN** the API server rejects the request

#### Scenario: Two union fields in one entry is rejected

- **WHEN** a CR is applied with `spec.transformations: [{patch: {...}, filterKinds: {...}}]`
- **THEN** the API server rejects the request

#### Scenario: Each of the four variants is accepted alone

- **WHEN** a CR is applied with `spec.transformations: [{patch: {target: {}, strategicMerge: {...}}}]`
- **THEN** the API server accepts the request

- **WHEN** a CR is applied with `spec.transformations: [{rewriteWebhookURL: {urlPrefix: "https://x:443"}}]`
- **THEN** the API server accepts the request

- **WHEN** a CR is applied with `spec.transformations: [{filterKinds: {kinds: ["Service"]}}]`
- **THEN** the API server accepts the request

- **WHEN** a CR is applied with `spec.transformations: [{packageWebhookConfigsForInjector: {configMapName: "webhooks"}}]`
- **THEN** the API server accepts the request

---

### Requirement: PatchSpec variant enforced by CEL

The CRD schema for each `PatchSpec` MUST carry a CEL rule that rejects any patch where the number of set variants (`strategicMerge`, `jsonPatch`) is not exactly one. The rule MUST be:

```
has(self.strategicMerge) != has(self.jsonPatch)
```

with the message `"exactly one of patch.strategicMerge or patch.jsonPatch must be set"`.

#### Scenario: Neither patch variant is rejected

- **WHEN** a CR is applied with `patch: {target: {}}` and neither `strategicMerge` nor `jsonPatch`
- **THEN** the API server rejects the request

#### Scenario: Both patch variants is rejected

- **WHEN** a CR is applied with `patch: {target: {}, strategicMerge: {...}, jsonPatch: [{...}]}`
- **THEN** the API server rejects the request

#### Scenario: strategicMerge alone is accepted

- **WHEN** a CR is applied with `patch: {target: {kind: Deployment}, strategicMerge: {metadata: {labels: {a: "b"}}}}`
- **THEN** the API server accepts the request

#### Scenario: jsonPatch alone is accepted

- **WHEN** a CR is applied with `patch: {target: {kind: Deployment}, jsonPatch: [{op: "add", path: "/metadata/labels/a", value: "b"}]}`
- **THEN** the API server accepts the request

---

### Requirement: Kustomize URL ref-pinning enforced by CEL

The CRD schema for `spec.source.kustomize.url` MUST carry a CEL rule that rejects URLs without a `ref=` query parameter. The rule MUST match a `?ref=<value>` or `&ref=<value>` fragment with a non-empty value:

```
self.matches('.*[?&]ref=.+')
```

with the message `"kustomize url must include a pinned ref= query parameter"`.

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

---

### Requirement: Validating admission webhook is not deployed

The v1 change MUST NOT deploy a `ValidatingWebhookConfiguration` for the `DualDeploymentOperator` kind. The `internal/webhook/v1alpha1/dualdeploymentoperator_webhook.go` `CustomValidator` methods (`ValidateCreate`, `ValidateUpdate`, `ValidateDelete`) MUST return `(nil, nil)` immediately. The kubebuilder-generated `config/webhook/`, `config/certmanager/`, and `[WEBHOOK]`/`[CERTMANAGER]` kustomize patch sections MUST remain in the repository but MUST NOT be enabled in the default overlay.

#### Scenario: Default overlay excludes webhook manifests

- **WHEN** `kustomize build config/default` is run
- **THEN** the output does NOT contain any `ValidatingWebhookConfiguration` resource
- **AND** the output does NOT contain a webhook `Service` resource
- **AND** the output does NOT contain any `cert-manager.io/v1` `Certificate` or `Issuer` resource

#### Scenario: Webhook scaffold files are present

- **WHEN** the repository is inspected after Phase 0 scaffold completes
- **THEN** `config/webhook/manifests.yaml` exists
- **AND** `config/webhook/service.yaml` exists
- **AND** `config/certmanager/certificate.yaml` exists
- **AND** `config/default/kustomization.yaml` contains commented `[WEBHOOK]` and `[CERTMANAGER]` sections that can be uncommented to enable the webhook in a future change

#### Scenario: CustomValidator methods return nil

- **WHEN** a CR is applied under the v1 default overlay
- **THEN** no webhook invocation is observed by the API server
- **AND** any subsequent programmatic invocation of `ValidateCreate` or `ValidateUpdate` returns `(nil, nil)`

---

### Requirement: Kubernetes 1.29+ is required for admission validation

The CRD MUST rely on Kubernetes 1.29+ features (CEL rules on CRD schemas). The operator's Makefile and `hack/` scripts MUST pin the envtest binary version such that CEL rules are exercised in CI.

#### Scenario: envtest version is pinned

- **WHEN** a maintainer runs `make envtest` or an equivalent CI target
- **THEN** the resolved envtest Kubernetes version is 1.29 or higher
- **AND** the pin is visible in the `Makefile` as an explicit `ENVTEST_K8S_VERSION` variable

#### Scenario: CI fails on unsupported envtest version

- **WHEN** a maintainer overrides `ENVTEST_K8S_VERSION` to `1.28.x` and runs `make test`
- **THEN** the envtest suite fails
- **AND** the failure is caused by the CEL rules being silently ignored (invalid CRs are accepted where they should be rejected)
