package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

func newDeadletterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deadletter",
		Short: "Manage dead-lettered tasks",
	}

	cmd.AddCommand(newDeadletterListCmd())
	return cmd
}

func newDeadletterListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all dead-lettered tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			resp, err := client.ListTasks(ctx, &forgepb.ListTasksRequest{
				StatusFilter: "dead_letter",
			})
			if err != nil {
				return fmt.Errorf("listing dead-lettered tasks: %w", err)
			}

			tasks := resp.GetTasks()
			if len(tasks) == 0 {
				fmt.Println("No dead-lettered tasks.")
				return nil
			}

			fmt.Printf("%-40s  %-12s  %-8s  %-20s\n", "TASK ID", "TYPE", "RETRIES", "UPDATED")
			for _, t := range tasks {
				updated := ""
				if t.GetUpdatedAt() != 0 {
					updated = time.Unix(0, t.GetUpdatedAt()).Format(time.RFC3339)
				}
				fmt.Printf("%-40s  %-12s  %-8d  %-20s\n",
					t.GetTaskId(), t.GetStatus(), t.GetRetryCount(), updated)
			}
			return nil
		},
	}
}
