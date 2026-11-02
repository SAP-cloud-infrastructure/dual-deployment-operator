# Task 6 Report — Git binary in the operator image

## Summary

Added a `git` binary (and its shared-library closure) to the operator's runtime
image so that krusty's `ClonerUsingGitExec` can shell out to `git` when resolving
remote kustomize bases.

---

## Why `git` is needed

`sigs.k8s.io/kustomize/api` v0.21.1 resolves remote bases via
`ClonerUsingGitExec` in `sigs.k8s.io/kustomize/pkg/git`. This function shells
out to a `git` binary on `$PATH` (confirmed in the kustomize source at
[pkg.go.dev/g.szar.club/kubernetes-sigs/kustomize/pkg/git#ClonerUsingGitExec](https://pkg.go.dev/g.szar.club/kubernetes-sigs/kustomize/pkg/git)):

> ClonerUsingGitExec uses a local git install, as opposed to say, some remote
> API, to obtain a local clone of a remote repo.

The operator's `gitResolver` (go-git, pure Go) handles the **initial repository
clone** for the `KustomizeSource`. krusty handles **inline remote-base
resolution** (`resources:` or `bases:` entries that are remote URLs) during the
actual kustomize build phase. These are two separate code paths; go-git does not
substitute for the krusty exec path.

---

## Decision: `distroless/base-debian13:nonroot` + multi-stage git copy

**Source**: [GoogleContainerTools/distroless README — "multi-stage builds"](https://github.com/GoogleContainerTools/distroless)

> The basic idea is that you'll have one stage to build your application
> artifacts, and insert them into your runtime distroless image.

### Why `distroless/base` instead of `distroless/static`

`distroless/static` contains no shared libraries beyond what fully-static
binaries need. Debian's `git` binary is **dynamically linked** against glibc
(`libc.so.6`, `libpcre2`, `libz`, `libcurl-gnutls`, etc.). Running it on
`distroless/static` would produce `error while loading shared libraries` at
exec time.

`distroless/base-debian13` adds glibc (`libc.so.6`, `libssl`, `libcrypto`)
and the C runtime onto the static base — enough for a Debian git binary to run,
provided we also copy git's own HTTPS-transport libs (`libcurl-gnutls`, etc.).

### Why Debian 13 (trixie) for the `git-provider` stage

`distroless/base-debian13` is built from Debian 13 (trixie) packages. Using
`debian:trixie-slim` as the git provider guarantees **exact glibc version
parity** between the shared libraries the git binary was compiled against and
the glibc present in the final image. Using a different Debian release (e.g.
bookworm / debian 12) would risk glibc version skew.

### What is copied

| Source | Destination | Purpose |
|---|---|---|
| `/usr/bin/git` | `/usr/bin/git` | main git binary |
| `/usr/lib/git-core/` | `/usr/lib/git-core/` | git sub-commands (`git-remote-https`, etc.) |
| `/git-libs/` (ldd closure) | `/` | shared libraries (`libc`, `libcurl-gnutls`, `libz`, `libpcre2-posix`, etc.) |

The ldd closure is collected by scanning **both** `/usr/bin/git` and every ELF
executable under `/usr/lib/git-core/` (e.g. `git-remote-https`, `git-remote-http`):

```sh
find /usr/bin/git /usr/lib/git-core/ -type f -executable \
     -exec sh -c 'file "$1" | grep -q ELF' _ {} \; -print | \
  xargs -I '{}' ldd '{}' 2>/dev/null | \
  awk '/=>/ { print $3 } /^\// { print $1 }' | \
  grep -E '^/' | sort -u | \
  xargs -I '{}' cp --parents '{}' /git-libs
```

Scanning the git-core helpers is essential: `git-remote-https` links against
`libcurl-gnutls` (for HTTPS transport) which `/usr/bin/git` itself does not
directly depend on. Omitting this step would cause HTTPS remote-base fetches to
fail at runtime with `error while loading shared libraries: libcurl-gnutls.so.4`.

No shell, package manager, or unrelated Debian tooling is copied into the final
image. The distroless posture is preserved.

---

## Image size impact

| Stage | Approximate size |
|---|---|
| `distroless/static:nonroot` (before) | ~2 MiB |
| `distroless/base-debian13:nonroot` (after) | ~20 MiB |
| + git binary + libs + git-core | +~30 MiB |
| **Total runtime image** | **~50 MiB** |

This is accepted: a Kubernetes operator that needs to clone remote git repos
cannot be purely static. The image remains well below a typical Alpine+shell
image (~100–200 MiB) and retains all distroless security properties (no shell,
no package manager, no setuid binaries).

---

## Fail-closed assertion (Task 6.2 requirement)

If the `git` binary is ever missing or broken at runtime, krusty emits:

```
no 'git' program on path
```

This error is **already caught and sanitized** by `sanitizeKrustyError()` (Task 3)
before surfacing to the operator's reconciliation log or status condition. The
raw message is dropped; the operator surfaces:

```
remote fetch or build failed: krusty encountered an error building the kustomization
(details omitted to prevent credential/URL leakage; check git access and remote base availability)
```

### Coverage

The existing test `TestKrustyRunWrapper_FailClosedNeverSilentPartialRender` in
`internal/source/kustomize_fetch_wrapper_test.go` already contains:

```go
{"git-not-found", fmt.Errorf("no 'git' program on path")},
```

This case verifies that the error is non-nil (fail-closed invariant). Task 6
adds `internal/source/git_binary_test.go` with two **dedicated** tests:

- `TestGitMissingError_SanitizedFailClosed` — unit-level: verifies the sanitized
  message does NOT echo `"no 'git'"` or `"program on path"`.
- `TestGitMissingError_EndToEndSanitized` — integration-level: verifies `Render()`
  propagates the sanitized error and returns nil manifests (fail-closed invariant).

Both tests pass (`go test ./internal/source/...` — all pass).

**No new Go code changes needed** — Task 3's existing `sanitizeKrustyError` is
fully sufficient. No changes to `kustomize.go` were required.

---

## Verification

### Automated (unit tests)

```bash
go test ./internal/source/... -run "TestGitMissing" -v
# PASS: TestGitMissingError_SanitizedFailClosed
# PASS: TestGitMissingError_EndToEndSanitized

go test ./internal/source/...  # full suite
# ok  github.com/SAP-cloud-infrastructure/dual-deployment-operator/internal/source
```

### Manual container verification (cannot run Docker in this sandbox)

The following procedure must be run before promoting a new image to production:

**Step 1 — Build the image:**
```bash
docker build -t ddo:task6-test .
```

**Step 2 — Verify git is present and executable:**
```bash
docker run --rm ddo:task6-test /usr/bin/git version
# Expected output: git version 2.x.y
```

**Step 3 — Verify HTTPS transport works (requires network):**
```bash
docker run --rm ddo:task6-test /usr/bin/git ls-remote https://github.com/kubernetes-sigs/kustomize HEAD
# Expected: prints a SHA + HEAD ref, exit 0
# This exercises git-remote-https and its libcurl-gnutls dependency.
```

**Step 4 — Verify manager binary still runs:**
```bash
docker run --rm --entrypoint=/usr/bin/git ddo:task6-test version
docker run --rm ddo:task6-test  # should print usage/error (no kubeconfig) — exit non-zero but not "exec format error"
```

**Step 5 — Verify non-root user:**
```bash
docker run --rm --entrypoint="" ddo:task6-test /usr/bin/id
# (will fail — no /usr/bin/id in distroless — that's expected)
# Alternative: check image metadata
docker inspect ddo:task6-test | jq '.[0].Config.User'
# Expected: "65532:65532"
```

**Step 6 — e2e kind test (optional, network-dependent):**
Apply a `KustomizeSource` CR with a remote kustomize base reference (e.g. pointing
to a real git repo at a pinned SHA). Verify the operator reconciles successfully
and the rendered manifests contain the expected resources from the remote base.

---

## Files changed

| File | Change |
|---|---|
| `Dockerfile` | New `git-provider` stage (debian:trixie-slim); switch final stage to `distroless/base-debian13:nonroot`; copy git binary + git-core + **full recursive shared-lib closure** (including libcurl-gnutls for HTTPS transport) |
| `internal/source/git_binary_test.go` | New — dedicated fail-closed tests for git-missing error path |
| `.superpowers/sdd/plan/task-6-report.md` | This file |
