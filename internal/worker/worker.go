package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
	"github.com/Ritpra93/forge/internal/worker/handlers"
)

const heartbeatInterval = 3 * time.Second

// Worker connects to the scheduler, receives task assignments, executes them
// via pluggable handlers, and reports results.
type Worker struct {
	id            string
	schedulerAddr string
	handlers      map[string]handlers.TaskHandler
	maxSlots      int32
	activeSlots   atomic.Int32
	logger        *slog.Logger
	dialOpts      []grpc.DialOption // custom dial options (for testing with bufconn)

	conn   *grpc.ClientConn
	client forgepb.ForgeSchedulerClient
}

// NewWorker creates a new worker with the given configuration.
func NewWorker(id, schedulerAddr string, maxSlots int32, logger *slog.Logger, taskHandlers ...handlers.TaskHandler) *Worker {
	return &Worker{
		id:            id,
		schedulerAddr: schedulerAddr,
		handlers:      handlers.NewRegistry(taskHandlers...),
		maxSlots:      maxSlots,
		logger:        logger,
	}
}

// NewWorkerWithDialOpts creates a worker with custom gRPC dial options.
// This is used in tests to connect via bufconn instead of a real network address.
func NewWorkerWithDialOpts(id string, maxSlots int32, logger *slog.Logger, dialOpts []grpc.DialOption, taskHandlers ...handlers.TaskHandler) *Worker {
	return &Worker{
		id:            id,
		schedulerAddr: "passthrough:///bufnet",
		handlers:      handlers.NewRegistry(taskHandlers...),
		maxSlots:      maxSlots,
		logger:        logger,
		dialOpts:      dialOpts,
	}
}

// capabilities returns the list of task types this worker can handle.
func (w *Worker) capabilities() []string {
	caps := make([]string, 0, len(w.handlers))
	for typ := range w.handlers {
		caps = append(caps, typ)
	}
	return caps
}

// availableSlots returns the number of free task slots.
func (w *Worker) availableSlots() int32 {
	active := w.activeSlots.Load()
	avail := w.maxSlots - active
	if avail < 0 {
		return 0
	}
	return avail
}

// Run connects to the scheduler and processes task assignments until ctx is
// cancelled. It blocks until the context is done or an unrecoverable error occurs.
func (w *Worker) Run(ctx context.Context) error {
	opts := w.dialOpts
	if opts == nil {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}

	conn, err := grpc.NewClient(w.schedulerAddr, opts...)
	if err != nil {
		return fmt.Errorf("connecting to scheduler: %w", err)
	}
	w.conn = conn
	defer conn.Close()

	return w.runStream(ctx)
}

// RunWithConn runs the worker using a pre-established connection. Used in tests.
func (w *Worker) RunWithConn(ctx context.Context) error {
	opts := w.dialOpts
	if opts == nil {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}

	conn, err := grpc.NewClient(w.schedulerAddr, opts...)
	if err != nil {
		return fmt.Errorf("connecting to scheduler: %w", err)
	}
	w.conn = conn
	defer conn.Close()

	return w.runStream(ctx)
}

func (w *Worker) runStream(ctx context.Context) error {
	w.client = forgepb.NewForgeSchedulerClient(w.conn)

	stream, err := w.client.RegisterWorker(ctx)
	if err != nil {
		return fmt.Errorf("opening RegisterWorker stream: %w", err)
	}

	// Send the initial heartbeat to register with the scheduler.
	if err := stream.Send(&forgepb.WorkerHeartbeat{
		WorkerId:       w.id,
		Capabilities:   w.capabilities(),
		AvailableSlots: w.availableSlots(),
	}); err != nil {
		return fmt.Errorf("sending initial heartbeat: %w", err)
	}

	w.logger.Info("registered with scheduler",
		"worker_id", w.id,
		"scheduler", w.schedulerAddr,
	)

	// Heartbeat goroutine.
	go w.heartbeatLoop(ctx, stream)

	// Receive task assignments until stream closes or ctx is cancelled.
	for {
		assignment, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return fmt.Errorf("receiving task assignment: %w", err)
		}

		w.logger.Info("received task assignment",
			"task_id", assignment.GetTaskId(),
			"type", assignment.GetType(),
		)

		go w.executeTask(ctx, assignment)
	}
}

// heartbeatLoop sends periodic heartbeats to the scheduler.
func (w *Worker) heartbeatLoop(ctx context.Context, stream grpc.BidiStreamingClient[forgepb.WorkerHeartbeat, forgepb.TaskAssignment]) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := stream.Send(&forgepb.WorkerHeartbeat{
				WorkerId:       w.id,
				Capabilities:   w.capabilities(),
				AvailableSlots: w.availableSlots(),
			}); err != nil {
				w.logger.Error("sending heartbeat", "error", err)
				return
			}
		}
	}
}

// executeTask runs a task assignment through the appropriate handler and
// reports the result to the scheduler.
func (w *Worker) executeTask(ctx context.Context, assignment *forgepb.TaskAssignment) {
	w.activeSlots.Add(1)
	defer w.activeSlots.Add(-1)

	taskID := assignment.GetTaskId()
	taskType := assignment.GetType()

	handler, ok := w.handlers[taskType]
	if !ok {
		w.reportResult(ctx, taskID, false, nil, fmt.Sprintf("no handler for task type %q", taskType))
		return
	}

	// Create a context with the task's timeout if specified.
	taskCtx := ctx
	if assignment.GetTimeoutSeconds() > 0 {
		var cancel context.CancelFunc
		taskCtx, cancel = context.WithTimeout(ctx, time.Duration(assignment.GetTimeoutSeconds())*time.Second)
		defer cancel()
	}

	result, err := handler.Execute(taskCtx, assignment.GetPayload())
	if err != nil {
		w.logger.Error("task execution failed",
			"task_id", taskID,
			"type", taskType,
			"error", err,
		)
		w.reportResult(ctx, taskID, false, nil, err.Error())
		return
	}

	w.logger.Info("task completed",
		"task_id", taskID,
		"type", taskType,
	)
	w.reportResult(ctx, taskID, true, result, "")
}

// reportResult sends a task result to the scheduler via ReportTaskResult RPC.
func (w *Worker) reportResult(ctx context.Context, taskID string, success bool, result []byte, errMsg string) {
	_, err := w.client.ReportTaskResult(ctx, &forgepb.TaskResult{
		TaskId:       taskID,
		WorkerId:     w.id,
		Success:      success,
		Result:       result,
		ErrorMessage: errMsg,
	})
	if err != nil {
		w.logger.Error("reporting task result",
			"task_id", taskID,
			"error", err,
		)
	}
}
