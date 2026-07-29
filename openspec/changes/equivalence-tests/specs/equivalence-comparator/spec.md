## ADDED Requirements

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
`boot-operator`, `argora-operator`, `khalkeon`, `ipam-capi`) as ordinary tests in the
default test job — no build tag or environment gate — so equivalence gates every PR.
The comparator's normalization and allowlist logic MUST additionally have offline
unit tests that verify the machinery independent of network access.

#### Scenario: Each operator runs as its own gating subtest

- **WHEN** the equivalence suite runs in CI
- **THEN** there is one subtest per operator, each executes in the default test job
  without a build tag or env gate, and a failure in any subtest fails the PR

#### Scenario: Comparator machinery is unit-tested offline

- **WHEN** the normalization and allowlist logic is tested
- **THEN** there are offline unit tests (no network) that verify canonicalization,
  allowlist stripping, and mismatch reporting behavior
