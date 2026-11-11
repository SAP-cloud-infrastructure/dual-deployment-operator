<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Proposal: kustomize-remote-bases-argo

## Why

Issue #58 asks DDO to support Kustomize remote bases (transitive git-directory
references and remote single-file manifests). This change delivers that in the
**Argo CD / Flux-aligned** shape: let the embedded krusty engine fetch remote
content live during `Run`, exactly as both industry controllers do, and layer on
the guardrails that remain achievable without a krusty fetch hook. It is a
deliberate alternative to a hermetic DDO-prefetch model (the stricter,
SHA-content-addressed posture discussed under issue #58; not implemented in this
repository), offered so the maintainers can compare postures.

The `origin/main` baseline already renders with `krusty.MakeKustomizer(...).Run()`
and `LoadRestrictionsNone` — krusty is already the transitive-base fetcher. Two
things block that path in production today: the operator image ships **no `git`
binary** (krusty's remote-base cloner shells out to `git`), and there is **no
DDO-side policy gate** on what refs krusty is allowed to fetch. This change closes
both gaps.

## What Changes

- Add a **`git` binary** to the operator image so krusty's `ClonerUsingGitExec`
  can fetch remote git-directory bases at render time (both Argo CD and Flux ship
  `git` in their controller images for exactly this reason).
- Add a **DDO-side pin-enforcement preflight**: before invoking krusty, walk the
  reachable kustomization tree and classify every remote reference. Under an
  operator-level flag (default off), reject any **top-level** remote ref the
  operator parses whose pin is not a 40-hex commit SHA — a hardening lever
  stricter than either peer, independent of krusty's (absent) fetch hooks.
  Enforcement covers the references the operator can see up front; it does **not**
  reach transitive bases nested inside remote repositories (krusty resolves those
  internally, and — matching Flux and Argo CD — no tool pre-enforces them).
- Add a **fail-closed error wrapper** around krusty remote fetches so an
  unreachable or non-2xx remote reference fails the render with a sanitized error
  (never a silent partial render, never echoing credentials).
- **Document the accepted posture**: live fetch each render, no hermetic offline
  render, no DDO SHA-content-addressed cache, ambient/process-global git
  credentials only, egress gated operationally by NetworkPolicy. Record the
  guardrail-retention analysis (retained / partial / lost) with Argo CD + Flux
  citations.
- Update the authoring guide and egress operational notes for the live-fetch
  contract.

Not changing: the `RootResolver` interface, the go-git root resolver, or
deployment egress policy defaults. No vendored CRDs are de-vendored
(`ipam-capi-remote` stays vendored). This change does not mutate the
process-global `http.DefaultTransport`: an earlier in-process transport-hardening
attempt was removed as an anti-pattern in favour of operational egress control
(see design.md D9). The chart ships an optional egress `NetworkPolicy` as the
recommended destination-control layer.

## Capabilities

### New Capabilities

- `kustomize-remote-base-policy`: the DDO-side classification + optional
  SHA-pin-enforcement preflight and fail-closed wrapper applied to the reachable
  kustomization tree before krusty fetches remote references.

### Modified Capabilities

- `kustomize-root-resolver`: clarify that transitive remote bases and remote
  single-file references are fetched **live by krusty** during `Run` (the model
  the spec already names), now gated by the new policy preflight and backed by the
  `git` binary in the image.

## Impact

- **Image / packaging**: operator Dockerfile gains a `git` binary; chart/egress
  docs updated.
- **Code**: `internal/source` (new policy preflight + fail-closed wrapper,
  wired into `kustomizeSource.Render`); `cmd/main.go` (new operator-level
  pin-enforcement flag, default off).
- **Docs**: `docs/kustomize-remote-bases.md` authoring guide and
  `docs/implementation.md` (new phase after the current last phase).
- **No API/CRD change**: pin enforcement is an operator-level flag, not a CRD
  field (CEL can only gate the root URL, never transitive refs).
