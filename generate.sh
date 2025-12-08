#!/bin/bash

# Generate Go bindings from contract ABIs
# This script requires abigen to be installed: go install github.com/ethereum/go-ethereum/cmd/abigen@latest

set -e

echo "Generating Go contract bindings..."

# Create output directory
mkdir -p internal/contracts

# Generate WarmStorageService binding
abigen --abi contracts/WarmStorageService.abi \
       --pkg contracts \
       --type WarmStorageService \
       --out internal/contracts/warm_storage_service.go

# Generate WarmStorageServiceStateView binding
abigen --abi contracts/WarmStorageServiceStateView.abi \
       --pkg contracts \
       --type WarmStorageServiceStateView \
       --out internal/contracts/warm_storage_view.go

# Generate ServiceProviderRegistry binding
abigen --abi contracts/ServiceProviderRegistry.abi \
       --pkg contracts \
       --type ServiceProviderRegistry \
       --out internal/contracts/sp_registry.go

# Generate ERC20 binding
abigen --abi contracts/ERC20.abi \
       --pkg contracts \
       --type ERC20 \
       --out internal/contracts/erc20.go

echo "✅ Contract bindings generated successfully!"
