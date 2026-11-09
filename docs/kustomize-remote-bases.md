# Authoring Kustomize Overlays with Remote Bases

This guide describes the remote `resources`, `components`, and `bases` supported
by the dual-deployment-operator's Kustomize renderer.

## Fetch and render model

The operator resolves the configured seed or shoot overlay, then runs krusty.
Krusty fetches transitive remote bases live during each `Render()`; seed and shoot
are rendered separately, so remote content is fetched as part of each render.
There is no hermetic offline render. A remote fetch failure fails the render rather
than silently omitting content.

> **Render cache (always-on):** The operator maintains an always-on in-memory
> render cache keyed by the resolved content identifier (git SHA or OCI digest).
> When a cache hit occurs, the cached result is returned directly and krusty is
> **not** invoked — meaning remote bases are **not** re-fetched and the preflight
> is **not** re-run. **Hazard:** if a remote base is referenced by a mutable tag
> or branch and that tag/branch is subsequently force-moved to a different commit,
> the cached render stays frozen at the original content until the cache is evicted
> (process restart, new root-SHA, or capacity eviction). To avoid silent staleness,
> pin remote bases to immutable 40-character commit SHAs (see
> [Pin refs for reproducibility](#pin-refs-for-reproducibility)) or accept that a
> restarted operator will re-fetch and reflect the moved tag.

For example, a GitHub remote directory may be referenced as:

```yaml
resources:
  - github.com/example/platform-config//base?ref=0123456789abcdef0123456789abcdef01234567
```

Use the **scheme-less** `github.com/<owner>/<repo>//<path>?ref=<sha>` shorthand
for a remote git directory, not an `https://` URL. Krusty attempts an HTTP GET
for any `http(s)://` reference *before* it tries a git clone, so an `https://`
directory URL can be served as an arbitrary file by the host and the `?ref=` is
never honored. The scheme-less shorthand goes straight to the git cloner, which
honors the pinned commit. A single remote *file* is the one exception: it must be
a canonical `https://raw.githubusercontent.com/<owner>/<repo>/<sha>/<path>` URL
(immutable blob at a commit SHA).

The operator runs a preflight over the reachable kustomization tree before
krusty fetches remote content. It classifies remote references across all
kustomization fields it understands and rejects unsupported forms, including SSH
URLs, `http://`, embedded user information, URL fragments, ambiguous repository
paths, and (in strict mode) bare `https://` git directories in `resources` and
`bases` (`components` is exempt — see the accepted-forms table). Errors are
sanitized: the error message preserves host/repo/path and exit
context for diagnosis but never contains credential material.

## Pin refs for reproducibility

Pin remote bases to immutable commit SHAs where possible. Argo CD's [Kustomize
Best Practices](https://argo-cd.readthedocs.io/en/stable/user-guide/best_practices/)
warn that remote bases can "suddenly change meaning, even without any changes to
your own Git repository." Since this operator fetches remote bases live, the same
reproducibility caveat applies here: a branch or tag can resolve to different
content on a later render.

SHA enforcement is opt-in through the operator-level flag:

```text
--kustomize-require-sha-pinned-remote-bases
```

The default is `false`, which permits tags and branches. When set to `true`, the
preflight requires each remote reference it parses from the **top-level**
kustomization to use a 40-character lowercase hexadecimal commit SHA and rejects
other refs before any remote fetch. This matches the Flux/Argo CD model: the
operator enforces pinning on the references it can see up front, not on
transitive bases nested inside remote repositories (which krusty resolves
internally and which no tool pre-enforces). The flag governs remote bases only,
not the configured source-root URL.

> **Preflight scope:** The preflight walks the **locally reachable** kustomization
> tree — the files that exist in the local clone at the time of the check. It does
> not fetch remote bases first and then inspect their kustomization files; remote
> bases that themselves reference further remotes are invisible to the preflight
> until after krusty fetches them. The SHA-pin check therefore covers the entry
> points visible before the first fetch, not kustomizations nested inside remote
> repositories.

### Strict-mode field scope and accepted remote forms

The preflight classifies references from **all** fields it understands in each
`kustomization.yaml`:

| Category | Fields | Behaviour in strict mode |
|---|---|---|
| **Resource-like** | `resources`, `components`, `bases` | Remote refs accepted in the accepted forms below; SHA-pinning enforced. |
| **Tree-edge** | `generators`, `transformers`, `validators` (path form only; inline YAML docs are silently skipped) | Must reference a **local path** only. A direct remote value is rejected. Local **directories** are recursed into, so an unpinned remote base inside a transformer-referenced local directory is caught. |
| **Local-only** | `patches[].path`, `patchesJson6902[].path`, `patchesStrategicMerge` (file-path form), `replacements[].path`, `crds`, `configurations`, `openapi.path`, `configMapGenerator`/`secretGenerator` file and env refs | Must reference a **local path** only. A direct remote value is rejected. These are not recursed further. |

> **Limitation:** the preflight does not parse inside a plugin/builtin config file
> (e.g. a `PatchTransformer`'s embedded `path:` field). Remote paths embedded
> within such a config file are not pre-enforced.

**Accepted remote forms in strict mode:**

| Form | Example | Notes |
|---|---|---|
| Scheme-less GitHub shorthand | `github.com/org/repo//dir?ref=<40-hex-sha>` | Preferred for git directories. Krusty resolves via git so the SHA is honored. |
| `git::https://` prefixed | `git::https://github.com/org/repo//dir?ref=<40-hex-sha>` | The `git::` go-getter force-prefix is stripped; the remaining URL is validated as above. |
| Canonical raw GitHub file | `https://raw.githubusercontent.com/<owner>/<repo>/<40-hex-sha>/<path>` | Single file only (not valid in `components`). Immutable blob addressed by commit SHA. |
| Bare `https://` git directory in `components` | `https://github.com/org/repo//dir?ref=<40-hex-sha>` | `components` is exempt from the bare-`https://` rejection that applies to `resources`/`bases`. The same form is rejected under `resources` and `bases`; use the scheme-less shorthand there. |

**Always rejected** (flag-independent):

- `http://` (cleartext) remote references
- `ssh://` or SCP-style (`user@host:path`) references
- References embedding URL userinfo (`user:token@host`)
- References with URL fragments (`#`)
- Username-prefixed references — a reference that *starts* with `user@`, such as
  `user@github.com/o/r//b?ref=<sha>` (a `user@` appearing only inside the `?ref=`
  value, e.g. `github.com/o/r//b?ref=user@x`, is not rejected)

## Credentials and network access

Remote Kustomize bases do not have per-source `authSecretRef` support. Any Git
credentials or URL rewrites used by krusty's Git subprocess come from
ambient/process-global configuration, not from an individual source or
reconcile. Prefer public remote bases or vendor the content rather than relying
on credentials that would be shared across sources.

Remote fetch egress is controlled operationally, not by an in-process
host-allowlist. The operator Pod needs network access to the remote hosts for
fetches to succeed. If you need to constrain which hosts it can contact, apply an
egress-restricting NetworkPolicy; this project does not enforce a host allowlist
inside the process. In Gardener, the operator Pod also needs the applicable
networking labels for egress to be enabled. See
[Phase 9's Gardener egress prerequisite](implementation.md#phase-9-deployment-charts-2-days)
in the [implementation guide](implementation.md).

The operator image includes `git` on `PATH` because krusty's remote git cloner
shells out to the Git executable.

### Protocol and transport hardening

The operator constrains the git protocol unconditionally (flag-independent):

- **`GIT_ALLOW_PROTOCOL=https`** is set at process startup, constraining the
  git-driven fetches krusty runs through `git` — including krusty-triggered
  remote bases and submodules — to HTTPS only. Any attempt to use `ssh://`,
  `git://`, `file://`, or other protocols at the git layer is denied.
  **The configured source-root URL is not covered by this:** it is resolved by
  the operator's own `go-git` client, which ignores `GIT_ALLOW_PROTOCOL` — with
  the variable set, the root resolver still contacts an `http://` server and
  clones `file://`. Constrain the root URL operationally (NetworkPolicy egress)
  rather than relying on this variable.

The operator does **not** mutate the process-global `http.DefaultTransport` or
force HTTP/1.1 in-process. Forcing a protocol version or replacing the global
transport is an anti-pattern — neither Flux CD nor Argo CD does it, and HTTP/2
does not make remote content more or less trustworthy. Both projects instead
bound fetches with timeouts and relocate egress control to the platform. This
operator follows that norm: containment of hung, slow, or misdirected
remote-base fetches is **operational**, not in-process:

- The chart ships an optional egress `NetworkPolicy`
  (`networkPolicy.egress.enabled`, scoped to the manager pod) as the recommended
  destination-control layer. Core `NetworkPolicy` expresses CIDR/port rules; for
  FQDN allowlisting use an egress proxy or a CNI that supports it.
- In a Gardener control-plane namespace the manager pod's egress labels
  (`networking.gardener.cloud/to-*`) gate outbound traffic behind the
  namespace's deny-all baseline.
- **Render duration is bounded by a context deadline.** A single render is
  wrapped with `context.WithTimeout` from the `--source-render-timeout` flag
  (default `5m`; set `0` to disable). This is the community-norm bound for GitOps
  remote fetches — Flux uses `GitRepository.spec.timeout`, Argo CD uses
  `ARGOCD_EXEC_TIMEOUT`. A context deadline (not transport mutation) is what
  bounds the whole `resolve → fetch → checkout` sequence, cancels a blocked `git`
  subprocess, and frees the stalled reconcile worker instead of blocking it
  indefinitely. **Caveat:** the deadline reaches the go-git root resolve and the
  `git` CLI krusty shells out to for remote bases, but **not** krusty's own
  internal `net/http` GET of a remote base (upstream krusty uses a bare
  `&http.Client{}` + `io.ReadAll` with no context seam). The egress NetworkPolicy
  and Gardener labels remain the layer that gates *where* a fetch may go, as
  opposed to *how long* it may run.

## Security and responsibility

Live-fetching remote bases follows the Argo CD / Flux model: remote fetch is
**enabled by default**, and trust is split across three parties. See the
[egress and source-trust decision](decisions/egress-hostname-control.md) for the
full rationale.

| Who | Owns |
|---|---|
| **User** | Who may create or change DDO resources (RBAC); which repos and registries are referenced, and their review; whether to pin to commit SHAs (`--kustomize-require-sha-pinned-remote-bases`). |
| **DDO** | Rejecting malformed or unsafe reference forms (SSH, embedded credentials, fragments, unknown schemes); local tree safety (cycles, symlink escapes); sanitized errors that never expose credentials; bounding render duration with a context deadline (`--source-render-timeout`, default `5m`) that cancels a stalled resolve/`git` fetch — though it does not reach krusty's own internal HTTP GET (see the render-duration note above). |
| **Platform** | Network egress policy. Effective reachability is the **union** of the applicable NetworkPolicies — Gardener labels only *allow* the categories they name, they do not deny others. The chart's default manager-pod labels include `to-public-networks` **and** `to-private-networks`, so private ranges are reachable by default; restrict them at the platform (drop `to-private-networks`, deny-all baseline) if that is not wanted. Also: any forward proxy with a hostname allowlist, if the platform chooses to run one. |

### Hostname-level egress control is out of scope

DDO does **not** restrict outbound connections by hostname (e.g. "only
`github.com`, `ghcr.io`, `raw.githubusercontent.com`"), and does not ship a source
allowlist or a proxy. This is deliberate:

- **It does not address the main risk.** The real risk of live fetching is
  applying untrusted *content*. `github.com` and `ghcr.io` are multi-tenant — an
  allowlist of those hostnames admits an attacker's repo as readily as yours, and
  the network layer cannot tell `github.com/sap/...` from
  `github.com/attacker/...` because the path is inside TLS. Content trust belongs
  where the source is chosen (RBAC + repo review), not at the packet layer.
- **It matches the community norm.** Neither Argo CD nor Flux ships hostname
  egress control; both leave egress to the platform and treat source trust as the
  user's (Argo CD's admin-configured `sourceRepos`, Flux's RBAC on source
  objects).
- **No reliable mechanism exists on the target.** DDO runs in a shoot
  control-plane namespace with no Istio sidecar (so `ServiceEntry` /
  `REGISTRY_ONLY` never applies), Cilium is not installed (`toFQDNs`
  unavailable), and Calico OSS has no native FQDN egress (`destination.domains`
  is Enterprise/Cloud only). Core/Calico `NetworkPolicy` can express CIDR/port
  rules but not hostnames. Reliable hostname allowlisting would require a
  **platform forward proxy** that DDO consumes via `HTTPS_PROXY` / `NO_PROXY` —
  if and when the platform runs one.

### Residual risks (accepted)

- Without an allowlist or RBAC restriction, any repo a DDO resource references can
  be fetched.
- Unpinned refs (branches, tags) can change after review unless SHA pinning is
  enabled.
- Nested remote bases inside a referenced repo are fetched too and are **not**
  checked by the preflight policy engine (it sees only the locally reachable tree).
- DDO can reach any endpoint the **union** of applicable egress policies allows. The
  chart's default manager-pod labels allow both `to-public-networks` and
  `to-private-networks`, so private/internal ranges are reachable by default; the
  cloud-metadata endpoint (`169.254.169.254`) and other internal services are only
  blocked if the platform drops `to-private-networks` (and whatever label covers
  link-local) behind a deny-all baseline.

## Runtime requirements

### Submodules

Krusty's git cloner runs `git submodule update --init --recursive` on every
cloned base unless the reference opts out. If a base has submodules you do not
need (or the submodule remote is unreachable from the operator Pod), append
`submodules=false` to the reference so the clone does not attempt to fetch them:

```yaml
resources:
  - github.com/example/platform-config//base?ref=0123456789abcdef0123456789abcdef01234567&submodules=false
```

Without `submodules=false`, a base whose submodules cannot be fetched fails the
render.

### Writable temp directory (`TMPDIR`)

Krusty clones remote bases under the process temp directory
(`os.MkdirTemp("", "kustomize-…")`, i.e. `/tmp`). The operator runs with
`readOnlyRootFilesystem: true`, so `/tmp` is read-only and the clone would fail
with `Read-only file system`. The deployment therefore sets
`TMPDIR=/var/run/ddo-source` — the same writable scratch `emptyDir` used for the
`--source-scratch-dir` Helm loader — in both `config/manager/manager.yaml` and
`chart/values.yaml`. If you build your own manifests, set `TMPDIR` to a writable
mounted path and size the volume to hold the largest clone (the stock
`sizeLimit` is `256Mi`).

When `--source-scratch-dir` is set, the operator also exports `TMPDIR` to that
path **in-process** at startup, so a Helm-values override of the environment
variable cannot silently drop it. Note that both the `go-git` root clone **and**
krusty's remote-base clones land under this directory, not just krusty's — so
size the volume for the combined peak. For reference, the `ipam-capi`
equivalence fixture's render peaked at roughly 100 MiB of the 256 MiB default,
so memory, not disk, is the first limit it hits (see the sizing note in the
review discussion).
