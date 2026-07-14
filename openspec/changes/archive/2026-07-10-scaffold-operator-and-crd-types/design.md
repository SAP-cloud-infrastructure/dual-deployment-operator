## Context

The `dual-deployment-operator` repository is currently a scaffold-only Go module (`go.mod`, `Makefile`, `README.md`, `docs/`) with no operator code. The design of the operator is documented in `docs/design.md` (revision 6), `docs/context.md`, `docs/implementation.md`, and `docs/related-artifacts.md`. This change implements Phase 0 + Phase 1 of `docs/implementation.md`: kubebuilder scaffold plus the full `v1alpha1` `DualDeploymentOperator` CRD types with CEL-based admission validation and a no-op reconciler that proves the wiring.

The operator will eventually render a Helm chart or kustomize source per operator (twice per reconcile, once for host and once for remote), apply typed Go transformations, and dual-apply the two renders to their target clusters via server-side apply. **None of that logic is in this change.** This change lands only the API surface (types + CEL rules) and the controller-manager Deployment that will host that logic.

Constraints:
- Target Kubernetes versions are 1.29+ (required for CEL rules on CRDs).
- The operator will be deployed per-shoot in Gardener seed clusters; the deployment chart itself is Phase 8 scope, not in this change.
- All target seeds already run `cert-manager.io/v1`; verified as safe to depend on in v2 wiring.
- Go 1.22+; kubebuilder v3 scaffold; controller-runtime, `helm.sh/helm/v3`, and `sigs.k8s.io/kustomize/api` as declared dependencies (only controller-runtime imported in this change; the other two sit in `go.mod` for future use).

Stakeholders: team CC-CoInf-CI (@d062284, @d051408, @d065300 per `docs/related-artifacts.md`); consumers are the five current wrapper charts (`metal-operator-remote`, `boot-operator-remote`, `argora-operator-remote`, `khalkeon-remote`, `ipam-capi-remote`) that will migrate later.

## Goals / Non-Goals

**Goals:**
- Kubebuilder-scaffolded Go project builds (`go build ./...`) and passes `go vet ./...`.
- CRD manifest generation via `make manifests` emits a single CRD file for `DualDeploymentOperator.dual-deployment-operator.cc.sap/v1alpha1` under `config/crd/bases/`.
- CRD schema encodes all admission-time validation rules for v1: exactly-one-of-Source, exactly-one-of-Transformation, exactly-one-of-PatchSpec-variant, kustomize URL contains `?ref=`, RFC 6902 `Op` enum, `KustomizeSource.HostPath`+`RemotePath` both required.
- No-op reconciler compiles, reads a CR, logs one info line, requeues after 10 minutes.
- Webhook infrastructure (Service, cert-manager Certificate, ValidatingWebhookConfiguration) is scaffolded via `kubebuilder create webhook --programmatic-validation`, but the webhook is not deployed and its `CustomValidator` methods return `nil`.
- Unit tests cover type marshal/unmarshal round-trips and enum boundaries.
- envtest suite installs the CRD, verifies CEL rejection paths for each documented invalid shape, and verifies the reconciler wiring end-to-end.

**Non-Goals:**
- Source rendering (Helm SDK, krusty) — Phase 2.
- Transformation execution (`patch`, `rewriteWebhookURL`, `filterKinds`, `packageWebhookConfigsForInjector`) — Phase 3.
- Server-side apply, dual-cluster clients, drift correction, health tracking — Phases 4-6.
- Finalizer for CR deletion — Phase 6.
- Deployment chart in `sapcc/helm-charts` — Phase 8.
- Wiring the validating admission webhook (methods stay no-op; ValidatingWebhookConfiguration is not deployed) — deferred to v2 per `docs/implementation.md` §"Validating admission webhook" and §9.9.
- Content-level patch validation against target OpenAPI schemas — deferred to v2 alongside webhook wiring.
- Strict semver-format validation of `HelmSource.Version` — only `MinLength=1` in v1; strict validation deferred to the same v2 webhook.

