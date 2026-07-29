## RENAMED Requirements

- FROM: `### Requirement: Pull to a temporary directory and clean up (no caching)`
- TO: `### Requirement: Pull to a temporary directory and clean up (loader-level, no chart caching)`

## MODIFIED Requirements

### Requirement: Pull to a temporary directory and clean up (loader-level, no chart caching)

The loader MUST pull each chart into a temporary directory, load it via `loader.Load` into a `*chart.Chart`, and remove the temporary directory after the chart is loaded. The loader itself MUST NOT cache charts — every `Load` fetches fresh into a fresh temporary directory. Render-result caching is a separate concern performed one layer above the loader by the `cachingSource` decorator (see the `render-cache` capability), keyed by the resolved OCI digest via the loader's `ResolveID` method (see the `source-id-resolution` capability); a cache hit skips the `Load` entirely, but any `Load` that does run still fetches fresh and cleans up. This is a layering statement, not a contradiction: the loader is stateless per call, while caching lives above it.

#### Scenario: each Load fetches fresh and cleans up its temp dir

- **WHEN** `Load` is called for a chart
- **THEN** the loader pulls the chart `.tgz` into a temporary directory and loads it
- **AND** removes the temporary directory before returning
- **AND** does not retain the chart for a subsequent call (a second `Load` of the same `repo|name|version` pulls again)

#### Scenario: render-layer cache can skip Load without changing loader semantics

- **WHEN** the `cachingSource` decorator has a cached render for the resolved chart digest, mode, inputs, and namespace
- **THEN** `Load` is not invoked for that render
- **AND** WHEN the cache misses and `Load` is invoked, the loader still fetches fresh into a temporary directory and cleans up as specified
