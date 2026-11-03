<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Design: kustomize-remote-bases-argo

## Context

Issue #58 requires DDO to support Kustomize remote bases. This change implements
the **Argo CD / Flux-aligned** posture: krusty fetches remote content live during
`Run`. It is a deliberate alternative to the hermetic DDO-prefetch pipeline in the
sibling change `kustomize-remote-bases`, so maintainers can compare.

**Current state (`origin/main`).** `kustomizeSource.Render` resolves only the root
via the go-git `RootResolver`, then calls
`krusty.MakeKustomizer(opts).Run(filesys.MakeFsOnDisk(), root)` with
`LoadRestrictions = LoadRestrictionsNone`. krusty is **already** the transitive-base
fetcher; the `kustomize-root-resolver` spec already states "krusty fetches
transitive remote bases itself." So this change is **additive** — it does not rip
out a prefetch pipeline (none exists on this branch).

**Hard constraint — krusty v0.21.1 exposes no fetch hook** (verified from the
module cache at `sigs.k8s.io/kustomize/api@v0.21.1`):

- `krusty.Options` has only `Reorder`, `AddManagedbyLabel`, `LoadRestrictions`,
  `PluginConfig`. No cloner, auth, or cache injection point.
- Remote-base fetch is hard-wired to `internal/git.ClonerUsingGitExec`, which
  **shells out to the `git` binary** (`exec.LookPath("git")`), runs
  `git fetch --depth=1 <ref>` with the ref passed **verbatim**, and clones to a
  fresh temp dir every `Run` (no caching).
- The git subprocess inherits the process environment (`os.Environ()`), so
  `GIT_*` env, `$HOME/.gitconfig` credential helpers, and `url.<base>.insteadOf`
  rewrites are usable **process-global** levers — never per-source/per-reconcile
  scoped.
- Public `NewLoader`/`NewLoaderOrDie` hard-code the git-exec cloner; the cloner
  field is private. No seam to inject a custom cloner/cache/auth.

DDO's image ships **no `git` binary** today (it uses in-process go-git). So the
baseline remote-base path fails at runtime with `no 'git' program on path`. Both
Argo CD and Flux ship `git` in their controller images for exactly this reason.

## Goals / Non-Goals

**Goals:**

- Make krusty's live remote-base fetch actually work in the operator image (add
  `git`).
- Retain every guardrail achievable without a krusty fetch hook: a DDO-side
  classification + optional SHA-pin-enforcement preflight, and a fail-closed
  sanitized error wrapper.
- Document the retained/partial/lost guardrail posture with Argo CD + Flux
  citations so the tradeoff is explicit and defensible.

**Non-Goals:**

- Hermetic offline render, a DDO SHA-content-addressed remote-base cache, and
  per-source `authSecretRef` credential scoping **for remote bases** — these are
  the sibling hermetic change's guarantees and are knowingly given up here (see
  Risks). Neither Argo CD nor Flux provides them for remote bases either.
- An OS-enforced network sandbox. Egress is gated operationally by NetworkPolicy
  (same posture as both peers).
- CRD/API changes; de-vendoring `ipam-capi-remote`; name-based filtering of a
  remote directory.

## Decisions

### D1 — Let krusty fetch; add `git` to the image

Keep `kustomizeSource.Render` delegating remote fetch to krusty. Add a `git`
binary to the operator Dockerfile so `ClonerUsingGitExec` works.

*Rationale / alternatives.* The in-tree krusty loader has no injection seam, so a
custom in-process (go-git) transitive fetcher would require either forking krusty
or rebuilding its loader — rejected as high-cost and divergent from the aligned
goal. Shipping `git` is the **industry norm**: both Argo CD and Flux install `git`
in their controller images and let upstream kustomize's `ClonerUsingGitExec` shell
out to it.

### D2 — DDO-side pin-enforcement preflight (the primary retained guardrail)

Before invoking krusty, walk the reachable kustomization tree (root + any local
directories), decode each kustomization with typed parsing, and classify every
`resources`/`components`/`bases` entry as local, remote-git-directory, or
remote-file. Under an operator-level flag
`--kustomize-require-sha-pinned-remote-bases` (default `false`), reject any remote
reference whose embedded ref is not a 40-hex lowercase commit SHA, **before any
fetch**. Default-off is tag-permissive (matches krusty/peers); on is a hardening
mode.

*Rationale / alternatives.* CEL on the CR validates only the **root** URL, never
transitive refs, so pin policy must be a DDO-side runtime pass. This is fully
DDO-controlled and independent of krusty's absent hooks. It is **stricter than
either peer**: Argo CD only *recommends* SHA/tag pins in its Best Practices; Flux
offers controller-level `--no-remote-bases=true` (default off) as its blunt
analogue. An operator-level flag (not a CRD field) matches Flux's controller-level
precedent and avoids an API change.

### D3 — Fail-closed sanitized wrapper

Wrap the krusty `Run` error path so an unreachable remote, a non-2xx remote file,
or a git fetch failure fails the render closed with a sanitized message that never
echoes credentials or full URLs. No silent partial render.

*Rationale.* krusty/git errors already propagate; the wrapper guarantees closed
failure and redaction, matching DDO's existing error hygiene.

