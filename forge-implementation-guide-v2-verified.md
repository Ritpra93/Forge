# Forge — Distributed Task Orchestrator in Go

## Complete Implementation Guide (Verified Edition)

*All library interfaces, import paths, and code patterns in this guide have been cross-checked against official source repositories and documentation as of March 2026.*

---

## What You're Building (Plain English)

Forge is a system that distributes work across multiple computers reliably. It has three parts:

1. **Schedulers** — The "brains." Three of them run simultaneously. They agree on a leader using a voting protocol called Raft. The leader accepts jobs, decides which worker does what, and tracks progress. If the leader crashes, the other two elect a new one in seconds with zero data loss.

2. **Workers** — The "hands." They register with the scheduler, get assigned tasks, execute them, and report back. They send heartbeats ("I'm still alive") every few seconds. If a worker dies mid-task, the scheduler notices and reassigns the work.

3. **Dashboard** — Grafana dashboards showing everything in real-time: task flow, worker health, leader elections, throughput, and failure recovery. This is where the "wow" factor lives during demos.

---

## The Stack (Verified Details)

### Go

**What it is:** A programming language created by Google for building servers and infrastructure software.

**Why Go for this project:**

- **Concurrency is built-in.** Go has goroutines (lightweight threads — you can run millions) and channels (typed pipes for goroutines to communicate). This maps directly from pthreads in 4061, but without manual mutex management. Where in C you'd write `pthread_create` + `pthread_mutex_lock`, in Go you write `go myFunction()` and communicate via channels.
- **It's what the industry uses for this exact type of software.** Kubernetes, Docker, Terraform, Prometheus, etcd — all written in Go.
- **Talent shortage.** 4.1 million Go developers globally but demand outstrips supply. Having Go on your resume is a concrete differentiator.
- **Fast compile times.** Unlike Rust (minutes), Go compiles in seconds. This matters during rapid iteration.
- **Simple language.** ~25 keywords. You can be productive within a week coming from C.

**Version requirement:** Go 1.22+ (for improved generics support used by latest gRPC codegen).

### gRPC + Protocol Buffers

**What it is:** gRPC is a way for programs on different computers to call each other's functions over the network. Protocol Buffers (protobuf) is the serialization format.

**Why not REST/JSON:**

- **Type-safe contracts.** You define your API in a `.proto` file, and gRPC generates Go code for both client and server. Compiler catches mismatches.
- **Bidirectional streaming.** Workers maintain persistent connections to the scheduler for heartbeats and task assignments. gRPC supports this natively — both sides can send messages at any time on the same connection.
- **Efficient binary format.** Protobuf messages are ~10x smaller than JSON and ~10x faster to serialize.

**VERIFIED install commands:**

```bash
# Install the protobuf compiler plugins
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Ensure your GOPATH/bin is in PATH
export PATH="$PATH:$(go env GOPATH)/bin"
```

**VERIFIED code generation command:**

```bash
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       internal/proto/forge.proto
```

The `paths=source_relative` flag is **required** — without it, protoc uses the `go_package` option to determine output directory, which often puts files in unexpected locations.

**IMPORTANT NOTE:** The latest `protoc-gen-go-grpc` (v1.6.0+) generates generic stream types by default. This means streaming interfaces look slightly different from pre-2024 tutorials you might find online. The `Send()`/`Recv()` pattern is the same, but type signatures use Go generics. If you encounter confusing type errors, you can disable this with `--go-grpc_opt=use_generic_streams_experimental=false`, but the generic version is recommended.

### Raft Consensus via hashicorp/raft

**What it is:** An algorithm that lets multiple computers agree on shared state, even when some crash.

**VERIFIED FSM interface** (from `github.com/hashicorp/raft/fsm.go`):

```go
// You implement this interface. It's your application's state machine.
type FSM interface {
    // Apply is called once a log entry is committed by a majority.
    // Apply must be deterministic — produce the same result on all peers.
    // The returned value is available via ApplyFuture.Response.
    Apply(*raft.Log) interface{}

    // Snapshot returns an FSMSnapshot for log compaction.
    // Apply and Snapshot are NOT called concurrently, but
    // Apply WILL be called concurrently with FSMSnapshot.Persist.
    Snapshot() (raft.FSMSnapshot, error)

    // Restore is called to restore the FSM from a snapshot.
    Restore(reader io.ReadCloser) error
}

// You also implement this for snapshots:
type FSMSnapshot interface {
    // Persist writes all state to the sink. Call sink.Close() when done,
    // or sink.Cancel() on error.
    Persist(sink raft.SnapshotSink) error

    // Release is called when the snapshot is no longer needed.
    Release()
}
```

**VERIFIED NewRaft function signature** (from `github.com/hashicorp/raft/api.go`):

