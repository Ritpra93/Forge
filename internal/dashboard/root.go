package dashboard

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

// tabBarHeight is the number of terminal lines consumed by the tab bar
// (1 content line + 1 top border + 1 bottom border).
const tabBarHeight = 3

// RootModel is the top-level Bubbletea model that manages all dashboard tabs.
type RootModel struct {
	client      forgepb.ForgeSchedulerClient
	refreshRate time.Duration
	tabs        []Tab
	activeTab   int
	width       int
	height      int
}

// New creates a new RootModel with the given gRPC client and refresh rate.
func New(client forgepb.ForgeSchedulerClient, refreshRate time.Duration) RootModel {
	return RootModel{
		client:      client,
		refreshRate: refreshRate,
		tabs: []Tab{
			NewOverview(client, refreshRate),
			NewTasks(client, refreshRate),
			placeholderTab{name: "Workers"},
			placeholderTab{name: "Cluster"},
			NewChaos(),
		},
	}
}

// Init starts the initial data fetch and refresh ticker.
func (m RootModel) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.tabs)+2)
	for _, tab := range m.tabs {
		if cmd := tab.Init(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	cmds = append(cmds, fetchDashboardData(m.client), fetchListTasks(m.client), tickCmd(m.refreshRate))
	return tea.Batch(cmds...)
}

// Update handles messages, propagates WindowSizeMsg and DashboardDataMsg to all
// tabs, routes other messages to the active tab.
func (m RootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			return m, fetchDashboardData(m.client)
		case "1", "2", "3", "4", "5":
			idx := int(msg.Runes[0] - '1')
			if idx < len(m.tabs) {
				m.activeTab = idx
			}
			return m, nil
		case "left":
			if m.activeTab > 0 {
				m.activeTab--
			}
			return m, nil
		case "right":
			if m.activeTab < len(m.tabs)-1 {
				m.activeTab++
			}
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Subtract the tab bar height so tabs size their content correctly.
		adjusted := tea.WindowSizeMsg{Width: msg.Width, Height: msg.Height - tabBarHeight}
		return m, m.propagateToAll(adjusted)

	case DashboardDataMsg:
		return m, m.propagateToAll(msg)

	case ListTasksMsg:
		return m, m.propagateToAll(msg)

	case TickMsg:
		return m, tea.Batch(
			fetchDashboardData(m.client),
			fetchListTasks(m.client),
			tickCmd(m.refreshRate),
		)
	}

	// Route all other messages (key presses for panel focus, etc.) to the active tab.
	updated, cmd := m.tabs[m.activeTab].Update(msg)
	m.tabs[m.activeTab] = updated.(Tab)
	return m, cmd
}

// propagateToAll sends msg to every tab and batches their returned commands.
func (m *RootModel) propagateToAll(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for i, tab := range m.tabs {
		updated, cmd := tab.Update(msg)
		m.tabs[i] = updated.(Tab)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// View renders the tab bar followed by the active tab's content.
func (m RootModel) View() string {
	tabBar := renderTabBar(m.tabs, m.activeTab, m.width)
	content := m.tabs[m.activeTab].View()
	return lipgloss.JoinVertical(lipgloss.Left, tabBar, content)
}

// renderTabBar builds the "1:Overview │ 2:Tasks │ …" header line inside a
// rounded border, consistent with the panel style used throughout the dashboard.
func renderTabBar(tabs []Tab, active, width int) string {
	parts := make([]string, len(tabs))
	for i, tab := range tabs {
		label := fmt.Sprintf("%d:%s", i+1, tab.TabName())
		if i == active {
			parts[i] = titleStyle.Render(" " + label + " ")
		} else {
			parts[i] = dimStyle.Render(" " + label + " ")
		}
	}
	bar := strings.Join(parts, dimStyle.Render("│"))
	innerWidth := width - 4
	if innerWidth < 40 {
		innerWidth = 40
	}
	return normalBorderStyle.Width(innerWidth).Render(bar)
}

// placeholderTab is a stub for tabs not yet implemented.
type placeholderTab struct {
	name   string
	width  int
	height int
}

// TabName implements Tab.
func (p placeholderTab) TabName() string { return p.name }

// Init implements tea.Model.
func (p placeholderTab) Init() tea.Cmd { return nil }

// Update implements tea.Model; captures window dimensions.
func (p placeholderTab) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		p.width = ws.Width
		p.height = ws.Height
	}
	return p, nil
}

// View implements tea.Model; renders "Coming soon..." centered in the panel.
func (p placeholderTab) View() string {
	const text = "Coming soon..."
	if p.height <= 0 || p.width <= 0 {
		return text
	}
	top := (p.height - 1) / 2
	pad := (p.width - len(text)) / 2
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat("\n", top) + strings.Repeat(" ", pad) + text
}
