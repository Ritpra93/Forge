package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

func newClusterCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cluster",
		Short: "Show Raft cluster state, leader, and node health",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := connect()
			if err != nil {
				return err
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			resp, err := client.GetClusterInfo(ctx, &forgepb.ClusterInfoRequest{})
			if err != nil {
				return fmt.Errorf("getting cluster info: %w", err)
			}

			fmt.Printf("Leader:  %s (%s)\n", resp.GetLeaderId(), resp.GetLeaderAddress())
			fmt.Printf("Nodes:   %d\n\n", len(resp.GetNodes()))

			for _, node := range resp.GetNodes() {
				fmt.Printf("  %-20s  %-30s  %s\n", node.GetId(), node.GetAddress(), node.GetState())
			}
			return nil
		},
	}
}
