package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/eduard-alloy/plumbix/internal/change"
	"github.com/eduard-alloy/plumbix/internal/client"
	"github.com/eduard-alloy/plumbix/internal/events"
	"github.com/eduard-alloy/plumbix/internal/run"
)

var errEnough = errors.New("enough")

// streamAPI serves the handler over a real socket, which SSE needs.
type streamAPI struct {
	*runAPI
	srv    *httptest.Server
	client *client.Client
}

func newStreamAPI(t *testing.T) *streamAPI {
	t.Helper()
	a := newRunAPI(t)
	srv := httptest.NewServer(a.h)
	t.Cleanup(srv.Close)
	return &streamAPI{runAPI: a, srv: srv, client: client.New(strings.TrimPrefix(srv.URL, "http://"), testToken)}
}

// collect streams from after until stop returns true for a frame, or the
// deadline passes, and returns the frames decoded as events.
func (s *streamAPI) collect(path string, after int64, stop func(events.Event) bool) []events.Event {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var got []events.Event
	err := s.client.Stream(ctx, path, after, func(f client.Frame) error {
		var e events.Event
		if err := json.Unmarshal(f.Data, &e); err != nil {
			return err
		}
		if f.ID != e.ID || string(e.Type) != f.Event {
			s.t.Errorf("frame id %d event %q, but the data says id %d type %s", f.ID, f.Event, e.ID, e.Type)
		}
		got = append(got, e)
		if stop(e) {
			return errEnough
		}
		return nil
	})
	if !errors.Is(err, errEnough) {
		s.t.Fatalf("stream ended with %v after %d events", err, len(got))
	}
	return got
}

// approve submits a change on branch and waits for it to be approved.
func (s *streamAPI) approve(branch string) change.Change {
	s.t.Helper()
	c := decode[change.Change](s.t, s.do(http.MethodPost, "/api/v1/changes", changeBody(s.repoID, branch)))
	first := decode[struct{ Runs []run.Run }](s.t, s.do(http.MethodGet, "/api/v1/changes/"+c.ID+"/runs", "")).Runs[0]
	s.waitRun(first.ID)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if got := decode[change.Change](s.t, s.do(http.MethodGet, "/api/v1/changes/"+c.ID, "")); got.State == change.Approved {
			return got
		}
	}
	s.t.Fatalf("change %s was not approved", c.ID)
	return change.Change{}
}

func stateChangedTo(to change.State) func(events.Event) bool {
	return func(e events.Event) bool {
		if e.Type != events.StateChanged {
			return false
		}
		data, _ := json.Marshal(e.Payload)
		var p events.StateChangedPayload
		_ = json.Unmarshal(data, &p)
		return p.To == string(to)
	}
}

func TestEventStreamReplaysThenFollows(t *testing.T) {
	o := loadOpenAPI(t)
	eventSchema, err := o.c.Compile("openapi.json#/components/schemas/Event")
	if err != nil {
		t.Fatalf("the Event schema must resolve to events.schema.json: %v", err)
	}
	s := newStreamAPI(t)
	c := s.approve("feat")

	// Replay: everything recorded so far, in order, valid against the contract.
	got := s.collect("/api/v1/events", 0, stateChangedTo(change.Approved))
	want := []events.Type{events.ChangeSubmitted, events.RunStarted, events.StateChanged, events.StepStarted, events.StepFinished, events.StepStarted, events.StepFinished, events.RunFinished, events.VerdictIssued, events.StateChanged}
	if len(got) != len(want) {
		t.Fatalf("replayed %d events %v, want %v", len(got), typesOf(got), want)
	}
	for i, e := range got {
		if e.Type != want[i] || e.ID != int64(i+1) || e.ChangeID != c.ID {
			t.Errorf("event %d = %+v, want type %s id %d change %s", i, e, want[i], i+1, c.ID)
		}
		checkSchema(t, eventSchema, e)
	}
	last := got[len(got)-1].ID

	// Follow: only what happens after last, exactly once.
	live := make(chan []events.Event, 1)
	go func() { live <- s.collect("/api/v1/events", last, stateChangedTo(change.Closed)) }()
	if rec := s.do(http.MethodPost, "/api/v1/changes/"+c.ID+"/close", ""); rec.Code != http.StatusOK {
		t.Fatalf("close: %d %s", rec.Code, rec.Body.String())
	}
	got = <-live
	if len(got) != 1 || got[0].ID != last+1 || got[0].Type != events.StateChanged {
		t.Fatalf("live events = %+v, want exactly the close as id %d", got, last+1)
	}
	checkSchema(t, eventSchema, got[0])

	// The query parameter resumes the same way as the header.
	if again := s.collect("/api/v1/events?after="+itoa(last), 0, stateChangedTo(change.Closed)); len(again) != 1 || again[0].ID != last+1 {
		t.Errorf("after= replay = %+v", again)
	}
}

