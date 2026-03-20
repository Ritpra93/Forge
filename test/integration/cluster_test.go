//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
	"github.com/Ritpra93/forge/internal/scheduler"
	"github.com/Ritpra93/forge/internal/worker"
	"github.com/Ritpra93/forge/internal/worker/handlers"
)

// TestTaskLifecycle verifies the end-to-end happy path: submit tasks,
// worker executes them, all reach "completed" status.
func TestTaskLifecycle(t *testing.T) {
	const taskCount = 10

	nodes := setupTestCluster(t, 1)
	leader := findLeader(t, nodes)

	// Start a worker with SleepHandler (0s sleep = instant completion).
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()

	w := worker.NewWorkerWithDialOpts(
		"test-worker-1",
		10,
		slog.Default(),
		dialOptsForBufconn(t, leader.bufLis),
		&handlers.SleepHandler{},
	)
	go w.RunWithConn(workerCtx)

	pollUntil(t, 5*time.Second, "worker to register", func() bool {
		return len(leader.server.GetConnectedWorkers()) >= 1
	})

	// Submit tasks.
	client := dialBufconn(t, leader.bufLis)
	taskIDs := make([]string, taskCount)
	for i := 0; i < taskCount; i++ {
		resp, err := client.SubmitTask(context.Background(), &forgepb.TaskRequest{
			Type:           "sleep",
			Payload:        []byte(`{"seconds": 0}`),
			MaxRetries:     3,
			TimeoutSeconds: 10,
		})
		if err != nil {
			t.Fatalf("submitting task %d: %v", i, err)
		}
		taskIDs[i] = resp.GetTaskId()
	}

	// Wait for all tasks to complete.
	pollUntil(t, 30*time.Second, "all tasks to complete", func() bool {
		return countTasksByStatus(leader.fsm, "completed") >= taskCount
	})

	// Verify each task individually.
	for _, id := range taskIDs {
		resp, err := client.GetTaskStatus(context.Background(), &forgepb.TaskStatusRequest{TaskId: id})
		if err != nil {
			t.Fatalf("getting status for task %s: %v", id, err)
		}
		if resp.GetStatus() != "completed" {
			t.Errorf("task %s: expected status completed, got %s", id, resp.GetStatus())
		}
	}

	// Verify no tasks stuck in non-terminal states.
	for _, s := range []string{"pending", "running", "retrying", "failed"} {
		if n := countTasksByStatus(leader.fsm, s); n != 0 {
			t.Errorf("expected 0 tasks in %s, got %d", s, n)
		}
	}
}

// TestMultipleTaskTypes verifies that a worker with multiple handlers can
// process different task types in the same run.
func TestMultipleTaskTypes(t *testing.T) {
	nodes := setupTestCluster(t, 1)
	leader := findLeader(t, nodes)

	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()

	w := worker.NewWorkerWithDialOpts(
		"multi-handler-worker",
		10,
		slog.Default(),
		dialOptsForBufconn(t, leader.bufLis),
		&handlers.SleepHandler{},
		&handlers.FibonacciHandler{},
	)
	go w.RunWithConn(workerCtx)

	pollUntil(t, 5*time.Second, "worker to register", func() bool {
		return len(leader.server.GetConnectedWorkers()) >= 1
	})

	client := dialBufconn(t, leader.bufLis)

	// Submit 5 sleep tasks.
	for i := 0; i < 5; i++ {
		_, err := client.SubmitTask(context.Background(), &forgepb.TaskRequest{
			Type:           "sleep",
			Payload:        []byte(`{"seconds": 0}`),
			MaxRetries:     3,
			TimeoutSeconds: 10,
		})
		if err != nil {
			t.Fatalf("submitting sleep task %d: %v", i, err)
		}
	}

	// Submit 5 fibonacci tasks.
	for i := 0; i < 5; i++ {
		_, err := client.SubmitTask(context.Background(), &forgepb.TaskRequest{
			Type:           "fibonacci",
			Payload:        []byte(`{"n": 10}`),
			MaxRetries:     3,
			TimeoutSeconds: 10,
		})
		if err != nil {
			t.Fatalf("submitting fibonacci task %d: %v", i, err)
		}
	}

	pollUntil(t, 30*time.Second, "all 10 tasks to complete", func() bool {
		return countTasksByStatus(leader.fsm, "completed") >= 10
	})
}

