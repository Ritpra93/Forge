package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SleepHandler sleeps for a configurable number of seconds.
// Payload: {"seconds": N}
// Result:  {"slept": N}
type SleepHandler struct{}

type sleepPayload struct {
	Seconds int `json:"seconds"`
}

type sleepResult struct {
	Slept int `json:"slept"`
}

// Type returns "sleep".
func (h *SleepHandler) Type() string { return "sleep" }

// Execute sleeps for the specified duration, respecting context cancellation.
func (h *SleepHandler) Execute(ctx context.Context, payload []byte) ([]byte, error) {
	var p sleepPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("parsing sleep payload: %w", err)
	}
	if p.Seconds < 0 {
		return nil, fmt.Errorf("seconds must be non-negative, got %d", p.Seconds)
	}

	select {
	case <-time.After(time.Duration(p.Seconds) * time.Second):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return json.Marshal(sleepResult{Slept: p.Seconds})
}
