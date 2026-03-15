package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Ritpra93/forge/internal/proto/forgepb"
)

var address string

func main() {
	rootCmd := &cobra.Command{
		Use:   "forgectl",
		Short: "CLI client for the Forge distributed task orchestrator",
	}

	rootCmd.PersistentFlags().StringVar(&address, "address", "localhost:50051", "scheduler gRPC address")

	rootCmd.AddCommand(
		newSubmitCmd(),
		newStatusCmd(),
		newWatchCmd(),
		newClusterCmd(),
		newDeadletterCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// connect creates a gRPC client connection and ForgeScheduler client.
func connect() (forgepb.ForgeSchedulerClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to %s: %w", address, err)
	}
	return forgepb.NewForgeSchedulerClient(conn), conn, nil
}
