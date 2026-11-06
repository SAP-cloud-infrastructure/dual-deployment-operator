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
paths, and (in strict mode) bare `https://` git directories in resource-like
fields. Errors are sanitized: the error message preserves host/repo/path and exit
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

**Always rejected** (flag-independent):

- `http://` (cleartext) remote references
- `ssh://` or SCP-style (`user@host:path`) references
- References embedding URL userinfo (`user:token@host`)
- References with URL fragments (`#`)
- Username-prefixed refs (`user@` in the `?ref=` value)

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

The operator applies the following hardening unconditionally (flag-independent):

- **`GIT_ALLOW_PROTOCOL=https`** is set at process startup, constraining all
  git-driven fetches — including krusty-triggered remote bases and submodules —
  to HTTPS only. Any attempt to use `ssh://`, `git://`, `file://`, or other
  protocols at the git layer is denied.

- **`HardenDefaultHTTPTransport`** replaces `http.DefaultTransport` with a
  transport that imposes explicit timeouts: dial 10 s, TLS handshake 10 s,
  response-header receipt 15 s, per-read idle deadline 30 s, HTTP/1.1 forced.
  Because `http.DefaultTransport` is a process global, this also affects
  **Helm's OCI registry client** (`helm/pkg/registry.NewClient` clones
  `DefaultTransport` per call), so OCI chart pulls inherit the same bounds.
  The `go-git` root resolver and the kube clients are unaffected (they capture
  `DefaultTransport` before this call runs, or build their own transport,
  respectively).

> **Note for go-makefile-maker users:** the `'**'` branch-glob variant in
> `pull_request.branches` that supports stacked-PR bases is not emitted by the
> current version of `go-makefile-maker`. If you need broad branch-glob support,
> raise it upstream in `sapcc/go-makefile-maker`.

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
