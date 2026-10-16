package events

import "sync"

// SubscriberBuffer is how many events a subscriber may fall behind before
// it is marked lagged and must resume from the store. A variable so tests
// can shrink it; set it before subscribing.
var SubscriberBuffer = 256

// Hub fans events out to live subscribers. Publish never blocks: a
// subscriber that does not keep up is cut off and told it lagged. The
// embedded Reader serves replays, so a stream has one dependency.
type Hub struct {
	Reader
	mu     sync.Mutex
	subs   map[*Subscription]struct{}
	closed bool
}

// Subscription is one subscriber's view of the hub. C delivers events in
// id order until Close, Hub.Close, or the subscriber lagged.
type Subscription struct {
	C      <-chan Event
	ch     chan Event
	filter Filter
	hub    *Hub
	lagged bool // guarded by hub.mu
	done   bool // guarded by hub.mu; ch is closed
}

// NewHub returns a hub whose replays come from r (nil if never replayed).
func NewHub(r Reader) *Hub {
	return &Hub{Reader: r, subs: map[*Subscription]struct{}{}}
}

// Subscribe registers a subscriber for events matching f. On a closed hub
// the returned channel is already closed.
func (h *Hub) Subscribe(f Filter) *Subscription {
	ch := make(chan Event, SubscriberBuffer)
	s := &Subscription{C: ch, ch: ch, filter: f, hub: h}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		s.done = true
		close(ch)
		return s
	}
	h.subs[s] = struct{}{}
	return s
}

// Publish delivers e to every matching subscriber without waiting.
func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if !s.filter.Matches(e) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			s.lagged = true
			h.drop(s)
		}
	}
}

// Close ends every subscription. Events already buffered are still read.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.subs {
		h.drop(s)
	}
}

// drop removes s and closes its channel. Callers hold h.mu.
func (h *Hub) drop(s *Subscription) {
	if s.done {
		return
	}
	s.done = true
	delete(h.subs, s)
	close(s.ch)
}

// Lagged reports whether the subscription ended because it fell behind.
// Meaningful once C is closed.
func (s *Subscription) Lagged() bool {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	return s.lagged
}

// Close ends the subscription. Closing twice is harmless.
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.drop(s)
}
