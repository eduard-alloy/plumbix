package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/host"
	"github.com/eduard-alloy/plumbix/internal/review"
	"github.com/eduard-alloy/plumbix/internal/run"
	"github.com/eduard-alloy/plumbix/internal/settings"
)

func typesOf(evs []events.Event) []events.Type {
	out := make([]events.Type, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func sameTypes(a, b []events.Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stepPayload(t *testing.T, e events.Event) events.StepFinishedPayload {
	t.Helper()
	data, err := json.Marshal(e.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var p events.StepFinishedPayload
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// waitState polls until the change reaches want, since the state moves
// right after the run is recorded as finished.
func (h *harness) waitState(id string, want change.State) {
	h.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if h.state(id) == want {
			return
		}
	}
	h.t.Fatalf("change %s is %s, want %s", id, h.state(id), want)
}

// TestChangeRunEmitsEventsInOrder checks the whole sequence one validation
// produces, including a step.started and step.finished per step, which
// only the engine knows.
func TestChangeRunEmitsEventsInOrder(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	c := h.submit("feat")
	r := h.validate(c.ID)
	h.wait(r.ID)
	h.waitState(c.ID, change.Approved)

	evs, err := h.db.EventsAfter(ctx, 0, events.Filter{ChangeID: c.ID}, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []events.Type{events.ChangeSubmitted, events.RunStarted, events.StateChanged, events.StepStarted, events.StepFinished, events.StepStarted, events.StepFinished, events.RunFinished, events.VerdictIssued, events.StateChanged}
	if got := typesOf(evs); !sameTypes(got, want) {
		t.Fatalf("events = %v\nwant     %v", got, want)
	}
	for i, e := range evs {
		if e.RepoID != h.repoID || e.ChangeID != c.ID || (i > 0 && e.ID <= evs[i-1].ID) {
			t.Errorf("event %d = %+v; want repo %s, change %s, increasing ids", i, e, h.repoID, c.ID)
		}
		if e.Type != events.ChangeSubmitted && e.Type != events.StateChanged && e.RunID != r.ID {
			t.Errorf("event %s has run %q, want %s", e.Type, e.RunID, r.ID)
		}
	}
	test, validate := stepPayload(t, evs[4]), stepPayload(t, evs[6])
	if test.Step != "test" || test.Status != "succeeded" || test.Validator {
		t.Errorf("first step event = %+v", test)
	}
	if validate.Step != "validate" || validate.Status != "succeeded" || !validate.Validator {
		t.Errorf("second step event = %+v", validate)
	}
}

// TestPlainRunEmitsEvents: a push without a change still announces its run,
// with no change id and no verdict.
func TestPlainRunEmitsEvents(t *testing.T) {
	h := newHarness(t, Config{})
	h.repo.Commit("ci", map[string]string{"b.go": "package b\n"}, "push")
	r, err := h.eng.RunBranch(ctx, h.repoID, "ci")
	if err != nil {
		t.Fatal(err)
	}
	h.wait(r.ID)
	all, err := h.db.EventsAfter(ctx, 0, events.Filter{RepoID: h.repoID}, 100)
	if err != nil {
		t.Fatal(err)
	}
	var evs []events.Event
	for _, e := range all {
		if e.RunID == r.ID {
			evs = append(evs, e)
		}
	}
	want := []events.Type{events.RunStarted, events.StepFinished, events.RunFinished}
	if got := typesOf(evs); !sameTypes(got, want) {
		t.Fatalf("events = %v\nwant     %v", got, want)
	}
	for _, e := range evs {
		if e.ChangeID != "" {
			t.Errorf("plain run event %s names change %q", e.Type, e.ChangeID)
		}
	}
	if p := stepPayload(t, evs[1]); p.Step != "test" || p.Status != "succeeded" {
		t.Errorf("step event = %+v", p)
	}
}

// TestFailedStepEventCarriesTheFailure: the step event says what went wrong,
// so a client can show it before the run's summary exists.
func TestFailedStepEventCarriesTheFailure(t *testing.T) {
	h := newHarness(t, Config{})
	h.fake.exit["test"] = 2
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	c := h.submit("feat")
	r := h.validate(c.ID)
	h.wait(r.ID)
	evs, err := h.db.EventsAfter(ctx, 0, events.Filter{ChangeID: c.ID}, 100)
	if err != nil {
		t.Fatal(err)
	}
	var steps []events.StepFinishedPayload
	for _, e := range evs {
		if e.Type == events.StepFinished {
			steps = append(steps, stepPayload(t, e))
		}
	}
	if len(steps) != 2 || steps[0].Step != "test" || steps[0].Status != "failed" || steps[0].ExitCode != 2 || steps[1].Step != "validate" {
		t.Fatalf("step events = %+v; want test failed with exit 2, then the validator", steps)
	}
	_ = r
}

// TestStepsAreRecordedAsTheyFinish: a running run already lists the steps
// that ended, so a client can show progress before the verdict.
func TestStepsAreRecordedAsTheyFinish(t *testing.T) {
	h := newHarness(t, Config{})
	h.fake.block["validate"] = true
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	c := h.submit("feat")
	r, err := h.eng.Validate(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got run.Run
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var err error
		if got, err = h.db.GetRun(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
		if len(got.Steps) > 1 {
			break
		}
	}
	// The finished step and, since WU-027, the one still running.
	if got.Status != run.Running || len(got.Steps) != 2 || got.Steps[0].Name != "test" || got.Steps[0].Status != "succeeded" || got.Steps[1].Name != "validate" || got.Steps[1].Status != "running" {
		t.Fatalf("mid-run = %+v; want the finished test step while validate runs", got)
	}
	// A blocked step only ends by cancellation: a new validation replaces
	// the run, and the finished run lists every step once, in order.
	h.fake.mu.Lock()
	h.fake.block["validate"] = false
	h.fake.mu.Unlock()
	done := h.wait(h.validate(c.ID).ID)
	if len(done.Steps) != 2 || done.Steps[0].Name != "test" || done.Steps[1].Name != "validate" || done.Steps[1].Status != "succeeded" {
		t.Fatalf("finished steps = %+v; want both once, in pipeline order", done.Steps)
	}
	if old := h.wait(r.ID); old.Status != run.Cancelled || len(old.Steps) != 2 {
		t.Fatalf("cancelled run = %+v; want its two steps (test succeeded, validate cancelled) once each", old)
	}
}

// fakeSettings stands in for the operator's settings: guidance and the
// reviewer profiles (WU-028), with def naming the default profile.
type fakeSettings struct {
	global, repo string
	reviewers    map[string]settings.Reviewer
	def          string
}

func (f fakeSettings) Guidance(context.Context, string) (settings.Guidance, error) {
	return settings.Guidance{Global: f.global, Repository: f.repo}, nil
}

func (f fakeSettings) Reviewer(_ context.Context, name string) (settings.Reviewer, bool, error) {
	if name == "" {
		name = f.def
	}
	r, ok := f.reviewers[name]
	return r, ok, nil
}

// claudeProfile is an anthropic profile with a model; the base URL is set
// although meaningless, to show it never reaches an anthropic step.
var claudeProfile = settings.Reviewer{Name: "claude", Kind: settings.KindAnthropic, Model: "claude-sonnet-5-5", BaseURL: "https://api.openai.com/v1", Tools: review.ToolsFull, KeySecret: "ANTHROPIC_API_KEY"}

var grokProfile = settings.Reviewer{Name: "grok", Kind: settings.KindOpenAI, BaseURL: "https://api.x.ai/v1", Model: "grok-4", Tools: review.ToolsReadOnly, KeySecret: "PLUMBIX_REVIEWER_GROK_KEY"}

// TestValidatorsGetGuidanceAndModel: validator steps see the operator's
// guidance in change.json and the default profile's model as
// PLUMBIX_AI_MODEL and its key as ANTHROPIC_API_KEY, unless the pipeline
// step sets its own; normal steps see neither.
func TestValidatorsGetGuidanceAndModel(t *testing.T) {
	h := newHarness(t, Config{Settings: fakeSettings{global: "Prefer small diffs.", repo: "Go needs tests.", reviewers: map[string]settings.Reviewer{"claude": claudeProfile}, def: "claude"}, Secrets: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"}})
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	c := h.submit("feat")
	h.wait(h.validate(c.ID).ID)
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	if len(h.fake.inputs) != 1 {
		t.Fatalf("validator inputs = %d", len(h.fake.inputs))
	}
	g := h.fake.inputs[0].Guidance
	if g == nil || g.Global != "Prefer small diffs." || g.Repository != "Go needs tests." {
		t.Errorf("guidance = %+v", g)
	}
	for _, j := range h.fake.jobs {
		got := j.Env["PLUMBIX_AI_MODEL"]
		if j.Name == "validate" && (got != "claude-sonnet-5-5" || j.Env["ANTHROPIC_API_KEY"] != "sk-ant-test") {
			t.Errorf("validator env = %v", j.Env)
		}
		if j.Name == "test" && (got != "" || j.Env["ANTHROPIC_API_KEY"] != "") {
			t.Errorf("normal step got the model or the key: %v", j.Env)
		}
		if _, ok := j.Env["PLUMBIX_AI_PROVIDER"]; ok {
			t.Errorf("%s got a provider although anthropic is the default: %v", j.Name, j.Env)
		}
		if _, ok := j.Env["PLUMBIX_AI_BASE_URL"]; ok {
			t.Errorf("%s got a base URL although the provider is anthropic: %v", j.Name, j.Env)
		}
	}
}

// TestValidatorsGetTheOpenAIProviderAndBaseURL: an openai default profile
// reaches the validator as provider, base URL, model and PLUMBIX_AI_API_KEY.
func TestValidatorsGetTheOpenAIProviderAndBaseURL(t *testing.T) {
	h := newHarness(t, Config{Settings: fakeSettings{reviewers: map[string]settings.Reviewer{"grok": grokProfile}, def: "grok"}, Secrets: map[string]string{"PLUMBIX_REVIEWER_GROK_KEY": "xai-test"}})
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	c := h.submit("feat")
	h.wait(h.validate(c.ID).ID)
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	for _, j := range h.fake.jobs {
		if j.Name == "validate" && (j.Env["PLUMBIX_AI_PROVIDER"] != "openai" || j.Env["PLUMBIX_AI_BASE_URL"] != "https://api.x.ai/v1" || j.Env["PLUMBIX_AI_MODEL"] != "grok-4" || j.Env["PLUMBIX_AI_API_KEY"] != "xai-test") {
			t.Errorf("validator env = %v", j.Env)
		}
		if j.Name == "test" && (j.Env["PLUMBIX_AI_PROVIDER"] != "" || j.Env["PLUMBIX_AI_BASE_URL"] != "" || j.Env["PLUMBIX_AI_API_KEY"] != "") {
			t.Errorf("normal step got provider settings: %v", j.Env)
		}
	}
}

// TestAStepNamesItsReviewerProfile: reviewer: on a validator step picks
// that profile over the default; a name nobody defined fails the step,
// and the run, instead of judging with another model.
func TestAStepNamesItsReviewerProfile(t *testing.T) {
	const yaml = `version: 1
steps:
  - {name: test, image: "golang:1.27", run: "go test ./..."}
  - {name: validate, image: "plumbix-reference-validator:dev", validator: true, needs: [test]}
  - {name: ai-grok, image: "plumbix-ai-validator:dev", validator: true, reviewer: grok, needs: [test]}
  - {name: ai-own, image: "plumbix-ai-validator:dev", validator: true, reviewer: claude2, needs: [test], secrets: [ANTHROPIC_API_KEY]}
`
	claude2 := claudeProfile
	claude2.Name, claude2.KeySecret = "claude2", "CLAUDE2_KEY"
	h := newHarness(t, Config{Settings: fakeSettings{reviewers: map[string]settings.Reviewer{"claude": claudeProfile, "claude2": claude2, "grok": grokProfile}, def: "claude"}, Secrets: map[string]string{"PLUMBIX_REVIEWER_GROK_KEY": "xai-test", "ANTHROPIC_API_KEY": "sk-ant-test", "CLAUDE2_KEY": "sk-other"}})
	h.repo.Commit("main", map[string]string{".plumbix/pipeline.yaml": yaml}, "two profiles")
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	c := h.submit("feat")
	h.wait(h.validate(c.ID).ID)
	h.fake.mu.Lock()
	for _, j := range h.fake.jobs {
		switch j.Name {
		case "ai-grok":
			if j.Env["PLUMBIX_AI_PROVIDER"] != "openai" || j.Env["PLUMBIX_AI_MODEL"] != "grok-4" || j.Env["PLUMBIX_AI_API_KEY"] != "xai-test" || j.Env["ANTHROPIC_API_KEY"] != "" {
				t.Errorf("ai-grok env = %v", j.Env)
			}
		case "validate":
			if j.Env["PLUMBIX_AI_MODEL"] != "claude-sonnet-5-5" || j.Env["ANTHROPIC_API_KEY"] != "sk-ant-test" || j.Env["PLUMBIX_AI_API_KEY"] != "" {
				t.Errorf("validate env = %v", j.Env)
			}
		case "ai-own":
			// A secret the step lists itself wins over the profile's key;
			// PLUMBIX_ names are reserved, so model, provider and URL always
			// come from the profile.
			if j.Env["ANTHROPIC_API_KEY"] != "sk-ant-test" || j.Env["PLUMBIX_AI_MODEL"] != "claude-sonnet-5-5" || j.Env["PLUMBIX_AI_API_KEY"] != "" {
				t.Errorf("ai-own env = %v", j.Env)
			}
		}
	}
	h.fake.mu.Unlock()

	// A profile that does not exist (the pipeline is read at the branch
	// head): the step errors and says why.
	h.repo.Commit("feat", map[string]string{".plumbix/pipeline.yaml": strings.Replace(yaml, "reviewer: grok", "reviewer: gone", 1), "b.go": "package a\n"}, "missing profile")
	r := h.validate(c.ID)
	h.wait(r.ID)
	done, _ := h.db.GetRun(ctx, r.ID)
	var errored *run.Step
	for i := range done.Steps {
		if done.Steps[i].Name == "ai-grok" {
			errored = &done.Steps[i]
		}
	}
	if errored == nil || errored.Status != "errored" || !strings.Contains(errored.Error, `reviewer profile "gone" is not defined`) {
		t.Errorf("step with a missing profile = %+v; run %s error %q", errored, done.Status, done.Error)
	}
	if done.Outcome == change.Approved {
		t.Errorf("run with a missing profile was approved: %+v", done.Result)
	}
}

func TestNoGuidanceMeansNoField(t *testing.T) {
	h := newHarness(t, Config{Settings: fakeSettings{}})
	h.repo.Commit("feat", map[string]string{"a.go": "package a\n"}, "feature")
	c := h.submit("feat")
	h.wait(h.validate(c.ID).ID)
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	if h.fake.inputs[0].Guidance != nil {
		t.Errorf("guidance = %+v, want absent", h.fake.inputs[0].Guidance)
	}
	for _, j := range h.fake.jobs {
		if _, ok := j.Env["PLUMBIX_AI_MODEL"]; ok {
			t.Errorf("%s got a model with none set", j.Name)
		}
	}
}

// TestCredentialsAreReadLive: a token that appears after the engine started
// is used by the next fetch, with no restart.
func TestCredentialsAreReadLive(t *testing.T) {
	const token = "late_token_42"
	values := map[string]string{}
	var mu sync.Mutex
	src := host.Live(func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]string{}
		for k, v := range values {
			out[k] = v
		}
		return out
	})
	h := newHarness(t, Config{Hosts: src})
	h.repo.Commit("feat", map[string]string{"a.go": "a\n"}, "a")
	repo := change.Repo{ID: "r_liverepo0001", Host: change.HostGitHub, ProtectedBranches: []string{"main"}, MergeStrategy: "squash", CreatedAt: time.Now()}
	repo.URL = h.repo.ServeHTTP("x-access-token", token)
	if err := h.db.CreateRepo(ctx, repo); err != nil {
		t.Fatal(err)
	}
	h.repoID = repo.ID
	c := h.submit("feat")
	if r := h.validate(c.ID); r.Outcome != change.NeedsHuman {
		t.Fatalf("without a token the fetch must fail: %s", r.Outcome)
	}
	mu.Lock()
	values[host.GitHubTokenKey] = token
	mu.Unlock()
	if r := h.validate(c.ID); r.Outcome != change.Approved {
		t.Fatalf("with the token set later: %s (%s)", r.Outcome, r.Error)
	}
}
