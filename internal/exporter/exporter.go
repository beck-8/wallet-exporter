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

	// Prometheus metrics.
	//
	// The per-wallet metrics are exposed through a custom prometheus.Collector
	// (Describe/Collect on *WalletExporter) rather than GaugeVecs. Collect emits
	// the full set from an immutable snapshot on every Gather, so a /metrics
	// scrape can never observe a half-populated state — this eliminates the
	// Reset()+Set() window that previously made series flicker to zero.
	registry                *prometheus.Registry
	filBalanceDesc          *prometheus.Desc
	usdfcBalanceDesc        *prometheus.Desc
	walletInfoDesc          *prometheus.Desc
	paymentsFundsDesc       *prometheus.Desc
	paymentsAvailableDesc   *prometheus.Desc
	paymentsLockedDesc      *prometheus.Desc
	paymentsFundedUntilDesc *prometheus.Desc
	pingSuccessDesc         *prometheus.Desc
	pingDurationDesc        *prometheus.Desc
	scrapeDuration          prometheus.Gauge
	scrapeErrors            prometheus.Counter

	// Cache / snapshot for the collector
	wallets     []WalletInfo
	pingResults map[uint64]PingResult
	walletsMux  sync.RWMutex
	lastScrape  time.Time

	// Last-good cache: keeps the most recent successful value per wallet so a
	// transient RPC failure (e.g. 429) carries the previous reading forward
	// instead of writing 0 or dropping the series (which produces the spiky
	// drop-to-zero graphs).
	lastGoodProviders map[uint64]WalletInfo // keyed by provider ID
	lastGoodCustom    map[string]WalletInfo // keyed by lower-case address
	lastApprovedMap   map[uint64]bool       // last successful approved-provider set
	cacheMux          sync.Mutex

	logger *slog.Logger
}

func New(cfg *config.Config, logger *slog.Logger) (*WalletExporter, error) {
	// Connect to Ethereum client with optional Bearer-token auth and a
	// retrying transport (handles RPC rate limiting / 429).
	client, err := dialRPC(context.Background(), cfg.RPCURL, cfg.RPCToken, cfg.RPCMaxRetries, logger)
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

	// Scalar metrics. These are updated atomically as single values and never
	// Reset(), so they are registered directly (no flicker risk).
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

	balanceLabels := []string{"address", "name", "type", "provider_id", "is_active", "approved"}
	pingLabels := []string{"address", "name", "provider_id", "service_url"}

	e := &WalletExporter{
		config:              cfg,
		client:              client,
		warmStorageContract: warmStorageContract,
		viewContract:        viewContract,
		registryContract:    registryContract,
		usdfcContract:       usdfcContract,
		registry:            registry,
		filBalanceDesc: prometheus.NewDesc(
			fmt.Sprintf("%s_wallet_fil_balance", cfg.MetricsPrefix),
			"FIL (native token) balance for each wallet", balanceLabels, nil),
		usdfcBalanceDesc: prometheus.NewDesc(
			fmt.Sprintf("%s_wallet_usdfc_balance", cfg.MetricsPrefix),
			"USDFC token balance for each wallet", balanceLabels, nil),
		walletInfoDesc: prometheus.NewDesc(
			fmt.Sprintf("%s_wallet_info", cfg.MetricsPrefix),
			"Wallet information (always 1)",
			[]string{"address", "name", "type", "provider_id", "description", "is_active", "approved"}, nil),
		paymentsFundsDesc: prometheus.NewDesc(
			fmt.Sprintf("%s_wallet_payments_funds", cfg.MetricsPrefix),
			"Total funds in Payments contract for each wallet", balanceLabels, nil),
		paymentsAvailableDesc: prometheus.NewDesc(
			fmt.Sprintf("%s_wallet_payments_available", cfg.MetricsPrefix),
			"Available funds in Payments contract (after lockup)", balanceLabels, nil),
		paymentsLockedDesc: prometheus.NewDesc(
			fmt.Sprintf("%s_wallet_payments_locked", cfg.MetricsPrefix),
			"Locked funds in Payments contract", balanceLabels, nil),
		paymentsFundedUntilDesc: prometheus.NewDesc(
			fmt.Sprintf("%s_wallet_payments_funded_until_epoch", cfg.MetricsPrefix),
			"Estimated epoch when Payments funds will run out", balanceLabels, nil),
		pingSuccessDesc: prometheus.NewDesc(
			fmt.Sprintf("%s_provider_ping_success", cfg.MetricsPrefix),
			"1 if the provider ping was successful (HTTP 200), 0 otherwise", pingLabels, nil),
		pingDurationDesc: prometheus.NewDesc(
			fmt.Sprintf("%s_provider_ping_ms", cfg.MetricsPrefix),
			"Duration of the ping request in milliseconds", pingLabels, nil),
		scrapeDuration:    scrapeDuration,
		scrapeErrors:      scrapeErrors,
		wallets:           []WalletInfo{},
		lastGoodProviders: make(map[uint64]WalletInfo),
		lastGoodCustom:    make(map[string]WalletInfo),
		lastApprovedMap:   make(map[uint64]bool),
		logger:            logger,
	}

	// Register the scalar metrics and the per-wallet collector (e itself).
	registry.MustRegister(scrapeDuration, scrapeErrors, e)

	return e, nil
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

	// Publish the snapshot the collector reads from. Swapping the references
	// under the lock is the only mutation the collector ever observes, so a
	// /metrics scrape always sees a complete, internally-consistent set.
	e.walletsMux.Lock()
	e.wallets = allWallets
	e.pingResults = pingResults
	e.walletsMux.Unlock()

	e.logger.Info("Successfully scraped total wallets", "count", len(allWallets))
	return nil
}

