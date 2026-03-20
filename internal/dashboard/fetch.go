package dashboard

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

// dashboardData is the message returned by the gRPC fetch command.
type dashboardData struct {
	data *forgepb.DashboardDataResponse
	err  error
}

// fetchDashboardData returns a tea.Cmd that fetches dashboard data from the scheduler.
func fetchDashboardData(client forgepb.ForgeSchedulerClient) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resp, err := client.GetDashboardData(ctx, &forgepb.DashboardDataRequest{})
		return dashboardData{data: resp, err: err}
	}
}

// tickMsg signals that the auto-refresh interval has elapsed.
type tickMsg time.Time

// tickCmd returns a tea.Cmd that fires a tickMsg after the given duration.
func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}
