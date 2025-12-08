# Build stage
FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make gcc musl-dev linux-headers

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o wallet-exporter ./cmd/exporter

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/wallet-exporter .

# Copy contracts directory (for reference)
COPY --from=builder /build/contracts ./contracts

# Expose metrics port (default 9090, can be overridden with EXPORTER_PORT env var)
EXPOSE 9090

# Health check (uses EXPORTER_PORT environment variable)
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:${EXPORTER_PORT:-9090}/health || exit 1

# Run as non-root user
RUN adduser -D -u 1000 exporter
USER exporter

# Start the exporter
ENTRYPOINT ["/app/wallet-exporter"]