func TestEventStreamFilters(t *testing.T) {
	s := newStreamAPI(t)
	s.repo.Commit("feat-2", map[string]string{"b.go": "package b\n"}, "second")
	first := s.approve("feat")
	second := s.approve("feat-2")

	got := s.collect("/api/v1/events?change_id="+second.ID, 0, stateChangedTo(change.Approved))
	if len(got) == 0 {
		t.Fatal("no events for the second change")
	}
	for _, e := range got {
		if e.ChangeID != second.ID {
			t.Errorf("filtered stream carried %+v", e)
		}
	}
	if got[0].Type != events.ChangeSubmitted || got[0].ID == 1 {
		t.Errorf("first filtered event = %+v; want the second change's submit, not the first change's", got[0])
	}
	byRepo := s.collect("/api/v1/events?repo_id="+s.repoID+"&after="+itoa(got[0].ID-1), 0, func(e events.Event) bool { return e.ChangeID == second.ID && stateChangedTo(change.Approved)(e) })
	if len(byRepo) != len(got) {
		t.Errorf("repo filter from the same point gave %d events, change filter %d", len(byRepo), len(got))
	}
	_ = first
}

func TestEventStreamRejectsBadRequests(t *testing.T) {
	s := newStreamAPI(t)
	var apiErr client.APIError
	if err := client.New(strings.TrimPrefix(s.srv.URL, "http://"), "wrong").Stream(context.Background(), "/api/v1/events", 0, nil); !errors.As(err, &apiErr) || apiErr.Code != "unauthorized" {
		t.Errorf("wrong token: %v", err)
	}
	for _, path := range []string{"/api/v1/events?after=abc", "/api/v1/events?after=-1"} {
		if err := s.client.Stream(context.Background(), path, 0, nil); !errors.As(err, &apiErr) || apiErr.Code != "invalid_request" {
			t.Errorf("%s: %v", path, err)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, s.srv.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Last-Event-ID", "nope")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad Last-Event-ID: %d", resp.StatusCode)
	}
}

func TestEventStreamHeartbeatAndShutdown(t *testing.T) {
	old := heartbeatInterval
	heartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = old })
	s := newStreamAPI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.srv.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	beats := 0
	for sc.Scan() && beats < 2 {
		if strings.HasPrefix(sc.Text(), ": keepalive") {
			beats++
		}
	}
	if beats < 2 {
		t.Fatalf("saw %d heartbeats before the stream ended: %v", beats, sc.Err())
	}
	s.hub.Close()
	closed := make(chan error, 1)
	go func() {
		for sc.Scan() {
		}
		closed <- sc.Err()
	}()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("stream ended with %v after the hub closed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end when the hub closed")
	}
}

// TestEventStreamSurvivesLag shrinks the subscriber buffer so the handler
// is likely to fall behind a burst and resume from the store. Whatever the
// timing, the client must get every event once, in order.
func TestEventStreamSurvivesLag(t *testing.T) {
	old := events.SubscriberBuffer
	events.SubscriberBuffer = 1
	t.Cleanup(func() { events.SubscriberBuffer = old })
	s := newStreamAPI(t)
	const burst = 50
	live := make(chan []events.Event, 1)
	go func() {
		live <- s.collect("/api/v1/events", 0, func(e events.Event) bool { return e.ID == burst })
	}()
	for i := 0; i < burst; i++ {
		if _, err := s.db.AppendEvent(ctxBG, events.Event{Type: events.StateChanged, RepoID: s.repoID, ChangeID: "c_burst", Payload: events.StateChangedPayload{From: "open", To: "running"}}); err != nil {
			t.Fatal(err)
		}
	}
	got := <-live
	if len(got) != burst {
		t.Fatalf("got %d events, want %d", len(got), burst)
	}
	for i, e := range got {
		if e.ID != int64(i+1) {
			t.Fatalf("event %d has id %d; want every id once, in order", i, e.ID)
		}
	}
}

func typesOf(evs []events.Event) []events.Type {
	out := make([]events.Type, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func checkSchema(t *testing.T, s *jsonschema.Schema, e events.Event) {
	t.Helper()
	data, _ := json.Marshal(e)
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(v); err != nil {
		t.Errorf("event does not match the contract:\n%s\n%v", data, err)
	}
}
