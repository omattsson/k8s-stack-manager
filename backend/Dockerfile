FROM golang:1.26.1 AS builder

WORKDIR /app

# Install helm CLI (multi-arch)
ARG HELM_VERSION=3.20.1
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://get.helm.sh/helm-v${HELM_VERSION}-linux-${TARGETARCH}.tar.gz \
    | tar xz -C /usr/local/bin --strip-components=1 linux-${TARGETARCH}/helm

# For development mode
FROM golang:1.26.1 AS development
WORKDIR /app
COPY --from=builder /usr/local/bin/helm /usr/local/bin/helm
RUN go install github.com/cosmtrek/air@v1.49.0
CMD ["air"]

# For production mode — build stage
FROM builder AS build-prod
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o main ./api/main.go

# Final production image — Alpine-based so helm can execute
FROM alpine:3.23 AS production
RUN apk add --no-cache ca-certificates
WORKDIR /app/
COPY --from=build-prod /app/main .
COPY --from=builder /usr/local/bin/helm /usr/local/bin/helm
EXPOSE 8081
RUN adduser -D -u 65532 nonroot
USER nonroot
CMD ["./main"]