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

	mu      sync.RWMutex
	workers map[string]*workerInfo
}

// NewForgeSchedulerServer creates a new scheduler server backed by the given
// Raft node and FSM.
func NewForgeSchedulerServer(r *hcraft.Raft, fsm *raftpkg.TaskFSM, logger *slog.Logger) *ForgeSchedulerServer {
	return &ForgeSchedulerServer{
		raft:    r,
		fsm:     fsm,
		logger:  logger,
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
