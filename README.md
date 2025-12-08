# Dealbot Wallet Exporter

A Prometheus exporter for monitoring Filecoin storage provider wallet balances (FIL and USDFC tokens) from the Synapse Warm Storage Service.

## Features

- ✅ Monitors **ALL** storage providers from ServiceProviderRegistry (not just approved ones)
- ✅ Tracks both **FIL** (native token) and **USDFC** (ERC20 token) balances
- ✅ Supports custom wallet monitoring (clients, operators, etc.)
- ✅ Rich Prometheus labels: `approved`, `is_active`, `provider_id`, `name`, `type`
- ✅ Supports both Filecoin mainnet and calibration testnet
- ✅ Concurrent balance fetching for performance
- ✅ Health, status, and metrics endpoints
- ✅ Docker support with docker-compose

## Quick Start

```bash
# 1. Copy and edit environment file
cp .env.example .env
# Edit .env with your settings (defaults work for calibration testnet)

# 2. Generate contract bindings
./generate.sh

# 3. Build
go build -o wallet-exporter ./cmd/exporter

# 4. Run
./wallet-exporter
```

The exporter will:
- Automatically load `.env` file
- Connect to the configured network
- Start monitoring all storage providers
- Expose metrics at http://localhost:9091/metrics

## Architecture

```
┌───────────────────────────────────────────────────────────────┐
│               Dealbot Wallet Exporter                         │
│                                                                │
│  1. Connect to Filecoin RPC                                   │
│  2. Query ServiceProviderRegistry.getProviderCount()          │
│  3. Query WarmStorageView.getApprovedProviders()              │
│  4. For each provider (1 to count):                           │
│     └─> getProvider(id) → {address, name, isActive, ...}     │
│     └─> Check if approved                                     │
│     └─> Get FIL balance (eth_getBalance)                      │
│     └─> Get USDFC balance (ERC20.balanceOf)                   │
│  5. Monitor custom wallets (if configured)                    │
│  6. Update Prometheus metrics with 'approved' label           │
│  7. Expose /metrics endpoint                                  │
└───────────────────────────────────────────────────────────────┘
                          │
                          ▼
                    Prometheus scrapes
                          │
                          ▼
                   Grafana visualizes
```

## Configuration

### Environment Variables

| Variable | Description | Default (Calibration) |
|----------|-------------|----------------------|
| `NETWORK` | Network name (mainnet or calibration) | `calibration` |
| `RPC_URL` | Filecoin RPC endpoint | `https://api.calibration.node.glif.io/rpc/v1` |
| `WARM_STORAGE_ADDRESS` | WarmStorageService contract address | `0x02925630df557F957f70E112bA06e50965417CA0` |
| `USDFC_TOKEN_ADDRESS` | USDFC ERC20 token address (auto-detected if not set) | `0xb3042734b608a1B16e9e86B374A3f3e389B4cDf0` |
| `CUSTOM_WALLETS` | Additional wallets to monitor (optional) | - |
| `EXPORTER_PORT` | HTTP server port | `9091` |
| `SCRAPE_INTERVAL` | How often to scrape blockchain | `60s` |
| `METRICS_PREFIX` | Prometheus metrics prefix | `dealbot` |
| `LOG_LEVEL` | Logging level | `debug` |

### Network Addresses

**Calibration Testnet (default):**
```bash
NETWORK=calibration
RPC_URL=https://api.calibration.node.glif.io/rpc/v1
WARM_STORAGE_ADDRESS=0x02925630df557F957f70E112bA06e50965417CA0
USDFC_TOKEN_ADDRESS=0xb3042734b608a1B16e9e86B374A3f3e389B4cDf0
```

**Mainnet:**
```bash
NETWORK=mainnet
RPC_URL=https://api.node.glif.io/rpc/v1
WARM_STORAGE_ADDRESS=0x8408502033C418E1bbC97cE9ac48E5528F371A9f
USDFC_TOKEN_ADDRESS=0x80B98d3aa09ffff255c3ba4A241111Ff1262F045
```

### Custom Wallet Monitoring

Monitor additional wallets (clients, operators, etc.):

```bash
# Format: address:name:type,address:name:type,...
CUSTOM_WALLETS=0xa108Be4331296Ec8b8C47c2Cd2FbfDDF06E27523:Client A:client,0x1234...:Operator B:operator
```

Supported types: `client`, `operator`, `other`

## Installation & Deployment

### Option 1: Local Build

```bash
# Prerequisites
go install github.com/ethereum/go-ethereum/cmd/abigen@latest

# Build
./generate.sh  # Generate contract bindings
go build -o wallet-exporter ./cmd/exporter

# Run (automatically loads .env)
./wallet-exporter
```

