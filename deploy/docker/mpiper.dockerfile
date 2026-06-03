# Stage 1: Builder - Build the Go binary
FROM golang:1.25-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build

# Copy go mod files and download dependencies
COPY ../../go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/ ./cmd/
COPY ../../internal ./internal/
COPY ../../pkg ./pkg/

# Build arguments for versioning
ARG VERSION=0.1.0
ARG COMMIT_HASH=unknown
ARG ENV=production
ARG BUILD_TIME
ARG AUTHOR=RndmCodeGuy

# Build static binary with optimizations
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
    -ldflags="-w -s \
    -X 'main.Version=${VERSION}' \
    -X 'main.BuildTime=${BUILD_TIME}' \
    -X 'main.CommitHash=${COMMIT_HASH}' \
    -X 'main.Env=${ENV}' \
    -X 'main.Author=${AUTHOR}'" \
    -a -installsuffix cgo \
    -o mpiper ./cmd/server/main.go

# Verify the binary was created
RUN ls -lh /build/mpiper && file /build/mpiper

# Stage 2: Runtime - Minimal distroless image
FROM gcr.io/distroless/static-debian12:nonroot

# Labels for container metadata
LABEL \
    org.opencontainers.image.title="MPiper API" \
    org.opencontainers.image.description="Go API server for MPiper media processing pipeline" \
    org.opencontainers.image.source="https://github.com/rndmcodeguy20/mpiper" \
    org.opencontainers.image.version="0.1.0" \
    org.opencontainers.image.vendor="RndmCodeGuy" \
    org.opencontainers.image.licenses="MIT"

# Copy CA certificates for HTTPS
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy timezone data
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy the binary
COPY --from=builder /build/mpiper /app/mpiper

# Use non-root user
USER nonroot:nonroot

WORKDIR /app

# Expose default port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/app/mpiper", "--health-check"] || exit 1

# Run the application
ENTRYPOINT ["/app/mpiper"]

