package exporter

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/prometheus/client_golang/prometheus"

	"wallet-exporter/internal/config"
	"wallet-exporter/internal/contracts"
)

type WalletInfo struct {
	Address      common.Address
	Name         string
	Type         string // "provider", "client", "operator", "other"
	ProviderID   uint64 // Only for providers
	IsActive     bool   // Only for providers
	IsApproved   bool   // Only for providers - whether approved in WarmStorage
	Description  string // Only for providers
	FILBalance   *big.Int
	USDFCBalance *big.Int

	// Payments contract account info
	PaymentsFunds       *big.Int // Total funds in Payments contract
	PaymentsAvailable   *big.Int // Available funds (funds - actualLockup)
	PaymentsLocked      *big.Int // Current locked funds
	PaymentsFundedUntil *big.Int // Epoch when funds run out (calculated)
}

type WalletExporter struct {
	config              *config.Config
	client              *ethclient.Client
	warmStorageContract *contracts.WarmStorageService
	viewContract        *contracts.WarmStorageServiceStateView
	registryContract    *contracts.ServiceProviderRegistry
	usdfcContract       *contracts.ERC20

	// Prometheus metrics
	registry                 *prometheus.Registry
	filBalanceGauge          *prometheus.GaugeVec
	usdfcBalanceGauge        *prometheus.GaugeVec
	walletInfoGauge          *prometheus.GaugeVec
	paymentsFundsGauge       *prometheus.GaugeVec
	paymentsAvailableGauge   *prometheus.GaugeVec
	paymentsLockedGauge      *prometheus.GaugeVec
	paymentsFundedUntilGauge *prometheus.GaugeVec
	scrapeDuration           prometheus.Gauge
	scrapeErrors             prometheus.Counter

	// Cache
	wallets    []WalletInfo
	walletsMux sync.RWMutex
	lastScrape time.Time

	// Ping metrics
	pingSuccessGauge  *prometheus.GaugeVec
	pingDurationGauge *prometheus.GaugeVec

	logger *slog.Logger
}

