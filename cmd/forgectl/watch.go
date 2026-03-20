package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

func newWatchCmd() *cobra.Command {
	var pollInterval int

	cmd := &cobra.Command{
		Use:   "watch <task-id>",
		Short: "Stream task status updates until the task reaches a terminal state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer conn.Close()

			stream, err := client.WatchTask(context.Background(), &forgepb.WatchTaskRequest{
				TaskId:         args[0],
				PollIntervalMs: int32(pollInterval),
			})
			if err != nil {
				return fmt.Errorf("starting watch stream: %w", err)
			}

			fmt.Printf("Watching task %s (poll every %dms)...\n", args[0], pollInterval)

			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					fmt.Println("Task reached terminal state.")
					return nil
				}
				if err != nil {
					return fmt.Errorf("receiving update: %w", err)
				}

				printTaskStatus(resp)
				fmt.Println("---")
			}
		},
	}

	cmd.Flags().IntVar(&pollInterval, "interval", 1000, "poll interval in milliseconds")

	return cmd
}
