package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"wallet-exporter/internal/config"
	"wallet-exporter/internal/exporter"
)

func toFloat(balance *big.Int) float64 {
	f, _ := new(big.Float).Quo(
		new(big.Float).SetInt(balance),
		big.NewFloat(1e18),
	).Float64()
	return f
}

func main() {
	// Set up logging
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// Catch panics
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC: %v", r)
			debug.PrintStack()
			os.Exit(1)
		}
	}()

	// Load configuration
	log.Println("Loading configuration...")
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Failed to load configuration: %v", err)
	}

	// Initialize structured logger
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, opts))

	logger.Info("Starting Dealbot Wallet Exporter...")
	logger.Info("Configuration loaded successfully",
		"network", cfg.Network,
		"rpc_url", cfg.RPCURL,
		"warm_storage_addr", cfg.WarmStorageAddress,
		"usdfc_token_addr", cfg.USDFCTokenAddress,
		"payments_addr", cfg.PaymentsAddress,
		"exporter_port", cfg.ExporterPort,
		"scrape_interval", cfg.ScrapeInterval,
		"custom_wallets", len(cfg.CustomWallets),
	)

	// Create exporter
	logger.Info("Creating exporter...")
	exp, err := exporter.New(cfg, logger)
	if err != nil {
		logger.Error("Failed to create exporter", "error", err)
		os.Exit(1)
	}
	defer exp.Close()

	log.Println("✓ Exporter created successfully")

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start exporter in background
	go func() {
		if err := exp.Start(ctx); err != nil && err != context.Canceled {
			logger.Error("Exporter failed", "error", err)
			os.Exit(1)
		}
	}()

	// Setup HTTP server
	mux := http.NewServeMux()

	// Metrics endpoint (use custom registry)
	mux.Handle("/metrics", promhttp.HandlerFor(
		exp.GetRegistry(),
		promhttp.HandlerOpts{},
	))

	// Health endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK\n")
	})

	// Status endpoint
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		wallets := exp.GetWallets()
		lastScrape := exp.GetLastScrape()

		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "Dealbot Wallet Exporter Status\n")
		fmt.Fprintf(w, "==============================\n\n")
		fmt.Fprintf(w, "Network: %s\n", cfg.Network)
		fmt.Fprintf(w, "Wallets monitored: %d\n", len(wallets))
		fmt.Fprintf(w, "Last scrape: %s\n", lastScrape.Format(time.RFC3339))
		fmt.Fprintf(w, "Time since last scrape: %s\n\n", time.Since(lastScrape).Round(time.Second))

		// Group by type
		providers := []exporter.WalletInfo{}
		clients := []exporter.WalletInfo{}
		others := []exporter.WalletInfo{}

		for _, w := range wallets {
			switch w.Type {
			case "provider":
				providers = append(providers, w)
			case "client":
				clients = append(clients, w)
			default:
				others = append(others, w)
			}
		}

		if len(providers) > 0 {
			fmt.Fprintf(w, "Storage Providers (%d):\n", len(providers))
			for _, p := range providers {
				fmt.Fprintf(w, "  - ID: %d, Name: %s\n", p.ProviderID, p.Name)
				fmt.Fprintf(w, "    Address: %s\n", p.Address.Hex())
				fmt.Fprintf(w, "    FIL Balance: %.6f FIL\n", toFloat(p.FILBalance))
				fmt.Fprintf(w, "    USDFC Balance: %.6f USDFC\n", toFloat(p.USDFCBalance))
				fmt.Fprintf(w, "    Active: %t\n\n", p.IsActive)
			}
		}

		if len(clients) > 0 {
			fmt.Fprintf(w, "Client Wallets (%d):\n", len(clients))
			for _, c := range clients {
				fmt.Fprintf(w, "  - Name: %s\n", c.Name)
				fmt.Fprintf(w, "    Address: %s\n", c.Address.Hex())
				fmt.Fprintf(w, "    FIL Balance: %.6f FIL\n", toFloat(c.FILBalance))
				fmt.Fprintf(w, "    USDFC Balance: %.6f USDFC\n\n", toFloat(c.USDFCBalance))
			}
		}

		if len(others) > 0 {
			fmt.Fprintf(w, "Other Wallets (%d):\n", len(others))
			for _, o := range others {
				fmt.Fprintf(w, "  - Name: %s (Type: %s)\n", o.Name, o.Type)
				fmt.Fprintf(w, "    Address: %s\n", o.Address.Hex())
				fmt.Fprintf(w, "    FIL Balance: %.6f FIL\n", toFloat(o.FILBalance))
				fmt.Fprintf(w, "    USDFC Balance: %.6f USDFC\n\n", toFloat(o.USDFCBalance))
			}
		}
	})

	// Root endpoint
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
<!DOCTYPE html>
<html>
<head>
    <title>Dealbot Wallet Exporter</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 40px; }
        h1 { color: #333; }
        a { color: #0066cc; text-decoration: none; margin-right: 20px; }
        a:hover { text-decoration: underline; }
    </style>
</head>
<body>
    <h1>Dealbot Wallet Exporter</h1>
    <p>Prometheus exporter for Synapse storage provider wallet balances</p>
    <div>
        <a href="/metrics">Metrics</a>
        <a href="/status">Status</a>
        <a href="/health">Health</a>
    </div>
</body>
</html>
`)
	})

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.ExporterPort),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Start HTTP server in background
	go func() {
		logger.Info("Starting HTTP server", "port", cfg.ExporterPort)
		logger.Info("Metrics available", "url", fmt.Sprintf("http://localhost:%d/metrics", cfg.ExporterPort))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	logger.Info("Shutting down gracefully...")

	// Cancel context to stop exporter
	cancel()

	// Shutdown HTTP server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", "error", err)
	}

	logger.Info("Exporter stopped")
}
