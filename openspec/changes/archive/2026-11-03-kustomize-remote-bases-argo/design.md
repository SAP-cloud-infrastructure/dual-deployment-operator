<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Design: kustomize-remote-bases-argo

## Context

Issue #58 requires DDO to support Kustomize remote bases. This change implements
the **Argo CD / Flux-aligned** posture: krusty fetches remote content live during
`Run`. It is a deliberate alternative to a hermetic DDO-prefetch pipeline (the
stricter SHA-content-addressed posture discussed under issue #58; not implemented
in this repository), so maintainers can compare.

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
reference across all fields the parser understands: `resources`/`components`/`bases`
(remote refs allowed); `generators`/`transformers`/`validators` path form (tree-edge:
local dirs recursed, direct remote rejected in strict mode; inline YAML docs
silently skipped); and local-only fields (patches, crds, configMap/secretGenerator
file refs, openapi, configurations). The `git::` go-getter force-prefix is stripped
before classification. Under an operator-level flag
`--kustomize-require-sha-pinned-remote-bases` (default `false`), reject any
top-level remote reference whose embedded ref is not a 40-hex lowercase commit SHA,
**before any fetch**. Accepted SHA-pinned forms: scheme-less GitHub shorthand
`github.com/org/repo//path?ref=<sha>`; `git::https://` prefixed variant (stripped
then validated); canonical `https://raw.githubusercontent.com/.../<sha>/<path>` for
single files. Default-off is tag-permissive (matches krusty/peers); on is a
hardening mode.

*Rationale / alternatives.* CEL on the CR validates only the **root** URL, never
transitive refs, so pin policy must be a DDO-side runtime pass. This is fully
DDO-controlled and independent of krusty's absent hooks. It is **stricter than
either peer**: Argo CD only *recommends* SHA/tag pins in its Best Practices; Flux
offers controller-level `--no-remote-bases=true` (default off) as its blunt
analogue. An operator-level flag (not a CRD field) matches Flux's controller-level
precedent and avoids an API change.

Top-level-only enforcement matches both Flux and Argo CD (neither pre-enforces
transitive refs nested inside remote repositories; krusty resolves those
internally and there is no loader-interception seam in krusty v0.21.1).

### D3 — Fail-closed sanitized wrapper (REDACT-AND-PRESERVE)

Wrap the krusty `Run` error path so an unreachable remote, a non-2xx remote file,
or a git fetch failure fails the render closed. The surfaced error uses a
**redact-and-preserve** policy: non-credential diagnostic context (host, repository
path, exit code, git subcommand) is preserved to keep the error actionable; only
credential material (URL userinfo including passwords containing `@` or `/`, and
secret-bearing query params such as `token=`, `access_token=`, `password=`, `sig=`)
is redacted. The error never contains credentials. No silent partial render.

*Rationale.* krusty/git errors already propagate; the wrapper guarantees closed
failure and credential redaction, matching DDO's existing error hygiene. A
reason-code prefix was considered but removed: it substring-matched the whole
message and mislabelled purely-local errors; the condition reason stays
`SeedRenderFailed`/`ShootRenderFailed`.

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

### D5 — Egress is operational; hostname control and source trust are out of scope

Document that reaching remote hosts requires NetworkPolicy egress (the verified
Gardener labels `networking.gardener.cloud/to-dns` + `to-public-networks` +
`to-private-networks`). Neither peer documents a remote-host allowlist for
remote-base fetches; both rely on CNI/NetworkPolicy.

DDO does **not** ship hostname-level egress control, a source allowlist, or a
proxy. Remote fetch is enabled by default (as in Argo CD), and trust is split:
**which** sources are trusted is the user's responsibility (RBAC on DDO
resources + review of the referenced repos; SHA pinning available opt-in via
`--kustomize-require-sha-pinned-remote-bases`), while **network egress** is the
platform's. Note the effective egress is the *union* of the applicable
NetworkPolicies: the chart's default manager-pod labels allow both
`to-public-networks` and `to-private-networks`, so private ranges are reachable by
default unless the platform drops `to-private-networks` behind a deny-all baseline.

