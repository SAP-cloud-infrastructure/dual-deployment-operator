# ipam-capi equivalence fixture

This is the fifth equivalence fixture in the suite. It follows the **exact same
structure** as the four Helm-source fixtures (metal-operator, boot-operator,
argora-operator, khalkeon), with one deliberate deviation noted below.

Golden side: `git clone` + `helm dependency build` + `helm template system/ipam-capi-remote/`
at the pinned SHA `33e68278`, then `UnwrapManagedResources` + `ClassifyGolden` — identical
to how `TestEquivalence` works for every other operator.

Operator side: `source.From` → `Render` (KustomizeSource, `shootPath: managedresources`) →
`transform.Build` — the real production render pipeline.

SHA is `33e68278ace26fe23e0c2b36a9266a58c9b47707` — the same commit pinned by all
four existing fixtures.

## Deliberate deviation: shoot-only capture

`CaptureShootOnly` is used instead of `CaptureOperator` (the main `TestEquivalence` loop
uses `CaptureOperator` for both seed+shoot). Only the shoot comparison is made; seed is
not rendered.

### Why the seed render is skipped

The `seed/manager` overlay at this SHA uses **repo-root-relative patch paths**:

```yaml
# system/kustomize/ipam-capi-remote/manager/kustomization.yaml
patches:
  - path: kustomize/ipam-capi-remote/manager/some-patch.yaml
```

These resolve when kustomize builds from the **repository root** (as `make build-ipam-capi-remote`
does), but the operator's `KustomizeSource` builds from the **overlay subdir**
(`system/kustomize/ipam-capi-remote/seed`), so the relative paths escape to a location that
doesn't exist there, and the build fails with `no such file`.

Closing this requires extending `KustomizeSource` so the clone root and the kustomize-build
directory can differ — a CRD + source change (design §9.4) explicitly out of scope here.

See **`docs/implementation.md`** → *"`ipam-capi` equivalence is DEFERRED to a standalone
future follow-up"* for the complete deferral rationale.

The `managedresources/` (shoot) overlay **is** self-contained and renders today — it
directly references upstream without any repo-root-relative paths. That is what this
fixture covers.

## Network requirements

This test is gated on `RUN_EQUIVALENCE=1` because it requires outbound network access:

- **Golden side (Helm)**: git clone of `github.com/sapcc/helm-charts` + `helm dependency build`
  pulling subcharts from the internal keppel OCI registry — same requirement as the other
  four Helm fixtures.
- **Operator side (kustomize)**: git clone of `github.com/sapcc/helm-charts` (same SHA) +
  krusty fetching the `managedresources/` overlay's remote bases from `github.com` and
  `raw.githubusercontent.com` (`cluster-api-ipam-provider-in-cluster ?ref=v1.1.0`,
  cluster-api CRD bases `v1.13.4`). All kustomize sources are public/anonymous.

## Files

| File | Purpose |
|------|---------|
| `fixture.yaml` | Pinned SHA, chart/kustomize paths, scope, known divergences |
| `cr.yaml` | `DualDeploymentOperator` CR with KustomizeSource, `shootPath: managedresources` |
| `overlay-values.yaml` | Per-cluster Helm overlay values for the golden render |
| `README.md` | This file |
