package dashboard

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/evertras/bubble-table/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

// Column keys for the task table.
const (
	colKeyID      = "id"
	colKeyStatus  = "status"
	colKeyWorker  = "worker"
	colKeyRetries = "retries"
	colKeyAge     = "age"

	// Hidden key used to attach the full task proto to each row.
	colKeyTask = "_task"
)

// TasksModel is the "Tasks" tab: a sortable, filterable bubble-table of all
// tasks, with a drill-down detail overlay on Enter.
type TasksModel struct {
	client      forgepb.ForgeSchedulerClient
	refreshRate time.Duration
	width       int
	height      int
	ready       bool

	tbl        table.Model
	tasks      []*forgepb.TaskStatusResponse
	err        error
	showDetail bool
	detail     TaskDetailModel
}

// NewTasks creates a new TasksModel.
func NewTasks(client forgepb.ForgeSchedulerClient, refreshRate time.Duration) TasksModel {
	cols := []table.Column{
		table.NewColumn(colKeyID, "ID", 14),
		table.NewColumn(colKeyStatus, "STATUS", 12),
		table.NewColumn(colKeyWorker, "WORKER", 18),
		table.NewColumn(colKeyRetries, "RETRIES", 8),
		table.NewColumn(colKeyAge, "AGE", 12),
	}

	tbl := table.New(cols).
		Focused(true).
		Filtered(true).
		WithFuzzyFilter().
		HeaderStyle(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#333333", Dark: "#aaaaaa"})).
		WithBaseStyle(lipgloss.NewStyle().Align(lipgloss.Left)).
		HighlightStyle(lipgloss.NewStyle().Background(lipgloss.Color("#334")))

	return TasksModel{
		client:      client,
		refreshRate: refreshRate,
		tbl:         tbl,
	}
}

// TabName implements Tab.
func (m TasksModel) TabName() string { return "Tasks" }

// Init is a no-op; the parent starts the fetch.
func (m TasksModel) Init() tea.Cmd { return nil }

// Update handles incoming messages.
func (m TasksModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Escape / q in detail view closes it.
	if m.showDetail {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "esc", "q":
				m.showDetail = false
				return m, nil
			}
		}
		// Don't route anything else while detail is open.
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		// Reserve top header line + bottom footer line.
		tableHeight := msg.Height - 4
		if tableHeight < 4 {
			tableHeight = 4
		}
		m.tbl = m.tbl.WithTargetWidth(msg.Width - 2).WithPageSize(tableHeight)
		return m, nil

	case ListTasksMsg:
		m.err = msg.Err
		if msg.Err == nil {
			m.tasks = msg.Tasks
			m.tbl = m.tbl.WithRows(buildTaskRows(msg.Tasks))
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			row := m.tbl.HighlightedRow()
			if task, ok := row.Data[colKeyTask].(*forgepb.TaskStatusResponse); ok && task != nil {
				m.detail = newTaskDetail(task)
				m.showDetail = true
				return m, nil
			}
		}
	}

	// Forward all other keys to the table (navigation, filtering).
	var cmd tea.Cmd
	m.tbl, cmd = m.tbl.Update(msg)
	return m, cmd
}

// View renders the task table or the detail overlay.
func (m TasksModel) View() string {
	if !m.ready {
		return "Loading..."
	}
	if m.err != nil {
		return renderErrorView(m.err, m.width)
	}

	visible := len(m.tbl.GetVisibleRows())
	total := len(m.tasks)
	filter := m.tbl.GetCurrentFilter()

	headerParts := []string{
		sectionHeaderStyle.Render("TASKS"),
		dimStyle.Render(fmt.Sprintf("  %d / %d tasks", visible, total)),
	}
	if filter != "" {
		headerParts = append(headerParts, runningStyle.Render(fmt.Sprintf("  filter: %q", filter)))
	} else {
		headerParts = append(headerParts, dimStyle.Render("  / to filter  ↑↓ navigate  enter drill-down"))
	}
	header := strings.Join(headerParts, "")

	body := lipgloss.JoinVertical(lipgloss.Left,
		header,
		m.tbl.View(),
	)

	if m.showDetail {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.detail.View())
	}

	return body
}

