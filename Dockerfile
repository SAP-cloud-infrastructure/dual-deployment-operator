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

# ── final image ───────────────────────────────────────────────────────────────
# krusty (sigs.k8s.io/kustomize/api) resolves remote kustomize bases by shelling
# out to `git` via ClonerUsingGitExec — a pure-Go git implementation is NOT used
# for this code path. The operator therefore needs a real git binary at runtime,
# AND a POSIX shell: git's `git submodule` is a /bin/sh script (git-submodule),
# and krusty's cloner always runs `git submodule update --init --recursive`
# unless the base ref carries `submodules=false`. A shell-less distroless image
# cannot execute git-submodule and fails every default-form git base with
# exit 128 (reported in PR #62 review).
#
# We therefore ship git on a minimal, shell-bearing Debian base rather than a
# distroless image — aligning with the industry norm for git-fetching Kubernetes
# controllers (Argo CD ships git + a shell + Git LFS on a Debian/Ubuntu base;
# Flux's source-controller likewise bundles a real git). A hand-assembled
# shared-library closure copied into distroless was tried previously but could
# not satisfy the git-submodule shell requirement without re-introducing a shell,
# so the minimal-Debian base is both simpler and correct.
#
# Using an apt-based final image also means the dpkg package database
# (/var/lib/dpkg/status) natively records git, libcurl and gnutls, so container
# CVE scanners can see and assess them (the previous distroless image copied the
# binaries without any dpkg metadata, making them invisible to scanners —
# PR #62 review finding).
FROM debian:trixie-slim
WORKDIR /

# Install git (+ its /bin/sh git-submodule helper) and CA certificates for HTTPS
# remote bases. --no-install-recommends keeps the image minimal; we drop the apt
# lists afterwards to avoid shipping a stale package index.
RUN apt-get update && \
    apt-get install -y --no-install-recommends git ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Fail-closed build-time smoke tests (offline, no network required so the build
# stays hermetic): abort the build if the git binary is missing/broken or if the
# git-submodule /bin/sh helper cannot be executed. These turn the runtime
# failures from PR #62 (exit 128 on `git submodule` in the shell-less image) into
# build-time failures. The HTTPS-fetch path is exercised separately by the
# combined-image render test (which needs network and therefore does not belong
# in a hermetic image build).
RUN ["git", "version"]
# `git submodule` is the /bin/sh helper that the shell-less image could not run.
# Running `git submodule status` inside an empty, freshly-initialized repository
# executes that helper (and its `git submodule--helper` subprocess) without any
# network; it exits 0 only if /bin/sh and the helper are present and runnable —
# which is exactly what failed with exit 128 in the shell-less image. We init a
# throwaway repo in a temp dir, run the helper, then remove the dir.
RUN ["git", "-C", "/tmp", "init", "-q", "smoke-submodule-check"]
RUN ["git", "-C", "/tmp/smoke-submodule-check", "submodule", "status"]
RUN ["rm", "-rf", "/tmp/smoke-submodule-check"]
# Confirm git-remote-https (the HTTPS transport helper, dynamically linked
# against libcurl) is installed, without performing a network fetch.
RUN ["test", "-x", "/usr/lib/git-core/git-remote-https"]

# Manager binary (statically linked Go binary)
COPY --from=builder /workspace/manager /manager

# Run as a non-root user. Debian's base image has no dedicated nonroot account at
# a fixed UID like distroless does, so we create one at UID 65532 (the same UID
# the previous distroless/base-debian13:nonroot image used) to keep the
# Kubernetes securityContext (runAsUser: 65532) unchanged.
RUN groupadd --gid 65532 nonroot && \
    useradd --uid 65532 --gid 65532 --no-create-home --shell /usr/sbin/nologin nonroot
USER 65532:65532

# Keppel's account validation policy requires a `source_repository` manifest
# label (or an in-toto attestation layer) to accept a push. This is a custom
# Keppel label key, distinct from org.opencontainers.image.source. The
# org.opencontainers.image.{created,revision,version} labels are injected at
# build time by docker/metadata-action in CI, so they are not set here.
LABEL source_repository="https://github.com/sap-cloud-infrastructure/dual-deployment-operator" \
  org.opencontainers.image.url="https://github.com/sap-cloud-infrastructure/dual-deployment-operator"

ENTRYPOINT ["/manager"]
