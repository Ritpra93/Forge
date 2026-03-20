package raft

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	hcraft "github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

// SetupRaft creates and configures a Raft instance with BoltDB storage,
// file-based snapshots, and TCP transport. If bootstrap is true, the node
// bootstraps a new single-node cluster (only the first node should do this).
func SetupRaft(nodeID, raftAddr, dataDir string, bootstrap bool) (*hcraft.Raft, *TaskFSM, error) {
	config := hcraft.DefaultConfig()
	config.LocalID = hcraft.ServerID(nodeID)

	// Tune for faster elections in demo environments.
	config.HeartbeatTimeout = 1 * time.Second
	config.ElectionTimeout = 1 * time.Second
	config.LeaderLeaseTimeout = 500 * time.Millisecond

	fsm := NewTaskFSM()

	// BoltStore serves as BOTH LogStore and StableStore.
	store, err := raftboltdb.NewBoltStore(filepath.Join(dataDir, "raft.db"))
	if err != nil {
		return nil, nil, fmt.Errorf("creating bolt store: %w", err)
	}

	// File-based snapshot store, retaining up to 2 snapshots.
	snapshotStore, err := hcraft.NewFileSnapshotStore(dataDir, 2, os.Stderr)
	if err != nil {
		return nil, nil, fmt.Errorf("creating snapshot store: %w", err)
	}

	// TCP transport for Raft peer communication.
	addr, err := net.ResolveTCPAddr("tcp", raftAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving TCP address: %w", err)
	}
	transport, err := hcraft.NewTCPTransport(raftAddr, addr, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, nil, fmt.Errorf("creating TCP transport: %w", err)
	}

	// Create the Raft instance. store is passed for both logStore and stableStore.
	r, err := hcraft.NewRaft(config, fsm, store, store, snapshotStore, transport)
	if err != nil {
		return nil, nil, fmt.Errorf("creating raft instance: %w", err)
	}

	// Bootstrap the cluster with this node as the sole voter.
	if bootstrap {
		cfg := hcraft.Configuration{
			Servers: []hcraft.Server{
				{
					Suffrage: hcraft.Voter,
					ID:       hcraft.ServerID(nodeID),
					Address:  hcraft.ServerAddress(raftAddr),
				},
			},
		}
		f := r.BootstrapCluster(cfg)
		if err := f.Error(); err != nil {
			return nil, nil, fmt.Errorf("bootstrapping cluster: %w", err)
		}
	}

	return r, fsm, nil
}
