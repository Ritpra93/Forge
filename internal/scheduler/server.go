package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	hcraft "github.com/hashicorp/raft"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
	raftpkg "github.com/Ritpra93/forge/internal/raft"
)

const defaultApplyTimeout = 5 * time.Second

// TrackerConfig holds configurable intervals for background goroutines.
// Zero values fall back to production defaults.
type TrackerConfig struct {
	TrackerTick   time.Duration // dead worker check interval (default 3s)
	HeartbeatDead time.Duration // time after which a worker is considered dead (default 9s)
	RetryTick     time.Duration // retry scheduler check interval (default 1s)
	AssignerTick  time.Duration // task assigner check interval (default 500ms)
}

// DefaultTrackerConfig returns production-default intervals.
func DefaultTrackerConfig() TrackerConfig {
	return TrackerConfig{
		TrackerTick:   3 * time.Second,
		HeartbeatDead: 9 * time.Second,
		RetryTick:     1 * time.Second,
		AssignerTick:  500 * time.Millisecond,
	}
}

// workerInfo tracks the state of a connected worker.
type workerInfo struct {
	workerID       string
	capabilities   []string
	availableSlots int32
	lastHeartbeat  time.Time
	assignCh       chan *forgepb.TaskAssignment
}

// ForgeSchedulerServer implements the forgepb.ForgeSchedulerServer interface.
// It bridges gRPC requests to the Raft consensus layer for task management.
type ForgeSchedulerServer struct {
	forgepb.UnimplementedForgeSchedulerServer

	raft   *hcraft.Raft
	fsm    *raftpkg.TaskFSM
	logger *slog.Logger
	config TrackerConfig

	mu          sync.RWMutex
	workers     map[string]*workerInfo
	leaderSince time.Time // when this node first observed itself as leader
}

// NewForgeSchedulerServer creates a new scheduler server backed by the given
// Raft node and FSM.
func NewForgeSchedulerServer(r *hcraft.Raft, fsm *raftpkg.TaskFSM, logger *slog.Logger) *ForgeSchedulerServer {
	return NewForgeSchedulerServerWithConfig(r, fsm, logger, DefaultTrackerConfig())
}

// NewForgeSchedulerServerWithConfig creates a scheduler server with custom
// tracker configuration. Useful for tests that need faster tick intervals.
func NewForgeSchedulerServerWithConfig(r *hcraft.Raft, fsm *raftpkg.TaskFSM, logger *slog.Logger, config TrackerConfig) *ForgeSchedulerServer {
	return &ForgeSchedulerServer{
		raft:    r,
		fsm:     fsm,
		logger:  logger,
		config:  config,
		workers: make(map[string]*workerInfo),
	}
}

// newTaskID generates a UUID v4 task identifier.
func newTaskID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("reading crypto/rand: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("task-%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// requireLeader returns a gRPC error if this node is not the Raft leader.
func (s *ForgeSchedulerServer) requireLeader() error {
	if s.raft.State() != hcraft.Leader {
		leaderAddr, _ := s.raft.LeaderWithID()
		return status.Errorf(codes.FailedPrecondition,
			"not the leader; current leader is %s", leaderAddr)
	}
	return nil
}

// applyCommand marshals the command and applies it through the Raft log.
func (s *ForgeSchedulerServer) applyCommand(cmd raftpkg.Command) error {
	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("marshaling command: %w", err)
	}

	future := s.raft.Apply(data, defaultApplyTimeout)
	if err := future.Error(); err != nil {
		return fmt.Errorf("applying raft command: %w", err)
	}

	if resp := future.Response(); resp != nil {
		if err, ok := resp.(error); ok {
			return err
		}
	}
	return nil
}

// SubmitTask creates a new task via Raft consensus. Leader-only.
func (s *ForgeSchedulerServer) SubmitTask(ctx context.Context, req *forgepb.TaskRequest) (*forgepb.TaskResponse, error) {
	if err := s.requireLeader(); err != nil {
		return nil, err
	}

	taskID := newTaskID()

	task := raftpkg.Task{
		ID:             taskID,
		Type:           req.GetType(),
		Payload:        req.GetPayload(),
		MaxRetries:     int(req.GetMaxRetries()),
		TimeoutSeconds: int(req.GetTimeoutSeconds()),
	}

	payload, err := json.Marshal(task)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshaling task: %v", err)
	}

	cmd := raftpkg.Command{
		Type:    "create_task",
		TaskID:  taskID,
		Payload: payload,
	}

	if err := s.applyCommand(cmd); err != nil {
		return nil, status.Errorf(codes.Internal, "creating task: %v", err)
	}

	s.logger.Info("task submitted", "task_id", taskID, "type", req.GetType())

	return &forgepb.TaskResponse{
		TaskId: taskID,
		Status: "pending",
	}, nil
}

