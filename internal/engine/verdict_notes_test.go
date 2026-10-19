package engine

import (
	"os"
	"strings"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/validation"
)

// TestContainerValidatorLogEndsWithTheVerdictNotes (WU-039): a container
// validator's log carries the same verdict notes an external step's does,
// after what the container printed, with cached tokens in the usage.
func TestContainerValidatorLogEndsWithTheVerdictNotes(t *testing.T) {
	h := newHarness(t, Config{})
	h.fake.verdicts["validate"] = `{"plumbix":"verdict/v1","outcome":"approve","summary":"Tests pass.\nNothing else to add.","criteria":[{"id":"AC-1","status":"met","reason":"tests pass","evidence":["test/log.txt"]}],"findings":[{"severity":"minor","message":"Name the error.","file":"a.go","line":4}],"risks":[{"area":"tests","message":"Only one test.","human_required":true}],"usage":{"model":"claude-opus-5-5","input_tokens":12,"output_tokens":300,"cache_read_tokens":41000,"cache_write_tokens":6000,"turns":6,"cost_usd":0.35}}`
	h.repo.Commit("feat", map[string]string{"app.go": "package app\n"}, "feature")
	c := h.submit("feat")
	r := h.validate(c.ID)
	log, err := os.ReadFile(h.eng.LogPath(r.ID, "validate"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(log)
	for _, want := range []string{"ran validate\n",
		"verdict: approve — Tests pass. (claude-opus-5-5, 12 input (41000 read from cache, 6000 written to cache) and 300 output tokens, 6 turns, $0.35 estimated)\n",
		"\n  Nothing else to add.\n", "finding minor: Name the error. (a.go:4)\n", "risk tests: Only one test., a person must look\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("validate log lacks %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "ran validate") > strings.Index(text, "verdict: approve") {
		t.Errorf("the notes should follow what the container printed:\n%s", text)
	}
	// A plain step gets no notes, and a validator that wrote no verdict gets none either.
	if plain, _ := os.ReadFile(h.eng.LogPath(r.ID, "test")); strings.Contains(string(plain), "verdict:") {
		t.Errorf("plain step log has verdict notes:\n%s", plain)
	}
}

func TestUsageNoteLeavesOutCacheWhenThereIsNone(t *testing.T) {
	if got := usageNote(&validation.Usage{Model: "grok-4.7", InputTokens: 900, OutputTokens: 80}); got != " (grok-4.7, 900 input and 80 output tokens)" {
		t.Errorf("usage note = %q", got)
	}
	if got := usageNote(&validation.Usage{Model: "m", InputTokens: 5, OutputTokens: 1, CacheReadTokens: 100}); got != " (m, 5 input (100 read from cache) and 1 output tokens)" {
		t.Errorf("usage note = %q", got)
	}
}