**Note**: The binary automatically loads `.env` file if it exists in the current directory. No shell scripts needed!

### Option 2: Docker

```bash
# Build and run with docker-compose
docker-compose up -d

# View logs
docker-compose logs -f wallet-exporter

# Stop
docker-compose down
```

### Option 3: Manual Docker

```bash
docker build -t dealbot-wallet-exporter .

docker run -d \
  --name dealbot-wallet-exporter \
  -p 9091:9091 \
  --env-file .env \
  dealbot-wallet-exporter
```

## Prometheus Metrics

### Available Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `dealbot_wallet_fil_balance` | Gauge | FIL (native token) balance |
| `dealbot_wallet_usdfc_balance` | Gauge | USDFC token balance |
| `dealbot_wallet_info` | Gauge | Wallet metadata (always 1) |
| `dealbot_scrape_duration_seconds` | Gauge | Scrape duration |
| `dealbot_scrape_errors_total` | Counter | Total scrape errors |

### Metric Labels

All wallet metrics include these labels:

| Label | Description | Example |
|-------|-------------|---------|
| `address` | Wallet address | `0x682467D59F5679cB0BF13115d4C94550b8218CF2` |
| `name` | Wallet/provider name | `pspsps-calibnet` |
| `type` | Wallet type | `provider`, `client`, `operator`, `other` |
| `provider_id` | Provider ID (providers only) | `11` |
| `is_active` | Active status (providers only) | `true` or `false` |
| `approved` | Approved in WarmStorage (providers only) | `true` or `false` |
| `description` | Provider description (wallet_info only) | - |

### Example Metrics Output

```promql
# FIL balances
dealbot_wallet_fil_balance{address="0x682467D59F5679cB0BF13115d4C94550b8218CF2",approved="true",is_active="true",name="pspsps-calibnet",provider_id="11",type="provider"} 329.84

dealbot_wallet_fil_balance{address="0x8c8c7a9BE47ed491B33B941fBc0276BD2ec25E7e",approved="false",is_active="true",name="Kubuxu's dev node",provider_id="1",type="provider"} 78.12

# USDFC balances
dealbot_wallet_usdfc_balance{address="0x86d026029052c6582d277d9b28700Edc9670B150",approved="false",is_active="true",name="beck-calib",provider_id="6",type="provider"} 339.9

# Wallet info
dealbot_wallet_info{address="0x682467D59F5679cB0BF13115d4C94550b8218CF2",approved="true",description="herding cats",is_active="true",name="pspsps-calibnet",provider_id="11",type="provider"} 1

# System metrics
dealbot_scrape_duration_seconds 2.36
dealbot_scrape_errors_total 0
```

## Prometheus Configuration

Add to your `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: 'wallet-exporter'
    static_configs:
      - targets: ['localhost:9091']
    metrics_path: '/metrics'
    scrape_interval: 60s
    scrape_timeout: 30s
```

## Grafana Dashboards

See [deployments/grafana-queries.md](deployments/grafana-queries.md) for:
- Dashboard panel queries
- Alert rule examples
- Dashboard variables
- Layout suggestions

### Quick Examples

```promql
# All providers sorted by FIL balance
sort_desc(dealbot_wallet_fil_balance{type="provider"})

# Only approved providers
dealbot_wallet_fil_balance{type="provider",approved="true"}

# Providers with low balance
dealbot_wallet_fil_balance{type="provider"} < 10

# Total balance by approval status
sum by(approved) (dealbot_wallet_fil_balance{type="provider"})

# USDFC balances (non-zero only)
dealbot_wallet_usdfc_balance > 0
```

## HTTP Endpoints

| Endpoint | Description |
|----------|-------------|
| `/` | Welcome page with navigation |
| `/metrics` | Prometheus metrics (text format) |
| `/health` | Health check (returns `OK`) |
| `/status` | Human-readable status with wallet list |

### Status Endpoint Example

```bash
$ curl http://localhost:9091/status

Dealbot Wallet Exporter Status
==============================

Network: calibration
Wallets monitored: 18
Last scrape: 2025-12-08T21:03:05+08:00
Time since last scrape: 14s

Storage Providers (18):
  - ID: 1, Name: Kubuxu's dev node
    Address: 0x8c8c7a9BE47ed491B33B941fBc0276BD2ec25E7e
    FIL Balance: 78.121964 FIL
    USDFC Balance: 10.000000 USDFC
    Active: true

  - ID: 6, Name: beck-calib
    Address: 0x86d026029052c6582d277d9b28700Edc9670B150
    FIL Balance: 1000470.334072 FIL
    USDFC Balance: 339.900000 USDFC
    Active: true

  ...
```

