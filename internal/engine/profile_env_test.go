package engine

import (
	"reflect"
	"testing"

	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/settings"
)

// TestProfileEnvByKind: each kind reaches its container under the variables
// its validator reads; the key only when the secret has a value; a
// claude-code-image profile passes its tools and token and no effort
// (WU-028, WU-034, WU-035).
func TestProfileEnvByKind(t *testing.T) {
	secrets := map[string]string{"K": "v", "EMPTY": ""}
	cases := []struct {
		name   string
		r      settings.Reviewer
		effort string
		want   map[string]string
	}{
		{"anthropic", settings.Reviewer{Kind: settings.KindAnthropic, Model: "m", KeySecret: "K", Effort: "high"}, "", map[string]string{"PLUMBIX_AI_MODEL": "m", "ANTHROPIC_API_KEY": "v", "PLUMBIX_AI_EFFORT": "high"}},
		{"openai with step effort", settings.Reviewer{Kind: settings.KindOpenAI, Model: "g", BaseURL: "https://api.x.ai/v1", KeySecret: "K", Effort: "high"}, "low", map[string]string{"PLUMBIX_AI_MODEL": "g", "PLUMBIX_AI_PROVIDER": "openai", "PLUMBIX_AI_BASE_URL": "https://api.x.ai/v1", "PLUMBIX_AI_API_KEY": "v", "PLUMBIX_AI_EFFORT": "low"}},
		{"claude-code-image", settings.Reviewer{Kind: settings.KindClaudeCodeImage, Model: "claude-opus-5-5", Tools: review.ToolsFull, KeySecret: "K"}, "low", map[string]string{"PLUMBIX_AI_MODEL": "claude-opus-5-5", "PLUMBIX_AI_TOOLS": "full", "PLUMBIX_AI_API_KEY": "v"}},
		{"claude-code-image without a token or model", settings.Reviewer{Kind: settings.KindClaudeCodeImage, Tools: review.ToolsReadOnly, KeySecret: "EMPTY"}, "", map[string]string{"PLUMBIX_AI_TOOLS": "read-only"}},
		{"nothing", settings.Reviewer{Kind: settings.KindAnthropic, KeySecret: "MISSING"}, "", nil},
	}
	for _, c := range cases {
		if got := profileEnv(c.r, secrets, c.effort); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: env = %v, want %v", c.name, got, c.want)
		}
	}
}
