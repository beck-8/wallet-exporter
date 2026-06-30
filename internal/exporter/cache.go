package exporter

import (
	"maps"
	"math/big"
	"strings"
)

// cacheKey normalises an address string for use as a map key.
func cacheKey(addr string) string {
	return strings.ToLower(strings.TrimSpace(addr))
}

// orZero returns v, or a new zero big.Int when v is nil.
func orZero(v *big.Int) *big.Int {
	if v == nil {
		return big.NewInt(0)
	}
	return v
}

// applyCachedPayments copies the cached Payments figures into w, falling back to
// zeros when there is no cached value to carry forward.
func applyCachedPayments(w *WalletInfo, prev *WalletInfo) {
	if prev != nil {
		w.PaymentsFunds = orZero(prev.PaymentsFunds)
		w.PaymentsAvailable = orZero(prev.PaymentsAvailable)
		w.PaymentsLocked = orZero(prev.PaymentsLocked)
		w.PaymentsFundedUntil = orZero(prev.PaymentsFundedUntil)
		return
	}
	w.PaymentsFunds = big.NewInt(0)
	w.PaymentsAvailable = big.NewInt(0)
	w.PaymentsLocked = big.NewInt(0)
	w.PaymentsFundedUntil = big.NewInt(0)
}

// mapToSlice flattens a provider-id keyed cache map into a slice.
func mapToSlice(m map[uint64]WalletInfo) []WalletInfo {
	out := make([]WalletInfo, 0, len(m))
	for _, w := range m {
		out = append(out, w)
	}
	return out
}

// --- provider last-good cache ---

func (e *WalletExporter) snapshotProviders() map[uint64]WalletInfo {
	e.cacheMux.Lock()
	defer e.cacheMux.Unlock()
	m := make(map[uint64]WalletInfo, len(e.lastGoodProviders))
	maps.Copy(m, e.lastGoodProviders)
	return m
}

func (e *WalletExporter) storeProviders(wallets []WalletInfo) {
	m := make(map[uint64]WalletInfo, len(wallets))
	for _, w := range wallets {
		m[w.ProviderID] = w
	}
	e.cacheMux.Lock()
	e.lastGoodProviders = m
	e.cacheMux.Unlock()
}

// --- custom-wallet last-good cache ---

func (e *WalletExporter) snapshotCustom() map[string]WalletInfo {
	e.cacheMux.Lock()
	defer e.cacheMux.Unlock()
	m := make(map[string]WalletInfo, len(e.lastGoodCustom))
	maps.Copy(m, e.lastGoodCustom)
	return m
}

func (e *WalletExporter) storeCustom(wallets []WalletInfo) {
	m := make(map[string]WalletInfo, len(wallets))
	for _, w := range wallets {
		m[cacheKey(w.Address.Hex())] = w
	}
	e.cacheMux.Lock()
	e.lastGoodCustom = m
	e.cacheMux.Unlock()
}

// --- approved-provider set cache ---

func (e *WalletExporter) snapshotApproved() map[uint64]bool {
	e.cacheMux.Lock()
	defer e.cacheMux.Unlock()
	m := make(map[uint64]bool, len(e.lastApprovedMap))
	maps.Copy(m, e.lastApprovedMap)
	return m
}

func (e *WalletExporter) storeApproved(approved map[uint64]bool) {
	e.cacheMux.Lock()
	e.lastApprovedMap = approved
	e.cacheMux.Unlock()
}
