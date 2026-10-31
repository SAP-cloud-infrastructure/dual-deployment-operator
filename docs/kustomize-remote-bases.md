<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Authoring Kustomize remote bases

DDO acquires Git content before running Kustomize: discover bases, resolve refs to
commit SHAs, fetch, stage local copies, preflight loading paths, then render. It does
not delegate remote acquisition to krusty.

## When to reference upstream

Use a remote base only when its rendered upstream resources match the shape you
intend to deploy. Pinning content does not establish that equivalence. Compare the
resource inventory and specifications, including RBAC, CRDs, webhooks and deployment
configuration. If upstream differs, keep a reviewed local adaptation or vendor the
required files rather than assuming a version pin makes it suitable.

An ipam-capi-style operand can keep `seed/` and `shoot/` overlays locally and reference
selected provider directories as remote bases. Select each concrete upstream directory
and review its transitive contents. CRDs from a separate Cluster API repository need
their own reviewed Git base or vendored files; raw HTTP YAML references are not
supported. Local ipam-capi-shaped integration coverage is not a live comparison with
the upstream operator or a deployed cluster.

## Remote-base syntax

Remote entries in `resources`, `components` and legacy `bases` use:

```text
https://host/organization/repository//selected/directory?ref=REF
https://host/organization/repository?ref=REF
github.com/organization/repository//selected/directory?ref=REF
```

GitHub shorthand also accepts the conventional path after `organization/repository`.
Prefer the explicit `//` separator to make the repository boundary clear. This is a
deliberate subset of Kustomize's Git syntax, not its complete URL grammar. SSH,
`git::` prefixes, embedded credentials, fragments and additional transport query
options are rejected. Ambiguous repository paths must use the explicit separator.

Every base needs exactly one nonempty `ref`. Tags and branches are resolved to a full
commit SHA before fetching; they can resolve differently on a later reconcile.
Use a full 40-character commit SHA for stability across reconciles. Missing or
unresolvable refs fail the render; there is no default-branch fallback.

All three Kustomize filenames are recognized: `kustomization.yaml`,
`kustomization.yml` and `Kustomization`. Local directories and remote bases are
traversed transitively. Cycles, paths escaping a repository and escaping symlinks
are rejected. Local sibling references inside the same repository are supported.

The CR's root URL is checked for a ref at admission. At rendering time it must also
use the same supported syntax as bases: extra query options such as `timeout` or
`submodules` are rejected, even if admission accepts them. Admission cannot inspect
Git contents: validation of transitive bases and loading paths happens during rendering.

## Credentials and caches

Set `authSecretRef` on the Kustomize source; never put credentials in a URL. The Secret
uses the existing `username`/`password`/`token` contract, with token taking precedence.
Credentials apply only to the root HTTPS origin: scheme, hostname and effective port.
Bases on another origin are fetched anonymously. Cross-origin Git redirects are
rejected rather than forwarding credentials. Private bases needing a different
credential set must be vendored or restructured; there is no per-base Secret map.

Snapshot identity includes canonical repository, resolved SHA and current credentials'
authorization context. Anonymous content can be shared; authenticated content is
shared only within the same context. Secret reads and mutable-ref resolution still
happen before cache use. Rotation changes identity; credential resolution failures
cannot return cached private content. Raw credentials are not cache keys.
An unchanged credential context with a resident full-SHA snapshot does not trigger
a fresh server ACL check; revocation without a credential change is observed on a
subsequent network fetch, not necessarily on each cache hit.

The in-process snapshot cache retains at most 8 MiB and 32 entries per shared Git
resolver. Eviction and oversized content can cause another fetch. Cached snapshots
are independent of temporary checkouts. Render-cache keys additionally include the
associated transitive identities and rendering inputs; a moved base tag invalidates
an otherwise unchanged root's rendered result. Preparation precedes render-cache
lookup, so a render hit is not a bypass for current refs, credentials or preflight.

## Accepted inputs and offline preflight

HTTP file loads are rejected, including resource YAML, patches, OpenAPI files,
transformer configurations, CRDs, replacements, generator files and environment files.
Vendor these files or put them in a reviewed Git base. Supplemental builtin
generator/transformer/validator configurations are also checked; external plugins
and Helm execution remain disabled.

Unknown Kustomization fields, duplicate keys, multiple documents or multiple
recognized filenames fail closed rather than being silently lost during rewriting.
Supplemental configuration directories and unreviewed builtin kinds are unsupported.
Absolute loading paths are rejected; use repository-relative paths.

This preflight covers accepted inputs under the pinned Kustomize dependency. It is
**not an OS-enforced network sandbox**. Loading-path coverage must be reviewed when
the dependency changes. Acquisition still needs network access; the operator's
existing egress policy is unchanged.

## Scratch capacity

Root/base checkouts and staging use `--source-scratch-dir`, backed by the chart's
existing `source-scratch` emptyDir. The configurable default is `256Mi`. Size for
full Git histories, working-tree staging copies, fetch overhead and concurrent Helm
or reconcile work, with sufficient node ephemeral storage. Arbitrarily large
repositories are not guaranteed to fit. Snapshot limits do not bound go-git's pack
memory or total process memory. Temporary work is removed after success, errors,
cancellation and render-cache hits.
Krusty's local build is not interruptible: cancellation during that build is
observed when it returns, before publishing a result and cleaning up.
