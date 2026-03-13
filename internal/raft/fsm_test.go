package raft

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	hcraft "github.com/hashicorp/raft"
)

// applyCommand is a test helper that applies a Command to the FSM.
func applyCommand(t *testing.T, fsm *TaskFSM, cmd Command) interface{} {
	t.Helper()
	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshaling command: %v", err)
	}
	return fsm.Apply(&hcraft.Log{Data: data})
}

// mustApplyCommand applies a command and fails the test if it returns an error.
func mustApplyCommand(t *testing.T, fsm *TaskFSM, cmd Command) {
	t.Helper()
	result := applyCommand(t, fsm, cmd)
	if err, ok := result.(error); ok {
		t.Fatalf("unexpected error applying command %q: %v", cmd.Type, err)
	}
}

// createTestTask is a helper that creates a task in the FSM.
func createTestTask(t *testing.T, fsm *TaskFSM, id, taskType string, maxRetries int) {
	t.Helper()
	payload, err := json.Marshal(Task{
		ID:         id,
		Type:       taskType,
		MaxRetries: maxRetries,
	})
	if err != nil {
		t.Fatalf("marshaling task: %v", err)
	}
	mustApplyCommand(t, fsm, Command{
		Type:    "create_task",
		TaskID:  id,
		Payload: payload,
	})
}

func TestApplyCreateTask(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		taskType   string
		maxRetries int
	}{
		{name: "fibonacci task", id: "task-1", taskType: "fibonacci", maxRetries: 3},
		{name: "sleep task", id: "task-2", taskType: "sleep", maxRetries: 0},
		{name: "http check task", id: "task-3", taskType: "http_check", maxRetries: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsm := NewTaskFSM()
			createTestTask(t, fsm, tt.id, tt.taskType, tt.maxRetries)

			task := fsm.GetTask(tt.id)
			if task == nil {
				t.Fatal("expected task to exist")
			}
			if task.ID != tt.id {
				t.Errorf("got ID %q, want %q", task.ID, tt.id)
			}
			if task.Type != tt.taskType {
				t.Errorf("got Type %q, want %q", task.Type, tt.taskType)
			}
			if task.Status != "pending" {
				t.Errorf("got Status %q, want %q", task.Status, "pending")
			}
			if task.MaxRetries != tt.maxRetries {
				t.Errorf("got MaxRetries %d, want %d", task.MaxRetries, tt.maxRetries)
			}
			if task.CreatedAt == 0 {
				t.Error("expected CreatedAt to be set")
			}
		})
	}
}

func TestApplyAssignTask(t *testing.T) {
	fsm := NewTaskFSM()
	createTestTask(t, fsm, "task-1", "fibonacci", 3)

	payload, _ := json.Marshal(AssignTaskPayload{WorkerID: "worker-1"})
	mustApplyCommand(t, fsm, Command{
		Type:    "assign_task",
		TaskID:  "task-1",
		Payload: payload,
	})

	task := fsm.GetTask("task-1")
	if task.Status != "running" {
		t.Errorf("got Status %q, want %q", task.Status, "running")
	}
	if task.AssignedWorker != "worker-1" {
		t.Errorf("got AssignedWorker %q, want %q", task.AssignedWorker, "worker-1")
	}
	if task.UpdatedAt == task.CreatedAt {
		t.Error("expected UpdatedAt to differ from CreatedAt after assignment")
	}
}

func TestApplyCompleteTask(t *testing.T) {
	fsm := NewTaskFSM()
	createTestTask(t, fsm, "task-1", "fibonacci", 3)

	payload, _ := json.Marshal(AssignTaskPayload{WorkerID: "worker-1"})
	mustApplyCommand(t, fsm, Command{
		Type:    "assign_task",
		TaskID:  "task-1",
		Payload: payload,
	})

	mustApplyCommand(t, fsm, Command{
		Type:   "complete_task",
		TaskID: "task-1",
	})

	task := fsm.GetTask("task-1")
	if task.Status != "completed" {
		t.Errorf("got Status %q, want %q", task.Status, "completed")
	}
}

func TestApplyFailTask(t *testing.T) {
	tests := []struct {
		name           string
		maxRetries     int
		failCount      int
		expectedStatus string
	}{
		{
			name:           "first failure with retries remaining",
			maxRetries:     3,
			failCount:      1,
			expectedStatus: "pending",
		},
		{
			name:           "second failure with retries remaining",
			maxRetries:     3,
			failCount:      2,
			expectedStatus: "pending",
		},
		{
			name:           "max retries exceeded",
			maxRetries:     3,
			failCount:      3,
			expectedStatus: "dead_letter",
		},
		{
			name:           "zero max retries dead letters immediately",
			maxRetries:     0,
			failCount:      1,
			expectedStatus: "dead_letter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsm := NewTaskFSM()
			createTestTask(t, fsm, "task-1", "fibonacci", tt.maxRetries)

			for i := 0; i < tt.failCount; i++ {
				mustApplyCommand(t, fsm, Command{
					Type:   "fail_task",
					TaskID: "task-1",
				})
			}

			task := fsm.GetTask("task-1")
			if task.Status != tt.expectedStatus {
				t.Errorf("got Status %q, want %q", task.Status, tt.expectedStatus)
			}
			if task.RetryCount != tt.failCount {
				t.Errorf("got RetryCount %d, want %d", task.RetryCount, tt.failCount)
			}
		})
	}
}

