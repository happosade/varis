package burn

import (
	"context"

	"varis/internal/execx"
)

// BuildISO wraps everything under sourceDir into an ISO 9660 image at isoPath.
func BuildISO(ctx context.Context, ex execx.Executor, sourceDir, isoPath string) error {
	_, err := ex.Run(ctx, "xorriso", "-as", "mkisofs", "-o", isoPath, "-r", "-J", sourceDir)
	return err
}