func (e *WalletExporter) fetchProviderWallets(ctx context.Context) ([]WalletInfo, error) {
	// Snapshot the last-good values so failed fetches can be carried forward.
	prevProviders := e.snapshotProviders()

	// Get total provider count
	providerCount, err := e.registryContract.GetProviderCount(nil)
	if err != nil {
		e.scrapeErrors.Inc()
		// Carry the entire cached provider set forward so the series survive a
		// transient failure instead of every provider dropping to zero.
		if len(prevProviders) > 0 {
			e.logger.Warn("Failed to get provider count, using cached providers",
				"error", err, "cached", len(prevProviders))
			return mapToSlice(prevProviders), nil
		}
		return nil, fmt.Errorf("failed to get provider count: %w", err)
	}

	// Get approved provider IDs for checking
	approvedIDs, err := e.viewContract.GetApprovedProviders(nil, big.NewInt(0), big.NewInt(0))
	var approvedMap map[uint64]bool
	if err != nil {
		// Reuse the last successful approved set so the `approved` label doesn't
		// flip to false for every provider on a transient failure.
		approvedMap = e.snapshotApproved()
		e.logger.Warn("Failed to get approved providers, using cached approved set",
			"error", err, "cached", len(approvedMap))
		e.scrapeErrors.Inc()
	} else {
		approvedMap = make(map[uint64]bool)
		for _, id := range approvedIDs {
			approvedMap[id.Uint64()] = true
		}
		e.storeApproved(approvedMap)
	}

	e.logger.Info("Provider count stats", "total", providerCount.Uint64(), "approved", len(approvedMap))

	// Fetch all providers (provider IDs start from 1)
	count := providerCount.Uint64()
	wallets := make([]WalletInfo, 0, count)
	walletChan := make(chan WalletInfo, count)
	errorChan := make(chan error, count)

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, e.config.MaxConcurrentRequests) // Limit concurrent requests

	for i := uint64(1); i <= count; i++ {
		wg.Add(1)
		go func(providerID uint64) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			var prev *WalletInfo
			if p, ok := prevProviders[providerID]; ok {
				prev = &p
			}
			isApproved := approvedMap[providerID]
			wallet, ok, err := e.fetchProviderWallet(ctx, big.NewInt(int64(providerID)), isApproved, prev)
			if err != nil {
				errorChan <- err
			}
			if ok {
				walletChan <- wallet
			}
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

	// Refresh the last-good cache with this round's usable values.
	e.storeProviders(wallets)

	return wallets, nil
}

// fetchProviderWallet fetches a single provider. It returns best-effort data:
// when an individual RPC call fails it falls back to the cached previous value
// (prev) rather than writing 0 or dropping the whole wallet. The bool result is
// false only when there is no fresh data and no cache to fall back on, in which
// case the wallet is skipped this round. A non-nil error is reported for metrics
// even when the wallet is still usable via the cache.
func (e *WalletExporter) fetchProviderWallet(ctx context.Context, providerID *big.Int, isApproved bool, prev *WalletInfo) (WalletInfo, bool, error) {
	id := providerID.Uint64()

	// Get provider info from registry
	result, err := e.registryContract.GetProvider(nil, providerID)
	if err != nil {
		if prev != nil {
			carried := *prev
			carried.IsApproved = isApproved
			return carried, true, fmt.Errorf("get provider %d info (using cached value): %w", id, err)
		}
		return WalletInfo{}, false, fmt.Errorf("get provider %d info: %w", id, err)
	}

	// Extract the nested info struct
	info := result.Info
	wallet := WalletInfo{
		Address:     info.ServiceProvider,
		Name:        info.Name,
		Type:        "provider",
		ProviderID:  id,
		IsActive:    info.IsActive,
		IsApproved:  isApproved,
		Description: info.Description,
	}

	var firstErr error
	note := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	// Get FIL balance
	filBalance, err := e.client.BalanceAt(ctx, info.ServiceProvider, nil)
	if err != nil {
		if prev != nil && prev.FILBalance != nil {
			filBalance = prev.FILBalance
			note(fmt.Errorf("get FIL balance for provider %d (using cached value): %w", id, err))
		} else {
			// No fresh and no cached balance — skip rather than emit a 0.
			return WalletInfo{}, false, fmt.Errorf("get FIL balance for provider %d: %w", id, err)
		}
	}
	wallet.FILBalance = filBalance

	// Get USDFC balance
	usdfcBalance, err := e.usdfcContract.BalanceOf(nil, info.ServiceProvider)
	if err != nil {
		note(fmt.Errorf("get USDFC balance for provider %d (using cached value): %w", id, err))
		if prev != nil {
			usdfcBalance = orZero(prev.USDFCBalance)
		} else {
			usdfcBalance = big.NewInt(0)
		}
	}
	wallet.USDFCBalance = usdfcBalance

	// Get Payments contract info
	paymentsInfo, err := e.fetchPaymentsInfo(ctx, info.ServiceProvider)
	if err != nil {
		note(fmt.Errorf("get Payments info for provider %d (using cached value): %w", id, err))
		applyCachedPayments(&wallet, prev)
	} else {
		wallet.PaymentsFunds = paymentsInfo.Funds
		wallet.PaymentsAvailable = paymentsInfo.Available
		wallet.PaymentsLocked = paymentsInfo.Locked
		wallet.PaymentsFundedUntil = paymentsInfo.FundedUntilEpoch
	}

	return wallet, true, firstErr
}

