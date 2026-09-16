package burn

import (
	"context"

	"varis/internal/execx"
)

// BurnISO writes isoPath to the optical device using wodim.
func BurnISO(ctx context.Context, ex execx.Executor, device, isoPath string) error {
	_, err := ex.Run(ctx, "wodim", "dev="+device, isoPath)
	return err
}
