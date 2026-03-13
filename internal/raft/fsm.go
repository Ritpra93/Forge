package raft

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	hcraft "github.com/hashicorp/raft"
)

// Command represents a state change in the task system.
// Commands are JSON-encoded in raft.Log.Data.
type Command struct {
	Type    string          `json:"type"`    // create_task, assign_task, complete_task, fail_task, reassign_task
	TaskID  string          `json:"task_id"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Task represents a task in the system.
type Task struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Status         string `json:"status"` // pending, scheduled, running, completed, failed, retrying, dead_letter
	Payload        []byte `json:"payload"`
	AssignedWorker string `json:"assigned_worker"`
	RetryCount     int    `json:"retry_count"`
	MaxRetries     int    `json:"max_retries"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

// AssignTaskPayload is the payload for assign_task commands.
type AssignTaskPayload struct {
	WorkerID string `json:"worker_id"`
}

// TaskFSM implements the hashicorp/raft FSM interface.
// It manages the replicated state of all tasks in the system.
type TaskFSM struct {
	mu    sync.RWMutex
	tasks map[string]*Task
}

// NewTaskFSM creates a new TaskFSM with an empty task map.
func NewTaskFSM() *TaskFSM {
	return &TaskFSM{
		tasks: make(map[string]*Task),
	}
}

// Apply is called once a log entry is committed by a majority of the cluster.
// It must be deterministic and produce the same result on all peers.
func (f *TaskFSM) Apply(log *hcraft.Log) interface{} {
	var cmd Command
	if err := json.Unmarshal(log.Data, &cmd); err != nil {
		return fmt.Errorf("unmarshaling command: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	switch cmd.Type {
	case "create_task":
		return f.applyCreateTask(cmd)
	case "assign_task":
		return f.applyAssignTask(cmd)
	case "complete_task":
		return f.applyCompleteTask(cmd)
	case "fail_task":
		return f.applyFailTask(cmd)
	case "reassign_task":
		return f.applyReassignTask(cmd)
	default:
		return nil
	}
}

func (f *TaskFSM) applyCreateTask(cmd Command) error {
	var task Task
	if err := json.Unmarshal(cmd.Payload, &task); err != nil {
		return fmt.Errorf("unmarshaling create_task payload: %w", err)
	}
	task.Status = "pending"
	task.CreatedAt = time.Now().UnixNano()
	task.UpdatedAt = task.CreatedAt
	f.tasks[task.ID] = &task
	return nil
}

func (f *TaskFSM) applyAssignTask(cmd Command) error {
	task, ok := f.tasks[cmd.TaskID]
	if !ok {
		return fmt.Errorf("task %s not found", cmd.TaskID)
	}

	var payload AssignTaskPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshaling assign_task payload: %w", err)
	}

	task.Status = "running"
	task.AssignedWorker = payload.WorkerID
	task.UpdatedAt = time.Now().UnixNano()
	return nil
}

func (f *TaskFSM) applyCompleteTask(cmd Command) error {
	task, ok := f.tasks[cmd.TaskID]
	if !ok {
		return fmt.Errorf("task %s not found", cmd.TaskID)
	}

	task.Status = "completed"
	task.UpdatedAt = time.Now().UnixNano()
	return nil
}

func (f *TaskFSM) applyFailTask(cmd Command) error {
	task, ok := f.tasks[cmd.TaskID]
	if !ok {
		return fmt.Errorf("task %s not found", cmd.TaskID)
	}

	task.RetryCount++
	task.UpdatedAt = time.Now().UnixNano()

	if task.RetryCount >= task.MaxRetries {
		task.Status = "dead_letter"
	} else {
		task.Status = "pending"
	}
	return nil
}

func (f *TaskFSM) applyReassignTask(cmd Command) error {
	task, ok := f.tasks[cmd.TaskID]
	if !ok {
		return fmt.Errorf("task %s not found", cmd.TaskID)
	}

	task.Status = "pending"
	task.AssignedWorker = ""
	task.UpdatedAt = time.Now().UnixNano()
	return nil
}

// Snapshot returns a point-in-time snapshot of all tasks.
// Apply will NOT be called while Snapshot runs, but Apply WILL be
// called concurrently with FSMSnapshot.Persist.
func (f *TaskFSM) Snapshot() (hcraft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	tasks := make(map[string]*Task, len(f.tasks))
	for k, v := range f.tasks {
		copied := *v
		tasks[k] = &copied
	}

	return &taskSnapshot{tasks: tasks}, nil
}

// Restore replaces all FSM state from a snapshot.
func (f *TaskFSM) Restore(reader io.ReadCloser) error {
	defer reader.Close()

	var tasks map[string]*Task
	if err := json.NewDecoder(reader).Decode(&tasks); err != nil {
		return fmt.Errorf("decoding snapshot: %w", err)
	}

	f.mu.Lock()
	f.tasks = tasks
	f.mu.Unlock()

	return nil
}

// GetTask returns a copy of the task with the given ID, or nil if not found.
func (f *TaskFSM) GetTask(id string) *Task {
	f.mu.RLock()
	defer f.mu.RUnlock()

	task, ok := f.tasks[id]
	if !ok {
		return nil
	}
	copied := *task
	return &copied
}

// GetAllTasks returns a deep copy of all tasks.
func (f *TaskFSM) GetAllTasks() map[string]*Task {
	f.mu.RLock()
	defer f.mu.RUnlock()

	tasks := make(map[string]*Task, len(f.tasks))
	for k, v := range f.tasks {
		copied := *v
		tasks[k] = &copied
	}
	return tasks
}

// taskSnapshot implements hcraft.FSMSnapshot.
type taskSnapshot struct {
	tasks map[string]*Task
}

// Persist writes all task state to the sink.
func (s *taskSnapshot) Persist(sink hcraft.SnapshotSink) error {
	data, err := json.Marshal(s.tasks)
	if err != nil {
		sink.Cancel()
		return fmt.Errorf("marshaling snapshot: %w", err)
	}

	if _, err := sink.Write(data); err != nil {
		sink.Cancel()
		return fmt.Errorf("writing snapshot: %w", err)
	}

	return sink.Close()
}

// Release is called when the snapshot is no longer needed.
func (s *taskSnapshot) Release() {}
