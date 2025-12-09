package main

import (
	"context"
	"fmt"
	"log"
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

	log.Println("Starting Dealbot Wallet Exporter...")

	// Load configuration
	log.Println("Loading configuration...")
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Failed to load configuration: %v", err)
	}

	log.Println("✓ Configuration loaded successfully")
	log.Printf("  Network: %s", cfg.Network)
	log.Printf("  RPC URL: %s", cfg.RPCURL)
	log.Printf("  Warm Storage Address: %s", cfg.WarmStorageAddress)
	log.Printf("  USDFC Token Address: %s", cfg.USDFCTokenAddress)
	log.Printf("  Payments Address: %s", cfg.PaymentsAddress)
	log.Printf("  Exporter Port: %d", cfg.ExporterPort)
	log.Printf("  Scrape Interval: %v", cfg.ScrapeInterval)
	log.Printf("  Custom Wallets: %d", len(cfg.CustomWallets))

	// Create exporter
	log.Println("Creating exporter...")
	exp, err := exporter.New(cfg)
	if err != nil {
		log.Fatalf("❌ Failed to create exporter: %v", err)
	}
	defer exp.Close()

	log.Println("✓ Exporter created successfully")

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start exporter in background
	go func() {
		if err := exp.Start(ctx); err != nil && err != context.Canceled {
			log.Fatalf("Exporter failed: %v", err)
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
		log.Printf("Starting HTTP server on port %d", cfg.ExporterPort)
		log.Printf("Metrics available at: http://localhost:%d/metrics", cfg.ExporterPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down gracefully...")

	// Cancel context to stop exporter
	cancel()

	// Shutdown HTTP server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	log.Println("Exporter stopped")
}
