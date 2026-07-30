# Design: Equivalence Tests (Phase 8)

## Overview

Equivalence tests prove the operator's rendered output matches today's
`<operator>-remote` wrapper charts for all five operators (`metal-operator`,
`boot-operator`, `argora-operator`, `khalkeon`, `ipam-capi`). This is a
load-bearing gate (design.md §7.3): a failure means the operator produces
different output than today's chart and must be investigated before migration.

## Architecture

New package `internal/equivalence/` with four concerns:

1. **Golden helper** — pull the published wrapper chart from GHCR
   (`oci://ghcr.io/sapcc/helm-charts/charts/<op>-remote`) at a pinned chart
   version, render the **packaged** chart (deps vendored, so no transitive keppel
   pull for `owner-info`) with `helm template`, and split the single rendered
   stream into seed-destined vs shoot-destined sets.
2. **Operator helper** — capture the operator's two manifest streams by calling
   the pipeline directly: `source.From(cr.Spec.Source, deps)` →
   `Render(ModeSeed, cr.Namespace)` + `Render(ModeShoot, cr.Spec.ShootNamespace)`
   → `transform.Build(cr.Spec.Transformations)` → apply each transform to both
   streams. No reconciler, no envtest, no fake Applier.
3. **Normalizer + comparator** — pure functions: parse YAML → sort by
   GVK+namespace+name → drop comments/whitespace/ordering → strip an allowlist of
   expected-diff fields → deep-equal per resource, reporting residual mismatches.
4. **Fixture registry** — per operator: `cr.yaml`, representative shoot values,
   and the pinned chart version.

## Golden side (today's chart)

- Source of truth: the wrapper charts published to GHCR by `sapcc/helm-charts`'s
  `helm-push.yaml` workflow (`helm push <pkg> oci://ghcr.io/sapcc/helm-charts/charts/`).
  Pull path: `oci://ghcr.io/sapcc/helm-charts/charts/<op>-remote --version <pinned>`.
- Pin by chart **version** (immutable/versioned OCI artifact), recorded per
  fixture — not by a git SHA. GHCR-pure; the operator itself publishes to GHCR
  (Phase 9.5) and keppel only mirrors ghcr.io.
- Render the **packaged** chart (its `charts/` already contains resolved
  subcharts), so `helm template` does not transitively pull keppel for the
  wrapper's `owner-info` dependency.
- Split the rendered stream into today's seed/shoot sets using the signals today's
  chart uses: committed `managedresources/*` + `webhooks.yaml` → shoot;
  `controller-manager.yaml` + chart templates → seed.
