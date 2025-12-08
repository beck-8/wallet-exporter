package exporter

import (
	"context"
	"fmt"
	"log"
	"math/big"
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
}

type WalletExporter struct {
	config              *config.Config
	client              *ethclient.Client
	warmStorageContract *contracts.WarmStorageService
	viewContract        *contracts.WarmStorageServiceStateView
	registryContract    *contracts.ServiceProviderRegistry
	usdfcContract       *contracts.ERC20

	// Prometheus metrics
	registry           *prometheus.Registry
	filBalanceGauge    *prometheus.GaugeVec
	usdfcBalanceGauge  *prometheus.GaugeVec
	walletInfoGauge    *prometheus.GaugeVec
	scrapeDuration     prometheus.Gauge
	scrapeErrors       prometheus.Counter

	// Cache
	wallets      []WalletInfo
	walletsMux   sync.RWMutex
	lastScrape   time.Time
}

func New(cfg *config.Config) (*WalletExporter, error) {
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

	// Register metrics with custom registry
	registry.MustRegister(filBalanceGauge)
	registry.MustRegister(usdfcBalanceGauge)
	registry.MustRegister(walletInfoGauge)
	registry.MustRegister(scrapeDuration)
	registry.MustRegister(scrapeErrors)

	return &WalletExporter{
		config:              cfg,
		client:              client,
		warmStorageContract: warmStorageContract,
		viewContract:        viewContract,
		registryContract:    registryContract,
		usdfcContract:       usdfcContract,
		registry:            registry,
		filBalanceGauge:     filBalanceGauge,
		usdfcBalanceGauge:   usdfcBalanceGauge,
		walletInfoGauge:     walletInfoGauge,
		scrapeDuration:      scrapeDuration,
		scrapeErrors:        scrapeErrors,
		wallets:             []WalletInfo{},
	}, nil
}

func (e *WalletExporter) Start(ctx context.Context) error {
	log.Printf("Starting wallet exporter with scrape interval: %v", e.config.ScrapeInterval)

	// Initial scrape
	if err := e.scrape(ctx); err != nil {
		log.Printf("Initial scrape failed: %v", err)
		e.scrapeErrors.Inc()
	}

	// Periodic scrape
	ticker := time.NewTicker(e.config.ScrapeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Stopping wallet exporter")
			return ctx.Err()
		case <-ticker.C:
			if err := e.scrape(ctx); err != nil {
				log.Printf("Scrape failed: %v", err)
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
		log.Printf("Scrape completed in %.2fs", duration)
	}()

	log.Println("Starting scrape...")

	var allWallets []WalletInfo

	// 1. Fetch storage provider wallets
	providerWallets, err := e.fetchProviderWallets(ctx)
	if err != nil {
		log.Printf("Warning: failed to fetch provider wallets: %v", err)
	} else {
		allWallets = append(allWallets, providerWallets...)
		log.Printf("Found %d storage providers", len(providerWallets))
	}

	// 2. Fetch custom wallets
	customWallets, err := e.fetchCustomWallets(ctx)
	if err != nil {
		log.Printf("Warning: failed to fetch custom wallets: %v", err)
	} else {
		allWallets = append(allWallets, customWallets...)
		log.Printf("Found %d custom wallets", len(customWallets))
	}

	// Update cache
	e.walletsMux.Lock()
	e.wallets = allWallets
	e.walletsMux.Unlock()

	// Update Prometheus metrics
	e.updateMetrics(allWallets)

	log.Printf("Successfully scraped %d total wallets", len(allWallets))
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
		log.Printf("Warning: failed to get approved providers: %v", err)
		approvedIDs = []*big.Int{} // Continue with empty approved list
	}

	// Create a map for quick lookup of approved providers
	approvedMap := make(map[uint64]bool)
	for _, id := range approvedIDs {
		approvedMap[id.Uint64()] = true
	}

	log.Printf("Found %d total providers, %d approved", providerCount.Uint64(), len(approvedIDs))

	// Fetch all providers (provider IDs start from 1)
	wallets := make([]WalletInfo, 0, int(providerCount.Int64()))
	walletChan := make(chan WalletInfo, int(providerCount.Int64()))
	errorChan := make(chan error, int(providerCount.Int64()))

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 10) // Limit concurrent requests to 10

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

	// Log any errors
	for err := range errorChan {
		log.Printf("Warning: %v", err)
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
		log.Printf("Warning: failed to get USDFC balance for %s: %v", info.ServiceProvider.Hex(), err)
		usdfcBalance = big.NewInt(0)
	}

	return WalletInfo{
		Address:      info.ServiceProvider,
		Name:         info.Name,
		Type:         "provider",
		ProviderID:   providerID.Uint64(),
		IsActive:     info.IsActive,
		IsApproved:   isApproved,
		Description:  info.Description,
		FILBalance:   filBalance,
		USDFCBalance: usdfcBalance,
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
	semaphore := make(chan struct{}, 10)

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
		log.Printf("Warning: %v", err)
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
		log.Printf("Warning: failed to get USDFC balance for %s: %v", address.Hex(), err)
		usdfcBalance = big.NewInt(0)
	}

	return WalletInfo{
		Address:      address,
		Name:         cw.Name,
		Type:         cw.Type,
		ProviderID:   0,
		IsActive:     false,
		IsApproved:   false,
		Description:  "",
		FILBalance:   filBalance,
		USDFCBalance: usdfcBalance,
	}, nil
}

func (e *WalletExporter) updateMetrics(wallets []WalletInfo) {
	// Reset metrics
	e.filBalanceGauge.Reset()
	e.usdfcBalanceGauge.Reset()
	e.walletInfoGauge.Reset()

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
