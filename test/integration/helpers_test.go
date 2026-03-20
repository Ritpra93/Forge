//go:build integration

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	hcraft "github.com/hashicorp/raft"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
	raftpkg "github.com/Ritpra93/forge/internal/raft"
	"github.com/Ritpra93/forge/internal/scheduler"
)

const bufSize = 1024 * 1024

// testClusterNode holds all resources for a single node in a test cluster.
type testClusterNode struct {
	raft       *hcraft.Raft
	fsm        *raftpkg.TaskFSM
	addr       hcraft.ServerAddress
	transport  *hcraft.InmemTransport
	server     *scheduler.ForgeSchedulerServer
	grpcServer *grpc.Server
	bufLis     *bufconn.Listener
}

// fastConfig returns a TrackerConfig with short intervals for testing.
func fastConfig() scheduler.TrackerConfig {
	return scheduler.TrackerConfig{
		TrackerTick:   100 * time.Millisecond,
		HeartbeatDead: 500 * time.Millisecond,
		RetryTick:     100 * time.Millisecond,
		AssignerTick:  100 * time.Millisecond,
	}
}

// setupTestCluster creates an N-node in-memory Raft cluster with gRPC servers
// using the default fast test configuration.
func setupTestCluster(t *testing.T, nodeCount int) []*testClusterNode {
	t.Helper()
	return setupTestClusterWithConfig(t, nodeCount, fastConfig())
}

// setupTestClusterWithConfig creates an N-node in-memory Raft cluster with gRPC servers.
// It bootstraps the cluster, waits for leader election, and starts background
// goroutines (tracker, retry, assigner) on each node.
func setupTestClusterWithConfig(t *testing.T, nodeCount int, config scheduler.TrackerConfig) []*testClusterNode {
	t.Helper()
	nodes := make([]*testClusterNode, nodeCount)

	// Create all Raft nodes.
	for i := 0; i < nodeCount; i++ {
		nodeID := hcraft.ServerID(nodeIDStr(i))

		raftConfig := hcraft.DefaultConfig()
		raftConfig.LocalID = nodeID
		raftConfig.HeartbeatTimeout = 200 * time.Millisecond
		raftConfig.ElectionTimeout = 200 * time.Millisecond
		raftConfig.LeaderLeaseTimeout = 100 * time.Millisecond
		raftConfig.Logger = hclog.NewNullLogger()

		fsm := raftpkg.NewTaskFSM()
		store := hcraft.NewInmemStore()
		snaps := hcraft.NewInmemSnapshotStore()
		addr, transport := hcraft.NewInmemTransport("")

		r, err := hcraft.NewRaft(raftConfig, fsm, store, store, snaps, transport)
		if err != nil {
			t.Fatalf("creating raft node %d: %v", i, err)
		}
		t.Cleanup(func() { r.Shutdown() })

		nodes[i] = &testClusterNode{
			raft:      r,
			fsm:       fsm,
			addr:      addr,
			transport: transport,
		}
	}

	// Connect all transports pairwise.
	for i := 0; i < nodeCount; i++ {
		for j := 0; j < nodeCount; j++ {
			if i != j {
				nodes[i].transport.Connect(nodes[j].addr, nodes[j].transport)
			}
		}
	}

	// Bootstrap from node 0.
	servers := make([]hcraft.Server, nodeCount)
	for i, n := range nodes {
		servers[i] = hcraft.Server{
			Suffrage: hcraft.Voter,
			ID:       hcraft.ServerID(nodeIDStr(i)),
			Address:  n.addr,
		}
	}
	f := nodes[0].raft.BootstrapCluster(hcraft.Configuration{Servers: servers})
	if err := f.Error(); err != nil {
		t.Fatalf("bootstrapping cluster: %v", err)
	}

	// Wait for leader election.
	rafts := make([]*hcraft.Raft, nodeCount)
	for i, n := range nodes {
		rafts[i] = n.raft
	}
	waitForLeader(t, rafts, 10*time.Second)

	// Create gRPC servers and start background goroutines.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	logger := slog.Default()
	for _, n := range nodes {
		n.bufLis = bufconn.Listen(bufSize)
		t.Cleanup(func() { n.bufLis.Close() })

		n.server = scheduler.NewForgeSchedulerServerWithConfig(n.raft, n.fsm, logger, config)

		n.grpcServer = grpc.NewServer()
		forgepb.RegisterForgeSchedulerServer(n.grpcServer, n.server)
		go n.grpcServer.Serve(n.bufLis)
		t.Cleanup(func() { n.grpcServer.Stop() })

		go n.server.StartWorkerTracker(ctx)
		go n.server.StartRetryScheduler(ctx)
		go n.server.StartAssigner(ctx)
	}

	return nodes
}

func nodeIDStr(i int) string {
	return fmt.Sprintf("node-%d", i)
}

// waitForLeader polls the given Raft nodes until one becomes leader.
func waitForLeader(t *testing.T, rafts []*hcraft.Raft, timeout time.Duration) *hcraft.Raft {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for leader election")
		default:
		}
		for _, r := range rafts {
			if r.State() == hcraft.Leader {
				return r
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// findLeader returns the leader node from the cluster.
func findLeader(t *testing.T, nodes []*testClusterNode) *testClusterNode {
	t.Helper()
	for _, n := range nodes {
		if n.raft.State() == hcraft.Leader {
			return n
		}
	}
	t.Fatal("no leader found")
	return nil
}

// findFollower returns a follower node from the cluster.
func findFollower(t *testing.T, nodes []*testClusterNode) *testClusterNode {
	t.Helper()
	for _, n := range nodes {
		if n.raft.State() != hcraft.Leader {
			return n
		}
	}
	t.Fatal("no follower found")
	return nil
}

// dialBufconn creates a gRPC client for the given bufconn listener.
func dialBufconn(t *testing.T, lis *bufconn.Listener) forgepb.ForgeSchedulerClient {
	t.Helper()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("creating grpc client: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return forgepb.NewForgeSchedulerClient(conn)
}

// dialOptsForBufconn returns gRPC dial options for connecting to a bufconn listener.
func dialOptsForBufconn(t *testing.T, lis *bufconn.Listener) []grpc.DialOption {
	t.Helper()
	return []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
}

// countTasksByStatus counts tasks in the given statuses across the given FSM.
func countTasksByStatus(fsm *raftpkg.TaskFSM, statuses ...string) int {
	total := 0
	for _, s := range statuses {
		total += len(fsm.GetTasksByStatus(s))
	}
	return total
}

// pollUntil polls a condition until it returns true or the timeout expires.
func pollUntil(t *testing.T, timeout time.Duration, msg string, condition func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if condition() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out: %s", msg)
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
}
