## Design Summary

Scaffold the `dual-deployment-operator` kubebuilder project and land the full `v1alpha1` `DualDeploymentOperator` CRD types (Spec + Status) with CRD-level CEL validation enforcing the discriminated-union constraints, plus a no-op reconciler that proves the wiring. This is the "foundation" change: it produces a repo that builds, generates CRDs (with embedded CEL rules), and reconciles CRs without doing any real work yet. Two-render rendering, transformations (per-render + cross-stream), and delivery are out of scope and land in subsequent changes.

The CRD types reflect the current design (`docs/design.md` revision 6): two-render pattern with `hostValues`/`remoteValues` on `HelmSource` and `hostPath`/`remotePath` on `KustomizeSource`; 4 transformation types across 2 scopes (per-render: `patch`, `rewriteWebhookURL`, `filterKinds`; cross-stream: `packageWebhookConfigsForInjector`); `patch` has typed variants (`strategicMerge` XOR `jsonPatch`).

## Alternatives Considered

### Option A: Single `dualdeploymentoperator_types.go` — chosen
- **Approach**: Put all Spec/Status types (Source, HelmSource, KustomizeSource, Transformation, PatchSpec, JSONPatchOp, RewriteWebhookURLSpec, FilterKindsSpec, PackageWebhookConfigsForInjectorSpec, Selector, DeletionPolicy, HealthState, ResourceStatus, etc.) in the single kubebuilder-scaffolded types file under `api/v1alpha1/`.
- **Pros**: Kubebuilder's `zz_generated.deepcopy.go` and CRD manifest generation work without any custom configuration; matches the layout in `docs/implementation.md` exactly; least surprise for readers familiar with kubebuilder; ~350 lines (larger than the earlier ~250-line estimate because of `PatchSpec` + `JSONPatchOp` in the union) is still comfortable to read.
- **Cons**: One file per API version; slightly larger than under the r5 menu.
- **Chosen because**: Matches implementation.md, keeps codegen paths standard, avoids speculative decomposition. The extra ~100 lines from `PatchSpec` + `JSONPatchOp` don't cross any usability threshold.

### Option B: Types split by concern
- **Approach**: `dualdeploymentoperator_types.go` (top-level Spec/Status), `source_types.go`, `transformation_types.go`, `patch_types.go`, `status_types.go` under `api/v1alpha1/`.
- **Pros**: Smaller files, cleaner git blame per concern; the `patch_types.go` split could isolate the DSL types.
- **Cons**: Non-standard layout for kubebuilder users; deepcopy tags must be repeated per file; more import juggling; no material benefit at this file size.
- **Why not chosen**: Splitting is speculative decomposition; readability of ~350 lines is not a problem worth solving.

### Option C: Types split into subpackages (`api/v1alpha1/source/`, `api/v1alpha1/transform/`)
- **Approach**: Sub-packages under the API version directory.
- **Pros**: Deepest separation of concerns.
- **Cons**: Kubebuilder does not scaffold this way; requires manual deepcopy setup and CRD generation config; adds cross-package types that complicate the API.
- **Why not chosen**: Overkill for the size, and fights the tooling.

## Agreed Approach

Option A. Scaffold with kubebuilder using standard defaults, keep types in one file, encode discriminator constraints as CEL validation rules on the CRD, and include a minimal no-op reconciler that reads the CR and requeues.

Scope boundary is deliberately narrow:
- Types + CEL validation + wiring only. No two-render rendering, no transformation execution, no shoot client construction, no finalizer.
- Testing is unit + envtest covering the CEL rule rejection paths and reconciler wiring — no kind cluster in CI for this change.
- The kubebuilder `webhook` scaffold IS created (empty `CustomValidator` methods returning `nil`) but NOT wired to a `ValidatingWebhookConfiguration` — matches `docs/implementation.md` line 1119/1136 explicit "v1: leave CustomValidator no-op; CRD schema + CEL handle all v1 admission validation."

## Key Decisions

### Repository and scaffold

