package exporter

import (
	"fmt"
	"math/big"

	"github.com/prometheus/client_golang/prometheus"
)

// WalletExporter implements prometheus.Collector for the per-wallet metrics.
//
// Collect emits the complete metric set from an immutable snapshot, so the
// /metrics endpoint can never observe a partially-populated state. This replaces
// the previous GaugeVec.Reset()+Set() approach, whose window between Reset and
// repopulate could expose empty/half-filled metrics to a concurrent scrape and
// made graphs flicker to zero.

var _ prometheus.Collector = (*WalletExporter)(nil)

// Describe sends the descriptors of all per-wallet metrics.
func (e *WalletExporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- e.filBalanceDesc
	ch <- e.usdfcBalanceDesc
	ch <- e.walletInfoDesc
	ch <- e.paymentsFundsDesc
	ch <- e.paymentsAvailableDesc
	ch <- e.paymentsLockedDesc
	ch <- e.paymentsFundedUntilDesc
	ch <- e.pingSuccessDesc
	ch <- e.pingDurationDesc
}

// Collect emits one consistent set of metrics from the latest scrape snapshot.
func (e *WalletExporter) Collect(ch chan<- prometheus.Metric) {
	wallets, pingResults := e.snapshotForCollect()

	// Guard against duplicate label sets in a single Collect: emitting two
	// metrics with identical desc+labels would make registry.Gather() fail with
	// a "duplicate metric" error (returning HTTP 500). The old GaugeVec silently
	// overwrote duplicates, so we mirror that by keeping the first and skipping
	// the rest.
	seen := make(map[string]struct{}, len(wallets))

	for _, wallet := range wallets {
		var providerID, isActive, approved string
		if wallet.Type == "provider" {
			providerID = fmt.Sprintf("%d", wallet.ProviderID)
			isActive = fmt.Sprintf("%t", wallet.IsActive)
			approved = fmt.Sprintf("%t", wallet.IsApproved)
		}

		// Balance-metric label set (also uniquely identifies the wallet here).
		key := wallet.Address.Hex() + "|" + wallet.Name + "|" + wallet.Type + "|" +
			providerID + "|" + isActive + "|" + approved
		if _, dup := seen[key]; dup {
			e.logger.Warn("Skipping duplicate wallet metric label set",
				"address", wallet.Address.Hex(), "name", wallet.Name, "type", wallet.Type)
			continue
		}
		seen[key] = struct{}{}

		addr := wallet.Address.Hex()
		balanceLabelVals := []string{addr, wallet.Name, wallet.Type, providerID, isActive, approved}

		ch <- prometheus.MustNewConstMetric(e.filBalanceDesc, prometheus.GaugeValue,
			toUnit(wallet.FILBalance), balanceLabelVals...)
		ch <- prometheus.MustNewConstMetric(e.usdfcBalanceDesc, prometheus.GaugeValue,
			toUnit(wallet.USDFCBalance), balanceLabelVals...)
		ch <- prometheus.MustNewConstMetric(e.paymentsFundsDesc, prometheus.GaugeValue,
			toUnit(wallet.PaymentsFunds), balanceLabelVals...)
		ch <- prometheus.MustNewConstMetric(e.paymentsAvailableDesc, prometheus.GaugeValue,
			toUnit(wallet.PaymentsAvailable), balanceLabelVals...)
		ch <- prometheus.MustNewConstMetric(e.paymentsLockedDesc, prometheus.GaugeValue,
			toUnit(wallet.PaymentsLocked), balanceLabelVals...)
		// PaymentsFundedUntil is an epoch (block number), not a token amount.
		ch <- prometheus.MustNewConstMetric(e.paymentsFundedUntilDesc, prometheus.GaugeValue,
			toRaw(wallet.PaymentsFundedUntil), balanceLabelVals...)

		ch <- prometheus.MustNewConstMetric(e.walletInfoDesc, prometheus.GaugeValue, 1,
			addr, wallet.Name, wallet.Type, providerID, wallet.Description, isActive, approved)

		// Ping metrics only exist for providers that were successfully pinged.
		if wallet.Type == "provider" {
			if result, ok := pingResults[wallet.ProviderID]; ok {
				successVal := 0.0
				if result.Success {
					successVal = 1.0
				}
				pingLabelVals := []string{addr, wallet.Name, providerID, result.ServiceURL}
				ch <- prometheus.MustNewConstMetric(e.pingSuccessDesc, prometheus.GaugeValue,
					successVal, pingLabelVals...)
				ch <- prometheus.MustNewConstMetric(e.pingDurationDesc, prometheus.GaugeValue,
					float64(result.Duration.Milliseconds()), pingLabelVals...)
			}
		}
	}
}

// snapshotForCollect returns the current wallet/ping snapshot. The scrape loop
// only ever swaps these references (it never mutates them in place), so reading
// them under the read lock yields a stable, self-consistent view.
func (e *WalletExporter) snapshotForCollect() ([]WalletInfo, map[uint64]PingResult) {
	e.walletsMux.RLock()
	defer e.walletsMux.RUnlock()
	return e.wallets, e.pingResults
}

// toUnit converts a wei-scale integer (18 decimals) to its whole-unit float.
func toUnit(v *big.Int) float64 {
	if v == nil {
		return 0
	}
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(v), big.NewFloat(1e18)).Float64()
	return f
}

// toRaw converts an integer to float without scaling (e.g. epoch numbers).
func toRaw(v *big.Int) float64 {
	if v == nil {
		return 0
	}
	f, _ := new(big.Float).SetInt(v).Float64()
	return f
}
