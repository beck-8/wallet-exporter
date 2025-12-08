# Grafana Dashboard Queries for Dealbot Wallet Exporter

This document provides example Prometheus queries for creating Grafana dashboards to monitor Filecoin storage provider wallets.

## Available Metrics

- `dealbot_wallet_fil_balance` - FIL (native token) balance for each wallet
- `dealbot_wallet_usdfc_balance` - USDFC token balance for each wallet
- `dealbot_wallet_info` - Wallet metadata (always 1)
- `dealbot_scrape_duration_seconds` - Duration of last scrape
- `dealbot_scrape_errors_total` - Total scrape errors

## Labels

All wallet metrics include these labels:
- `address` - Wallet address
- `name` - Wallet/provider name
- `type` - Wallet type (provider, client, operator, other)
- `provider_id` - Provider ID (only for providers)
- `is_active` - Whether provider is active (only for providers)
- `approved` - Whether provider is approved in WarmStorage (only for providers)

## Dashboard Panels

### Panel 1: Total FIL Balance by Provider Type
```promql
sum by(type) (dealbot_wallet_fil_balance)
```

### Panel 2: Provider FIL Balances (Table)
Shows all providers sorted by balance:
```promql
sort_desc(dealbot_wallet_fil_balance{type="provider"})
```

### Panel 3: Approved vs Non-Approved Providers Balance
```promql
sum by(approved) (dealbot_wallet_fil_balance{type="provider"})
```

### Panel 4: USDFC Token Balances (Table)
Only show wallets with USDFC balance:
```promql
dealbot_wallet_usdfc_balance > 0
```

### Panel 5: Low Balance Alert - FIL
Providers with less than 10 FIL:
```promql
dealbot_wallet_fil_balance{type="provider"} < 10
```

### Panel 6: Low Balance Alert - USDFC
Providers with less than 100 USDFC:
```promql
dealbot_wallet_usdfc_balance{type="provider"} < 100
```

### Panel 7: Active Approved Providers Count
```promql
count(dealbot_wallet_info{type="provider",is_active="true",approved="true"})
```

### Panel 8: Provider Status Overview (Stat Panel)
```promql
# Total providers
count(dealbot_wallet_info{type="provider"})

# Active providers
count(dealbot_wallet_info{type="provider",is_active="true"})

# Approved providers
count(dealbot_wallet_info{type="provider",approved="true"})
```

### Panel 9: Scrape Performance
```promql
dealbot_scrape_duration_seconds
```

### Panel 10: Scrape Error Rate
```promql
rate(dealbot_scrape_errors_total[5m])
```

### Panel 11: Top 10 Providers by FIL Balance
```promql
topk(10, dealbot_wallet_fil_balance{type="provider"})
```

### Panel 12: Custom Wallet Monitoring
Monitor specific client/operator wallets:
```promql
dealbot_wallet_fil_balance{type=~"client|operator"}
```

### Panel 13: Balance Change Over Time (Graph)
Track FIL balance changes:
```promql
dealbot_wallet_fil_balance{name="your-provider-name"}
```

## Alert Rules

### Low FIL Balance Alert (Warning)
```yaml
- alert: ProviderLowFILBalance
  expr: dealbot_wallet_fil_balance{type="provider",is_active="true"} < 10
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Provider {{ $labels.name }} has low FIL balance"
    description: "Provider {{ $labels.name }} ({{ $labels.address }}) has {{ $value | humanize }} FIL remaining"
```

### Critical FIL Balance Alert
```yaml
- alert: ProviderCriticalFILBalance
  expr: dealbot_wallet_fil_balance{type="provider",is_active="true"} < 1
  for: 5m
  labels:
    severity: critical
  annotations:
    summary: "Provider {{ $labels.name }} has CRITICAL low FIL balance"
    description: "Provider {{ $labels.name }} ({{ $labels.address }}) has only {{ $value | humanize }} FIL - IMMEDIATE ACTION REQUIRED"
```

### Low USDFC Balance Alert
```yaml
- alert: ProviderLowUSDFCBalance
  expr: dealbot_wallet_usdfc_balance{type="provider",is_active="true"} < 50
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Provider {{ $labels.name }} has low USDFC balance"
    description: "Provider {{ $labels.name }} ({{ $labels.address }}) has {{ $value | humanize }} USDFC remaining"
```

### Scrape Failure Alert
```yaml
- alert: WalletExporterScrapeFailed
  expr: increase(dealbot_scrape_errors_total[10m]) > 3
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "Wallet exporter experiencing scrape failures"
    description: "Wallet exporter has failed {{ $value }} times in the last 10 minutes"
```

### Exporter Down Alert
```yaml
- alert: WalletExporterDown
  expr: up{job="wallet-exporter"} == 0
  for: 2m
  labels:
    severity: critical
  annotations:
    summary: "Wallet exporter is down"
    description: "The wallet exporter has been down for more than 2 minutes"
```

### Inactive Approved Provider Alert
```yaml
- alert: ApprovedProviderInactive
  expr: dealbot_wallet_info{type="provider",approved="true",is_active="false"} == 1
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "Approved provider {{ $labels.name }} is inactive"
    description: "Provider {{ $labels.name }} is approved but marked as inactive"
```

## Grafana Dashboard Variables

Add these variables to make your dashboard more interactive:

### Provider Filter
```
label_values(dealbot_wallet_fil_balance{type="provider"}, name)
```

### Provider Type Filter
```
label_values(dealbot_wallet_fil_balance, type)
```

### Approval Status Filter
```
label_values(dealbot_wallet_fil_balance{type="provider"}, approved)
```

### Active Status Filter
```
label_values(dealbot_wallet_fil_balance{type="provider"}, is_active)
```

## Example Dashboard Layout

### Row 1: Overview Stats
- Total Providers
- Active Providers
- Approved Providers
- Total FIL Balance
- Total USDFC Balance

### Row 2: Balance Charts
- FIL Balance by Provider (Bar Chart)
- USDFC Balance Distribution (Pie Chart)
- Balance Trend Over Time (Graph)

### Row 3: Provider Tables
- All Providers (sorted by balance)
- Low Balance Warnings
- Custom Wallets

### Row 4: System Health
- Scrape Duration
- Error Rate
- Last Scrape Time
