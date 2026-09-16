package dashboard

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

// DashboardDataMsg is the message returned by the gRPC fetch command.
type DashboardDataMsg struct {
	Data *forgepb.DashboardDataResponse
	Err  error
}

// ListTasksMsg is the message returned by the ListTasks gRPC fetch command.
type ListTasksMsg struct {
	Tasks []*forgepb.TaskStatusResponse
	Err   error
}

// TickMsg signals that the auto-refresh interval has elapsed.
type TickMsg time.Time

// Tab is the interface that all dashboard tab models must implement.
type Tab interface {
	tea.Model
	TabName() string
}
