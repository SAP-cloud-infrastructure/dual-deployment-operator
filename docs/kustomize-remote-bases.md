# Authoring Kustomize Overlays with Remote Bases

This guide describes the remote `resources`, `components`, and `bases` supported
by the dual-deployment-operator's Kustomize renderer.

## Fetch and render model

The operator resolves the configured seed or shoot overlay, then runs krusty.
Krusty fetches transitive remote bases live during each `Render()`; seed and shoot
are rendered separately, so remote content is fetched as part of each render.
There is no hermetic offline render or SHA-keyed cache for remote Kustomize bases.
A remote fetch failure fails the render rather than silently omitting content.

For example, a GitHub remote directory may be referenced as:

```yaml
resources:
  - https://github.com/example/platform-config//base?ref=0123456789abcdef0123456789abcdef01234567
```

The operator runs a preflight over the reachable kustomization tree before
krusty fetches remote content. It classifies remote references and rejects
unsupported forms, including SSH URLs, embedded user information, URL fragments,
and ambiguous repository paths. Errors are sanitized and do not echo full URLs or
credentials.

## Pin refs for reproducibility

Pin remote bases to immutable commit SHAs where possible. Argo CD's [Kustomize
Best Practices](https://argo-cd.readthedocs.io/en/stable/user-guide/best_practices/)
warn that remote bases can “suddenly change meaning, even without any changes to
your own Git repository.” Since this operator fetches remote bases live, the same
reproducibility caveat applies here: a branch or tag can resolve to different
content on a later render.

SHA enforcement is opt-in through the operator-level flag:

```text
--kustomize-require-sha-pinned-remote-bases
```

The default is `false`, which permits tags and branches. When set to `true`, the
preflight requires every reachable remote reference to use a 40-character
lowercase hexadecimal commit SHA and rejects other refs before any remote fetch.
The flag governs transitive remote bases, not the configured source-root URL.

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
[Phase 9's Gardener egress prerequisite](implementation.md#phase-9-deployment-charts)
in the [implementation guide](implementation.md).

The operator image includes `git` on `PATH` because krusty's remote git cloner
shells out to the Git executable.
