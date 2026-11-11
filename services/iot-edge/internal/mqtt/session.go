package mqtt

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Session keeps one connection to the broker alive: it dials, subscribes to Filters, hands messages to the
// handler, and dials again with a growing wait when the connection drops. Publish uses the current
// connection and waits a few seconds for one when there is none.
type Session struct {
	Options
	Filters []string
	QoS     byte // subscription QoS, 0 or 1
	// BackoffMin and BackoffMax bound the wait between dials (default 1 s and 30 s).
	BackoffMin, BackoffMax time.Duration
	// ConnectWait is how long Publish waits for a connection (default 5 s).
	ConnectWait time.Duration
	OnConnect   func(attempt int) // called after each successful connect and subscribe
	OnError     func(error)       // called with each failed dial or dropped connection

	mu      sync.Mutex
	conn    *Conn
	ready   chan struct{} // closed while connected
	handler func(Message)
}

// SetHandler sets the function that receives subscribed messages. Call it before Run.
func (s *Session) SetHandler(f func(Message)) {
	s.mu.Lock()
	s.handler = f
	s.mu.Unlock()
}

func (s *Session) setConn(c *Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn = c
	if c != nil {
		if s.ready != nil {
			close(s.ready)
			s.ready = nil
		}
	}
}

// Connected reports whether a connection is up.
func (s *Session) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn != nil && s.conn.Err() == nil
}

// Run keeps the connection until ctx ends.
func (s *Session) Run(ctx context.Context) error {
	lo, hi := s.BackoffMin, s.BackoffMax
	if lo <= 0 {
		lo = time.Second
	}
	if hi <= 0 {
		hi = 30 * time.Second
	}
	wait := lo
	for attempt := 1; ctx.Err() == nil; attempt++ {
		o := s.Options
		o.OnMessage = func(m Message) {
			s.mu.Lock()
			h := s.handler
			s.mu.Unlock()
			if h != nil {
				h(m)
			}
		}
		c, err := Dial(ctx, o)
		if err == nil {
			s.mu.Lock()
			filters := append([]string(nil), s.Filters...)
			s.mu.Unlock()
			err = c.Subscribe(ctx, s.QoS, filters...)
			if err != nil {
				c.Close()
			}
		}
		if err != nil {
			if s.OnError != nil && ctx.Err() == nil {
				s.OnError(err)
			}
		} else {
			s.setConn(c)
			if s.OnConnect != nil {
				s.OnConnect(attempt)
			}
			wait = lo
			select {
			case <-c.Done():
				if s.OnError != nil && ctx.Err() == nil {
					s.OnError(c.Err())
				}
			case <-ctx.Done():
				c.Close()
			}
			s.mu.Lock()
			s.conn = nil
			s.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, hi)
	}
	return ctx.Err()
}

// Subscribe adds filters to the session: they are subscribed on the current connection now and again after
// every reconnect. A filter already in the session is not subscribed twice.
func (s *Session) Subscribe(ctx context.Context, filters ...string) error {
	s.mu.Lock()
	have := map[string]bool{}
	for _, f := range s.Filters {
		have[f] = true
	}
	var add []string
	for _, f := range filters {
		if !have[f] {
			have[f] = true
			add = append(add, f)
		}
	}
	s.Filters = append(s.Filters, add...)
	c := s.conn
	s.mu.Unlock()
	if c == nil || c.Err() != nil || len(add) == 0 {
		return nil // the next connect subscribes them
	}
	return c.Subscribe(ctx, s.QoS, add...)
}

// ErrNotConnected is returned by Publish when no connection came up in time.
var ErrNotConnected = errors.New("mqtt: not connected to the broker")

// Publish sends through the current connection.
func (s *Session) Publish(ctx context.Context, topic string, payload []byte, qos byte, retain bool) error {
	wait := s.ConnectWait
	if wait <= 0 {
		wait = 5 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		s.mu.Lock()
		c := s.conn
		if c == nil || c.Err() != nil {
			if s.ready == nil {
				s.ready = make(chan struct{})
			}
		}
		ready := s.ready
		s.mu.Unlock()
		if c != nil && c.Err() == nil {
			return c.Publish(ctx, topic, payload, qos, retain)
		}
		left := time.Until(deadline)
		if left <= 0 {
			return ErrNotConnected
		}
		select {
		case <-ready:
		case <-time.After(min(left, 200*time.Millisecond)):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
