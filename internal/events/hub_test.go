package events

import (
	"testing"
	"time"
)

func ev(id int64, t Type, change string) Event {
	return Event{ID: id, Type: t, Time: time.Unix(id, 0), RepoID: "r_1", ChangeID: change, Payload: StateChangedPayload{From: "open", To: "running"}}
}

// receive reads one event or fails after a second.
func receive(t *testing.T, s *Subscription) (Event, bool) {
	t.Helper()
	select {
	case e, ok := <-s.C:
		return e, ok
	case <-time.After(time.Second):
		t.Fatal("no event within a second")
		return Event{}, false
	}
}

func TestHubDeliversInOrderAndFiltered(t *testing.T) {
	h := NewHub(nil)
	all := h.Subscribe(Filter{})
	one := h.Subscribe(Filter{ChangeID: "c_1"})
	other := h.Subscribe(Filter{RepoID: "r_other"})
	defer all.Close()
	defer one.Close()
	defer other.Close()

	h.Publish(ev(1, ChangeSubmitted, "c_1"))
	h.Publish(ev(2, RunStarted, "c_2"))
	h.Publish(ev(3, StateChanged, "c_1"))

	for want := int64(1); want <= 3; want++ {
		if e, ok := receive(t, all); !ok || e.ID != want {
			t.Fatalf("all: got %+v (%v), want id %d", e, ok, want)
		}
	}
	for _, want := range []int64{1, 3} {
		if e, ok := receive(t, one); !ok || e.ID != want {
			t.Fatalf("filtered: got %+v (%v), want id %d", e, ok, want)
		}
	}
	select {
	case e := <-other.C:
		t.Fatalf("subscriber for another repo got %+v", e)
	default:
	}
}

// TestHubMarksSlowSubscriberLagged: Publish is synchronous and never waits
// for a subscriber, so publishing past a full buffer returns at once; the
// subscriber that fell behind is cut off, a reading one gets everything.
func TestHubMarksSlowSubscriberLagged(t *testing.T) {
	h := NewHub(nil)
	slow := h.Subscribe(Filter{})
	fast := h.Subscribe(Filter{})
	defer fast.Close()
	for i := 1; i <= SubscriberBuffer+1; i++ {
		h.Publish(ev(int64(i), StateChanged, "c_1"))
		if e, ok := receive(t, fast); !ok || e.ID != int64(i) {
			t.Fatalf("fast subscriber: got %+v (%v), want id %d", e, ok, i)
		}
	}
	got := 0
	for range slow.C {
		got++
	}
	if got != SubscriberBuffer || !slow.Lagged() {
		t.Fatalf("slow subscriber: got %d events, lagged %v; want %d events then lagged", got, slow.Lagged(), SubscriberBuffer)
	}
	h.Publish(ev(999, StateChanged, "c_1"))
	if e, ok := receive(t, fast); !ok || e.ID != 999 {
		t.Fatalf("after the slow one was dropped: got %+v (%v)", e, ok)
	}
	if fast.Lagged() {
		t.Error("a reading subscriber must not be marked lagged")
	}
}

func TestHubCloseEndsSubscriptions(t *testing.T) {
	h := NewHub(nil)
	s := h.Subscribe(Filter{})
	h.Publish(ev(1, StateChanged, "c_1"))
	h.Close()
	if e, ok := receive(t, s); !ok || e.ID != 1 {
		t.Fatalf("buffered event lost on close: %+v (%v)", e, ok)
	}
	if _, ok := receive(t, s); ok {
		t.Fatal("channel still open after Close")
	}
	if s.Lagged() {
		t.Error("a closed hub is not lag")
	}
	late := h.Subscribe(Filter{})
	if _, ok := receive(t, late); ok {
		t.Error("subscribing to a closed hub should yield a closed channel")
	}
	h.Publish(ev(2, StateChanged, "c_1")) // must not panic
	s.Close()                             // closing twice is fine
}

func TestFilterMatches(t *testing.T) {
	e := Event{RepoID: "r_1", ChangeID: "c_1"}
	plain := Event{RepoID: "r_1"}
	cases := []struct {
		f    Filter
		e    Event
		want bool
	}{
		{Filter{}, e, true},
		{Filter{RepoID: "r_1"}, e, true},
		{Filter{RepoID: "r_2"}, e, false},
		{Filter{ChangeID: "c_1"}, e, true},
		{Filter{ChangeID: "c_1"}, plain, false},
		{Filter{RepoID: "r_1", ChangeID: "c_2"}, e, false},
	}
	for _, c := range cases {
		if got := c.f.Matches(c.e); got != c.want {
			t.Errorf("%+v matches %+v = %v, want %v", c.f, c.e, got, c.want)
		}
	}
}
