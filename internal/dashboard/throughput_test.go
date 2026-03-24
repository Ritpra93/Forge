package dashboard

import (
	"testing"
	"time"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

// feedSnapshots drives the model through n DashboardDataMsg updates with the
// given completed counts, returning the final OverviewModel.
func feedSnapshots(t *testing.T, counts []int64, rate time.Duration) OverviewModel {
	t.Helper()
	m := NewOverview(&mockClient{}, rate)
	for _, c := range counts {
		data := &forgepb.DashboardDataResponse{
			TaskCounts: &forgepb.TaskCounts{Completed: int32(c)},
		}
		updated, _ := m.Update(DashboardDataMsg{Data: data})
		m = updated.(OverviewModel)
	}
	return m
}

func TestThroughputTracking(t *testing.T) {
	// Feed 10 snapshots: 0, 10, 20, ..., 90 with a 2s refresh rate.
	// Expected tps = 10 / 2.0 = 5.0 for all 9 intervals.
	counts := []int64{0, 10, 20, 30, 40, 50, 60, 70, 80, 90}
	m := feedSnapshots(t, counts, 2*time.Second)

	if got := len(m.throughputBuf); got != 9 {
		t.Fatalf("throughputBuf len = %d, want 9", got)
	}
	for i, tps := range m.throughputBuf {
		if tps != 5.0 {
			t.Errorf("throughputBuf[%d] = %.4f, want 5.0", i, tps)
		}
	}
}

func TestThroughputRingBufferCap(t *testing.T) {
	// Feed 65 snapshots — ring buffer should stay capped at 60.
	counts := make([]int64, 65)
	for i := range counts {
		counts[i] = int64(i * 4)
	}
	m := feedSnapshots(t, counts, 1*time.Second)

	if got := len(m.throughputBuf); got > throughputBufCap {
		t.Errorf("throughputBuf len = %d, exceeds cap %d", got, throughputBufCap)
	}
	if got := len(m.completedSnaps); got > throughputBufCap {
		t.Errorf("completedSnaps len = %d, exceeds cap %d", got, throughputBufCap)
	}
}

func TestThroughputNegativeDeltaClamped(t *testing.T) {
	// Completed count going backwards should yield 0 tps, not negative.
	counts := []int64{100, 50, 80}
	m := feedSnapshots(t, counts, 2*time.Second)

	if got := len(m.throughputBuf); got != 2 {
		t.Fatalf("throughputBuf len = %d, want 2", got)
	}
	if m.throughputBuf[0] != 0.0 {
		t.Errorf("expected 0 tps for negative delta, got %.4f", m.throughputBuf[0])
	}
}

func TestWorkerUtilizationTracking(t *testing.T) {
	m := NewOverview(&mockClient{}, 2*time.Second)

	// Two updates: worker-1 starts with 3 slots used (available=3), then 5 (available=1).
	for _, available := range []int32{3, 1} {
		data := &forgepb.DashboardDataResponse{
			TaskCounts: &forgepb.TaskCounts{Completed: 0},
			Workers: []*forgepb.WorkerStatus{
				{WorkerId: "worker-1", AvailableSlots: available},
			},
		}
		updated, _ := m.Update(DashboardDataMsg{Data: data})
		m = updated.(OverviewModel)
	}

	hist := m.workerUtilHistory["worker-1"]
	if len(hist) != 2 {
		t.Fatalf("workerUtilHistory len = %d, want 2", len(hist))
	}
	// available=3 out of 6 → util = (6-3)/6 = 0.5
	if hist[0] != 0.5 {
		t.Errorf("hist[0] = %.4f, want 0.5", hist[0])
	}
	// available=1 out of 6 → util = (6-1)/6 ≈ 0.833...
	want1 := 5.0 / 6.0
	if hist[1] != want1 {
		t.Errorf("hist[1] = %.4f, want %.4f", hist[1], want1)
	}
}
