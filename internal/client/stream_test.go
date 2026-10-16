package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamParsesFrames(t *testing.T) {
	var gotLastID, gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLastID, gotAuth, gotAccept = r.Header.Get("Last-Event-ID"), r.Header.Get("Authorization"), r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": ok\n\n")
		fmt.Fprint(w, "id: 7\nevent: run.started\ndata: {\"id\":7}\n\n")
		fmt.Fprint(w, "retry: 1000\nid: 8\nevent: step.finished\ndata: {\"a\":1,\ndata:  \"b\":2}\n\n")
		fmt.Fprint(w, ": keepalive\n\n")
		fmt.Fprint(w, "data: no id\r\n\r\n")
	}))
	defer srv.Close()
	c := New(strings.TrimPrefix(srv.URL, "http://"), "tok")
	var frames []Frame
	err := c.Stream(context.Background(), "/api/v1/events", 6, func(f Frame) error {
		frames = append(frames, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotLastID != "6" || gotAuth != "Bearer tok" || gotAccept != "text/event-stream" {
		t.Errorf("headers: Last-Event-ID %q, Authorization %q, Accept %q", gotLastID, gotAuth, gotAccept)
	}
	want := []Frame{
		{ID: 7, Event: "run.started", Data: []byte(`{"id":7}`)},
		{ID: 8, Event: "step.finished", Data: []byte("{\"a\":1,\n \"b\":2}")},
		{Data: []byte("no id")},
	}
	if len(frames) != len(want) {
		t.Fatalf("frames = %+v, want %+v", frames, want)
	}
	for i := range want {
		if frames[i].ID != want[i].ID || frames[i].Event != want[i].Event || string(frames[i].Data) != string(want[i].Data) {
			t.Errorf("frame %d = %+v, want %+v", i, frames[i], want[i])
		}
	}
}

func TestStreamStopsWhenCallbackSaysSo(t *testing.T) {
	stop := errors.New("enough")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 1; ; i++ {
			if _, err := fmt.Fprintf(w, "id: %d\ndata: x\n\n", i); err != nil {
				return
			}
			_ = rc.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()
	c := New(strings.TrimPrefix(srv.URL, "http://"), "tok")
	n := 0
	err := c.Stream(context.Background(), "/x", 0, func(Frame) error {
		n++
		if n == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || n != 3 {
		t.Fatalf("err = %v after %d frames; want the callback's error after 3", err, n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	err = c.Stream(ctx, "/x", 0, func(Frame) error {
		cancel()
		return nil
	})
	if err != nil {
		t.Fatalf("a cancelled stream is a clean stop, got %v", err)
	}
}

func TestStreamReportsAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"code":"unauthorized","message":"missing token"}}`)
	}))
	defer srv.Close()
	c := New(strings.TrimPrefix(srv.URL, "http://"), "")
	err := c.Stream(context.Background(), "/x", 0, func(Frame) error { t.Fatal("no frames on an error response"); return nil })
	var apiErr APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "unauthorized" || !strings.Contains(err.Error(), "missing token") {
		t.Fatalf("err = %v (%T), want the API error", err, err)
	}
}
