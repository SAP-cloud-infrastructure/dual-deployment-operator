# kustomize-remote-base-policy Specification

## Purpose
Define the operator-side policy that governs how a `KustomizeSource` may
reference remote bases. Before invoking krusty, the operator classifies every
remote reference it can parse from the top-level kustomization, rejects
unsupported or ambiguous forms with sanitized errors, and — when strict mode is
enabled — enforces 40-character commit-SHA pinning on those top-level
references. This matches the Flux/Argo CD model: pinning is enforced on the
references the operator can see up front, not on transitive bases nested inside
remote repositories (which krusty resolves internally and no tool pre-enforces);
those are contained operationally by NetworkPolicy egress and least-privilege
credentials. The policy is always-on for classification and fail-closed
sanitized fetch; SHA-pin enforcement is opt-in via
`--kustomize-require-sha-pinned-remote-bases` (default `false`).
## Requirements
### Requirement: Remote reference classification before render

Before invoking krusty, the operator MUST walk the reachable kustomization tree
(the resolved root and any local directories it references) and classify every
`resources`, `components`, and `bases` entry as one of: a local path, a remote
git-directory base, or a remote single-file reference. Classification MUST use the
bounded public-API grammar (HTTPS repositories with a mandatory `?ref=`, plus
GitHub shorthand normalized to HTTPS) and MUST reject unsupported remote forms
(SSH, embedded userinfo credentials, URL fragments, ambiguous repository paths)
with a sanitized error that does not echo credentials. The walk MUST detect cycles
and MUST NOT follow references outside the resolved root boundary when walking
local directories.

#### Scenario: supported remote git base is classified and accepted

- **WHEN** a reachable kustomization lists a `base` of the form
  `github.com/org/repo//path?ref=<ref>` (scheme-less GitHub shorthand)
- **THEN** the preflight classifies it as a remote git-directory base
- **AND** the render proceeds to krusty without error from classification

#### Scenario: SSH or credential-embedding remote form is rejected

- **WHEN** a reachable kustomization lists a remote reference using an SSH URL or
  one embedding `user:token@host` userinfo
- **THEN** the preflight fails the render with a sanitized error
- **AND** the error does not contain the credential value

#### Scenario: cycle in local references is rejected

- **WHEN** the reachable local kustomization directories form a reference cycle
- **THEN** the preflight fails the render with a cycle error rather than looping

---

### Requirement: Optional SHA-pin enforcement for remote references

The operator MUST expose an operator-level flag
`--kustomize-require-sha-pinned-remote-bases` (default `false`). When the flag is
`false`, remote references pinned by tag, branch, or SHA are all permitted (the
tag-permissive default, matching krusty and peer controllers). When the flag is
`true`, the preflight MUST reject — **before any remote fetch** — any remote
reference whose embedded ref is not a 40-character lowercase hexadecimal commit
SHA. Ref extraction MUST be: for a remote git base, the `?ref=` value; for a
`raw.githubusercontent.com` single-file URL, the commit-SHA path segment after
`<org>/<repo>`. In strict mode the ONLY accepted remote forms are (a) a
scheme-less GitHub shorthand git-directory reference (`github.com/org/repo//path?ref=<sha>`,
which krusty resolves via git so the SHA is honored) and (b) a canonical
`https://raw.githubusercontent.com/<org>/<repo>/<40-hex-sha>/<path>` single file
(an immutable blob). An `https://`/`http://` git-directory reference in a
file-first field (`resources`/`bases`) is rejected in strict mode because krusty
attempts an HTTP GET of the URL before falling back to git, so the `?ref=` is not
guaranteed to be honored (`components` is exempt — krusty resolves it via git
directly). A reference with no determinable ref MUST be rejected in strict mode.

#### Scenario: strict mode rejects a tag-pinned remote base

- **WHEN** the flag is `true` and a reachable kustomization references a remote
  base pinned by a tag (not a 40-hex SHA)
- **THEN** the preflight fails the render before any fetch
- **AND** the error states a 40-character lowercase hexadecimal SHA ref is required

#### Scenario: strict mode accepts a SHA-pinned remote base

- **WHEN** the flag is `true` and the remote base is pinned by a 40-hex commit SHA
- **THEN** the preflight accepts it and the render proceeds to krusty

#### Scenario: permissive default accepts a tag-pinned remote base

- **WHEN** the flag is `false` (default) and the remote base is pinned by a tag
- **THEN** the preflight accepts it and the render proceeds to krusty

---

### Requirement: Fail-closed sanitized remote fetch

A remote reference that krusty cannot fetch (unreachable host, git fetch failure,
or a non-2xx remote single-file response) MUST fail the render closed. The
operator MUST NOT emit a silent partial render that omits the unreachable
reference, and the surfaced error MUST be sanitized so it never contains
credential material. Non-credential diagnostic context (host, repository, path,
exit code) MAY be preserved to keep the error actionable (redact-and-preserve).

#### Scenario: unreachable remote base fails the render

- **WHEN** a classified remote reference cannot be fetched during krusty `Run`
- **THEN** the render returns an error
- **AND** no manifest set is produced that silently omits the unreachable content

#### Scenario: fetch error is sanitized

- **WHEN** the underlying git/HTTP fetch error contains a URL or credential hint
- **THEN** the surfaced render error does not contain the credential value