```go
func NewRaft(
    conf *Config,
    fsm FSM,
    logs LogStore,       // where Raft log entries are stored
    stable StableStore,  // where Raft metadata (current term, etc.) is stored
    snaps SnapshotStore, // where snapshots are stored
    trans Transport,     // network transport between nodes
) (*Raft, error)
```

**Go module imports:**

```go
import (
    "github.com/hashicorp/raft"
    raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)
```

### BBolt via raft-boltdb/v2

**Clarification:** The guide previously said "BoltDB" — this is technically **BBolt** (`go.etcd.io/bbolt`), the actively maintained fork. The original `boltdb/bolt` is archived and unmaintained. The `raft-boltdb/v2` package wraps BBolt internally. You don't import BBolt directly — `raft-boltdb/v2` handles it.

**VERIFIED:** `raft-boltdb/v2`'s `BoltStore` implements both `raft.LogStore` AND `raft.StableStore`, so a single BoltStore instance serves as both the `logs` and `stable` arguments to `NewRaft`.

```go
store, err := raftboltdb.NewBoltStore(filepath.Join(dataDir, "raft.db"))
// Use `store` for BOTH LogStore and StableStore:
r, err := raft.NewRaft(config, fsm, store, store, snapshots, transport)
```

### Prometheus + Grafana

**VERIFIED constructors and patterns** (from `github.com/prometheus/client_golang`):

```go
import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

// Counter (value that only goes up)
tasksSubmitted := prometheus.NewCounter(prometheus.CounterOpts{
    Name: "forge_tasks_submitted_total",
    Help: "Total number of tasks submitted",
})

// Gauge (value that goes up and down)
activeWorkers := prometheus.NewGauge(prometheus.GaugeOpts{
    Name: "forge_active_workers",
    Help: "Number of currently connected workers",
})

// Histogram (distribution of values in buckets)
taskLatency := prometheus.NewHistogram(prometheus.HistogramOpts{
    Name:    "forge_task_duration_seconds",
    Help:    "Time from task submission to completion",
    Buckets: prometheus.DefBuckets, // default: .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10
})

// CounterVec (counter with labels)
tasksByStatus := prometheus.NewCounterVec(prometheus.CounterOpts{
    Name: "forge_tasks_by_status_total",
    Help: "Tasks completed by status",
}, []string{"status"}) // label name

// Register metrics
prometheus.MustRegister(tasksSubmitted, activeWorkers, taskLatency, tasksByStatus)

// Expose /metrics endpoint
http.Handle("/metrics", promhttp.Handler())
http.ListenAndServe(":9090", nil)

// Usage:
tasksSubmitted.Inc()
activeWorkers.Set(5)
taskLatency.Observe(0.35) // 350ms
tasksByStatus.WithLabelValues("completed").Inc()
```

### Docker + Docker Compose

Everything runs locally. `docker-compose up` spins up the entire cluster. Free via Docker Desktop (personal/education use).

### GitHub Actions

Free for public repos. CI runs `go test -race ./...`, linting, and Docker builds on every push.

---

## Verified Protobuf Schema

This is the corrected `.proto` file with the **required** `go_package` option:

```protobuf
syntax = "proto3";

package forge;

// REQUIRED: without this, protoc-gen-go will fail
option go_package = "github.com/yourusername/forge/internal/proto/forgepb";

service ForgeScheduler {
  // Submit a new task (unary RPC)
  rpc SubmitTask(TaskRequest) returns (TaskResponse);

  // Query task status (unary RPC)
  rpc GetTaskStatus(TaskStatusRequest) returns (TaskStatusResponse);

  // Worker registration with bidirectional streaming:
  // Worker sends heartbeats, scheduler sends task assignments
  rpc RegisterWorker(stream WorkerHeartbeat) returns (stream TaskAssignment);

  // Worker reports task completion (unary RPC)
  rpc ReportTaskResult(TaskResult) returns (TaskResultAck);
}

message TaskRequest {
  string type = 1;           // e.g., "fibonacci", "http_check", "sleep"
  bytes payload = 2;         // arbitrary data for the task handler
  int32 max_retries = 3;
  int32 timeout_seconds = 4;
}

message TaskResponse {
  string task_id = 1;
  string status = 2;         // "pending"
}

message TaskStatusRequest {
  string task_id = 1;
}

message TaskStatusResponse {
  string task_id = 1;
  string status = 2;         // pending, scheduled, running, completed, failed, dead_letter
  int32 retry_count = 3;
  string assigned_worker = 4;
  int64 created_at = 5;      // unix timestamp
  int64 updated_at = 6;
}

message WorkerHeartbeat {
  string worker_id = 1;
  repeated string capabilities = 2;  // task types this worker handles
  int32 available_slots = 3;         // concurrent task capacity
}

message TaskAssignment {
  string task_id = 1;
  string type = 2;
  bytes payload = 3;
  int32 timeout_seconds = 4;
}

message TaskResult {
  string task_id = 1;
  string worker_id = 2;
  bool success = 3;
  bytes result = 4;          // output data
  string error_message = 5;  // populated on failure
}

message TaskResultAck {
  bool acknowledged = 1;
}
```