## Decisions

**Decision: Types file layout**
- Chosen: Single monolithic `api/v1alpha1/dualdeploymentoperator_types.go` (~350 lines) containing all Spec, Status, discriminated-union, and helper types.
- Reason: Matches `docs/implementation.md` §Phase 1 exactly; kubebuilder's `zz_generated.deepcopy.go` and CRD manifest generation work with zero custom config; ~350 lines is comfortable to read; splitting adds import juggling with no material benefit at this size.
- Alternatives considered: split by concern (`source_types.go`, `transformation_types.go`, `patch_types.go`, `status_types.go`) — rejected as speculative decomposition. Subpackage split (`api/v1alpha1/source/`, `api/v1alpha1/transform/`) — rejected because kubebuilder does not scaffold this layout; requires manual deepcopy and CRD-generation config.

**Decision: Admission validation via CEL, not a validating admission webhook**
- Chosen: Encode all v1 discriminator constraints as `+kubebuilder:validation:XValidation` CEL rules on the CRD schema. Scaffold the webhook `CustomValidator` interface but leave `ValidateCreate` / `ValidateUpdate` / `ValidateDelete` returning `nil`; do not deploy the `ValidatingWebhookConfiguration`.
- Reason: `docs/implementation.md` §Phase 1 (line 254) explicitly recommends CEL as "cleaner than validating webhook" for the discriminator work this change needs. CEL rules ship inside the CRD manifest, requiring no additional Pod, no cert lifecycle, no bootstrap ordering. The four discriminator constraints (Source, Transformation, PatchSpec, kustomize URL) all express cleanly in CEL. Kubernetes 1.29+ (already required for target seeds) supports CEL on CRDs.
- Alternatives considered:
  1. Validating admission webhook (my earlier brainstorm draft) — rejected because it requires cert-manager, Pod availability coupling, and bootstrap ordering to enforce constraints that fit CEL cleanly. Webhook is still needed for v2 content-level patch validation (which CEL cannot express), so the scaffold is kept — but not deployed.
  2. Runtime-only validation in the reconciler — rejected because it lets invalid CRs land in etcd, appearing in `kubectl get` output, permanently emitting Degraded status conditions. Admission-time rejection is the standard Kubernetes UX.

**Decision: `HelmSource` has 3 values fields; `KustomizeSource` has no `values` field**
- Chosen: `HelmSource` = `Repo`, `Name`, `Version`, `Values *apiextensionsv1.JSON`, `HostValues *apiextensionsv1.JSON`, `RemoteValues *apiextensionsv1.JSON`. `KustomizeSource` = `URL`, `HostPath`, `RemotePath`. No `values`-like field on `KustomizeSource`.
- Reason: Kustomize has no Helm-values equivalent per `docs/design.md` §3.3. Under the two-render pattern (r4), `HelmSource` needs three values slots: common values applied to both renders, host-only overrides, and remote-only overrides — merged in that precedence order at render time (Phase 2). Kustomize handles the same "different per mode" problem via overlay paths (`HostPath` / `RemotePath`) instead. The asymmetry reflects real capability differences in the two source engines; forcing symmetry (giving kustomize a fake `values` map) would either duplicate Helm behavior badly or expose kustomize internals through a misleading interface.
- Alternatives considered:
  1. Single `Values *apiextensionsv1.JSON` on `HelmSource` plus mode-selection via a mode-value convention (e.g., user sets `.Values.mode = "host"` themselves) — rejected because it puts the two-render mechanism in the user's hands instead of the operator's, and duplicates the operator's mode injection.
  2. Give `KustomizeSource` a `Values map[string]string` that projects into a `configMapGenerator` at render time — rejected per `docs/implementation.md` §Phase 2: kustomize sources are self-contained; per-CR parameterization uses overlays or operator transformations.

