// Package acceptance exercises the actual independent Backend and Gateway binaries.
package acceptance

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestActualBackendAndGatewayLocalJourney(t *testing.T) {
	b, g := os.Getenv("NOX_GATEWAY_TEST_BACKEND_BINARY"), os.Getenv("NOX_GATEWAY_TEST_GATEWAY_BINARY")
	if b == "" {
		t.Skip("actual Backend binary supplied by the required CI integration job")
	}
	if g == "" {
		t.Fatal("missing Gateway binary")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "../../scripts/check-backend.py", "--backend", b, "--gateway", g)
	out, e := command.CombinedOutput()
	if e != nil {
		t.Fatalf("actual service integration failed: %v\n%s", e, out)
	}
	t.Log(string(out))
}
