#!/bin/bash

# Build script for wallet-exporter

set -e

echo "Building Dealbot Wallet Exporter..."

# Clean previous builds
rm -f wallet-exporter

# Generate contract bindings if needed
if [ ! -d "internal/contracts" ] || [ -z "$(ls -A internal/contracts)" ]; then
    echo "Generating contract bindings..."
    ./generate.sh
fi

# Build the binary
echo "Compiling Go binary..."
go build -o wallet-exporter ./cmd/exporter

echo "✅ Build complete! Binary: ./wallet-exporter"
echo ""
echo "To run the exporter:"
echo "  1. Copy .env.example to .env and configure"
echo "  2. Run: ./wallet-exporter"
