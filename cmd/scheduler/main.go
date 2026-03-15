package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	hcraft "github.com/hashicorp/raft"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"

	"github.com/Ritpra93/forge/internal/metrics"
	"github.com/Ritpra93/forge/internal/proto/forgepb"
	raftpkg "github.com/Ritpra93/forge/internal/raft"
	"github.com/Ritpra93/forge/internal/scheduler"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Parse required environment variables.
	nodeID := requireEnv(logger, "NODE_ID")
	raftBind := requireEnv(logger, "RAFT_BIND")
	grpcAddr := requireEnv(logger, "GRPC_ADDR")
	peersRaw := requireEnv(logger, "PEERS")

	metricsPort := envOrDefault("METRICS_PORT", "9090")
	bootstrap := os.Getenv("BOOTSTRAP") == "true"

	peers := strings.Split(peersRaw, ",")
	dataDir := "/tmp/forge-raft-" + nodeID

	logger.Info("scheduler starting",
		"node_id", nodeID,
		"raft_bind", raftBind,
		"grpc_addr", grpcAddr,
		"peers", peers,
		"bootstrap", bootstrap,
		"data_dir", dataDir,
	)

	// Ensure the Raft data directory exists.
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		logger.Error("creating data directory", "path", dataDir, "error", err)
		os.Exit(1)
	}

	// Set up Raft (without bootstrap — we handle cluster formation below).
	logger.Info("setting up Raft node")
	r, fsm, err := raftpkg.SetupRaft(nodeID, raftBind, dataDir, false)
	if err != nil {
		logger.Error("setting up raft", "error", err)
		os.Exit(1)
	}

	// Bootstrap cluster with all known nodes.
	// All nodes attempt this — already-bootstrapped clusters return an error
	// that we safely ignore.
	if bootstrap {
		servers := buildClusterConfig(nodeID, raftBind, peers)
		logger.Info("bootstrapping cluster", "servers", len(servers))
		f := r.BootstrapCluster(hcraft.Configuration{Servers: servers})
		if err := f.Error(); err != nil {
			// ErrCantBootstrap is expected on restart — the cluster is already formed.
			logger.Warn("bootstrap result (may be expected on restart)", "error", err)
		}
	}

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

	// Create the gRPC scheduler server.
	srv := scheduler.NewForgeSchedulerServer(r, fsm, logger)

	grpcServer := grpc.NewServer()
	forgepb.RegisterForgeSchedulerServer(grpcServer, srv)

	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		logger.Error("listening on gRPC address", "addr", grpcAddr, "error", err)
		os.Exit(1)
	}

	logger.Info("gRPC server listening", "addr", grpcAddr)

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			logger.Error("gRPC server failed", "error", err)
		}
	}()

	// Start background goroutines.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.StartWorkerTracker(ctx)
	go srv.StartRetryScheduler(ctx)
	go srv.StartAssigner(ctx)

	logger.Info("scheduler ready",
		"node_id", nodeID,
		"grpc_addr", grpcAddr,
	)

	// Wait for shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	logger.Info("received shutdown signal", "signal", sig)

	cancel()
	grpcServer.GracefulStop()

	if err := r.Shutdown().Error(); err != nil {
		logger.Error("shutting down raft", "error", err)
	}

	logger.Info("scheduler stopped")
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

// buildClusterConfig creates the Raft cluster configuration from the local node
// and its peer addresses. Peer IDs are derived from addresses (the hostname
// portion matches the docker-compose service name which is also the NODE_ID).
func buildClusterConfig(localID, localAddr string, peerAddrs []string) []hcraft.Server {
	servers := []hcraft.Server{
		{
			Suffrage: hcraft.Voter,
			ID:       hcraft.ServerID(localID),
			Address:  hcraft.ServerAddress(localAddr),
		},
	}

	for _, addr := range peerAddrs {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		// Derive peer ID from the hostname part of the address.
		// e.g. "scheduler-2:7000" → "scheduler-2"
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		servers = append(servers, hcraft.Server{
			Suffrage: hcraft.Voter,
			ID:       hcraft.ServerID(host),
			Address:  hcraft.ServerAddress(addr),
		})
	}

	return servers
}