- **Classify the golden stream into THREE buckets (REQUIRED).** Today's chart does
  NOT apply directly to the shoot, and it emits seed-side install plumbing the
  operator's chart (Phase 9) owns rather than the operator's render. Each rendered
  doc falls into exactly one bucket:

  1. **Unwrap → bare objects.** Delivery wrappers whose payload IS an operator-side
     object:
     - `ManagedResource` + paired `Secret` (CRDs/RBAC): follow `spec.secretRefs` to
       the `Secret`, base64-decode `data["objects.yaml"]`, splat the contained
       doc(s) back as bare objects; discard the `ManagedResource`+`Secret`.
     - the injector webhook-config `ConfigMap`: decode `data["webhooks.yaml"]` into
       bare WebhookConfiguration objects; discard the ConfigMap.

     **Identify the injector ConfigMap PRECISELY — only it, never other ConfigMaps.**
     Match on its stable identity: name `== <chart-fullname>-webhook-config` (e.g.
     `metal-operator-remote-webhook-config`, `ipam-capi-remote-webhook-config`) AND
     its sole data key is `webhooks.yaml`. Do NOT match on `kind == ConfigMap` — the
     charts contain other, real ConfigMaps that must pass through to bucket 3.

  2. **Exclude entirely** — ONLY objects the operator's render emits no equivalent
     for (decided by render-emission, NOT by name). Enumerated + justified per
     operator — e.g. the `owner-info` subchart output, or a legacy delivery object
     with no seed/shoot-render counterpart. These would otherwise show up as false
     "operator is missing these resources" failures.

     CAUTION — do NOT blanket-exclude `remote-kubeconfig`. The
     `<operator>-remote-kubeconfig` Secret and the `remote-kubeconfig` kubeconfig
     ConfigMap are mounted by the **managed controller-manager** (metal/boot/…) to
     reach ITS OWN CRs in the shoot, and the operator's SEED render emits both the
     controller-manager Deployment AND these access objects (design §3.5: line
     `ConfigMap/remote-kubeconfig | additions | seed`). They therefore belong in
     bucket 3 (keep+compare), not here. A *separate* consumer — the
     dual-deployment-operator's own `spec.shootAccess` delivery credential — is not a
     rendered object, so there is nothing to exclude for it either.

  3. **Keep + compare** — everything else, i.e. the real application resources:
     Deployments, Services, NetworkPolicy, seed RBAC, the managed
     controller-manager's shoot-access objects (`remote-kubeconfig` ConfigMap +
     `<operator>-remote-kubeconfig` Secret mount), and every NON-injector ConfigMap
     (e.g. metal-operator's `dns-record-template`, ipam-capi's `manager-config`).
     These are compared object-for-object against the operator render.

  After bucketing: bucket-1 injector-ConfigMap webhooks AND the ManagedResource-
  unwrapped payloads are **shoot**-destined (today's chart delivers `managedresources/*`
  to the shoot); bucket-3 kept chart-template objects are **seed**-destined. Together
  they form the golden seed/shoot sets compared against the operator's two renders.
  This is the concrete realization of "compare resolved end-state objects, not
  delivery wrapper". `ClassifyGolden(passthrough, shootFromMR, opts)` takes the MR
  payloads as a separate shoot-destined input; `UnwrapManagedResources` returns
  `(fromMR, passthrough)`. The bucket-2 exclusion list is enumerated per operator in
  the fixture and is itself a reviewable artifact (adding an entry needs a one-line
  justification).
- **INVARIANT — every golden-side manipulation is identity-gated and no-op-safe on a
  generic chart.** The bucketing/unwrap/exclude logic MUST NOT assume any wrapper
  exists. Each transform triggers ONLY on a positively-matched identity and is a
  pass-through when its trigger is absent:
  - No `ManagedResource` present → bucket-1 MR-unwrap does nothing; docs flow to
    bucket 3.
  - No injector ConfigMap (name `<fullname>-webhook-config` + sole key
    `webhooks.yaml`) → no ConfigMap is unwrapped; ALL ConfigMaps flow to bucket 3
    unchanged. (Verified real: `boot-operator`, `argora-operator`, `khalkeon` have
    NO injector ConfigMap.)
  - Empty/absent bucket-2 exclusion list → nothing excluded.
  A chart with none of these wrappers (a plain application chart) therefore passes
  through the golden pipeline **unchanged** — every doc lands in bucket 3 and is
  compared as-is. The default for any unrecognized doc is bucket 3 (keep+compare),
  never silent drop: exclusion (bucket 2) requires an explicit enumerated match, and
  unwrap (bucket 1) requires a positive wrapper-identity match. This keeps the harness
  safe to point at future operators and generic charts without special-casing, and
  guarantees the manipulation can never quietly delete a resource it failed to
  recognize.
- Pinned chart versions per fixture (current `master`, 2026-07):
  `metal-operator-remote` `0.6.30`, `boot-operator-remote` `0.4.21`,
  `argora-operator-remote` `0.0.60`, `khalkeon-remote` `0.1.1`,
  `ipam-capi-remote` `1.2.31`.
- `ipam-capi` render: RESOLVED — uniform `helm template`. `ipam-capi-remote` is a
  normal Helm chart (`apiVersion: v2`, `type: application`) with the same layout as
  the others; its upstream kustomize output is already pre-rendered/committed into
  `managedresources/*.yaml`. All five golden renders use `helm pull` +
  `helm template`; no `kubectl kustomize` path on the golden side. This makes
  ipam-capi the highest-signal case: operator side renders live from the real
  upstream kustomize source, golden side is a frozen helmify snapshot.

