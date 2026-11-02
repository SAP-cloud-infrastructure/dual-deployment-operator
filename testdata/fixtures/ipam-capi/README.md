# ipam-capi equivalence fixture

This is the fifth equivalence fixture in the suite. It proves that the operator's
kustomize render for the **ipam-capi shoot overlay** (`managedresources/`) produces
output equivalent to today's **ipam-capi-remote Helm chart** (`system/ipam-capi-remote/`)
at the same pinned `sapcc/helm-charts` commit SHA — the same golden baseline pattern
used by all four existing operator fixtures.

Golden side: `git clone` + `helm dependency build` + `helm template system/ipam-capi-remote/`,
then unwrap ManagedResources → shoot object set.

Operator side: `source.From` → `Render` (KustomizeSource, shoot mode only) →
`transform.Build` pipeline, captured before the delivery layer.

## Scope: shoot overlay only

**Only the `managedresources/` (shoot) overlay is covered.** The `seed/manager` overlay
is deliberately **NOT** tested here.

### Why the seed overlay is excluded

ipam-capi's `manager` overlay references patch files by **repo-root-relative paths**:

```yaml
# system/kustomize/ipam-capi-remote/manager/kustomization.yaml
patches:
  - path: kustomize/ipam-capi-remote/manager/some-patch.yaml
    ...
```

These paths only resolve when kustomize builds from the **repository root** (the default
for `make build-ipam-capi-remote` in `sapcc/helm-charts`). The operator's `KustomizeSource`
resolves the build directory to the **overlay subdir** (`system/kustomize/ipam-capi-remote/seed`),
so the relative patch paths escape to a location that does not exist under that subdir,
and the build fails with `no such file`.

Closing the seed overlay equivalence requires extending `KustomizeSource` so the clone
root and the kustomize-build directory can differ (i.e. clone the full repo but build from
the overlay subdir, keeping relative paths resolvable relative to the clone root). This is
a **CRD + source change** (design §9.4) explicitly out of scope for the current change.

See **`docs/implementation.md`** → *"`ipam-capi` equivalence is DEFERRED to a standalone
future follow-up (not tied to any phase)"* for the full deferral rationale and the two
ipam-capi-specific blockers. The relevant excerpt:

> *Deferred:* ipam-capi's kustomize overlays reference patch files by **repo-root-relative**
> paths (`patches[].path: kustomize/ipam-capi-remote/manager/…`), so today's `make build-`
> runs kustomize from the repo root. The operator's `KustomizeSource` (`url` + `seedPath`/`shootPath`)
> resolves the build dir to the overlay **subdir**, so those relative patch paths double and
> the seed/`manager` overlay fails to build (`no such file`). Closing ipam-capi equivalence
> requires extending `KustomizeSource` so the clone root and the kustomize-build dir can differ
> (build from the repo root, target the overlay) — a CRD + source change (design §9.4),
> hence its own follow-up rather than part of the equivalence-tests change.

When that `KustomizeSource` extension ships, a second test covering the seed/manager overlay
can be added alongside this fixture. The `cr.yaml` here already has a `seedPath: seed` field
that will be exercised once the seed render is unblocked.

## Network requirements

This test is gated on `RUN_EQUIVALENCE=1` because it requires outbound network access:

- **Golden side (Helm)**: git clone of `github.com/sapcc/helm-charts` + `helm dependency build`
  pulling subcharts from the internal keppel OCI registry (`keppel.global.cloud.sap/ccloud-helm/…`).
  Same requirement as the other four Helm fixtures.
- **Operator side (kustomize)**: git clone of `github.com/sapcc/helm-charts` (same SHA) +
  krusty fetching transitive remote bases (`cluster-api-ipam-provider-in-cluster ?ref=v1.1.0`,
  cluster-api CRD bases `v1.13.4`) from `github.com` and `raw.githubusercontent.com`.
  All kustomize sources are public/anonymous (no OCI registry credentials needed for this side).

## Files

| File | Purpose |
|------|---------|
| `fixture.yaml` | Pinned SHA, Helm chart path, overlay values file, kustomize overlay path, comparison scope config |
| `cr.yaml` | Representative `DualDeploymentOperator` CR for the operator-side kustomize render |
| `overlay-values.yaml` | Per-cluster Helm overlay values for the golden `ipam-capi-remote` chart render |
| `README.md` | This file — scope rationale and network requirements |
