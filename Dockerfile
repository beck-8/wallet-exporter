# Build stage
FROM golang:1.21-alpine AS builder

# Install build dependencies including abigen
RUN apk add --no-cache git make gcc musl-dev linux-headers

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

# Generate Go contract bindings from ABIs
RUN echo "Generating Go contract bindings..." && \
    mkdir -p internal/contracts && \
    abigen --abi contracts/WarmStorageService.abi \
           --pkg contracts \
           --type WarmStorageService \
           --out internal/contracts/warm_storage_service.go && \
    abigen --abi contracts/WarmStorageServiceStateView.abi \
           --pkg contracts \
           --type WarmStorageServiceStateView \
           --out internal/contracts/warm_storage_view.go && \
    abigen --abi contracts/ServiceProviderRegistry.abi \
           --pkg contracts \
           --type ServiceProviderRegistry \
           --out internal/contracts/sp_registry.go && \
    abigen --abi contracts/ERC20.abi \
           --pkg contracts \
           --type ERC20 \
           --out internal/contracts/erc20.go && \
    echo "✅ Contract bindings generated successfully!"

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
EXPOSE 9091

# Health check (uses EXPORTER_PORT environment variable)
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:${EXPORTER_PORT:-9091}/health || exit 1

# Run as non-root user
RUN adduser -D -u 1000 exporter
USER exporter

# Start the exporter
ENTRYPOINT ["/app/wallet-exporter"]
