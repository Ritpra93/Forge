package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Ritpra93/forge/internal/metrics"
)

func main() {
	metricsPort := 9090
	if v := os.Getenv("METRICS_PORT"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &metricsPort); err != nil {
			slog.Error("invalid METRICS_PORT", "value", v, "error", err)
			os.Exit(1)
		}
	}

	metrics.Register()

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		slog.Info("metrics server starting", "port", metricsPort)
		if err := http.ListenAndServe(fmt.Sprintf(":%d", metricsPort), mux); err != nil {
			slog.Error("metrics server failed", "error", err)
		}
	}()

	// TODO: full scheduler setup (Raft, gRPC server, background goroutines)
	slog.Info("scheduler starting")
	select {}
}
