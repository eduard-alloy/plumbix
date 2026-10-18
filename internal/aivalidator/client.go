package aivalidator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// Request defaults. Effort is set explicitly because Opus 5.5 defaults to
// medium, and review quality matters more than cost here.
const (
	DefaultModel  = "claude-opus-5-5"
	DefaultEffort = "high"
	MaxTokens     = 16000
	// fallbackBeta enables server-side refusal fallbacks.
	fallbackBeta = "server-side-fallback-2026-07-01"
)

// Providers the validator can ask (WU-025, A25).
const (
	ProviderAnthropic = "anthropic" // Claude through Anthropic's SDK, with structured output and effort
	ProviderOpenAI    = "openai"    // any endpoint speaking the OpenAI chat-completions format
)

// Config selects the provider, model and credentials.
type Config struct {
	Provider string // ProviderAnthropic when empty
	BaseURL  string // the openai provider's base, such as https://api.openai.com/v1
	APIKey   string
	Model    string
	Effort   string // the reasoning effort: always sent to Anthropic; sent to an OpenAI-compatible endpoint only when EffortSet
	// EffortSet says the operator chose Effort (WU-034). Without it the
	// OpenAI-compatible path leaves reasoning_effort to the vendor's default,
	// since vendors differ on what they accept.
	EffortSet bool
	// Progress, when set, receives one line when a request goes out and
	// when a format is refused, so a slow model never looks hung (WU-031).
	// Model, host, sizes and effort only; never a key.
	Progress io.Writer
}

// Validate reviews the change described by inDir and returns a checked
// verdict. Any failure returns an error and no verdict.
func Validate(ctx context.Context, cfg Config, inDir, workspace string, opts ...option.RequestOption) (validation.Verdict, error) {
	switch cfg.Provider {
	case "", ProviderAnthropic:
		return validateAnthropic(ctx, cfg, inDir, workspace, opts...)
	case ProviderOpenAI:
		return validateOpenAI(ctx, cfg, inDir, workspace)
	}
	return validation.Verdict{}, fmt.Errorf("PLUMBIX_AI_PROVIDER %q: must be anthropic or openai", cfg.Provider)
}

func validateAnthropic(ctx context.Context, cfg Config, inDir, workspace string, opts ...option.RequestOption) (validation.Verdict, error) {
	if cfg.APIKey == "" {
		return validation.Verdict{}, errors.New("ANTHROPIC_API_KEY is not set; add it to the step's secrets")
	}
	in, err := validation.ReadInput(inDir)
	if err != nil {
		return validation.Verdict{}, err
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return validation.Verdict{}, fmt.Errorf("generate tag nonce: %w", err)
	}
	prompt, err := review.BuildPrompt(inDir, workspace, hex.EncodeToString(nonce))
	if err != nil {
		return validation.Verdict{}, err
	}
	cfg.note("asking %s through the Anthropic API (effort %s): %d characters of prompt", cfg.Model, cfg.Effort, len(prompt.System)+len(prompt.User))

	client := anthropic.NewClient(append([]option.RequestOption{option.WithAPIKey(cfg.APIKey)}, opts...)...)
	resp, err := client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(cfg.Model),
		MaxTokens: MaxTokens,
		System:    []anthropic.BetaTextBlockParam{{Text: prompt.System}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(prompt.User))},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffort(cfg.Effort),
			Format: anthropic.BetaJSONOutputFormatParam{Schema: review.OutputSchema(review.CriterionIDs(in.Change.AcceptanceCriteria))},
		},
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
		Betas:     []anthropic.AnthropicBeta{fallbackBeta},
	})
	if err != nil {
		return validation.Verdict{}, fmt.Errorf("Claude API request failed: %w", err)
	}

	switch resp.StopReason {
	case "refusal":
		return validation.Verdict{}, fmt.Errorf("the model refused to review this change (category %q): %s", resp.StopDetails.Category, resp.StopDetails.Explanation)
	case "max_tokens":
		return validation.Verdict{}, fmt.Errorf("the review hit the %d-token output limit before finishing", MaxTokens)
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(tb.Text)
		}
	}
	usage := validation.Usage{Model: string(resp.Model), InputTokens: int(resp.Usage.InputTokens), OutputTokens: int(resp.Usage.OutputTokens)}
	return review.ToVerdict([]byte(text.String()), in.Change.AcceptanceCriteria, usage)
}

// note writes one progress line, when a Progress writer is set.
func (c Config) note(format string, args ...any) {
	if c.Progress != nil {
		fmt.Fprintf(c.Progress, "ai validator: "+format+"\n", args...)
	}
}

// endpointHost names an OpenAI-compatible endpoint for a log line: its host,
// never its path or any credential.
func endpointHost(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "the endpoint"
}
