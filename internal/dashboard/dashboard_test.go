package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"google.golang.org/grpc"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

// mockClient implements forgepb.ForgeSchedulerClient for testing.
type mockClient struct {
	forgepb.ForgeSchedulerClient
	resp *forgepb.DashboardDataResponse
	err  error
}

func (m *mockClient) GetDashboardData(_ context.Context, _ *forgepb.DashboardDataRequest, _ ...grpc.CallOption) (*forgepb.DashboardDataResponse, error) {
	return m.resp, m.err
}

func TestNewModel(t *testing.T) {
	client := &mockClient{}
	m := New(client, time.Second)

	cmd := m.Init()
	if cmd == nil {
		t.Error("Init() returned nil cmd")
	}
}

func TestViewNotReady(t *testing.T) {
	m := NewOverview(&mockClient{}, time.Second)
	view := m.View()
	if view != "Loading..." {
		t.Errorf("got %q, want %q", view, "Loading...")
	}
}

func TestViewWithError(t *testing.T) {
	m := NewOverview(&mockClient{}, time.Second)
	m.ready = true
	m.err = errors.New("connection refused")

	view := m.View()
	if view == "" {
		t.Error("View() returned empty string for error state")
	}
	if len(view) < 10 {
		t.Errorf("expected error view with content, got %q", view)
	}
}

func TestViewWithData(t *testing.T) {
	m := NewOverview(&mockClient{}, time.Second)
	m.ready = true
	m.width = 120
	m.height = 40
	m.data = &forgepb.DashboardDataResponse{
		Cluster: &forgepb.ClusterInfoResponse{
			LeaderId:      "scheduler-1",
			LeaderAddress: "scheduler-1:7000",
			Nodes: []*forgepb.NodeInfo{
				{Id: "scheduler-1", Address: "scheduler-1:7000", State: "leader"},
				{Id: "scheduler-2", Address: "scheduler-2:7000", State: "follower"},
			},
		},
		TaskCounts: &forgepb.TaskCounts{
			Pending:   5,
			Running:   3,
			Completed: 100,
			Failed:    2,
		},
		Workers: []*forgepb.WorkerStatus{
			{
				WorkerId:       "worker-1",
				AvailableSlots: 2,
				LastHeartbeat:  time.Now().UnixNano(),
				Status:         "healthy",
			},
		},
		RecentEvents: []*forgepb.ClusterEvent{
			{
				Timestamp: time.Now().UnixNano(),
				Type:      "task_completed",
				Message:   "Task abc completed",
			},
		},
	}

	view := m.View()
	if view == "" {
		t.Error("View() returned empty string with valid data")
	}
}

func TestUpdateQuitKey(t *testing.T) {
	m := New(&mockClient{}, time.Second)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if updated == nil {
		t.Error("Update returned nil model")
	}
	if cmd == nil {
		t.Error("expected quit cmd, got nil")
	}
}

func TestUpdateCtrlC(t *testing.T) {
	m := New(&mockClient{}, time.Second)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("expected quit cmd for ctrl+c, got nil")
	}
}

func TestUpdateTabKey(t *testing.T) {
	m := NewOverview(&mockClient{}, time.Second)
	if m.focusedPanel != 0 {
		t.Fatalf("initial focusedPanel should be 0, got %d", m.focusedPanel)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	um := updated.(OverviewModel)
	if um.focusedPanel != 1 {
		t.Errorf("expected focusedPanel 1 after first tab, got %d", um.focusedPanel)
	}

	// Wrap around
	um.focusedPanel = numPanels - 1
	updated2, _ := um.Update(tea.KeyMsg{Type: tea.KeyTab})
	um2 := updated2.(OverviewModel)
	if um2.focusedPanel != 0 {
		t.Errorf("expected focusedPanel 0 after wrap, got %d", um2.focusedPanel)
	}
}

func TestUpdateWindowSizeMsg(t *testing.T) {
	m := New(&mockClient{}, time.Second)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	um := updated.(RootModel)
	if um.width != 120 {
		t.Errorf("expected width=120, got %d", um.width)
	}
	if um.height != 40 {
		t.Errorf("expected height=40, got %d", um.height)
	}

	// All tabs must have received the size — overview tab should now be ready.
	overview, ok := um.tabs[0].(OverviewModel)
	if !ok {
		t.Fatal("tabs[0] is not OverviewModel")
	}
	if !overview.ready {
		t.Error("expected overview tab ready=true after WindowSizeMsg propagation")
	}
}

func TestFetchConnectionError(t *testing.T) {
	client := &mockClient{err: errors.New("connection refused")}
	cmd := fetchDashboardData(client)
	msg := cmd()

	dd, ok := msg.(DashboardDataMsg)
	if !ok {
		t.Fatalf("expected DashboardDataMsg, got %T", msg)
	}
	if dd.Err == nil {
		t.Error("expected non-nil error from fetch with broken client")
	}
}