// TestFollowerReadsLeaderWrites verifies that tasks written via the leader
// are readable from a follower node after Raft replication.
func TestFollowerReadsLeaderWrites(t *testing.T) {
	nodes := setupTestCluster(t, 3)
	leader := findLeader(t, nodes)
	follower := findFollower(t, nodes)

	// Start a worker connected to the leader.
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()

	w := worker.NewWorkerWithDialOpts(
		"replication-worker",
		10,
		slog.Default(),
		dialOptsForBufconn(t, leader.bufLis),
		&handlers.SleepHandler{},
	)
	go w.RunWithConn(workerCtx)

	pollUntil(t, 5*time.Second, "worker to register", func() bool {
		return len(leader.server.GetConnectedWorkers()) >= 1
	})

	// Submit tasks via leader.
	leaderClient := dialBufconn(t, leader.bufLis)
	taskIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		resp, err := leaderClient.SubmitTask(context.Background(), &forgepb.TaskRequest{
			Type:           "sleep",
			Payload:        []byte(`{"seconds": 0}`),
			MaxRetries:     3,
			TimeoutSeconds: 10,
		})
		if err != nil {
			t.Fatalf("submitting task %d: %v", i, err)
		}
		taskIDs[i] = resp.GetTaskId()
	}

	// Wait for tasks to complete on leader.
	pollUntil(t, 30*time.Second, "all tasks to complete", func() bool {
		return countTasksByStatus(leader.fsm, "completed") >= 5
	})

	// Query each task from the follower — Raft replication may need a moment.
	followerClient := dialBufconn(t, follower.bufLis)
	for _, id := range taskIDs {
		taskID := id
		pollUntil(t, 5*time.Second, "follower to replicate task "+taskID, func() bool {
			resp, err := followerClient.GetTaskStatus(context.Background(), &forgepb.TaskStatusRequest{TaskId: taskID})
			if err != nil {
				return false
			}
			return resp.GetStatus() == "completed"
		})
	}
}

// TestTaskRetryAndDeadLetter verifies the retry and dead-letter flow.
// A sleep task with an invalid payload (negative seconds) fails on each
// attempt until max retries is exceeded and it moves to dead_letter.
func TestTaskRetryAndDeadLetter(t *testing.T) {
	// Use a custom cluster with a longer heartbeat deadline so the worker
	// stays alive through the retry cycle (worker sends heartbeats every 3s,
	// so HeartbeatDead must be > 3s).
	nodes := setupTestClusterWithConfig(t, 1, scheduler.TrackerConfig{
		TrackerTick:   100 * time.Millisecond,
		HeartbeatDead: 5 * time.Second,
		RetryTick:     100 * time.Millisecond,
		AssignerTick:  100 * time.Millisecond,
	})
	leader := findLeader(t, nodes)

	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()

	w := worker.NewWorkerWithDialOpts(
		"retry-worker",
		10,
		slog.Default(),
		dialOptsForBufconn(t, leader.bufLis),
		&handlers.SleepHandler{},
	)
	go w.RunWithConn(workerCtx)

	pollUntil(t, 5*time.Second, "worker to register", func() bool {
		return len(leader.server.GetConnectedWorkers()) >= 1
	})

	// Submit a sleep task with negative seconds — SleepHandler will return an error.
	client := dialBufconn(t, leader.bufLis)
	resp, err := client.SubmitTask(context.Background(), &forgepb.TaskRequest{
		Type:           "sleep",
		Payload:        []byte(`{"seconds": -1}`),
		MaxRetries:     2,
		TimeoutSeconds: 10,
	})
	if err != nil {
		t.Fatalf("submitting task: %v", err)
	}

	taskID := resp.GetTaskId()

	// Wait for the task to reach dead_letter (after 2 failed retries).
	pollUntil(t, 60*time.Second, "task to reach dead_letter", func() bool {
		return countTasksByStatus(leader.fsm, "dead_letter") >= 1
	})

	statusResp, err := client.GetTaskStatus(context.Background(), &forgepb.TaskStatusRequest{TaskId: taskID})
	if err != nil {
		t.Fatalf("getting task status: %v", err)
	}
	if statusResp.GetStatus() != "dead_letter" {
		t.Errorf("expected dead_letter, got %s", statusResp.GetStatus())
	}
	if statusResp.GetRetryCount() != 2 {
		t.Errorf("expected retry_count=2, got %d", statusResp.GetRetryCount())
	}
}