**Decision: `KustomizeSource.HostPath` and `RemotePath` are both required, no defaults**
- Chosen: Both fields declared as `+kubebuilder:validation:MinLength=1`, no `omitempty` on the JSON tag, no `+kubebuilder:default`.
- Reason: (1) Only ipam-capi uses `KustomizeSource` in v1 — the verbosity-reduction argument for defaults barely applies. (2) The subpath name is a coordination point between the operator config and the kustomize source repo's layout; making it explicit in the CR forces both sides to name it in one place. (3) Silent defaults produce a bad failure mode on source-layout renames: operator renders from a non-existent default path, krusty fails deep inside render with an obscure "path not found" error, not at admission time. (4) Symmetry with `HelmSource.HostValues` / `RemoteValues`, which have no defaults.
- **Deviation from docs**: `docs/design.md` §3.3 lines 459-460 and `docs/implementation.md` §Phase 1 lines 128-130 describe both fields as optional with defaults `"host"` and `"remote"`. This design intentionally overrides the docs; the migration plan below records the doc update as a task.
- Alternatives considered:
  1. `+kubebuilder:default="host"` / `+kubebuilder:default="remote"` (matches docs exactly, surfaces defaults in `kubectl explain`) — rejected for the reasons above.
  2. Apply defaults in Go layer at render time (matches docs behaviorally, hides defaults from CRD introspection) — rejected: worst of both worlds (implicit default plus no CRD visibility).

**Decision: Transformation menu has 4 types across 2 scopes**
- Chosen: `Transformation` is a discriminated union over four pointer fields:
  - Per-render (3): `Patch *PatchSpec`, `RewriteWebhookURL *RewriteWebhookURLSpec`, `FilterKinds *FilterKindsSpec`
  - Cross-stream (1): `PackageWebhookConfigsForInjector *PackageWebhookConfigsForInjectorSpec`
  Exactly one field is set per entry, enforced by CEL:
  `(has(self.patch)?1:0) + (has(self.rewriteWebhookURL)?1:0) + (has(self.filterKinds)?1:0) + (has(self.packageWebhookConfigsForInjector)?1:0) == 1`
- Reason: Matches `docs/design.md` §3.4 revision 6 menu exactly. `renameKind` was removed in r6 (direct-apply preserves Roles as Roles across all target operators). `injectInitContainer` and `addLabels` collapsed into `patch` in r6 (their typed wrappers hid the same content a patch would show explicitly). `packageWebhookConfigsForInjector` was added in r5 to accommodate the unmodifiable webhook-injector's ConfigMap contract. This change lands the CRD shape only; execution logic is Phase 3.
- Alternatives considered:
  1. Keep r5's 5-type per-render menu — rejected because `docs/design.md` has moved on to r6.
  2. Fold cross-stream into per-render (a single scope) — rejected because `packageWebhookConfigsForInjector` fundamentally operates on both renders' outputs together (reads WebhookConfigurations from remote, emits a ConfigMap into host). Per-render scope can't express that.

**Decision: `PatchSpec` uses typed variants `strategicMerge` XOR `jsonPatch`, not an opaque string**
- Chosen: `PatchSpec` has three fields: `Target Selector` (required), `StrategicMerge *apiextensionsv1.JSON` (schemaless, preserves unknown fields), `JSONPatch []JSONPatchOp` (typed op array with RFC 6902 enum). CEL rule enforces `has(self.strategicMerge) != has(self.jsonPatch)`.
- Reason: `docs/design.md` §3.4.1 and `docs/context.md` r6 refinement. Typed variants let the CRD's OpenAPI schema validate the outer shape at admission time (object vs. array, RFC 6902 op enum, required fields per op) rather than treating patch content as an opaque string blob. Content-level correctness (field types inside the patch body, unknown fields against the target kind's schema) is not validated in v1 — deferred to v2's validating admission webhook per `docs/design.md` §9.9.
- Alternatives considered:
  1. Opaque `patch: {type: <enum>, patch: <string>}` (r6 initial draft) — rejected in r6 because the string content bypasses admission validation entirely; users can submit invalid YAML/JSON and only discover it at reconcile time.
  2. Fully validated patches with target-kind schema check at admission — rejected for v1 because it requires the OpenAPI-schema-aware webhook (v2 scope).

