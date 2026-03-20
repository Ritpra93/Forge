//go:build integration

package chaos

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
	"github.com/Ritpra93/forge/internal/worker"
	"github.com/Ritpra93/forge/internal/worker/handlers"
)

func TestWorkerCrashMidTask(t *testing.T) {
	const taskCount = 5

	nodes := setupTestCluster(t, 1)
	leader := findLeader(t, nodes)

	// Connect a worker with a long-sleep handler so tasks stay "running".
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()

	w := worker.NewWorkerWithDialOpts(
		"crash-worker",
		10,
		slog.Default(),
		dialOptsForBufconn(t, leader.bufLis),
		&handlers.SleepHandler{},
	)
	go w.RunWithConn(workerCtx)

	// Wait for worker to register.
	pollUntil(t, 5*time.Second, "worker to register", func() bool {
		return len(leader.server.GetConnectedWorkers()) >= 1
	})

	// Submit tasks with a 30s sleep so they stay "running".
	client := dialBufconn(t, leader.bufLis)
	for i := 0; i < taskCount; i++ {
		_, err := client.SubmitTask(context.Background(), &forgepb.TaskRequest{
			Type:           "sleep",
			Payload:        []byte(`{"seconds": 300}`),
			MaxRetries:     3,
			TimeoutSeconds: 600,
		})
		if err != nil {
			t.Fatalf("submitting task %d: %v", i, err)
		}
	}

	// Wait for all tasks to be assigned and running.
	pollUntil(t, 10*time.Second, "all tasks to be running", func() bool {
		return countTasksByStatus(leader.fsm, "running") >= taskCount
	})

	t.Logf("all %d tasks are running, crashing worker", taskCount)

	// Crash the worker by cancelling its context.
	cancelWorker()

	// The RegisterWorker deferred cleanup removes the worker from the map
	// immediately. On the next tracker tick, orphan detection finds the
	// running tasks with an unknown assigned worker and reassigns them.
	pollUntil(t, 15*time.Second, "tasks to be reassigned to pending", func() bool {
		return countTasksByStatus(leader.fsm, "pending") >= taskCount
	})

	pending := countTasksByStatus(leader.fsm, "pending")
	t.Logf("after worker crash: %d tasks returned to pending", pending)

	if pending < taskCount {
		t.Errorf("expected at least %d pending tasks, got %d", taskCount, pending)
	}
}
