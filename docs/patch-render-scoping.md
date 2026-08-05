# dual-deployment-operator — Per-Render Patch Scoping (no-op on zero-match)

**Status:** Implemented — a small `internal/transform` change. Implemented shape: a zero-match `patch` is a clean, **silent no-op** (return the input manifests unchanged, no error), consistent with `rewriteWebhookURL` and `filterKinds`. No controller change, no cross-render backstop, no `TransformNoMatch` status/event (simpler than the "error if nothing matches anywhere" form described in §3–§4 below, which was the original proposal). ~3-line deletion of the fail-loud guard in `internal/transform/patch.go`. Unblocks production webhooks for shoot-only-object charts (`metal-operator-remote-v2`).
**Scope (as shipped):** `internal/transform/patch.go` only — no controller change, no CRD change, no source/loader/delivery change. (The original proposal in §3–§4 below also touched the controller transform loop for a cross-render backstop; that backstop was dropped — see the Status note above.)
**Motivating consumer:** [`sapcc/helm-charts` `system/metal-operator-remote-v2`](https://github.com/sapcc/helm-charts/tree/master/system/metal-operator-remote-v2) — needs a `patch` to stamp the webhook-injector target-label on a shoot-only `ValidatingWebhookConfiguration`.
**Related design:** see `design.md` §3.4.4 (labeling webhook objects for the injector), §3.8 (injector coexistence); `context.md` "Revision 10".

---

## TL;DR

**Before this change:** the `patch` transform was **fail-loud on zero matches** and every transform runs against **both** renders (seed and shoot) independently. So a `patch` whose target kind exists in only ONE render (e.g. a `ValidatingWebhookConfiguration`, which is shoot-only) **errored on the other render** and failed the whole reconcile — even though the patch is correct and matched in the render it was meant for.

This blocks the r7 pattern the design itself prescribes (§3.4.4): "the injector label is added with the existing `patch` transformation … stamped by the chart on the upstream webhook objects." A chart that lets the upstream subchart emit the VWC (shoot render) and uses a CR `patch` to add the injector `--target-label` cannot work today: the patch matches the VWC in the shoot render but finds zero matches in the seed render → `SeedTransformFailed`.

This change makes a `patch` that matches nothing in a render a **clean, silent no-op** on that render, instead of erroring. **As shipped**, a zero-match is a silent no-op in every case (one render or both) — consistent with `rewriteWebhookURL` and `filterKinds`; it is a ~3-line deletion of the fail-loud guard in `internal/transform/patch.go`, with no controller change. (The original proposal below (§3–§4) additionally kept a cross-render "matched nothing anywhere" backstop in the controller; that was dropped for simplicity and cross-transform consistency — see the Status note at the top.)

---

## 1. Background

### 1.1 How transforms run today

`internal/controller/dualdeploymentoperator_controller.go` applies each transform to both renders independently, in declaration order:

```go
// 4. Apply each transformation to both renders independently, in declaration order.
for _, t := range transforms {
    seedManifests, err = t.Apply(seedManifests)
    if err != nil {
        return r.errStatus(ctx, cr, "SeedTransformFailed", err)
    }
    shootManifests, err = t.Apply(shootManifests)
    if err != nil {
        return r.errStatus(ctx, cr, "ShootTransformFailed", err)
    }
}
```

And `internal/transform/patch.go` `Apply` is fail-loud on zero matches:

```go
matched := 0
for i, m := range manifests {
    if !Match(m, p.spec.Target) { out[i] = m; continue }
    matched++
    // … apply …
}
if matched == 0 {
    return nil, errors.New("no matching resource for patch target selector")
}
```

The zero-match guard is deliberate and valuable — it catches a typo'd target selector that would otherwise silently do nothing. But combined with "apply to both renders," it makes any patch targeting a single-render kind unusable: the render that lacks the kind fails.

### 1.2 Why this matters for metal-operator-remote-v2

Per r7 (design.md §3.4.4/§3.8), webhook configs are emitted by the render and the operator applies them caBundle-unset; the webhook-injector (target-patch mode) then patches caBundle only on objects carrying its `--target-label`. The chart's job is to get that label onto the VWC. For `-v2` the VWC comes from the upstream `metal-operator` subchart (`shootValues.webhook.enable=true`), which exposes no values hook to add a custom label, and a Helm wrapper cannot edit a subchart's rendered output. So the label must be added by a CR patch transform — exactly what §3.4.4 says. But that patch targets a `ValidatingWebhookConfiguration`, which exists only in the shoot render, so it errors on the seed render today.

Net: the design's own prescribed mechanism (§3.4.4) is not executable with the current per-render fail-loud patch. `-v2` webhooks are non-functional until this is fixed.

## 2. Problem

`patch` cannot target a kind that exists in only one of the two renders. Because transforms are applied per-render and `patch` errors on zero matches, a shoot-only (or seed-only) target always fails on the other render. This directly contradicts design.md §3.4.4, which assumes a patch can stamp the injector label on webhook objects.

## 3. Proposed change

> **As shipped (see Status at top):** form (A) below was simplified — a zero-match `patch` is a clean, silent no-op with **no** cross-render backstop and **no** controller change. The "matched nothing anywhere → error" check described in (A) was dropped so `patch` behaves identically to `rewriteWebhookURL`/`filterKinds`. §3–§4 record the original proposal for historical context.

Preserve the "catch a typo'd selector" value of the zero-match guard, but scope it to the whole transform application rather than a single render. Two viable forms — the first is preferred:

**(A) No-op on per-render zero-match; error only if a patch matches nothing anywhere (preferred, no CRD change).** Change per-render `patch.Apply` so a zero-match render is a clean no-op (return the manifests unchanged, no error). Move the "matched nothing → error" check up to the controller: after applying a transform to BOTH renders, if it matched zero objects across both, surface an `InvalidTransformation`/`TransformNoMatch` status. This keeps the typo protection (a patch that matches nothing in either render still errors) while letting a legitimately single-render-scoped patch succeed. Requires `Apply` to report whether it matched (e.g. return a matched count / bool), and a small controller aggregation across the two `Apply` calls. ~10–20 LOC in `internal/transform/patch.go` + the controller transform loop; no CRD change.

**(B) Optional explicit render scope on the transform (CRD change).** Add an optional `scope: seed|shoot|both` (default `both`) to the transformation entry; the controller skips applying a transform to a render outside its scope. More explicit and self-documenting, but adds a CRD field and CR authoring surface. Prefer (A) unless an explicit scope is independently desired.

Either form makes the `-v2` webhook-label patch valid: it matches the VWC in the shoot render and no-ops (A) or is skipped (B) on the seed render.

## 4. Risks

1. **Weakening the typo guard.** Form (A) must keep the "matched nothing in ANY render → error" backstop, or a mistargeted patch silently does nothing. The check moves from per-render to per-transform; it must not be dropped. Form (B) keeps the per-render guard but only within the declared scope.
2. **Ordering interaction.** Transforms apply in declaration order; a no-op on one render must still pass the (unchanged) manifests through so later transforms see them. Form (A) returns the input unchanged on zero-match, preserving order — verify no transform assumes a prior one mutated a render.
3. **Kustomize path.** The kustomize source produces its own two renders via overlays; the same transform loop applies. The change is in the shared transform/controller layer, so it benefits both source types uniformly — confirm the kustomize render path exercises the same `Apply` contract.

None of these change the delivery or source layers.

## 5. Decision framing

> **As shipped (see Status at top):** form (A) was adopted but *simplified* — a zero-match `patch` is always a clean, silent no-op, with **no** controller backstop and **no** `TransformNoMatch` error (not "error on match-nothing-anywhere"). Form (B) (`scope:` CRD field) was deferred as non-breaking future work. The bullets below record the original decision framing.

- Proposed as a small `internal/transform` change, prerequisite for any chart that patches a single-render-only kind — starting with `-v2`'s webhook-injector label. It is not deferrable if `-v2` webhooks are to function.
- Prefer form (A) (no CRD change) unless an explicit scope field is independently wanted. **(Shipped: form (A) with no backstop — zero-match is always a silent no-op, consistent with `rewriteWebhookURL`/`filterKinds`.)**
- Sequencing vs. the helm-charts side: this operator change lands before `-v2` is cut over on a live cluster. `-v2` ships the CR patch transform with a comment that it requires this enhancement; a cluster on the un-enhanced operator must not enable the CR (the reconcile would fail `SeedTransformFailed`).

Rationale and evolution are recorded in `context.md` Revision 10.
