package raft

import (
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	hcraft "github.com/hashicorp/raft"
)

// setupTestRaftNode creates a single Raft node backed by in-memory stores
// and transport. The node is shut down automatically when the test ends.
func setupTestRaftNode(t *testing.T, nodeID string) (*hcraft.Raft, *TaskFSM, hcraft.ServerAddress, *hcraft.InmemTransport) {
	t.Helper()

	config := hcraft.DefaultConfig()
	config.LocalID = hcraft.ServerID(nodeID)
	config.HeartbeatTimeout = 200 * time.Millisecond
	config.ElectionTimeout = 200 * time.Millisecond
	config.LeaderLeaseTimeout = 100 * time.Millisecond
	config.Logger = hclog.NewNullLogger()

	fsm := NewTaskFSM()
	store := hcraft.NewInmemStore()
	snaps := hcraft.NewInmemSnapshotStore()
	addr, transport := hcraft.NewInmemTransport("")

	r, err := hcraft.NewRaft(config, fsm, store, store, snaps, transport)
	if err != nil {
		t.Fatalf("creating raft node %s: %v", nodeID, err)
	}
	t.Cleanup(func() {
		f := r.Shutdown()
		if err := f.Error(); err != nil {
			t.Logf("shutting down raft node %s: %v", nodeID, err)
		}
	})

	return r, fsm, addr, transport
}

// waitForLeader polls the given Raft nodes until one becomes leader.
// It returns the leader node or fails the test on timeout.
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

// getFreePort returns a free TCP port on localhost.
func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("getting free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func TestThreeNodeCluster(t *testing.T) {
	// Create 3 nodes with in-memory transport.
	r1, fsm1, addr1, trans1 := setupTestRaftNode(t, "node-1")
	r2, fsm2, addr2, trans2 := setupTestRaftNode(t, "node-2")
	r3, fsm3, addr3, trans3 := setupTestRaftNode(t, "node-3")

	// Connect all transports to each other.
	trans1.Connect(addr2, trans2)
	trans1.Connect(addr3, trans3)
	trans2.Connect(addr1, trans1)
	trans2.Connect(addr3, trans3)
	trans3.Connect(addr1, trans1)
	trans3.Connect(addr2, trans2)

	// Bootstrap node-1 with all 3 servers in the configuration.
	cfg := hcraft.Configuration{
		Servers: []hcraft.Server{
			{Suffrage: hcraft.Voter, ID: "node-1", Address: addr1},
			{Suffrage: hcraft.Voter, ID: "node-2", Address: addr2},
			{Suffrage: hcraft.Voter, ID: "node-3", Address: addr3},
		},
	}
	f := r1.BootstrapCluster(cfg)
	if err := f.Error(); err != nil {
		t.Fatalf("bootstrapping cluster: %v", err)
	}

	// Wait for leader election.
	rafts := []*hcraft.Raft{r1, r2, r3}
	leader := waitForLeader(t, rafts, 10*time.Second)

	// Verify exactly 1 leader and 2 followers.
	leaders := 0
	followers := 0
	for _, r := range rafts {
		switch r.State() {
		case hcraft.Leader:
			leaders++
		case hcraft.Follower:
			followers++
		}
	}
	if leaders != 1 {
		t.Fatalf("expected 1 leader, got %d", leaders)
	}
	if followers != 2 {
		t.Fatalf("expected 2 followers, got %d", followers)
	}

	// Apply a create_task command via the leader.
	task := Task{
		ID:         "test-task-1",
		Type:       "fibonacci",
		MaxRetries: 3,
	}
	payload, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshaling task: %v", err)
	}
	cmd := Command{
		Type:    "create_task",
		TaskID:  "test-task-1",
		Payload: payload,
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshaling command: %v", err)
	}

	applyFuture := leader.Apply(data, 5*time.Second)
	if err := applyFuture.Error(); err != nil {
		t.Fatalf("applying command: %v", err)
	}

	// Wait for all 3 FSMs to replicate the task.
	fsms := []*TaskFSM{fsm1, fsm2, fsm3}
	deadline := time.After(5 * time.Second)
	for {
		allHaveTask := true
		for _, fsm := range fsms {
			if fsm.GetTask("test-task-1") == nil {
				allHaveTask = false
				break
			}
		}
		if allHaveTask {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for task replication across all nodes")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Verify consistent state across all FSMs.
	for i, fsm := range fsms {
		got := fsm.GetTask("test-task-1")
		if got.Status != "pending" {
			t.Errorf("node %d: expected status 'pending', got %q", i+1, got.Status)
		}
		if got.Type != "fibonacci" {
			t.Errorf("node %d: expected type 'fibonacci', got %q", i+1, got.Type)
		}
		if got.MaxRetries != 3 {
			t.Errorf("node %d: expected max_retries 3, got %d", i+1, got.MaxRetries)
		}
	}
}

func TestSetupRaftSingleNode(t *testing.T) {
	dataDir := t.TempDir()
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	r, fsm, err := SetupRaft("test-node", addr, dataDir, true)
	if err != nil {
		t.Fatalf("SetupRaft: %v", err)
	}
	t.Cleanup(func() {
		f := r.Shutdown()
		if err := f.Error(); err != nil {
			t.Logf("shutting down raft: %v", err)
		}
	})

	// Single-node cluster should elect itself as leader quickly.
	waitForLeader(t, []*hcraft.Raft{r}, 10*time.Second)

	if r.State() != hcraft.Leader {
		t.Fatalf("expected single node to be leader, got %v", r.State())
	}

	// Apply a command and verify FSM state.
	task := Task{
		ID:         "single-task-1",
		Type:       "sleep",
		MaxRetries: 1,
	}
	payload, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshaling task: %v", err)
	}
	cmd := Command{
		Type:    "create_task",
		TaskID:  "single-task-1",
		Payload: payload,
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshaling command: %v", err)
	}

	applyFuture := r.Apply(data, 5*time.Second)
	if err := applyFuture.Error(); err != nil {
		t.Fatalf("applying command: %v", err)
	}

	got := fsm.GetTask("single-task-1")
	if got == nil {
		t.Fatal("expected task to exist in FSM after apply")
	}
	if got.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", got.Status)
	}
	if got.Type != "sleep" {
		t.Errorf("expected type 'sleep', got %q", got.Type)
	}
}
