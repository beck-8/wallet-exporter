package exporter

import (
	"io"
	"log/slog"
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"
)

func newTestExporter() *WalletExporter {
	const p = "test"
	balanceLabels := []string{"address", "name", "type", "provider_id", "is_active", "approved"}
	pingLabels := []string{"address", "name", "provider_id", "service_url"}
	return &WalletExporter{
		logger:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
		filBalanceDesc:          prometheus.NewDesc(p+"_wallet_fil_balance", "", balanceLabels, nil),
		usdfcBalanceDesc:        prometheus.NewDesc(p+"_wallet_usdfc_balance", "", balanceLabels, nil),
		walletInfoDesc:          prometheus.NewDesc(p+"_wallet_info", "", []string{"address", "name", "type", "provider_id", "description", "is_active", "approved"}, nil),
		paymentsFundsDesc:       prometheus.NewDesc(p+"_wallet_payments_funds", "", balanceLabels, nil),
		paymentsAvailableDesc:   prometheus.NewDesc(p+"_wallet_payments_available", "", balanceLabels, nil),
		paymentsLockedDesc:      prometheus.NewDesc(p+"_wallet_payments_locked", "", balanceLabels, nil),
		paymentsFundedUntilDesc: prometheus.NewDesc(p+"_wallet_payments_funded_until_epoch", "", balanceLabels, nil),
		pingSuccessDesc:         prometheus.NewDesc(p+"_provider_ping_success", "", pingLabels, nil),
		pingDurationDesc:        prometheus.NewDesc(p+"_provider_ping_ms", "", pingLabels, nil),
	}
}

func wei(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), big.NewInt(1e18)) }

func TestCollectEmitsCompleteSet(t *testing.T) {
	e := newTestExporter()
	e.wallets = []WalletInfo{
		{
			Address: common.HexToAddress("0x1"), Name: "p1", Type: "provider",
			ProviderID: 1, IsActive: true, IsApproved: true,
			FILBalance: wei(3), USDFCBalance: wei(5),
			PaymentsFunds: wei(10), PaymentsAvailable: wei(7), PaymentsLocked: wei(3),
			PaymentsFundedUntil: big.NewInt(12345),
		},
		{
			Address: common.HexToAddress("0x2"), Name: "c1", Type: "client",
			FILBalance: wei(1), USDFCBalance: wei(2),
			PaymentsFunds: big.NewInt(0), PaymentsAvailable: big.NewInt(0),
			PaymentsLocked: big.NewInt(0), PaymentsFundedUntil: big.NewInt(0),
		},
	}
	e.pingResults = map[uint64]PingResult{
		1: {Success: true, Duration: 0, ServiceURL: "https://sp1"},
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(e)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	got := map[string]int{}
	for _, mf := range mfs {
		got[mf.GetName()] = len(mf.GetMetric())
	}

	// 7 per-wallet families have a series for each of the 2 wallets.
	for _, name := range []string{
		"test_wallet_fil_balance", "test_wallet_usdfc_balance", "test_wallet_info",
		"test_wallet_payments_funds", "test_wallet_payments_available",
		"test_wallet_payments_locked", "test_wallet_payments_funded_until_epoch",
	} {
		if got[name] != 2 {
			t.Errorf("%s: got %d series, want 2", name, got[name])
		}
	}
	// Ping metrics only for the one pinged provider.
	if got["test_provider_ping_success"] != 1 {
		t.Errorf("ping_success: got %d series, want 1", got["test_provider_ping_success"])
	}

	// Spot-check a value: provider FIL balance should be 3 (scaled from wei).
	for _, mf := range mfs {
		if mf.GetName() != "test_wallet_fil_balance" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "provider_id" && l.GetValue() == "1" {
					if v := m.GetGauge().GetValue(); v != 3 {
						t.Errorf("provider 1 FIL = %v, want 3", v)
					}
				}
			}
		}
	}
}

func TestCollectSkipsDuplicateLabelSets(t *testing.T) {
	e := newTestExporter()
	dup := WalletInfo{
		Address: common.HexToAddress("0xabc"), Name: "dup", Type: "other",
		FILBalance: wei(1), USDFCBalance: wei(1),
		PaymentsFunds: big.NewInt(0), PaymentsAvailable: big.NewInt(0),
		PaymentsLocked: big.NewInt(0), PaymentsFundedUntil: big.NewInt(0),
	}
	e.wallets = []WalletInfo{dup, dup} // identical label set

	reg := prometheus.NewRegistry()
	reg.MustRegister(e)

	// Without dedup this would fail with a "duplicate metric" error.
	if _, err := reg.Gather(); err != nil {
		t.Fatalf("Gather returned error on duplicate label set: %v", err)
	}
}

func TestCollectConsistentUnderConcurrentSnapshotSwap(t *testing.T) {
	e := newTestExporter()
	mk := func(n int) []WalletInfo {
		out := make([]WalletInfo, n)
		for i := range out {
			out[i] = WalletInfo{
				Address: common.BigToAddress(big.NewInt(int64(i + 1))),
				Name:    "p", Type: "provider", ProviderID: uint64(i + 1),
				FILBalance: wei(1), USDFCBalance: wei(1),
				PaymentsFunds: big.NewInt(0), PaymentsAvailable: big.NewInt(0),
				PaymentsLocked: big.NewInt(0), PaymentsFundedUntil: big.NewInt(0),
			}
		}
		return out
	}
	snapA, snapB := mk(10), mk(20)
	e.wallets = snapA

	reg := prometheus.NewRegistry()
	reg.MustRegister(e)

	var writerWG, readerWG sync.WaitGroup
	stop := make(chan struct{})

	// Writer: continuously swap between the two snapshots until told to stop.
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			next := snapA
			if i%2 == 1 {
				next = snapB
			}
			e.walletsMux.Lock()
			e.wallets = next
			e.walletsMux.Unlock()
		}
	}()

	// Readers: Gather repeatedly; every result must be a complete set for one
	// snapshot (10 or 20 series), never empty or partial.
	for range 4 {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			for range 500 {
				mfs, err := reg.Gather()
				if err != nil {
					t.Errorf("Gather: %v", err)
					return
				}
				for _, mf := range mfs {
					if mf.GetName() != "test_wallet_fil_balance" {
						continue
					}
					if n := len(mf.GetMetric()); n != 10 && n != 20 {
						t.Errorf("partial/empty set observed: %d series", n)
						return
					}
				}
			}
		}()
	}

	readerWG.Wait()
	close(stop)
	writerWG.Wait()
}
