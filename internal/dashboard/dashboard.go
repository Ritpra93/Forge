package dashboard

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

const numPanels = 4

// Model is the Bubbletea model for the Forge live dashboard.
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

// New creates a new dashboard model with the given gRPC client and refresh rate.
func New(client forgepb.ForgeSchedulerClient, refreshRate time.Duration) Model {
	return Model{
		client:      client,
		refreshRate: refreshRate,
	}
}

// Init starts the initial data fetch and refresh ticker.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchDashboardData(m.client),
		tickCmd(m.refreshRate),
	)
}

// Update handles incoming messages and updates the model state.
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
		return m, nil

	case dashboardData:
		m.data = msg.data
		m.err = msg.err
		return m, nil

	case tickMsg:
		return m, tea.Batch(
			fetchDashboardData(m.client),
			tickCmd(m.refreshRate),
		)
	}

	return m, nil
}

// View renders the full dashboard to a string.
func (m Model) View() string {
	if !m.ready {
		return "Loading..."
	}
	if m.err != nil {
		return renderErrorView(m.err, m.width)
	}
	if m.data == nil {
		return "Waiting for data..."
	}

	contentWidth := m.width - 4
	if contentWidth < 40 {
		contentWidth = 40
	}

	sections := []string{
		renderClusterBar(m.data.GetCluster(), contentWidth, m.focusedPanel == 0),
		renderTaskCounts(m.data.GetTaskCounts(), contentWidth, m.focusedPanel == 1),
		renderWorkerTable(m.data.GetWorkers(), contentWidth, m.focusedPanel == 2),
		renderEvents(m.data.GetRecentEvents(), contentWidth, m.height, m.focusedPanel == 3),
	}

	footer := footerStyle.Render(fmt.Sprintf(
		"  q quit │ r refresh │ tab focus │ refresh: %s",
		m.refreshRate,
	))
	sections = append(sections, footer)

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func renderErrorView(err error, width int) string {
	box := errorStyle.Width(width - 4).Render(fmt.Sprintf(
		"Connection Error\n\n%v\n\nCheck that the scheduler is running and the --address flag is correct.",
		err,
	))
	return box
}

func renderClusterBar(cluster *forgepb.ClusterInfoResponse, width int, focused bool) string {
	if cluster == nil {
		return wrapSection("CLUSTER", "No cluster data", width, focused)
	}

	title := titleStyle.Render(" FORGE DASHBOARD ")
	leader := fmt.Sprintf("  Leader: %s %s",
		leaderDotStyle.Render("●"),
		cluster.GetLeaderId(),
	)

	var nodeDots []string
	for _, n := range cluster.GetNodes() {
		dot := followerDotStyle.Render("●")
		if n.GetState() == "leader" {
			dot = leaderDotStyle.Render("●")
		}
		nodeDots = append(nodeDots, fmt.Sprintf("%s %s", dot, n.GetId()))
	}
	nodes := fmt.Sprintf("  Nodes (%d): %s", len(cluster.GetNodes()), strings.Join(nodeDots, "  "))

	content := lipgloss.JoinVertical(lipgloss.Left, title, leader, nodes)
	return wrapSection("", content, width, focused)
}

func renderTaskCounts(counts *forgepb.TaskCounts, width int, focused bool) string {
	if counts == nil {
		return wrapSection("TASKS", "No task data", width, focused)
	}

	counters := []string{
		pendingStyle.Render(fmt.Sprintf("Pending: %d", counts.GetPending())),
		runningStyle.Render(fmt.Sprintf("Running: %d", counts.GetRunning())),
		completedStyle.Render(fmt.Sprintf("Completed: %d", counts.GetCompleted())),
		failedStyle.Render(fmt.Sprintf("Failed: %d", counts.GetFailed())),
		retryingStyle.Render(fmt.Sprintf("Retrying: %d", counts.GetRetrying())),
		deadLetterStyle.Render(fmt.Sprintf("Dead Letter: %d", counts.GetDeadLetter())),
	}

	total := counts.GetPending() + counts.GetRunning() + counts.GetCompleted() +
		counts.GetFailed() + counts.GetRetrying() + counts.GetDeadLetter()
	header := sectionHeaderStyle.Render("TASKS") + dimStyle.Render(fmt.Sprintf("  (total: %d)", total))

	return wrapSection("", lipgloss.JoinVertical(lipgloss.Left,
		header,
		"  "+strings.Join(counters, "  │  "),
	), width, focused)
}

func renderWorkerTable(workers []*forgepb.WorkerStatus, width int, focused bool) string {
	header := sectionHeaderStyle.Render("WORKERS")

	if len(workers) == 0 {
		return wrapSection("", lipgloss.JoinVertical(lipgloss.Left,
			header,
			dimStyle.Render("  No workers connected"),
		), width, focused)
	}

	// Table header
	tableHeader := fmt.Sprintf("  %-16s %-10s %-20s %s",
		"WORKER ID", "STATUS", "UTILIZATION", "LAST HEARTBEAT")
	lines := []string{header, dimStyle.Render(tableHeader)}

	for _, w := range workers {
		statusStr := healthyStyle.Render("healthy")
		if w.GetStatus() == "dead" {
			statusStr = deadStyle.Render("dead   ")
		}

		utilBar := renderUtilizationBar(w.GetAvailableSlots(), 6)

		hbAge := "unknown"
		if w.GetLastHeartbeat() > 0 {
			age := time.Since(time.Unix(0, w.GetLastHeartbeat()))
			hbAge = formatDuration(age)
		}

		wID := w.GetWorkerId()
		if len(wID) > 16 {
			wID = wID[:16]
		}

		line := fmt.Sprintf("  %-16s %s  %s  %s",
			wID, statusStr, utilBar, dimStyle.Render(hbAge))
		lines = append(lines, line)
	}

	return wrapSection("", lipgloss.JoinVertical(lipgloss.Left, lines...), width, focused)
}

func renderUtilizationBar(availableSlots int32, totalSlots int32) string {
	if totalSlots <= 0 {
		totalSlots = 6
	}
	used := totalSlots - availableSlots
	if used < 0 {
		used = 0
	}
	if used > totalSlots {
		used = totalSlots
	}

	filled := strings.Repeat("█", int(used))
	empty := strings.Repeat("░", int(totalSlots-used))
	return fmt.Sprintf("[%s%s] %d/%d", filled, empty, used, totalSlots)
}

func renderEvents(events []*forgepb.ClusterEvent, width, height int, focused bool) string {
	header := sectionHeaderStyle.Render("RECENT EVENTS")

	if len(events) == 0 {
		return wrapSection("", lipgloss.JoinVertical(lipgloss.Left,
			header,
			dimStyle.Render("  No events yet"),
		), width, focused)
	}

	// Show events in reverse chronological order (newest first)
	maxEvents := 15
	if height > 0 && height < 40 {
		maxEvents = 8
	}
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}

	lines := []string{header}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		ts := time.Unix(0, ev.GetTimestamp())
		age := formatDuration(time.Since(ts))
		badge := eventStyle(ev.GetType()).Render(fmt.Sprintf("[%s]", ev.GetType()))
		line := fmt.Sprintf("  %s %s %s",
			dimStyle.Render(age),
			badge,
			ev.GetMessage(),
		)
		if width > 0 && len(line) > width {
			line = line[:width-1] + "…"
		}
		lines = append(lines, line)
	}

	return wrapSection("", lipgloss.JoinVertical(lipgloss.Left, lines...), width, focused)
}

func wrapSection(title, content string, width int, focused bool) string {
	style := normalBorderStyle.Width(width)
	if focused {
		style = focusBorderStyle.Width(width)
	}
	if title != "" {
		return style.Render(sectionHeaderStyle.Render(title) + "\n" + content)
	}
	return style.Render(content)
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return "<1s ago"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}
