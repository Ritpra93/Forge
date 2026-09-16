package dashboard

import (
	"fmt"
	"strings"
	"time"

	"github.com/NimbleMarkets/ntcharts/sparkline"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

const numPanels = 4

// throughputBufCap is the maximum number of tasks/sec samples retained.
const throughputBufCap = 60

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

	// Throughput tracking: ring buffers of capacity 60.
	completedSnaps   []int64            // raw completed counts (last 60 snapshots)
	throughputBuf    []float64          // tasks/sec values (len = len(completedSnaps)-1)
	workerUtilHistory map[string][]float64 // per-worker available-slots history
}

// NewOverview creates a new OverviewModel with the given gRPC client and refresh rate.
func NewOverview(client forgepb.ForgeSchedulerClient, refreshRate time.Duration) OverviewModel {
	return OverviewModel{
		client:            client,
		refreshRate:       refreshRate,
		workerUtilHistory: make(map[string][]float64),
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
		if msg.Data != nil {
			m.recordThroughput(int64(msg.Data.GetTaskCounts().GetCompleted()))
			m.recordWorkerUtilization(msg.Data.GetWorkers())
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
		renderThroughputSparkline(m.throughputBuf),
		renderWorkerTable(m.data.GetWorkers(), m.workerUtilHistory, contentWidth, m.focusedPanel == 2),
		renderEvents(m.data.GetRecentEvents(), contentWidth, m.height, m.focusedPanel == 3),
	}

	footer := footerStyle.Render(fmt.Sprintf(
		"  1-5/←→ switch tab │ tab panel focus │ r refresh │ q quit │ auto: %s",
		m.refreshRate,
	))
	sections = append(sections, footer)

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

// recordThroughput appends a new completed-count snapshot and computes tasks/sec.
func (m *OverviewModel) recordThroughput(completed int64) {
	secs := m.refreshRate.Seconds()
	if secs <= 0 {
		secs = 2.0
	}
	if len(m.completedSnaps) > 0 {
		prev := m.completedSnaps[len(m.completedSnaps)-1]
		delta := float64(completed - prev)
		if delta < 0 {
			delta = 0
		}
		m.throughputBuf = append(m.throughputBuf, delta/secs)
		if len(m.throughputBuf) > throughputBufCap {
			m.throughputBuf = m.throughputBuf[1:]
		}
	}
	m.completedSnaps = append(m.completedSnaps, completed)
	if len(m.completedSnaps) > throughputBufCap {
		m.completedSnaps = m.completedSnaps[1:]
	}
}

// recordWorkerUtilization records available-slot fractions per worker.
func (m *OverviewModel) recordWorkerUtilization(workers []*forgepb.WorkerStatus) {
	const totalSlots = 6
	for _, w := range workers {
		id := w.GetWorkerId()
		used := float64(totalSlots-w.GetAvailableSlots()) / totalSlots
		if used < 0 {
			used = 0
		}
		hist := m.workerUtilHistory[id]
		hist = append(hist, used)
		if len(hist) > throughputBufCap {
			hist = hist[1:]
		}
		m.workerUtilHistory[id] = hist
	}
}

// renderThroughputSparkline renders a 40×3 braille sparkline for tasks/sec.
func renderThroughputSparkline(throughputBuf []float64) string {
	sl := sparkline.New(40, 3, sparkline.WithStyle(completedStyle))
	sl.PushAll(throughputBuf)
	sl.DrawBraille()

	var current float64
	if len(throughputBuf) > 0 {
		current = throughputBuf[len(throughputBuf)-1]
	}
	chart := lipgloss.JoinHorizontal(lipgloss.Center,
		sl.View(),
		fmt.Sprintf("  %.2f/s", current),
	)
	label := dimStyle.Render("  Throughput (tasks/sec)")
	return lipgloss.JoinVertical(lipgloss.Left, label, chart)
}

// renderMiniSparkline renders a 10×1 braille sparkline for a worker's utilization history.
func renderMiniSparkline(history []float64) string {
	sl := sparkline.New(10, 1, sparkline.WithStyle(runningStyle))
	sl.PushAll(history)
	sl.DrawBraille()
	return sl.View()
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

func renderWorkerTable(workers []*forgepb.WorkerStatus, utilHistory map[string][]float64, width int, focused bool) string {
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
		mini := renderMiniSparkline(utilHistory[w.GetWorkerId()])

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
			wID, statusStr, utilBar, mini, dimStyle.Render(hbAge))
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