**Decision: Semver-strict validation of `HelmSource.Version` is deferred to v2**
- Chosen: `Version` gets `+kubebuilder:validation:MinLength=1` only. No CEL semver check.
- Reason: CEL's regex support (`self.matches(...)`) can express a naive semver pattern, but a strict `github.com/Masterminds/semver/v3` constraint check requires Go code, not CEL. `docs/implementation.md` line 271: "semver-strict validation deferred to webhook — CEL can't validate semver format cleanly." Combined with the v1 webhook-not-wired stance, this means v1 accepts any non-empty version string; Phase 2's Helm renderer will fail with a clear error if the string doesn't parse as a semver constraint.
- Alternatives considered:
  1. Loose CEL regex for semver (`self.matches('^v?[0-9]+\\.[0-9]+\\.[0-9]+.*$')`) — rejected because a partial check catches obvious garbage but misses real constraint expressions like `">=0.7, <0.8"` or `"0.7.x"` that Helm accepts. Half-validation with false rejections is worse than no validation with clear runtime errors.

**Decision: Webhook Service + cert-management config scaffolded, not deployed**
- Chosen: Run `kubebuilder create webhook --programmatic-validation` to scaffold the full webhook infrastructure. Keep every generated file (`config/webhook/*.yaml`, `config/certmanager/*.yaml`, `config/default/*_webhook_patch.yaml`, `config/default/webhookcainjection_patch.yaml`, `internal/webhook/v1alpha1/*.go`). Leave kubebuilder's `[WEBHOOK]` and `[CERTMANAGER]` comment-guarded sections in `config/default/kustomization.yaml` and `config/crd/kustomization.yaml` **commented out** — the webhook infrastructure ships in the repo but is off in the default deployment path.
- Reason: v2 wiring becomes a pure kustomize-overlay flip (uncomment 2 sections + implement content-level validation in `CustomValidator` methods), avoiding re-scaffolding and the risk of scaffold drift between kubebuilder versions. Runtime cost in v1 is zero (files aren't deployed); disk cost is ~10 unused YAML files in `config/`; reviewer attention cost is one-time (v1 reviewers understand the scaffold; ongoing PRs won't touch these files).
- Alternatives considered:
  1. Delete generated webhook files — rejected because re-scaffolding in v2 risks drift with the kubebuilder version used at that time.
  2. Deploy the webhook now with no-op `ValidateCreate` etc. — rejected because it introduces the cert-manager dependency, Pod-availability coupling for CR writes, and bootstrap ordering without any admission-time benefit (CEL already validates all v1 constraints).

**Decision: No-op reconciler with 10-minute requeue**
- Chosen: `Reconcile` fetches the CR (return `client.IgnoreNotFound(err)` on missing), logs one info line `"reconciling"` with `cr.Name`, and returns `ctrl.Result{RequeueAfter: 10*time.Minute}, nil`. No finalizer, no status writes, no owner references.
- Reason: Proves that the manager, scheme, reconciler wiring, and CRD registration all work end-to-end. The 10-minute interval matches `docs/design.md` §3.6.2's drift-correction default, so v1 lands with the same cadence Phase 6 will use for real work — no "temporary" value that later needs migration. The no-op tick does nothing useful per fire, but 10 minutes is low enough overhead (one work-queue enqueue per shoot-cp namespace every 10 minutes) that the waste is negligible.
- Alternatives considered:
  1. `ctrl.Result{}, nil` (controller-runtime literal default, event-driven only, no periodic requeue) — rejected because it doesn't exercise the requeue path in envtest, so a bug where Phase 6 forgets to set `RequeueAfter` at all would slip through. Landing 10 minutes now bakes the correct-by-design interval into the test suite from v1.
  2. `RequeueAfter: 5*time.Minute` (earlier brainstorm draft) — rejected because it introduces a value that Phase 6 has to migrate for no lasting benefit; keeps only one interval in play across the operator's lifetime.
  3. Include finalizer add/remove now — rejected as speculative; the finalizer name and cleanup semantics belong with the cleanup logic in Phase 6.
  4. Include a status.condition write to `Ready=True` — rejected because it commits the reconciler to a status shape before real logic exists; better to write status when it means something.

