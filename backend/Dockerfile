# syntax=docker/dockerfile:1
# The helm and build stages run on the build machine's platform
# ($BUILDPLATFORM) and cross-compile for $TARGETOS/$TARGETARCH. A
# multi-platform build therefore never runs the Go compiler under QEMU
# emulation (the arm64 build took about 15 minutes that way).
FROM --platform=$BUILDPLATFORM golang:1.27.2 AS helm

# Download the helm CLI for the target architecture (download only, so it
# works from any build platform).
ARG HELM_VERSION=3.20.1
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://get.helm.sh/helm-v${HELM_VERSION}-linux-${TARGETARCH}.tar.gz \
    | tar xz -C /usr/local/bin --strip-components=1 linux-${TARGETARCH}/helm

# For development mode
FROM golang:1.27.2 AS development
WORKDIR /app
COPY --from=helm /usr/local/bin/helm /usr/local/bin/helm
RUN go install github.com/cosmtrek/air@v1.49.0
CMD ["air"]

# For production mode — build stage. The module download does not depend on
# the target platform, so one cached layer serves all platforms. The modules
# stay in the image layer (not in a cache mount): the GitHub Actions cache
# stores layers, but not cache mounts.
FROM --platform=$BUILDPLATFORM golang:1.27.2 AS build-prod
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
RUN apk add --no-cache ca-certificates
WORKDIR /app/
COPY --from=build-prod /app/main .
COPY --from=helm /usr/local/bin/helm /usr/local/bin/helm
EXPOSE 8081
RUN adduser -D -u 65532 nonroot
USER 65532:65532
CMD ["./main"]