## Project Structure

```
wallet-exporter/
├── cmd/
│   └── exporter/main.go       # Main application
├── internal/
│   ├── config/config.go       # Configuration management
│   ├── contracts/             # Generated Go bindings (git-ignored)
│   └── exporter/exporter.go   # Core exporter logic
├── contracts/                 # Contract ABIs
│   ├── WarmStorageService.abi
│   ├── WarmStorageServiceStateView.abi
│   ├── ServiceProviderRegistry.abi
│   └── ERC20.abi
├── deployments/
│   ├── prometheus.yml         # Prometheus config example
│   └── grafana-queries.md     # Grafana dashboard examples
├── .env.example               # Environment template
├── .env                       # Your config (git-ignored)
├── build.sh                   # Build script
├── generate.sh                # Generate contract bindings
├── run.sh                     # Startup script
├── docker-compose.yml
├── Dockerfile
├── go.mod
├── go.sum
└── README.md
```

## Development

### Prerequisites

```bash
# Install abigen for contract binding generation
go install github.com/ethereum/go-ethereum/cmd/abigen@latest

# Make sure it's in PATH
export PATH=$PATH:$(go env GOPATH)/bin
```

### Generate Contract Bindings

After modifying any `.abi` files:

```bash
./generate.sh
```

This regenerates Go bindings in `internal/contracts/`.

### Build

```bash
# Development build
go build -o wallet-exporter ./cmd/exporter

# Production build (optimized)
CGO_ENABLED=0 go build -ldflags="-s -w" -o wallet-exporter ./cmd/exporter
```

## Troubleshooting

### Common Issues

**1. No providers found**
- Check `WARM_STORAGE_ADDRESS` is correct for your network
- Verify RPC connectivity: `curl $RPC_URL`
- Try calibration testnet first for testing

**2. USDFC balance always 0**
- Most providers don't have USDFC tokens - this is normal
- Verify `USDFC_TOKEN_ADDRESS` is correct

**3. Contract call reverted**
- Wrong contract address for network
- Use addresses from Synapse SDK:
  - Calibration: `0x02925630df557F957f70E112bA06e50965417CA0`
  - Mainnet: `0x8408502033C418E1bbC97cE9ac48E5528F371A9f`

**4. High scrape duration**
- Normal for 18 providers: 2-5 seconds
- Reduce `SCRAPE_INTERVAL` to avoid overlap
- Use a faster RPC endpoint

**5. Port already in use**
- Change `EXPORTER_PORT` in `.env`
- Check what's using the port: `lsof -i :9091`

### Debug Mode

```bash
export LOG_LEVEL=debug
./wallet-exporter
```

### Verify Installation

```bash
# 1. Health check
curl http://localhost:9091/health

# 2. Check providers found
curl http://localhost:9091/status | grep "Wallets monitored"

# 3. Check metrics
curl -s http://localhost:9091/metrics | grep "dealbot_wallet_fil_balance" | wc -l
```

## Performance

- **Concurrent fetching**: Up to 10 parallel requests
- **Typical scrape time**: 2-5 seconds for 18 providers
- **Memory usage**: ~50-100 MB
- **CPU usage**: Minimal (event-driven)

## Security

- ✅ Read-only operations (no private keys needed)
- ✅ Non-root Docker user
- ✅ No sensitive data exposure
- ✅ Health checks included
- ✅ Graceful shutdown handling

## Contract Addresses Reference

All addresses from [Synapse SDK](https://github.com/FilOzone/synapse-sdk):

### Calibration Testnet (Chain ID: 314159)
- WarmStorage: `0x02925630df557F957f70E112bA06e50965417CA0`
- USDFC Token: `0xb3042734b608a1B16e9e86B374A3f3e389B4cDf0`
- RPC: `https://api.calibration.node.glif.io/rpc/v1`

### Mainnet (Chain ID: 314)
- WarmStorage: `0x8408502033C418E1bbC97cE9ac48E5528F371A9f`
- USDFC Token: `0x80B98d3aa09ffff255c3ba4A241111Ff1262F045`
- RPC: `https://api.node.glif.io/rpc/v1`

## License

See parent project license.

## Acknowledgments

- Built with [go-ethereum](https://github.com/ethereum/go-ethereum)
- Metrics powered by [Prometheus Go client](https://github.com/prometheus/client_golang)
- Designed for [Synapse SDK](https://github.com/FilOzone/synapse-sdk)
- Contract addresses from Synapse SDK codebase
