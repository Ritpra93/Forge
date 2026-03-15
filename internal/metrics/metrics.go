package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Scheduler metrics.
var (
	TasksSubmitted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "forge_tasks_submitted_total",
		Help: "Total number of tasks submitted.",
	})
	TasksCompleted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "forge_tasks_completed_total",
		Help: "Total number of tasks completed successfully.",
	})
	TasksFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "forge_tasks_failed_total",
		Help: "Total number of tasks that failed (after all retries).",
	})
	TasksDeadLettered = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "forge_tasks_dead_lettered_total",
		Help: "Total number of tasks moved to dead letter.",
	})
	TaskDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "forge_task_duration_seconds",
		Help:    "Time from task submission to completion.",
		Buckets: prometheus.DefBuckets,
	})
	ActiveWorkers = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "forge_active_workers",
		Help: "Number of currently connected workers.",
	})
	PendingQueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "forge_pending_queue_depth",
		Help: "Number of tasks waiting to be assigned.",
	})
	RaftIsLeader = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "forge_raft_is_leader",
		Help: "1 if this node is leader, 0 otherwise.",
	})
	RaftCommitIndex = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "forge_raft_commit_index",
		Help: "Latest committed Raft log index.",
	})
	RaftElectionTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "forge_raft_election_total",
		Help: "Number of leader elections observed.",
	})
	WorkerHeartbeatLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "forge_worker_heartbeat_latency_seconds",
		Help:    "Round-trip heartbeat time.",
		Buckets: prometheus.DefBuckets,
	})
)

// Worker metrics.
var (
	WorkerTasksExecuted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "forge_worker_tasks_executed_total",
		Help: "Tasks this worker has executed.",
	})
	WorkerTaskExecution = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "forge_worker_task_execution_seconds",
		Help:    "Per-task execution time.",
		Buckets: prometheus.DefBuckets,
	})
	WorkerAvailableSlots = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "forge_worker_available_slots",
		Help: "Remaining task capacity.",
	})
)

var registerOnce sync.Once

// Register registers all Forge metrics with the default Prometheus registry.
// Safe to call multiple times; only the first call takes effect.
func Register() {
	registerOnce.Do(func() {
		prometheus.MustRegister(
			TasksSubmitted,
			TasksCompleted,
			TasksFailed,
			TasksDeadLettered,
			TaskDuration,
			ActiveWorkers,
			PendingQueueDepth,
			RaftIsLeader,
			RaftCommitIndex,
			RaftElectionTotal,
			WorkerHeartbeatLatency,
			WorkerTasksExecuted,
			WorkerTaskExecution,
			WorkerAvailableSlots,
		)
	})
}
