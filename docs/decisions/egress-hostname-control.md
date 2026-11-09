# DDO remote fetch: egress and source-trust decision

**Status:** Decided · 2026-11-09
**Component:** `dual-deployment-operator` (DDO)
**Feature:** Kustomize remote-base policy engine ([issue #58](https://github.com/SAP-cloud-infrastructure/dual-deployment-operator/issues/58))

## TL;DR

DDO follows the Argo CD / Flux model. Kustomize remote bases and Helm OCI charts
are live-fetched at reconcile time and enabled by default. **Which** sources are
trusted is the user's responsibility; **network egress** is the platform's. DDO
does not ship hostname-level egress control, a source allowlist, or a proxy.

## Context

DDO is gaining the ability to fetch Kustomize remote bases (git URLs) and Helm
OCI charts at reconcile time. The work lands in three PRs, merged in order
[#62](https://github.com/SAP-cloud-infrastructure/dual-deployment-operator/pull/62)
→ [#61](https://github.com/SAP-cloud-infrastructure/dual-deployment-operator/pull/61)
→ [#63](https://github.com/SAP-cloud-infrastructure/dual-deployment-operator/pull/63).

An earlier revision of #61 hardened egress **in-process** by mutating Go's global
`http.DefaultTransport` and **forcing HTTP/1.1** (stripping HTTP/2 from ALPN).
Both were removed in commit
[`eef27cb`](https://github.com/SAP-cloud-infrastructure/dual-deployment-operator/commit/eef27cb)
as anti-patterns that Flux and Argo CD avoid. That left one open question: should
DDO restrict outbound connections by hostname (e.g. only `github.com`, `ghcr.io`,
`raw.githubusercontent.com`)?

The target environment limits what is possible. DDO runs in a shoot control-plane
namespace (`shoot--cp--*`) on the seed, as a single container with **no Istio
sidecar**. **Cilium is not installed**, and **Calico OSS has no native FQDN egress
policy**. (Verified read-only on `a-qa-de-200` and `rt-qa-de-1`.)

The #61 policy engine already checks the **form** of remote references: it rejects
SSH references, embedded credentials, URL fragments and unknown schemes, walks the
local tree for cycles and symlink escapes, and offers SHA pinning behind
`--kustomize-require-sha-pinned-remote-bases` (default `false`).

## Decision

| Topic | Decision |
| --- | --- |
| Remote fetch | Enabled by default, as in Argo CD. No opt-in switch is added. |
| Source trust | User's responsibility: RBAC on DDO resources decides who may set a source, and review in the referenced repos decides what it pulls in. DDO adds no source allowlist. |
| Content pinning | Available, not enforced: `--kustomize-require-sha-pinned-remote-bases` stays opt-in (default `false`). |
| Hostname egress control | Out of scope for DDO; a platform concern. |
| Network egress | Relies on the platform. On Gardener, the standard `to-public-networks` policy limits egress to public IPs and blocks private and CGNAT ranges. The chart's existing egress NetworkPolicy stays optional and off by default; no new work. |
| Forward proxy | Not shipped. No proxy values are added to the chart now; can be added later if a platform proxy appears. |
| In-process transport hardening | Stays removed (`eef27cb`). |

## Rationale

### Hostname filtering does not address the main risk

The real risk of live fetching is applying **untrusted content**. `github.com`
and `ghcr.io` are multi-tenant: anyone can host a repo or image there. An
allowlist of those hostnames lets an attacker's repo through just as easily as
ours. The network layer cannot tell `github.com/sap/...` from
`github.com/attacker/...`, because the path is inside TLS. Content trust has to be
set **where the source is chosen**, not where the packets leave.

### It matches the community norm

- **Argo CD:** remote bases work by default. Source restriction is a per-project
  allowlist (`sourceRepos`) that admins configure and that allows all repos by
  default. Egress and proxies are left to the platform; a proxy is consumed
  through `HTTPS_PROXY` / `NO_PROXY`.
- **Flux:** trust comes from who may create source objects (RBAC) and what those
  repos contain. Flux recommends `--no-remote-bases` for production but does not
  ship hostname egress control either.
- Neither forces HTTP/1.1 or mutates the global HTTP transport, which is why that
  hardening was removed.

### No reliable mechanism exists on the target

| Mechanism | Usable for DDO here? |
| --- | --- |
| Core / Calico CIDR NetworkPolicy | Yes, but CIDR and port only, no hostnames. |
| Istio `ServiceEntry` + `REGISTRY_ONLY` | No: DDO has no Envoy sidecar, so its traffic never passes through Istio. |
| Cilium `toFQDNs` | No: Cilium not installed. |
| Calico `destination.domains` | No: Calico Enterprise / Cloud only, not OSS. |
| Proxy sidecar in the DDO pod | No: containers share the pod's network, so the manager could bypass the proxy. |
| Chart-managed proxy Deployment | Works, but adds a component to run and patch, and still allows any repo on an allowed host. |
| DNS → Calico `GlobalNetworkSet` updater | Not recommended: breaks on CDN IP churn. |

### Why remote fetch stays on by default

Live-fetched remote bases are the feature users asked for (issue #58), and Argo CD
ships them on by default. Users who need stricter guarantees already have SHA
pinning as an opt-in flag, and can restrict who creates DDO resources through RBAC.

## Options considered

| Option | Outcome | Reason |
| --- | --- | --- |
| A: CIDR NetworkPolicy + document `HTTPS_PROXY` | Not adopted as framed | The existing optional NetworkPolicy stays; hostname allowlisting is no longer the goal. |
| B: A + proxy env values in the chart | Not adopted now | Convenience only, not a security control; no platform proxy exists today. |
| C: Defer hostname layer to a follow-up | Not adopted | The question is closed as out of scope, not deferred. |
| Proxy sidecar in the DDO pod | Rejected | Not enforceable: NetworkPolicy applies to the whole pod. |
| Chart-managed egress proxy Deployment | Rejected | New component to maintain; allowing `github.com` still allows any repo there. |
| DDO-maintained source allowlist | Rejected | Source trust is the user's, as in Flux (RBAC) and Argo CD (admin-configured `sourceRepos`). |
| Enforce SHA pinning / remote fetch off by default | Rejected | Leans too far from the Argo CD norm; pinning stays available as an opt-in flag. |

## Shared responsibility

| Who | Owns |
| --- | --- |
| **User** | Who may create or change DDO resources (RBAC); which repos and registries are referenced, and their review; whether to pin to commit SHAs (enable `--kustomize-require-sha-pinned-remote-bases`). |
| **DDO** | Rejecting malformed or unsafe reference forms (SSH, embedded credentials, fragments, unknown schemes); local tree safety (cycles, symlink escapes); sanitized errors that never expose credentials. DDO does **not** bound fetch duration in-process (see residual risks). |
| **Platform** | Network egress policy. Effective reachability is the **union** of the applicable NetworkPolicies — Gardener labels only *allow* the categories they name. The chart's default manager-pod labels include `to-public-networks` **and** `to-private-networks`, so private ranges are reachable by default unless the platform drops `to-private-networks` behind a deny-all baseline; any forward proxy with a hostname allowlist, if the platform chooses to run one. |

## Consequences and follow-ups

**Residual risks, accepted and documented:**

- Without an allowlist or RBAC restriction, any repo a DDO resource references can
  be fetched.
- Unpinned refs (branches, tags) can change after review unless users enable SHA
  pinning.
- Nested remote bases inside a referenced repo are fetched too, and are not checked
  by the #61 policy engine.
- DDO can reach any endpoint the **union** of applicable egress policies allows. The
  chart's default manager-pod labels allow both `to-public-networks` and
  `to-private-networks`, so private/internal ranges are reachable by default; the
  metadata endpoint (`169.254.169.254`) and internal services are blocked only if the
  platform drops `to-private-networks` (and link-local) behind a deny-all baseline.
- Fetch duration is not bounded in-process (`runKrusty` has no context deadline;
  upstream krusty uses a bare `&http.Client{}` + `io.ReadAll`), so an unresponsive
  endpoint can stall a reconcile indefinitely. Bounding it reliably needs an external
  watchdog, not in-process transport mutation.

**Follow-ups:**

- [ ] Optional: git lockdown in the #62 image (`https`-only protocols, no redirects,
  no submodules, no credential prompts) so git cannot reach beyond the referenced
  URL. Low cost; not decided.
- [ ] Confirm with the Gardener team that the cloud metadata IP
  (`169.254.169.254`) is blocked for pods with `to-public-networks`.

Revisit this decision if DDO becomes multi-tenant or if a platform forward proxy
becomes available.

## Sources

- [PR #61](https://github.com/SAP-cloud-infrastructure/dual-deployment-operator/pull/61),
  [PR #62](https://github.com/SAP-cloud-infrastructure/dual-deployment-operator/pull/62),
  [PR #63](https://github.com/SAP-cloud-infrastructure/dual-deployment-operator/pull/63)
- [Gardener network policies](https://gardener.cloud/docs/gardener/network_policies/)
- [fluxcd/kustomize-controller#638 (`--no-remote-bases`)](https://github.com/fluxcd/kustomize-controller/pull/638)
- [Flux proxy settings](https://v2-8.docs.fluxcd.io/flux/installation/configuration/proxy-setting/)
- [Calico DNS policy (Enterprise / Cloud)](https://docs.tigera.io/calico-cloud/network-policy/domain-based-policy)