**Decision: Include full Status types now even though the reconciler doesn't populate them**
- Chosen: Land `DualDeploymentOperatorStatus`, `ResourceStatus`, `HealthState` enum, and all fields (`HostResources`, `RemoteResources`, `Conditions`, `LastReconcile`) in this change.
- Reason: Adding status-subresource fields later carries CRD-migration risk (existing CRs need to survive schema tightening). Landing the full shape now — while no CRs exist — costs nothing and prevents future migration friction. The reconciler leaves the fields at zero values; Phase 6 populates them.

**Decision: Kubebuilder scaffold defaults; API group `dual-deployment-operator.cc.sap`; module `github.com/SAP-cloud-infrastructure/dual-deployment-operator`**
- Chosen: `kubebuilder init --domain cc.sap --repo github.com/SAP-cloud-infrastructure/dual-deployment-operator`, then `kubebuilder create api --group dual-deployment-operator --version v1alpha1 --kind DualDeploymentOperator --resource --controller`, then `kubebuilder create webhook --group dual-deployment-operator --version v1alpha1 --kind DualDeploymentOperator --programmatic-validation`.
- Reason: `github.com/SAP-cloud-infrastructure/` colocates the operator with its cert-lifecycle companion `webhook-injector` (`SAP-cloud-infrastructure/webhook-injector`) — they coexist per `docs/design.md` §3.8. Group and domain per `docs/design.md` §3.2.

## Capabilities

New capabilities (each becomes `specs/<name>/spec.md`):

- **crd-types** — The `v1alpha1` `DualDeploymentOperator` Custom Resource Definition, its Spec and Status Go types, and Go-level type invariants (`Origin`, `Target`, `HealthState`, `Mode` enums; `PatchSpec` discriminated variants; `Selector` matching semantics).
- **cel-admission-validation** — CRD-level CEL validation rules that reject invalid CRs at admission time (discriminator constraints on `Source`, `Transformation`, `PatchSpec`; kustomize URL ref-pinning; JSON Patch op enum; required fields).
- **operator-scaffold** — The kubebuilder project structure: `go.mod`, `cmd/main.go`, `Makefile`, `Dockerfile`, `config/` overlays, `PROJECT` file, `hack/`, and the scaffolded webhook infrastructure (present but not deployed in v1).
- **noop-reconciler** — The `DualDeploymentOperator` controller: watches CRs, logs on reconcile, requeues after 10 minutes, does no other work.

Modified capabilities: none (greenfield change).

## Example Custom Resources

These examples exercise every field the `v1alpha1` CRD accepts. They serve as fixtures for the envtest acceptance-path scenarios and as the canonical reference for CR authors.

> Scope note: in this change (Phase 0+1) the operator only validates these CRs at admission and logs one line per reconcile. The `source`, `transformations`, `remoteKubeconfig`, and `status` fields are parsed but not acted on — rendering, transformation execution, apply, and status population land in later phases.

### Example 1 — Helm source, all transformation types (metal-operator)

The most complex real-world case. Uses every `HelmSource` field, all four transformation types, and both `patch` variants (`strategicMerge` and `jsonPatch`).

