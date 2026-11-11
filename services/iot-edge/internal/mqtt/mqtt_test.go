package mqtt

import (
	"context"
	"sync"
	"testing"
	"time"
)

func startBroker(t *testing.T) *Broker {
	t.Helper()
	b := &Broker{}
	if err := b.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

type inbox struct {
	mu sync.Mutex
	m  []Message
}

func (i *inbox) add(m Message) { i.mu.Lock(); i.m = append(i.m, m); i.mu.Unlock() }
func (i *inbox) all() []Message {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]Message(nil), i.m...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPublishAndSubscribeWithWildcards(t *testing.T) {
	b := startBroker(t)
	ctx := context.Background()
	var in inbox
	sub, err := Dial(ctx, Options{Addr: b.Addr(), ClientID: "sub", OnMessage: in.add})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := sub.Subscribe(ctx, 1, "plant/+/count", "alarms/#"); err != nil {
		t.Fatal(err)
	}
	pub, err := Dial(ctx, Options{Addr: b.Addr(), ClientID: "pub"})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	for _, m := range []struct {
		topic string
		qos   byte
	}{{"plant/press1/count", 1}, {"plant/press1/state", 0}, {"alarms/a/b", 0}, {"plant/press2/count", 0}} {
		if err := pub.Publish(ctx, m.topic, []byte("x"), m.qos, false); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "three messages", func() bool { return len(in.all()) == 3 })
	got := in.all()
	if got[0].Topic != "plant/press1/count" || got[1].Topic != "alarms/a/b" || got[2].Topic != "plant/press2/count" {
		t.Fatalf("received %+v", got)
	}
	if n := len(b.Received()); n != 4 {
		t.Fatalf("broker logged %d publishes", n)
	}
}

func TestBrokerChecksCredentialsAndRetainsMessages(t *testing.T) {
	b := &Broker{Username: "edge", Password: "s3cret"}
	if err := b.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()
	if _, err := Dial(ctx, Options{Addr: b.Addr(), ClientID: "x", Username: "edge", Password: "wrong"}); err == nil {
		t.Fatal("a wrong password must be refused")
	}
	pub, err := Dial(ctx, Options{Addr: b.Addr(), ClientID: "p", Username: "edge", Password: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	if err := pub.Publish(ctx, "setpoints/t1", []byte("80"), 1, true); err != nil {
		t.Fatal(err)
	}
	var in inbox
	sub, err := Dial(ctx, Options{Addr: b.Addr(), ClientID: "s", Username: "edge", Password: "s3cret", OnMessage: in.add})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := sub.Subscribe(ctx, 0, "setpoints/#"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the retained message", func() bool { return len(in.all()) == 1 })
	if m := in.all()[0]; !m.Retained || string(m.Payload) != "80" {
		t.Fatalf("got %+v", m)
	}
}

func TestPublishFailsWhenTheBrokerGoesAway(t *testing.T) {
	b := startBroker(t)
	ctx := context.Background()
	c, err := Dial(ctx, Options{Addr: b.Addr(), ClientID: "p", Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	b.DropClients()
	waitFor(t, "the drop", func() bool { return c.Err() != nil })
	if err := c.Publish(ctx, "a/b", []byte("1"), 1, false); err == nil {
		t.Fatal("publishing on a dropped connection must fail")
	}
}

func TestSessionReconnectsAndSubscribesAgain(t *testing.T) {
	b := startBroker(t)
	var in inbox
	s := &Session{Options: Options{Addr: b.Addr(), ClientID: "s"}, Filters: []string{"t/#"}, BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond}
	s.SetHandler(in.add)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	waitFor(t, "the first subscription", func() bool { return b.Subscribers() == 1 })
	b.Inject("t/1", []byte("a"), false)
	waitFor(t, "the first message", func() bool { return len(in.all()) == 1 })
	b.DropClients()
	waitFor(t, "the session to notice", func() bool { return b.Subscribers() == 0 })
	waitFor(t, "the second subscription", func() bool { return b.Subscribers() == 1 })
	b.Inject("t/2", []byte("b"), false)
	waitFor(t, "the message after the reconnect", func() bool { return len(in.all()) == 2 })
	if err := s.Publish(ctx, "out/1", []byte("v"), 1, false); err != nil {
		t.Fatal(err)
	}
	if got := b.Received(); len(got) != 1 || got[0].Topic != "out/1" {
		t.Fatalf("broker got %+v", got)
	}
}

func TestPublishWaitsForTheFirstConnection(t *testing.T) {
	b := startBroker(t)
	s := &Session{Options: Options{Addr: b.Addr(), ClientID: "s"}, ConnectWait: 2 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { time.Sleep(100 * time.Millisecond); s.Run(ctx) }()
	if err := s.Publish(ctx, "x/y", []byte("1"), 1, false); err != nil {
		t.Fatal(err)
	}
	s2 := &Session{ConnectWait: 50 * time.Millisecond}
	if err := s2.Publish(ctx, "x", nil, 0, false); err != ErrNotConnected {
		t.Fatalf("got %v, want ErrNotConnected", err)
	}
}

func TestFilterMatching(t *testing.T) {
	cases := []struct {
		f, t string
		want bool
	}{
		{"a/b", "a/b", true}, {"a/+", "a/b", true}, {"a/+", "a/b/c", false}, {"a/#", "a/b/c", true},
		{"#", "x", true}, {"a/#", "a", true}, {"+/b", "a/b", true}, {"a/b", "a", false},
	}
	for _, c := range cases {
		if got := matches(c.f, c.t); got != c.want {
			t.Errorf("matches(%q,%q)=%v", c.f, c.t, got)
		}
	}
	for _, f := range []string{"", "a/#/b", "a+/b", "a#"} {
		if ValidFilter(f) {
			t.Errorf("%q should be invalid", f)
		}
	}
}
