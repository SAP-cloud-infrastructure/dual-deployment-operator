## ADDED Requirements

### Requirement: Golden chart render from GHCR

The equivalence harness SHALL produce the "golden" (today's chart) manifest stream
for an operator by pulling the published `<operator>-remote` wrapper chart from GHCR
(`oci://ghcr.io/sapcc/helm-charts/charts/<operator>-remote`) at a chart version
pinned per fixture, and rendering it with `helm template` supplied with the
per-operator **value baseline**: the wrapper chart's own `values.yaml` plus one
named, representative per-cluster overlay (the `cc/kube-secrets`-shaped values for a
single chosen shoot), recorded in the fixture. The pull MUST use the packaged chart
(subchart dependencies vendored) so the golden render does not transitively pull any
other registry (e.g. keppel for the `owner-info` dependency).

#### Scenario: Pull and render a pinned wrapper chart with the value baseline

- **WHEN** the harness renders the golden stream for an operator whose fixture pins
  chart version `V` and a representative per-cluster overlay
- **THEN** it pulls `oci://ghcr.io/sapcc/helm-charts/charts/<operator>-remote` at
  version `V`, runs `helm template` with the wrapper `values.yaml` plus the overlay,
  and returns the rendered multi-document stream

#### Scenario: Per-cluster placeholders resolve via the overlay

- **WHEN** the wrapper `values.yaml` carries a per-cluster placeholder (e.g. an
  apiserver host) that the deploy-time overlay fills
- **THEN** the golden render uses the overlay value so the rendered object matches
  the same value carried into the operator render, rather than an unresolved
  placeholder

#### Scenario: Missing or unreachable chart fails loudly

- **WHEN** the pinned chart version is absent from GHCR, or GHCR is unreachable
- **THEN** the harness returns an error naming the chart and version, and the test
  FAILS (it MUST NOT skip, silently pass, or fall back to a stale render)

#### Scenario: Golden render is registry-isolated to GHCR

- **WHEN** rendering the packaged chart
- **THEN** no transitive pull to a non-GHCR registry is required to complete the
  render (subchart dependencies are already vendored in the packaged chart)

---

### Requirement: Seed/shoot classification of the golden stream

The harness SHALL partition the golden render into a seed-destined set and a
shoot-destined set using the delivery signals today's chart uses: content under the
chart's `managedresources/*` and its `webhooks.yaml` is shoot-destined; the
controller-manager template and the chart's other application templates are
seed-destined.

#### Scenario: Shoot-destined content is separated from seed-destined content

- **WHEN** the harness classifies a rendered golden stream
- **THEN** ManagedResource-wrapped `managedresources/*` content and the injector
  webhook-config content are placed in the shoot set, and the controller-manager
  and other application resources are placed in the seed set

---

### Requirement: Golden shoot-stream unwrapping to bare objects

Because today's chart delivers shoot resources through wrapper objects rather than
applying them directly, the harness SHALL unwrap those wrappers into the bare
Kubernetes objects the operator applies directly, so the golden shoot set is
comparable object-for-object with the operator's shoot render. Specifically:

- For each `ManagedResource`, the harness MUST follow `spec.secretRefs` to the
  paired `Secret`, base64-decode `data["objects.yaml"]`, and emit the contained
  document(s) as bare objects, discarding the `ManagedResource` and its `Secret`.
- For the injector webhook-config `ConfigMap`, the harness MUST decode
  `data["webhooks.yaml"]` into bare WebhookConfiguration object(s), discarding the
  `ConfigMap`.

The injector `ConfigMap` MUST be identified precisely by its stable identity — name
equal to `<chart-fullname>-webhook-config` AND a sole data key `webhooks.yaml` — and
MUST NOT be identified by `kind == ConfigMap` alone.

#### Scenario: ManagedResource-wrapped CRDs/RBAC are unwrapped

- **WHEN** the golden shoot set contains a `ManagedResource` referencing a `Secret`
  whose `data["objects.yaml"]` holds a CRD document
- **THEN** the harness emits the bare CRD object and removes the `ManagedResource`
  and the `Secret` from the golden set

#### Scenario: Injector ConfigMap is unwrapped into bare WebhookConfigurations

- **WHEN** the golden shoot set contains a `ConfigMap` named
  `<chart-fullname>-webhook-config` whose sole data key is `webhooks.yaml`
- **THEN** the harness emits the bare WebhookConfiguration object(s) decoded from
  that key and removes the `ConfigMap` from the golden set

#### Scenario: Non-injector ConfigMaps are never unwrapped

- **WHEN** the golden stream contains a `ConfigMap` that is not named
  `<chart-fullname>-webhook-config` or whose data keys are not exactly
  `{webhooks.yaml}` (for example an application config ConfigMap)
- **THEN** that `ConfigMap` is left intact and passes through to the keep-and-compare
  set unchanged

---

### Requirement: Enumerated exclusion of replaced delivery plumbing

The harness SHALL exclude from the golden set ONLY the seed-side plumbing that the
operator's own render does not emit, using an exclusion list enumerated per operator
in the fixture. Exclusion MUST be decided by whether the operator's render emits an
equivalent object, NOT by the object's name.

CRITICAL — the `<operator>-remote-kubeconfig` Secret and the `remote-kubeconfig`
ConfigMap serve TWO distinct consumers and MUST NOT be blanket-excluded:

- The **managed controller-manager** (e.g. metal-operator, boot-operator) runs in the
  seed and mounts the token-requestor Secret `<operator>-remote-kubeconfig` plus the
  `remote-kubeconfig` kubeconfig ConfigMap to reach and reconcile ITS OWN CRs in the
  shoot (`KUBECONFIG=/var/run/remote-kubeconfig/kubeconfig`). The operator's SEED
  render emits this controller-manager Deployment AND these access objects (design
  §3.5: `ConfigMap/remote-kubeconfig` and the Deployment's Secret mount are seed-render
  outputs). These objects, and the Deployment's volume/env references to them, are
  therefore part of the seed render and MUST be KEPT and compared — they are NOT
  excluded.
- The **dual-deployment-operator itself** separately consumes a token-requestor Secret
  via `spec.shootAccess` to DELIVER resources to the shoot; that consumption is not a
  rendered object and does not appear in either render, so there is nothing to exclude
  for it.

Only objects the operator's render genuinely does not emit are excluded (e.g. the
`owner-info` subchart output, or any legacy delivery object with no seed/shoot-render
counterpart). Each exclusion entry MUST be justified in the fixture by "the operator
render emits no equivalent", MUST identify the object by kind + name, and MUST NOT be
based on a name substring.

