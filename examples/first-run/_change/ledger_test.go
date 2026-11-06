package ledger

import "testing"

// TestBalance is the change the example submits: the test Balance never
// had. It is what the acceptance criteria ask for.
func TestBalance(t *testing.T) {
	if got := Balance([]int{1, 2, 3}); got != 6 {
		t.Fatalf("Balance([1 2 3]) = %d, want 6", got)
	}
	if got := Balance(nil); got != 0 {
		t.Fatalf("Balance(nil) = %d, want 0", got)
	}
}