```yaml
apiVersion: dual-deployment-operator.cc.sap/v1alpha1
kind: DualDeploymentOperator
metadata:
  name: metal-operator
  namespace: shoot--cp--m-eu-de-1
spec:
  source:
    helm:                                    # discriminated union: helm XOR kustomize
      repo: oci://keppel.eu-de-1.cloud.sap/ccloud-helm
      name: metal-operator-remote
      version: "0.7.x"                        # any non-empty string in v1; strict semver deferred to v2
      values:                                 # applied to BOTH host and remote renders
        apiserverURL: "api.m-eu-de-1.cp.cloud.sap"
        webhookInjector:
          image: keppel.global.cloud.sap/ccloud/webhook-injector
          tag: sha-fd8a075
      hostValues:                             # host-render-only overrides
        metal-operator-core:
          controllerManager: {enable: true}
          rbac: {enable: false}
          crd: {enable: false}
          webhook: {enable: false}
      remoteValues:                           # remote-render-only overrides
        metal-operator-core:
          controllerManager: {enable: false}
          rbac: {enable: true}
          crd: {enable: true}
          webhook: {enable: true}
  remoteKubeconfig:
    secretName: metal-operator-remote-kubeconfig
    key: kubeconfig
  transformations:
    # patch / strategicMerge — inject webhook-injector sidecar (host render)
    - patch:
        target: {kind: Deployment, name: metal-operator-controller-manager, origin: upstream}
        strategicMerge:
          spec:
            template:
              spec:
                initContainers:
                  - name: webhook-injector
                    image: keppel.global.cloud.sap/ccloud/webhook-injector:sha-fd8a075
                    args: [--webhook-config-name=metal-operator-remote-webhook-config]
                    volumeMounts:
                      - {name: webhook-certs, mountPath: /tmp/k8s-webhook-server/serving-certs, readOnly: true}
                volumes:
                  - {name: webhook-certs, emptyDir: {}}
    # patch / jsonPatch — RFC 6902 ops (illustrative)
    - patch:
        target: {kind: ServiceAccount, name: metal-operator-controller-manager, origin: upstream}
        jsonPatch:
          - {op: add, path: /metadata/annotations/managed-by, value: "dual-deployment-operator"}
          - {op: test, path: /kind, value: "ServiceAccount"}
    # rewriteWebhookURL — service → url (remote render)
    - rewriteWebhookURL:
        urlPrefix: "https://metal-operator-remote-webhook-service:443"
        targetKinds: [ValidatingWebhookConfiguration, MutatingWebhookConfiguration]  # optional; these are the defaults
    # filterKinds — drop upstream Services, keep ours
    - filterKinds:
        kinds: [Service]
        source: upstream
    # packageWebhookConfigsForInjector — cross-stream; emits ConfigMap on host from remote WebhookConfigs
    - packageWebhookConfigsForInjector:
        configMapName: metal-operator-remote-webhook-config
        dataKey: webhooks.yaml                 # optional; default "webhooks.yaml"
  deletionPolicy:
    crds: Retain                              # default; shown explicitly
```

### Example 2 — Kustomize source (ipam-capi)

`KustomizeSource` has no `values` field (kustomize has no Helm-values equivalent). Per-mode differentiation comes from the two required overlay subpaths inside the pinned source. `url` must include `?ref=` (CEL-enforced).

