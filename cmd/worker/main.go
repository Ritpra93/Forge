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
	metricsPort := 9091
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

	// TODO: full worker setup (gRPC connection, handler registration, task execution)
	slog.Info("worker starting")
	select {}
}
