package pullrequest

import (
	"encoding/json"

	"github.com/eduard-alloy/plumbix/internal/validation"
)

// reportedCost reads cost_usd from the usage as the validator reported it,
// through the JSON form so the field's Go type can change without this
// package deciding what an absent cost means: absent is unknown.
func reportedCost(u *validation.Usage) (float64, bool) {
	data, err := json.Marshal(u)
	if err != nil {
		return 0, false
	}
	var m struct {
		Cost *float64 `json:"cost_usd"`
	}
	if json.Unmarshal(data, &m) != nil || m.Cost == nil {
		return 0, false
	}
	return *m.Cost, true
}