func TestApplyReassignTask(t *testing.T) {
	fsm := NewTaskFSM()
	createTestTask(t, fsm, "task-1", "fibonacci", 3)

	payload, _ := json.Marshal(AssignTaskPayload{WorkerID: "worker-1"})
	mustApplyCommand(t, fsm, Command{
		Type:    "assign_task",
		TaskID:  "task-1",
		Payload: payload,
	})

	mustApplyCommand(t, fsm, Command{
		Type:   "reassign_task",
		TaskID: "task-1",
	})

	task := fsm.GetTask("task-1")
	if task.Status != "pending" {
		t.Errorf("got Status %q, want %q", task.Status, "pending")
	}
	if task.AssignedWorker != "" {
		t.Errorf("got AssignedWorker %q, want empty", task.AssignedWorker)
	}
}

func TestSnapshotAndRestore(t *testing.T) {
	fsm := NewTaskFSM()
	createTestTask(t, fsm, "task-1", "fibonacci", 3)
	createTestTask(t, fsm, "task-2", "sleep", 1)

	// Assign task-1
	payload, _ := json.Marshal(AssignTaskPayload{WorkerID: "worker-1"})
	mustApplyCommand(t, fsm, Command{
		Type:    "assign_task",
		TaskID:  "task-1",
		Payload: payload,
	})

	// Snapshot
	snap, err := fsm.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Persist snapshot to buffer
	var buf bytes.Buffer
	sink := &mockSnapshotSink{buf: &buf}
	if err := snap.Persist(sink); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// Restore into a new FSM
	fsm2 := NewTaskFSM()
	if err := fsm2.Restore(io.NopCloser(&buf)); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Verify state matches
	task1 := fsm2.GetTask("task-1")
	if task1 == nil {
		t.Fatal("task-1 not found after restore")
	}
	if task1.Status != "running" {
		t.Errorf("task-1: got Status %q, want %q", task1.Status, "running")
	}
	if task1.AssignedWorker != "worker-1" {
		t.Errorf("task-1: got AssignedWorker %q, want %q", task1.AssignedWorker, "worker-1")
	}

	task2 := fsm2.GetTask("task-2")
	if task2 == nil {
		t.Fatal("task-2 not found after restore")
	}
	if task2.Status != "pending" {
		t.Errorf("task-2: got Status %q, want %q", task2.Status, "pending")
	}
}

func TestApplyInvalidJSON(t *testing.T) {
	fsm := NewTaskFSM()
	result := fsm.Apply(&hcraft.Log{Data: []byte("not json")})
	if result == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if _, ok := result.(error); !ok {
		t.Fatalf("expected error type, got %T", result)
	}
}

func TestApplyUnknownCommand(t *testing.T) {
	fsm := NewTaskFSM()
	result := applyCommand(t, fsm, Command{Type: "unknown_command"})
	if result != nil {
		t.Errorf("expected nil for unknown command, got %v", result)
	}
}

func TestApplyCreateTaskInvalidPayload(t *testing.T) {
	fsm := NewTaskFSM()
	// Construct raw JSON with a valid command wrapper but invalid payload.
	// We bypass applyCommand because json.Marshal rejects invalid RawMessage.
	data := []byte(`{"type":"create_task","task_id":"t1","payload":"not an object"}`)
	result := fsm.Apply(&hcraft.Log{Data: data})
	if result == nil {
		t.Fatal("expected error for invalid payload")
	}
	if _, ok := result.(error); !ok {
		t.Fatalf("expected error type, got %T", result)
	}
}

func TestApplyCommandOnNonexistentTask(t *testing.T) {
	tests := []struct {
		name    string
		cmdType string
	}{
		{name: "assign nonexistent", cmdType: "assign_task"},
		{name: "complete nonexistent", cmdType: "complete_task"},
		{name: "fail nonexistent", cmdType: "fail_task"},
		{name: "reassign nonexistent", cmdType: "reassign_task"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsm := NewTaskFSM()
			cmd := Command{
				Type:   tt.cmdType,
				TaskID: "nonexistent",
			}
			if tt.cmdType == "assign_task" {
				payload, _ := json.Marshal(AssignTaskPayload{WorkerID: "w1"})
				cmd.Payload = payload
			}
			result := applyCommand(t, fsm, cmd)
			if result == nil {
				t.Fatal("expected error for nonexistent task")
			}
			if _, ok := result.(error); !ok {
				t.Fatalf("expected error type, got %T", result)
			}
		})
	}
}

func TestGetTaskReturnsNilForMissing(t *testing.T) {
	fsm := NewTaskFSM()
	if task := fsm.GetTask("nonexistent"); task != nil {
		t.Errorf("expected nil, got %+v", task)
	}
}

func TestGetAllTasks(t *testing.T) {
	fsm := NewTaskFSM()
	createTestTask(t, fsm, "task-1", "fibonacci", 3)
	createTestTask(t, fsm, "task-2", "sleep", 1)

	tasks := fsm.GetAllTasks()
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks["task-1"] == nil || tasks["task-2"] == nil {
		t.Error("expected both tasks to be present")
	}
}

// mockSnapshotSink implements hcraft.SnapshotSink for testing.
type mockSnapshotSink struct {
	buf      *bytes.Buffer
	canceled bool
}

func (m *mockSnapshotSink) Write(p []byte) (n int, err error) {
	return m.buf.Write(p)
}

func (m *mockSnapshotSink) Close() error {
	return nil
}

func (m *mockSnapshotSink) ID() string {
	return "mock-snapshot"
}

func (m *mockSnapshotSink) Cancel() error {
	m.canceled = true
	return nil
}
