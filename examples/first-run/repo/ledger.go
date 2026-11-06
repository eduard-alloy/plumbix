// Package ledger is the sample repository of the first-run example: one
// function, no test, so that the change the example submits can add one.
package ledger

// Balance sums the entries of an account.
func Balance(entries []int) int {
	total := 0
	for _, e := range entries {
		total += e
	}
	return total
}