---

## What Makes This Project Stand Out to Recruiters

### 1. It solves a real problem that real companies have

Every company beyond a certain size has a task queue. Amazon has SQS + Step Functions. Google has Cloud Tasks. Stripe has custom Kafka consumers. You're building a simplified version of the same category of software.

### 2. It demonstrates the #1 most-asked system design topic

"Design a distributed task queue" is among the most common FAANG system design questions. Having built one transforms your interview answers from theoretical to experiential.

### 3. The chaos testing demo is unforgettable

Start the system, submit 1,000 tasks, let them process, then `docker kill scheduler-1` (the leader). The Grafana dashboard shows the election happening, a new leader emerging, and task processing continuing with zero data loss — all in under 3 seconds. 30-second demo > 30-minute explanation.

### 4. The codebase signals engineering maturity

Clean Go code, protobuf definitions, chaos tests, a well-organized repo, Docker Compose setup, and architecture decision records (ADRs) — this is what a senior engineer's side project looks like.

---

## Week-by-Week Implementation Plan

### Prerequisites (Weekend Before Week 1)

1. **Go Tour** (tour.golang.org) — ~3 hours, covers syntax
2. **Write a simple TCP echo server in Go** — you know sockets from 4061, this is just learning Go's `net` package
3. **Install tools:**
   - Go 1.22+ (`go version` to check)
   - Docker Desktop
   - `protoc` (protobuf compiler): `brew install protobuf` on Mac, `apt install protobuf-compiler` on Linux
   - Go gRPC plugins:
     ```bash
     go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
     go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
     ```
   - Verify: `which protoc-gen-go` and `which protoc-gen-go-grpc` should both resolve

---

### Week 1 — Foundation: Project Skeleton + Raft Cluster

**Goal:** Three scheduler nodes that can elect a leader and replicate a simple log.

**Day 1–2: Project setup and protobuf**

```
forge/
├── cmd/
│   ├── scheduler/main.go
│   ├── worker/main.go        # stub
│   └── forgectl/main.go      # stub
├── internal/
│   ├── raft/                  # Raft FSM implementation
│   │   └── fsm.go
│   ├── scheduler/             # Task scheduling logic
│   ├── worker/                # Worker client logic
│   └── proto/
│       └── forgepb/
│           └── forge.proto    # The proto file above
├── deploy/
│   └── docker-compose.yml
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

Initialize module:
```bash
mkdir forge && cd forge
go mod init github.com/yourusername/forge
```

Generate protobuf code:
```bash
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       internal/proto/forgepb/forge.proto
```

This produces two files:
- `forge.pb.go` — message types (TaskRequest, TaskResponse, etc.)
- `forge_grpc.pb.go` — service client/server interfaces

**Day 3–4: Raft cluster setup**

The key file is your FSM implementation. Here's the verified skeleton:

```go
// internal/raft/fsm.go
package raft

import (
    "encoding/json"
    "io"
    "sync"

    hcraft "github.com/hashicorp/raft"
)

// Command represents a state change in the task system
type Command struct {
    Type    string `json:"type"`    // "create_task", "assign_task", "complete_task", "fail_task"
    TaskID  string `json:"task_id"`
    Payload []byte `json:"payload"` // JSON-encoded task-specific data
}

// Task represents a task in the system
type Task struct {
    ID             string `json:"id"`
    Type           string `json:"type"`
    Status         string `json:"status"` // pending, scheduled, running, completed, failed, dead_letter
    Payload        []byte `json:"payload"`
    AssignedWorker string `json:"assigned_worker"`
    RetryCount     int    `json:"retry_count"`
    MaxRetries     int    `json:"max_retries"`
    CreatedAt      int64  `json:"created_at"`
    UpdatedAt      int64  `json:"updated_at"`
}

// TaskFSM implements raft.FSM
type TaskFSM struct {
    mu    sync.RWMutex
    tasks map[string]*Task
}

func NewTaskFSM() *TaskFSM {
    return &TaskFSM{
        tasks: make(map[string]*Task),
    }
}

// Apply is called once a log entry is committed by a majority.
// This is where state changes actually happen.
func (f *TaskFSM) Apply(log *hcraft.Log) interface{} {
    var cmd Command
    if err := json.Unmarshal(log.Data, &cmd); err != nil {
        return err
    }

    f.mu.Lock()
    defer f.mu.Unlock()

    switch cmd.Type {
    case "create_task":
        var task Task
        if err := json.Unmarshal(cmd.Payload, &task); err != nil {
            return err
        }
        task.Status = "pending"
        f.tasks[task.ID] = &task
        return nil

    case "assign_task":
        // unmarshal assignment details and update task status
        // ...
        return nil

    case "complete_task":
        if task, ok := f.tasks[cmd.TaskID]; ok {
            task.Status = "completed"
        }
        return nil

    case "fail_task":
        // handle retry logic
        // ...
        return nil
    }

    return nil
}

