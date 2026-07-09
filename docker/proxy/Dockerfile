FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /docker-orbit ./cmd/docker-orbit

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM scratch

COPY --from=builder /docker-orbit /docker-orbit

# Default environment (overridden by generate-injected values)
ENV ORBIT_CONTROL_PORT=9900

EXPOSE 9900

ENTRYPOINT ["/docker-orbit", "proxy"]
