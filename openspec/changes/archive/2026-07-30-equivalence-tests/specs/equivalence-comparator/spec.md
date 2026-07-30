## ADDED Requirements

### Requirement: Scoped equivalence over delivered kinds with known divergences

The equivalence assertion SHALL be SCOPED, not full-object-set equality. The
operator renders the UPSTREAM chart directly while today's golden side renders the
`<operator>-remote` WRAPPER chart (which disables most of upstream and substitutes
pre-rendered `managedresources/*` plus sapcc additions); the two therefore
legitimately emit different object sets. The comparator MUST support:

- a per-fixture set of **compared kinds** — the delivered resource kinds both sides
  are expected to produce (e.g. `CustomResourceDefinition`, `ClusterRole`,
  `ClusterRoleBinding`, `Role`, `RoleBinding`, `ServiceAccount`,
  `ValidatingWebhookConfiguration`, `MutatingWebhookConfiguration`). Resources whose
  kind is not in this set are ignored by the comparison; and
- a per-fixture **known-divergence** list of specific objects (by kind + name) that
  are expected on exactly one side and MUST NOT be reported as missing/extra. Each
  entry MUST carry a justification in the fixture.

Within the compared-kinds scope and after removing known divergences, the comparator
MUST still perform full per-resource deep-equal (see the per-resource requirement)
and MUST report any residual mismatch, missing, or extra as a failure. Scoping
narrows WHICH objects are compared; it MUST NOT weaken the field-level rigor applied
to the objects that ARE in scope, and MUST NOT be used to hide a transformation-output
difference.

#### Scenario: Out-of-scope kinds are ignored

- **WHEN** the golden or operator render contains a resource whose kind is not in the
  fixture's compared-kinds set (e.g. a cert-manager `Issuer` the upstream chart emits
  but the wrapper does not)
- **THEN** the comparator neither compares it nor reports it as missing/extra

#### Scenario: Known divergence is not reported

- **WHEN** an object listed in the fixture's known-divergence list (by kind + name)
  is present on only one side
- **THEN** the comparator does not report it as missing or extra

#### Scenario: In-scope objects still compared with full rigor

- **WHEN** a resource is of a compared kind and is not a known divergence
- **THEN** the comparator applies full per-resource deep-equal (allowlist for
  provenance only; transformation outputs are never suppressed) and fails on any
  residual difference

#### Scenario: Known-divergence entry cannot hide an in-scope mismatch

- **WHEN** a fixture attempts to use a known-divergence entry to suppress a
  field-level difference on an object that exists on BOTH sides
- **THEN** the entry has no effect (known-divergence only suppresses missing/extra
  for single-sided objects); the field mismatch is still reported

---

### Requirement: Canonical normalization and per-resource comparison

The equivalence comparator SHALL compare the golden set (after unwrap/exclude) and
the operator render on a per-resource basis after normalizing both sides to a
canonical form: parse each document to a structured object, key resources by
group/kind/namespace/name, and eliminate incidental ordering, comment, and
whitespace differences. For each resource key present on either side, the comparator
MUST assert the two objects are deep-equal after normalization, and MUST report every
residual mismatch identified by group/kind/namespace/name and the differing field
paths.

#### Scenario: Matching resources compare equal

- **WHEN** the golden set and the operator render contain the same resource
  (group/kind/namespace/name) and it differs only in ordering, comments, or
  whitespace
- **THEN** the comparator treats them as equivalent

#### Scenario: A field difference is reported per resource

- **WHEN** a resource present on both sides differs in a non-allowlisted field after
  normalization
- **THEN** the comparator reports a mismatch naming the resource
  (group/kind/namespace/name) and the differing field path(s), and the test FAILS

#### Scenario: A resource present on only one side is reported

- **WHEN** a resource key exists in the golden set but not the operator render (or
  vice versa) after unwrap/exclude
- **THEN** the comparator reports the resource as missing/extra on the respective
  side, and the test FAILS

---

### Requirement: Allowlist limited to provenance and incidental fields

The comparator SHALL strip, before deep-equal, only an allowlist of
provenance/incidental fields that are expected to differ between a Helm-rendered
chart and the operator's render. The allowlist MUST include chart-provenance labels
(`helm.sh/chart`, `app.kubernetes.io/managed-by`, `app.kubernetes.io/version`, and
`owner-info` chart labels) and the operator-internal
`dual-deployment-operator.cc.sap/origin` and
`dual-deployment-operator.cc.sap/owned-by` labels/annotations. The allowlist MUST NOT
include any field that is a transformation output.

#### Scenario: Provenance labels are ignored

- **WHEN** two matched resources differ only in `helm.sh/chart` or
  `app.kubernetes.io/managed-by`
- **THEN** the comparator treats them as equivalent

#### Scenario: Operator-internal labels are ignored

- **WHEN** the operator resource carries `dual-deployment-operator.cc.sap/origin` or
  `dual-deployment-operator.cc.sap/owned-by` and the golden resource does not
- **THEN** the comparator treats them as equivalent

#### Scenario: A transformation output is never allowlisted

- **WHEN** two matched resources differ in a field produced by a transformation (for
  example an injector `--target-label`, an injected sidecar initContainer, a
  rewritten webhook URL, or a resource dropped by `filterKinds`)
- **THEN** the comparator reports the difference as a mismatch (it MUST NOT be
  suppressed by the allowlist)

---

### Requirement: caBundle parity without special handling

The comparator SHALL treat the webhook `caBundle` field as naturally equal because
both sides render it absent — today's chart embeds a `webhooks.yaml` with no
`caBundle` (the injector fills it at runtime) and the operator applies webhook
objects with `caBundle` unset. Any allowlisting of `caBundle` MUST be belt-and-braces
only and MUST NOT mask a real difference in any other field.

#### Scenario: caBundle-absent webhook objects compare equal

- **WHEN** a WebhookConfiguration (or conversion-webhook CRD) appears on both sides
  with no `caBundle` set
- **THEN** the comparator treats the two objects as equivalent with respect to
  `caBundle`

---

### Requirement: Per-operator equivalence subtests gate CI

The equivalence suite SHALL run one subtest per operator (`metal-operator`,
`boot-operator`, `argora-operator`, `khalkeon`, `ipam-capi`), each opt-in behind the
`RUN_EQUIVALENCE` environment variable. When `RUN_EQUIVALENCE` is set, every subtest
runs and a failure fails the run; when it is unset the suite skips. The gate is required
because the golden render runs `helm dependency build`, which pulls subcharts from the
internal-only keppel OCI registry that public CI runners cannot reach — so the suite gates
CI wherever that registry is reachable (local dev, internal runners). The comparator's
normalization and allowlist logic MUST additionally have offline unit tests that verify the
machinery independent of network access (these always run).

#### Scenario: Each operator runs as its own gating subtest

- **WHEN** the equivalence suite runs with `RUN_EQUIVALENCE` set
- **THEN** there is one subtest per operator, each executes and a failure in any subtest
  fails the run
- **AND WHEN** `RUN_EQUIVALENCE` is unset, the suite skips instead of failing

#### Scenario: Comparator machinery is unit-tested offline

- **WHEN** the normalization and allowlist logic is tested
- **THEN** there are offline unit tests (no network) that verify canonicalization,
  allowlist stripping, and mismatch reporting behavior
