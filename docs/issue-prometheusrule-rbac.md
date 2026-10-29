
---

# Addendum (2026-10-29): two more gaps surfaced by live validation on argora

After applying the PrometheusRule RBAC fix above (seed) AND the operand-side fix that
makes the controller run, a full reconcile on a-qa-de-200 surfaced two further
operator-side issues. Both are independent of the seed PrometheusRule RBAC fix.

## Gap 2 — shoot-applier cannot deliver operand CR *instances*

The operator applies **shoot** renders with a distinct SA
`dual-deployment-operator-shoot-applier` (ClusterRole of the same name on the shoot).
That ClusterRole is a **fixed allowlist**: `apiextensions/customresourcedefinitions`,
`rbac.authorization.k8s.io/*`, `admissionregistration/*webhookconfigurations`, and core
`serviceaccounts;configmaps;secrets;services;namespaces`.

It does **not** cover operand-specific **CR instances**. argora delivers data CRs to the
shoot (`argora.cloud.sap/{clusterimports,ippoolimports,updates}`); the applier installs
the CRDs fine but cannot create the instances:

```
clusterimports.argora.cloud.sap "mgmt-qa-de-200" is forbidden:
User "system:serviceaccount:kube-system:dual-deployment-operator-shoot-applier"
cannot patch resource "clusterimports" in API group "argora.cloud.sap" ...
```

argora is the first operand to push CR *instances* through the shoot render, so it is the
first to hit this. metal/boot deliver only CRDs+RBAC+webhooks to the shoot.

**Options** (needs a design decision, not a one-line marker — the operand CRD groups are
not known to the operator a priori):
- Let the operand's shoot render carry a ClusterRole/Binding that grants the
  shoot-applier SA the operand CRD-group verbs (self-describing; the render already
  creates RBAC, and the applier already has rbac `bind;escalate`). Cleanest — no operator
  change, each operand declares what its shoot-applier needs.
- Or have the operator derive/extend the shoot-applier grant from the CRD groups present
  in the shoot render.

Validated live by patching the shoot-applier ClusterRole with
`argora.cloud.sap/{clusterimports,ippoolimports,updates}` verbs → the 3 data CRs applied
and went Ready.

## Gap 3 — `prune()` cannot clear an orphan whose kind no longer exists on the target

`internal/controller/dualdeploymentoperator_controller.go` `prune()` (~L669): for each
previously-applied resource no longer in the current render, it calls
`applier.Get(ctx, m)`. If the orphan's **kind does not exist on the target** (no CRD / no
REST mapping), Get returns a *"no matches for kind"* error, which is **not**
`apierrors.IsNotFound`. The code then does `errs = append(errs, err); retained = append(retained, rs)`
— so the stale resource is retained in status and the CR stays `Ready=False (ResourcesDegraded)`
**forever**, even though the object never existed on the target.

Repro: argora's PrometheusRule was (wrongly) rendered to the shoot, which has no
Prometheus-operator CRDs. After fixing the render to make it seed-only
(`shootValues.prometheusRules.create: false`), the orphan could not be pruned — the shoot
has no `PrometheusRule` kind to Get/Delete — so the CR stayed Degraded on a phantom status
entry until the entry was removed by hand via the status subresource.

**Fix:** in `prune()`, treat a missing REST mapping / "no matches for kind" from `Get`
(and `Delete`) the same as `IsNotFound` — there is nothing to delete, so `continue`
(drop the orphan from status) instead of appending to errs+retained. Use
`meta.IsNoMatchError(err)` (k8s.io/apimachinery/pkg/api/meta) alongside the existing
`apierrors.IsNotFound(err)` check.

Validated live: removing the phantom status entry (status subresource) + a reconcile →
CR went `Ready=True`. With this fix it would self-heal.

---

## Resolution status (branch `feat/monitoring-crd-rbac`, PR #57)

- **Gap 1 (seed PrometheusRule RBAC)** — **FIXED in operator.** Added a
  `+kubebuilder:rbac` marker for `monitoring.coreos.com`
  (`prometheusrules;servicemonitors;podmonitors`); regenerated `config/rbac/role.yaml`
  and synced `chart/templates/rbac/dual-deployment-operator.yaml`.
- **Gap 2 (shoot-applier cannot deliver operand CR instances)** — **DOCUMENTED as an
  operand-side contract** (no operator code change). `docs/design.md` §3.6.7 now states:
  an operand whose shoot render carries CR *instances* must include, in that same render,
  a `ClusterRole`/`ClusterRoleBinding` granting the `dual-deployment-operator-shoot-applier`
  SA the verbs on its CRD group(s). Self-describing; the applier already holds
  `rbac …bind;escalate`. Operator-side derivation of the grant from render CRD groups is a
  deferred future enhancement.
- **Gap 3 (`prune()` cannot clear an orphan whose kind has no REST mapping)** —
  **FIXED in operator.** `prune()` now treats `meta.IsNoMatchError(err)` from `Get` the
  same as `apierrors.IsNotFound(err)` (nothing to delete → drop the orphan from status),
  so a phantom entry self-heals instead of pinning the CR `Degraded` forever. Covered by
  unit tests in `internal/controller/prune_nomatch_test.go`.