func New(cfg *config.Config, logger *slog.Logger) (*WalletExporter, error) {
	// Connect to Ethereum client
	client, err := ethclient.Dial(cfg.RPCURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Ethereum client: %w", err)
	}

	// Create contract instances
	warmStorageAddr := common.HexToAddress(cfg.WarmStorageAddress)
	warmStorageContract, err := contracts.NewWarmStorageService(warmStorageAddr, client)
	if err != nil {
		return nil, fmt.Errorf("failed to create WarmStorageService contract: %w", err)
	}

	// Get view contract address
	viewAddr, err := warmStorageContract.ViewContractAddress(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get view contract address: %w", err)
	}

	viewContract, err := contracts.NewWarmStorageServiceStateView(viewAddr, client)
	if err != nil {
		return nil, fmt.Errorf("failed to create view contract: %w", err)
	}

	// Get registry contract address
	registryAddr, err := warmStorageContract.ServiceProviderRegistry(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get registry address: %w", err)
	}

	registryContract, err := contracts.NewServiceProviderRegistry(registryAddr, client)
	if err != nil {
		return nil, fmt.Errorf("failed to create registry contract: %w", err)
	}

	// Create USDFC token contract
	usdfcAddr := common.HexToAddress(cfg.USDFCTokenAddress)
	usdfcContract, err := contracts.NewERC20(usdfcAddr, client)
	if err != nil {
		return nil, fmt.Errorf("failed to create USDFC contract: %w", err)
	}

	// Create custom registry to avoid conflicts
	registry := prometheus.NewRegistry()

	// Create Prometheus metrics
	filBalanceGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: fmt.Sprintf("%s_wallet_fil_balance", cfg.MetricsPrefix),
			Help: "FIL (native token) balance for each wallet",
		},
		[]string{"address", "name", "type", "provider_id", "is_active", "approved"},
	)

	usdfcBalanceGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: fmt.Sprintf("%s_wallet_usdfc_balance", cfg.MetricsPrefix),
			Help: "USDFC token balance for each wallet",
		},
		[]string{"address", "name", "type", "provider_id", "is_active", "approved"},
	)

	walletInfoGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: fmt.Sprintf("%s_wallet_info", cfg.MetricsPrefix),
			Help: "Wallet information (always 1)",
		},
		[]string{"address", "name", "type", "provider_id", "description", "is_active", "approved"},
	)

	paymentsFundsGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: fmt.Sprintf("%s_wallet_payments_funds", cfg.MetricsPrefix),
			Help: "Total funds in Payments contract for each wallet",
		},
		[]string{"address", "name", "type", "provider_id", "is_active", "approved"},
	)

	paymentsAvailableGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: fmt.Sprintf("%s_wallet_payments_available", cfg.MetricsPrefix),
			Help: "Available funds in Payments contract (after lockup)",
		},
		[]string{"address", "name", "type", "provider_id", "is_active", "approved"},
	)

	paymentsLockedGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: fmt.Sprintf("%s_wallet_payments_locked", cfg.MetricsPrefix),
			Help: "Locked funds in Payments contract",
		},
		[]string{"address", "name", "type", "provider_id", "is_active", "approved"},
	)

	paymentsFundedUntilGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: fmt.Sprintf("%s_wallet_payments_funded_until_epoch", cfg.MetricsPrefix),
			Help: "Estimated epoch when Payments funds will run out",
		},
		[]string{"address", "name", "type", "provider_id", "is_active", "approved"},
	)

	scrapeDuration := prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: fmt.Sprintf("%s_scrape_duration_seconds", cfg.MetricsPrefix),
			Help: "Duration of the last scrape in seconds",
		},
	)

	scrapeErrors := prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: fmt.Sprintf("%s_scrape_errors_total", cfg.MetricsPrefix),
			Help: "Total number of scrape errors",
		},
	)

	pingSuccessGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: fmt.Sprintf("%s_provider_ping_success", cfg.MetricsPrefix),
			Help: "1 if the provider ping was successful (HTTP 200), 0 otherwise",
		},
		[]string{"address", "name", "provider_id", "service_url"},
	)

	pingDurationGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: fmt.Sprintf("%s_provider_ping_ms", cfg.MetricsPrefix),
			Help: "Duration of the ping request in milliseconds",
		},
		[]string{"address", "name", "provider_id", "service_url"},
	)

	// Register metrics with custom registry
	registry.MustRegister(filBalanceGauge)
	registry.MustRegister(usdfcBalanceGauge)
	registry.MustRegister(walletInfoGauge)
	registry.MustRegister(paymentsFundsGauge)
	registry.MustRegister(paymentsAvailableGauge)
	registry.MustRegister(paymentsLockedGauge)
	registry.MustRegister(paymentsFundedUntilGauge)
	registry.MustRegister(scrapeDuration)
	registry.MustRegister(scrapeErrors)
	registry.MustRegister(pingSuccessGauge)
	registry.MustRegister(pingDurationGauge)

	return &WalletExporter{
		config:                   cfg,
		client:                   client,
		warmStorageContract:      warmStorageContract,
		viewContract:             viewContract,
		registryContract:         registryContract,
		usdfcContract:            usdfcContract,
		registry:                 registry,
		filBalanceGauge:          filBalanceGauge,
		usdfcBalanceGauge:        usdfcBalanceGauge,
		walletInfoGauge:          walletInfoGauge,
		paymentsFundsGauge:       paymentsFundsGauge,
		paymentsAvailableGauge:   paymentsAvailableGauge,
		paymentsLockedGauge:      paymentsLockedGauge,
		paymentsFundedUntilGauge: paymentsFundedUntilGauge,
		scrapeDuration:           scrapeDuration,
		scrapeErrors:             scrapeErrors,
		pingSuccessGauge:         pingSuccessGauge,
		pingDurationGauge:        pingDurationGauge,
		wallets:                  []WalletInfo{},
		logger:                   logger,
	}, nil
}

