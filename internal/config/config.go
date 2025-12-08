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
	Network             string
	RPCURL              string
	WarmStorageAddress  string
	USDFCTokenAddress   string
	CustomWallets       []CustomWallet
	ExporterPort        int
	ScrapeInterval      time.Duration
	MetricsPrefix       string
	LogLevel            string
}

type CustomWallet struct {
	Address string
	Name    string
	Type    string // "client", "operator", "other"
}

func Load() (*Config, error) {
	// Try to load .env file (ignore error if file doesn't exist)
	_ = godotenv.Load()

	// Default USDFC addresses per network
	defaultUSDFC := map[string]string{
		"calibration": "0xb3042734b608a1B16e9e86B374A3f3e389B4cDf0",
		"mainnet":     "0x80B98d3aa09ffff255c3ba4A241111Ff1262F045",
	}

	network := getEnv("NETWORK", "calibration")

	cfg := &Config{
		Network:             network,
		RPCURL:              getEnv("RPC_URL", "https://api.calibration.node.glif.io/rpc/v1"),
		WarmStorageAddress:  getEnv("WARM_STORAGE_ADDRESS", "0x80617b65FD2EEa1D7fDe2B4F85977670690ed348"),
		USDFCTokenAddress:   getEnv("USDFC_TOKEN_ADDRESS", defaultUSDFC[network]),
		CustomWallets:       parseCustomWallets(getEnv("CUSTOM_WALLETS", "")),
		ExporterPort:        getEnvInt("EXPORTER_PORT", 9090),
		ScrapeInterval:      getEnvDuration("SCRAPE_INTERVAL", 60*time.Second),
		MetricsPrefix:       getEnv("METRICS_PREFIX", "dealbot"),
		LogLevel:            getEnv("LOG_LEVEL", "info"),
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return cfg, nil
}

// parseCustomWallets parses custom wallet configuration
// Format: "address1:name1:type1,address2:name2:type2,..."
// Example: "0x123...:Client A:client,0x456...:Operator B:operator"
func parseCustomWallets(walletsStr string) []CustomWallet {
	if walletsStr == "" {
		return []CustomWallet{}
	}

	var wallets []CustomWallet
	entries := strings.Split(walletsStr, ",")

	for _, entry := range entries {
		parts := strings.Split(strings.TrimSpace(entry), ":")
		if len(parts) < 2 {
			continue
		}

		wallet := CustomWallet{
			Address: strings.TrimSpace(parts[0]),
			Name:    strings.TrimSpace(parts[1]),
			Type:    "other",
		}

		if len(parts) >= 3 {
			wallet.Type = strings.TrimSpace(parts[2])
		}

		wallets = append(wallets, wallet)
	}

	return wallets
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