// buildTaskRows converts a slice of TaskStatusResponse into bubble-table rows.
func buildTaskRows(tasks []*forgepb.TaskStatusResponse) []table.Row {
	rows := make([]table.Row, 0, len(tasks))
	for _, t := range tasks {
		rows = append(rows, table.NewRow(table.RowData{
			colKeyID:     shortID(t.GetTaskId()),
			colKeyStatus: table.NewStyledCell(t.GetStatus(), statusStyle(t.GetStatus())),
			colKeyWorker: shortWorker(t.GetAssignedWorker()),
			colKeyRetries: fmt.Sprintf("%d", t.GetRetryCount()),
			colKeyAge:    ageString(t.GetCreatedAt()),
			colKeyTask:   t,
		}))
	}
	return rows
}

// shortID returns the first 12 characters of a UUID.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// shortWorker truncates worker IDs to fit the column.
func shortWorker(w string) string {
	if w == "" {
		return "—"
	}
	if len(w) > 16 {
		return w[:16]
	}
	return w
}

// ageString formats a Unix-nano timestamp as a human-readable age.
func ageString(nanos int64) string {
	if nanos == 0 {
		return "—"
	}
	return formatDuration(time.Since(time.Unix(0, nanos)))
}

// statusStyle returns the lipgloss style matching the task status string.
func statusStyle(status string) lipgloss.Style {
	switch status {
	case "pending":
		return pendingStyle
	case "running":
		return runningStyle
	case "completed":
		return completedStyle
	case "failed":
		return failedStyle
	case "retrying":
		return retryingStyle
	case "dead_letter":
		return deadLetterStyle
	default:
		return dimStyle
	}
}

// ---------------------------------------------------------------------------
// TaskDetailModel — Prompt 3.2
// ---------------------------------------------------------------------------

// TaskDetailModel renders a bordered detail panel for a single task.
type TaskDetailModel struct {
	task *forgepb.TaskStatusResponse
}

// newTaskDetail creates a TaskDetailModel for the given task.
func newTaskDetail(task *forgepb.TaskStatusResponse) TaskDetailModel {
	return TaskDetailModel{task: task}
}

// View renders the detail overlay box (~60 chars wide, auto-height).
func (d TaskDetailModel) View() string {
	if d.task == nil {
		return ""
	}
	t := d.task
	const boxWidth = 62

	lines := []string{
		titleStyle.Render(" TASK DETAIL "),
		"",
		field("ID", t.GetTaskId()),
		field("Status", statusStyle(t.GetStatus()).Render(t.GetStatus())),
		field("Worker", workerOrDash(t.GetAssignedWorker())),
		field("Retries", fmt.Sprintf("%d", t.GetRetryCount())),
		field("Created", humanTime(t.GetCreatedAt())),
		field("Updated", humanTime(t.GetUpdatedAt())),
	}

	payload := prettyPayload(t)
	if payload != "" {
		lines = append(lines, "", dimStyle.Render("Payload:"), payload)
	}

	lines = append(lines, "", dimStyle.Render("esc / q  close"))

	content := strings.Join(lines, "\n")

	return focusBorderStyle.
		Width(boxWidth).
		Padding(1, 2).
		Render(content)
}

func field(label, value string) string {
	return fmt.Sprintf("%s  %s",
		dimStyle.Render(fmt.Sprintf("%-12s", label+":")),
		value,
	)
}

func workerOrDash(w string) string {
	if w == "" {
		return "—"
	}
	return w
}

func humanTime(nanos int64) string {
	if nanos == 0 {
		return "—"
	}
	t := time.Unix(0, nanos)
	age := formatDuration(time.Since(t))
	return fmt.Sprintf("%s  (%s)", t.Format("2006-01-02 15:04:05"), age)
}

// prettyPayload pretty-prints any JSON embedded in the task, truncated to 10 lines.
// TaskStatusResponse has no payload field, so this is a no-op placeholder that
// returns empty — ready for when the proto is extended.
func prettyPayload(t *forgepb.TaskStatusResponse) string {
	// TaskStatusResponse does not carry a raw payload field.
	// If the proto is extended, marshal t to JSON and pretty-print it here.
	raw, err := json.MarshalIndent(map[string]any{
		"task_id":         t.GetTaskId(),
		"status":          t.GetStatus(),
		"assigned_worker": t.GetAssignedWorker(),
		"retry_count":     t.GetRetryCount(),
	}, "", "  ")
	if err != nil {
		return ""
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) > 10 {
		lines = lines[:10]
		lines = append(lines, dimStyle.Render("  … (truncated)"))
	}
	return strings.Join(lines, "\n")
}
