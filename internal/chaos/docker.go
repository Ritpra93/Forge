// Package chaos provides a ChaosController for injecting failures into Forge
// cluster containers via the Docker daemon API.
package chaos

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// ChaosController wraps the Docker client with chaos-engineering operations
// scoped to Forge containers (names containing "forge", "scheduler", or "worker").
type ChaosController struct {
	cli *client.Client
}

// New creates a ChaosController using the standard environment-based Docker
// client (DOCKER_HOST, DOCKER_TLS_VERIFY, etc.).
func New() (*ChaosController, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}
	return &ChaosController{cli: cli}, nil
}

// Close releases the underlying Docker client.
func (c *ChaosController) Close() error {
	return c.cli.Close()
}

// ForgeContainer holds the subset of Docker container metadata relevant for
// chaos operations.
type ForgeContainer struct {
	ID     string
	Name   string
	Status string // e.g. "running", "paused", "exited"
	State  string // raw state string from Docker
}

// ListForgeContainers returns all containers whose name includes "forge",
// "scheduler", or "worker".
func (c *ChaosController) ListForgeContainers(ctx context.Context) ([]ForgeContainer, error) {
	all, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	var out []ForgeContainer
	for _, ct := range all {
		name := containerName(ct.Names)
		lower := strings.ToLower(name)
		if strings.Contains(lower, "forge") ||
			strings.Contains(lower, "scheduler") ||
			strings.Contains(lower, "worker") {
			out = append(out, ForgeContainer{
				ID:     ct.ID,
				Name:   name,
				Status: ct.Status,
				State:  string(ct.State),
			})
		}
	}
	return out, nil
}

// StopContainer gracefully stops the container within a 10-second timeout.
func (c *ChaosController) StopContainer(ctx context.Context, id string) error {
	timeout := 10
	if err := c.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("stopping container %s: %w", id, err)
	}
	return nil
}

// KillContainer sends SIGKILL to the container immediately.
func (c *ChaosController) KillContainer(ctx context.Context, id string) error {
	if err := c.cli.ContainerKill(ctx, id, "SIGKILL"); err != nil {
		return fmt.Errorf("killing container %s: %w", id, err)
	}
	return nil
}

// PauseContainer freezes all processes in the container (SIGSTOP via cgroups).
func (c *ChaosController) PauseContainer(ctx context.Context, id string) error {
	if err := c.cli.ContainerPause(ctx, id); err != nil {
		return fmt.Errorf("pausing container %s: %w", id, err)
	}
	return nil
}

// UnpauseContainer resumes a previously paused container.
func (c *ChaosController) UnpauseContainer(ctx context.Context, id string) error {
	if err := c.cli.ContainerUnpause(ctx, id); err != nil {
		return fmt.Errorf("unpausing container %s: %w", id, err)
	}
	return nil
}

// RestartContainer restarts the container with a 10-second graceful stop.
func (c *ChaosController) RestartContainer(ctx context.Context, id string) error {
	timeout := 10
	if err := c.cli.ContainerRestart(ctx, id, container.StopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("restarting container %s: %w", id, err)
	}
	return nil
}

// InjectLatency adds artificial latency to outbound traffic on eth0 inside the
// container using tc netem. The container must have NET_ADMIN capability and
// iproute2 installed.
func (c *ChaosController) InjectLatency(ctx context.Context, containerID string, latencyMs int) error {
	return c.runExec(ctx, containerID,
		"tc", "qdisc", "add", "dev", "eth0", "root", "netem",
		"delay", fmt.Sprintf("%dms", latencyMs),
	)
}

// InjectPacketLoss drops a percentage of outbound packets on eth0.
func (c *ChaosController) InjectPacketLoss(ctx context.Context, containerID string, percent int) error {
	return c.runExec(ctx, containerID,
		"tc", "qdisc", "add", "dev", "eth0", "root", "netem",
		"loss", fmt.Sprintf("%d%%", percent),
	)
}

// PartitionContainer simulates a network partition by dropping all outbound
// packets (100% loss) on eth0.
func (c *ChaosController) PartitionContainer(ctx context.Context, containerID string) error {
	return c.runExec(ctx, containerID,
		"tc", "qdisc", "add", "dev", "eth0", "root", "netem",
		"loss", "100%",
	)
}

// ClearNetworkChaos removes any tc qdisc rule applied to eth0, restoring
// normal network behaviour.
func (c *ChaosController) ClearNetworkChaos(ctx context.Context, containerID string) error {
	return c.runExec(ctx, containerID,
		"tc", "qdisc", "del", "dev", "eth0", "root",
	)
}

// runExec runs a one-shot command inside a container via the Docker exec API
// and waits up to 5 seconds for it to complete.
func (c *ChaosController) runExec(ctx context.Context, containerID string, cmd ...string) error {
	execResp, err := c.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec create in %s: %w", containerID, err)
	}

	startCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := c.cli.ContainerExecStart(startCtx, execResp.ID, container.ExecStartOptions{
		Detach: false,
	}); err != nil {
		return fmt.Errorf("exec start in %s: %w", containerID, err)
	}
	return nil
}

// containerName returns the primary (first) name for a container, stripping
// the leading "/" that Docker prepends.
func containerName(names []string) string {
	if len(names) == 0 {
		return "<unknown>"
	}
	return strings.TrimPrefix(names[0], "/")
}