*Rationale.* Hostname filtering does not address the real risk — applying
untrusted *content*. `github.com`/`ghcr.io` are multi-tenant, so a hostname
allowlist admits an attacker's repo as readily as ours, and the network layer
cannot distinguish `github.com/sap/...` from `github.com/attacker/...` (the path
is inside TLS). This matches Argo CD (`sourceRepos` admin allowlist, egress left
to the platform) and Flux (RBAC on source objects). No reliable hostname
mechanism exists on the target anyway: no Istio sidecar (so `ServiceEntry` /
`REGISTRY_ONLY` never applies), no Cilium (`toFQDNs` unavailable), and Calico OSS
has no native FQDN egress. See
[`docs/decisions/egress-hostname-control.md`](../../../../docs/decisions/egress-hostname-control.md)
for the full decision, options considered, and accepted residual risks.

### D6 — Phase placement

Add a **new phase after the current last phase** in `docs/implementation.md`;
leave completed/ongoing phases untouched (phase-scoping rule).

### D7 — Top-level-only SHA enforcement (matches Flux/Argo CD)

The SHA-pin gate enforces only on the top-level remote references the operator
parses directly. Transitive/nested remote refs that krusty resolves internally
(inside remote repositories krusty has already fetched) are NOT pre-enforced.
This matches both Flux and Argo CD: neither peer pre-enforces transitive bases;
both rely on operational controls (NetworkPolicy egress, least-privilege
credentials) for the transitive layer. krusty v0.21.1 has no loader-interception
seam that would allow hooking into transitive fetches.

### D8 — GIT_ALLOW_PROTOCOL=https (process-wide git protocol constraint)

`GIT_ALLOW_PROTOCOL=https` is set via `os.Setenv` early in `main()` (after
`flag.Parse()`). This constrains the
git-CLI fetches krusty shells out to — krusty-triggered remote bases AND
submodules — to HTTPS only. Setting it in-process (not only as a
Deployment env var) ensures a Helm `manager.env` list-override cannot silently
drop it.

**It does NOT cover the operator's own go-git calls.** The `RootResolver`
clones the configured source-root URL with go-git, which does not consult
`GIT_ALLOW_PROTOCOL`: with the variable set, the root resolver still contacts an
`http://` server and clones `file://`. The root URL has no scheme rule of its
own (CEL only requires `ref=`); if HTTPS-only must hold for the root it needs a
separate explicit scheme check. Constrain the root operationally (NetworkPolicy
egress) in the meantime.

### D9 — No in-process transport hardening (anti-pattern removed)

An earlier revision mutated the process-global `http.DefaultTransport`
(bounded timeouts + forced HTTP/1.1 by stripping ALPN h2) because krusty's file
loader and Helm's OCI registry client have no transport injection seam. This was
removed: forcing a protocol version and replacing the global transport are
anti-patterns, and neither Flux CD nor Argo CD does it. HTTP/2 does not make
remote content more or less trustworthy; the real risks (SSRF, oversized bodies,
slow reads, redirects) are addressed independently. Both peer projects bound
fetches with context/process timeouts and relocate egress control to the
platform (NetworkPolicy, egress proxy, repo allowlists).

Containment of hung, slow, or misdirected remote-base fetches is therefore
**operational**: the chart's optional egress `NetworkPolicy` (D5), the Gardener
egress labels on the manager pod, and the reconcile/pod lifecycle. The operator
does not mutate `http.DefaultTransport` in-process.

### D10 — REDACT-AND-PRESERVE error policy

Sanitized krusty/git errors preserve non-credential diagnostic context (host,
repository path, exit code, git subcommand) to keep the error actionable. Only
credential material is redacted: URL userinfo (passwords containing `@` or `/`),
and secret-bearing query params (`token=`, `access_token=`, `password=`, `sig=`,
etc.). No reason-code prefix is added (it mislabelled purely-local errors and was
removed in round-3).

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

The flag is additive and default-off, but **flag-off is not a zero-behavior-change
deployment**: the following apply unconditionally regardless of the flag:

- Remote reference classification and rejection of always-banned forms (`http://`,
  SSH, embedded userinfo, URL fragments, username-prefixed refs).
- The REDACT-AND-PRESERVE sanitized error format replacing raw krusty/git errors.
- `GIT_ALLOW_PROTOCOL=https` set at process startup (constrains all git-driven
  fetches — krusty transitive bases, submodules — to HTTPS).
- The optional egress `NetworkPolicy` (chart) is the recommended
  destination-control layer; the operator does not harden `http.DefaultTransport`
  in-process (anti-pattern, removed — see D9).

SHA-pin enforcement is the only behavior gated on the flag.

Deploy: ship the image with `git` + the new flag. Rollback: unset the flag / prior
image. The above unconditional hardening is lost on rollback, but no persistent
state is involved.

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
