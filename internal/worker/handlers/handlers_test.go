package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSleepHandler(t *testing.T) {
	h := &SleepHandler{}

	if h.Type() != "sleep" {
		t.Errorf("Type() = %q, want %q", h.Type(), "sleep")
	}

	t.Run("valid payload", func(t *testing.T) {
		payload, _ := json.Marshal(sleepPayload{Seconds: 0})
		result, err := h.Execute(context.Background(), payload)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		var r sleepResult
		if err := json.Unmarshal(result, &r); err != nil {
			t.Fatalf("unmarshaling result: %v", err)
		}
		if r.Slept != 0 {
			t.Errorf("got slept=%d, want 0", r.Slept)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		payload, _ := json.Marshal(sleepPayload{Seconds: 60})
		_, err := h.Execute(ctx, payload)
		if err == nil {
			t.Fatal("expected error from cancelled context")
		}
		if err != context.Canceled {
			t.Errorf("got error %v, want context.Canceled", err)
		}
	})

	t.Run("negative seconds", func(t *testing.T) {
		payload, _ := json.Marshal(sleepPayload{Seconds: -1})
		_, err := h.Execute(context.Background(), payload)
		if err == nil {
			t.Fatal("expected error for negative seconds")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		_, err := h.Execute(context.Background(), []byte("not json"))
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})
}

func TestFibonacciHandler(t *testing.T) {
	h := &FibonacciHandler{}

	if h.Type() != "fibonacci" {
		t.Errorf("Type() = %q, want %q", h.Type(), "fibonacci")
	}

	tests := []struct {
		n    int
		want int64
	}{
		{0, 0},
		{1, 1},
		{2, 1},
		{5, 5},
		{10, 55},
		{20, 6765},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("fib(%d)", tt.n), func(t *testing.T) {
			payload, _ := json.Marshal(fibPayload{N: tt.n})
			result, err := h.Execute(context.Background(), payload)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			var r fibResult
			if err := json.Unmarshal(result, &r); err != nil {
				t.Fatalf("unmarshaling result: %v", err)
			}
			if r.Result != tt.want {
				t.Errorf("fib(%d) = %d, want %d", tt.n, r.Result, tt.want)
			}
			if r.N != tt.n {
				t.Errorf("got n=%d, want %d", r.N, tt.n)
			}
		})
	}

	t.Run("negative n", func(t *testing.T) {
		payload, _ := json.Marshal(fibPayload{N: -1})
		_, err := h.Execute(context.Background(), payload)
		if err == nil {
			t.Fatal("expected error for negative n")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		_, err := h.Execute(context.Background(), []byte("{bad"))
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})
}

func TestHTTPCheckHandler(t *testing.T) {
	h := &HTTPCheckHandler{}

	if h.Type() != "httpcheck" {
		t.Errorf("Type() = %q, want %q", h.Type(), "httpcheck")
	}

	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		payload, _ := json.Marshal(httpCheckPayload{URL: srv.URL})
		result, err := h.Execute(context.Background(), payload)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		var r httpCheckResult
		if err := json.Unmarshal(result, &r); err != nil {
			t.Fatalf("unmarshaling result: %v", err)
		}
		if r.StatusCode != 200 {
			t.Errorf("got status_code=%d, want 200", r.StatusCode)
		}
		if r.URL != srv.URL {
			t.Errorf("got url=%q, want %q", r.URL, srv.URL)
		}
	})

	t.Run("non-200 status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		payload, _ := json.Marshal(httpCheckPayload{URL: srv.URL})
		result, err := h.Execute(context.Background(), payload)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		var r httpCheckResult
		if err := json.Unmarshal(result, &r); err != nil {
			t.Fatalf("unmarshaling result: %v", err)
		}
		if r.StatusCode != 503 {
			t.Errorf("got status_code=%d, want 503", r.StatusCode)
		}
	})

	t.Run("context timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer srv.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		payload, _ := json.Marshal(httpCheckPayload{URL: srv.URL})
		_, err := h.Execute(ctx, payload)
		if err == nil {
			t.Fatal("expected error from context timeout")
		}
	})

	t.Run("empty url", func(t *testing.T) {
		payload, _ := json.Marshal(httpCheckPayload{URL: ""})
		_, err := h.Execute(context.Background(), payload)
		if err == nil {
			t.Fatal("expected error for empty URL")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		_, err := h.Execute(context.Background(), []byte("nope"))
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})
}

func TestNewRegistry(t *testing.T) {
	registry := NewRegistry(&SleepHandler{}, &FibonacciHandler{}, &HTTPCheckHandler{})

	if len(registry) != 3 {
		t.Fatalf("got %d handlers, want 3", len(registry))
	}

	for _, typ := range []string{"sleep", "fibonacci", "httpcheck"} {
		if _, ok := registry[typ]; !ok {
			t.Errorf("handler %q not found in registry", typ)
		}
	}
}
