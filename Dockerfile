# Build stage
FROM golang:alpine AS builder

# Set working directory
WORKDIR /build

# Install abigen for contract binding generation
RUN go install github.com/ethereum/go-ethereum/cmd/abigen@latest

# Copy go mod files
COPY go.mod go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code and contracts
COPY . .

# Generate Go contract bindings from ABIs using the generate script
RUN chmod +x generate.sh && ./generate.sh

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o wallet-exporter ./cmd/exporter

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/wallet-exporter .

# Copy contracts directory (for reference)
COPY --from=builder /build/contracts ./contracts

# Expose metrics port (default 9081, can be overridden with EXPORTER_PORT env var)
EXPOSE 9091

# Health check (uses EXPORTER_PORT environment variable)
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:${EXPORTER_PORT:-9091}/health || exit 1

# Run as non-root user
RUN adduser -D -u 1000 exporter
USER exporter

# Start the exporter
ENTRYPOINT ["/app/wallet-exporter"]
