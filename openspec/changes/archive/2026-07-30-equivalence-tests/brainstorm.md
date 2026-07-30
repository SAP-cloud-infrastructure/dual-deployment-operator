# Brainstorm: Equivalence Tests (Phase 8)

## Design Summary

Add equivalence tests that prove the operator's rendered output matches today's
`<operator>-remote` wrapper charts for all five operators (`metal-operator`,
`boot-operator`, `argora-operator`, `khalkeon`, `ipam-capi`). Each test renders
the golden "today" stream by pulling the published wrapper chart from GHCR
(`oci://ghcr.io/sapcc/helm-charts/charts/<op>-remote`) at a pinned chart version
and running `helm template`; because today's chart wraps shoot resources in
delivery objects (ManagedResource+Secret for CRDs/RBAC, ConfigMap for
WebhookConfigs), the golden side then **unwraps** those back into bare objects. It
produces the operator stream by driving the
`source.From → Render(seed/shoot) → transform.Build → Apply` pipeline directly
(no reconciler, no envtest); it then normalizes both sides to a canonical form
with an allowlist of provenance/incidental fields and asserts deep-equal per
resource. Transformations themselves are already unit-tested in Phase 3; this
phase asserts whole-pipeline output equivalence, so transformation outputs MUST
match (they are never allowlisted).
Tests run in the default CI `Test` job and gate every PR (per `design.md` §7.3).
This is a load-bearing gate: a failure means the operator produces different
output than today's chart and must be investigated before migration.

## Alternatives Considered

### Option A: Committed captured golden fixtures (offline)
- **Approach**: Capture `helm template` / `kubectl kustomize` of today's wrapper
  charts once, commit the rendered YAML as `testdata/fixtures/<op>/today-chart-render.yaml`,
  compare against the operator render fully offline. Matches the original
  `implementation.md` §Phase 8 sketch.
- **Pros**: Deterministic; hermetic; no CI egress; fast.
- **Cons**: Golden fixtures go stale silently as wrapper charts evolve;
  re-capture is a manual off-band step (the same "silent drift off engineers'
  laptops" failure mode the project set out to kill); a captured render can be
  wrong at capture time and nobody notices.
- **Why not chosen**: The user wants the golden side to track today's *actual*
  published chart, not a frozen snapshot that can drift from reality.

### Option B: Render today's charts live in-test (chosen shape)
- **Approach**: Fetch/pull today's wrapper chart at test time and render it live,
  then diff against the operator render.
- **Pros**: Golden side always reflects the real, currently-published chart; no
  stale committed render to maintain; catches upstream chart changes immediately.
- **Cons**: Needs network egress in CI; slower than offline; render-tool version
  must be pinned or it reintroduces `helm`/`kustomize` version nondeterminism.
- **Why not chosen**: It *was* chosen — refined below into "pull the published
  OCI chart from GHCR at a pinned version" rather than "fetch git source at a
  pinned SHA and build deps", which is more immutable and more GHCR-pure.

### Option C: Full reconciler + recording Applier under envtest
- **Approach**: Run the real reconciler under envtest with a fake `deliver.Applier`
  that records every `Apply(manifest)` into seed/shoot slices instead of writing
  to a cluster; compare the recorded streams against today's chart.
- **Pros**: Exercises real reconcile ordering/gating end-to-end.
- **Cons**: Pulls in envtest + a fake shoot client + finalizer/status machinery;
  re-tests delivery orchestration that Phase 5/6 already cover; adds surface area
  for no equivalence benefit — the equivalence claim is about rendered/transformed
  output, not delivery orchestration.
- **Why not chosen**: Too heavy. Equivalence is a render+transform property; the
  pipeline can be driven directly and the two streams fall out naturally.

## Agreed Approach

**Option B, refined**: live-render the golden side by **pulling the published
wrapper chart from GHCR** (`oci://ghcr.io/sapcc/helm-charts/charts/<op>-remote`)
at a **pinned chart version** recorded per fixture, then `helm template` it. The
operator side is captured by **calling the render+transform pipeline directly**
(`source.From → Render(ModeSeed/ModeShoot) → transform.Build → Apply`), not by
running the reconciler. Both streams are normalized to a canonical form and
compared with an allowlist of expected-diff fields (deep-equal per resource). The
suite runs in the default `Test` job and gates every PR.

Rationale:
- GHCR-published charts are **versioned and immutable** — pinning by chart
  `version` is more sound than pinning a git SHA and re-building dependencies, and
  it is GHCR-pure (the operator publishes to GHCR too, per Phase 9.5; keppel only
  mirrors ghcr.io).
- Pulling the **packaged** chart (`.tgz` with dependencies vendored under
  `charts/`) avoids a transitive dependency re-fetch at render time — notably the
  wrapper's `owner-info` dependency, which is declared against
  `oci://keppel.eu-de-1.cloud.sap/ccloud-helm`. Rendering the packaged chart keeps
  the golden side from transitively pulling keppel.
- Driving the pipeline directly keeps the test focused on the equivalence property
  (rendered/transformed output) and reuses the delivery tests (Phase 5/6) for
  everything below the manifest streams.
- Comparing **resolved end-state objects** (not the delivery wrapper) is what makes
  r7's architectural differences (two disjoint direct-apply streams vs. today's
  three delivery paths) reconcilable: the golden-side unwrap decodes today's wrapped
  shoot resources back into bare objects, so we assert what ends up applied per
  target cluster, not how it got there.

