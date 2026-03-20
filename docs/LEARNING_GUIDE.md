# The Forge Learning Guide

## Understanding Distributed Systems, Go Patterns, and Production Infrastructure Through Code

**Who this is for:** You are a beginner who wants to understand how real distributed systems work. This guide walks you through every pattern, concept, and technique in the Forge codebase — a fault-tolerant distributed task orchestrator built in Go.

**How to read this guide:** Start from the top and read straight through. Each section builds on the previous one. When you see a file reference like `internal/raft/fsm.go:56`, open that file and read the code alongside this guide. The ASCII diagrams are meant to be studied — trace the arrows with your finger.

**What you will learn:**
- How to build distributed systems that survive machine failures
- Go patterns used in production systems (interfaces, concurrency, error handling)
- gRPC and Protocol Buffers for service communication
- Prometheus and Grafana for observability
- Docker for deployment
- Terminal UIs with Bubbletea
- Testing strategies for distributed systems

---

## Table of Contents

- [Section 1: The Big Picture](#section-1-the-big-picture)
- [Section 2: Go Patterns](#section-2-go-patterns)
- [Section 3: Distributed Systems Concepts](#section-3-distributed-systems-concepts)
- [Section 4: gRPC and Protocol Buffers](#section-4-grpc-and-protocol-buffers)
- [Section 5: Observability](#section-5-observability)
- [Section 6: Docker and Deployment](#section-6-docker-and-deployment)
- [Section 7: CLI Design](#section-7-cli-design)
- [Section 8: Terminal UI (Bubbletea)](#section-8-terminal-ui-bubbletea)
- [Section 9: Testing Patterns](#section-9-testing-patterns)
- [Section 10: Patterns You Can Reuse](#section-10-patterns-you-can-reuse)
- [Glossary](#glossary)

---

## Section 1: The Big Picture

### What Is Forge?

Forge is a **distributed task orchestrator**. Think of it as a job assignment system:

- You submit work ("compute fibonacci(42)", "sleep for 5 seconds", "check if google.com is up")
- Forge assigns that work to available worker machines
- If a worker dies mid-task, Forge detects the failure and reassigns the work
- The entire system survives machine crashes because the state is replicated across multiple nodes

### The Three Components

Forge has three types of processes that work together:

```
+------------------------------------------------------------------+
|                        FORGE ARCHITECTURE                         |
+------------------------------------------------------------------+
|                                                                    |
|   +----------------+         +-------------------------------+     |
|   |   forgectl     |  gRPC   |      Scheduler Cluster        |     |
|   |   (CLI client) | ------> |                               |     |
|   |                |         |   +-------+ +-------+ +-----+ |     |
|   | submit tasks   |         |   | Sched | | Sched | | Sch | |     |
|   | check status   |         |   |   1   | |   2   | |  3  | |     |
|   | watch progress |         |   +---+---+ +---+---+ +-+---+ |     |
|   +----------------+         |       |         |        |     |     |
|                              |       +----+----+--------+     |     |
|                              |            |                   |     |
|                              |      Raft Consensus            |     |
|                              |   (they vote on changes)       |     |
|                              +------------+------------------+     |
|                                           |                        |
|                              gRPC bidi    |    gRPC bidi            |
|                              streaming    |    streaming            |
|                                           |                        |
|                              +------------+------------------+     |
|                              |                               |     |
|                         +----+----+                +----+----+     |
|                         | Worker  |                | Worker  |     |
|                         |    1    |                |    2    |     |
|                         |         |                |         |     |
|                         | sleep   |                | sleep   |     |
|                         | fib     |                | fib     |     |
|                         | http    |                | http    |     |
|                         +---------+                +---------+     |
|                                                                    |
+------------------------------------------------------------------+
```

**Component 1: Scheduler Cluster** (3 nodes running together)
- Accepts tasks from clients
- Decides which worker should run each task
- Tracks the state of every task (pending, running, completed, failed)
- Uses Raft consensus so all 3 nodes agree on the state
- Source: `cmd/scheduler/main.go`

**Component 2: Workers** (any number of nodes)
- Connect to the scheduler and say "I'm available for work"
- Receive task assignments via a streaming connection
- Execute tasks using pluggable handlers (sleep, fibonacci, httpcheck)
- Report results back to the scheduler
- Source: `cmd/worker/main.go`

**Component 3: forgectl CLI** (command-line client)
- The human interface — you use this to submit tasks and check on them
- Connects to any scheduler node via gRPC
- Follows redirects to the leader automatically
- Source: `cmd/forgectl/main.go`

### The Task Lifecycle

Every task in Forge goes through a state machine. This is one of the most important concepts in the entire codebase:

```
                         +----------+
            +----------> | pending  | <-----------+
            |            +----+-----+             |
            |                 |                   |
            |            assign_task          reassign_task
            |            (assigner.go)        (worker died or
            |                 |                retry ready)
            |            +----v-----+             |
            |            | running  |             |
            |            +----+-----+             |
            |                 |                   |
            |      +----------+----------+        |
            |      |                     |        |
            | complete_task         fail_task      |
            | (worker reports      (worker reports |
            |  success)             failure)       |
            |      |                     |        |
            | +----v------+    +---------v---+    |
            | | completed |    |   retrying  +----+
            | +-----------+    |  (if retries     |
            |                  |   remaining)     |
            |                  +------+------+    |
            |                         |           |
            |                    fail_task        |
            |                  (max retries       |
            |                   exceeded)         |
            |                         |           |
            |                  +------v------+    |
            |                  | dead_letter |    |
            |                  +-------------+    |
            |                                     |
            +-------------------------------------+
```

This state machine is defined in `internal/raft/fsm.go:65-78`:

```go
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
}
```

Each command type corresponds to a transition in the state diagram above.

### Following One Task Through the Entire System

Let's trace a fibonacci task from submission to completion. This is the best way to understand how all the pieces fit together.

```
  forgectl                Scheduler (Leader)           Worker
     |                          |                        |
     |  1. SubmitTask(fib)      |                        |
     | -----------------------> |                        |
     |                          |                        |
     |                    2. requireLeader()              |
     |                    3. Generate task ID             |
     |                    4. raft.Apply(create_task)      |
     |                          |                        |
     |                    5. FSM.Apply() runs on          |
     |                       all 3 scheduler nodes        |
     |                       → task status = "pending"    |
     |                          |                        |
     |  6. Response: task-abc   |                        |
     | <----------------------- |                        |
     |                          |                        |
     |                    7. Assigner goroutine wakes     |
     |                       (every 500ms tick)           |
     |                       Finds pending task           |
     |                       Finds worker with slots      |
     |                          |                        |
     |                    8. raft.Apply(assign_task)       |
     |                       → task status = "running"    |
     |                          |                        |
     |                    9. Push to worker's             |
     |                       assignCh channel             |
     |                          | ---- TaskAssignment --> |
     |                          |                        |
     |                          |                  10. Look up handler
     |                          |                      for "fibonacci"
     |                          |                        |
     |                          |                  11. Execute handler
     |                          |                      fibonacci(42)
     |                          |                        |
     |                          |  12. ReportTaskResult   |
     |                          | <---- (success) ------ |
     |                          |                        |
     |                   13. raft.Apply(complete_task)     |
     |                       → task status = "completed"  |
     |                          |                        |
```

Here are the exact functions involved:

1. **Client submits task:** `cmd/forgectl/submit.go:61` — `submitWithRedirect()` sends a `SubmitTask` RPC
2. **Leader check:** `internal/scheduler/server.go:148` — `requireLeader()` verifies this node is the Raft leader
3. **ID generation:** `internal/scheduler/server.go:137` — `newTaskID()` generates a UUID
4. **Raft apply:** `internal/scheduler/server.go:164` — `s.raft.Apply(data, timeout)` replicates the command
5. **FSM processes command:** `internal/raft/fsm.go:56` — `Apply()` switches on command type
6. **Task created:** `internal/raft/fsm.go:81` — `applyCreateTask()` sets status to "pending"
7. **Assigner wakes up:** `internal/scheduler/assigner.go:18` — `StartAssigner()` runs on a ticker
8. **Task assignment:** `internal/scheduler/assigner.go:103` — `assignTask()` applies assign_task command
9. **Push to worker channel:** `internal/scheduler/server.go:362` — `AssignTaskToWorker()` sends to channel
10. **Worker receives:** `internal/worker/worker.go:140` — `stream.Recv()` gets the assignment
11. **Handler executes:** `internal/worker/handlers/fibonacci.go:27` — `Execute()` computes the result
12. **Result reported:** `internal/worker/worker.go:226` — `reportResult()` calls `ReportTaskResult` RPC
13. **Task completed:** `internal/scheduler/server.go:306` — `ReportTaskResult()` applies complete_task

> **Try This:** Open each file referenced above and follow the function calls yourself. Write down the chain of function calls on paper. This exercise will make the architecture click.

### Files You Need to Know

Here is every file in the project and what it does:

| File | Purpose |
|------|---------|
| `cmd/scheduler/main.go` | Scheduler binary — sets up Raft, gRPC server, background goroutines |
| `cmd/worker/main.go` | Worker binary — creates worker, connects to scheduler |
| `cmd/forgectl/main.go` | CLI root command, gRPC connection helper |
| `cmd/forgectl/submit.go` | Submit tasks with leader redirect |
| `cmd/forgectl/status.go` | Query single task status |
| `cmd/forgectl/watch.go` | Stream task status updates |
| `cmd/forgectl/cluster.go` | Display cluster topology |
| `cmd/forgectl/deadletter.go` | List dead-lettered tasks |
| `cmd/forgectl/dashboard.go` | Launch live terminal dashboard |
| `internal/raft/fsm.go` | The state machine — all task state transitions |
| `internal/raft/node.go` | Raft node setup with BoltDB and TCP transport |
| `internal/scheduler/server.go` | gRPC server — all RPC handlers |
| `internal/scheduler/assigner.go` | Background task-to-worker matching |
| `internal/scheduler/worker_tracker.go` | Dead worker detection and task reassignment |
| `internal/scheduler/retry.go` | Exponential backoff retry scheduler |
| `internal/worker/worker.go` | Worker logic — heartbeats, task execution |
| `internal/worker/handlers/handler.go` | TaskHandler interface definition |
| `internal/worker/handlers/sleep.go` | Sleep task handler |
| `internal/worker/handlers/fibonacci.go` | Fibonacci computation handler |
| `internal/worker/handlers/httpcheck.go` | HTTP health check handler |
| `internal/metrics/metrics.go` | Prometheus metric definitions |
| `internal/dashboard/dashboard.go` | Bubbletea TUI model |
| `internal/dashboard/fetch.go` | Async data fetching for dashboard |
| `internal/dashboard/styles.go` | Lipgloss style definitions |
| `internal/proto/forgepb/forge.proto` | Protocol Buffer definitions |
| `deploy/Dockerfile` | Multi-stage Docker build |
| `deploy/docker-compose.yml` | Full cluster deployment |
| `deploy/prometheus.yml` | Prometheus scrape configuration |

### Understanding the Background Goroutines

The scheduler runs three critical background goroutines. Each one follows the same pattern but does different work:

```
  BACKGROUND GOROUTINE RESPONSIBILITIES

  ┌─────────────────────────────────────────────────────────────────┐
  │                                                                 │
  │  StartAssigner (every 500ms)                                    │
  │  ┌─────────────────────────────────────────────────────────┐   │
  │  │ 1. Am I the leader? If not, skip.                       │   │
  │  │ 2. Get all tasks with status "pending"                  │   │
  │  │ 3. Get all connected workers with available slots       │   │
  │  │ 4. Match tasks to workers (round-robin)                 │   │
  │  │ 5. For each match:                                      │   │
  │  │    a. Apply assign_task via Raft                        │   │
  │  │    b. Push TaskAssignment to worker's channel           │   │
  │  └─────────────────────────────────────────────────────────┘   │
  │                                                                 │
  │  StartWorkerTracker (every 3s)                                  │
  │  ┌─────────────────────────────────────────────────────────┐   │
  │  │ 1. Update Raft metrics (is_leader, commit_index)        │   │
  │  │ 2. Detect new leader elections                          │   │
  │  │ 3. Am I the leader? If not, skip.                       │   │
  │  │ 4. Phase 1: Check all workers' last heartbeat time      │   │
  │  │    If time since last heartbeat > 9s → declare dead     │   │
  │  │    Reassign all running tasks from dead worker           │   │
  │  │ 5. Phase 2: After grace period, check for orphaned      │   │
  │  │    tasks (running tasks assigned to unknown workers)     │   │
  │  └─────────────────────────────────────────────────────────┘   │
  │                                                                 │
  │  StartRetryScheduler (every 1s)                                 │
  │  ┌─────────────────────────────────────────────────────────┐   │
  │  │ 1. Am I the leader? If not, skip.                       │   │
  │  │ 2. Get all tasks with status "retrying"                 │   │
  │  │ 3. For each: compute backoff based on retry count       │   │
  │  │ 4. If enough time has passed since last failure:        │   │
  │  │    Apply reassign_task → moves task back to "pending"   │   │
  │  └─────────────────────────────────────────────────────────┘   │
  │                                                                 │
  └─────────────────────────────────────────────────────────────────┘
```

**The Round-Robin Assigner in Detail:**

The assigner at `internal/scheduler/assigner.go:32-98` uses round-robin to distribute tasks fairly:

```go
wi := 0 // round-robin index
for _, task := range pending {
    for attempts := 0; attempts < len(available); attempts++ {
        idx := (wi + attempts) % len(available)
        w := &available[idx]
        if w.slots <= 0 { continue }
        if !workerCanHandle(w.info, task.Type) { continue }
        // assign!
        wi = (idx + 1) % len(available)  // advance round-robin
        break
    }
}
```

This ensures that if you have 2 workers, tasks alternate between them: W1, W2, W1, W2, ... rather than all going to W1.

**The Worker Tracker's Two-Phase Design:**

Phase 1 (dead worker detection) runs every tick. Phase 2 (orphan detection) only runs after the leader has been leader for at least `HeartbeatDead` time. This grace period exists because after a leader failover, workers need time to discover and reconnect to the new leader. Without the grace period, the new leader would immediately reassign all tasks — even those being actively worked on by healthy workers.

> **Try This:** In `worker_tracker.go:79`, change `s.config.HeartbeatDead` to `0`. What would happen? (Answer: Immediately after becoming leader, the tracker would reassign ALL running tasks, even those being actively worked on. Workers would complete tasks that have already been reassigned, causing duplicate work.)

### How the Worker Processes Tasks

The worker's task execution flow is worth understanding in detail:

```
  WORKER TASK EXECUTION FLOW

  stream.Recv() returns a TaskAssignment
       │
       v
  go w.executeTask(ctx, assignment)     ← runs in its own goroutine
       │
       ├── w.activeSlots.Add(1)          ← increment slot counter
       │   defer w.activeSlots.Add(-1)   ← decrement on exit
       │
       ├── Look up handler by task type
       │   handler, ok := w.handlers[taskType]
       │   If not found → report failure immediately
       │
       ├── Create timeout context if timeout > 0
       │   taskCtx, cancel = context.WithTimeout(ctx, timeout)
       │   defer cancel()
       │
       ├── result, err := handler.Execute(taskCtx, payload)
       │
       ├── Record metrics:
       │   WorkerTaskExecution.Observe(duration)
       │   WorkerTasksExecuted.Inc()
       │
       └── reportResult(ctx, taskID, success, result, errMsg)
            │
            └── client.ReportTaskResult(ctx, &TaskResult{...})
                 │
                 └── Scheduler receives → applies complete_task
                     or fail_task via Raft
```

Notice that `reportResult` uses the parent `ctx`, not `taskCtx`. This is deliberate — even if the task times out, we still want to report the failure. If we used `taskCtx`, the report would also be cancelled.

---

## Section 2: Go Patterns

This section covers every Go pattern used in Forge. For each pattern, you will learn what it is, where it appears in the code, and when to use it in your own projects.

### 2.1 Interfaces: Go's Alternative to Classes

**What it is:** An interface in Go defines a set of method signatures. Any type that has those methods automatically satisfies the interface — no `implements` keyword needed.

**Analogy:** Think of a job posting. The posting says "must be able to Type() and Execute()". Anyone who can do those things qualifies for the job. They do not need to submit a formal application saying "I implement the TaskHandler interface." They just need to have the skills.

**Where it appears in Forge:**

The simplest interface in the codebase is `TaskHandler` at `internal/worker/handlers/handler.go:8-14`:

```go
type TaskHandler interface {
    // Type returns the task type this handler can execute (e.g., "sleep").
    Type() string
    // Execute runs the task with the given payload and returns the result.
    Execute(ctx context.Context, payload []byte) ([]byte, error)
}
```

Three structs satisfy this interface without ever declaring it:

- `SleepHandler` at `internal/worker/handlers/sleep.go:13` — has `Type()` returning `"sleep"` and `Execute()` that sleeps
- `FibonacciHandler` at `internal/worker/handlers/fibonacci.go:12` — has `Type()` returning `"fibonacci"` and `Execute()` that computes
- `HTTPCheckHandler` at `internal/worker/handlers/httpcheck.go:13` — has `Type()` returning `"httpcheck"` and `Execute()` that makes HTTP requests

The second critical interface is `hcraft.FSM` from hashicorp/raft, satisfied by `TaskFSM` at `internal/raft/fsm.go:42`:

```go
type TaskFSM struct {
    mu    sync.RWMutex
    tasks map[string]*Task
}
```

`TaskFSM` never says "I implement FSM." But it has `Apply()`, `Snapshot()`, and `Restore()` — the three methods the FSM interface requires. Go checks this at compile time.

**When to use this pattern:** Use interfaces when you need to swap implementations. The worker does not care whether a handler sleeps, computes fibonacci, or checks HTTP — it just calls `Execute()`. Tomorrow you could add a `DatabaseBackupHandler` and the worker would use it without any changes.

**Common Mistakes:**
> - Looking for an `implements` keyword — Go does not have one
> - Defining interfaces too early — in Go, define interfaces where they are *consumed*, not where they are implemented
> - Making interfaces too large — Go interfaces should be small (1-3 methods). The `TaskHandler` interface has just 2 methods

### 2.2 Struct Embedding (Composition Over Inheritance)

**What it is:** Go does not have class inheritance. Instead, you embed one struct inside another, and the outer struct "inherits" all the methods of the inner struct. This is called **composition**.

**Analogy:** Imagine you have a universal TV remote (the embedded struct) that has basic buttons. You duct-tape it onto a fancy remote (the outer struct) that adds extra buttons. The fancy remote can do everything the basic remote can, plus its own extras.

**Where it appears in Forge:**

The most important example is at `internal/scheduler/server.go:55-56`:

```go
type ForgeSchedulerServer struct {
    forgepb.UnimplementedForgeSchedulerServer  // <-- embedded!

    raft   *hcraft.Raft
    fsm    *raftpkg.TaskFSM
    // ... more fields
}
```

`UnimplementedForgeSchedulerServer` is generated by protoc. It provides default "not implemented" responses for every RPC method. By embedding it, `ForgeSchedulerServer` automatically satisfies the `ForgeSchedulerServer` gRPC interface. Then Forge overrides only the methods it actually implements (like `SubmitTask`, `GetTaskStatus`, etc.).

**Why this matters:** If a new RPC method is added to the `.proto` file in the future, the `Unimplemented` version provides a default response instead of causing a compilation error. This is forward-compatibility.

**When to use this pattern:** Use embedding when you want a struct to "inherit" behavior from another struct but also add or override behavior. It is the Go way of doing what other languages do with class inheritance.

**Common Mistakes:**
> - Thinking embedding is the same as inheritance — the embedded struct's methods do not have access to the outer struct's fields
> - Embedding when you should use a field — only embed if you want the methods to be promoted to the outer struct

### 2.3 Goroutines: Lightweight Concurrent Functions

**What it is:** A **goroutine** is a lightweight thread managed by Go's runtime. You start one with the `go` keyword before a function call. Goroutines let you do multiple things simultaneously — like handling network requests while also checking heartbeats.

**Analogy:** A restaurant kitchen with multiple cooks. Each cook (goroutine) works on their own dish simultaneously. The head chef (the main goroutine) starts each cook's task and coordinates everything.

**Where it appears in Forge:**

The scheduler is the most goroutine-heavy component. Here is the goroutine landscape when a scheduler starts (`cmd/scheduler/main.go`):

```
main() goroutine
  |
  |  (line 79)
  +---> go func() { http.ListenAndServe(metricsPort) }
  |     Serves the /metrics endpoint for Prometheus
  |
  |  (line 103)
  +---> go func() { grpcServer.Serve(lis) }
  |     Accepts incoming gRPC connections
  |
  |  (line 113)
  +---> go srv.StartWorkerTracker(ctx)
  |     Detects dead workers every 3 seconds
  |
  |  (line 114)
  +---> go srv.StartRetryScheduler(ctx)
  |     Moves "retrying" tasks back to "pending"
  |
  |  (line 115)
  +---> go srv.StartAssigner(ctx)
  |     Matches pending tasks to available workers
  |
  +---> [blocks on signal channel, waiting for SIGINT/SIGTERM]

  Per worker connection (server.go:238):
  +---> RegisterWorker handler goroutine
        |
        |  (line 272)
        +---> go func() { for { stream.Recv() ... } }
              Receives heartbeats from this worker
```

Notice how the main goroutine starts all the background work and then blocks on the signal channel. When a SIGINT arrives, it cancels the context and all goroutines shut down.

The worker also uses goroutines (`internal/worker/worker.go`):

```go
// Line 136: heartbeat goroutine sends periodic heartbeats
go w.heartbeatLoop(ctx, stream)

// Line 153: each task runs in its own goroutine
go w.executeTask(ctx, assignment)
```

**When to use goroutines:** Use them whenever you need to do something concurrently — handle multiple network connections, run background tasks on a timer, or execute work without blocking the caller.

**Common Mistakes:**
> - Starting a goroutine without a way to stop it — always pass a `context.Context` or use a channel for shutdown signaling
> - Accessing shared data from multiple goroutines without synchronization — this causes race conditions (see the mutex section below)
> - Starting too many goroutines without backpressure — in Forge, the worker limits concurrent tasks with `maxSlots`

### 2.4 Channels: Communication Between Goroutines

**What it is:** A **channel** is a typed pipe that lets goroutines send and receive values. One goroutine puts a value in, another takes it out. Channels are Go's primary mechanism for goroutine communication.

**Analogy:** A pneumatic tube in a bank. The teller puts a capsule in one end, and it arrives at the other end. If the tube is full, the sender waits. If it is empty, the receiver waits.

**Where channels appear in Forge:**

**Task assignment channel** — `internal/scheduler/server.go:245`:
```go
assignCh := make(chan *forgepb.TaskAssignment, 10)
```
This is a **buffered channel** with capacity 10. The assigner puts task assignments in, and the RegisterWorker goroutine takes them out and sends them to the worker over the network. The buffer of 10 means up to 10 assignments can queue up before the sender blocks.

**Error channel** — `internal/scheduler/server.go:271`:
```go
errCh := make(chan error, 1)
go func() {
    for {
        hb, err := stream.Recv()
        if err != nil {
            errCh <- err
            return
        }
        // ... process heartbeat
    }
}()
```
This recv goroutine uses `errCh` to signal the main loop when the stream breaks. The main loop uses `select` to multiplex between the assignment channel and the error channel.

**Signal channel** — `cmd/scheduler/main.go:123`:
```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
sig := <-sigCh  // blocks until a signal arrives
```

**The select statement** — `internal/scheduler/server.go:291-301`:
```go
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
```
`select` lets you wait on multiple channels at once. Whichever channel has data first, that case runs.

> **Try This:** What happens if the `assignCh` buffer (size 10) fills up? Look at `internal/scheduler/server.go:362-367` — the `AssignTaskToWorker` function uses a `select` with a `default` case to return an error instead of blocking forever.

**When to use channels:** Use channels to pass data between goroutines, signal events, or implement fan-out/fan-in patterns. Prefer channels over shared memory when the communication is sequential (producer → consumer).

**Common Mistakes:**
> - Sending on a closed channel — this panics. Only the sender should close a channel
> - Forgetting to close channels — receivers may block forever waiting for more data
> - Using unbuffered channels when you need buffered — unbuffered channels block the sender until a receiver is ready

### 2.5 Mutex: Protecting Shared Data

**What it is:** A **mutex** (mutual exclusion) is a lock that ensures only one goroutine can access shared data at a time. Go provides `sync.Mutex` (exclusive lock) and `sync.RWMutex` (allows multiple readers OR one writer).

**Analogy:** A bathroom door with a lock. `Mutex` is a single-occupancy bathroom — one person at a time. `RWMutex` is a library reading room — many people can read simultaneously, but when someone wants to write on the whiteboard, everyone must leave until they're done.

```
RWMutex Timeline:
Time --->
          Reader1   Reader2   Writer    Reader3
          RLock()   RLock()
          reading   reading   [blocked - waiting for readers]
          RUnlock() RUnlock()
                              Lock()
                              writing   [blocked - waiting for writer]
                              Unlock()
                                        RLock()
                                        reading
                                        RUnlock()
```

**Where it appears in Forge:**

**TaskFSM** uses `sync.RWMutex` at `internal/raft/fsm.go:42-44`:
```go
type TaskFSM struct {
    mu    sync.RWMutex
    tasks map[string]*Task
}
```

- **Write lock** for `Apply()` (modifies tasks) at line 62: `f.mu.Lock()`
- **Read lock** for `GetTask()` (reads tasks) at line 184: `f.mu.RLock()`
- **Read lock** for `GetAllTasks()` at line 197: `f.mu.RLock()`
- **Read lock** for `Snapshot()` at line 154: `f.mu.RLock()`

**ForgeSchedulerServer** uses `sync.RWMutex` at `internal/scheduler/server.go:63`:
```go
mu      sync.RWMutex
workers map[string]*workerInfo
```
- **Write lock** when adding/removing workers (lines 247, 262, 99)
- **Read lock** when reading worker info (lines 354, 372, 64 in worker_tracker.go)

**atomic.Int32** for lock-free counting at `internal/worker/worker.go:27`:
```go
activeSlots   atomic.Int32
```
The worker uses an atomic integer to track how many tasks are running. `atomic.Int32` is faster than a mutex for simple counters because it uses hardware-level atomic operations.

**When to use which:**
- `sync.Mutex` — when every access modifies the data
- `sync.RWMutex` — when reads far outnumber writes (like the FSM where GetTask is called frequently but Apply is called less often)
- `atomic.Int32/Int64` — when you only need to increment/decrement a single number

**Common Mistakes:**
> - Forgetting to unlock — always use `defer mu.Unlock()` immediately after locking
> - Copying a mutex — never pass a struct containing a mutex by value. Use pointers (`*TaskFSM`)
> - Holding a lock while doing I/O — this can cause performance problems. Lock, copy data, unlock, then do I/O
> - Deadlocks — if goroutine A holds lock X and wants lock Y, while goroutine B holds lock Y and wants lock X, both wait forever

### 2.6 Context: Propagating Cancellation and Timeouts

**What it is:** `context.Context` is Go's mechanism for carrying deadlines, cancellation signals, and request-scoped values across goroutine boundaries. When a parent context is cancelled, all child contexts are cancelled too.

**Analogy:** A fire alarm in a building. When the alarm goes off (context cancelled), everyone on every floor (every goroutine holding that context) stops what they are doing and exits.

**Where it appears in Forge:**

**Root context with cancel** — `cmd/scheduler/main.go:110`:
```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
```
This is the parent context for all background goroutines. When the scheduler receives SIGINT, it calls `cancel()` at line 129, and all goroutines checking `ctx.Done()` stop.

**Per-task timeout** — `internal/worker/worker.go:197-200`:
```go
taskCtx := ctx
if assignment.GetTimeoutSeconds() > 0 {
    var cancel context.CancelFunc
    taskCtx, cancel = context.WithTimeout(ctx, time.Duration(assignment.GetTimeoutSeconds())*time.Second)
    defer cancel()
}
```
Each task gets its own context with a timeout. If the task takes too long, the context expires and the handler's `ctx.Done()` channel fires.

**Context-aware sleep** — `internal/worker/handlers/sleep.go:36-40`:
```go
select {
case <-time.After(time.Duration(p.Seconds) * time.Second):
case <-ctx.Done():
    return nil, ctx.Err()
}
```
Instead of using `time.Sleep()` (which cannot be cancelled), the handler uses `select` to race between the timer and the context's Done channel.

**Context-aware HTTP request** — `internal/worker/handlers/httpcheck.go:37`:
```go
req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
```
The HTTP request will be cancelled if the context expires.

**Stream context** — `internal/scheduler/server.go:416`:
```go
case <-stream.Context().Done():
    return stream.Context().Err()
```
gRPC streams carry a context. When the client disconnects, the stream's context is cancelled.

**When to use context:** Pass context as the first argument to every function that does I/O or runs concurrently. This is a Go convention — you will see it everywhere.

**Common Mistakes:**
> - Using `context.Background()` everywhere — this defeats the purpose. Always derive from a parent context
> - Storing context in a struct — pass it as a function parameter instead
> - Ignoring `ctx.Done()` in long-running operations — your function should check the context periodically

### 2.7 Error Handling

**What it is:** Go handles errors explicitly through return values rather than exceptions. Functions that can fail return an `error` as their last value. Callers must check it.

**Where it appears in Forge:**

**Error wrapping with context** — throughout the codebase:
```go
// internal/raft/node.go:31
store, err := raftboltdb.NewBoltStore(filepath.Join(dataDir, "raft.db"))
if err != nil {
    return nil, nil, fmt.Errorf("creating bolt store: %w", err)
}
```

The `%w` verb wraps the original error so callers can unwrap it later. The string prefix ("creating bolt store:") adds context about what was happening when the error occurred. This creates a chain: `"creating raft instance: creating bolt store: permission denied"`.

**Intentional ignore with blank identifier** — `internal/raft/fsm.go:248`:
```go
_ = sink.Cancel()
```
The `_` blank identifier explicitly ignores the return value. This is acceptable here because we are already handling an error (the marshal failed) and `Cancel()` is just cleanup.

**Never ignore errors in production code.** Every `err` in Forge is checked. Search the codebase — you will not find an unchecked error in production code.

**Common Mistakes:**
> - Using `_` to ignore errors — only do this when you truly cannot handle the error and have documented why
> - Not wrapping errors — a bare `return err` loses context about where the error occurred
> - Using `panic()` for recoverable errors — `panic` is for truly unrecoverable situations. Forge uses it only in `newTaskID()` when crypto/rand fails (which means the system is fundamentally broken)

### 2.8 sync.Once: Initialize Exactly Once

**What it is:** `sync.Once` ensures a piece of code runs exactly once, even if called from multiple goroutines simultaneously. It is perfect for lazy initialization.

**Where it appears in Forge:**

`internal/metrics/metrics.go:76-98`:
```go
var registerOnce sync.Once

func Register() {
    registerOnce.Do(func() {
        prometheus.MustRegister(
            TasksSubmitted,
            TasksCompleted,
            // ... all metrics
        )
    })
}
```

Both the scheduler (`cmd/scheduler/main.go:76`) and worker (`cmd/worker/main.go:46`) call `metrics.Register()`. Without `sync.Once`, calling `prometheus.MustRegister` twice with the same metric would panic. `sync.Once` guarantees the registration happens exactly once.

**When to use this pattern:** Any time you have initialization that must happen exactly once — database connection pools, configuration loading, or metric registration.

### 2.9 Defer: Cleanup That Always Runs

**What it is:** `defer` schedules a function call to run when the enclosing function returns, regardless of whether it returns normally or due to an error. Deferred calls execute in LIFO (last in, first out) order.

**Analogy:** Setting a reminder on your phone to turn off the oven. No matter what you do between now and leaving the kitchen, the reminder fires.

**Where it appears in Forge:**

**Unlock after lock** — `internal/raft/fsm.go:62-63`:
```go
f.mu.Lock()
defer f.mu.Unlock()
```
This pattern appears throughout the codebase. By deferring the unlock immediately after locking, you guarantee the lock is released even if the function panics.

**Close connections** — `internal/worker/worker.go:91`:
```go
defer conn.Close()
```

**Cancel contexts** — `cmd/scheduler/main.go:110-111`:
```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
```

**Stop tickers** — `internal/scheduler/assigner.go:19-20`:
```go
ticker := time.NewTicker(s.config.AssignerTick)
defer ticker.Stop()
```

**Worker cleanup on disconnect** — `internal/scheduler/server.go:261-269`:
```go
defer func() {
    s.mu.Lock()
    delete(s.workers, workerID)
    s.mu.Unlock()
    close(assignCh)
    metrics.ActiveWorkers.Dec()
    s.AddEvent("worker_disconnected", fmt.Sprintf("Worker %s disconnected", workerID))
}()
```
This deferred function cleans up all worker state when the stream ends, whether the worker disconnected normally or crashed.

**Slot tracking** — `internal/worker/worker.go:183-184`:
```go
w.activeSlots.Add(1)
defer w.activeSlots.Add(-1)
```
Increment the active slot count when a task starts, decrement when it finishes. The defer guarantees the decrement happens even if the task panics.

**Common Mistakes:**
> - Deferring in a loop — each iteration adds a new deferred call. They all pile up and only run when the function returns, not at the end of each loop iteration
> - Expecting defer to run immediately — it runs when the *function* returns, not when the block ends

### 2.10 Table-Driven Tests

**What it is:** A pattern where you define test cases as a slice of structs, then loop through them. Each struct contains the inputs and expected outputs for one test case.

**Where it appears in Forge:**

`internal/raft/fsm_test.go` uses this pattern extensively. Here is a simplified example from the fail task test:

```go
tests := []struct {
    name           string
    maxRetries     int
    failCount      int
    expectedStatus string
}{
    {name: "retry when under max", maxRetries: 3, failCount: 1, expectedStatus: "retrying"},
    {name: "dead letter at max",  maxRetries: 3, failCount: 3, expectedStatus: "dead_letter"},
    {name: "zero retries",        maxRetries: 0, failCount: 1, expectedStatus: "dead_letter"},
}

for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        // ... test logic using tt.maxRetries, tt.failCount, tt.expectedStatus
    })
}
```

The handler tests at `internal/worker/handlers/handlers_test.go` follow the same pattern for testing all three handlers.

**When to use this pattern:** Whenever you have multiple test cases that follow the same structure. It eliminates copy-paste and makes it trivial to add new test cases.

**Common Mistakes:**
> - Forgetting `t.Run()` with the test name — without it, you cannot tell which case failed
> - Sharing state between test cases — each case should be independent
> - Not using `t.Helper()` in helper functions — without it, error messages point to the helper instead of the actual test line

### 2.11 Build Tags

**What it is:** Build tags (also called build constraints) are special comments that tell Go to only compile a file under certain conditions. Forge uses them to separate integration tests from unit tests.

**Where it appears in Forge:**

`test/integration/cluster_test.go:1`:
```go
//go:build integration
```

This means the file is only compiled when you pass `-tags=integration` to the Go toolchain. Running `go test ./...` skips these files. Running `go test -tags=integration ./test/...` includes them.

Look at the Makefile:
```make
test:
    go test -race ./...              # unit tests only

test-integration:
    go test -race -tags=integration ./test/...  # integration + chaos tests
```

**When to use this pattern:** Separate slow, complex, or environment-dependent tests from fast unit tests. Unit tests should run in under a second. Integration tests might take minutes.

### 2.12 Package Organization

**What it is:** Go projects use a conventional directory structure to organize code. Forge follows the standard Go project layout.

```
forge/
├── cmd/           ← Binary entry points (one per subdirectory)
│   ├── scheduler/ ← go build ./cmd/scheduler produces "scheduler" binary
│   ├── worker/    ← go build ./cmd/worker produces "worker" binary
│   └── forgectl/  ← go build ./cmd/forgectl produces "forgectl" binary
├── internal/      ← Private packages — CANNOT be imported by external code
│   ├── raft/      ← Raft FSM and node setup
│   ├── scheduler/ ← gRPC server and background goroutines
│   ├── worker/    ← Worker client and task execution
│   ├── metrics/   ← Prometheus metric definitions
│   ├── dashboard/ ← Bubbletea TUI
│   └── proto/     ← Protobuf definitions and generated code
├── test/          ← Integration and chaos tests
└── deploy/        ← Docker and deployment configuration
```

The `internal/` directory is special in Go. Code inside `internal/` can only be imported by code in the parent module. External projects cannot import `internal/raft` — it is private to Forge.

**Import alias for collision avoidance** — the project's `internal/raft` package would collide with `github.com/hashicorp/raft`. Forge solves this with aliases:

```go
import (
    hcraft "github.com/hashicorp/raft"                    // external raft library
    raftpkg "github.com/Ritpra93/forge/internal/raft"     // our raft package
)
```

### 2.13 io.Reader, io.Writer, io.Closer

**What it is:** Go's `io` package defines tiny interfaces that represent streams of data. `io.Reader` has one method: `Read(p []byte) (n int, err error)`. These small interfaces are the backbone of Go's I/O system.

**Where it appears in Forge:**

**FSM Restore** — `internal/raft/fsm.go:167`:
```go
func (f *TaskFSM) Restore(reader io.ReadCloser) error {
    defer reader.Close()
    var tasks map[string]*Task
    if err := json.NewDecoder(reader).Decode(&tasks); err != nil {
        return fmt.Errorf("decoding snapshot: %w", err)
    }
    // ...
}
```

The `reader` parameter is `io.ReadCloser` (which combines `io.Reader` and `io.Closer`). The Raft library passes in the snapshot data through this interface. The FSM does not care whether the data comes from a file, a network connection, or an in-memory buffer — it just reads from the interface.

**Snapshot Persist** — `internal/raft/fsm.go:245`:
```go
func (s *taskSnapshot) Persist(sink hcraft.SnapshotSink) error {
    data, err := json.Marshal(s.tasks)
    if err != nil {
        _ = sink.Cancel()
        return fmt.Errorf("marshaling snapshot: %w", err)
    }
    if _, err := sink.Write(data); err != nil { // sink implements io.Writer
```

`SnapshotSink` implements `io.WriteCloser`. The snapshot writes serialized JSON to it.

**When to use these interfaces:** Accept `io.Reader` or `io.Writer` in your function signatures whenever possible. This makes your code work with files, network connections, buffers, and anything else that implements the interface.

### 2.14 Type Assertions and Type Switches

**What it is:** A type assertion extracts the concrete type from an interface value. A type switch does this for multiple types.

**Where it appears in Forge:**

**Type assertion** — `internal/scheduler/server.go:170`:
```go
if resp := future.Response(); resp != nil {
    if err, ok := resp.(error); ok {
        return err
    }
}
```
The Raft `Apply` function returns `interface{}`. This code checks if the response is actually an `error` type, and if so, returns it.

**Type switch** — `internal/dashboard/dashboard.go:46`:
```go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        // handle keyboard input
    case tea.WindowSizeMsg:
        // handle window resize
    case dashboardData:
        // handle data from gRPC fetch
    case tickMsg:
        // handle auto-refresh tick
    }
    return m, nil
}
```

Bubbletea's `Update` receives messages as the `tea.Msg` interface. The type switch determines what kind of message arrived and handles each type differently.

### 2.15 Graceful Shutdown

**What it is:** When a process receives a termination signal (SIGINT from Ctrl+C, or SIGTERM from Docker/Kubernetes), it should stop accepting new work, finish in-progress work, and clean up resources before exiting.

**Where it appears in Forge:**

**Scheduler shutdown** — `cmd/scheduler/main.go:122-136`:
```go
// Wait for shutdown signal.
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
sig := <-sigCh

logger.Info("received shutdown signal", "signal", sig)

cancel()                          // 1. Cancel context → stops background goroutines
grpcServer.GracefulStop()         // 2. Stop accepting new RPCs, finish in-flight ones

if err := r.Shutdown().Error(); err != nil {  // 3. Shut down Raft cleanly
    logger.Error("shutting down raft", "error", err)
}
```

**Worker shutdown** — `cmd/worker/main.go:74-81`:
```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

go func() {
    sig := <-sigCh
    logger.Info("received shutdown signal", "signal", sig)
    cancel()  // cancels the context → heartbeat loop stops → stream closes
}()
```

The shutdown order matters:
1. Cancel the context (stops background goroutines)
2. Gracefully stop the gRPC server (finishes in-flight RPCs)
3. Shut down Raft (flushes pending log entries)

**When to use this pattern:** Every production service needs graceful shutdown. Without it, in-flight requests are dropped and data can be lost.

**Common Mistakes:**
> - Calling `os.Exit()` instead of graceful shutdown — this kills everything immediately
> - Not propagating the cancel — if you cancel the context but a goroutine does not check `ctx.Done()`, it keeps running
> - Wrong shutdown order — stop accepting new work first, then drain existing work

---

## Section 3: Distributed Systems Concepts

This section explains the distributed systems concepts that make Forge work. These are the same concepts used by databases like CockroachDB, orchestrators like Kubernetes, and service meshes like Consul.

### 3.1 Why Do We Need Consensus?

**The problem:** Forge runs 3 scheduler nodes. Each one needs to know the state of every task. If you just wrote the state to one node, and that node crashed, all state would be lost.

**Analogy:** Imagine three bank managers each keeping a copy of every account balance. A customer deposits $100. If only one manager writes it down and then goes home sick, the other two managers have the wrong balance. You need a protocol where at least two managers agree on every change before it is considered "done."

**How Forge solves it:** Forge uses **Raft consensus** (via the hashicorp/raft library). Every state change goes through a voting process:

1. The **leader** receives the change request
2. The leader sends the change to all followers
3. Once a **majority** (2 out of 3) acknowledge it, the change is committed
4. The committed change is applied to the FSM on all nodes

```
  The Consensus Problem:

  Without consensus:          With consensus (Raft):

  Node 1: balance = $200      Node 1 (leader): balance = $200
  Node 2: balance = $100      Node 2 (follower): balance = $200
  Node 3: balance = $150      Node 3 (follower): balance = $200
        (inconsistent!)                  (all agree!)
```

### 3.2 Raft Leader Election

**The problem:** One node must coordinate changes. But which one? And what happens when it crashes?

**How it works:**

```
  RAFT LEADER ELECTION

  Phase 1: All nodes start as followers

  Time ────────────────────────────────────────────>

  Node 1: [Follower] ─── election timeout! ──> [Candidate]
  Node 2: [Follower] ──────────────────────────────────────
  Node 3: [Follower] ──────────────────────────────────────

  Phase 2: Candidate requests votes

  Node 1: [Candidate] ──── "Vote for me!" ────> Node 2
                     ──── "Vote for me!" ────> Node 3

  Phase 3: Nodes vote (each node votes only once per term)

  Node 1: votes for itself (1 vote)
  Node 2: ──── "Yes, I vote for you" ────> Node 1 (2 votes)
  Node 3: ──── "Yes, I vote for you" ────> Node 1 (3 votes)

  Phase 4: Candidate with majority becomes leader

  Node 1: [LEADER]   (got 3/3 votes, only needed 2)
  Node 2: [Follower]
  Node 3: [Follower]
```

**Where it appears in Forge:**

`internal/raft/node.go:18-24` configures the election timeouts:
```go
config.HeartbeatTimeout = 1 * time.Second
config.ElectionTimeout = 1 * time.Second
config.LeaderLeaseTimeout = 500 * time.Millisecond
```

These are tuned for demo environments. In production, you would use longer timeouts (e.g., 5 seconds) to avoid unnecessary elections on slow networks.

The bootstrap process at `cmd/scheduler/main.go:65-73` tells the first node to form a cluster:
```go
if bootstrap {
    servers := buildClusterConfig(nodeID, raftBind, peers)
    f := r.BootstrapCluster(hcraft.Configuration{Servers: servers})
}
```

**Real-world analogy:** A group of friends deciding where to eat. If no one speaks up, eventually someone (the one whose patience timer runs out first) says "How about pizza?" If a majority agrees, it is decided. If the pizza-chooser disappears, someone else's timer runs out and proposes a new restaurant.

**Real-world usage:** etcd (used by Kubernetes), Consul (used by HashiCorp Nomad), CockroachDB, TiKV.

### 3.3 Log Replication

**The problem:** Once you have a leader, how do changes spread to all nodes?

**How it works:**

```
  LOG REPLICATION

  Client sends: SubmitTask("fibonacci")

  Step 1: Leader appends to its own log
  ┌──────────────────────────────────────────────┐
  │ Leader Log:  [create_task: task-abc]          │
  │ Follower 2:  [empty]                         │
  │ Follower 3:  [empty]                         │
  └──────────────────────────────────────────────┘

  Step 2: Leader sends log entry to followers
  ┌──────────────────────────────────────────────┐
  │ Leader ─── AppendEntries ──> Follower 2      │
  │ Leader ─── AppendEntries ──> Follower 3      │
  └──────────────────────────────────────────────┘

  Step 3: Followers acknowledge
  ┌──────────────────────────────────────────────┐
  │ Follower 2 ─── ACK ──> Leader               │
  │ Follower 3 ─── ACK ──> Leader               │
  │                                              │
  │ Leader: 2/3 acknowledged = majority!         │
  │         Entry is COMMITTED                   │
  └──────────────────────────────────────────────┘

  Step 4: All nodes apply the committed entry to their FSM
  ┌──────────────────────────────────────────────┐
  │ Leader FSM:     tasks["task-abc"] = pending  │
  │ Follower 2 FSM: tasks["task-abc"] = pending  │
  │ Follower 3 FSM: tasks["task-abc"] = pending  │
  │                                              │
  │ All three nodes now have identical state!     │
  └──────────────────────────────────────────────┘
```

**Where it appears in Forge:**

The apply call at `internal/scheduler/server.go:164`:
```go
future := s.raft.Apply(data, defaultApplyTimeout)
```

This sends the JSON-encoded command through the Raft log. The `future.Error()` call blocks until the majority has acknowledged. Then `FSM.Apply()` runs on every node.

The commands are JSON-encoded in the Raft log (`internal/raft/fsm.go:56-60`):
```go
func (f *TaskFSM) Apply(log *hcraft.Log) interface{} {
    var cmd Command
    if err := json.Unmarshal(log.Data, &cmd); err != nil {
        return fmt.Errorf("unmarshaling command: %w", err)
    }
    // ...
}
```

### 3.4 The FSM (Finite State Machine) Pattern

**What it is:** A **finite state machine** is a system that can be in one of a fixed number of states, and transitions between states based on inputs (commands).

**Analogy:** A traffic light. It can be in three states: red, yellow, green. It transitions between them in a defined order. It cannot be in two states at once, and the transition rules are fixed.

**Where it appears in Forge:**

The task FSM has 7 states and 5 transition commands:

```
States: pending, scheduled, running, completed, failed, retrying, dead_letter

Commands and their effects:
  create_task:   → pending
  assign_task:   pending → running
  complete_task: running → completed
  fail_task:     running → retrying (if retries remaining)
                 running → dead_letter (if max retries exceeded)
  reassign_task: running → pending (worker died)
                 retrying → pending (retry backoff elapsed)
```

The FSM is implemented at `internal/raft/fsm.go:56-78`. The key property: **Apply must be deterministic**. Given the same log entry, every node must produce the same result. This is why the FSM uses the command's data and the current state — never randomness or wall clock for decisions.

> **Try This:** What happens if you call `assign_task` on a task that does not exist? Look at `fsm.go:94-96` — it returns an error. This is important for correctness.

### 3.5 Fault Tolerance and Failure Detection

**The problem:** Workers can crash at any time. The scheduler needs to detect this and reassign their tasks.

**How Forge solves it with heartbeats:**

```
  HEARTBEAT-BASED FAILURE DETECTION

  Worker sends heartbeat every 3 seconds:

  Worker:     |--HB--|--HB--|--HB--|           [CRASH!]
  Time:       0s     3s     6s     9s          10s

  Scheduler checks every 3s (TrackerTick):

  Check:      |---------|---------|---------|---------|
  Time:       0s        3s        6s        9s        12s

  At 9s:  time since last HB = 9s - 6s = 3s
          3s < HeartbeatDead(9s)? YES → still alive

  At 12s: time since last HB = 12s - 6s = 6s
          6s < HeartbeatDead(9s)? YES → still alive

  At 15s: time since last HB = 15s - 6s = 9s
          9s >= HeartbeatDead(9s)? YES → DEAD!
          → Reassign all running tasks from this worker
```

**Where it appears in Forge:**

**Heartbeat sending** — `internal/worker/worker.go:157-178`:
```go
func (w *Worker) heartbeatLoop(ctx context.Context, stream ...) {
    ticker := time.NewTicker(heartbeatInterval) // 3 seconds
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            stream.Send(&forgepb.WorkerHeartbeat{...})
        }
    }
}
```

**Heartbeat receiving** — `internal/scheduler/server.go:272-289`:
The recv goroutine updates `w.lastHeartbeat = time.Now()` for each heartbeat.

**Dead worker detection** — `internal/scheduler/worker_tracker.go:63-75`:
```go
for id, w := range s.workers {
    if now.Sub(w.lastHeartbeat) > s.config.HeartbeatDead {
        deadWorkerIDs = append(deadWorkerIDs, id)
    }
}
```

**Task reassignment** — `internal/scheduler/worker_tracker.go:84-108`:
```go
func (s *ForgeSchedulerServer) handleDeadWorker(workerID string) {
    tasks := s.fsm.GetTasksByStatusAndWorker("running", workerID)
    for _, task := range tasks {
        s.applyCommand(raftpkg.Command{Type: "reassign_task", TaskID: task.ID})
    }
    delete(s.workers, workerID)
}
```

### 3.6 Orphan Detection After Leader Failover

**The problem:** When the leader crashes and a new leader is elected, the new leader does not know about the workers that were connected to the old leader. Tasks assigned to those workers are "orphaned."

**How Forge solves it:**

The new leader waits a **grace period** (equal to `HeartbeatDead`) for workers to reconnect. After the grace period, any "running" task assigned to an unknown worker is reassigned.

`internal/scheduler/worker_tracker.go:77-81`:
```go
// Phase 2: detect orphaned tasks
// Only after a grace period so workers have time to reconnect after failover.
if now.Sub(leaderSince) > s.config.HeartbeatDead {
    s.handleOrphanedTasks()
}
```

### 3.7 At-Least-Once Delivery

**What it is:** There are three delivery guarantees in distributed systems:
- **At-most-once:** Send once, never retry. May lose messages.
- **At-least-once:** Retry until acknowledged. May deliver duplicates.
- **Exactly-once:** Each message delivered exactly once. Very hard to implement.

```
  DELIVERY GUARANTEES

  At-most-once:     Client ──msg──> Server
                    "I sent it. Not my problem anymore."
                    Simple. May lose data.

  At-least-once:    Client ──msg──> Server
                    Client ──msg──> Server  (retry)
                    Client ──msg──> Server  (retry)
                    Server: "I got it 3 times!"
                    Safe. May have duplicates.

  Exactly-once:     Client ──msg──> Server
                    Server: "Got exactly 1 copy"
                    Ideal. Very hard to implement.
```

**Forge uses at-least-once delivery.** If a worker crashes mid-task, the task is reassigned to another worker, which means the task might run twice. This is safe because the handlers are **idempotent** — running them twice produces the same result.

- `SleepHandler` — sleeping twice is harmless
- `FibonacciHandler` — computing fibonacci(42) twice gives the same answer
- `HTTPCheckHandler` — checking a URL twice is harmless

**Real-world analogy:** A restaurant kitchen where the chef writes an order and hands it to a cook. If the cook drops the order slip, the chef writes a new one and hands it to another cook. The customer might get two burgers — but that is better than getting zero burgers.

### 3.8 Idempotency and Why Task Handlers Need It

**What it is:** An operation is **idempotent** if performing it multiple times has the same effect as performing it once.

**Why it matters:** Since Forge uses at-least-once delivery, handlers must be idempotent. If a task is assigned twice (once to a crashed worker, once to a new worker), both executions must produce the same result.

Examples:
- `fibonacci(42)` = always 267914296 (idempotent)
- `sleep(5)` = always sleeps 5 seconds (idempotent)
- `INSERT INTO users (id, name) VALUES (1, 'Alice')` = NOT idempotent (would fail on second insert or create duplicates)

> **Try This:** If you needed to add a `DatabaseInsertHandler`, how would you make it idempotent? (Hint: use `INSERT ... ON CONFLICT DO NOTHING` or check if the record exists first)

### 3.9 Quorum: Why 3 Nodes?

**What it is:** A **quorum** is the minimum number of nodes that must agree for a change to be committed. For Raft, quorum = (N/2) + 1.

```
  QUORUM MATH

  Nodes    Quorum    Can Tolerate     Notes
  ─────    ──────    ─────────────    ────────────────────────
    1         1       0 failures      No fault tolerance at all
    2         2       0 failures      WORSE than 1 (need both!)
    3         2       1 failure       Sweet spot for small clusters
    4         3       1 failure       Same tolerance as 3 (wasteful)
    5         3       2 failures      For critical production systems
    6         4       2 failures      Same tolerance as 5 (wasteful)
    7         4       3 failures      Rarely needed

  KEY INSIGHT: Even numbers waste a node.
  3 nodes tolerates 1 failure.
  4 nodes ALSO tolerates only 1 failure.
  You added a node for nothing!

  ALWAYS USE ODD NUMBERS: 3, 5, 7
```

**Where it appears in Forge:**

Docker-compose defines 3 scheduler nodes (`deploy/docker-compose.yml`). The bootstrap configures all 3 as voters (`cmd/scheduler/main.go:160-188`). With 3 nodes and a quorum of 2, Forge can survive 1 node failure.

### 3.10 Split Brain and How Raft Prevents It

**What it is:** **Split brain** happens when a network partition causes two groups of nodes to each think they are the authoritative cluster. Both accept writes independently, and their state diverges.

```
  SPLIT BRAIN SCENARIO (without Raft)

  Network partition splits the cluster:

  [Node 1] [Node 2]  |  [Node 3]
                      |
  Both sides accept   |  This side also
  writes!             |  accepts writes!
                      |
  State diverges!     |  Different state!

  When partition heals: CONFLICT! Which state is correct?
```

**Raft prevents this** because writes require a quorum. With 3 nodes:
- The side with 2 nodes has a quorum and can elect a leader
- The side with 1 node cannot get a quorum and stops accepting writes
- When the partition heals, the lone node catches up from the majority

This is why the `requireLeader()` check at `server.go:148` is critical. Only the leader (which must have a quorum) can accept writes.

### 3.11 Snapshots and Log Compaction

**The problem:** The Raft log grows forever. If you have applied 1 million task commands, you would need to replay all 1 million on startup. That is too slow.

**The solution:** Take periodic snapshots of the FSM state. After a snapshot, old log entries can be discarded.

```
  LOG COMPACTION

  Before snapshot:
  Log: [cmd1] [cmd2] [cmd3] [cmd4] [cmd5] [cmd6] [cmd7] [cmd8]
  FSM state: {task-1: completed, task-2: running, task-3: pending}

  After snapshot at entry 5:
  Snapshot: {task-1: completed, task-2: running}
  Log: [cmd6] [cmd7] [cmd8]  (entries 1-5 can be discarded)

  On restart:
  1. Load snapshot → FSM has state as of entry 5
  2. Replay entries 6-8 → FSM is fully up-to-date
  Much faster than replaying all 8 entries!
```

**Where it appears in Forge:**

**Creating a snapshot** — `internal/raft/fsm.go:153-163`:
```go
func (f *TaskFSM) Snapshot() (hcraft.FSMSnapshot, error) {
    f.mu.RLock()
    defer f.mu.RUnlock()
    tasks := make(map[string]*Task, len(f.tasks))
    for k, v := range f.tasks {
        copied := *v       // DEEP COPY — critical!
        tasks[k] = &copied
    }
    return &taskSnapshot{tasks: tasks}, nil
}
```

The deep copy (`copied := *v`) is critical. `Apply()` will be called concurrently with `Persist()`, so the snapshot must capture an independent copy of the state.

**Persisting a snapshot** — `internal/raft/fsm.go:245-257`:
```go
func (s *taskSnapshot) Persist(sink hcraft.SnapshotSink) error {
    data, err := json.Marshal(s.tasks)
    if _, err := sink.Write(data); err != nil { ... }
    return sink.Close()
}
```

**Restoring from a snapshot** — `internal/raft/fsm.go:167-180`:
```go
func (f *TaskFSM) Restore(reader io.ReadCloser) error {
    defer reader.Close()
    var tasks map[string]*Task
    json.NewDecoder(reader).Decode(&tasks)
    f.mu.Lock()
    f.tasks = tasks
    f.mu.Unlock()
    return nil
}
```

### 3.12 Exponential Backoff for Retries

**The problem:** When a task fails, you want to retry it — but not immediately. If the failure is caused by a temporary condition (e.g., a service is down), hammering it with retries makes things worse.

**The solution:** Wait longer between each retry attempt.

```
  EXPONENTIAL BACKOFF

  Retry 1: wait 1 second     (base)
  Retry 2: wait 2 seconds    (base * 2)
  Retry 3: wait 4 seconds    (base * 4)
  Retry 4: wait 8 seconds    (base * 8)
  Retry 5: wait 16 seconds   (base * 16)
  Retry 6: wait 32 seconds   (base * 32)
  Retry 7: wait 60 seconds   (capped at max)

  Formula: min(maxBackoff, baseBackoff * 2^(retryCount-1))
```

**Where it appears in Forge:**

`internal/scheduler/retry.go:69-81`:
```go
func computeBackoff(retryCount int) time.Duration {
    if retryCount <= 0 {
        return baseBackoff  // 1 second
    }
    backoff := baseBackoff
    for i := 1; i < retryCount; i++ {
        backoff *= 2
        if backoff >= maxBackoff {
            return maxBackoff  // 60 seconds
        }
    }
    return backoff
}
```

The retry scheduler at `retry.go:34-65` checks retrying tasks and transitions them back to "pending" when the backoff has elapsed.

**Real-world analogy:** Calling a friend who is not answering. You call, wait 1 minute, call again, wait 2 minutes, call again, wait 4 minutes. You do not call 100 times in a row.

---

## Section 4: gRPC and Protocol Buffers

### 4.1 What Problem Does gRPC Solve?

**The problem:** Services need to communicate over the network. REST/JSON is the most common approach, but it has limitations:
- No type safety — you can send any JSON, and the server discovers errors at runtime
- No streaming — REST is request-response only
- JSON is slow to serialize/deserialize compared to binary formats

**gRPC solves these problems:**
- **Type safety** — both client and server are generated from the same `.proto` file
- **Streaming** — supports unary, server-streaming, client-streaming, and bidirectional streaming
- **Efficient** — uses Protocol Buffers (binary format), much faster than JSON
- **Code generation** — the client/server code is auto-generated

### 4.2 The Proto File: Forge's API Definition

The entire API is defined in `internal/proto/forgepb/forge.proto`. Let's walk through it.

**The service definition** declares all RPCs:
```protobuf
service ForgeScheduler {
  rpc SubmitTask(TaskRequest) returns (TaskResponse);
  rpc GetTaskStatus(TaskStatusRequest) returns (TaskStatusResponse);
  rpc RegisterWorker(stream WorkerHeartbeat) returns (stream TaskAssignment);
  rpc ReportTaskResult(TaskResult) returns (TaskResultAck);
  rpc WatchTask(WatchTaskRequest) returns (stream TaskStatusResponse);
  rpc GetClusterInfo(ClusterInfoRequest) returns (ClusterInfoResponse);
  rpc ListTasks(ListTasksRequest) returns (ListTasksResponse);
  rpc GetDashboardData(DashboardDataRequest) returns (DashboardDataResponse);
}
```

### 4.3 RPC Types Used in Forge

```
  RPC TYPE COMPARISON

  ┌─────────────────────┬──────────────────────────────────────────┐
  │ Type                │ Forge RPCs                               │
  ├─────────────────────┼──────────────────────────────────────────┤
  │ Unary               │ SubmitTask, GetTaskStatus,               │
  │ (req → resp)        │ ReportTaskResult, GetClusterInfo,        │
  │                     │ ListTasks, GetDashboardData              │
  │                     │                                          │
  │ Server-streaming    │ WatchTask                                │
  │ (req → stream resp) │ Client sends task ID, server streams     │
  │                     │ status updates until terminal state      │
  │                     │                                          │
  │ Bidi-streaming      │ RegisterWorker                           │
  │ (stream ↔ stream)   │ Worker sends heartbeats, scheduler       │
  │                     │ sends task assignments. Both directions  │
  │                     │ are independent and concurrent.          │
  └─────────────────────┴──────────────────────────────────────────┘
```

**Unary RPC** — request in, response out:
```
  Client                    Server
    |                         |
    |──── TaskRequest ─────> |
    |                         | (process)
    | <─── TaskResponse ──── |
    |                         |
```

**Server-streaming RPC (WatchTask)** — one request, multiple responses:
```
  Client                    Server
    |                         |
    |── WatchTaskRequest ──> |
    |                         |
    | <─ TaskStatusResponse ─ | (status: pending)
    | <─ TaskStatusResponse ─ | (status: running)
    | <─ TaskStatusResponse ─ | (status: completed)
    | <──── EOF ───────────── | (stream ends)
    |                         |
```

**Bidirectional streaming (RegisterWorker)** — both sides send independently:
```
  Worker (Client)                Scheduler (Server)
    |                                  |
    |── WorkerHeartbeat (initial) ──> |  register worker
    |                                  |
    |── WorkerHeartbeat (periodic) ─> |  update lastHeartbeat
    |                                  |
    |                                  |  assigner finds pending task
    | <───── TaskAssignment ───────── |  assign to this worker
    |                                  |
    |── WorkerHeartbeat (periodic) ─> |  update slots
    |                                  |
    | <───── TaskAssignment ───────── |  another task
    |                                  |
    |── WorkerHeartbeat (periodic) ─> |
    |                                  |
    |   [worker crashes / disconnects] |  cleanup in defer
```

### 4.4 How Proto Files Become Go Code

The pipeline:

```
  forge.proto
       |
       v
  protoc compiler
  (with --go_out and --go-grpc_out plugins)
       |
       ├──> forge.pb.go        (message types: TaskRequest, TaskResponse, etc.)
       └──> forge_grpc.pb.go   (service interfaces and client/server stubs)
```

The Makefile command (`make proto`):
```bash
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       internal/proto/forgepb/forge.proto
```

**Never edit generated files.** They are regenerated every time you run `make proto`.

### 4.5 The Unimplemented Server Pattern

The generated code includes `UnimplementedForgeSchedulerServer`, which returns "unimplemented" errors for every RPC. By embedding it (as shown in Section 2.2), new RPCs automatically have a safe default.

### 4.6 gRPC Error Codes

gRPC uses numeric status codes (like HTTP status codes but different). Forge uses:

| Code | Meaning | Where Used |
|------|---------|------------|
| `codes.FailedPrecondition` | Not the leader | `server.go:151` |
| `codes.NotFound` | Task not found | `server.go:222` |
| `codes.Internal` | Server error | `server.go:195, 205, 241, 295, 325` |

The CLI parses error codes at `cmd/forgectl/submit.go:93-104`:
```go
func extractLeaderAddress(err error) string {
    st, ok := status.FromError(err)
    if !ok || st.Code() != codes.FailedPrecondition {
        return ""
    }
    msg := st.Message()
    const prefix = "not the leader; current leader is "
    if len(msg) > len(prefix) {
        return msg[len(prefix):]
    }
    return ""
}
```

This is how the CLI automatically redirects to the leader.

> **Try This:** What gRPC error code would you use if a worker tries to submit a task type that does not exist? (Hint: look at gRPC's `codes.InvalidArgument`)

---

## Section 5: Observability

### 5.1 What Observability Means

**Observability** is the ability to understand what is happening inside your system by looking at its outputs. In Forge, observability means answering questions like:
- How many tasks per second are we processing?
- Are any workers down?
- How long do tasks take to complete?
- Is the leader election happening too often?

Forge uses **Prometheus** (metrics collection) and **Grafana** (dashboards).

### 5.2 The Metrics Pipeline

```
  THE METRICS PIPELINE

  ┌─────────────────┐     scrape every      ┌─────────────────┐
  │   Scheduler-1    │     15 seconds         │                 │
  │  :9090/metrics   │ ◄──────────────────── │                 │
  ├─────────────────┤                        │   Prometheus    │
  │   Scheduler-2    │ ◄──────────────────── │   (time-series  │     query
  │  :9090/metrics   │                        │    database)    │ ◄──────────
  ├─────────────────┤                        │                 │            │
  │   Scheduler-3    │ ◄──────────────────── │   :9099         │    ┌───────┴──────┐
  │  :9090/metrics   │                        └─────────────────┘    │   Grafana    │
  ├─────────────────┤                                               │  (dashboards │
  │   Worker-1       │ ◄──────────────────── (same Prometheus)      │   & graphs)  │
  │  :9091/metrics   │                                               │   :3000      │
  ├─────────────────┤                                               └──────────────┘
  │   Worker-2       │ ◄────────────────────
  │  :9091/metrics   │
  └─────────────────┘

  Pull model: Prometheus PULLS from services.
  Services do NOT push to Prometheus.
```

**How it works:**
1. Each Forge process exposes a `/metrics` HTTP endpoint
2. Prometheus scrapes (pulls) these endpoints every 15 seconds
3. Prometheus stores the data in a time-series database
4. Grafana queries Prometheus and renders graphs

### 5.3 Metric Types with Real Examples

Forge defines all metrics in `internal/metrics/metrics.go`:

**Counter** — a value that only goes up (like a car's odometer):
```go
// metrics.go:11-14
TasksSubmitted = prometheus.NewCounter(prometheus.CounterOpts{
    Name: "forge_tasks_submitted_total",
    Help: "Total number of tasks submitted.",
})
```
Incremented at `server.go:208`: `metrics.TasksSubmitted.Inc()`

**Gauge** — a value that goes up and down (like a speedometer):
```go
// metrics.go:32-35
ActiveWorkers = prometheus.NewGauge(prometheus.GaugeOpts{
    Name: "forge_active_workers",
    Help: "Number of currently connected workers.",
})
```
Incremented when worker connects (`server.go:257`), decremented when worker disconnects (`server.go:266`).

**Histogram** — distributes values into buckets (like sorting mail by weight class):
```go
// metrics.go:27-31
TaskDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
    Name:    "forge_task_duration_seconds",
    Help:    "Time from task submission to completion.",
    Buckets: prometheus.DefBuckets,
})
```
Records the duration at `server.go:332`: `metrics.TaskDuration.Observe(duration.Seconds())`

Histograms automatically give you percentiles (P50, P95, P99) in Prometheus queries.

### 5.4 Metric Registration with sync.Once

`internal/metrics/metrics.go:76-98`:
```go
var registerOnce sync.Once

func Register() {
    registerOnce.Do(func() {
        prometheus.MustRegister(
            TasksSubmitted, TasksCompleted, TasksFailed, TasksDeadLettered,
            TaskDuration, ActiveWorkers, PendingQueueDepth,
            RaftIsLeader, RaftCommitIndex, RaftElectionTotal,
            WorkerHeartbeatLatency,
            WorkerTasksExecuted, WorkerTaskExecution, WorkerAvailableSlots,
        )
    })
}
```

Both `cmd/scheduler/main.go:76` and `cmd/worker/main.go:46` call `Register()`. The `sync.Once` ensures `MustRegister` only runs once — calling it twice would panic.

### 5.5 Prometheus Scrape Configuration

`deploy/prometheus.yml`:
```yaml
scrape_configs:
  - job_name: 'forge-scheduler'
    static_configs:
      - targets: ['scheduler-1:9090', 'scheduler-2:9090', 'scheduler-3:9090']

  - job_name: 'forge-worker'
    static_configs:
      - targets: ['worker-1:9091', 'worker-2:9091']
```

### 5.6 Grafana Dashboards

Forge includes 4 Grafana dashboards in `deploy/grafana/dashboards/`:

| Dashboard | File | Purpose |
|-----------|------|---------|
| Cluster Overview | `cluster_overview.json` | Leader status, elections, active workers |
| Task Flow | `task_flow.json` | Tasks/sec, duration percentiles, pending depth |
| Worker Health | `worker_health.json` | Available slots, execution time, heartbeat latency |
| Chaos Demo | `chaos_demo.json` | Real-time view during failure injection tests |

**Naming convention for metrics:** Always use a prefix (`forge_`), use snake_case, and end counters with `_total`. This is the Prometheus convention.

**Common Mistakes:**
> - Using too many labels — high-cardinality labels (like task ID) create millions of time series and crash Prometheus
> - Using counters where you should use gauges — counters only go up. For "current active workers", use a gauge
> - Not adding `_total` suffix to counters — this is a Prometheus naming convention

---

## Section 6: Docker and Deployment

### 6.1 What Containers Are

**Analogy:** A shipping container. You pack everything your application needs (code, dependencies, runtime) into a standardized box. That box runs the same way on your laptop, in CI, and in production. No more "works on my machine."

### 6.2 The Dockerfile: Line by Line

`deploy/Dockerfile`:
```dockerfile
# STAGE 1: Build (uses a full Go environment)
FROM golang:1.24-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./          # Copy dependency files first
RUN go mod download             # Download dependencies (cached if go.mod unchanged)
COPY . .                        # Copy all source code

RUN CGO_ENABLED=0 go build -o /bin/scheduler ./cmd/scheduler
RUN CGO_ENABLED=0 go build -o /bin/worker ./cmd/worker
RUN CGO_ENABLED=0 go build -o /bin/forgectl ./cmd/forgectl

# STAGE 2: Run (uses a tiny base image)
FROM alpine:3.19

RUN apk add --no-cache ca-certificates    # For HTTPS support
COPY --from=builder /bin/scheduler /usr/local/bin/scheduler
COPY --from=builder /bin/worker /usr/local/bin/worker
COPY --from=builder /bin/forgectl /usr/local/bin/forgectl
```

**Multi-stage build:** Stage 1 (builder) has the full Go toolchain (~800MB). Stage 2 copies only the compiled binaries into a tiny Alpine image (~5MB). The final image is small and contains no source code or build tools.

**CGO_ENABLED=0:** This creates a statically linked binary — no external C libraries needed. Essential for Alpine (which uses musl instead of glibc).

**COPY go.mod/go.sum first:** Docker caches layers. If your dependencies have not changed (go.mod/go.sum unchanged), Docker reuses the cached `go mod download` layer. Only your source code layer is rebuilt. This makes builds much faster.

### 6.3 Docker Compose: The Full Cluster

Docker Compose orchestrates all services. Here is the network topology:

```
  DOCKER COMPOSE NETWORK: "forge" (bridge)
  ┌────────────────────────────────────────────────────────────┐
  │                                                            │
  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐       │
  │  │ scheduler-1  │  │ scheduler-2  │  │ scheduler-3  │       │
  │  │  :50051 gRPC │  │  :50051 gRPC │  │  :50051 gRPC │       │
  │  │  :7000  Raft │  │  :7000  Raft │  │  :7000  Raft │       │
  │  │  :9090  metr │  │  :9090  metr │  │  :9090  metr │       │
  │  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘       │
  │         │                 │                 │               │
  │         └────── Raft ─────┴──── Raft ───────┘               │
  │                    consensus                                │
  │                       │                                     │
  │  ┌─────────────┐  ┌──┴──────────┐                          │
  │  │  worker-1    │  │  worker-2    │                          │
  │  │  :9091  metr │  │  :9091  metr │                          │
  │  │ connects to  │  │ connects to  │                          │
  │  │ scheduler-1  │  │ scheduler-1  │                          │
  │  └─────────────┘  └─────────────┘                          │
  │                                                            │
  │  ┌─────────────┐  ┌─────────────┐                          │
  │  │ prometheus   │  │  grafana     │                          │
  │  │ scrapes all  │  │ queries      │                          │
  │  │ services     │  │ prometheus   │                          │
  │  │  :9090       │  │  :3000       │                          │
  │  └─────────────┘  └─────────────┘                          │
  │                                                            │
  └────────────────────────────────────────────────────────────┘

  Host port mapping:
  ┌──────────┬───────────────────────┐
  │ Host     │ Container             │
  ├──────────┼───────────────────────┤
  │ 50051    │ scheduler-1:50051     │
  │ 50052    │ scheduler-2:50051     │
  │ 50053    │ scheduler-3:50051     │
  │ 9090     │ scheduler-1:9090      │
  │ 9092     │ scheduler-2:9090      │
  │ 9093     │ scheduler-3:9090      │
  │ 9094     │ worker-1:9091         │
  │ 9095     │ worker-2:9091         │
  │ 9099     │ prometheus:9090       │
  │ 3000     │ grafana:3000          │
  └──────────┴───────────────────────┘
```

**Docker networking:** All services are on the `forge` bridge network. This means `scheduler-1` can reach `scheduler-2` by hostname: `scheduler-2:7000`. Docker's built-in DNS resolves service names to container IPs.

**Environment variables** configure each service:
- `NODE_ID=scheduler-1` — the Raft node identifier
- `RAFT_BIND=scheduler-1:7000` — the Raft peer communication address
- `GRPC_ADDR=:50051` — listen on all interfaces, port 50051
- `PEERS=scheduler-2:7000,scheduler-3:7000` — the other Raft nodes
- `BOOTSTRAP=true` — only scheduler-1 bootstraps the cluster

**depends_on** ensures workers start after schedulers, and Grafana starts after Prometheus. Note: `depends_on` only waits for the container to start, not for the service to be ready.

**Volumes** persist data:
- `prometheus-data` — Prometheus time-series data
- `grafana-data` — Grafana dashboard configurations and users
- Config files are mounted read-only (`:ro`)

> **Try This:** Add a third worker to `docker-compose.yml`. You would need:
> - A new `worker-3` service block
> - `WORKER_ID=worker-3`, `SCHEDULER_ADDR=scheduler-1:50051`, `MAX_SLOTS=4`, `METRICS_PORT=9091`
> - A host port mapping like `9096:9091`
> - Add `worker-3:9091` to `deploy/prometheus.yml` under `forge-worker` targets

---

## Section 7: CLI Design

### 7.1 Cobra Command Structure

**Cobra** is Go's most popular CLI framework (used by kubectl, Docker, Hugo). It structures CLIs as a tree of commands.

```
  FORGECTL COMMAND TREE

  forgectl (root command)
    │
    ├── submit       --type, --payload, --count, --retries, --timeout
    │                "Submit one or more tasks to the scheduler"
    │
    ├── status       <task-id>
    │                "Query task status by ID"
    │
    ├── watch        <task-id>  --interval
    │                "Stream task status updates until terminal"
    │
    ├── cluster
    │                "Display Raft cluster state"
    │
    ├── deadletter
    │   └── list     "List dead-lettered tasks"
    │
    └── dashboard    --refresh
                     "Launch live terminal dashboard"
```

### 7.2 How Cobra Works in Forge

`cmd/forgectl/main.go:16-36`:
```go
func main() {
    rootCmd := &cobra.Command{
        Use:   "forgectl",
        Short: "CLI client for the Forge distributed task orchestrator",
    }

    rootCmd.PersistentFlags().StringVar(&address, "address", "localhost:50051",
        "scheduler gRPC address")

    rootCmd.AddCommand(
        newSubmitCmd(),
        newStatusCmd(),
        newWatchCmd(),
        newClusterCmd(),
        newDeadletterCmd(),
        newDashboardCmd(),
    )

    if err := rootCmd.Execute(); err != nil {
        os.Exit(1)
    }
}
```

**PersistentFlags** are inherited by all subcommands. The `--address` flag works with every command.

Each subcommand is defined in its own file (one command per file):
- `submit.go` → `newSubmitCmd()`
- `status.go` → `newStatusCmd()`
- `watch.go` → `newWatchCmd()`

### 7.3 The Connect Helper

`cmd/forgectl/main.go:39-47`:
```go
func connect() (forgepb.ForgeSchedulerClient, *grpc.ClientConn, error) {
    conn, err := grpc.NewClient(address,
        grpc.WithTransportCredentials(insecure.NewCredentials()),
    )
    if err != nil {
        return nil, nil, fmt.Errorf("connecting to %s: %w", address, err)
    }
    return forgepb.NewForgeSchedulerClient(conn), conn, nil
}
```

Every subcommand calls `connect()` to get a gRPC client. The caller is responsible for closing the connection (`defer conn.Close()`).

### 7.4 Leader Redirect: A Distributed Systems Pattern in the CLI

When you submit a task to a follower, it returns a `FailedPrecondition` error with the leader's address. The CLI automatically reconnects to the leader and retries.

`cmd/forgectl/submit.go:60-90` — `submitWithRedirect()`:
```go
resp, err := client.SubmitTask(ctx, req)
if err == nil {
    return resp, nil
}

leaderAddr := extractLeaderAddress(err)
if leaderAddr == "" {
    return nil, err
}

fmt.Printf("Redirecting to leader at %s\n", leaderAddr)
// reconnect to leader and retry
```

This is a common pattern in distributed systems. Clients for etcd, CockroachDB, and Consul all do this.

### 7.5 Stream Consumption in the Watch Command

`cmd/forgectl/watch.go:37-49`:
```go
for {
    resp, err := stream.Recv()
    if err == io.EOF {
        fmt.Println("Task reached terminal state.")
        return nil
    }
    if err != nil {
        return fmt.Errorf("receiving update: %w", err)
    }
    printTaskStatus(resp)
}
```

The pattern: call `stream.Recv()` in a loop. `io.EOF` means the server closed the stream (task reached terminal state). Any other error is a real failure.

---

## Section 8: Terminal UI (Bubbletea)

### 8.1 The Elm Architecture

Bubbletea uses the **Elm Architecture**, a pattern from the Elm programming language. It has three parts:

1. **Model** — the state of your application
2. **Update** — a function that takes a message and returns a new model
3. **View** — a function that takes the model and returns a string to render

```
  THE BUBBLETEA EVENT LOOP

       ┌─────────────┐
       │   Init()     │  returns initial commands
       └──────┬───────┘  (fetch data, start timer)
              │
              v
  ┌─> Update(msg) ◄─── KeyMsg ("q", "r", "tab")
  │       │              WindowSizeMsg (terminal resized)
  │       │              dashboardData (gRPC response arrived)
  │       │              tickMsg (auto-refresh timer fired)
  │       v
  │   View() ──────> render string to terminal
  │       │
  │       │
  └───────┘  (loop continues until tea.Quit)
```

**Analogy:** A game loop. Every "frame":
1. Check for input (keyboard, network data, timer)
2. Update the game state
3. Render the screen

### 8.2 The Model

`internal/dashboard/dashboard.go:17-26`:
```go
type Model struct {
    client       forgepb.ForgeSchedulerClient
    data         *forgepb.DashboardDataResponse
    err          error
    width        int
    height       int
    refreshRate  time.Duration
    focusedPanel int
    ready        bool
}
```

The model holds everything the dashboard needs: the gRPC client, the latest data from the server, the terminal dimensions, which panel is focused, and whether the initial data has loaded.

### 8.3 Init: Starting Up

`internal/dashboard/dashboard.go:37-42`:
```go
func (m Model) Init() tea.Cmd {
    return tea.Batch(
        fetchDashboardData(m.client),
        tickCmd(m.refreshRate),
    )
}
```

`tea.Batch` runs multiple commands concurrently. On startup, the dashboard:
1. Fetches data from the scheduler (`fetchDashboardData`)
2. Starts a timer for auto-refresh (`tickCmd`)

### 8.4 Update: Handling Events

`internal/dashboard/dashboard.go:45-77`:
```go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        switch msg.String() {
        case "q", "ctrl+c":
            return m, tea.Quit
        case "r":
            return m, fetchDashboardData(m.client)
        case "tab":
            m.focusedPanel = (m.focusedPanel + 1) % numPanels
            return m, nil
        }
    case tea.WindowSizeMsg:
        m.width = msg.Width
        m.height = msg.Height
        m.ready = true
    case dashboardData:
        m.data = msg.data
        m.err = msg.err
    case tickMsg:
        return m, tea.Batch(
            fetchDashboardData(m.client),
            tickCmd(m.refreshRate),
        )
    }
    return m, nil
}
```

Notice how `Update` never does I/O directly. When the tick fires, it returns *commands* that Bubbletea will execute asynchronously. This keeps the UI responsive.

### 8.5 Async Data Fetching with tea.Cmd

`internal/dashboard/fetch.go:19-26`:
```go
func fetchDashboardData(client forgepb.ForgeSchedulerClient) tea.Cmd {
    return func() tea.Msg {
        ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
        defer cancel()
        resp, err := client.GetDashboardData(ctx, &forgepb.DashboardDataRequest{})
        return dashboardData{data: resp, err: err}
    }
}
```

A `tea.Cmd` is a function that returns a `tea.Msg`. Bubbletea runs it in a goroutine and sends the result back to `Update`. This is how you do async operations without blocking the UI.

### 8.6 Auto-Refresh with tea.Tick

`internal/dashboard/fetch.go:32-36`:
```go
func tickCmd(d time.Duration) tea.Cmd {
    return tea.Tick(d, func(t time.Time) tea.Msg {
        return tickMsg(t)
    })
}
```

`tea.Tick` creates a command that waits for the specified duration, then sends a message. The `Update` function handles `tickMsg` by fetching new data AND scheduling the next tick — creating a repeating refresh cycle.

### 8.7 Styling with Lipgloss

`internal/dashboard/styles.go` defines all visual styles using Lipgloss:

```go
pendingStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#b8860b", Dark: "#ffd700"})
runningStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#1e90ff", Dark: "#5dade2"})
completedStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#228b22", Dark: "#2ecc71"})
```

**AdaptiveColor** automatically picks the right color based on whether the terminal uses a light or dark background.

The utilization bar at `dashboard.go:211-226`:
```go
func renderUtilizationBar(availableSlots int32, totalSlots int32) string {
    filled := strings.Repeat("█", int(used))
    empty := strings.Repeat("░", int(totalSlots-used))
    return fmt.Sprintf("[%s%s] %d/%d", filled, empty, used, totalSlots)
}
```
Renders something like `[████░░] 4/6`.

> **Try This:** The dashboard has 4 panels (cluster, tasks, workers, events). Add keyboard shortcuts "1" through "4" to jump directly to a panel. You would add cases in the `tea.KeyMsg` switch: `case "1": m.focusedPanel = 0`.

---

## Section 9: Testing Patterns

### 9.1 The Testing Pyramid in Forge

```
  FORGE TESTING PYRAMID

          /\
         /  \
        / Ch \     Chaos tests: leader failover, worker crash
       / aos  \    (test/chaos/)
      /--------\
     /  Integ.  \   Integration tests: multi-node cluster
    /   ration   \  (test/integration/)
   /--------------\
  /    Unit Tests   \   FSM logic, handlers, individual components
 /                   \  (internal/raft/fsm_test.go, handlers_test.go, etc.)
/─────────────────────\
```

- **Unit tests** run fast (milliseconds), test one function at a time
- **Integration tests** spin up a full cluster in-memory, take seconds
- **Chaos tests** simulate real failures (killing leaders, crashing workers)

### 9.2 Unit Tests: Table-Driven FSM Testing

`internal/raft/fsm_test.go` tests every state transition:

```go
// Testing that fail_task transitions correctly
tests := []struct {
    name           string
    maxRetries     int
    failCount      int
    expectedStatus string
}{
    {"retry when under max", 3, 1, "retrying"},
    {"dead letter at max",  3, 3, "dead_letter"},
    {"zero retries",        0, 1, "dead_letter"},
}
```

The test uses helper functions with `t.Helper()`:
```go
func mustApplyCommand(t *testing.T, fsm *TaskFSM, cmd Command) {
    t.Helper()  // makes error messages point to the caller, not this function
    // ...
}
```

### 9.3 Testing Handlers with httptest

`internal/worker/handlers/handlers_test.go` tests the HTTP handler using Go's built-in test HTTP server:

```go
ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
}))
defer ts.Close()

handler := &HTTPCheckHandler{}
result, err := handler.Execute(context.Background(), []byte(`{"url": "`+ts.URL+`"}`))
```

This creates a real HTTP server on a random port, tests the handler against it, then tears it down. No mocking required.

### 9.4 Integration Tests with bufconn

**The problem:** gRPC tests need a server and client. Using real TCP ports is slow, unreliable (port conflicts), and prevents parallel testing.

**The solution:** `bufconn` creates an in-memory gRPC connection:

```
  NORMAL (production):

  Client ──── TCP Network ────> Server


  BUFCONN (testing):

  Client ──── In-Memory Pipe ──> Server

  ✓ No real ports needed
  ✓ No network latency
  ✓ Tests run in parallel safely
  ✓ Identical gRPC semantics
```

**Where it appears in Forge:**

`test/integration/helpers_test.go` sets up a full cluster in memory:

```go
func setupTestCluster(t *testing.T, n int) []*testClusterNode {
    return setupTestClusterWithConfig(t, n, fastConfig())
}

func fastConfig() scheduler.TrackerConfig {
    return scheduler.TrackerConfig{
        TrackerTick:   100 * time.Millisecond,  // 30x faster than production
        HeartbeatDead: 500 * time.Millisecond,   // 18x faster
        RetryTick:     100 * time.Millisecond,   // 10x faster
        AssignerTick:  100 * time.Millisecond,   // 5x faster
    }
}
```

The `fastConfig()` function uses much shorter intervals than production. This makes tests run in milliseconds instead of seconds.

Each test cluster uses:
- In-memory Raft transport (`hcraft.NewInmemTransport`)
- In-memory Raft stores (`hcraft.NewInmemStore`)
- bufconn listeners instead of TCP
- `t.TempDir()` for any file-based storage
- `t.Cleanup()` for automatic teardown

### 9.5 Chaos Tests: Simulating Real Failures

**Leader failover test** — `test/chaos/leader_failover_test.go`:

The test:
1. Creates a 3-node scheduler cluster
2. Starts 3 workers
3. Submits 500 tasks
4. Waits for 200 to complete
5. **Kills the leader node** (calls `r.Shutdown()` on the leader)
6. Verifies a new leader is elected
7. Workers reconnect to the new leader
8. All 500 tasks eventually reach a terminal state

**Worker crash test** — `test/chaos/worker_crash_test.go`:

The test:
1. Creates a cluster with one worker
2. Submits 5 long-running tasks
3. Waits for tasks to be assigned and running
4. **Kills the worker** (cancels its context)
5. Verifies the tracker detects the dead worker
6. Verifies all 5 tasks are reassigned to "pending"

### 9.6 The pollUntil Pattern

Instead of `time.Sleep()`, Forge tests use a polling helper:

```go
func pollUntil(t *testing.T, timeout time.Duration, condition func() bool) {
    t.Helper()
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        if condition() {
            return
        }
        time.Sleep(50 * time.Millisecond)
    }
    t.Fatal("condition not met within timeout")
}
```

Usage:
```go
pollUntil(t, 10*time.Second, func() bool {
    return countTasksByStatus(nodes, "completed") >= 200
})
```

This is much better than `time.Sleep(5 * time.Second)` because:
- It returns as soon as the condition is met (fast tests)
- It has a timeout so tests do not hang forever
- The error message tells you what failed

**Common Mistakes:**
> - Using `time.Sleep` in tests — makes tests slow and flaky
> - Not using `t.Helper()` in helper functions — error messages point to the wrong line
> - Not using `t.TempDir()` — temporary directories must be cleaned up or they accumulate
> - Running tests without `-race` — race conditions are silent without the race detector

---

## Section 10: Patterns You Can Reuse

This is a quick-reference catalog of every reusable pattern in Forge.

### Pattern Catalog

| # | Pattern | Description | Forge Location | Other Projects |
|---|---------|-------------|----------------|----------------|
| 1 | **Background Ticker Goroutine** | Goroutine that runs work on a timer, exits on ctx.Done() | `assigner.go:18-30`, `worker_tracker.go:18-30`, `retry.go:20-32` | Any service with periodic work: cache cleanup, health checks, metric collection |
| 2 | **Leader Guard** | Check `raft.State() == Leader` before accepting writes | `server.go:148-155`, `assigner.go:33`, `retry.go:35`, `worker_tracker.go:33` | Any multi-node service where only one node should write |
| 3 | **Ring Buffer for Events** | Fixed-size circular buffer with modular indexing | `server.go:90-134` | Activity feeds, log viewers, audit trails, any bounded-memory event store |
| 4 | **Deep Copy for Thread Safety** | Copy structs before returning from mutex-protected reads | `fsm.go:159` (`copied := *v`), `fsm.go:191` | Any concurrent data structure where returned values must not be mutated |
| 5 | **Handler Registry** | Map from string to interface implementation | `handler.go:18-24`, used in `worker.go:40` | Plugin systems, webhook processors, command dispatchers |
| 6 | **Graceful Shutdown** | Signal → cancel ctx → drain work → close connections | `cmd/scheduler/main.go:122-136` | Every production service |
| 7 | **Exponential Backoff** | `min(max, base * 2^(attempt-1))` | `retry.go:69-81` | HTTP retries, reconnection logic, rate limiting |
| 8 | **gRPC Leader Redirect** | Parse FailedPrecondition error, reconnect to leader | `submit.go:60-105` | Any client for a Raft-backed service |
| 9 | **Factory with Config** | Default constructor + config constructor for testing | `server.go:74-88` (default vs WithConfig) | Any component with tunable intervals or dependencies |
| 10 | **Type Switch for Dispatch** | `switch msg.(type)` for handling different message types | `fsm.go:65-78`, `dashboard.go:46-77` | Event handlers, protocol decoders, UI frameworks |
| 11 | **bufconn for gRPC Testing** | In-memory gRPC connections without real ports | `test/integration/helpers_test.go` | Any gRPC service tests |
| 12 | **Atomic Counters** | `sync/atomic.Int32` for lock-free counting | `worker.go:27` | Connection pools, rate limiters, metrics |
| 13 | **pollUntil for Async Tests** | Poll a condition with timeout instead of sleeping | `test/integration/helpers_test.go` | Any test that waits for async results |
| 14 | **Bidirectional Stream** | Server and client send independently on one connection | `server.go:238-301`, `worker.go:113-155` | Chat apps, live dashboards, collaborative editing |
| 15 | **sync.Once for Registration** | Ensure initialization runs exactly once | `metrics.go:76-98` | Database connection pools, config loading, singleton patterns |
| 16 | **Bubbletea Model-Update-View** | Elm architecture for terminal UIs | `dashboard.go:17-110` | Any TUI application: file browsers, dashboards, wizards |
| 17 | **Context-Aware Sleep** | `select` between `time.After` and `ctx.Done()` | `handlers/sleep.go:36-40` | Any cancellable delay or polling loop |
| 18 | **Multi-Stage Docker Build** | Builder stage compiles, runtime stage runs | `deploy/Dockerfile` | Any Go/Rust/C++ service deployment |

### Detailed Pattern Templates

#### Pattern 1: Background Ticker Goroutine

This appears 3 times in Forge with identical structure:

```go
func (s *Server) StartXxx(ctx context.Context) {
    ticker := time.NewTicker(s.config.XxxTick)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            s.doXxx()  // the actual work
        }
    }
}
```

The `doXxx()` function always starts with a leader guard:
```go
func (s *Server) doXxx() {
    if s.raft.State() != hcraft.Leader {
        return  // only the leader does this work
    }
    // ... actual logic
}
```

**Why this pattern works:**
- `ticker.Stop()` prevents the ticker from leaking
- `ctx.Done()` lets the parent cancel the goroutine
- The leader guard prevents followers from doing duplicate work

#### Pattern 5: Handler Registry

```go
// Define the interface
type TaskHandler interface {
    Type() string
    Execute(ctx context.Context, payload []byte) ([]byte, error)
}

// Build a registry
func NewRegistry(handlers ...TaskHandler) map[string]TaskHandler {
    registry := make(map[string]TaskHandler, len(handlers))
    for _, h := range handlers {
        registry[h.Type()] = h
    }
    return registry
}

// Use the registry
handler, ok := registry[taskType]
if !ok {
    return fmt.Errorf("no handler for type %q", taskType)
}
result, err := handler.Execute(ctx, payload)
```

This pattern is used in: webhook processors (route by event type), HTTP routers (route by path), command-line tools (route by subcommand), protocol handlers (route by message type).

#### Pattern 3: Ring Buffer

```go
const maxEvents = 50

type Server struct {
    events   []*Event
    eventIdx int
}

func (s *Server) AddEvent(ev *Event) {
    if len(s.events) < maxEvents {
        s.events = append(s.events, ev)  // still growing
    } else {
        s.events[s.eventIdx % maxEvents] = ev  // overwrite oldest
    }
    s.eventIdx++
}
```

This gives you a fixed-memory event store. The newest 50 events are always available. Old events are automatically discarded.

#### Pattern 7: Exponential Backoff

```go
func computeBackoff(retryCount int) time.Duration {
    const base = 1 * time.Second
    const max  = 60 * time.Second

    backoff := base
    for i := 1; i < retryCount; i++ {
        backoff *= 2
        if backoff >= max {
            return max
        }
    }
    return backoff
}
```

Use this anywhere you retry operations: HTTP requests, database connections, message queue consumers.

---

## Glossary

| Term | Definition |
|------|------------|
| **At-least-once delivery** | A guarantee that messages will be delivered at least once, possibly more. Requires idempotent handlers. |
| **Bidirectional streaming** | A gRPC pattern where both client and server can send messages independently on the same connection. |
| **bufconn** | An in-memory gRPC transport for testing without real network ports. |
| **Build tags** | Go directives (e.g., `//go:build integration`) that control which files are compiled. |
| **Bubbletea** | A Go library for building terminal user interfaces using the Elm Architecture. |
| **Channel** | A Go concurrency primitive for passing values between goroutines. |
| **Cobra** | A Go library for building command-line interfaces with commands and flags. |
| **Consensus** | Agreement among distributed nodes on a single value or state. |
| **Context** | Go's `context.Context` — carries deadlines, cancellation signals, and request-scoped values. |
| **Dead letter** | A task that has exhausted all retry attempts and will not be retried. |
| **Defer** | A Go keyword that schedules a function call to run when the enclosing function returns. |
| **Elm Architecture** | A UI pattern with three parts: Model (state), Update (handle events), View (render). |
| **Exponential backoff** | Waiting longer between each retry attempt: 1s, 2s, 4s, 8s, ... |
| **FSM (Finite State Machine)** | A system with a fixed set of states and defined transitions between them. |
| **Gauge** | A Prometheus metric type whose value can go up or down (e.g., active workers). |
| **Goroutine** | A lightweight thread managed by Go's runtime. Started with the `go` keyword. |
| **Grafana** | An open-source visualization platform for creating dashboards from metric data. |
| **gRPC** | A high-performance RPC framework using Protocol Buffers and HTTP/2. |
| **Heartbeat** | A periodic signal sent to indicate a node is alive. |
| **Histogram** | A Prometheus metric type that distributes observed values into configurable buckets. |
| **Idempotent** | An operation that produces the same result whether executed once or multiple times. |
| **Leader election** | The process by which distributed nodes choose a coordinator. |
| **Lipgloss** | A Go library for styling terminal output with colors, borders, and layout. |
| **Log replication** | Copying committed log entries from the leader to all followers. |
| **Mutex** | A synchronization primitive that ensures mutual exclusion (only one goroutine accesses shared data). |
| **Prometheus** | An open-source monitoring system that scrapes metrics from services and stores time-series data. |
| **Protocol Buffers (protobuf)** | A language-neutral binary serialization format developed by Google. |
| **Quorum** | The minimum number of nodes required to commit a change: (N/2) + 1. |
| **Raft** | A consensus algorithm that ensures a cluster of nodes agrees on a sequence of commands. |
| **Ring buffer** | A fixed-size buffer that overwrites the oldest entries when full. |
| **RWMutex** | A reader-writer mutex — allows multiple concurrent readers OR one exclusive writer. |
| **select** | A Go statement that waits on multiple channel operations, executing whichever is ready first. |
| **Snapshot** | A point-in-time capture of the entire FSM state, used for log compaction. |
| **Split brain** | A failure mode where network partitions cause two groups to diverge independently. |
| **sync.Once** | A Go primitive that ensures a function runs exactly once, even from multiple goroutines. |
| **Table-driven tests** | A Go testing pattern where test cases are defined as a slice of structs. |
| **Type switch** | A Go construct that dispatches on the concrete type of an interface value. |

---

## Files Read for This Guide

Every file in the codebase was read before writing this guide:

**Binaries:** `cmd/scheduler/main.go`, `cmd/worker/main.go`, `cmd/forgectl/main.go`, `cmd/forgectl/submit.go`, `cmd/forgectl/status.go`, `cmd/forgectl/watch.go`, `cmd/forgectl/cluster.go`, `cmd/forgectl/deadletter.go`, `cmd/forgectl/dashboard.go`

**Core logic:** `internal/raft/fsm.go`, `internal/raft/node.go`, `internal/scheduler/server.go`, `internal/scheduler/assigner.go`, `internal/scheduler/worker_tracker.go`, `internal/scheduler/retry.go`, `internal/worker/worker.go`, `internal/worker/handlers/handler.go`, `internal/worker/handlers/sleep.go`, `internal/worker/handlers/fibonacci.go`, `internal/worker/handlers/httpcheck.go`

**Infrastructure:** `internal/metrics/metrics.go`, `internal/dashboard/dashboard.go`, `internal/dashboard/fetch.go`, `internal/dashboard/styles.go`

**Proto:** `internal/proto/forgepb/forge.proto`, `internal/proto/forgepb/forge.pb.go`, `internal/proto/forgepb/forge_grpc.pb.go`

**Tests:** `internal/raft/fsm_test.go`, `internal/raft/node_test.go`, `internal/scheduler/server_test.go`, `internal/worker/handlers/handlers_test.go`, `internal/dashboard/dashboard_test.go`, `test/integration/cluster_test.go`, `test/integration/helpers_test.go`, `test/chaos/leader_failover_test.go`, `test/chaos/worker_crash_test.go`, `test/chaos/helpers_test.go`

**Config:** `deploy/Dockerfile`, `deploy/docker-compose.yml`, `deploy/prometheus.yml`, `deploy/grafana/provisioning/dashboards/dashboards.yml`, `deploy/grafana/provisioning/datasources/prometheus.yml`, `deploy/grafana/dashboards/cluster_overview.json`, `deploy/grafana/dashboards/task_flow.json`, `deploy/grafana/dashboards/worker_health.json`, `deploy/grafana/dashboards/chaos_demo.json`

**Other:** `Makefile`, `go.mod`, `.gitignore`, `.github/workflows/ci.yml`
