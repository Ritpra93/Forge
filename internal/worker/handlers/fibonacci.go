package handlers

import (
	"context"
	"encoding/json"
	"fmt"
)

// FibonacciHandler computes the Nth Fibonacci number iteratively.
// Payload: {"n": N}
// Result:  {"n": N, "result": R}
type FibonacciHandler struct{}

type fibPayload struct {
	N int `json:"n"`
}

type fibResult struct {
	N      int   `json:"n"`
	Result int64 `json:"result"`
}

// Type returns "fibonacci".
func (h *FibonacciHandler) Type() string { return "fibonacci" }

// Execute computes fibonacci(N) iteratively.
func (h *FibonacciHandler) Execute(ctx context.Context, payload []byte) ([]byte, error) {
	var p fibPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("parsing fibonacci payload: %w", err)
	}
	if p.N < 0 {
		return nil, fmt.Errorf("n must be non-negative, got %d", p.N)
	}

	result := fibonacci(p.N)
	return json.Marshal(fibResult{N: p.N, Result: result})
}

func fibonacci(n int) int64 {
	if n <= 1 {
		return int64(n)
	}
	var a, b int64 = 0, 1
	for i := 2; i <= n; i++ {
		a, b = b, a+b
	}
	return b
}
