package scheduler

import (
	"sync"
	"testing"

	raftpkg "github.com/Ritpra93/forge/internal/raft"
)

// makeTask is a test helper that constructs a minimal Task with the given
// priority and creation timestamp.
func makeTask(id string, priority int, createdAt int64) *raftpkg.Task {
	return &raftpkg.Task{
		ID:        id,
		Priority:  priority,
		CreatedAt: createdAt,
	}
}

// TestPopOrderByPriority verifies that tasks are returned in descending
// priority order (CRITICAL=3 before HIGH=2 before NORMAL=1 before LOW=0).
func TestPopOrderByPriority(t *testing.T) {
	t.Helper()
	pq := NewPriorityQueue()

	// Push in deliberately shuffled order.
	pq.Push(makeTask("low", 0, 100))
	pq.Push(makeTask("critical", 3, 100))
	pq.Push(makeTask("normal", 1, 100))
	pq.Push(makeTask("high", 2, 100))

	want := []string{"critical", "high", "normal", "low"}
	for i, wantID := range want {
		got := pq.Pop()
		if got == nil {
			t.Fatalf("pop %d: got nil, want %q", i, wantID)
		}
		if got.ID != wantID {
			t.Errorf("pop %d: got %q, want %q", i, got.ID, wantID)
		}
	}
	if pq.Pop() != nil {
		t.Error("expected nil after draining queue")
	}
}

// TestFIFOWithinSamePriority verifies that tasks with equal priority are
// returned oldest-first (ascending CreatedAt — FIFO).
func TestFIFOWithinSamePriority(t *testing.T) {
	pq := NewPriorityQueue()

	// All NORMAL priority; push in reverse age order so the heap must reorder.
	pq.Push(makeTask("newest", 1, 300))
	pq.Push(makeTask("oldest", 1, 100))
	pq.Push(makeTask("middle", 1, 200))

	want := []string{"oldest", "middle", "newest"}
	for i, wantID := range want {
		got := pq.Pop()
		if got == nil {
			t.Fatalf("pop %d: got nil, want %q", i, wantID)
		}
		if got.ID != wantID {
			t.Errorf("pop %d: got %q, want %q", i, got.ID, wantID)
		}
	}
}

// TestMixedPriorityAndTimestamp verifies that priority beats age: a newer
// CRITICAL task should come before an older LOW task.
func TestMixedPriorityAndTimestamp(t *testing.T) {
	pq := NewPriorityQueue()

	pq.Push(makeTask("old-low", 0, 1))   // oldest but lowest priority
	pq.Push(makeTask("new-crit", 3, 999)) // newest but highest priority

	first := pq.Pop()
	if first == nil || first.ID != "new-crit" {
		t.Errorf("expected new-crit first, got %v", first)
	}
}

// TestEmptyQueueBehaviour verifies that Pop and Peek return nil on an empty queue.
func TestEmptyQueueBehaviour(t *testing.T) {
	pq := NewPriorityQueue()

	if pq.Pop() != nil {
		t.Error("Pop() on empty queue should return nil")
	}
	if pq.Peek() != nil {
		t.Error("Peek() on empty queue should return nil")
	}
	if pq.Len() != 0 {
		t.Errorf("Len() on empty queue = %d, want 0", pq.Len())
	}
}

// TestPeekDoesNotRemove verifies that Peek leaves the task in the queue.
func TestPeekDoesNotRemove(t *testing.T) {
	pq := NewPriorityQueue()
	pq.Push(makeTask("t1", 2, 1))

	if pq.Peek() == nil {
		t.Fatal("Peek() returned nil")
	}
	if pq.Len() != 1 {
		t.Errorf("Len() after Peek = %d, want 1", pq.Len())
	}
	if pq.Pop() == nil {
		t.Error("Pop() after Peek returned nil")
	}
	if pq.Len() != 0 {
		t.Errorf("Len() after Pop = %d, want 0", pq.Len())
	}
}

// TestConcurrentPushPop exercises the mutex under the race detector.
// It pushes 200 tasks from 10 goroutines and pops them all from 5 goroutines,
// verifying that the total count is correct and no data races occur.
func TestConcurrentPushPop(t *testing.T) {
	const producers = 10
	const tasksPerProducer = 20
	const consumers = 5
	const total = producers * tasksPerProducer

	pq := NewPriorityQueue()

	var wg sync.WaitGroup

	// Producers.
	for p := range producers {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := range tasksPerProducer {
				pq.Push(makeTask(
					// unique ID per task
					"t-"+string(rune('A'+p))+"-"+string(rune('0'+i%10)),
					p%4,             // priority 0–3
					int64(p*100+i), // deterministic creation time
				))
			}
		}(p)
	}

	// Consumers — drain concurrently while producers are still pushing.
	popped := make(chan struct{}, total)
	for range consumers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if pq.Pop() != nil {
					popped <- struct{}{}
				}
				if len(popped) == total {
					return
				}
			}
		}()
	}

	wg.Wait()
	close(popped)

	// Count what was actually consumed (producers always finish before we count).
	var count int
	for range popped {
		count++
	}
	// Any remainder still in the queue is fine — we just want no races and
	// no panics. Pop the rest.
	for pq.Pop() != nil {
		count++
	}
	if count != total {
		t.Errorf("total tasks processed = %d, want %d", count, total)
	}
}
