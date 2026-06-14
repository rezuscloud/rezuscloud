# Multi-stage Dockerfile for rezuscloud management plane.
#
# Pure-Go build (CGO_ENABLED=0). Uses modernc.org/sqlite instead of mattn/go-sqlite3
# so no C toolchain is needed. The runtime image is distroless/static-debian12
# (smaller, no glibc dependency).
#
# Bundles the OpenTofu (`tofu`) binary: RezusCloud exec's it as a subprocess to
# reconcile infrastructure (ADR 22). The version is pinned by .opentofu-version,
# the SINGLE SOURCE OF TRUTH also consumed by the CI integration-test job, so
# the bundled binary and the tested binary can never drift. tofu is a statically
# linked Go binary, so it runs on distroless/static-debian12 (no libc needed).
#
# Used by:
#   - CI per-PR image builds (docker-pr job → ghcr.io/rezuscloud/rezuscloud:pr-<N>-<sha>)
#   - Local docker build for testing
#   - Same pattern as .github/Dockerfile.release (GoReleaser pipeline).
FROM golang:1.26-bookworm AS builder

ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown

WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

# Copy source.
COPY . .

# Pure-Go build: no CGO, fully static binary.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -ldflags="-s -w \
        -X github.com/rezuscloud/rezuscloud/version.Version=${VERSION} \
        -X github.com/rezuscloud/rezuscloud/version.GitCommit=${GIT_COMMIT} \
        -X github.com/rezuscloud/rezuscloud/version.BuildTime=${BUILD_TIME}" \
      -trimpath \
      -o /rezuscloud \
      .

# tofu stage: download the OpenTofu binary for the build arch, version-pinned by
# .opentofu-version (single source of truth). `tofu version` at the end verifies
# the download is a working binary before the image is assembled.
FROM alpine:3.20 AS tofu
ARG TARGETARCH
COPY .opentofu-version /tmp/.opentofu-version
RUN set -eux; \
    apk add --no-cache curl unzip ca-certificates; \
    V="$(tr -d '[:space:]' < /tmp/.opentofu-version)"; \
    case "$TARGETARCH" in \
      amd64) ARCH=amd64 ;; \
      arm64) ARCH=arm64 ;; \
      *) echo "unsupported TARGETARCH=$TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/tofu.zip \
      "https://github.com/opentofu/opentofu/releases/download/v${V}/tofu_${V}_linux_${ARCH}.zip"; \
    mkdir -p /out; \
    unzip -o /tmp/tofu.zip -d /out; \
    /out/tofu version

# Runtime stage — static-debian12 has no glibc, fine for pure-Go binaries.
# Bundle tofu (ADR 22) from the .opentofu-version-pinned stage below; it lands on
# PATH at /usr/local/bin so tfexec's default `tofu` lookup resolves it. Verified
# at build time (`tofu version`) and at CI time (docker-pr runs it in the image).
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /

COPY --from=builder /rezuscloud /rezuscloud
COPY --from=tofu /out/tofu /usr/local/bin/tofu

# rezuscloud listens on three TCP ports:
#   8080  — HTTP (WebUI + REST API + healthz/readyz/version)
#   50180 — MachineLink (machines phone home over WireGuard-over-gRPC)
#   50190 — Provider gRPC (outbound-only providers connect here)
EXPOSE 8080 50180 50190

USER nonroot:nonroot

ENTRYPOINT ["/rezuscloud"]
