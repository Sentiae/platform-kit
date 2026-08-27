package gitowner

import "testing"

// TestStableOwnerID_Pinned proves the derivation is the FNV-1a 32 of the login
// bytes and that it is PINNED: the two literals were computed independently of
// this package (P0-SPEC-A §0) and any change to the hash, the byte source or
// the fold would move them.
func TestStableOwnerID_Pinned(t *testing.T) {
	tests := []struct {
		login string
		want  uint32
	}{
		{"sentiae", 1260380820},
		{"acme", 1174237615},
	}
	for _, tt := range tests {
		t.Run(tt.login, func(t *testing.T) {
			if got := StableOwnerID(tt.login); got != tt.want {
				t.Fatalf("StableOwnerID(%q) = %d, want %d", tt.login, got, tt.want)
			}
		})
	}
}

// TestStableOwnerID_NeverZero proves the forced-non-zero fold: the empty login
// hashes to the FNV-1a offset basis (the positive anchor that the fold did not
// silently rewrite an ordinary value) and no login in the table returns 0.
// "a-gbn-c" is a real preimage of 0 under FNV-1a 32, so it is the row that
// turns red the moment the fold is removed.
func TestStableOwnerID_NeverZero(t *testing.T) {
	if got := StableOwnerID(""); got != 2166136261 {
		t.Fatalf("StableOwnerID(%q) = %d, want %d", "", got, 2166136261)
	}
	for _, login := range []string{"sentiae", "acme", "a", "owner-with-dash", "a-gbn-c"} {
		if got := StableOwnerID(login); got == 0 {
			t.Fatalf("StableOwnerID(%q) = 0", login)
		}
	}
}
