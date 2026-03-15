package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

func newSubmitCmd() *cobra.Command {
	var (
		taskType   string
		payload    string
		count      int
		maxRetries int
		timeout    int
	)

	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Submit one or more tasks to the scheduler",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer conn.Close()

			for i := 0; i < count; i++ {
				resp, err := submitWithRedirect(client, &forgepb.TaskRequest{
					Type:           taskType,
					Payload:        []byte(payload),
					MaxRetries:     int32(maxRetries),
					TimeoutSeconds: int32(timeout),
				})
				if err != nil {
					return fmt.Errorf("submitting task %d: %w", i+1, err)
				}
				fmt.Printf("Task %d: id=%s status=%s\n", i+1, resp.GetTaskId(), resp.GetStatus())
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&taskType, "type", "", "task type (e.g. fibonacci, sleep, httpcheck)")
	cmd.Flags().StringVar(&payload, "payload", "{}", "JSON payload for the task")
	cmd.Flags().IntVar(&count, "count", 1, "number of tasks to submit")
	cmd.Flags().IntVar(&maxRetries, "retries", 3, "maximum retry attempts")
	cmd.Flags().IntVar(&timeout, "timeout", 30, "task timeout in seconds")
	_ = cmd.MarkFlagRequired("type")

	return cmd
}

// submitWithRedirect submits a task and follows leader redirects.
func submitWithRedirect(client forgepb.ForgeSchedulerClient, req *forgepb.TaskRequest) (*forgepb.TaskResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.SubmitTask(ctx, req)
	if err == nil {
		return resp, nil
	}

	// If not the leader, parse the leader address and retry.
	leaderAddr := extractLeaderAddress(err)
	if leaderAddr == "" {
		return nil, err
	}

	fmt.Printf("Redirecting to leader at %s\n", leaderAddr)

	// Temporarily override the address and reconnect.
	origAddr := address
	address = leaderAddr
	defer func() { address = origAddr }()

	leaderClient, leaderConn, connErr := connect()
	if connErr != nil {
		return nil, fmt.Errorf("connecting to leader %s: %w", leaderAddr, connErr)
	}
	defer leaderConn.Close()

	return leaderClient.SubmitTask(ctx, req)
}

// extractLeaderAddress parses the leader address from a FailedPrecondition error.
func extractLeaderAddress(err error) string {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		return ""
	}
	msg := st.Message()
	// Expected format: "not the leader; current leader is <address>"
	const prefix = "not the leader; current leader is "
	if len(msg) > len(prefix) {
		return msg[len(prefix):]
	}
	return ""
}
