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

// ExtractFromDisc pulls one named entry (pathInISO, absolute within the
// image, e.g. "/BD:0001.tar") out of an ISO 9660 image at src — which may
// be a physical device (e.g. "/dev/sr0") or a plain .iso file, both of
// which xorriso's -indev accepts identically. Reconstruction (see
// reconstruct.go) exploits this to read a reconstructed image the same
// way ReadDisk reads a live disc.
func ExtractFromDisc(ctx context.Context, ex execx.Executor, src, pathInISO, destPath string) error {
	_, err := ex.Run(ctx, "xorriso", "-indev", src, "-extract", pathInISO, destPath)
	return err
}
