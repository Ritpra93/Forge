package main

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/Ritpra93/forge/internal/dashboard"
)

func newDashboardCmd() *cobra.Command {
	var refreshMs int

	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Launch live terminal dashboard showing cluster status, tasks, workers, and events",
		RunE: func(cmd *cobra.Command, args []string) error {
			refresh := time.Duration(refreshMs) * time.Millisecond
			if refresh < 500*time.Millisecond {
				return fmt.Errorf("--refresh must be at least 500 (ms)")
			}
			if refresh > 60*time.Second {
				return fmt.Errorf("--refresh must be at most 60000 (ms)")
			}

			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer conn.Close()

			model := dashboard.New(client, refresh)
			p := tea.NewProgram(model, tea.WithAltScreen())
			_, err = p.Run()
			return err
		},
	}

	cmd.Flags().IntVar(&refreshMs, "refresh", 2000, "dashboard refresh interval in milliseconds (500-60000)")

	return cmd
}
