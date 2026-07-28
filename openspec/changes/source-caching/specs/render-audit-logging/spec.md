## ADDED Requirements

### Requirement: Log the resolved content id per render

The system MUST emit a structured log line recording the resolved content id used for each render, so
that every render is auditable and reproducible even when the CR pins a mutable ref (e.g. `?ref=main`).
The log line MUST identify the source and its resolved id (git commit SHA / OCI manifest digest / HTTP
chart digest-or-version). The system MUST NOT log credentials or rendered values. This change MUST NOT
add a CRD `status` field for the resolved id (logging only).

#### Scenario: resolved id is logged on render

- **WHEN** a render resolves a source to a content id
- **THEN** a structured log line records the source and its resolved id
- **AND** the line contains no credentials and no rendered values

#### Scenario: mutable ref still yields a reproducible audit record

- **WHEN** a CR pins a mutable ref such as `?ref=main`
- **THEN** the logged resolved id is the concrete commit SHA (or digest) that was actually rendered
- **AND** no CRD `status` field is written for the resolved id