- **Go module path**: `github.com/SAP-cloud-infrastructure/dual-deployment-operator`. Same GitHub org as the companion `webhook-injector` (`SAP-cloud-infrastructure/webhook-injector`), colocating the two components that coexist per `docs/design.md` §3.8.
- **API group / domain**: `dual-deployment-operator.cc.sap`, version `v1alpha1`, kind `DualDeploymentOperator`. Per `docs/design.md` §3.2 and README.md.
- **Kubebuilder scaffold commands**:
  ```bash
  kubebuilder init --domain cc.sap --repo github.com/SAP-cloud-infrastructure/dual-deployment-operator
  kubebuilder create api --group dual-deployment-operator --version v1alpha1 --kind DualDeploymentOperator --resource --controller
  kubebuilder create webhook --group dual-deployment-operator --version v1alpha1 --kind DualDeploymentOperator --programmatic-validation
  ```
  Standard defaults; the `webhook` scaffold is created so v2's content-level patch validation can wire into it without re-scaffolding, but its `ValidateCreate`/`ValidateUpdate` methods return `nil` in v1 and no `ValidatingWebhookConfiguration` is deployed.
- **Webhook Service + cert-management config scaffolded now, not deployed in v1.** Keep every file kubebuilder generates for the webhook (Service manifest in `config/webhook/`, `ValidatingWebhookConfiguration` manifest, cert-manager Issuer/Certificate in `config/certmanager/`, kustomize overlays wiring them together). Do NOT include these in the default kustomize overlay that Phase 8's deployment chart will consume — leave them wired only under a separate `config/default-with-webhook/` (or the kubebuilder-generated equivalent) overlay that's off by default. Rationale: v2's "flip webhook on" change becomes a pure kustomize-overlay flip with no re-scaffolding, no re-generating cert manifests, no re-wiring `caBundle` injection annotations. The cost in v1 is a few unused YAML files sitting in `config/`; the v2 saving is real work avoided.

  **How to turn it on later (v2)** — the checklist a future change will follow to enable the webhook:

  1. **Populate the CustomValidator methods** in `internal/webhook/v1alpha1/dualdeploymentoperator_webhook.go`. Replace the v1 `return nil` bodies of `ValidateCreate` / `ValidateUpdate` / `ValidateDelete` with the content-level patch validation logic (parse `patch.strategicMerge` / `patch.jsonPatch` against the target kind's OpenAPI schema fetched from the API server at startup, per `docs/design.md` §9.9).
  2. **Uncomment kubebuilder markers in `config/default/kustomization.yaml`**. Kubebuilder scaffolds this file with three commented-out sections that need to be enabled together:
     ```yaml
     # [WEBHOOK] To enable webhook, uncomment all the sections with [WEBHOOK] prefix including the one in crd/kustomization.yaml
     # [CERTMANAGER] To enable cert-manager, uncomment all sections with 'CERTMANAGER'. 'WEBHOOK' components are required.
     # [PROMETHEUS] To enable prometheus monitor, uncomment all sections with 'PROMETHEUS'.
     ```
     Uncomment `[WEBHOOK]` and `[CERTMANAGER]` sections. This wires in `../webhook`, `../certmanager`, `manager_webhook_patch.yaml`, and `webhookcainjection_patch.yaml`.
  3. **Uncomment `[WEBHOOK]` in `config/crd/kustomization.yaml`**. This adds `patches/webhook_in_dualdeploymentoperators.yaml` and `patches/cainjection_in_dualdeploymentoperators.yaml` to the CRD, wiring conversion webhook + caBundle injection annotation.
  4. **Verify cert-manager is present** in every target seed. Kubebuilder's default cert manifest uses `cert-manager.io/v1`. If a target seed lacks cert-manager, either install it or replace `config/certmanager/` with an alternative (self-signed operator-managed cert, or an existing cluster cert-issuer). No code change to the operator itself, only the manifest generator.
  5. **Update the deployment chart** (Phase 8 in `sapcc/helm-charts` under `system/dual-deployment-operator/`) to render from the webhook-enabled overlay. If Phase 8 uses `kustomize build config/default | helm template` (or equivalent), the diff is one path change.
  6. **Decide `failurePolicy`** on the `ValidatingWebhookConfiguration`. Kubebuilder default is `Fail` (writes rejected if webhook Pod is down). For a per-shoot operator with a single-Pod webhook this couples CR writes to Pod availability; consider `failurePolicy: Ignore` (writes go through if webhook is unreachable, at the cost of "invalid patches slip through during outages") or leave `Fail` if strict admission-time validation is preferred. Recorded as a decision the v2 brainstorm resolves, not a v1 concern.
  7. **Bootstrap ordering**: cert-manager must issue the cert Secret before the operator Pod is Ready and before the `ValidatingWebhookConfiguration` becomes effective. Kubebuilder's default overlays handle this via caBundle injection annotations (config becomes effective only when cert exists). Verify during the first QA-seed rollout; add `failurePolicy: Ignore` during initial bootstrap if a chicken-and-egg is observed.
  8. **envtest updates**: add cert-manager to the envtest fixture (or use the built-in test helper that injects a self-signed cert into the operator's webhook cert path) so envtest can exercise the wired webhook. Kubebuilder scaffolds a `suite_test.go` block for this — it's commented out in v1 and enabled in v2.

  Steps 2-3 are purely kustomize edits — no Go code, no re-scaffolding. Steps 1 and 8 are the actual v2 work. Steps 4-7 are ops/deploy concerns, not repo changes.
- **All Spec/Status types live in `api/v1alpha1/dualdeploymentoperator_types.go`** (Option A). No split into concern-based or subpackage layouts.

### CRD types (per `docs/implementation.md` §Phase 1)

- **`DualDeploymentOperatorSpec`**: `Source`, `RemoteKubeconfig`, `Transformations []Transformation`, `DeletionPolicy`.
- **`Source` discriminated union**: `Helm *HelmSource` XOR `Kustomize *KustomizeSource`. Discriminator enforced by CEL.
- **`HelmSource`** — 5 fields, all pointer/optional except identifiers:
  - `Repo string` (required)
  - `Name string` (required)
  - `Version string` (required; semver-strict validation deferred to a future webhook per `docs/implementation.md` line 271; v1 only enforces `MinLength=1`)
  - `Values *apiextensionsv1.JSON` — common per-cluster values, applied to both host and remote renders
  - `HostValues *apiextensionsv1.JSON` — host-render-only overrides (merged on top of `Values`)
  - `RemoteValues *apiextensionsv1.JSON` — remote-render-only overrides (merged on top of `Values`)
- **`KustomizeSource`** — 3 fields:
  - `URL string` (required; must include `?ref=<value>` per CEL rule)
  - `HostPath string` (**required**; `+kubebuilder:validation:MinLength=1`; no default)
  - `RemotePath string` (**required**; `+kubebuilder:validation:MinLength=1`; no default)

  **Deviation from docs**: `docs/design.md` §3.3 and `docs/implementation.md` §Phase 1 describe `HostPath` and `RemotePath` as optional with defaults `"host"` and `"remote"`. This brainstorm locks them as required, no defaults, for four reasons: (1) only one v1 CR uses `KustomizeSource` at all (ipam-capi), so verbosity reduction is negligible; (2) the subpath is a coordination point between operator config and kustomize source layout — making it explicit in the CR forces both sides to name it in one place; (3) silent defaults produce confusing failure modes on source-layout renames (operator renders from a non-existent default path, krusty fails deep in render, not at admission); (4) symmetry with `HelmSource.HostValues`/`RemoteValues`, which have no defaults. The design artifact should update the docs to match, or record the deviation formally.
- **`Transformation` discriminated union** — exactly ONE of 4 fields set (per CEL rule):
  - Per-render (3): `Patch *PatchSpec`, `RewriteWebhookURL *RewriteWebhookURLSpec`, `FilterKinds *FilterKindsSpec`
  - Cross-stream (1): `PackageWebhookConfigsForInjector *PackageWebhookConfigsForInjectorSpec`
- **`PatchSpec`** — the v1 DSL entry:
  - `Target Selector` (required)
  - `StrategicMerge *apiextensionsv1.JSON` XOR `JSONPatch []JSONPatchOp` — enforced by CEL: `has(self.strategicMerge) != has(self.jsonPatch)`
  - `StrategicMerge` marked `+kubebuilder:validation:Schemaless` + `+kubebuilder:pruning:PreserveUnknownFields` so arbitrary strategic-merge content is accepted (structural type-validation only)
- **`JSONPatchOp`** — one RFC 6902 operation:
  - `Op string` with `+kubebuilder:validation:Enum=add;remove;replace;move;copy;test`
  - `Path string` with `+kubebuilder:validation:MinLength=1`
  - `From string` (optional; used by move/copy)
  - `Value *apiextensionsv1.JSON` (optional; `Schemaless` + `PreserveUnknownFields` for the value payload)
- **`RewriteWebhookURLSpec`**: `URLPrefix string` (required), `TargetKinds []string` (optional, defaults to Validating+Mutating WebhookConfiguration kinds)
- **`FilterKindsSpec`**: `Kinds []string` (required), `Source string` (optional, `"upstream"` or `"additions"` or empty for both)
- **`PackageWebhookConfigsForInjectorSpec`**: `ConfigMapName string` (required), `DataKey string` (optional, default `"webhooks.yaml"`)
- **`Selector`**: `Kind`, `Name` (supports glob `*`), `Origin` (`"upstream"` | `"additions"`)
- **`DeletionPolicy`**: `CRDs string` with `+kubebuilder:validation:Enum=Retain;Delete`, default `"Retain"`.

### Admission-time validation strategy

- **CEL rules are the v1 validation mechanism.** Docs (both `docs/implementation.md` §Phase 1 and `docs/context.md`) explicitly call CEL cleaner than a validating webhook for the discriminator work this change needs. Requires Kubernetes 1.29+.
- **CEL rules to encode on the CRD** (per `docs/implementation.md` line 254-272):
  - On `Source`: `has(self.helm) != has(self.kustomize)` — "exactly one of source.helm or source.kustomize must be set"
  - On `PatchSpec`: `has(self.strategicMerge) != has(self.jsonPatch)` — "exactly one of patch.strategicMerge or patch.jsonPatch must be set"
  - On `Transformation`: `(has(self.patch)?1:0) + (has(self.rewriteWebhookURL)?1:0) + (has(self.filterKinds)?1:0) + (has(self.packageWebhookConfigsForInjector)?1:0) == 1` — "exactly one transformation type must be set per entry"
  - On `KustomizeSource.URL`: `self.matches('.*[?&]ref=.+')` — "kustomize url must include a pinned ref= parameter"
- **Semver-strict Helm version validation** cannot be expressed cleanly in CEL. `HelmSource.Version` in v1 is only `MinLength=1`; strict semver check deferred to future webhook (per docs).
- **Validating webhook scaffold created but not wired**. `docs/implementation.md` line 1119: "Kubebuilder scaffold created but not wired in v1. Structural validation is provided by CRD schema + CEL rules." Line 1136: "v1: leave the CustomValidator methods as no-op (return `nil` immediately). Do not deploy the webhook ValidatingWebhookConfiguration. CRD schema + CEL handle all v1 admission validation." **This flips the earlier brainstorm decision** — earlier draft said "validating webhook is the mechanism"; docs now say CEL is v1, webhook is v2 for content-level patch validation.

### Kubernetes version dependency

- **CEL rules require Kubernetes 1.29+.** All target seeds are 1.29+ per team roadmap (verify at implementation time). If a target cluster is below 1.29, CEL rules are silently ignored and invalid CRs go through — regression to runtime detection. This is a deployment prerequisite, not a code decision.

### No-op reconciler

- **Behavior**: `Get` the CR; if not found, return nil (already handled by kubebuilder scaffold's `client.IgnoreNotFound`); log one info line (`"reconciling"`, `cr.Name`); return `ctrl.Result{RequeueAfter: 10*time.Minute}, nil`.
- **`RequeueAfter` interval**: **10 minutes**. Matches `docs/design.md` §3.6.2's drift-correction default so v1 lands with the same cadence Phase 6 will use for real work. Rationale: keeps a single well-known interval across the operator's lifetime — no "temporary" value that later gets migrated. The no-op tick does nothing useful per fire, but 10 minutes is low enough overhead (one work-queue enqueue per shoot-cp namespace every 10 minutes) that the waste is negligible, and the interval is verified in envtest from day one.
- No finalizer, no status writes. Purpose is to prove wiring — subsequent changes add real behavior.

### Included but not populated

- **Status types included in full now** even though the reconciler doesn't populate them yet. Reason: adding status subresource fields later is a CRD-breaking migration risk; landing the shape now costs nothing. Includes `HostResources`, `RemoteResources`, `Conditions`, `LastReconcile`, `ResourceStatus`, `HealthState` enum (`Healthy`, `Progressing`, `Degraded`, `Unknown`).
- **Dependencies pulled in early** (`helm.sh/helm/v3`, `sigs.k8s.io/kustomize/api`) added to `go.mod` even though not imported yet, so `go.sum` settles once and future changes don't drag them in. Blank imports in an `_ = ...` unused-import block are avoided (compiler rejects them for stdlib-forbidden reasons); instead, deps sit only in `go.mod`'s `require` block until Phase 2 code imports them. If Go tooling errors on unused `require`, use `go mod tidy` conservatively and accept re-pulling in Phase 2.

### Testing

- **Unit tests**: type marshal/unmarshal round-trips for each Spec/Status struct; enum validation on `DeletionPolicy.CRDs`, `HealthState`, `JSONPatchOp.Op`.
- **envtest** (via kubebuilder default `test` target) exercises the **CEL rules**:
  - Reject: neither `Source.Helm` nor `Source.Kustomize` set
  - Reject: both `Source.Helm` and `Source.Kustomize` set
  - Reject: `Transformation` with zero union fields
  - Reject: `Transformation` with two or more union fields
  - Reject: `PatchSpec` with neither `strategicMerge` nor `jsonPatch`
  - Reject: `PatchSpec` with both `strategicMerge` and `jsonPatch`
  - Reject: `KustomizeSource.URL` missing `?ref=`
  - Reject: `KustomizeSource` missing `HostPath` (both `HostPath` and `RemotePath` are required, no defaults)
  - Reject: `KustomizeSource` missing `RemotePath`
  - Reject: `JSONPatchOp.Op` outside the RFC 6902 enum
  - Accept: valid Helm CR (with `Values`+`HostValues`+`RemoteValues`)
  - Accept: valid kustomize CR (with `URL` + explicit `HostPath` + explicit `RemotePath`)
  - Accept: CR with each of the 4 transformation types
- **envtest reconciler wiring**: Create CR → reconciler logs one line → CR unmodified → next requeue scheduled after 10 minutes (verified via returned `ctrl.Result.RequeueAfter`, not real sleep).
- **No kind cluster** smoke test in this change.

## Open Questions

- [ ] Concrete versions of `helm.sh/helm/v3`, `sigs.k8s.io/kustomize/api`, `sigs.k8s.io/controller-runtime`, `k8s.io/*` at implementation time — verify current stable when Phase 0 lands. `docs/implementation.md` §"Concrete v1 dependencies" gives an illustrative pin set; verify against latest before locking. — owner: implementer
- [ ] envtest binary version and Kubernetes API server version — must be **1.29+ to support CEL rules on CRDs**. Kubebuilder's default `ENVTEST_K8S_VERSION` typically follows recent stable; confirm the value used in `bin/setup-envtest` supports CEL and matches what the target seeds run. — owner: implementer
