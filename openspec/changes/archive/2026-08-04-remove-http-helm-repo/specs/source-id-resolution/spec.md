<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

## MODIFIED Requirements

### Requirement: Resolve a source to its immutable content id

The system MUST provide a `Resolver` capability that reports the immutable content id a source currently points at, without fetching the full artifact, via a method `ResolveID(ctx context.Context, mode Mode) (id string, err error)`. This resolved id is the soundness anchor for the render cache: a moved ref MUST yield a different id (and therefore a cache miss) with no TTL. Resolution MUST reuse the same per-source credentials already wired for fetching (`withHelmCreds` / `withGitCreds`), so an authenticated source resolves authenticated. For Helm sources, `ResolveID` MUST support ONLY the OCI transport (manifest digest); a non-`oci://` Helm `repo` MUST NOT reach `ResolveID` (it is rejected earlier at admission and by the loader).

#### Scenario: moved ref changes the resolved id

- **WHEN** a source pins a mutable ref (e.g. `?ref=main`) and the ref advances to a new commit
- **THEN** `ResolveID` returns the new content id on the next reconcile
- **AND** the render cache treats it as a miss and re-renders the new content

---

## REMOVED Requirements

### Requirement: Resolve classic HTTP Helm repos via the index, degrade gracefully

**Reason**: The classic HTTP(S) Helm-repo parent-chart transport is removed from the loader (Phase 7.7 — the Helm loader becomes OCI-only). With no HTTP(S) Helm-repo path in `Load`, there is no HTTP source for `ResolveID` to resolve via `index.yaml`. Phase 7.6 already restricted subchart dependency resolution to OCI, which made the HTTP(S) parent path a dead end; the whole fleet publishes to the keppel OCI registry.

**Migration**: No live consumer uses a non-`oci://` Helm `repo` (the fleet is OCI-only; the operator is not deployed live yet). Any Helm source MUST use an `oci://` `repo`, which `ResolveID` resolves to its immutable manifest digest via the existing "Resolve OCI Helm charts to their manifest digest" requirement. A non-`oci://` Helm `repo` is now rejected at admission (CEL rule on `HelmSource.repo`) and by the loader, so it never reaches `ResolveID`.
