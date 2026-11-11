package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/domain"
	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/mqtt"
)

func testBroker(t *testing.T) *mqtt.Broker {
	t.Helper()
	b := &mqtt.Broker{}
	if err := b.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestParseMQTTReading(t *testing.T) {
	now := t0
	for _, c := range []struct {
		payload string
		num     float64
		text    string
		ts      time.Time
		bad     bool
	}{
		{payload: "42", num: 42, text: "42", ts: now},
		{payload: " 4.5 ", num: 4.5, text: "4.5", ts: now},
		{payload: "RUNNING", text: "RUNNING", ts: now},
		{payload: `{"value": 17}`, num: 17, text: "17", ts: now},
		{payload: `{"value":"DOWN","ts":"2026-11-10T07:00:00Z"}`, text: "DOWN", ts: time.Date(2026, 11, 10, 7, 0, 0, 0, time.UTC)},
		{payload: `{"ts":"2026-11-10T07:00:00Z"}`, bad: true},
		{payload: `{"value":1,"ts":"yesterday"}`, bad: true},
		{payload: ``, bad: true},
	} {
		rd, err := ParseMQTTReading("plant/p1", []byte(c.payload), now)
		if c.bad {
			if err == nil {
				t.Errorf("%q should be refused", c.payload)
			}
			continue
		}
		if err != nil || rd.Number != c.num || rd.Text != c.text || !rd.TS.Equal(c.ts) || rd.Address != "plant/p1" {
			t.Errorf("%q: %+v %v", c.payload, rd, err)
		}
	}
}

// Machine data published to a broker becomes machine events, through the whole runtime to the server;
// a topic no tag maps is ignored; a dropped broker connection costs nothing but the readings in between.
func TestMQTTSourceFeedsTheRuntime(t *testing.T) {
	b := testBroker(t)
	srv := newServer()
	sess := &mqtt.Session{Options: mqtt.Options{Addr: b.Addr(), ClientID: "edge-1"}, BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond}
	src := &MQTTSource{Session: sess}
	r, err := New(Options{Site: "demo-plant", EdgeID: "edge-1", Transport: srv, Source: src, FlushEvery: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	cfg := domain.EdgeConfig{Version: 1, Machines: []domain.MachineSpec{{MachineID: "PRESS-1", ResourceID: resA}},
		Tags: []domain.TagMapping{
			{Address: "plant/press1/count", MachineID: "PRESS-1", Kind: domain.KindCount},
			{Address: "plant/press1/state", MachineID: "PRESS-1", Kind: domain.KindState},
		}}
	if _, err := r.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	eventually(t, "the subscriptions", func() bool { return b.Subscribers() == 1 })

	b.Inject("plant/press1/count", []byte("100"), false) // baseline
	b.Inject("plant/press1/state", []byte("RUNNING"), false)
	b.Inject("plant/press1/count", []byte(`{"value": 107}`), false)
	b.Inject("plant/other/count", []byte("5"), false) // not mapped
	eventually(t, "two events", func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return len(srv.stored) == 2
	})
	var counted bool
	srv.mu.Lock()
	for _, ev := range srv.stored {
		if ev.Kind == domain.KindCount && ev.GoodCount != nil && *ev.GoodCount == 7 {
			counted = true
		}
	}
	srv.mu.Unlock()
	if !counted {
		t.Fatalf("no count event with delta 7: %+v", srv.stored)
	}

	b.DropClients()
	eventually(t, "the broker to drop the session", func() bool { return b.Subscribers() == 0 })
	eventually(t, "the session to come back", func() bool { return b.Subscribers() == 1 })
	b.Inject("plant/press1/count", []byte("110"), false)
	eventually(t, "the event after the reconnect", func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return len(srv.stored) == 3
	})
	cancel()
	<-done
}

// FR-25.15 over MQTT: the recipe document goes to the recipe topic and each allowlisted setpoint to its
// topic, at QoS 1; the report says there is no readback and the recipe is not an error.
func TestMQTTWriterPublishesTheRecipeAndItsSetpoints(t *testing.T) {
	b := testBroker(t)
	sess := &mqtt.Session{Options: mqtt.Options{Addr: b.Addr(), ClientID: "edge-1"}, ConnectWait: 2 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sess.Run(ctx)

	const topic = "plant/reactor1/setpoints/temp"
	doc := json.RawMessage(`{"procedure":{"name":"Batch"}}`)
	rec := &fakeRecipes{recipes: []Recipe{{ID: "m1", OrderID: "o1", Destination: "pcs-mqtt", Status: "sent", Topic: "plant/reactor1/recipe",
		Document: doc, Setpoints: []Setpoint{{PhasePath: "Batch/R/Heat/Ramp", Node: topic, Value: 72}, {PhasePath: "Batch/R/Heat/Hold", Node: "plant/not/allowed", Value: 5}}}}}
	r, _ := simRuntime(t, newServer(), 1, Options{Recipes: rec, Writers: map[string]Writer{"pcs-mqtt": &MQTTWriter{Session: sess}}})
	cfg, _ := SimConfig(2, 1, func(int) string { return resA })
	cfg.WriteRules = []domain.WriteRule{{Node: topic, Min: 60, Max: 90}}
	if _, err := r.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ExecuteRecipes(ctx); err != nil {
		t.Fatal(err)
	}
	got := b.Received()
	if len(got) != 2 || got[0].Topic != "plant/reactor1/recipe" || string(got[0].Payload) != string(doc) || got[1].Topic != topic || got[1].QoS != 1 {
		t.Fatalf("broker received %+v", got)
	}
	var sp struct{ Value float64 }
	if err := json.Unmarshal(got[1].Payload, &sp); err != nil || sp.Value != 72 {
		t.Fatalf("setpoint payload %s: %v", got[1].Payload, err)
	}
	// The second setpoint is not on the allowlist: refused, never published, so the recipe is an error.
	if len(rec.writes) != 2 || !rec.writes[0].Written || rec.writes[0].Readback != nil || !strings.Contains(rec.writes[0].Reason, "no readback") {
		t.Fatalf("writes %+v", rec.writes)
	}
	if rec.writes[1].Written || rec.reports[len(rec.reports)-1] != "m1:error" {
		t.Fatalf("refused write %+v reports %v", rec.writes[1], rec.reports)
	}
}

func TestMQTTWriterReportsABrokerThatIsNotThere(t *testing.T) {
	sess := &mqtt.Session{Options: mqtt.Options{Addr: "127.0.0.1:1"}, ConnectWait: 50 * time.Millisecond}
	rec := &fakeRecipes{recipes: []Recipe{{ID: "m2", Destination: "pcs-mqtt", Status: "in-execution", Topic: "t/recipe", Document: json.RawMessage(`{}`)}}}
	r, _ := simRuntime(t, newServer(), 1, Options{Recipes: rec, Writers: map[string]Writer{"pcs-mqtt": &MQTTWriter{Session: sess}}})
	if _, err := r.ExecuteRecipes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.reports) != 1 || rec.reports[0] != "m2:error" {
		t.Fatalf("reports %v", rec.reports)
	}
}
