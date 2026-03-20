package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Ritpra93/forge/internal/metrics"
	"github.com/Ritpra93/forge/internal/worker"
	"github.com/Ritpra93/forge/internal/worker/handlers"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Parse required environment variables.
	workerID := requireEnv(logger, "WORKER_ID")
	schedulerAddr := requireEnv(logger, "SCHEDULER_ADDR")

	maxSlots := int32(4)
	if v := os.Getenv("MAX_SLOTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			logger.Error("invalid MAX_SLOTS", "value", v, "error", err)
			os.Exit(1)
		}
		maxSlots = int32(n)
	}

	metricsPort := envOrDefault("METRICS_PORT", "9091")

	logger.Info("worker starting",
		"worker_id", workerID,
		"scheduler_addr", schedulerAddr,
		"max_slots", maxSlots,
	)

	// Register Prometheus metrics.
	metrics.Register()

	// Start metrics HTTP server.
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		addr := fmt.Sprintf(":%s", metricsPort)
		logger.Info("metrics server starting", "addr", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			logger.Error("metrics server failed", "error", err)
		}
	}()

	// Create the worker with all available task handlers.
	w := worker.NewWorker(
		workerID,
		schedulerAddr,
		maxSlots,
		logger,
		&handlers.SleepHandler{},
		&handlers.FibonacciHandler{},
		&handlers.HTTPCheckHandler{},
	)

	// Set up graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		logger.Info("received shutdown signal", "signal", sig)
		cancel()
	}()

	logger.Info("worker connecting to scheduler",
		"worker_id", workerID,
		"scheduler_addr", schedulerAddr,
	)

	if err := w.Run(ctx); err != nil {
		logger.Error("worker stopped with error", "error", err)
		os.Exit(1)
	}

	logger.Info("worker stopped")
}

// requireEnv reads an environment variable or exits with an error.
func requireEnv(logger *slog.Logger, key string) string {
	v := os.Getenv(key)
	if v == "" {
		logger.Error("required environment variable not set", "key", key)
		os.Exit(1)
	}
	return v
}

// envOrDefault reads an environment variable with a fallback default.
func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