## Operator side

- Drive `source.From → Render(seed/shoot) → transform.Build → Apply` directly and
  capture `[]manifest.Manifest` per mode **before** the deliver layer.
- Real source fetch still happens here (keppel anonymous chart pull / github public
  kustomize) — both already proven in Phase 7.
- Delivery (SSA/prune/health) is intentionally out of scope: covered by Phase 5/6.
- **Production resolver fix (found here).** Wiring the ipam-capi kustomize fixture
  (which pins a historical git SHA) exposed a bug in the Phase-7 git `RootResolver`:
  `fetchSHA` used a `Depth: 1` shallow fetch of branch/tag tips, so an arbitrary
  historical commit SHA (not a ref tip) was never downloaded and the checkout
  failed with "object not found (fail-closed)". Fixed by fetching advertised refs
  to full depth, still fail-closed if the hash is genuinely absent. See the
  `kustomize-root-resolver` spec delta in this change and the regression test
  `TestGitResolverResolvesHistoricalSHA`.

## Comparison

Canonical normalize + allowlisted ignores, deep-equal per resource, applied within
a **scoped equivalence** (Decision B).

**Scoped equivalence (Decision B).** The operator renders the UPSTREAM chart
directly; the golden side renders the `<operator>-remote` WRAPPER, which disables
most of upstream and substitutes pre-rendered `managedresources/*` + sapcc
additions. The two legitimately emit different object sets (verified in triage:
operator emits per-CRD `*-admin/editor/viewer` ClusterRoles, cert-manager
Issuer/Certificate, metrics services; golden emits Ingress, NetworkPolicies,
webhook-injector RBAC, remote-kubeconfig, macdb, token-rotate). So the assertion is
scoped, not full-object-set:

- **Compared kinds** (per fixture): the delivered kinds both sides are expected to
  produce — `CustomResourceDefinition`, `ClusterRole`, `ClusterRoleBinding`, `Role`,
  `RoleBinding`, `ServiceAccount`, `Validating`/`MutatingWebhookConfiguration`.
  Objects of other kinds are ignored by the comparison.
- **Known divergences** (per fixture, kind+name + justification): specific in-scope
  objects expected on exactly one side, not reported as missing/extra.
- Within scope and after removing known divergences, FULL per-resource deep-equal
  still applies. Scoping narrows WHICH objects are compared; it never weakens
  field-level rigor on in-scope objects and never hides a transformation-output
  difference. A known-divergence entry only suppresses single-sided missing/extra —
  it cannot suppress a field mismatch on an object present on both sides.

**Scope boundary — this phase does NOT re-test the transformations.** The three
transformations (`patch`, `rewriteWebhookURL`, `filterKinds`) are already
unit-tested in Phase 3 (`internal/transform/*_test.go`, table-driven). Phase 8
tests that the operator's **whole-pipeline output** (source render + those
transformations applied) matches today's chart's **effective delivered objects**.
Two buckets of difference, treated oppositely:

1. **Delivery-wrapper artifacts** (today's ManagedResource/ConfigMap mechanism,
   which the operator replaces entirely): the `ManagedResource`/`Secret`/`ConfigMap`
   objects, `spec.secretRefs` indirection, base64 encoding, `keepObjects: true`.
   → **Removed by the golden-side unwrap pass** (see Golden side). They have no
   operator-side counterpart, so they are not compared.
