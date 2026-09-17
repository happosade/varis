package burn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"varis/internal/execx"
)

// HashFile returns the hex-encoded SHA256 of the file at path.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyBurn re-reads the device and confirms its hash matches the ISO that
// was burned, then runs par2verify against the parity-protected payload as
// a finer-grained corruption check than a gross hash mismatch would give.
func VerifyBurn(ctx context.Context, ex execx.Executor, device, isoHash, dataPathOnDisc string) error {
	readBack, err := HashFile(device)
	if err != nil {
		return fmt.Errorf("reading back device: %w", err)
	}
	if readBack != isoHash {
		return fmt.Errorf("burned disc hash %s does not match ISO hash %s", readBack, isoHash)
	}
	return VerifyParity(ctx, ex, dataPathOnDisc)
}
