package gitowner

import "hash/fnv"

// StableOwnerID derives a non-zero uint32 owner id from an org string for the
// legacy uint32 create surface (real repos resolve by owner_login and never hit
// this). FNV-32a, forced non-zero.
func StableOwnerID(login string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(login))
	id := h.Sum32()
	if id == 0 {
		id = 1
	}
	return id
}
