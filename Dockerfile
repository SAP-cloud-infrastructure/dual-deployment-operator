# SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
#
# SPDX-License-Identifier: Apache-2.0

# Build the manager binary
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Build
# the GOARCH has no default value to allow the binary to be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# ── git provider ──────────────────────────────────────────────────────────────
# krusty (sigs.k8s.io/kustomize/api) resolves remote kustomize bases by shelling
# out to `git` via ClonerUsingGitExec — a pure-Go git implementation is NOT used
# for this code path. The operator therefore needs a real git binary at runtime.
#
# We install git from the same Debian 13 (trixie) base that distroless/base-debian13
# is built from, so that glibc versions match exactly (no ABI mismatch at runtime).
# The binary and its shared-library closure are collected here; only those files are
# copied into the final distroless stage — no shell, no package manager, no apt.
#
# Pattern reference: GoogleContainerTools/distroless README § "multi-stage builds"
# (https://github.com/GoogleContainerTools/distroless) — copy binaries and their
# shared-library closure from a builder stage into a distroless base image.
FROM debian:trixie-slim AS git-provider
RUN apt-get update && \
    apt-get install -y --no-install-recommends git ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Collect the shared-library closure of /usr/bin/git into /git-libs so the final
# COPY step is a single, auditable directory.  We use `ldd` + awk to extract only
# absolute library paths, then copy each with its full directory tree preserved.
RUN mkdir -p /git-libs && \
    ldd /usr/bin/git | awk '/=>/ { print $3 } /^\// { print $1 }' | \
      grep -E '^/' | sort -u | \
      xargs -I '{}' cp --parents '{}' /git-libs

# ── final image ───────────────────────────────────────────────────────────────
# Use distroless/base (not static) as the final base image.
# distroless/base-debian13 adds glibc, libssl, and related runtime libraries on
# top of distroless/static-debian13; this is required because the Debian git
# binary is dynamically linked against glibc.  We accept the slightly larger
# image size (~20 MiB vs ~2 MiB) in exchange for a real git binary that krusty
# can exec for remote kustomize-base resolution.
#
# distroless/base-debian13:nonroot runs as UID 65532 (nonroot) — same as the
# previous distroless/static:nonroot tag — so the USER directive below is
# unchanged and no Kubernetes securityContext changes are needed.
FROM gcr.io/distroless/base-debian13:nonroot
WORKDIR /

# Manager binary (statically linked Go binary — unchanged from before)
COPY --from=builder /workspace/manager .

# git binary, git-core helpers, and shared-library closure
COPY --from=git-provider /usr/bin/git /usr/bin/git
COPY --from=git-provider /usr/lib/git-core /usr/lib/git-core
COPY --from=git-provider /git-libs /

USER 65532:65532

# Keppel's account validation policy requires a `source_repository` manifest
# label (or an in-toto attestation layer) to accept a push. This is a custom
# Keppel label key, distinct from org.opencontainers.image.source. The
# org.opencontainers.image.{created,revision,version} labels are injected at
# build time by docker/metadata-action in CI, so they are not set here.
LABEL source_repository="https://github.com/sap-cloud-infrastructure/dual-deployment-operator" \
  org.opencontainers.image.url="https://github.com/sap-cloud-infrastructure/dual-deployment-operator"

ENTRYPOINT ["/manager"]
