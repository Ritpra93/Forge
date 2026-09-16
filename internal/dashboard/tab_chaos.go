package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Ritpra93/forge/internal/chaos"
)

// chaosRefreshInterval is how often the container list auto-refreshes.
const chaosRefreshInterval = 5 * time.Second

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

// chaosContainersMsg carries a fresh container list (or an error).
type chaosContainersMsg struct {
	containers []chaos.ForgeContainer
	err        error
}

// chaosActionResultMsg carries the outcome of a destructive action.
type chaosActionResultMsg struct {
	label string
	err   error
}

// chaosTickMsg fires the periodic container-list refresh.
type chaosTickMsg time.Time

// ---------------------------------------------------------------------------
// Pending action / confirmation state
// ---------------------------------------------------------------------------

type chaosAction int

const (
	actionNone chaosAction = iota
	actionStop
	actionKill
	actionPause
	actionUnpause
	actionRestart
	actionLatency
	actionPartition
	actionClearNet
)

func (a chaosAction) label(name string) string {
	switch a {
	case actionStop:
		return fmt.Sprintf("Stop %s?", name)
	case actionKill:
		return fmt.Sprintf("Kill %s?", name)
	case actionPause:
		return fmt.Sprintf("Pause %s?", name)
	case actionUnpause:
		return fmt.Sprintf("Unpause %s?", name)
	case actionRestart:
		return fmt.Sprintf("Restart %s?", name)
	case actionLatency:
		return fmt.Sprintf("Inject 500ms latency into %s?", name)
	case actionPartition:
		return fmt.Sprintf("Network-partition %s (100%% loss)?", name)
	case actionClearNet:
		return fmt.Sprintf("Clear network chaos on %s?", name)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Last-action result
// ---------------------------------------------------------------------------

type lastResult struct {
	label string
	err   error
	at    time.Time
}

func (r lastResult) render() string {
	if r.label == "" {
		return ""
	}
	age := formatDuration(time.Since(r.at))
	if r.err != nil {
		return failedStyle.Render(fmt.Sprintf("✗ %s: %v (%s)", r.label, r.err, age))
	}
	return completedStyle.Render(fmt.Sprintf("✓ %s (%s)", r.label, age))
}

// ---------------------------------------------------------------------------
// ChaosModel
// ---------------------------------------------------------------------------

// ChaosModel is the "Chaos" tab: live Docker container list with destructive
// action keys and confirmation prompts.
type ChaosModel struct {
	cc         *chaos.ChaosController
	ccErr      error // set if the Docker client could not be created
	containers []chaos.ForgeContainer
	fetchErr   error

	cursor     int
	width      int
	height     int
	ready      bool

	pending    chaosAction
	last       lastResult
}

// NewChaos creates a ChaosModel. If the Docker client cannot be initialised
// (daemon not running) the tab renders a friendly error instead of panicking.
func NewChaos() ChaosModel {
	cc, err := chaos.New()
	return ChaosModel{cc: cc, ccErr: err}
}

// TabName implements Tab.
func (m ChaosModel) TabName() string { return "Chaos" }

// Init starts the first container-list fetch and the refresh ticker.
func (m ChaosModel) Init() tea.Cmd {
	if m.ccErr != nil {
		return nil
	}
	return tea.Batch(m.fetchContainers(), chaosTick())
}

// Update handles all messages for the chaos tab.
func (m ChaosModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		return m, nil

	case chaosContainersMsg:
		m.fetchErr = msg.err
		if msg.err == nil {
			m.containers = msg.containers
			if m.cursor >= len(m.containers) && len(m.containers) > 0 {
				m.cursor = len(m.containers) - 1
			}
		}
		return m, nil

	case chaosActionResultMsg:
		m.last = lastResult{label: msg.label, err: msg.err, at: time.Now()}
		m.pending = actionNone
		// Refresh list after any action.
		return m, m.fetchContainers()

	case chaosTickMsg:
		return m, tea.Batch(m.fetchContainers(), chaosTick())

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

// handleKey routes key presses to navigation, confirmation, or action dispatch.
func (m ChaosModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// --- confirmation mode ---
	if m.pending != actionNone {
		switch msg.String() {
		case "y", "Y":
			return m.executeAction()
		case "n", "N", "esc":
			m.pending = actionNone
		}
		return m, nil
	}

	// --- normal mode ---
	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.containers)-1 {
			m.cursor++
		}
	case "s":
		m.pending = actionStop
	case "K":
		m.pending = actionKill
	case "p":
		m.pending = actionPause
	case "u":
		m.pending = actionUnpause
	case "r":
		m.pending = actionRestart
	case "l":
		m.pending = actionLatency
	case "n":
		m.pending = actionPartition
	case "c":
		m.pending = actionClearNet
	}

	return m, nil
}

// selectedContainer returns the currently highlighted container, or nil.
func (m ChaosModel) selectedContainer() *chaos.ForgeContainer {
	if len(m.containers) == 0 || m.cursor >= len(m.containers) {
		return nil
	}
	c := m.containers[m.cursor]
	return &c
}

// executeAction runs the confirmed destructive action as an async tea.Cmd.
func (m ChaosModel) executeAction() (tea.Model, tea.Cmd) {
	sel := m.selectedContainer()
	if sel == nil {
		m.pending = actionNone
		return m, nil
	}

	id := sel.ID
	name := sel.Name
	action := m.pending

	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		var err error
		var label string

		switch action {
		case actionStop:
			err = m.cc.StopContainer(ctx, id)
			label = fmt.Sprintf("Stopped %s", name)
		case actionKill:
			err = m.cc.KillContainer(ctx, id)
			label = fmt.Sprintf("Killed %s", name)
		case actionPause:
			err = m.cc.PauseContainer(ctx, id)
			label = fmt.Sprintf("Paused %s", name)
		case actionUnpause:
			err = m.cc.UnpauseContainer(ctx, id)
			label = fmt.Sprintf("Unpaused %s", name)
		case actionRestart:
			err = m.cc.RestartContainer(ctx, id)
			label = fmt.Sprintf("Restarted %s", name)
		case actionLatency:
			err = m.cc.InjectLatency(ctx, id, 500)
			label = fmt.Sprintf("Injected 500ms latency into %s", name)
		case actionPartition:
			err = m.cc.PartitionContainer(ctx, id)
			label = fmt.Sprintf("Partitioned %s", name)
		case actionClearNet:
			err = m.cc.ClearNetworkChaos(ctx, id)
			label = fmt.Sprintf("Cleared network chaos on %s", name)
		}

		return chaosActionResultMsg{label: label, err: err}
	}
}

