package handlers

import "context"

// TaskHandler defines the interface for pluggable task executors.
// Each handler registers itself by type name and executes tasks with
// the given payload.
type TaskHandler interface {
	// Type returns the task type this handler can execute (e.g., "sleep", "fibonacci").
	Type() string

	// Execute runs the task with the given payload and returns the result.
	// The context carries timeout and cancellation signals.
	Execute(ctx context.Context, payload []byte) ([]byte, error)
}

// NewRegistry builds a handler lookup map from the given handlers, keyed by Type().
func NewRegistry(handlers ...TaskHandler) map[string]TaskHandler {
	registry := make(map[string]TaskHandler, len(handlers))
	for _, h := range handlers {
		registry[h.Type()] = h
	}
	return registry
}
