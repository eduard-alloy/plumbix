package aivalidator

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// openAITimeout bounds one chat-completions request.
const openAITimeout = 10 * time.Minute

// validateOpenAI asks an OpenAI-compatible endpoint. Vendors differ in
// how they enforce a response format, so the request is tried with
// json_schema, then json_object, then plain; the prompt carries the schema
// and the instruction either way, and review.ToVerdict checks the answer.
func validateOpenAI(ctx context.Context, cfg Config, inDir, workspace string) (validation.Verdict, error) {
	if cfg.APIKey == "" {
		return validation.Verdict{}, errors.New("no API key for the openai provider: add PLUMBIX_AI_API_KEY (or OPENAI_API_KEY) to the step's secrets")
	}
	if cfg.BaseURL == "" {
		return validation.Verdict{}, errors.New("PLUMBIX_AI_BASE_URL is not set: the provider's chat-completions base, such as https://api.openai.com/v1")
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
	criteria := in.Change.AcceptanceCriteria
	schema := review.OutputSchema(review.CriterionIDs(criteria))
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return validation.Verdict{}, err
	}
	user := prompt.User + "\n\n# Answer format\nAnswer with a single JSON object and nothing else, no prose and no code fence, matching this JSON schema:\n" + string(schemaJSON) + "\n"
	formats := []map[string]any{
		{"type": "json_schema", "json_schema": map[string]any{"name": "plumbix_verdict", "schema": schema}},
		{"type": "json_object"},
		nil,
	}
	var lastErr error
	host := endpointHost(cfg.BaseURL)
	effort := openAIEffort(cfg)
	for i, rf := range formats {
	again:
		body := map[string]any{
			"model":    cfg.Model,
			"messages": []map[string]string{{"role": "system", "content": prompt.System}, {"role": "user", "content": user}},
		}
		if rf != nil {
			body["response_format"] = rf
		}
		if effort != "" {
			body["reasoning_effort"] = effort
		}
		cfg.note("asking %s at %s (OpenAI-compatible): %d characters of prompt, response_format %s%s", cfg.Model, host, len(prompt.System)+len(user), formatName(rf), effortNote(effort))
		text, usage, status, err := chatCompletion(ctx, cfg, body)
		if err != nil {
			// A vendor that does not know reasoning_effort says so with a 400:
			// the same request goes again without it, once.
			if status == http.StatusBadRequest && effort != "" && mentionsEffort(err.Error()) {
				cfg.note("%s refused reasoning_effort %s; retrying without it", host, effort)
				effort = ""
				goto again
			}
			// A vendor that does not know this response_format says so with a
			// 400; the next, looser format is tried.
			if status == http.StatusBadRequest && rf != nil && mentionsFormat(err.Error()) && i+1 < len(formats) {
				cfg.note("%s refused response_format %s; retrying with %s", host, formatName(rf), formatName(formats[i+1]))
				lastErr = err
				continue
			}
			return validation.Verdict{}, err
		}
		return review.ToVerdict([]byte(unfence(text)), criteria, usage)
	}
	return validation.Verdict{}, lastErr
}

// chatCompletion posts one request and returns the assistant's text and usage.
func chatCompletion(ctx context.Context, cfg Config, body map[string]any) (string, validation.Usage, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", validation.Usage{}, 0, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, openAITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, strings.TrimSuffix(cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", validation.Usage{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", validation.Usage{}, 0, fmt.Errorf("provider request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", validation.Usage{}, resp.StatusCode, fmt.Errorf("read provider response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(data))
			if len(msg) > 300 {
				msg = msg[:300] + "…"
			}
		}
		return "", validation.Usage{}, resp.StatusCode, fmt.Errorf("the provider answered %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			Details          struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", validation.Usage{}, resp.StatusCode, fmt.Errorf("provider response is not a chat completion: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", validation.Usage{}, resp.StatusCode, errors.New("the provider returned no choices")
	}
	model := out.Model
	if model == "" {
		model = cfg.Model
	}
	usage := validation.Usage{Model: model, InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens, ReasoningTokens: out.Usage.Details.ReasoningTokens}
	return contentText(out.Choices[0].Message.Content), usage, resp.StatusCode, nil
}

// contentText reads a message content that is a string or a list of parts.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "" || p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return string(raw)
}

// mentionsFormat reports whether a 400 is about the response format.
func mentionsFormat(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "response_format") || strings.Contains(m, "json_schema") || strings.Contains(m, "json_object") || strings.Contains(m, "structured output")
}

// unfence strips a ```json fence a model may wrap its answer in.
func unfence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	t = strings.TrimPrefix(t, "```")
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[i+1:] // drop the language tag line
	}
	t = strings.TrimSuffix(strings.TrimSpace(t), "```")
	return strings.TrimSpace(t)
}

// formatName names a response_format for a log line.
func formatName(rf map[string]any) string {
	if rf == nil {
		return "none"
	}
	if t, ok := rf["type"].(string); ok {
		return t
	}
	return "custom"
}

// openAIEffort maps the operator's effort to what OpenAI-compatible vendors
// accept: low, medium or high; nothing when the operator set none.
func openAIEffort(cfg Config) string {
	if !cfg.EffortSet {
		return ""
	}
	switch cfg.Effort {
	case "low", "medium", "high":
		return cfg.Effort
	case "xhigh", "max":
		return "high"
	}
	return ""
}

func effortNote(effort string) string {
	if effort == "" {
		return ""
	}
	return ", reasoning_effort " + effort
}

// mentionsEffort recognises a vendor's refusal of the reasoning_effort field.
func mentionsEffort(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "reasoning_effort")
}
