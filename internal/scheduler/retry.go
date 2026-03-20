package scheduler

import (
	"context"
	"time"

	hcraft "github.com/hashicorp/raft"

	raftpkg "github.com/Ritpra93/forge/internal/raft"
)

const (
	baseBackoff = 1 * time.Second
	maxBackoff  = 60 * time.Second
)

// StartRetryScheduler launches a background goroutine that transitions
// "retrying" tasks back to "pending" after their exponential backoff expires.
// Only operates when this node is the Raft leader. Exits when ctx is cancelled.
func (s *ForgeSchedulerServer) StartRetryScheduler(ctx context.Context) {
	ticker := time.NewTicker(s.config.RetryTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processRetries()
		}
	}
}

func (s *ForgeSchedulerServer) processRetries() {
	if s.raft.State() != hcraft.Leader {
		return
	}

	retrying := s.fsm.GetTasksByStatus("retrying")
	now := time.Now()

	for _, task := range retrying {
		backoff := computeBackoff(task.RetryCount)
		updatedAt := time.Unix(0, task.UpdatedAt)

		if now.Sub(updatedAt) >= backoff {
			if err := s.applyCommand(raftpkg.Command{
				Type:   "reassign_task",
				TaskID: task.ID,
			}); err != nil {
				s.logger.Warn("retrying task",
					"task_id", task.ID,
					"retry_count", task.RetryCount,
					"error", err,
				)
			} else {
				s.logger.Info("task ready for retry",
					"task_id", task.ID,
					"retry_count", task.RetryCount,
					"backoff", backoff,
				)
			}
		}
	}
}

// computeBackoff returns the exponential backoff duration for a given retry count.
// Formula: min(maxBackoff, baseBackoff * 2^(retryCount-1))
func computeBackoff(retryCount int) time.Duration {
	if retryCount <= 0 {
		return baseBackoff
	}
	backoff := baseBackoff
	for i := 1; i < retryCount; i++ {
		backoff *= 2
		if backoff >= maxBackoff {
			return maxBackoff
		}
	}
	return backoff
}
