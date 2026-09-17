package burn

import (
	"context"
	"fmt"

	"varis/internal/execx"
)

// CreateParity runs par2create against dataPath with the given redundancy
// percentage, producing dataPath + ".par2" (and volume files) alongside it.
func CreateParity(ctx context.Context, ex execx.Executor, dataPath string, parityPercent int) error {
	_, err := ex.Run(ctx, "par2create", fmt.Sprintf("-r%d", parityPercent), dataPath)
	return err
}

// VerifyParity runs par2verify against dataPath's .par2 index, repairing
// minor corruption in place where possible.
func VerifyParity(ctx context.Context, ex execx.Executor, dataPath string) error {
	_, err := ex.Run(ctx, "par2verify", dataPath+".par2")
	return err
}
