package dashboard

import "time"

const defaultThroughputCap = 60

// snapshot records a cumulative completed count at a point in time.
type snapshot struct {
	completed int64
	ts        time.Time
}

// ThroughputTracker computes tasks-per-second from consecutive dashboard
// snapshots using ring buffers for both raw observations and derived rates.
type ThroughputTracker struct {
	snapshots  []snapshot
	throughput []float64
	snapHead   int
	snapLen    int
	tpHead     int
	tpLen      int
	cap        int
}

// NewThroughputTracker creates a tracker with the given ring buffer capacity.
func NewThroughputTracker(cap int) *ThroughputTracker {
	if cap <= 0 {
		cap = defaultThroughputCap
	}
	return &ThroughputTracker{
		snapshots:  make([]snapshot, cap),
		throughput: make([]float64, cap),
		cap:        cap,
	}
}

// Record ingests a new completed count at the given time. If there is a
// previous snapshot, it computes delta_completed / delta_seconds and appends
// the rate to the throughput buffer. Negative deltas are clamped to 0.
func (t *ThroughputTracker) Record(completed int64, ts time.Time) {
	// Compute rate from previous snapshot if one exists.
	if t.snapLen > 0 {
		prevIdx := (t.snapHead - 1 + t.cap) % t.cap
		prev := t.snapshots[prevIdx]
		dt := ts.Sub(prev.ts).Seconds()
		if dt > 0 {
			delta := completed - prev.completed
			rate := float64(delta) / dt
			if rate < 0 {
				rate = 0
			}
			t.throughput[t.tpHead] = rate
			t.tpHead = (t.tpHead + 1) % t.cap
			if t.tpLen < t.cap {
				t.tpLen++
			}
		}
	}

	// Store the new snapshot.
	t.snapshots[t.snapHead] = snapshot{completed: completed, ts: ts}
	t.snapHead = (t.snapHead + 1) % t.cap
	if t.snapLen < t.cap {
		t.snapLen++
	}
}

// Throughput returns a chronological copy of the throughput ring buffer
// (oldest first). This is the slice ntcharts will consume for rendering.
func (t *ThroughputTracker) Throughput() []float64 {
	if t.tpLen == 0 {
		return nil
	}
	result := make([]float64, t.tpLen)
	start := (t.tpHead - t.tpLen + t.cap) % t.cap
	for i := 0; i < t.tpLen; i++ {
		result[i] = t.throughput[(start+i)%t.cap]
	}
	return result
}

// Len returns the number of throughput data points recorded.
func (t *ThroughputTracker) Len() int {
	return t.tpLen
}
