package dashboard

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

// fetchDashboardData returns a tea.Cmd that fetches dashboard data from the scheduler.
func fetchDashboardData(client forgepb.ForgeSchedulerClient) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resp, err := client.GetDashboardData(ctx, &forgepb.DashboardDataRequest{})
		return DashboardDataMsg{Data: resp, Err: err}
	}
}

// fetchListTasks returns a tea.Cmd that fetches the full task list from the scheduler.
func fetchListTasks(client forgepb.ForgeSchedulerClient) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resp, err := client.ListTasks(ctx, &forgepb.ListTasksRequest{})
		if err != nil {
			return ListTasksMsg{Err: err}
		}
		return ListTasksMsg{Tasks: resp.GetTasks()}
	}
}

// tickCmd returns a tea.Cmd that fires a TickMsg after the given duration.
func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}