// fetchContainers returns a tea.Cmd that asynchronously lists Forge containers.
func (m ChaosModel) fetchContainers() tea.Cmd {
	if m.cc == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		containers, err := m.cc.ListForgeContainers(ctx)
		return chaosContainersMsg{containers: containers, err: err}
	}
}

// chaosTick returns a tea.Cmd that fires chaosTickMsg after chaosRefreshInterval.
func chaosTick() tea.Cmd {
	return tea.Tick(chaosRefreshInterval, func(t time.Time) tea.Msg {
		return chaosTickMsg(t)
	})
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

var (
	dotRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("#2ecc71")).Render("●")
	dotPaused  = lipgloss.NewStyle().Foreground(lipgloss.Color("#f39c12")).Render("●")
	dotStopped = lipgloss.NewStyle().Foreground(lipgloss.Color("#e74c3c")).Render("●")

	chaosCursorStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#1e90ff", Dark: "#5dade2"})
	chaosSelectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("#334"))
)

// View renders the chaos tab.
func (m ChaosModel) View() string {
	if !m.ready {
		return "Loading..."
	}
	if m.ccErr != nil {
		return errorStyle.Width(m.width - 4).Render(fmt.Sprintf(
			"Docker Unavailable\n\n%v\n\nStart the Docker daemon and switch back to this tab.",
			m.ccErr,
		))
	}

	lines := []string{
		sectionHeaderStyle.Render("CHAOS ENGINEERING") + dimStyle.Render(
			fmt.Sprintf("  %d containers  auto-refresh: 5s", len(m.containers)),
		),
	}

	if m.fetchErr != nil {
		lines = append(lines, failedStyle.Render(fmt.Sprintf("  fetch error: %v", m.fetchErr)))
	}

	if len(m.containers) == 0 {
		lines = append(lines, dimStyle.Render("\n  No Forge containers found. Start the cluster first."))
	} else {
		// Table header
		lines = append(lines, dimStyle.Render(fmt.Sprintf(
			"  %-3s %-30s %-10s %-10s",
			"", "NAME", "STATE", "ROLE",
		)))

		for i, ct := range m.containers {
			dot := stateIndicator(ct.State)
			cursor := "  "
			style := lipgloss.NewStyle()
			if i == m.cursor {
				cursor = chaosCursorStyle.Render("▶ ")
				style = chaosSelectedStyle
			}
			line := style.Render(fmt.Sprintf("%s%-30s %-10s %-10s",
				cursor,
				truncate(ct.Name, 28),
				ct.State,
				inferRole(ct.Name),
			))
			// Prepend dot separately so style doesn't clobber its color
			lines = append(lines, dot+" "+line)
		}
	}

	// Spacer
	lines = append(lines, "")

	// Confirmation prompt or help bar
	if m.pending != actionNone {
		if sel := m.selectedContainer(); sel != nil {
			prompt := m.pending.label(sel.Name) + "  [y/n]"
			lines = append(lines, failedStyle.Render("  ⚡ "+prompt))
		}
	} else {
		lines = append(lines, dimStyle.Render(
			"  s stop  K kill  p pause  u unpause  r restart  "+
				"l latency  n partition  c clear-net  ↑↓ navigate",
		))
	}

	// Last action result
	if result := m.last.render(); result != "" {
		lines = append(lines, "  "+result)
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// stateIndicator returns a colored dot for the given Docker container state.
func stateIndicator(state string) string {
	switch strings.ToLower(state) {
	case "running":
		return dotRunning
	case "paused":
		return dotPaused
	default:
		return dotStopped
	}
}

// inferRole guesses whether a container is a scheduler or worker from its name.
func inferRole(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "scheduler"):
		return "scheduler"
	case strings.Contains(lower, "worker"):
		return "worker"
	default:
		return "—"
	}
}

// truncate shortens s to at most n runes, appending "…" if cut.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
