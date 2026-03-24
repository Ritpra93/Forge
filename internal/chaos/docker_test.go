package chaos

import (
	"context"
	"testing"
)

// TestNewChaosController verifies that a ChaosController can be created and
// pings the Docker daemon. Skipped automatically when Docker is not available.
func TestNewChaosController(t *testing.T) {
	cc, err := New()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	defer cc.Close()

	ctx := context.Background()
	_, err = cc.cli.Ping(ctx)
	if err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
}

// TestListForgeContainers verifies that ListForgeContainers returns without
// error when Docker is available (result may be empty in CI).
func TestListForgeContainers(t *testing.T) {
	cc, err := New()
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	defer cc.Close()

	ctx := context.Background()
	if _, pingErr := cc.cli.Ping(ctx); pingErr != nil {
		t.Skipf("docker daemon not reachable: %v", pingErr)
	}

	containers, err := cc.ListForgeContainers(ctx)
	if err != nil {
		t.Fatalf("ListForgeContainers() error = %v", err)
	}
	// Result may be empty; just verify the call succeeded.
	t.Logf("found %d forge containers", len(containers))
}
