.PHONY: help generate build run docker-build docker-run clean test

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

generate: ## Generate Go contract bindings from ABIs
	@echo "Generating contract bindings..."
	@./generate.sh

build: generate ## Build the exporter binary
	@echo "Building exporter..."
	@go build -o wallet-exporter ./cmd/exporter
	@echo "✅ Build complete: ./wallet-exporter"

run: build ## Build and run the exporter
	@echo "Starting exporter..."
	@./wallet-exporter

docker-build: ## Build Docker image
	@echo "Building Docker image..."
	@docker build -t dealbot-wallet-exporter:latest .
	@echo "✅ Docker image built: dealbot-wallet-exporter:latest"

docker-run: docker-build ## Build and run Docker container
	@echo "Starting Docker container..."
	@docker-compose up -d
	@echo "✅ Exporter running at http://localhost:9090"
	@echo "View logs: docker-compose logs -f"

docker-stop: ## Stop Docker container
	@docker-compose down

docker-logs: ## View Docker container logs
	@docker-compose logs -f wallet-exporter

clean: ## Clean build artifacts
	@echo "Cleaning..."
	@rm -f wallet-exporter
	@rm -rf internal/contracts/*.go
	@echo "✅ Clean complete"

test: ## Run tests
	@echo "Running tests..."
	@go test -v ./...

test-coverage: ## Run tests with coverage
	@echo "Running tests with coverage..."
	@go test -v -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "✅ Coverage report: coverage.html"

lint: ## Run linter (requires golangci-lint)
	@echo "Running linter..."
	@golangci-lint run ./...

fmt: ## Format Go code
	@echo "Formatting code..."
	@go fmt ./...
	@echo "✅ Code formatted"

deps: ## Download dependencies
	@echo "Downloading dependencies..."
	@go mod download
	@go mod tidy
	@echo "✅ Dependencies updated"

install-tools: ## Install development tools
	@echo "Installing development tools..."
	@go install github.com/ethereum/go-ethereum/cmd/abigen@latest
	@echo "✅ Tools installed"

status: ## Check exporter status
	@curl -s http://localhost:9090/status || echo "Exporter not running"

metrics: ## View current metrics
	@curl -s http://localhost:9090/metrics | grep dealbot_provider || echo "Exporter not running"

health: ## Check exporter health
	@curl -s http://localhost:9090/health || echo "Exporter not running"
