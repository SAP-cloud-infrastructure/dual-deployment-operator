# Task 3 Report: Fail-closed sanitized fetch wrapper around krusty Run

## What was implemented

### RED Phase (kustomize_fetch_wrapper_test.go)

Added 10 new tests in `internal/source/kustomize_fetch_wrapper_test.go` that covered:

1. **Unit tests for `sanitizeKrustyError`** (function-level):
   - `TestSanitizeKrustyError_NilPassthrough` — nil in → nil out (success path unaffected)
   - `TestSanitizeKrustyError_GitExecURLNotEchoed` — raw git-exec error with hostname/path → sanitized
   - `TestSanitizeKrustyError_CredentialBearingURLNotEchoed` — embedded `user:token@host` → credential dropped
   - `TestSanitizeKrustyError_HTTPErrorSanitized` — HTTP error with internal hostname → sanitized
   - `TestSanitizeKrustyError_NonFetchErrorPassedThrough` — any krusty error → non-nil result

2. **End-to-end wrapper tests via `krustyRunHook`**:
   - `TestKrustyRunWrapper_FetchFailureProducesRenderError` — Render() is fail-closed; URL not echoed
   - `TestKrustyRunWrapper_CredentialBearingFetchError` — token in raw error not surfaced
   - `TestKrustyRunWrapper_HTTPNon2xxProducesRenderError` — HTTP 404 from krusty → sanitized render error
   - `TestKrustyRunWrapper_SuccessUnaffected` — no hook → success path unchanged
   - `TestKrustyRunWrapper_FailClosedNeverSilentPartialRender` — 5 subtests covering all error variant types

All tests were COMPILE-FAILING (RED) before the implementation was added, confirming the TDD cycle.

### GREEN Phase (kustomize.go)

Changes to `internal/source/kustomize.go`:

1. **`krustyRunHook` var** — package-level `error` var (nil in production). When non-nil, `runKrusty()` returns this error instead of calling real `krust.Run`. Enables test injection of raw-URL errors without network access.

2. **`runKrusty()` function** — thin extraction of `krust.Run` that:
   - Checks `krustyRunHook` and returns it if set (test path)
   - Otherwise delegates to `kust.Run(filesys.MakeFsOnDisk(), root)` (production path)
   - Return type is `resmap.ResMap` (correct; `krust.Run` returns `resmap.ResMap`, not `krusty.ResMap`)

3. **`sanitizeKrustyError()` function** — uniform sanitization:
   - Returns `nil` iff `err == nil` (success unaffected)
   - For any non-nil error: returns `errors.New(...)` with a fixed stable text, never `fmt.Errorf(...%w..., err)` — so the raw error is **not** accessible via `errors.Unwrap` or `errors.As`
   - Design: uniform drop (not pattern-matched) is strictly safer than partial sanitization — any classification attempt risks missing a new krusty error format

4. **Wiring in `Render()`** — `kust.Run(...)` replaced with `runKrusty(kust, root)`, and the error handling now calls `sanitizeKrustyError(err)` before wrapping with mode context.

5. **`resmap` import** added (was missing — `krust.Run` returns `resmap.ResMap`).

6. **`kustomizeFetchHook` preserved** — left intact as required by brief.

## Test names

```
TestSanitizeKrustyError_NilPassthrough
TestSanitizeKrustyError_GitExecURLNotEchoed
TestSanitizeKrustyError_CredentialBearingURLNotEchoed
TestSanitizeKrustyError_HTTPErrorSanitized
TestSanitizeKrustyError_NonFetchErrorPassedThrough
TestKrustyRunWrapper_FetchFailureProducesRenderError
TestKrustyRunWrapper_CredentialBearingFetchError
TestKrustyRunWrapper_HTTPNon2xxProducesRenderError
TestKrustyRunWrapper_SuccessUnaffected
TestKrustyRunWrapper_FailClosedNeverSilentPartialRender
  /git-not-found
  /git-clone-fail
  /http-error
  /generic-error
  /nil-error
```

## Real test output (GREEN)

```
=== RUN   TestSanitizeKrustyError_NilPassthrough
--- PASS: TestSanitizeKrustyError_NilPassthrough (0.00s)
=== RUN   TestSanitizeKrustyError_GitExecURLNotEchoed
--- PASS: TestSanitizeKrustyError_GitExecURLNotEchoed (0.00s)
=== RUN   TestSanitizeKrustyError_CredentialBearingURLNotEchoed
--- PASS: TestSanitizeKrustyError_CredentialBearingURLNotEchoed (0.00s)
=== RUN   TestSanitizeKrustyError_HTTPErrorSanitized
--- PASS: TestSanitizeKrustyError_HTTPErrorSanitized (0.00s)
=== RUN   TestSanitizeKrustyError_NonFetchErrorPassedThrough
--- PASS: TestSanitizeKrustyError_NonFetchErrorPassedThrough (0.00s)
=== RUN   TestKrustyRunWrapper_FetchFailureProducesRenderError
--- PASS: TestKrustyRunWrapper_FetchFailureProducesRenderError (0.00s)
=== RUN   TestKrustyRunWrapper_CredentialBearingFetchError
--- PASS: TestKrustyRunWrapper_CredentialBearingFetchError (0.00s)
=== RUN   TestKrustyRunWrapper_HTTPNon2xxProducesRenderError
--- PASS: TestKrustyRunWrapper_HTTPNon2xxProducesRenderError (0.00s)
=== RUN   TestKrustyRunWrapper_SuccessUnaffected
--- PASS: TestKrustyRunWrapper_SuccessUnaffected (0.01s)
=== RUN   TestKrustyRunWrapper_FailClosedNeverSilentPartialRender
=== RUN   TestKrustyRunWrapper_FailClosedNeverSilentPartialRender/git-not-found
=== RUN   TestKrustyRunWrapper_FailClosedNeverSilentPartialRender/git-clone-fail
=== RUN   TestKrustyRunWrapper_FailClosedNeverSilentPartialRender/http-error
=== RUN   TestKrustyRunWrapper_FailClosedNeverSilentPartialRender/generic-error
=== RUN   TestKrustyRunWrapper_FailClosedNeverSilentPartialRender/nil-error
--- PASS: TestKrustyRunWrapper_FailClosedNeverSilentPartialRender (0.01s)
    --- PASS: TestKrustyRunWrapper_FailClosedNeverSilentPartialRender/git-not-found (0.00s)
    --- PASS: TestKrustyRunWrapper_FailClosedNeverSilentPartialRender/git-clone-fail (0.00s)
    --- PASS: TestKrustyRunWrapper_FailClosedNeverSilentPartialRender/http-error (0.00s)
    --- PASS: TestKrustyRunWrapper_FailClosedNeverSilentPartialRender/generic-error (0.00s)
    --- PASS: TestKrustyRunWrapper_FailClosedNeverSilentPartialRender/nil-error (0.00s)
PASS
ok  	github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source	0.785s
```

Full suite (no regressions): `ok  github.com/.../internal/source  14.319s`
Full build: `go build ./...` — no output (clean).

## Commit SHAs

- `8b7fbac` — feat(source): fail-closed sanitized wrapper around krusty Run

## Concerns

None. The uniform sanitization approach (always drop raw error message, never `%w`-wrap the raw error) is deliberately conservative. A caller observing `errors.Is(err, someKrustyError)` will not work — but this is correct by design: krusty's internal error types are not part of DDO's public API contract and forwarding them would create brittle coupling that risks future leakage.
