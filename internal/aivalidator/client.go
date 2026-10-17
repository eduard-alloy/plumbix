package aivalidator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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

// Config selects the model and credentials.
type Config struct {
	APIKey string
	Model  string
	Effort string
}

// Validate reviews the change described by inDir and returns a checked
// verdict. Any failure returns an error and no verdict.
func Validate(ctx context.Context, cfg Config, inDir, workspace string, opts ...option.RequestOption) (validation.Verdict, error) {
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
