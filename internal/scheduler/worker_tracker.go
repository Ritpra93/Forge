package scheduler

import (
	"context"
	"time"

	hcraft "github.com/hashicorp/raft"

	raftpkg "github.com/Ritpra93/forge/internal/raft"
)

// StartWorkerTracker launches a background goroutine that detects dead workers
// and reassigns their in-flight tasks. It also detects orphaned tasks whose
// assigned worker is unknown to this leader (e.g., after a leader failover).
// Only operates when this node is the Raft leader. Exits when ctx.Done() fires.
func (s *ForgeSchedulerServer) StartWorkerTracker(ctx context.Context) {
	ticker := time.NewTicker(s.config.TrackerTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.trackWorkers()
		}
	}
}

func (s *ForgeSchedulerServer) trackWorkers() {
	if s.raft.State() != hcraft.Leader {
		s.mu.Lock()
		s.leaderSince = time.Time{} // reset when not leader
		s.mu.Unlock()
		return
	}

	now := time.Now()

	s.mu.Lock()
	if s.leaderSince.IsZero() {
		s.leaderSince = now
		s.logger.Info("became leader, starting worker tracking")
	}
	leaderSince := s.leaderSince
	s.mu.Unlock()

	// Phase 1: detect dead workers by heartbeat timeout.
	s.mu.RLock()
	var deadWorkerIDs []string
	for id, w := range s.workers {
		if now.Sub(w.lastHeartbeat) > s.config.HeartbeatDead {
			deadWorkerIDs = append(deadWorkerIDs, id)
		}
	}
	s.mu.RUnlock()

	for _, workerID := range deadWorkerIDs {
		s.handleDeadWorker(workerID)
	}

	// Phase 2: detect orphaned tasks (running tasks assigned to unknown workers).
	// Only after a grace period so workers have time to reconnect after failover.
	if now.Sub(leaderSince) > s.config.HeartbeatDead {
		s.handleOrphanedTasks()
	}
}

func (s *ForgeSchedulerServer) handleDeadWorker(workerID string) {
	tasks := s.fsm.GetTasksByStatusAndWorker("running", workerID)
	for _, task := range tasks {
		if err := s.applyCommand(raftpkg.Command{
			Type:   "reassign_task",
			TaskID: task.ID,
		}); err != nil {
			s.logger.Warn("reassigning task from dead worker",
				"task_id", task.ID,
				"worker_id", workerID,
				"error", err,
			)
		}
	}

	s.mu.Lock()
	delete(s.workers, workerID)
	s.mu.Unlock()

	s.logger.Info("worker declared dead",
		"worker_id", workerID,
		"reassigned_tasks", len(tasks),
	)
}

func (s *ForgeSchedulerServer) handleOrphanedTasks() {
	allRunning := s.fsm.GetTasksByStatus("running")

	s.mu.RLock()
	var orphaned []*raftpkg.Task
	for _, task := range allRunning {
		if task.AssignedWorker != "" {
			if _, known := s.workers[task.AssignedWorker]; !known {
				orphaned = append(orphaned, task)
			}
		}
	}
	s.mu.RUnlock()

	for _, task := range orphaned {
		if err := s.applyCommand(raftpkg.Command{
			Type:   "reassign_task",
			TaskID: task.ID,
		}); err != nil {
			s.logger.Warn("reassigning orphaned task",
				"task_id", task.ID,
				"assigned_worker", task.AssignedWorker,
				"error", err,
			)
		}
	}

	if len(orphaned) > 0 {
		s.logger.Info("reassigned orphaned tasks", "count", len(orphaned))
	}
}