## Key Decisions

- **Golden source = GHCR OCI pull at pinned chart version**: pull
  `oci://ghcr.io/sapcc/helm-charts/charts/<op>-remote --version <pinned>` (recorded
  per fixture), render with `helm template`. Rationale: immutable/versioned,
  GHCR-pure, no git-SHA + dependency-build dance.
- **Pull the packaged chart (deps vendored), option (a)**: render the published
  `.tgz` whose `charts/` already contains resolved subcharts, so the golden render
  does not transitively pull keppel for `owner-info`. Rationale: hermetic-to-GHCR;
  avoids a second registry in the golden path.
- **Live render, not committed fixtures**: golden side tracks the real published
  chart every run. Rationale: kills stale-golden silent drift; the user explicitly
  wants the golden side current.
- **Operator side = call render+transform pipeline directly**: no reconciler, no
  envtest, no fake Applier — capture the two `[]manifest.Manifest` before the
  deliver layer. Rationale: equivalence is a render/transform property; delivery
  is already covered by Phase 5/6.
- **Golden stream classified into THREE buckets, only the injector ConfigMap
  touched among ConfigMaps**: today's chart does not apply directly to the shoot and
  emits seed-side install plumbing the operator replaces. Each rendered doc goes to
  exactly one bucket:
  1. **Unwrap → bare objects**: `ManagedResource`+`Secret` pairs (CRDs/RBAC — decode
     `Secret.data["objects.yaml"]`) and the injector webhook-config `ConfigMap`
     (decode `data["webhooks.yaml"]`); discard the wrappers. Identify the injector
     ConfigMap PRECISELY by name `== <chart-fullname>-webhook-config` AND sole data
     key `webhooks.yaml` — NEVER by `kind == ConfigMap`. Other ConfigMaps are NOT
     touched.
   2. **Exclude entirely** — ONLY objects the operator render emits no equivalent for
      (decided by render-emission, NOT by name): `owner-info` subchart output, or a
      legacy delivery object with no render counterpart. Enumerated + justified per
      operator. CAUTION: do NOT exclude `remote-kubeconfig` — see bucket 3.
   3. **Keep + compare** — everything else, including every NON-injector ConfigMap
      (metal-operator `dns-record-template`, ipam-capi `manager-config`), Deployments,
      Services, NetworkPolicy, seed RBAC, AND the managed controller-manager's
      shoot-access objects (`remote-kubeconfig` ConfigMap + `<op>-remote-kubeconfig`
      Secret). The managed operator (metal/boot/…) mounts these to reach its OWN CRs
      in the shoot, and the operator's SEED render emits them (design §3.5:
      `ConfigMap/remote-kubeconfig | additions | seed`), so they are compared, not
      excluded. (The dual-deployment-operator's own `spec.shootAccess` delivery
      credential is a separate, non-rendered consumer — nothing to exclude for it.)
  Rationale: only bucket 1 + bucket 3 form the golden set that lines up 1:1 with the
  operator render; blindly discarding all ConfigMaps (or comparing the wrappers)
  would be wrong. This is the concrete mechanism behind "compare resolved end-state,
  not delivery wrapper".