func (e *WalletExporter) Start(ctx context.Context) error {
	e.logger.Info("Starting wallet exporter", "scrape_interval", e.config.ScrapeInterval)

	// Initial scrape
	if err := e.scrape(ctx); err != nil {
		e.logger.Error("Initial scrape failed", "error", err)
		e.scrapeErrors.Inc()
	}

	// Periodic scrape
	ticker := time.NewTicker(e.config.ScrapeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			e.logger.Info("Stopping wallet exporter")
			return ctx.Err()
		case <-ticker.C:
			if err := e.scrape(ctx); err != nil {
				e.logger.Error("Scrape failed", "error", err)
				e.scrapeErrors.Inc()
			}
		}
	}
}

func (e *WalletExporter) scrape(ctx context.Context) error {
	start := time.Now()
	defer func() {
		duration := time.Since(start).Seconds()
		e.scrapeDuration.Set(duration)
		e.lastScrape = time.Now()
		e.logger.Info("Scrape completed", "duration_seconds", duration)
	}()

	e.logger.Info("Starting scrape...")

	var allWallets []WalletInfo
	var wg sync.WaitGroup
	var pingResults map[uint64]PingResult

	// 1. Fetch storage provider wallets
	providerWallets, err := e.fetchProviderWallets(ctx)
	if err != nil {
		e.logger.Warn("Failed to fetch provider wallets", "error", err)
	} else {
		allWallets = append(allWallets, providerWallets...)
		e.logger.Info("Found storage providers", "count", len(providerWallets))

		// Start concurrent pings for providers
		wg.Add(1)
		go func() {
			defer wg.Done()
			pingResults = e.pingProviders(ctx, providerWallets)
		}()
	}

	// 2. Fetch custom wallets
	customWallets, err := e.fetchCustomWallets(ctx)
	if err != nil {
		e.logger.Warn("Failed to fetch custom wallets", "error", err)
	} else {
		allWallets = append(allWallets, customWallets...)
		e.logger.Info("Found custom wallets", "count", len(customWallets))
	}

	// Wait for pings to complete
	wg.Wait()

	// Update cache
	e.walletsMux.Lock()
	e.wallets = allWallets
	e.walletsMux.Unlock()

	// Update Prometheus metrics
	e.updateMetrics(allWallets, pingResults)

	e.logger.Info("Successfully scraped total wallets", "count", len(allWallets))
	return nil
}

func (e *WalletExporter) fetchProviderWallets(ctx context.Context) ([]WalletInfo, error) {
	// Get total provider count
	providerCount, err := e.registryContract.GetProviderCount(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get provider count: %w", err)
	}

	// Get approved provider IDs for checking
	approvedIDs, err := e.viewContract.GetApprovedProviders(nil, big.NewInt(0), big.NewInt(0))
	if err != nil {
		e.logger.Warn("Failed to get approved providers", "error", err)
		e.scrapeErrors.Inc()
		approvedIDs = []*big.Int{} // Continue with empty approved list
	}

	// Create a map for quick lookup of approved providers
	approvedMap := make(map[uint64]bool)
	for _, id := range approvedIDs {
		approvedMap[id.Uint64()] = true
	}

	e.logger.Info("Provider count stats", "total", providerCount.Uint64(), "approved", len(approvedIDs))

	// Fetch all providers (provider IDs start from 1)
	wallets := make([]WalletInfo, 0, int(providerCount.Int64()))
	walletChan := make(chan WalletInfo, int(providerCount.Int64()))
	errorChan := make(chan error, int(providerCount.Int64()))

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, e.config.MaxConcurrentRequests) // Limit concurrent requests

	for i := uint64(1); i <= providerCount.Uint64(); i++ {
		wg.Add(1)
		go func(providerID uint64) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			isApproved := approvedMap[providerID]
			wallet, err := e.fetchProviderWallet(ctx, big.NewInt(int64(providerID)), isApproved)
			if err != nil {
				errorChan <- fmt.Errorf("failed to fetch provider %d: %w", providerID, err)
				return
			}
			walletChan <- wallet
		}(i)
	}

	// Wait for all goroutines to finish
	go func() {
		wg.Wait()
		close(walletChan)
		close(errorChan)
	}()

	// Collect results
	for wallet := range walletChan {
		wallets = append(wallets, wallet)
	}

	// Log any errors and increment scrape error counter
	for err := range errorChan {
		e.logger.Warn("Provider fetch warning", "error", err)
		e.scrapeErrors.Inc()
	}

	return wallets, nil
}