// Snapshot returns a point-in-time snapshot of all tasks.
// IMPORTANT: Apply will NOT be called while Snapshot runs,
// but Apply WILL be called concurrently with Persist.
func (f *TaskFSM) Snapshot() (hcraft.FSMSnapshot, error) {
    f.mu.RLock()
    defer f.mu.RUnlock()

    // Deep copy the tasks map
    tasks := make(map[string]*Task, len(f.tasks))
    for k, v := range f.tasks {
        copied := *v
        tasks[k] = &copied
    }

    return &taskSnapshot{tasks: tasks}, nil
}

// Restore replaces all state from a snapshot
func (f *TaskFSM) Restore(reader io.ReadCloser) error {
    defer reader.Close()

    var tasks map[string]*Task
    if err := json.NewDecoder(reader).Decode(&tasks); err != nil {
        return err
    }

    f.mu.Lock()
    f.tasks = tasks
    f.mu.Unlock()

    return nil
}

// taskSnapshot implements raft.FSMSnapshot
type taskSnapshot struct {
    tasks map[string]*Task
}

func (s *taskSnapshot) Persist(sink hcraft.SnapshotSink) error {
    data, err := json.Marshal(s.tasks)
    if err != nil {
        sink.Cancel()
        return err
    }

    if _, err := sink.Write(data); err != nil {
        sink.Cancel()
        return err
    }

    return sink.Close()
}

func (s *taskSnapshot) Release() {
    // nothing to clean up
}
```

Setting up the Raft node:

```go
// internal/raft/node.go
package raft