- **Golden-side manipulation must be no-op-safe on a generic chart (INVARIANT)**:
  every bucketing/unwrap/exclude transform is identity-gated — it triggers ONLY on a
  positively-matched wrapper identity and is a pass-through when absent. No
  `ManagedResource` → no MR-unwrap; no injector ConfigMap → no ConfigMap unwrapped
  (verified: boot/argora/khalkeon have none); empty exclusion list → nothing
  excluded. Default for any unrecognized doc is bucket 3 (keep+compare), never silent
  drop. A plain application chart with none of these wrappers passes through
  unchanged. Rationale: the harness must be safe to point at future operators and
  generic charts without special-casing, and can never quietly delete a resource it
  failed to recognize.
- **Phase 8 does NOT re-test the transformations**: `patch` / `rewriteWebhookURL` /
  `filterKinds` are already unit-tested in Phase 3 (`internal/transform/*_test.go`).
  Phase 8 asserts whole-pipeline output equivalence. Handling of differences follows
  the three-bucket classification above plus the allowlist: (a) **delivery wrappers**
  (`ManagedResource`+`Secret`, the injector webhook-config ConfigMap) → unwrapped to
  their bare payload, wrappers discarded; (b) **objects with no operator-render
  counterpart** (e.g. `owner-info`) → excluded entirely (NOT remote-kubeconfig — that
  is emitted by the seed render and kept); (c) **provenance/incidental fields** (chart
  labels, operator-internal `origin`/`owned-by`) → allowlisted. Everything else —
  crucially the **transformation outputs** (injector `--target-label`, sidecar initContainer,
  rewritten webhook URL, dropped Services) → MUST match, never stripped. Reproducing
  them IS the equivalence claim; a mismatch is a real finding.
- **Comparison = canonical normalize + allowlisted ignores, deep-equal per
  resource**: parse YAML → sort by GVK+namespace+name → drop
  comments/whitespace/ordering → strip an allowlist of ONLY provenance/incidental
  fields (chart-provenance labels `helm.sh/chart`, `app.kubernetes.io/managed-by`,
  `app.kubernetes.io/version`; operator-internal `owned-by`/`origin`) → deep-equal.
  The r7 `caBundle` leaf is belt-and-suspenders only (both sides render it absent —
  today's `webhooks.yaml` has no caBundle, injector fills it at runtime; operator
  omits it per §3.8). Rationale: strong equivalence guarantee while tolerating only
  known-incidental differences; NEVER allowlist a transformation output. Residual
  mismatches reported per resource.
- **r7 differences handled by unwrap + comparing resolved objects, not delivery
  shape**: the golden-side unwrap turns today's wrapped shoot stream into the same
  bare objects the operator applies, so WebhookConfigs compare as bare objects (both
  caBundle-absent) and Roles stay Roles (no `renameKind`, already match). Rationale:
  the equivalence claim is about applied end-state per target cluster.
- **CI = default `Test` job, gates every PR**: ordinary `go test ./...`, no build
  tag / env gate, matching `design.md` §7.3 ("run in CI on every operator PR").
  Requires CI egress to GHCR (golden) + keppel/github (operator source fetch, both
  already proven in Phase 7). Rationale: strict per-PR gating is the point of an
  equivalence test.
- **Split-by-destination for the golden stream**: today's single rendered stream
  is partitioned into seed-destined vs shoot-destined sets using the same signals
  today's chart uses (committed `managedresources/*` + `webhooks.yaml` → shoot;
  `controller-manager.yaml` + chart templates → seed), so it can be compared
  against the operator's two renders.