#### Scenario: Access objects the managed controller-manager mounts are kept and compared

- **WHEN** the golden seed set contains the `remote-kubeconfig` ConfigMap and/or the
  `<operator>-remote-kubeconfig` Secret that the managed controller-manager Deployment
  mounts to reach its CRs in the shoot
- **THEN** these objects are retained in the keep-and-compare set (NOT excluded),
  because the operator's seed render emits equivalents, and the controller-manager
  Deployment's volume/env references to them are compared

#### Scenario: An object with no operator-render counterpart is excluded by kind+name

- **WHEN** the golden render contains an object the operator's render emits no
  equivalent for (e.g. the `owner-info` subchart output), listed in the fixture's
  exclusion list by kind + name with a justification
- **THEN** that object is removed from the golden set and does not appear as a residual
  difference

#### Scenario: Exclusion never uses a name-substring match

- **WHEN** two objects share a base name but differ in kind or role, and only one has
  no operator-render counterpart
- **THEN** only the object explicitly enumerated by its own kind+name is excluded; a
  name-substring match MUST NOT sweep in a same-named object that IS emitted by the
  operator render

#### Scenario: Exclusion requires an explicit enumerated match

- **WHEN** the harness encounters a rendered document that is neither a recognized
  wrapper nor listed in the exclusion list
- **THEN** the document is retained in the keep-and-compare set (it is NOT excluded)

---

### Requirement: Identity-gated, no-op-safe golden manipulation

Every golden-side manipulation (unwrap, exclude, classify) SHALL be identity-gated:
it triggers only on a positively matched identity and is a pass-through when its
trigger is absent. A chart lacking a given wrapper MUST render its documents through
the pipeline unchanged. The default disposition for any unrecognized document MUST be
keep-and-compare; the harness MUST NOT silently drop a document it fails to recognize.

#### Scenario: Chart without an injector ConfigMap is unaffected

- **WHEN** the harness processes an operator chart that has no injector webhook-config
  `ConfigMap` (for example boot-operator, argora-operator, or khalkeon)
- **THEN** no ConfigMap is unwrapped and every ConfigMap in the render passes through
  to the keep-and-compare set unchanged

#### Scenario: Generic chart with no wrappers passes through unchanged

- **WHEN** the harness processes a chart that contains no `ManagedResource`, no
  injector `ConfigMap`, and an empty exclusion list
- **THEN** every rendered document lands in the keep-and-compare set unchanged and
  none is dropped
