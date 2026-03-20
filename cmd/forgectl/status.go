package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <task-id>",
		Short: "Query the status of a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			resp, err := client.GetTaskStatus(ctx, &forgepb.TaskStatusRequest{
				TaskId: args[0],
			})
			if err != nil {
				return fmt.Errorf("getting task status: %w", err)
			}

			printTaskStatus(resp)
			return nil
		},
	}
}

func printTaskStatus(t *forgepb.TaskStatusResponse) {
	fmt.Printf("Task ID:    %s\n", t.GetTaskId())
	fmt.Printf("Status:     %s\n", t.GetStatus())
	fmt.Printf("Retries:    %d\n", t.GetRetryCount())
	fmt.Printf("Worker:     %s\n", t.GetAssignedWorker())
	if t.GetCreatedAt() != 0 {
		fmt.Printf("Created:    %s\n", time.Unix(0, t.GetCreatedAt()).Format(time.RFC3339))
	}
	if t.GetUpdatedAt() != 0 {
		fmt.Printf("Updated:    %s\n", time.Unix(0, t.GetUpdatedAt()).Format(time.RFC3339))
	}
}
