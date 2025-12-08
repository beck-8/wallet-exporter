package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefault(t *testing.T) {
	// Clean environment
	os.Clearenv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Check defaults
	if cfg.Network != "calibration" {
		t.Errorf("Expected network 'calibration', got '%s'", cfg.Network)
	}

	if cfg.ExporterPort != 9090 {
		t.Errorf("Expected port 9090, got %d", cfg.ExporterPort)
	}

	if cfg.MetricsPrefix != "dealbot" {
		t.Errorf("Expected prefix 'dealbot', got '%s'", cfg.MetricsPrefix)
	}

	if cfg.ScrapeInterval != 60*time.Second {
		t.Errorf("Expected interval 60s, got %v", cfg.ScrapeInterval)
	}
}

func TestLoadFromEnv(t *testing.T) {
	// Set environment variables
	os.Setenv("NETWORK", "mainnet")
	os.Setenv("EXPORTER_PORT", "8080")
	os.Setenv("SCRAPE_INTERVAL", "30s")
	os.Setenv("METRICS_PREFIX", "test")
	defer os.Clearenv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.Network != "mainnet" {
		t.Errorf("Expected network 'mainnet', got '%s'", cfg.Network)
	}

	if cfg.ExporterPort != 8080 {
		t.Errorf("Expected port 8080, got %d", cfg.ExporterPort)
	}

	if cfg.ScrapeInterval != 30*time.Second {
		t.Errorf("Expected interval 30s, got %v", cfg.ScrapeInterval)
	}

	if cfg.MetricsPrefix != "test" {
		t.Errorf("Expected prefix 'test', got '%s'", cfg.MetricsPrefix)
	}
}

func TestValidateWarmStorageAddress(t *testing.T) {
	cfg := &Config{
		Network:            "calibration",
		RPCURL:             "https://api.calibration.node.glif.io/rpc/v1",
		WarmStorageAddress: "",
		ExporterPort:       9090,
		ScrapeInterval:     60 * time.Second,
		MetricsPrefix:      "dealbot",
	}

	err := cfg.Validate()
	if err == nil {
		t.Error("Expected validation error for empty WarmStorageAddress")
	}
}

func TestValidateRPCURL(t *testing.T) {
	cfg := &Config{
		Network:            "calibration",
		RPCURL:             "",
		WarmStorageAddress: "0x1234567890123456789012345678901234567890",
		ExporterPort:       9090,
		ScrapeInterval:     60 * time.Second,
		MetricsPrefix:      "dealbot",
	}

	err := cfg.Validate()
	if err == nil {
		t.Error("Expected validation error for empty RPCURL")
	}
}

func TestParseCustomWallets(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"", 0},
		{"0x123:Wallet1:client", 1},
		{"0x123:Wallet1:client,0x456:Wallet2:operator", 2},
		{"invalid", 0},
	}

	for _, tt := range tests {
		wallets := parseCustomWallets(tt.input)
		if len(wallets) != tt.expected {
			t.Errorf("parseCustomWallets(%q) = %d wallets, want %d",
				tt.input, len(wallets), tt.expected)
		}
	}
}

func TestDefaultUSDFCAddress(t *testing.T) {
	tests := []struct {
		network  string
		expected string
	}{
		{"calibration", "0xb3042734b608a1B16e9e86B374A3f3e389B4cDf0"},
		{"mainnet", "0x80B98d3aa09ffff255c3ba4A241111Ff1262F045"},
	}

	for _, tt := range tests {
		os.Clearenv()
		os.Setenv("NETWORK", tt.network)
		os.Setenv("WARM_STORAGE_ADDRESS", "0x1234567890123456789012345678901234567890")
		os.Setenv("RPC_URL", "https://test.com")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.USDFCTokenAddress != tt.expected {
			t.Errorf("Network %s: expected USDFC address %s, got %s",
				tt.network, tt.expected, cfg.USDFCTokenAddress)
		}
	}
}
