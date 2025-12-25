package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Network               string
	RPCURL                string
	WarmStorageAddress    string
	USDFCTokenAddress     string
	PaymentsAddress       string
	CustomWallets         []CustomWallet
	ExporterPort          int
	ScrapeInterval        time.Duration
	MetricsPrefix         string
	LogLevel              string
	MaxConcurrentRequests int
}

type CustomWallet struct {
	Address string
	Name    string
	Type    string // "client", "operator", "other"
}

func Load() (*Config, error) {
	// Try to load .env file (ignore error if file doesn't exist)
	_ = godotenv.Load()

	// Default addresses per network
	// Official contract addresses from Filecoin Synapse
	defaultWarmStorage := map[string]string{
		"calibration": "0x02925630df557F957f70E112bA06e50965417CA0",
		"mainnet":     "0x8408502033C418E1bbC97cE9ac48E5528F371A9f",
	}

	defaultUSDFC := map[string]string{
		"calibration": "0xb3042734b608a1B16e9e86B374A3f3e389B4cDf0",
		"mainnet":     "0x80B98d3aa09ffff255c3ba4A241111Ff1262F045",
	}

	// Filecoin Pay contract (Payments)
	defaultPayments := map[string]string{
		"calibration": "0x09a0fDc2723fAd1A7b8e3e00eE5DF73841df55a0",
		"mainnet":     "0x23b1e018F08BB982348b15a86ee926eEBf7F4DAa",
	}

	network := getEnv("NETWORK", "calibration")

	cfg := &Config{
		Network:               network,
		RPCURL:                getEnv("RPC_URL", "https://api.calibration.node.glif.io/rpc/v1"),
		WarmStorageAddress:    getEnv("WARM_STORAGE_ADDRESS", defaultWarmStorage[network]),
		USDFCTokenAddress:     getEnv("USDFC_TOKEN_ADDRESS", defaultUSDFC[network]),
		PaymentsAddress:       getEnv("PAYMENTS_ADDRESS", defaultPayments[network]),
		CustomWallets:         parseCustomWallets(),
		ExporterPort:          getEnvInt("EXPORTER_PORT", 9091),
		ScrapeInterval:        getEnvDuration("SCRAPE_INTERVAL", 60*time.Second),
		MetricsPrefix:         getEnv("METRICS_PREFIX", "dealbot"),
		LogLevel:              getEnv("LOG_LEVEL", "info"),
		MaxConcurrentRequests: getEnvInt("MAX_CONCURRENT_REQUESTS", 10),
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return cfg, nil
}

// parseCustomWallets parses custom wallet configuration
// Supports two formats:
//  1. Legacy format (CUSTOM_WALLETS): "address1:name1:type1,address2:name2:type2,..."
//  2. Multi-line format (recommended): CUSTOM_WALLET_1, CUSTOM_WALLET_2, ...
//     Each line format: "address:name:type" or "address:name" (type defaults to "other")
//
// Example:
//
//	CUSTOM_WALLET_1=0x123...:Client A:client
//	CUSTOM_WALLET_2=0x456...:Operator B:operator
func parseCustomWallets() []CustomWallet {
	var wallets []CustomWallet

	// First, check for legacy CUSTOM_WALLETS format (for backward compatibility)
	if legacyWallets := getEnv("CUSTOM_WALLETS", ""); legacyWallets != "" {
		wallets = append(wallets, parseLegacyFormat(legacyWallets)...)
	}

	// Then, check for new CUSTOM_WALLET_N format
	for i := 1; i <= 1000; i++ { // Support up to 1000 custom wallets
		key := fmt.Sprintf("CUSTOM_WALLET_%d", i)
		if walletStr := os.Getenv(key); walletStr != "" {
			if wallet := parseWalletEntry(walletStr); wallet != nil {
				wallets = append(wallets, *wallet)
			}
		}
	}

	return wallets
}

// parseLegacyFormat parses the old comma-separated format
func parseLegacyFormat(walletsStr string) []CustomWallet {
	var wallets []CustomWallet
	entries := strings.Split(walletsStr, ",")

	for _, entry := range entries {
		if wallet := parseWalletEntry(entry); wallet != nil {
			wallets = append(wallets, *wallet)
		}
	}

	return wallets
}

// parseWalletEntry parses a single wallet entry
// Format: "address:name:type" or "address:name"
func parseWalletEntry(entry string) *CustomWallet {
	parts := strings.Split(strings.TrimSpace(entry), ":")
	if len(parts) < 2 {
		return nil
	}

	wallet := &CustomWallet{
		Address: strings.TrimSpace(parts[0]),
		Name:    strings.TrimSpace(parts[1]),
		Type:    "other",
	}

	if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
		wallet.Type = strings.TrimSpace(parts[2])
	}

	return wallet
}

func (c *Config) Validate() error {
	if c.RPCURL == "" {
		return fmt.Errorf("RPC_URL is required")
	}
	if c.WarmStorageAddress == "" {
		return fmt.Errorf("WARM_STORAGE_ADDRESS is required")
	}
	if c.ExporterPort <= 0 || c.ExporterPort > 65535 {
		return fmt.Errorf("EXPORTER_PORT must be between 1 and 65535")
	}
	if c.MaxConcurrentRequests <= 0 || c.MaxConcurrentRequests > 1000 {
		return fmt.Errorf("MAX_CONCURRENT_REQUESTS must be between 1 and 1000")
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}