func (e *WalletExporter) fetchProviderWallet(ctx context.Context, providerID *big.Int, isApproved bool) (WalletInfo, error) {
	// Get provider info from registry
	result, err := e.registryContract.GetProvider(nil, providerID)
	if err != nil {
		return WalletInfo{}, fmt.Errorf("failed to get provider info: %w", err)
	}

	// Extract the nested info struct
	info := result.Info

	// Get FIL balance
	filBalance, err := e.client.BalanceAt(ctx, info.ServiceProvider, nil)
	if err != nil {
		return WalletInfo{}, fmt.Errorf("failed to get FIL balance: %w", err)
	}

	// Get USDFC balance
	usdfcBalance, err := e.usdfcContract.BalanceOf(nil, info.ServiceProvider)
	if err != nil {
		e.logger.Warn("Failed to get USDFC balance", "address", info.ServiceProvider.Hex(), "error", err)
		usdfcBalance = big.NewInt(0)
	}

	// Get Payments contract info
	paymentsInfo, err := e.fetchPaymentsInfo(ctx, info.ServiceProvider)
	if err != nil {
		e.logger.Warn("Failed to get Payments info", "address", info.ServiceProvider.Hex(), "error", err)
		paymentsInfo = &PaymentsInfo{
			Funds:            big.NewInt(0),
			Available:        big.NewInt(0),
			Locked:           big.NewInt(0),
			FundedUntilEpoch: big.NewInt(0),
		}
	}

	return WalletInfo{
		Address:             info.ServiceProvider,
		Name:                info.Name,
		Type:                "provider",
		ProviderID:          providerID.Uint64(),
		IsActive:            info.IsActive,
		IsApproved:          isApproved,
		Description:         info.Description,
		FILBalance:          filBalance,
		USDFCBalance:        usdfcBalance,
		PaymentsFunds:       paymentsInfo.Funds,
		PaymentsAvailable:   paymentsInfo.Available,
		PaymentsLocked:      paymentsInfo.Locked,
		PaymentsFundedUntil: paymentsInfo.FundedUntilEpoch,
	}, nil
}

func (e *WalletExporter) fetchCustomWallets(ctx context.Context) ([]WalletInfo, error) {
	if len(e.config.CustomWallets) == 0 {
		return []WalletInfo{}, nil
	}

	wallets := make([]WalletInfo, 0, len(e.config.CustomWallets))
	walletChan := make(chan WalletInfo, len(e.config.CustomWallets))
	errorChan := make(chan error, len(e.config.CustomWallets))

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, e.config.MaxConcurrentRequests)

	for _, customWallet := range e.config.CustomWallets {
		wg.Add(1)
		go func(cw config.CustomWallet) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			wallet, err := e.fetchCustomWallet(ctx, cw)
			if err != nil {
				errorChan <- fmt.Errorf("failed to fetch custom wallet %s: %w", cw.Address, err)
				return
			}
			walletChan <- wallet
		}(customWallet)
	}

	go func() {
		wg.Wait()
		close(walletChan)
		close(errorChan)
	}()

	for wallet := range walletChan {
		wallets = append(wallets, wallet)
	}

	for err := range errorChan {
		e.logger.Warn("Custom wallet fetch warning", "error", err)
		e.scrapeErrors.Inc()
	}

	return wallets, nil
}