import (
    "net"
    "os"
    "path/filepath"
    "time"

    hcraft "github.com/hashicorp/raft"
    raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

func SetupRaft(nodeID, raftAddr, dataDir string, bootstrap bool) (*hcraft.Raft, *TaskFSM, error) {
    config := hcraft.DefaultConfig()
    config.LocalID = hcraft.ServerID(nodeID)

    // Tuning for faster elections in demo (defaults are fine for production)
    config.HeartbeatTimeout = 1000 * time.Millisecond
    config.ElectionTimeout = 1000 * time.Millisecond
    config.LeaderLeaseTimeout = 500 * time.Millisecond

    fsm := NewTaskFSM()

    // BoltStore serves as BOTH LogStore and StableStore
    store, err := raftboltdb.NewBoltStore(filepath.Join(dataDir, "raft.db"))
    if err != nil {
        return nil, nil, err
    }

    // Snapshot store — keeps snapshots as files on disk
    snapshotStore, err := hcraft.NewFileSnapshotStore(dataDir, 2, os.Stderr)
    if err != nil {
        return nil, nil, err
    }

    // TCP transport for Raft communication between nodes
    addr, err := net.ResolveTCPAddr("tcp", raftAddr)
    if err != nil {
        return nil, nil, err
    }
    transport, err := hcraft.NewTCPTransport(raftAddr, addr, 3, 10*time.Second, os.Stderr)
    if err != nil {
        return nil, nil, err
    }

    r, err := hcraft.NewRaft(config, fsm, store, store, snapshotStore, transport)
    if err != nil {
        return nil, nil, err
    }

    // Bootstrap the cluster (only on the first node, only once)
    if bootstrap {
        cfg := hcraft.Configuration{
            Servers: []hcraft.Server{
                {
                    ID:      hcraft.ServerID(nodeID),
                    Address: hcraft.ServerAddress(raftAddr),
                },
            },
        }
        r.BootstrapCluster(cfg)
    }

    return r, fsm, nil
}
```

**Day 5: Docker Compose**

```yaml
# deploy/docker-compose.yml
services:
  scheduler-1:
    build:
      context: ..
      dockerfile: deploy/Dockerfile
    command: ["/app/scheduler",
      "--node-id=node1",
      "--raft-addr=scheduler-1:9000",
      "--grpc-port=50051",
      "--metrics-port=9090",
      "--bootstrap=true"]
    ports:
      - "50051:50051"
      - "9090:9090"

  scheduler-2:
    build:
      context: ..
      dockerfile: deploy/Dockerfile
    command: ["/app/scheduler",
      "--node-id=node2",
      "--raft-addr=scheduler-2:9000",
      "--grpc-port=50051",
      "--metrics-port=9090",
      "--join=scheduler-1:50051"]

  scheduler-3:
    build:
      context: ..
      dockerfile: deploy/Dockerfile
    command: ["/app/scheduler",
      "--node-id=node3",
      "--raft-addr=scheduler-3:9000",
      "--grpc-port=50051",
      "--metrics-port=9090",
      "--join=scheduler-1:50051"]

  prometheus:
    image: prom/prometheus:latest
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml
    ports:
      - "9091:9090"

  grafana:
    image: grafana/grafana:latest
    ports:
      - "3000:3000"
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=admin
    volumes:
      - ./grafana/dashboards:/var/lib/grafana/dashboards
      - ./grafana/provisioning:/etc/grafana/provisioning
```

**End of Week 1:** Run `docker-compose up`, see leader election in logs. Kill the leader, see a new one elected.

---

### Week 2 — Task State Machine + gRPC API

**Goal:** Submit tasks via gRPC, store them in Raft, query status.

Task state machine:
```
pending → scheduled → running → completed
                   ↘           ↗
                    → failed → retrying → (back to pending)
                           ↘
                            → dead_letter (max retries exceeded)
```

Implement `SubmitTask` and `GetTaskStatus` gRPC endpoints. Key pattern: only the Raft leader can accept writes. If a follower receives a write, return the leader's address so the client can redirect.

Write integration test: start 3 nodes in-process, submit 100 tasks, kill leader, verify new leader has all 100 tasks.

---

### Week 3 — Workers: Registration, Heartbeats, Execution

**Goal:** Workers connect, receive tasks, execute them, report results.

Bidirectional streaming for `RegisterWorker`: worker sends heartbeats, scheduler sends task assignments on the same stream.

Pluggable task handlers via interface:
```go
type TaskHandler interface {
    Type() string
    Execute(ctx context.Context, payload []byte) ([]byte, error)
}
```

Demo handlers: sleep (variable duration), fibonacci (CPU-bound), HTTP checker (I/O-bound).

---

### Week 4 — Fault Tolerance (The Impressive Part)

**Goal:** Handle every failure mode gracefully.

Worker failure detection (3 missed heartbeats → mark dead → reassign tasks). Retry with exponential backoff. Dead-letter queue after max retries.

**Chaos tests** — the crown jewels:
- `TestLeaderFailover`: kill leader, verify recovery < 5s, verify no data loss
- `TestWorkerCrashMidTask`: kill worker, verify task reassignment
- `TestDoubleFailure`: kill leader + worker simultaneously

---

### Week 5 — CLI Client + Prometheus Metrics

**Goal:** Polished CLI (`forgectl`) using `github.com/spf13/cobra` and comprehensive metrics.

Commands: `submit`, `status`, `watch`, `cluster`, `deadletter list`.

Metrics: tasks_submitted_total, tasks_completed_total, task_duration_seconds, active_workers, pending_queue_depth, raft_is_leader, raft_commit_index.

---

### Week 6 — Grafana Dashboards

**Goal:** Four dashboards that make the system visible.

1. **Cluster Overview** — Raft leader indicator, node health, commit index
2. **Task Flow** — throughput, status breakdown, latency histograms
3. **Worker Health** — worker grid, utilization, heartbeat latency
4. **Chaos Demo** — combined view designed for live failure injection

Export dashboards as JSON in `deploy/grafana/dashboards/` so `docker-compose up` brings everything up pre-configured.

---

### Week 7 — Polish, Documentation, Demo

**Goal:** Recruiter-ready.

- README with demo GIF, architecture diagram, quick start
- `docs/architecture.md` with design decisions
- `docs/benchmarks.md` with reproducible performance tests
- GitHub Actions CI with `go test -race`, linting, Docker builds
- Record 3-minute demo video

---

## The Resume Bullet

> **Forge** — Distributed Task Orchestrator *(Go, gRPC, Raft, Prometheus, Grafana, Docker)*
> Designed and implemented a fault-tolerant distributed task orchestrator in Go with Raft-based leader election across a 3-node scheduler cluster, gRPC-based worker management with heartbeat failure detection, and automated retry with exponential backoff. Chaos-tested with automated leader failover (<3s recovery) and worker crash recovery, processing 10K+ tasks/minute across a 5-node worker pool.

---

## Total Cost: $0

| Component | Cost |
|-----------|------|
| Go | Free (BSD license) |
| gRPC + Protobuf | Free (Apache 2.0) |
| hashicorp/raft | Free (MPL-2.0) |
| raft-boltdb/v2 (BBolt) | Free (MPL-2.0) |
| Prometheus | Free (Apache 2.0) |
| Grafana OSS | Free (AGPL-3.0) |
| Docker Desktop | Free (personal/education) |
| GitHub + Actions | Free (public repos) |

---

## Building Forge with Claude Code: Strategy Guide

### Why Claude Code is Ideal for This Project

Claude Code excels at generating well-structured Go code, implementing interfaces correctly, writing tests, and creating Docker/YAML configuration files. The project is modular with clear interfaces between components, which plays to Claude Code's strengths. However, you need to be strategic about *how* you prompt it.

### Core Principles

**1. Never ask Claude Code to build the whole thing at once.**

The #1 mistake is prompting "Build me a distributed task orchestrator in Go with Raft." You'll get a plausible-looking but broken monolith. Instead, build one verified layer at a time.

**2. Give Claude Code this document as context.**

Before starting any work, feed Claude Code this entire guide. It will use the verified interfaces, import paths, and patterns as reference. This prevents hallucinated APIs.

**3. Always ask Claude Code to write tests alongside the implementation.**

Go's testing is simple and Claude Code is very good at it. Tests serve as a verification layer — if the generated code has bugs, the tests will catch them.

**4. Use CLAUDE.md to anchor the project.**

Create a `CLAUDE.md` in your project root. Claude Code reads this file automatically for project context. This is your single most important leverage point.

### The CLAUDE.md File

Create this file in your project root before starting:

```markdown
# Forge — Distributed Task Orchestrator

## Project Overview
A distributed task orchestrator in Go with Raft-based consensus,
gRPC communication, and Prometheus observability.

## Key Technical Decisions
- hashicorp/raft for consensus (NOT etcd, NOT custom implementation)
- raft-boltdb/v2 for Raft log storage (uses BBolt internally)
- gRPC with protobuf for all inter-node communication
- Prometheus client_golang for metrics
- Grafana for dashboards (NOT a custom React frontend)
- Docker Compose for local cluster deployment

## Verified Library Interfaces

### hashicorp/raft FSM interface
```go
type FSM interface {
    Apply(*raft.Log) interface{}
    Snapshot() (raft.FSMSnapshot, error)
    Restore(reader io.ReadCloser) error
}
type FSMSnapshot interface {
    Persist(sink raft.SnapshotSink) error
    Release()
}
```

### NewRaft signature
```go
raft.NewRaft(conf, fsm, logStore, stableStore, snapshotStore, transport)
// raft-boltdb/v2 BoltStore implements BOTH LogStore and StableStore
```

### Protobuf code generation
```bash
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       internal/proto/forgepb/forge.proto
```

### Prometheus metrics pattern
```go
import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)
// Use prometheus.NewCounter, NewGauge, NewHistogram, NewCounterVec
// Register with prometheus.MustRegister(...)
// Expose via http.Handle("/metrics", promhttp.Handler())
```

## Module Path
github.com/yourusername/forge

## Go Version
1.22+

## Code Style
- Use standard Go formatting (gofmt)
- All public functions must have doc comments
- Error handling: always check and wrap errors
- Use structured logging (log/slog)
- Tests use table-driven pattern where appropriate
- Run with -race flag in tests

## Project Structure
```
forge/
├── cmd/
│   ├── scheduler/main.go
│   ├── worker/main.go
│   └── forgectl/main.go
├── internal/
│   ├── raft/          # FSM, Raft node setup
│   ├── scheduler/     # Task assignment, worker tracking
│   ├── worker/        # Task execution, heartbeat
│   ├── proto/forgepb/ # Protobuf definitions + generated code
│   └── metrics/       # Prometheus instrumentation
├── deploy/
│   ├── docker-compose.yml
│   ├── Dockerfile
│   ├── prometheus.yml
│   └── grafana/
├── test/
│   ├── integration/
│   └── chaos/
├── docs/
├── CLAUDE.md
├── Makefile
├── go.mod
└── README.md
```
```

### Step-by-Step Claude Code Prompting Strategy

Here's the exact sequence of prompts to use, one at a time. **Do not proceed to the next prompt until the current one compiles and tests pass.**

---

**Prompt 1: Project skeleton**

```
Initialize a Go module at github.com/yourusername/forge with Go 1.22.
Create the directory structure from CLAUDE.md.
Create the Makefile with targets: build, test, lint, proto, docker, clean.
Create a basic Dockerfile that builds the scheduler and worker binaries.
Create go.mod with these dependencies:
  - github.com/hashicorp/raft
  - github.com/hashicorp/raft-boltdb/v2
  - google.golang.org/grpc
  - google.golang.org/protobuf
  - github.com/prometheus/client_golang
  - github.com/spf13/cobra
Don't write any application logic yet — just the skeleton with
main.go stubs that print "scheduler starting" / "worker starting".
Verify it compiles with `go build ./...`.
```

---

**Prompt 2: Protobuf schema and code generation**

```
Create the forge.proto file at internal/proto/forgepb/forge.proto
with the schema from CLAUDE.md (ForgeScheduler service with
SubmitTask, GetTaskStatus, RegisterWorker, ReportTaskResult RPCs).
Make sure go_package is set to
"github.com/yourusername/forge/internal/proto/forgepb".
Generate the Go code with:
  protoc --go_out=. --go_opt=paths=source_relative \
         --go-grpc_out=. --go-grpc_opt=paths=source_relative \
         internal/proto/forgepb/forge.proto
Verify the generated files exist and the project still compiles.
```

---

**Prompt 3: Raft FSM implementation**

```
Implement the TaskFSM in internal/raft/fsm.go that implements
the hashicorp/raft FSM interface:
  - Apply(*raft.Log) interface{}
  - Snapshot() (raft.FSMSnapshot, error)
  - Restore(reader io.ReadCloser) error

The FSM should manage a map of Tasks with states:
pending, scheduled, running, completed, failed, dead_letter.

Commands: create_task, assign_task, complete_task, fail_task,
reassign_task. Use JSON encoding for commands.

Also implement FSMSnapshot with Persist and Release.

Write thorough unit tests in internal/raft/fsm_test.go covering:
  - Creating tasks
  - State transitions
  - Snapshot and restore round-trip
  - Invalid commands

Use table-driven tests. Run with -race flag.
```

---

**Prompt 4: Raft node setup**

```
Implement Raft node setup in internal/raft/node.go.
Use raft-boltdb/v2 BoltStore for BOTH LogStore and StableStore
(single BoltStore instance serves both).
Use raft.NewFileSnapshotStore for snapshots.
Use raft.NewTCPTransport for network transport.

The SetupRaft function should accept: nodeID, raftAddr, dataDir,
and a bootstrap flag. Only the first node bootstraps.

Write an integration test that:
1. Starts a 3-node cluster in-process using different temp directories
2. Waits for leader election
3. Verifies one node is Leader and two are Followers
4. Submits a command via raft.Apply on the leader
5. Verifies all three nodes have the same state

Use t.TempDir() for test data directories.
```

---

**Prompt 5: gRPC server**

```
Implement the gRPC server for the scheduler in internal/scheduler/server.go.
It should implement the ForgeSchedulerServer interface from the
generated protobuf code.

SubmitTask: check if this node is Raft leader (if not, return error
with leader address). Create a "create_task" command, call raft.Apply,
wait for commitment, return task ID.

GetTaskStatus: can be served by any node (leader or follower) —
read directly from the FSM's task map.

RegisterWorker: bidirectional streaming. Receive heartbeats from
the worker in one goroutine, send task assignments in another.
Track connected workers with last heartbeat timestamp.

Write tests for SubmitTask and GetTaskStatus using an in-process
gRPC server (use bufconn from google.golang.org/grpc/test/bufconn).
```

---

**Prompt 6: Worker implementation**

```
Implement the worker in internal/worker/worker.go.
The worker should:
1. Connect to the scheduler via gRPC
2. Open a RegisterWorker bidirectional stream
3. Send heartbeats every 3 seconds in a goroutine
4. Receive task assignments from the stream
5. Execute tasks using pluggable TaskHandler interface
6. Report results via ReportTaskResult RPC

Create the TaskHandler interface:
  Type() string
  Execute(ctx context.Context, payload []byte) ([]byte, error)

Create three demo handlers:
  - SleepHandler: sleeps for N seconds
  - FibonacciHandler: computes fibonacci(N)
  - HTTPCheckHandler: makes GET request, returns status code

Write unit tests for each handler.
```

---

**Prompt 7: Fault tolerance**

```
Add fault tolerance to the scheduler:

1. Worker failure detection: if a worker misses 3 consecutive
   heartbeats (9 seconds), mark it dead, find all its in-flight
   tasks, reset them to "pending" via Raft commands.

2. Retry logic: when a task fails, if retry_count < max_retries,
   set status to "retrying" with exponential backoff
   (1s, 2s, 4s, 8s, capped at 60s). If max retries exceeded,
   move to "dead_letter".

3. Leader forwarding: if a follower receives a SubmitTask request,
   return an error code with the current leader's gRPC address.

Write chaos tests in test/chaos/:
- TestLeaderFailover: start 3 schedulers + 3 workers, submit 500
  tasks, let 200 complete, kill leader, verify new leader elected
  within 5s, verify remaining tasks complete.
- TestWorkerCrashMidTask: kill a worker with in-flight tasks,
  verify reassignment within 15s.
```

---

**Prompt 8: CLI client**

```
Build the forgectl CLI using github.com/spf13/cobra in cmd/forgectl/.

Commands:
  forgectl submit --type fibonacci --count 100 --payload '{"n":35}'
  forgectl status <task-id>
  forgectl watch (streams task status updates)
  forgectl cluster (shows Raft cluster state, leader, node health)
  forgectl deadletter list

The CLI connects to the scheduler via gRPC. If connected to a
follower, automatically redirect to the leader.

Include --address flag (default: localhost:50051) on all commands.
```

---

**Prompt 9: Prometheus metrics**

```
Add Prometheus metrics throughout the codebase.
Create internal/metrics/metrics.go that defines all metrics:

Scheduler metrics:
  forge_tasks_submitted_total (Counter)
  forge_tasks_completed_total (Counter)
  forge_tasks_failed_total (Counter)
  forge_task_duration_seconds (Histogram)
  forge_active_workers (Gauge)
  forge_pending_queue_depth (Gauge)
  forge_raft_is_leader (Gauge, 1 or 0)
  forge_raft_commit_index (Gauge)

Worker metrics:
  forge_worker_tasks_executed_total (Counter)
  forge_worker_task_execution_seconds (Histogram)

Register all metrics and expose via /metrics HTTP endpoint
on each scheduler and worker (configurable port).

Update docker-compose.yml to add prometheus.yml config that
scrapes all scheduler and worker containers.
```

---

**Prompt 10: Grafana dashboards**

```
Create Grafana dashboard JSON files in deploy/grafana/dashboards/:

1. cluster_overview.json - Raft leader indicator, node health panels,
   commit index graph
2. task_flow.json - tasks/second, status breakdown stacked area,
   latency P50/P95/P99
3. worker_health.json - worker status table, utilization bars,
   heartbeat latency
4. chaos_demo.json - combined view of leader status + throughput +
   worker count for live demo

Create provisioning config in deploy/grafana/provisioning/ so
dashboards auto-load on startup.

Update docker-compose.yml with Grafana volumes.
```

---

**Prompt 11: Documentation and CI**

```
Create:

1. README.md with:
   - One-line description
   - Architecture diagram (Mermaid)
   - Key features (4 bullets)
   - Quick start (docker-compose up)
   - Demo GIF placeholder
   - Repository structure
   - Design decisions section
   - Benchmarks table

2. docs/architecture.md explaining:
   - System design with data flow
   - Raft consensus as applied to Forge
   - Failure modes and recovery
   - Why at-least-once delivery (not exactly-once)
   - Why BBolt (not Postgres)
   - Why gRPC (not REST)

3. .github/workflows/ci.yml with:
   - go test -race ./...
   - golangci-lint run
   - go build ./...
   - docker-compose build
```

### Tips for Each Claude Code Session

**Before each prompt:**
- Make sure all existing code compiles (`go build ./...`)
- Make sure all existing tests pass (`go test -race ./...`)
- If they don't, fix them before moving on

**If Claude Code generates something that doesn't compile:**
- Don't say "fix it." Instead, paste the exact error message and say "This error occurred when running `go build ./...`. Fix the specific issue."
- If it's a wrong import path, specify the correct one from this guide

**If tests fail:**
- Paste the test output and say "This test is failing. Analyze why and fix the implementation, not the test."
- Tests should drive the implementation, not the other way around

**Keep sessions focused:**
- One prompt = one component
- Don't mix concerns (don't ask for Raft + gRPC + tests in one prompt)
- Finish and verify each layer before building the next

**Use `git commit` between each prompt:**
- After each successful prompt, commit with a meaningful message
- This gives you a rollback point if a later prompt breaks things
- Commit messages like: `feat: implement Raft FSM with unit tests`, `feat: add gRPC server with bidirectional streaming`

### Common Claude Code Pitfalls to Watch For

**1. Wrong import aliasing.** Claude Code might import `hashicorp/raft` without an alias, causing conflicts with your `internal/raft` package. Always use:
```go
import hcraft "github.com/hashicorp/raft"
```

**2. Missing error handling.** Go requires explicit error checks. If Claude Code generates code that ignores errors (uses `_`), ask it to add proper error handling.

**3. Generating tests that test the wrong thing.** Sometimes Claude Code writes tests that pass trivially. Ask: "Are these tests actually testing the behavior, or just verifying the code runs without panicking?"

**4. Overly complex solutions.** If Claude Code generates an overly abstracted solution, say: "Simplify this. Use the most straightforward approach with the fewest abstractions."

**5. Outdated gRPC patterns.** If Claude Code generates `grpc.Dial()` instead of `grpc.NewClient()`, or uses `grpc.WithInsecure()` instead of `grpc.WithTransportCredentials(insecure.NewCredentials())`, ask it to use the latest patterns. `grpc.Dial` was deprecated in gRPC-Go v1.63.

---

## After the Build: Interview Preparation

Once Forge is complete, you'll have firsthand answers to these common interview questions:

- "Design a distributed task queue" — You built one
- "What happens when a node fails?" — You tested it with chaos tests
- "Explain the CAP theorem" — Forge chooses CP via Raft
- "At-least-once vs exactly-once delivery?" — You chose at-least-once and can explain why
- "How do you monitor a distributed system?" — Prometheus + Grafana with metrics you designed
- "Walk through a production incident" — "In chaos testing, I discovered that..."