2. **Transformation outputs** (the operator produces these via its transforms;
   today's chart produces the equivalent via `make build-` `sed`/`yq` + templates):
   the injector `--target-label` on CRDs/WebhookConfigs, the sidecar initContainer,
   the rewritten webhook URL, dropped Services. → **These MUST match** — reproducing
   them is the equivalence claim. They are NEVER stripped. A difference here is a
   real finding (the operator's transform doesn't reproduce today's output), which
   is exactly what Phase 8 exists to catch.

Comparison steps:

- Parse YAML → sort by GVK+namespace+name → drop comments/whitespace/ordering.
- Strip an allowlist of ONLY provenance/incidental fields (NOT transformation
  outputs; capture is BEFORE the deliver layer, so operator-internal keys are
  present and must be stripped here):
  - chart-provenance labels only today's Helm render carries: `helm.sh/chart`,
    `app.kubernetes.io/managed-by`, `app.kubernetes.io/version` (+ `owner-info`
    chart labels);
  - operator-internal labels/annotations: `dual-deployment-operator.cc.sap/origin`
    (stripped at apply by `StripInternalAnnotations`, but present pre-deliver),
    `dual-deployment-operator.cc.sap/owned-by` (stamped at apply by
    `SetOwnedByLabel`, absent pre-deliver — included defensively);
  - the r7 `caBundle` leaf — belt-and-suspenders only: today's rendered
    `webhooks.yaml` carries NO caBundle (injector fills it at runtime) and the
    operator omits it (§3.8), so both sides are caBundle-absent and match naturally.
  - FINALIZE by run-and-triage: the first real comparator run reveals expected
    provenance/incidental artifacts no upfront reasoning catches (e.g. Helm
    `checksum/config` annotations, namespace injection from `shootNamespace`). Add
    each provably-incidental residual field with a one-line justification during
    plan/apply. Do NOT allowlist a difference that is a transformation output.
- Deep-equal per resource; report residual mismatches with GVK+ns+name + differing
  fields.

**r7 differences reconciled by comparing resolved end-state objects, not delivery
shape**: the golden-side unwrap (see Golden side) turns today's wrapped shoot
stream (ManagedResource+Secret, injector ConfigMap) into the same bare objects the
operator applies directly, so the comparison is over what ends up applied per
target cluster, not how it got there. WebhookConfigs are then compared as bare
objects (both sides caBundle-absent); Roles stay Roles (no `renameKind`) so they
already match today's chart.

## Error handling

- GHCR unreachable / chart version missing → test **fails** (not skips); it gates
  every PR and a silent skip would mask drift. Error names the chart + version.
- Operator render error (source fetch / transform) → fail, surfacing the operator
  error.
- Comparison mismatch → fail with per-resource residual diffs (actionable).

## Testing

- One `t.Run(<operator>)` subtest per operator, table-driven over the fixture
  registry.
- Normalizer + comparator have their own **offline** unit tests, so the
  equivalence machinery is verified independent of network.
- Runs in the default `Test` job (`go test ./...`) — no build tag / env gate —
  gating every PR (design.md §7.3). Requires CI egress to GHCR (golden) +
  keppel/github (operator source).
- Fixtures under `testdata/fixtures/<operator>/`: `cr.yaml`, shoot values, pinned
  chart version. CRITICAL constraint: both sides must render from the SAME inputs.
  Each `-remote` chart's `values.yaml` IS the representative config; the fixture
  CR's `values`/`seedValues`/`shootValues` (Helm) or `patch` transforms (ipam-capi
  kustomize) must reproduce the same effective input the wrapper chart's
  `values.yaml` feeds its render — otherwise diffs are configuration noise, not
  drift. Build each fixture as a PAIR derived from one source of truth. Author
  metal-operator first (richest: sidecar patch, rewriteWebhookURL, injector label,
  filterKinds) to prove pair-derivation, then replicate for the other four.

## Open questions

Investigated against live `sapcc/helm-charts` + this codebase (2026-07); only the
last two carry residual implementation work.

- **Pinned chart versions** — RESOLVED (recorded in "Golden side" above).
- **ipam-capi golden render mechanism** — RESOLVED (uniform `helm template`;
  documented in "Golden side" above).
- **Complete field allowlist** — Starter list defined in "Comparison" above;
  finalize by run-and-triage during plan/apply.
- **Per-operator representative shoot values / CR fixtures** — Pair-derivation
  approach defined in "Testing" above; authoring the five fixtures is the phase's
  main implementation work.
