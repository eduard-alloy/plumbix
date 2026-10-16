package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/eduard-alloy/plumbix/internal/events"
)

// EventSource is what the stream needs: live subscriptions and the log.
type EventSource interface {
	Subscribe(f events.Filter) *events.Subscription
	EventsAfter(ctx context.Context, after int64, f events.Filter, limit int) ([]events.Event, error)
}

// replayBatch is how many recorded events one query returns.
const replayBatch = 500

// heartbeatInterval is how often an idle stream sends a comment line, so
// proxies keep the connection and dead clients are noticed. A variable so
// tests can shorten it.
var heartbeatInterval = 15 * time.Second

type eventHandlers struct {
	events EventSource
	log    *slog.Logger
}

func registerEventRoutes(mux *http.ServeMux, h *eventHandlers) {
	mux.HandleFunc("GET /api/v1/events", h.stream)
}

// stream serves GET /api/v1/events as Server-Sent Events. It subscribes
// first, replays recorded events after the client's last id, then follows
// live, skipping anything already sent. A subscription that fell behind
// goes back to replaying from the last id sent, so the client never sees
// a gap or a duplicate.
func (h *eventHandlers) stream(w http.ResponseWriter, r *http.Request) {
	after, ok := lastEventID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "Last-Event-ID and after must be a non-negative integer")
		return
	}
	q := r.URL.Query()
	filter := events.Filter{RepoID: q.Get("repo_id"), ChangeID: q.Get("change_id")}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, ": ok\n\n"); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		h.log.Warn("event stream needs a flushing connection", "err", err)
		return
	}
	ctx := r.Context()
	last := after
	for {
		sub := h.events.Subscribe(filter)
		if err := h.replay(ctx, w, rc, filter, &last); err != nil {
			sub.Close()
			return
		}
		lagged := h.follow(ctx, w, rc, sub, &last)
		sub.Close()
		if !lagged {
			return
		}
	}
}

// lastEventID reads the Last-Event-ID header, or the after query parameter.
func lastEventID(r *http.Request) (int64, bool) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	if raw == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n >= 0
}

// replay sends every recorded event after *last and advances it.
func (h *eventHandlers) replay(ctx context.Context, w io.Writer, rc *http.ResponseController, f events.Filter, last *int64) error {
	for {
		batch, err := h.events.EventsAfter(ctx, *last, f, replayBatch)
		if err != nil {
			if ctx.Err() == nil {
				h.log.Error("read events", "err", err)
			}
			return err
		}
		for _, e := range batch {
			if err := writeEvent(w, rc, e); err != nil {
				return err
			}
			*last = e.ID
		}
		if len(batch) < replayBatch {
			return nil
		}
	}
}

// follow sends live events until the client leaves, the hub closes, or the
// subscription lagged, which it reports so the caller can replay.
func (h *eventHandlers) follow(ctx context.Context, w io.Writer, rc *http.ResponseController, sub *events.Subscription, last *int64) bool {
	tick := time.NewTicker(heartbeatInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case e, ok := <-sub.C:
			if !ok {
				return sub.Lagged()
			}
			if e.ID <= *last {
				continue
			}
			if err := writeEvent(w, rc, e); err != nil {
				return false
			}
			*last = e.ID
		case <-tick.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return false
			}
			if err := rc.Flush(); err != nil {
				return false
			}
		}
	}
}

func writeEvent(w io.Writer, rc *http.ResponseController, e events.Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, data); err != nil {
		return err
	}
	return rc.Flush()
}
