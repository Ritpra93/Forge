//go:build integration

package chaos

import (
	"context"
	"log/slog"
	"testing"
	"time"

	hcraft "github.com/hashicorp/raft"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
	"github.com/Ritpra93/forge/internal/worker"
	"github.com/Ritpra93/forge/internal/worker/handlers"
)

func TestLeaderFailover(t *testing.T) {
	const (
		totalTasks        = 500
		waitForCompleted  = 200
		workerCount       = 3
		maxSlots    int32 = 100
	)

	nodes := setupTestCluster(t, 3)
	leader := findLeader(t, nodes)

	// Start workers connected to the leader.
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()

	for i := 0; i < workerCount; i++ {
		w := worker.NewWorkerWithDialOpts(
			nodeIDStr(100+i),
			maxSlots,
			slog.Default(),
			dialOptsForBufconn(t, leader.bufLis),
			&handlers.SleepHandler{},
		)
		go w.RunWithConn(workerCtx)
	}

	// Wait for workers to register.
	pollUntil(t, 5*time.Second, "workers to register", func() bool {
		return len(leader.server.GetConnectedWorkers()) >= workerCount
	})

	// Submit 500 tasks.
	client := dialBufconn(t, leader.bufLis)
	for i := 0; i < totalTasks; i++ {
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

	// Wait for at least 200 tasks to complete.
	pollUntil(t, 30*time.Second, "200 tasks to complete", func() bool {
		return countTasksByStatus(leader.fsm, "completed") >= waitForCompleted
	})

	completedBeforeKill := countTasksByStatus(leader.fsm, "completed")
	t.Logf("completed %d tasks before killing leader", completedBeforeKill)

	// Kill the leader.
	cancelWorkers()
	leader.grpcServer.Stop()
	leader.raft.Shutdown()

	// Wait for new leader among surviving nodes.
	var surviving []*testClusterNode
	var survivingRafts []*hcraft.Raft
	for _, n := range nodes {
		if n != leader {
			surviving = append(surviving, n)
			survivingRafts = append(survivingRafts, n.raft)
		}
	}

	newLeader := waitForLeader(t, survivingRafts, 5*time.Second)
	t.Log("new leader elected")

	// Find the new leader node.
	var newLeaderNode *testClusterNode
	for _, n := range surviving {
		if n.raft == newLeader {
			newLeaderNode = n
			break
		}
	}

	// Reconnect workers to the new leader.
	workerCtx2, cancelWorkers2 := context.WithCancel(context.Background())
	defer cancelWorkers2()

	for i := 0; i < workerCount; i++ {
		w := worker.NewWorkerWithDialOpts(
			nodeIDStr(200+i),
			maxSlots,
			slog.Default(),
			dialOptsForBufconn(t, newLeaderNode.bufLis),
			&handlers.SleepHandler{},
		)
		go w.RunWithConn(workerCtx2)
	}

	// Wait for workers to register on new leader.
	pollUntil(t, 5*time.Second, "workers to register on new leader", func() bool {
		return len(newLeaderNode.server.GetConnectedWorkers()) >= workerCount
	})

	// Wait for all tasks to reach terminal state.
	pollUntil(t, 60*time.Second, "all tasks to complete", func() bool {
		terminal := countTasksByStatus(newLeaderNode.fsm, "completed", "dead_letter")
		return terminal >= totalTasks
	})

	completed := countTasksByStatus(newLeaderNode.fsm, "completed")
	deadLettered := countTasksByStatus(newLeaderNode.fsm, "dead_letter")
	t.Logf("final: completed=%d, dead_letter=%d, total=%d", completed, deadLettered, completed+deadLettered)

	if completed+deadLettered != totalTasks {
		t.Errorf("expected %d terminal tasks, got %d (completed=%d, dead_letter=%d)",
			totalTasks, completed+deadLettered, completed, deadLettered)
	}
}
