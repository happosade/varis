package xordisk

// XOR zero-pads every image in images to length, then XORs them together
// byte-for-byte. Used both to compute a group's parity disc image (XOR of
// all data discs) and to reconstruct one missing member (XOR of every
// other member, parity disc included) — the same operation both ways,
// which is the whole point of XOR parity.
func XOR(images [][]byte, length int64) []byte {
	out := make([]byte, length)
	for _, img := range images {
		n := int64(len(img))
		if n > length {
			n = length
		}
		for i := int64(0); i < n; i++ {
			out[i] ^= img[i]
		}
	}
	return out
}