- **Package layout = new `internal/equivalence/` package**: holds the GHCR
  pull+render golden helper, the operator-pipeline capture helper, the canonical
  normalizer + comparator, and the allowlist. Normalizer/comparator are pure
  functions with their own offline unit tests so the equivalence machinery is
  verified independent of network.

## Open Questions

All four brainstorm-time open questions were investigated against the live
`sapcc/helm-charts` repo and this codebase (2026-07). Resolutions below; only the
last two carry residual implementation work.

- [x] **Exact pinned chart versions per operator** — RESOLVED. Current published
  `<op>-remote` chart versions (from each `Chart.yaml` on `master`):
  `metal-operator-remote` `0.6.30`, `boot-operator-remote` `0.4.21`,
  `argora-operator-remote` `0.0.60`, `khalkeon-remote` `0.1.1`,
  `ipam-capi-remote` `1.2.31`. Record these per fixture; a pin bump is a
  deliberate one-line fixture edit.
- [x] **ipam-capi golden render mechanism** — RESOLVED: uniform `helm template`.
  `ipam-capi-remote` is a normal Helm chart (`apiVersion: v2`, `type: application`)
  with the same layout as the others; ipam-capi's kustomize-ness is entirely
  upstream and already pre-rendered/committed into `managedresources/*.yaml` by
  today's `make build-ipam-capi-remote`. So all five golden renders use one
  mechanism (`helm pull` + `helm template`); no `kubectl kustomize` path is needed
  on the golden side. NOTE: this makes ipam-capi the **highest-signal** comparison —
  operator side renders live from the real upstream kustomize source (github),
  golden side is a frozen helmify snapshot, so it is the case most likely to
  surface drift.
- [~] **Complete field allowlist** — APPROACH + STARTER LIST RESOLVED; finalize by
  run-and-triage. Because the operator stream is captured BEFORE the deliver layer,
  the comparator must strip:
  - operator-internal (verified keys in code): `dual-deployment-operator.cc.sap/origin`
    annotation (stripped at apply by `StripInternalAnnotations`, but present
    pre-deliver → strip in comparator); `dual-deployment-operator.cc.sap/owned-by`
    label (stamped at apply by `SetOwnedByLabel`, absent pre-deliver → include
    defensively, harmless);
  - chart-provenance labels only today's Helm render carries:
    `helm.sh/chart`, `app.kubernetes.io/managed-by`, `app.kubernetes.io/version`
    (+ `owner-info` chart labels);
  - the r7 `caBundle` leaf (operator omits it; injector owns it — §3.8).
  Residual: the first real comparator run will reveal expected artifacts no upfront
  reasoning catches (e.g. Helm `checksum/config` annotations, namespace injection
  from `shootNamespace`). Complete the allowlist by running once, reading the
  residual per-resource diff, and adding each provably-expected field with a
  one-line justification. — owner: implementer (finalize during plan/apply)
- [~] **Per-operator representative shoot values / CR fixtures** — APPROACH
  RESOLVED; authoring is the phase's main work. Each `-remote` chart's own
  `values.yaml` IS the representative config (e.g. metal-operator's shows
  `webhook.enable: true`, the `webhookInjector` image, `metal-operator-core`
  subchart toggles with upstream disabled — the design's exact pattern). CRITICAL
  constraint: both sides must render from the SAME inputs, so the fixture CR's
  `values`/`seedValues`/`shootValues` (Helm) or `patch` transforms (ipam-capi
  kustomize) must reproduce the same effective input the wrapper chart's
  `values.yaml` feeds its render — otherwise every diff is configuration noise, not
  drift. Build each fixture as a PAIR derived from one source of truth (the
  published chart's `values.yaml`). Start with metal-operator (richest: sidecar
  patch, rewriteWebhookURL, injector label, filterKinds) to prove pair-derivation,
  then replicate for the other four. — owner: implementer
