## MODIFIED Requirements

### Requirement: Pinned ref resolution is fail-closed for tags, branches, and SHAs

The resolver MUST honor the pinned `?ref=` extracted from the URL and MUST fail closed if the ref cannot be resolved — it MUST NOT silently build the default branch (`HEAD`). A tag or branch ref MAY be fetched via a shallow clone (`ReferenceName` + `Depth: 1`). A commit SHA ref MUST be fetched by initializing the repo, fetching the advertised branch and tag refs to **full depth** (NOT `Depth: 1`), then checking out the commit hash. Full depth is required because a shallow fetch downloads only each ref's tip commit, so an arbitrary historical SHA (a commit that is not the tip of any branch or tag) would be absent and the checkout would fail with "object not found". The checkout remains fail-closed: if the hash is genuinely absent from the fetched history, `Resolve` returns an error and does NOT fall back to the default branch.

#### Scenario: tag ref builds the pinned revision

- **WHEN** the `url` pins `?ref=v1.1.0` (a tag)
- **THEN** the resolver fetches that tag and the resolved root reflects the `v1.1.0` revision

#### Scenario: commit SHA ref is fetched and checked out

- **WHEN** the `url` pins `?ref=<40-char-sha>`
- **THEN** the resolver fetches that specific commit and checks out its hash
- **AND** the resolved root reflects exactly that commit

#### Scenario: arbitrary historical SHA (not a ref tip) is resolvable

- **WHEN** the `url` pins `?ref=<sha>` naming a commit that is NOT the tip of any branch or tag (an older commit in history)
- **THEN** the resolver still fetches enough history to check that commit out (it MUST NOT use a shallow tip-only fetch that would omit it)
- **AND** the resolved root reflects exactly that historical commit

#### Scenario: unresolvable ref fails closed

- **WHEN** the pinned `?ref=` names a tag/branch/SHA that does not exist on the remote
- **THEN** `Resolve` returns an error
- **AND** it does NOT fall back to building the default branch