```yaml
apiVersion: dual-deployment-operator.cc.sap/v1alpha1
kind: DualDeploymentOperator
metadata:
  name: ipam-capi
  namespace: shoot--cp--m-eu-de-1
spec:
  source:
    kustomize:
      url: "https://github.com/sapcc/helm-charts//system/kustomize/ipam-capi-remote/?ref=v1.2.31"
      hostPath: "host"                        # required — no default
      remotePath: "remote"                    # required — no default
  remoteKubeconfig:
    secretName: ipam-capi-remote-kubeconfig
    key: kubeconfig
  transformations:
    - patch:
        target: {kind: Deployment, name: capi-ipam-in-cluster-controller-manager, origin: upstream}
        strategicMerge:
          spec:
            template:
              spec:
                initContainers:
                  - name: webhook-injector
                    image: keppel.global.cloud.sap/ccloud/webhook-injector:sha-fd8a075
                    args: [--webhook-config-name=ipam-capi-remote-webhook-config]
                    volumeMounts:
                      - {name: webhook-certs, mountPath: /tmp/k8s-webhook-server/serving-certs, readOnly: true}
                volumes:
                  - {name: webhook-certs, emptyDir: {}}
    - rewriteWebhookURL:
        urlPrefix: "https://ipam-capi-remote-webhook-service:443"
    - filterKinds:
        kinds: [Service]
        source: upstream
    - packageWebhookConfigsForInjector:
        configMapName: ipam-capi-remote-webhook-config
```

### Example 3 — Minimal Helm CR, no transformations

The smallest valid CR: only `source` and `remoteKubeconfig` are required. `transformations` defaults to empty; `deletionPolicy.crds` defaults to `Retain`.

```yaml
apiVersion: dual-deployment-operator.cc.sap/v1alpha1
kind: DualDeploymentOperator
metadata:
  name: simple-operator
  namespace: shoot--cp--m-eu-de-1
spec:
  source:
    helm:
      repo: oci://keppel.eu-de-1.cloud.sap/ccloud-helm
      name: simple-operator-remote
      version: "1.0.0"
  remoteKubeconfig:
    secretName: simple-operator-remote-kubeconfig
    key: kubeconfig
```

### Example 4 — Populated status (illustrative; written by Phase 6, not this change)

The status shape lands in this change but is populated only from Phase 6 onward. Shown so CR authors know what a reconciled CR looks like.

```yaml
status:
  lastReconcile: "2026-07-09T14:32:10Z"
  hostResources:
    - {kind: Deployment, apiVersion: apps/v1, namespace: shoot--cp--m-eu-de-1, name: metal-operator-controller-manager, health: Healthy, lastApplied: "2026-07-09T14:32:09Z"}
    - {kind: ConfigMap, apiVersion: v1, namespace: shoot--cp--m-eu-de-1, name: metal-operator-remote-webhook-config, health: Healthy, lastApplied: "2026-07-09T14:32:09Z"}
  remoteResources:
    - {kind: CustomResourceDefinition, apiVersion: apiextensions.k8s.io/v1, name: endpoints.metal.ironcore.dev, health: Healthy, lastApplied: "2026-07-09T14:32:09Z"}
    - {kind: ValidatingWebhookConfiguration, apiVersion: admissionregistration.k8s.io/v1, name: metal-operator-validating-webhook-configuration, health: Healthy, lastApplied: "2026-07-09T14:32:09Z"}
  conditions:
    - {type: Ready, status: "True", reason: AllResourcesHealthy, message: "All host and remote resources reconciled and healthy", lastTransitionTime: "2026-07-09T14:32:10Z"}
    - {type: HostReconciled, status: "True", reason: HostApplySucceeded, message: "host resources Healthy", lastTransitionTime: "2026-07-09T14:32:09Z"}
    - {type: RemoteReconciled, status: "True", reason: RemoteApplySucceeded, message: "remote resources Healthy", lastTransitionTime: "2026-07-09T14:32:09Z"}
```

## Risks / Trade-offs

