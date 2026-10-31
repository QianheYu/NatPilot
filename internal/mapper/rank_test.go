package mapper

import "testing"

// TestProtocolRank checks the downgrade order: newest/most capable first.
func TestProtocolRank(t *testing.T) {
	order := []string{"igd2", "pcp", "natpmp", "igd1", "upnp", "unknown"}
	prev := -1
	for _, kind := range order {
		r := protocolRank(kind)
		if r <= prev {
			t.Fatalf("protocolRank(%q)=%d should be greater than previous %d", kind, r, prev)
		}
		prev = r
	}
	// Different spellings should still map to the same rank bucket.
	if protocolRank("IGD2") != protocolRank("igd2") {
		t.Fatal("protocolRank must be case-insensitive")
	}
}