### D4 — Credentials: ambient only, documented

Private remote bases, if ever needed, rely on process-global ambient git config
(a `GIT_*`/`insteadOf`/credential-helper lever), not per-source `authSecretRef`.
Document this; do not build a false per-source scoping abstraction over a
process-global mechanism.

*Rationale / alternatives.* No krusty cred hook exists. Both peers are the same:
Argo CD remote bases inherit the **app repo's** credentials only (an app "only
ever knows about its own repo"); Flux's remote-base fetch does **not** receive the
`GitRepository` `secretRef` and depends on controller-level ambient config
(kustomize-controller issues #158, #392). So ambient-only is aligned, not a
regression versus peers. Primary guidance: use **public** remote bases, or vendor.

### D5 — Egress is operational, not in-process

Document that reaching remote hosts requires NetworkPolicy egress (the verified
Gardener labels `networking.gardener.cloud/to-dns` + `to-public-networks` +
`to-private-networks`). Neither peer documents a remote-host allowlist for
remote-base fetches; both rely on CNI/NetworkPolicy.

### D6 — Phase placement

Add a **new phase after the current last phase** in `docs/implementation.md`;
leave completed/ongoing phases untouched (phase-scoping rule).

## Guardrail retention (the heart of this change)

Answers the user's "see how many guardrails we can retain" under the aligned
model. Baseline = issue-#58 guardrails the hermetic sibling change provides.

| Guardrail | Status | Mechanism / cost under the aligned model |
| --- | --- | --- |
| Fail-closed on unreachable/error | **Retained** | D3 wrapper; krusty/git errors propagate, sanitized. |
| SHA-pin enforcement (#3) | **Retained (opt-in, stricter than peers)** | D2 DDO-side preflight under `--kustomize-require-sha-pinned-remote-bases`; independent of krusty. Default off = tag-permissive like krusty/peers. |
| Remote-ref classification / reject unsupported forms | **Retained** | D2 preflight reuses the bounded HTTPS/GitHub-shorthand classifier; rejects SSH/userinfo/ambiguous forms before fetch. |
| Egress gating | **Partial (operational)** | D5 NetworkPolicy; not an in-process sandbox. Same as both peers. |
| Credential isolation / per-source `authSecretRef` (#6) | **Lost (ambient only)** | D4; no krusty hook. Both peers are ambient/app-repo-only too → aligned. |
| SHA-keyed fetch-once cache / reproducibility (#5) | **Lost** | krusty re-clones every `Run`; no content-addressed cache. Flux re-fetches every reconcile; Argo caches only by **app-repo** SHA (24h TTL), not per remote base. |
| Hermetic offline render | **Lost** | Remote content fetched live each render; the sibling hermetic change is the alternative that keeps this. |

## Risks / Trade-offs

- **Non-reproducible renders for mutable remote refs** → Mitigation: enable
  `--kustomize-require-sha-pinned-remote-bases`, or vendor. Argo CD's Best
  Practices warns remote bases "can suddenly change meaning, even without any
  changes to your own Git repository."
- **`git` binary enlarges the image + adds a supply-chain surface** → Mitigation:
  pinned base-image `git`; this matches both peers' accepted posture.
- **Process-global git credentials could leak across sources** → Mitigation:
  prefer public remote bases / vendoring; document that private remote bases are
  unsupported-by-design beyond ambient config.
- **krusty upgrade could change loading-path coverage** → Mitigation: the D2
  classifier is version-bounded; dependency bumps must revisit it.

## Migration Plan

Additive and flag-gated (default off → no behavior change for existing CRs).
Deploy: ship the image with `git` + the new flag. Rollback: unset the flag / prior
image; remote bases simply return to the (failing-without-git) baseline, no state
to unwind.

## Open Questions

- Should the pin-enforcement flag default **on** for this operator's threat model
  (stricter than both peers)? Proposed default off to match krusty/peer
  permissiveness; revisit if maintainers want hardened-by-default.

## Sources and verification

- krusty v0.21.1 no-hook + git-exec cloner: `sigs.k8s.io/kustomize/api@v0.21.1`
  (`krusty/options.go`, `internal/git/cloner.go`, `internal/git/gitrunner.go`,
  `internal/loader/loader.go`).
- Flux fetches remote bases every reconcile; `--no-remote-bases` default-off,
  prod-recommended: https://fluxcd.io/flux/faq/#should-i-be-using-kustomize-remote-bases ,
  https://fluxcd.io/flux/security/best-practices/ ,
  https://fluxcd.io/flux/components/kustomize/options/
- Flux remote-base credentials not wired from `secretRef`:
  fluxcd/kustomize-controller issues #158, #392.
- Argo CD manifest cache keyed to app-repo SHA (24h), remote-base non-stability
  warning + SHA/tag pin recommendation:
  https://argo-cd.readthedocs.io/en/stable/operator-manual/high_availability/ ,
  https://argo-cd.readthedocs.io/en/stable/user-guide/best_practices/
- Argo CD remote bases inherit app-repo credentials only: Kustomize user-guide
  "Private Remote Bases", argoproj/argo-cd issue #6440.
