# Multi-stage Dockerfile for rezuscloud management plane.
#
# Pure-Go build (CGO_ENABLED=0). Uses modernc.org/sqlite instead of mattn/go-sqlite3
# so no C toolchain is needed. The runtime image is distroless/static-debian12
# (smaller, no glibc dependency).
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

# Runtime stage — static-debian12 has no glibc, fine for pure-Go binaries.
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /

COPY --from=builder /rezuscloud /rezuscloud

# rezuscloud listens on three TCP ports:
#   8080  — HTTP (WebUI + REST API + healthz/readyz/version)
#   50180 — MachineLink (machines phone home over WireGuard-over-gRPC)
#   50190 — Provider gRPC (outbound-only providers connect here)
EXPOSE 8080 50180 50190

USER nonroot:nonroot

ENTRYPOINT ["/rezuscloud"]