func (e *WalletExporter) fetchCustomWallets(ctx context.Context) ([]WalletInfo, error) {
	if len(e.config.CustomWallets) == 0 {
		return []WalletInfo{}, nil
	}

	prevCustom := e.snapshotCustom()

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

			var prev *WalletInfo
			if p, ok := prevCustom[cacheKey(cw.Address)]; ok {
				prev = &p
			}
			wallet, ok, err := e.fetchCustomWallet(ctx, cw, prev)
			if err != nil {
				errorChan <- err
			}
			if ok {
				walletChan <- wallet
			}
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

	e.storeCustom(wallets)

	return wallets, nil
}

// fetchCustomWallet mirrors fetchProviderWallet's carry-forward behaviour for a
// configured custom wallet: failed RPC calls fall back to the cached previous
// value instead of writing 0 or dropping the series.
func (e *WalletExporter) fetchCustomWallet(ctx context.Context, cw config.CustomWallet, prev *WalletInfo) (WalletInfo, bool, error) {
	address := common.HexToAddress(cw.Address)

	wallet := WalletInfo{
		Address:    address,
		Name:       cw.Name,
		Type:       cw.Type,
		ProviderID: 0,
		IsActive:   false,
		IsApproved: false,
	}

	var firstErr error
	note := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	// Get FIL balance
	filBalance, err := e.client.BalanceAt(ctx, address, nil)
	if err != nil {
		if prev != nil && prev.FILBalance != nil {
			filBalance = prev.FILBalance
			note(fmt.Errorf("get FIL balance for %s (using cached value): %w", cw.Address, err))
		} else {
			return WalletInfo{}, false, fmt.Errorf("get FIL balance for %s: %w", cw.Address, err)
		}
	}
	wallet.FILBalance = filBalance

	// Get USDFC balance
	usdfcBalance, err := e.usdfcContract.BalanceOf(nil, address)
	if err != nil {
		note(fmt.Errorf("get USDFC balance for %s (using cached value): %w", cw.Address, err))
		if prev != nil {
			usdfcBalance = orZero(prev.USDFCBalance)
		} else {
			usdfcBalance = big.NewInt(0)
		}
	}
	wallet.USDFCBalance = usdfcBalance

	// Get Payments contract info
	paymentsInfo, err := e.fetchPaymentsInfo(ctx, address)
	if err != nil {
		note(fmt.Errorf("get Payments info for %s (using cached value): %w", cw.Address, err))
		applyCachedPayments(&wallet, prev)
	} else {
		wallet.PaymentsFunds = paymentsInfo.Funds
		wallet.PaymentsAvailable = paymentsInfo.Available
		wallet.PaymentsLocked = paymentsInfo.Locked
		wallet.PaymentsFundedUntil = paymentsInfo.FundedUntilEpoch
	}

	return wallet, true, firstErr
}

type PingResult struct {
	Success    bool
	Duration   time.Duration
	ServiceURL string
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
		// A reverted call means the account simply has no Payments entry yet,
		// which is a legitimate zero — not an RPC failure. Return zeros without
		// an error so it is reported as 0 rather than carried forward.
		if strings.Contains(err.Error(), "execution reverted") {
			return &PaymentsInfo{
				Funds:            big.NewInt(0),
				Available:        big.NewInt(0),
				Locked:           big.NewInt(0),
				FundedUntilEpoch: big.NewInt(0),
			}, nil
		}
		// Any other error (e.g. RPC rate limiting) is transient — surface it so
		// the caller can carry the previous value forward instead of zeroing.
		return nil, fmt.Errorf("getAccountInfoIfSettled: %w", err)
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
