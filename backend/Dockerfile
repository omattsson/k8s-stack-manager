# syntax=docker/dockerfile:1
# The helm and build stages run on the build machine's platform
# ($BUILDPLATFORM) and cross-compile for $TARGETOS/$TARGETARCH. A
# multi-platform build therefore never runs the Go compiler under QEMU
# emulation (the arm64 build took about 15 minutes that way).
FROM --platform=$BUILDPLATFORM golang:1.27.3 AS helm

# Download the helm CLI for the target architecture (download only, so it
# works from any build platform). The checksums are pinned here, not fetched
# from get.helm.sh, so a changed tarball fails the build. Update them with
# HELM_VERSION from the release notes (github.com/helm/helm/releases).
ARG HELM_VERSION=3.22.1
ARG HELM_SHA256_AMD64=896cf63684a42a723d61c980369d6e588330927deb13676ca0d85006dd6da0ea
ARG HELM_SHA256_ARM64=4c29e286b279d170fa048ddfc3e4e83dcd069ca9c52102766ba672d7149e694d
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) sum="${HELM_SHA256_AMD64}" ;; \
      arm64) sum="${HELM_SHA256_ARM64}" ;; \
      *) echo "no helm checksum for ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo /tmp/helm.tgz https://get.helm.sh/helm-v${HELM_VERSION}-linux-${TARGETARCH}.tar.gz; \
    echo "${sum}  /tmp/helm.tgz" | sha256sum -c -; \
    tar xzf /tmp/helm.tgz -C /usr/local/bin --strip-components=1 linux-${TARGETARCH}/helm; \
    rm /tmp/helm.tgz

# For development mode
FROM golang:1.27.3 AS development
WORKDIR /app
COPY --from=helm /usr/local/bin/helm /usr/local/bin/helm
RUN go install github.com/cosmtrek/air@v1.49.0
CMD ["air"]

# For production mode — build stage. The module download does not depend on
# the target platform, so one cached layer serves all platforms. The modules
# stay in the image layer (not in a cache mount): the GitHub Actions cache
# stores layers, but not cache mounts.
FROM --platform=$BUILDPLATFORM golang:1.27.3 AS build-prod
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o main ./api/

# Final production image — Alpine-based so helm can execute
FROM alpine:3.24 AS production
# 20261020-r0 or later distrusts the compromised eMudhra intermediate CAs (#665).
RUN apk add --no-cache 'ca-certificates>=20261020-r0' 'ca-certificates-bundle>=20261020-r0'
WORKDIR /app/
COPY --from=build-prod /app/main .
COPY --from=helm /usr/local/bin/helm /usr/local/bin/helm
EXPOSE 8081
RUN adduser -D -u 65532 nonroot
USER 65532:65532
CMD ["./main"]