package execx

import (
	"context"
	"os/exec"
)

// Executor runs external commands. Production code uses RealExecutor;
// tests use FakeExecutor so the burn/retrieve state machines can be
// tested without par2/xorriso/wodim or real hardware.
type Executor interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type RealExecutor struct{}

func (RealExecutor) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