func (e *WalletExporter) fetchCustomWallet(ctx context.Context, cw config.CustomWallet) (WalletInfo, error) {
	address := common.HexToAddress(cw.Address)

	// Get FIL balance
	filBalance, err := e.client.BalanceAt(ctx, address, nil)
	if err != nil {
		return WalletInfo{}, fmt.Errorf("failed to get FIL balance: %w", err)
	}

	// Get USDFC balance
	usdfcBalance, err := e.usdfcContract.BalanceOf(nil, address)
	if err != nil {
		e.logger.Warn("Failed to get USDFC balance", "address", address.Hex(), "error", err)
		usdfcBalance = big.NewInt(0)
	}

	// Get Payments contract info
	paymentsInfo, err := e.fetchPaymentsInfo(ctx, address)
	if err != nil {
		e.logger.Warn("Failed to get Payments info", "address", address.Hex(), "error", err)
		paymentsInfo = &PaymentsInfo{
			Funds:            big.NewInt(0),
			Available:        big.NewInt(0),
			Locked:           big.NewInt(0),
			FundedUntilEpoch: big.NewInt(0),
		}
	}

	return WalletInfo{
		Address:             address,
		Name:                cw.Name,
		Type:                cw.Type,
		ProviderID:          0,
		IsActive:            false,
		IsApproved:          false,
		Description:         "",
		FILBalance:          filBalance,
		USDFCBalance:        usdfcBalance,
		PaymentsFunds:       paymentsInfo.Funds,
		PaymentsAvailable:   paymentsInfo.Available,
		PaymentsLocked:      paymentsInfo.Locked,
		PaymentsFundedUntil: paymentsInfo.FundedUntilEpoch,
	}, nil
}

type PingResult struct {
	Success    bool
	Duration   time.Duration
	ServiceURL string
}

