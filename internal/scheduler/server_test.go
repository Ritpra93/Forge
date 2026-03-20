package scheduler

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	hcraft "github.com/hashicorp/raft"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
	raftpkg "github.com/Ritpra93/forge/internal/raft"
)

const bufSize = 1024 * 1024

// setupTestServer creates a single-node Raft leader with an in-process gRPC
// server over bufconn. Returns a client and the server instance.
func setupTestServer(t *testing.T) (forgepb.ForgeSchedulerClient, *ForgeSchedulerServer) {
	t.Helper()

	r, fsm := setupLeaderRaft(t)

	lis := bufconn.Listen(bufSize)
	t.Cleanup(func() { lis.Close() })

	logger := slog.Default()
	srv := NewForgeSchedulerServer(r, fsm, logger)

	grpcServer := grpc.NewServer()
	forgepb.RegisterForgeSchedulerServer(grpcServer, srv)
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			t.Logf("grpc server error: %v", err)
		}
	}()
	t.Cleanup(func() { grpcServer.Stop() })

	client := dialBufconn(t, lis)
	return client, srv
}

// setupFollowerServer creates an unbootstrapped Raft node (stays follower)
// with an in-process gRPC server. Returns a client for testing not-leader errors.
func setupFollowerServer(t *testing.T) forgepb.ForgeSchedulerClient {
	t.Helper()

	config := hcraft.DefaultConfig()
	config.LocalID = "follower-node"
	config.HeartbeatTimeout = 200 * time.Millisecond
	config.ElectionTimeout = 200 * time.Millisecond
	config.LeaderLeaseTimeout = 100 * time.Millisecond
	config.Logger = hclog.NewNullLogger()

	fsm := raftpkg.NewTaskFSM()
	store := hcraft.NewInmemStore()
	snaps := hcraft.NewInmemSnapshotStore()
	_, transport := hcraft.NewInmemTransport("")

	r, err := hcraft.NewRaft(config, fsm, store, store, snaps, transport)
	if err != nil {
		t.Fatalf("creating follower raft: %v", err)
	}
	t.Cleanup(func() { r.Shutdown() })

	lis := bufconn.Listen(bufSize)
	t.Cleanup(func() { lis.Close() })

	srv := NewForgeSchedulerServer(r, fsm, slog.Default())

	grpcServer := grpc.NewServer()
	forgepb.RegisterForgeSchedulerServer(grpcServer, srv)
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			t.Logf("grpc server error: %v", err)
		}
	}()
	t.Cleanup(func() { grpcServer.Stop() })

	return dialBufconn(t, lis)
}

// setupLeaderRaft creates a single-node in-memory Raft cluster that
// bootstraps and self-elects as leader.
func setupLeaderRaft(t *testing.T) (*hcraft.Raft, *raftpkg.TaskFSM) {
	t.Helper()

	config := hcraft.DefaultConfig()
	config.LocalID = "test-node"
	config.HeartbeatTimeout = 200 * time.Millisecond
	config.ElectionTimeout = 200 * time.Millisecond
	config.LeaderLeaseTimeout = 100 * time.Millisecond
	config.Logger = hclog.NewNullLogger()

	fsm := raftpkg.NewTaskFSM()
	store := hcraft.NewInmemStore()
	snaps := hcraft.NewInmemSnapshotStore()
	addr, transport := hcraft.NewInmemTransport("")

	r, err := hcraft.NewRaft(config, fsm, store, store, snaps, transport)
	if err != nil {
		t.Fatalf("creating raft: %v", err)
	}
	t.Cleanup(func() { r.Shutdown() })

	f := r.BootstrapCluster(hcraft.Configuration{
		Servers: []hcraft.Server{
			{Suffrage: hcraft.Voter, ID: "test-node", Address: addr},
		},
	})
	if err := f.Error(); err != nil {
		t.Fatalf("bootstrapping cluster: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.State() == hcraft.Leader {
			return r, fsm
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("node did not become leader")
	return nil, nil
}

// dialBufconn creates a gRPC client connection over the given bufconn listener.
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

func TestSubmitTask_Success(t *testing.T) {
	client, _ := setupTestServer(t)
	ctx := context.Background()

	resp, err := client.SubmitTask(ctx, &forgepb.TaskRequest{
		Type:           "fibonacci",
		Payload:        []byte(`{"n": 10}`),
		MaxRetries:     3,
		TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatalf("SubmitTask: %v", err)
	}
	if resp.GetTaskId() == "" {
		t.Error("expected non-empty task_id")
	}
	if resp.GetStatus() != "pending" {
		t.Errorf("got status %q, want %q", resp.GetStatus(), "pending")
	}
}

func TestSubmitTask_NotLeader(t *testing.T) {
	client := setupFollowerServer(t)
	ctx := context.Background()

	resp, err := client.SubmitTask(ctx, &forgepb.TaskRequest{
		Type: "sleep",
	})
	if resp != nil {
		t.Errorf("expected nil response, got %+v", resp)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("got code %v, want FailedPrecondition", st.Code())
	}
}

func TestGetTaskStatus_Found(t *testing.T) {
	client, _ := setupTestServer(t)
	ctx := context.Background()

	submitResp, err := client.SubmitTask(ctx, &forgepb.TaskRequest{
		Type:           "fibonacci",
		Payload:        []byte(`{"n": 5}`),
		MaxRetries:     2,
		TimeoutSeconds: 10,
	})
	if err != nil {
		t.Fatalf("SubmitTask: %v", err)
	}

	statusResp, err := client.GetTaskStatus(ctx, &forgepb.TaskStatusRequest{
		TaskId: submitResp.GetTaskId(),
	})
	if err != nil {
		t.Fatalf("GetTaskStatus: %v", err)
	}
	if statusResp.GetTaskId() != submitResp.GetTaskId() {
		t.Errorf("task_id mismatch: got %q, want %q", statusResp.GetTaskId(), submitResp.GetTaskId())
	}
	if statusResp.GetStatus() != "pending" {
		t.Errorf("got status %q, want %q", statusResp.GetStatus(), "pending")
	}
	if statusResp.GetRetryCount() != 0 {
		t.Errorf("got retry_count %d, want 0", statusResp.GetRetryCount())
	}
	if statusResp.GetCreatedAt() == 0 {
		t.Error("expected non-zero created_at")
	}
}

func TestGetTaskStatus_NotFound(t *testing.T) {
	client, _ := setupTestServer(t)
	ctx := context.Background()

	_, err := client.GetTaskStatus(ctx, &forgepb.TaskStatusRequest{
		TaskId: "nonexistent-task",
	})
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("got code %v, want NotFound", st.Code())
	}
}
