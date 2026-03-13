package scheduler

import (
	"context"
	"encoding/json"
	"time"

	hcraft "github.com/hashicorp/raft"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
	raftpkg "github.com/Ritpra93/forge/internal/raft"
)

// StartAssigner launches a background goroutine that matches pending tasks to
// available workers. Only operates when this node is the Raft leader. Exits
// when ctx is cancelled.
func (s *ForgeSchedulerServer) StartAssigner(ctx context.Context) {
	ticker := time.NewTicker(s.config.AssignerTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.assignPendingTasks()
		}
	}
}

func (s *ForgeSchedulerServer) assignPendingTasks() {
	if s.raft.State() != hcraft.Leader {
		return
	}

	pending := s.fsm.GetTasksByStatus("pending")
	if len(pending) == 0 {
		return
	}

	workers := s.GetConnectedWorkers()
	if len(workers) == 0 {
		return
	}

	// Build a mutable slots tracker to avoid over-assigning in one tick.
	type workerSlot struct {
		info  workerInfo
		slots int32
	}
	available := make([]workerSlot, 0, len(workers))
	for _, w := range workers {
		if w.availableSlots > 0 {
			available = append(available, workerSlot{info: w, slots: w.availableSlots})
		}
	}

	wi := 0 // round-robin index
	for _, task := range pending {
		if len(available) == 0 {
			break
		}

		// Find a worker with capacity that can handle this task type.
		assigned := false
		for attempts := 0; attempts < len(available); attempts++ {
			idx := (wi + attempts) % len(available)
			w := &available[idx]

			if w.slots <= 0 {
				continue
			}
			if !workerCanHandle(w.info, task.Type) {
				continue
			}

			if err := s.assignTask(task, w.info.workerID); err != nil {
				s.logger.Warn("assigning task",
					"task_id", task.ID,
					"worker_id", w.info.workerID,
					"error", err,
				)
				continue
			}

			w.slots--
			wi = (idx + 1) % len(available)
			assigned = true
			break
		}

		if !assigned {
			break // no worker can take more tasks this tick
		}
	}
}

// assignTask applies the assign_task Raft command and pushes the assignment
// to the worker's stream channel.
func (s *ForgeSchedulerServer) assignTask(task *raftpkg.Task, workerID string) error {
	payload, err := json.Marshal(raftpkg.AssignTaskPayload{WorkerID: workerID})
	if err != nil {
		return err
	}

	if err := s.applyCommand(raftpkg.Command{
		Type:    "assign_task",
		TaskID:  task.ID,
		Payload: payload,
	}); err != nil {
		return err
	}

	return s.AssignTaskToWorker(workerID, &forgepb.TaskAssignment{
		TaskId:         task.ID,
		Type:           task.Type,
		Payload:        task.Payload,
		TimeoutSeconds: int32(task.TimeoutSeconds),
	})
}

// workerCanHandle returns true if the worker's capabilities include the task
// type, or if the worker has no capability restrictions (empty list).
func workerCanHandle(w workerInfo, taskType string) bool {
	if len(w.capabilities) == 0 {
		return true
	}
	for _, cap := range w.capabilities {
		if cap == taskType {
			return true
		}
	}
	return false
}