func (e *WalletExporter) updateMetrics(wallets []WalletInfo, pingResults map[uint64]PingResult) {
	// Reset metrics to avoid stale data
	e.filBalanceGauge.Reset()
	e.usdfcBalanceGauge.Reset()
	e.walletInfoGauge.Reset()
	e.paymentsFundsGauge.Reset()
	e.paymentsAvailableGauge.Reset()
	e.paymentsLockedGauge.Reset()
	e.paymentsFundedUntilGauge.Reset()
	e.pingSuccessGauge.Reset()
	e.pingDurationGauge.Reset()

	for _, wallet := range wallets {
		providerID := fmt.Sprintf("%d", wallet.ProviderID)
		if wallet.Type != "provider" {
			providerID = ""
		}

		isActive := fmt.Sprintf("%t", wallet.IsActive)
		if wallet.Type != "provider" {
			isActive = ""
		}

		approved := fmt.Sprintf("%t", wallet.IsApproved)
		if wallet.Type != "provider" {
			approved = ""
		}

		labels := prometheus.Labels{
			"address":     wallet.Address.Hex(),
			"name":        wallet.Name,
			"type":        wallet.Type,
			"provider_id": providerID,
			"is_active":   isActive,
			"approved":    approved,
		}

		// Set FIL balance (in FIL, not wei)
		filFloat, _ := new(big.Float).Quo(
			new(big.Float).SetInt(wallet.FILBalance),
			big.NewFloat(1e18),
		).Float64()
		e.filBalanceGauge.With(labels).Set(filFloat)

		// Set USDFC balance (USDFC has 18 decimals)
		usdfcFloat, _ := new(big.Float).Quo(
			new(big.Float).SetInt(wallet.USDFCBalance),
			big.NewFloat(1e18),
		).Float64()
		e.usdfcBalanceGauge.With(labels).Set(usdfcFloat)

		// Set Payments contract metrics (USDFC has 18 decimals)
		paymentsFundsFloat, _ := new(big.Float).Quo(
			new(big.Float).SetInt(wallet.PaymentsFunds),
			big.NewFloat(1e18),
		).Float64()
		e.paymentsFundsGauge.With(labels).Set(paymentsFundsFloat)

		paymentsAvailableFloat, _ := new(big.Float).Quo(
			new(big.Float).SetInt(wallet.PaymentsAvailable),
			big.NewFloat(1e18),
		).Float64()
		e.paymentsAvailableGauge.With(labels).Set(paymentsAvailableFloat)

		paymentsLockedFloat, _ := new(big.Float).Quo(
			new(big.Float).SetInt(wallet.PaymentsLocked),
			big.NewFloat(1e18),
		).Float64()
		e.paymentsLockedGauge.With(labels).Set(paymentsLockedFloat)

		// PaymentsFundedUntil is an epoch (block number), not a token amount
		paymentsFundedUntilFloat, _ := new(big.Float).SetInt(wallet.PaymentsFundedUntil).Float64()
		e.paymentsFundedUntilGauge.With(labels).Set(paymentsFundedUntilFloat)

		// Set info metric
		infoLabels := prometheus.Labels{
			"address":     wallet.Address.Hex(),
			"name":        wallet.Name,
			"type":        wallet.Type,
			"provider_id": providerID,
			"description": wallet.Description,
			"is_active":   isActive,
			"approved":    approved,
		}
		e.walletInfoGauge.With(infoLabels).Set(1)

		// Set Ping metrics if available (only for providers)
		if wallet.Type == "provider" {
			if result, ok := pingResults[wallet.ProviderID]; ok {
				pingLabels := prometheus.Labels{
					"address":     wallet.Address.Hex(),
					"name":        wallet.Name,
					"provider_id": providerID,
					"service_url": result.ServiceURL,
				}

				successVal := 0.0
				if result.Success {
					successVal = 1.0
				}
				e.pingSuccessGauge.With(pingLabels).Set(successVal)
				e.pingDurationGauge.With(pingLabels).Set(float64(result.Duration.Milliseconds()))
			}
		}
	}
}

func (e *WalletExporter) GetWallets() []WalletInfo {
	e.walletsMux.RLock()
	defer e.walletsMux.RUnlock()
	return e.wallets
}

func (e *WalletExporter) GetLastScrape() time.Time {
	e.walletsMux.RLock()
	defer e.walletsMux.RUnlock()
	return e.lastScrape
}

func (e *WalletExporter) GetRegistry() *prometheus.Registry {
	return e.registry
}

func (e *WalletExporter) Close() {
	if e.client != nil {
		e.client.Close()
	}
}

// PaymentsInfo holds the calculated Payments contract account information
type PaymentsInfo struct {
	Funds            *big.Int // Total funds in contract
	Available        *big.Int // Available funds (funds - actualLockup)
	Locked           *big.Int // Current locked funds
	FundedUntilEpoch *big.Int // Estimated epoch when funds run out
}

