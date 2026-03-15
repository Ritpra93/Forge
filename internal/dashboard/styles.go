package dashboard

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#1a1a2e", Dark: "#e94560"}).
			Background(lipgloss.AdaptiveColor{Light: "#e94560", Dark: "#1a1a2e"}).
			Padding(0, 1)

	sectionHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#333333", Dark: "#aaaaaa"}).
				MarginTop(1).
				MarginBottom(0).
				Underline(true)

	// Status colors for task counters.
	pendingStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#b8860b", Dark: "#ffd700"})
	runningStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#1e90ff", Dark: "#5dade2"})
	completedStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#228b22", Dark: "#2ecc71"})
	failedStyle     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#dc143c", Dark: "#e74c3c"})
	deadLetterStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8b008b", Dark: "#bb86fc"})
	retryingStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#ff8c00", Dark: "#f39c12"})

	// Worker health.
	healthyStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#228b22", Dark: "#2ecc71"})
	deadStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#dc143c", Dark: "#e74c3c"})

	// Node indicators.
	leaderDotStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#228b22", Dark: "#2ecc71"})
	followerDotStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#1e90ff", Dark: "#5dade2"})

	// Event type colors.
	eventSubmittedStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#555555", Dark: "#cccccc"})
	eventCompletedStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#228b22", Dark: "#2ecc71"})
	eventFailedStyle       = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#dc143c", Dark: "#e74c3c"})
	eventElectedStyle      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#b8860b", Dark: "#ffd700"})
	eventWorkerStyle       = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8b008b", Dark: "#bb86fc"})
	eventDeadLetterStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8b008b", Dark: "#bb86fc"})

	dimStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#999999", Dark: "#666666"})

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#dc143c", Dark: "#e74c3c"}).
			Bold(true)

	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#888888", Dark: "#555555"})

	focusBorderStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.AdaptiveColor{Light: "#1e90ff", Dark: "#5dade2"})

	normalBorderStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.AdaptiveColor{Light: "#cccccc", Dark: "#333333"})
)

// eventStyle returns the appropriate style for an event type.
func eventStyle(eventType string) lipgloss.Style {
	switch eventType {
	case "task_completed":
		return eventCompletedStyle
	case "task_failed":
		return eventFailedStyle
	case "task_dead_lettered":
		return eventDeadLetterStyle
	case "leader_elected":
		return eventElectedStyle
	case "worker_connected", "worker_disconnected", "worker_dead":
		return eventWorkerStyle
	default:
		return eventSubmittedStyle
	}
}
