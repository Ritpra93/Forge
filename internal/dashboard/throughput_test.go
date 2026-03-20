package dashboard

import (
	"math"
	"testing"
	"time"
)

func TestThroughputTracker_BasicRate(t *testing.T) {
	t.Helper()
	tracker := NewThroughputTracker(60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Feed 10 snapshots, 2s apart, +10 completed each time.
	for i := 0; i < 10; i++ {
		tracker.Record(int64(i*10), base.Add(time.Duration(i)*2*time.Second))
	}

	if tracker.Len() != 9 {
		t.Fatalf("expected 9 throughput values, got %d", tracker.Len())
	}

	tp := tracker.Throughput()
	for i, v := range tp {
		if math.Abs(v-5.0) > 0.001 {
			t.Errorf("throughput[%d] = %f, want 5.0", i, v)
		}
	}
}

func TestThroughputTracker_VaryingRates(t *testing.T) {
	tracker := NewThroughputTracker(60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	counts := []int64{0, 5, 20, 20, 25}
	for i, c := range counts {
		tracker.Record(c, base.Add(time.Duration(i)*2*time.Second))
	}

	if tracker.Len() != 4 {
		t.Fatalf("expected 4 throughput values, got %d", tracker.Len())
	}

	tp := tracker.Throughput()
	expected := []float64{2.5, 7.5, 0.0, 2.5}
	for i, want := range expected {
		if math.Abs(tp[i]-want) > 0.001 {
			t.Errorf("throughput[%d] = %f, want %f", i, tp[i], want)
		}
	}
}

func TestThroughputTracker_RingBufferWrap(t *testing.T) {
	tracker := NewThroughputTracker(5)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Feed 8 snapshots → 7 throughput values, but cap is 5 so only last 5.
	for i := 0; i < 8; i++ {
		tracker.Record(int64(i*10), base.Add(time.Duration(i)*2*time.Second))
	}

	if tracker.Len() != 5 {
		t.Fatalf("expected 5 throughput values (capped), got %d", tracker.Len())
	}

	tp := tracker.Throughput()
	// All rates should be 5.0 (10 completed / 2 seconds).
	for i, v := range tp {
		if math.Abs(v-5.0) > 0.001 {
			t.Errorf("throughput[%d] = %f, want 5.0", i, v)
		}
	}
}

func TestThroughputTracker_SingleSnapshot(t *testing.T) {
	tracker := NewThroughputTracker(60)
	tracker.Record(100, time.Now())

	if tracker.Len() != 0 {
		t.Errorf("expected 0 throughput values from single snapshot, got %d", tracker.Len())
	}

	tp := tracker.Throughput()
	if tp != nil {
		t.Errorf("expected nil throughput from single snapshot, got %v", tp)
	}
}

func TestThroughputTracker_NegativeDelta(t *testing.T) {
	tracker := NewThroughputTracker(60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Completed count decreases (e.g., leader failover with stale data).
	tracker.Record(100, base)
	tracker.Record(50, base.Add(2*time.Second))

	if tracker.Len() != 1 {
		t.Fatalf("expected 1 throughput value, got %d", tracker.Len())
	}

	tp := tracker.Throughput()
	if tp[0] != 0.0 {
		t.Errorf("expected 0.0 for negative delta, got %f", tp[0])
	}
}

func TestThroughputTracker_ZeroTimeDelta(t *testing.T) {
	tracker := NewThroughputTracker(60)
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Two snapshots at the exact same time — should not produce a throughput value.
	tracker.Record(0, ts)
	tracker.Record(10, ts)

	if tracker.Len() != 0 {
		t.Errorf("expected 0 throughput values for zero time delta, got %d", tracker.Len())
	}
}