// GetTaskStatus reads task state directly from the FSM. Any node can serve this.
func (s *ForgeSchedulerServer) GetTaskStatus(ctx context.Context, req *forgepb.TaskStatusRequest) (*forgepb.TaskStatusResponse, error) {
	task := s.fsm.GetTask(req.GetTaskId())
	if task == nil {
		return nil, status.Errorf(codes.NotFound, "task %s not found", req.GetTaskId())
	}

	return &forgepb.TaskStatusResponse{
		TaskId:         task.ID,
		Status:         task.Status,
		RetryCount:     int32(task.RetryCount),
		AssignedWorker: task.AssignedWorker,
		CreatedAt:      task.CreatedAt,
		UpdatedAt:      task.UpdatedAt,
	}, nil
}

// RegisterWorker handles bidirectional streaming for worker heartbeats and
// task assignments. A recv goroutine processes incoming heartbeats while the
// main loop sends task assignments from the worker's channel.
func (s *ForgeSchedulerServer) RegisterWorker(stream grpc.BidiStreamingServer[forgepb.WorkerHeartbeat, forgepb.TaskAssignment]) error {
	firstHB, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.Internal, "receiving initial heartbeat: %v", err)
	}

	workerID := firstHB.GetWorkerId()
	assignCh := make(chan *forgepb.TaskAssignment, 10)

	s.mu.Lock()
	s.workers[workerID] = &workerInfo{
		workerID:       workerID,
		capabilities:   firstHB.GetCapabilities(),
		availableSlots: firstHB.GetAvailableSlots(),
		lastHeartbeat:  time.Now(),
		assignCh:       assignCh,
	}
	s.mu.Unlock()

	s.logger.Info("worker registered", "worker_id", workerID)

	defer func() {
		s.mu.Lock()
		delete(s.workers, workerID)
		s.mu.Unlock()
		close(assignCh)
		s.logger.Info("worker disconnected", "worker_id", workerID)
	}()

	errCh := make(chan error, 1)
	go func() {
		for {
			hb, err := stream.Recv()
			if err != nil {
				errCh <- err
				return
			}
			s.mu.Lock()
			if w, ok := s.workers[workerID]; ok {
				w.capabilities = hb.GetCapabilities()
				w.availableSlots = hb.GetAvailableSlots()
				w.lastHeartbeat = time.Now()
			}
			s.mu.Unlock()
		}
	}()

	for {
		select {
		case assignment := <-assignCh:
			if err := stream.Send(assignment); err != nil {
				return status.Errorf(codes.Internal, "sending assignment: %v", err)
			}
		case err := <-errCh:
			s.logger.Info("worker stream ended", "worker_id", workerID, "error", err)
			return nil
		}
	}
}

// ReportTaskResult applies a complete_task or fail_task command based on the
// result. Leader-only.
func (s *ForgeSchedulerServer) ReportTaskResult(ctx context.Context, req *forgepb.TaskResult) (*forgepb.TaskResultAck, error) {
	if err := s.requireLeader(); err != nil {
		return nil, err
	}

	var cmd raftpkg.Command
	if req.GetSuccess() {
		cmd = raftpkg.Command{
			Type:   "complete_task",
			TaskID: req.GetTaskId(),
		}
	} else {
		cmd = raftpkg.Command{
			Type:   "fail_task",
			TaskID: req.GetTaskId(),
		}
	}

	if err := s.applyCommand(cmd); err != nil {
		return nil, status.Errorf(codes.Internal, "reporting result: %v", err)
	}

	s.logger.Info("task result reported",
		"task_id", req.GetTaskId(),
		"worker_id", req.GetWorkerId(),
		"success", req.GetSuccess(),
	)

	return &forgepb.TaskResultAck{Acknowledged: true}, nil
}