// fetchPaymentsInfo fetches account info from Payments contract using getAccountInfoIfSettled
func (e *WalletExporter) fetchPaymentsInfo(ctx context.Context, address common.Address) (*PaymentsInfo, error) {
	usdfcAddr := common.HexToAddress(e.config.USDFCTokenAddress)
	paymentsAddr := common.HexToAddress(e.config.PaymentsAddress)

	// Create Payments contract instance using abigen generated binding
	paymentsContract, err := contracts.NewPaymentsCaller(paymentsAddr, e.client)
	if err != nil {
		return nil, fmt.Errorf("failed to create Payments contract: %w", err)
	}

	// Call getAccountInfoIfSettled - type-safe method from abigen
	result, err := paymentsContract.GetAccountInfoIfSettled(nil, usdfcAddr, address)
	if err != nil {
		// Handle error - might be account doesn't exist
		return &PaymentsInfo{
			Funds:            big.NewInt(0),
			Available:        big.NewInt(0),
			Locked:           big.NewInt(0),
			FundedUntilEpoch: big.NewInt(0),
		}, nil
	}

	// Extract values from the result struct
	fundedUntilEpoch := result.FundedUntilEpoch
	currentFunds := result.CurrentFunds
	availableFunds := result.AvailableFunds
	// currentLockupRate := result.CurrentLockupRate // not needed for now

	// Calculate locked amount: locked = currentFunds - availableFunds
	locked := new(big.Int).Sub(currentFunds, availableFunds)
	if locked.Cmp(big.NewInt(0)) < 0 {
		locked = big.NewInt(0)
	}

	return &PaymentsInfo{
		Funds:            currentFunds,
		Available:        availableFunds,
		Locked:           locked,
		FundedUntilEpoch: fundedUntilEpoch,
	}, nil
}

// pingProviders pings all providers concurrently and returns results
func (e *WalletExporter) pingProviders(ctx context.Context, providers []WalletInfo) map[uint64]PingResult {
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, e.config.MaxConcurrentRequests)

	results := make(map[uint64]PingResult)
	var mu sync.Mutex

	for _, p := range providers {
		// specific check for provider ID > 0 just in case
		if p.ProviderID == 0 {
			continue
		}

		wg.Add(1)
		go func(p WalletInfo) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			result, ok := e.pingProvider(ctx, p)
			if ok {
				mu.Lock()
				results[p.ProviderID] = result
				mu.Unlock()
			}
		}(p)
	}

	wg.Wait()
	return results
}

func (e *WalletExporter) pingProvider(ctx context.Context, p WalletInfo) (PingResult, bool) {
	// 1. Get Provider with Product (Product Type 0 for PDP)
	// We use the generated struct directly
	result, err := e.registryContract.GetProviderWithProduct(nil, big.NewInt(int64(p.ProviderID)), 0)
	if err != nil {
		// Log detailed error to debug
		e.logger.Debug("Failed to get PDP product", "provider_id", p.ProviderID, "error", err)
		return PingResult{}, false
	}

	// Check if product is active
	if !result.Product.IsActive {
		return PingResult{}, false
	}

	// 2. Decode Capabilities to find Service URL
	var serviceURL string
	for i, key := range result.Product.CapabilityKeys {
		if key == "serviceURL" {
			if i < len(result.ProductCapabilityValues) {
				serviceURL = string(result.ProductCapabilityValues[i])
			}
			break
		}
	}

	if serviceURL == "" {
		e.logger.Debug("PDP product has no serviceURL", "provider_id", p.ProviderID)
		return PingResult{}, false
	}

	e.logger.Debug("Found serviceURL", "provider_id", p.ProviderID, "url", serviceURL)

	// 3. Ping
	// Remove trailing slash if present
	baseURL := strings.TrimRight(serviceURL, "/")
	pingURL := baseURL + "/pdp/ping"

	client := http.Client{
		Timeout: 5 * time.Second,
	}

	start := time.Now()
	resp, err := client.Get(pingURL)
	duration := time.Since(start)

	if err != nil {
		e.logger.Warn("Ping failed", "provider_id", p.ProviderID, "name", p.Name, "url", pingURL, "error", err)
		return PingResult{Success: false, Duration: duration, ServiceURL: serviceURL}, true
	}
	defer resp.Body.Close()

	success := resp.StatusCode == http.StatusOK
	if !success {
		e.logger.Warn("Ping returned non-200 status", "status", resp.StatusCode, "provider_id", p.ProviderID, "name", p.Name, "url", pingURL)
	}

	return PingResult{Success: success, Duration: duration, ServiceURL: serviceURL}, true
}
