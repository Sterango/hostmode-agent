package moddata

// curseForgeFingerprint computes CurseForge's file fingerprint: 32-bit
// MurmurHash2 (seed 1) over the file with all whitespace bytes (tab, LF, CR,
// space) removed. Used to identify installed jars via /v1/fingerprints.
func curseForgeFingerprint(data []byte) uint32 {
	isWS := func(b byte) bool { return b == 9 || b == 10 || b == 13 || b == 32 }

	var n uint32
	for _, b := range data {
		if !isWS(b) {
			n++
		}
	}

	const m = 0x5bd1e995
	h := uint32(1) ^ n
	var k, shift uint32
	for _, b := range data {
		if isWS(b) {
			continue
		}
		k |= uint32(b) << shift
		shift += 8
		if shift == 32 {
			k *= m
			k ^= k >> 24
			k *= m
			h = h*m ^ k
			k, shift = 0, 0
		}
	}
	if shift > 0 {
		h ^= k
		h *= m
	}
	h ^= h >> 13
	h *= m
	h ^= h >> 15
	return h
}