// AssignTaskToWorker pushes a task assignment to a connected worker's channel.
func (s *ForgeSchedulerServer) AssignTaskToWorker(workerID string, assignment *forgepb.TaskAssignment) error {
	s.mu.RLock()
	w, ok := s.workers[workerID]
	s.mu.RUnlock()

	if !ok {
		return fmt.Errorf("worker %s not connected", workerID)
	}

	select {
	case w.assignCh <- assignment:
		return nil
	default:
		return fmt.Errorf("worker %s assignment channel full", workerID)
	}
}

// GetConnectedWorkers returns a snapshot of all currently connected workers.
func (s *ForgeSchedulerServer) GetConnectedWorkers() []workerInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]workerInfo, 0, len(s.workers))
	for _, w := range s.workers {
		result = append(result, *w)
	}
	return result
}

// WatchTask streams task status updates until the task reaches a terminal state.
func (s *ForgeSchedulerServer) WatchTask(req *forgepb.WatchTaskRequest, stream grpc.ServerStreamingServer[forgepb.TaskStatusResponse]) error {
	taskID := req.GetTaskId()
	interval := time.Duration(req.GetPollIntervalMs()) * time.Millisecond
	if interval <= 0 {
		interval = 1 * time.Second
	}

	var lastStatus string
	for {
		task := s.fsm.GetTask(taskID)
		if task == nil {
			return status.Errorf(codes.NotFound, "task %s not found", taskID)
		}

		if task.Status != lastStatus {
			lastStatus = task.Status
			if err := stream.Send(&forgepb.TaskStatusResponse{
				TaskId:         task.ID,
				Status:         task.Status,
				RetryCount:     int32(task.RetryCount),
				AssignedWorker: task.AssignedWorker,
				CreatedAt:      task.CreatedAt,
				UpdatedAt:      task.UpdatedAt,
			}); err != nil {
				return err
			}

			if isTerminal(task.Status) {
				return nil
			}
		}

		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-time.After(interval):
		}
	}
}

// isTerminal returns true for task states that will not change further.
func isTerminal(s string) bool {
	return s == "completed" || s == "dead_letter"
}

// GetClusterInfo returns the current Raft cluster state.
func (s *ForgeSchedulerServer) GetClusterInfo(_ context.Context, _ *forgepb.ClusterInfoRequest) (*forgepb.ClusterInfoResponse, error) {
	leaderAddr, leaderID := s.raft.LeaderWithID()

	configFuture := s.raft.GetConfiguration()
	if err := configFuture.Error(); err != nil {
		return nil, status.Errorf(codes.Internal, "getting cluster configuration: %v", err)
	}

	var nodes []*forgepb.NodeInfo
	for _, server := range configFuture.Configuration().Servers {
		nodes = append(nodes, &forgepb.NodeInfo{
			Id:      string(server.ID),
			Address: string(server.Address),
			State:   s.nodeState(server.ID),
		})
	}

	return &forgepb.ClusterInfoResponse{
		LeaderAddress: string(leaderAddr),
		LeaderId:      string(leaderID),
		Nodes:         nodes,
	}, nil
}

// nodeState returns a human-readable state for a node in the cluster.
func (s *ForgeSchedulerServer) nodeState(id hcraft.ServerID) string {
	_, leaderID := s.raft.LeaderWithID()
	if id == leaderID {
		return "leader"
	}
	return "follower"
}

// ListTasks returns tasks, optionally filtered by status.
func (s *ForgeSchedulerServer) ListTasks(_ context.Context, req *forgepb.ListTasksRequest) (*forgepb.ListTasksResponse, error) {
	var tasks []*raftpkg.Task

	if filter := req.GetStatusFilter(); filter != "" {
		tasks = s.fsm.GetTasksByStatus(filter)
	} else {
		allTasks := s.fsm.GetAllTasks()
		tasks = make([]*raftpkg.Task, 0, len(allTasks))
		for _, t := range allTasks {
			tasks = append(tasks, t)
		}
	}

	resp := &forgepb.ListTasksResponse{}
	for _, t := range tasks {
		resp.Tasks = append(resp.Tasks, &forgepb.TaskStatusResponse{
			TaskId:         t.ID,
			Status:         t.Status,
			RetryCount:     int32(t.RetryCount),
			AssignedWorker: t.AssignedWorker,
			CreatedAt:      t.CreatedAt,
			UpdatedAt:      t.UpdatedAt,
		})
	}

	return resp, nil
}