// TestFollowerRejectsWrite verifies that a follower returns a
// FailedPrecondition error when a client tries to submit a task.
func TestFollowerRejectsWrite(t *testing.T) {
	nodes := setupTestCluster(t, 3)
	follower := findFollower(t, nodes)

	followerClient := dialBufconn(t, follower.bufLis)
	_, err := followerClient.SubmitTask(context.Background(), &forgepb.TaskRequest{
		Type:           "sleep",
		Payload:        []byte(`{"seconds": 0}`),
		MaxRetries:     1,
		TimeoutSeconds: 5,
	})
	if err == nil {
		t.Fatal("expected error when submitting to follower, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("expected FailedPrecondition, got %s", st.Code())
	}
	if st.Message() == "" {
		t.Error("expected error message to contain leader info")
	}
}

// TestClusterInfoRPC verifies the GetClusterInfo RPC returns correct
// cluster topology.
func TestClusterInfoRPC(t *testing.T) {
	nodes := setupTestCluster(t, 3)
	leader := findLeader(t, nodes)

	client := dialBufconn(t, leader.bufLis)
	resp, err := client.GetClusterInfo(context.Background(), &forgepb.ClusterInfoRequest{})
	if err != nil {
		t.Fatalf("GetClusterInfo: %v", err)
	}

	if len(resp.GetNodes()) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(resp.GetNodes()))
	}

	if resp.GetLeaderId() == "" {
		t.Error("expected non-empty leader_id")
	}

	leaderCount := 0
	for _, n := range resp.GetNodes() {
		if n.GetState() == "leader" {
			leaderCount++
		}
	}
	if leaderCount != 1 {
		t.Errorf("expected exactly 1 leader node, got %d", leaderCount)
	}
}

// TestListTasksFiltering verifies the ListTasks RPC with and without
// status filtering.
func TestListTasksFiltering(t *testing.T) {
	const taskCount = 5

	nodes := setupTestCluster(t, 1)
	leader := findLeader(t, nodes)

	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()

	w := worker.NewWorkerWithDialOpts(
		"list-worker",
		10,
		slog.Default(),
		dialOptsForBufconn(t, leader.bufLis),
		&handlers.SleepHandler{},
	)
	go w.RunWithConn(workerCtx)

	pollUntil(t, 5*time.Second, "worker to register", func() bool {
		return len(leader.server.GetConnectedWorkers()) >= 1
	})

	client := dialBufconn(t, leader.bufLis)
	for i := 0; i < taskCount; i++ {
		_, err := client.SubmitTask(context.Background(), &forgepb.TaskRequest{
			Type:           "sleep",
			Payload:        []byte(`{"seconds": 0}`),
			MaxRetries:     3,
			TimeoutSeconds: 10,
		})
		if err != nil {
			t.Fatalf("submitting task %d: %v", i, err)
		}
	}

	pollUntil(t, 30*time.Second, "all tasks to complete", func() bool {
		return countTasksByStatus(leader.fsm, "completed") >= taskCount
	})

	// List all tasks (no filter).
	allResp, err := client.ListTasks(context.Background(), &forgepb.ListTasksRequest{})
	if err != nil {
		t.Fatalf("ListTasks (no filter): %v", err)
	}
	if len(allResp.GetTasks()) != taskCount {
		t.Errorf("expected %d tasks, got %d", taskCount, len(allResp.GetTasks()))
	}

	// List completed tasks.
	completedResp, err := client.ListTasks(context.Background(), &forgepb.ListTasksRequest{
		StatusFilter: "completed",
	})
	if err != nil {
		t.Fatalf("ListTasks (completed): %v", err)
	}
	if len(completedResp.GetTasks()) != taskCount {
		t.Errorf("expected %d completed tasks, got %d", taskCount, len(completedResp.GetTasks()))
	}

	// List pending tasks — should be 0.
	pendingResp, err := client.ListTasks(context.Background(), &forgepb.ListTasksRequest{
		StatusFilter: "pending",
	})
	if err != nil {
		t.Fatalf("ListTasks (pending): %v", err)
	}
	if len(pendingResp.GetTasks()) != 0 {
		t.Errorf("expected 0 pending tasks, got %d", len(pendingResp.GetTasks()))
	}
}
