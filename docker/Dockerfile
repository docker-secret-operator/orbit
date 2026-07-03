# Multi-stage build for Orbit

# Stage 1: Build
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git make

WORKDIR /app

COPY . .

# Build docker-orbit CLI binary
RUN CGO_ENABLED=0 GOOS=linux go build -o docker-orbit ./cmd/docker-orbit/main.go

# Stage 2: Runtime
FROM alpine:latest

RUN apk add --no-cache ca-certificates docker-cli curl bash

# Copy binary from builder
COPY --from=builder /app/docker-orbit /usr/local/bin/docker-orbit
RUN chmod +x /usr/local/bin/docker-orbit

# Create state directory
RUN mkdir -p /var/lib/orbit

# Copy entrypoint script
COPY docker/docker-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /workspace

# Run docker-orbit binary through entrypoint
ENTRYPOINT ["/entrypoint.sh"]
CMD ["--help"]
