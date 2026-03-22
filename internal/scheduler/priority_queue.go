package scheduler

import (
	"container/heap"
	"sync"

	raftpkg "github.com/Ritpra93/forge/internal/raft"
)

// ---------------------------------------------------------------------------
// heap.Interface implementation (unexported)
// ---------------------------------------------------------------------------

// pqItem wraps a task with its pre-extracted sort keys so the heap comparator
// never touches the underlying Task pointer after insertion.
type pqItem struct {
	task      *raftpkg.Task
	priority  int   // snapshot of task.Priority at insertion time
	createdAt int64 // snapshot of task.CreatedAt at insertion time
	index     int   // maintained by heap.Interface for O(log n) removal
}

type pqHeap []*pqItem

func (h pqHeap) Len() int { return len(h) }

// Less returns true when item i should be popped before item j.
// Ordering rules:
//  1. Higher priority value wins (CRITICAL=3 before LOW=0).
//  2. Among equal priority, smaller CreatedAt wins (FIFO — older tasks first).
func (h pqHeap) Less(i, j int) bool {
	if h[i].priority != h[j].priority {
		return h[i].priority > h[j].priority
	}
	return h[i].createdAt < h[j].createdAt
}

func (h pqHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *pqHeap) Push(x any) {
	n := len(*h)
	item := x.(*pqItem)
	item.index = n
	*h = append(*h, item)
}

func (h *pqHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.index = -1 // mark as removed
	*h = old[:n-1]
	return item
}

// ---------------------------------------------------------------------------
// PriorityQueue — thread-safe public wrapper
// ---------------------------------------------------------------------------

// PriorityQueue is a thread-safe min-max heap of Tasks ordered by:
//  1. Priority descending (CRITICAL first)
//  2. CreatedAt ascending within the same priority tier (FIFO)
type PriorityQueue struct {
	mu sync.Mutex
	h  pqHeap
}

// NewPriorityQueue creates an empty, initialised PriorityQueue.
func NewPriorityQueue() *PriorityQueue {
	pq := &PriorityQueue{}
	heap.Init(&pq.h)
	return pq
}

// Push adds a task to the queue.
func (pq *PriorityQueue) Push(task *raftpkg.Task) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	heap.Push(&pq.h, &pqItem{
		task:      task,
		priority:  task.Priority,
		createdAt: task.CreatedAt,
	})
}

// Pop removes and returns the highest-priority task, or nil if empty.
func (pq *PriorityQueue) Pop() *raftpkg.Task {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if len(pq.h) == 0 {
		return nil
	}
	return heap.Pop(&pq.h).(*pqItem).task
}

// Peek returns the highest-priority task without removing it, or nil if empty.
func (pq *PriorityQueue) Peek() *raftpkg.Task {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if len(pq.h) == 0 {
		return nil
	}
	return pq.h[0].task
}

// Len returns the number of tasks currently in the queue.
func (pq *PriorityQueue) Len() int {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return len(pq.h)
}