- [envtest CEL support requires Kubernetes 1.29+] → Pin `ENVTEST_K8S_VERSION` in the Makefile to a 1.29+ version explicitly (rather than relying on kubebuilder's default). Fail CI early with a clear error if the pinned version isn't available.

- [CEL rules on CRDs are silently ignored on Kubernetes < 1.29] → All target seeds run 1.29+ per team roadmap; verify per-seed before v1 rollout. Documented as a deployment prerequisite in the Migration Plan below and in the deployment chart's README (Phase 8 scope).

- [Deviation from docs on `HostPath` / `RemotePath` required-ness] → Update `docs/design.md` §3.3 and `docs/implementation.md` §Phase 1 to match the code. Recorded as a task in the plan artifact.

- [Semver-strict validation deferred means users can save CRs with garbage `Version` strings] → Phase 2's Helm renderer surfaces a clear error and sets a Degraded condition; v2's webhook rejects at admission. Between v1 land and v2 land, invalid CRs sit in etcd emitting reconcile errors — same failure mode as any other content-level runtime error, acceptable for v1alpha1.

- [Webhook scaffold files sit unused in the repo] → Reviewer confusion when new contributors browse `config/`. Mitigation: a one-paragraph `CONTRIBUTING.md` (or a comment in `config/default/kustomization.yaml`) explaining the `[WEBHOOK]` / `[CERTMANAGER]` sections are v2-gated. Include as a task in the plan.

- [Kubebuilder version drift between v1 scaffold and v2 wiring] → Pin the kubebuilder version used in this change (documented in `Makefile` `KUBEBUILDER_VERSION` variable). v2 uses the same pinned version to avoid drift, upgrading kubebuilder as a separate change.

- [`*apiextensionsv1.JSON` fields with `PreserveUnknownFields` bypass CRD structural schema] → Content inside `HelmSource.Values`, `HelmSource.HostValues`, `HelmSource.RemoteValues`, `PatchSpec.StrategicMerge`, `JSONPatchOp.Value` is not admission-validated. Failures surface at reconcile time. Documented in `docs/implementation.md` line 279-282; accepted as the trade for arbitrary user-supplied content.

- [Status types included but not populated make `kubectl describe` show empty status fields] → Cosmetic only; the CRD's `printerColumns` will not reference status fields until Phase 6 lands them, so `kubectl get dualdeploymentoperator` output is not affected.

## Migration Plan

Greenfield change, no live CRs to migrate. Deployment steps:

1. Merge this change to `main`; tag the resulting commit for reference.
2. Verify `make manifests test build` all pass in CI.
3. Update `docs/design.md` §3.3 and `docs/implementation.md` §Phase 1 to reflect the `HostPath`/`RemotePath` required-ness decision (recorded in the plan artifact).
4. The CRD manifest at `config/crd/bases/dual-deployment-operator.cc.sap_dualdeploymentoperators.yaml` is the artifact consumed by Phase 8's deployment chart. This change lands the CRD; Phase 8 installs it into shoot-cp namespaces.
5. No cluster-side deployment happens as part of this change — the deployment chart is Phase 8.

Rollback: since no cluster-side deployment happens, rollback is a `git revert` of the merge commit. If Phase 8 has already deployed based on this change, rollback requires uninstalling the CRD from any cluster it was applied to (which will fail if any CRs exist; standard `kubectl delete crd` with `--wait=false` if forced).

## Open Questions

- [ ] Concrete versions of `helm.sh/helm/v3`, `sigs.k8s.io/kustomize/api`, `sigs.k8s.io/controller-runtime`, `k8s.io/*` at implementation time — verify current stable when Phase 0 lands. `docs/implementation.md` §"Concrete v1 dependencies" gives an illustrative pin set; verify against latest before locking. — owner: implementer during the plan phase
- [ ] envtest binary version — must be 1.29+ to support CEL rules on CRDs. Kubebuilder's default `ENVTEST_K8S_VERSION` typically follows recent stable; confirm the value used in `bin/setup-envtest` supports CEL and matches what the target seeds run. — owner: implementer during the plan phase
- [ ] Should `docs/design.md` §3.3 and `docs/implementation.md` §Phase 1 be updated in this change or in a follow-up doc-only PR? Recommendation: in this change, since the deviation is visible in the code that lands with this change. — owner: plan artifact
