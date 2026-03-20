package dashboard

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NimbleMarkets/ntcharts/sparkline"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

const (
	sparklineWidth  = 40
	sparklineHeight = 3
	miniSparkWidth  = 10
	miniSparkHeight = 1
	workerUtilCap   = 60
)

const numPanels = 4

// OverviewModel renders the 4-panel overview tab (cluster bar, task counts,
// worker table, recent events).
type OverviewModel struct {
	client       forgepb.ForgeSchedulerClient
	data         *forgepb.DashboardDataResponse
	err          error
	width        int
	height       int
	refreshRate  time.Duration
	focusedPanel int
	ready        bool
	throughput   *ThroughputTracker
	workerUtil   map[string][]float64 // per-worker utilization history
}

// NewOverview creates a new OverviewModel with the given gRPC client and refresh rate.
func NewOverview(client forgepb.ForgeSchedulerClient, refreshRate time.Duration) OverviewModel {
	return OverviewModel{
		client:      client,
		refreshRate: refreshRate,
		throughput:  NewThroughputTracker(defaultThroughputCap),
		workerUtil:  make(map[string][]float64),
	}
}

// TabName implements Tab.
func (m OverviewModel) TabName() string { return "Overview" }

// Init is a no-op; the parent RootModel starts the fetch and tick.
func (m OverviewModel) Init() tea.Cmd { return nil }

// Update handles incoming messages and updates the model state.
func (m OverviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "tab":
			m.focusedPanel = (m.focusedPanel + 1) % numPanels
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		return m, nil

	case DashboardDataMsg:
		m.data = msg.Data
		m.err = msg.Err
		if msg.Data != nil && msg.Data.GetTaskCounts() != nil {
			m.throughput.Record(
				int64(msg.Data.GetTaskCounts().GetCompleted()),
				time.Now(),
			)
		}
		if msg.Data != nil {
			for _, w := range msg.Data.GetWorkers() {
				id := w.GetWorkerId()
				used := float64(6-w.GetAvailableSlots()) / 6.0
				if used < 0 {
					used = 0
				}
				hist := m.workerUtil[id]
				if len(hist) >= workerUtilCap {
					hist = hist[1:]
				}
				m.workerUtil[id] = append(hist, used)
			}
		}
		return m, nil
	}

	return m, nil
}

// View renders the full 4-panel overview.
func (m OverviewModel) View() string {
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
		renderThroughputSparkline(m.throughput, contentWidth),
		renderWorkerTable(m.data.GetWorkers(), contentWidth, m.focusedPanel == 2, m.workerUtil),
		renderEvents(m.data.GetRecentEvents(), contentWidth, m.height, m.focusedPanel == 3),
	}

	footer := footerStyle.Render(fmt.Sprintf(
		"  1-5/←→ switch tab │ tab panel focus │ r refresh │ q quit │ auto: %s",
		m.refreshRate,
	))
	sections = append(sections, footer)

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func renderErrorView(err error, width int) string {
	return errorStyle.Width(width - 4).Render(fmt.Sprintf(
		"Connection Error\n\n%v\n\nCheck that the scheduler is running and the --address flag is correct.",
		err,
	))
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

func renderThroughputSparkline(tracker *ThroughputTracker, width int) string {
	label := dimStyle.Render("Throughput (tasks/sec)")

	data := tracker.Throughput()
	if len(data) == 0 {
		return lipgloss.JoinVertical(lipgloss.Left,
			label,
			dimStyle.Render("  Collecting data..."),
		)
	}

	sl := sparkline.New(sparklineWidth, sparklineHeight)
	sl.AutoMaxValue = true
	sl.PushAll(data)
	sl.DrawBraille()

	currentVal := data[len(data)-1]
	valStr := dimStyle.Render(fmt.Sprintf(" %.1f t/s", currentVal))

	chart := lipgloss.JoinHorizontal(lipgloss.Bottom, sl.View(), valStr)
	return lipgloss.JoinVertical(lipgloss.Left, label, "  "+chart)
}

func renderWorkerTable(workers []*forgepb.WorkerStatus, width int, focused bool, workerUtil map[string][]float64) string {
	header := sectionHeaderStyle.Render("WORKERS")

	if len(workers) == 0 {
		return wrapSection("", lipgloss.JoinVertical(lipgloss.Left,
			header,
			dimStyle.Render("  No workers connected"),
		), width, focused)
	}

	tableHeader := fmt.Sprintf("  %-16s %-10s %-20s %-12s %s",
		"WORKER ID", "STATUS", "UTILIZATION", "TREND", "LAST HEARTBEAT")
	lines := []string{header, dimStyle.Render(tableHeader)}

	for _, w := range workers {
		statusStr := healthyStyle.Render("healthy")
		if w.GetStatus() == "dead" {
			statusStr = deadStyle.Render("dead   ")
		}

		utilBar := renderUtilizationBar(w.GetAvailableSlots(), 6)

		miniSpark := renderMiniSparkline(workerUtil[w.GetWorkerId()])

		hbAge := "unknown"
		if w.GetLastHeartbeat() > 0 {
			age := time.Since(time.Unix(0, w.GetLastHeartbeat()))
			hbAge = formatDuration(age)
		}

		wID := w.GetWorkerId()
		if len(wID) > 16 {
			wID = wID[:16]
		}

		line := fmt.Sprintf("  %-16s %s  %s  %s  %s",
			wID, statusStr, utilBar, miniSpark, dimStyle.Render(hbAge))
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

func renderMiniSparkline(history []float64) string {
	if len(history) == 0 {
		return strings.Repeat("·", miniSparkWidth)
	}
	sl := sparkline.New(miniSparkWidth, miniSparkHeight, sparkline.WithMaxValue(1.0))
	sl.PushAll(history)
	sl.DrawBraille()
	return sl.View()
}

func renderEvents(events []*forgepb.ClusterEvent, width, height int, focused bool) string {
	header := sectionHeaderStyle.Render("RECENT EVENTS")

	if len(events) == 0 {
		return wrapSection("", lipgloss.JoinVertical(lipgloss.Left,
			header,
			dimStyle.Render("  No events yet"),
		), width, focused)
	}

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
