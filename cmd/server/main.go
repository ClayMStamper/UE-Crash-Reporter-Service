package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"ue-crash-reporter/internal/server"
	"ue-crash-reporter/internal/storage"
)

func main() {
	logger := log.New(os.Stdout, "[ue-crash] ", log.LstdFlags|log.Lmsgprefix)

	// --- Configuration from environment (Docker-friendly) ---
	addr := envOr("ADDR", ":8080")
	dataDir := envOr("DATA_DIR", "./data")
	dbPath := envOr("DB_PATH", filepath.Join(dataDir, "crashes.db"))

	// Ensure directories exist.
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		logger.Fatalf("create data dir: %v", err)
	}

	// --- Storage ---
	store, err := storage.New(dbPath)
	if err != nil {
		logger.Fatalf("open storage: %v", err)
	}
	defer store.Close()
	logger.Printf("database: %s", dbPath)

	// --- HTTP server ---
	srv, err := server.New(store, dataDir, logger)
	if err != nil {
		logger.Fatalf("init server: %v", err)
	}

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      srv.Routes(),
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown on SIGINT / SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		logger.Printf("listening on %s", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("listen: %v", err)
		}
	}()

	<-quit
	logger.Println("shutting down…")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		logger.Printf("shutdown: %v", err)
	}
	logger.Println("stopped")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
