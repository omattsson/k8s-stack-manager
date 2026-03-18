FROM golang:1.25 AS builder

WORKDIR /app

# For development mode
FROM golang:1.25 AS development
WORKDIR /app
RUN go install github.com/cosmtrek/air@v1.49.0
CMD ["air"]

# For production mode — build stage
FROM builder AS build-prod
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o main ./api/main.go

# Final production image — minimal distroless, non-root
FROM gcr.io/distroless/static-debian11:nonroot AS production
WORKDIR /app/
COPY --from=build-prod /app/main .
EXPOSE 8081
USER nonroot:nonroot
CMD ["./main"]