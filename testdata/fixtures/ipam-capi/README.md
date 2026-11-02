# ipam-capi equivalence fixture

This is the fifth equivalence fixture in the suite. It proves that the operator's
kustomize render for the **ipam-capi shoot overlay** (`managedresources/`) matches the
golden output produced by running `krusty.Run` directly on the same overlay at the
same pinned `sapcc/helm-charts` commit SHA.

Unlike the four Helm fixtures (metal-operator, boot-operator, argora-operator, khalkeon),
whose golden render uses `helm dependency build` + `helm template`, this fixture's golden
render uses `krusty.Run` with `LoadRestrictionsNone` — the same engine the operator's
production `kustomizeSource.Render` uses. This means both sides ultimately call the same
krusty code path; the comparison proves end-to-end equivalence between the direct golden
krusty run and the operator's full render+transform pipeline.

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

When that `KustomizeSource` extension ships, a **second fixture** (e.g.
`testdata/fixtures/ipam-capi-seed/`) can be added to cover the manager/seed overlay,
and the `cr.yaml` here updated to use `seedPath: seed` with the extended CR field
that specifies the clone root separately from the build dir.

## Network requirements

- The golden krusty build fetches transitive remote bases at build time:
  - `github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster//config/*?ref=v1.1.0`
  - `raw.githubusercontent.com/kubernetes-sigs/cluster-api/v1.13.4/config/crd/bases/*.yaml`
- The operator-side render clones `sapcc/helm-charts` at the pinned SHA via `gitResolver`.
- All sources are **public/anonymous** — no OCI registry credentials needed (unlike the
  Helm fixtures, which pull subcharts from the internal keppel OCI registry).

The test is still gated on `RUN_EQUIVALENCE=1` because it requires outbound network access
to `github.com` and `raw.githubusercontent.com`, which is not available on public CI runners.

## Files

| File | Purpose |
|------|---------|
| `fixture.yaml` | Pinned SHA, kustomize overlay path, comparison scope config |
| `cr.yaml` | Representative `DualDeploymentOperator` CR for the operator-side render |
| `README.md` | This file — scope rationale and network requirements |
