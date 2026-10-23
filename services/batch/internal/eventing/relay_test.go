package eventing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

type fakeSource struct {
	rows           []Row
	crashBeforeAck bool
}

func (s *fakeSource) Drain(_ context.Context, limit int, handle func(Row) error) (int, error) {
	n := min(limit, len(s.rows))
	acked := 0
	var handleErr error
	for _, row := range s.rows[:n] {
		if handleErr = handle(row); handleErr != nil {
			break
		}
		acked++
	}
	if s.crashBeforeAck {
		return 0, errors.New("crash before ack")
	}
	s.rows = s.rows[acked:]
	return acked, handleErr
}

type fakeBroker struct {
	messages []Message
	failOn   string
}

func (b *fakeBroker) Publish(_ context.Context, msg Message) error {
	if msg.ID == b.failOn {
		return errors.New("broker unavailable")
	}
	b.messages = append(b.messages, msg)
	return nil
}

type fakeInbox struct{ seen map[string]bool }

func (i *fakeInbox) Once(_ context.Context, consumer, eventID string, effect func(pgx.Tx) error) (bool, error) {
	key := consumer + "/" + eventID
	if i.seen[key] {
		return false, nil
	}
	if err := effect(nil); err != nil {
		return false, err
	}
	i.seen[key] = true
	return true, nil
}

func outboxRow(t *testing.T, id, tenantID, aggregateID string) Row {
	t.Helper()
	evt, err := events.New("dev.passage.platform.legalentity.created.v1", "/passage/platform/test", aggregateID, tenantID, map[string]string{"id": aggregateID})
	if err != nil {
		t.Fatal(err)
	}
	evt.ID = id
	payload, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	return Row{ID: id, TenantID: tenantID, AggregateID: aggregateID, Type: evt.Type, Payload: payload}
}

func newRelay(source Source, broker Publisher) *Relay {
	return NewRelay(source, broker, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func TestRelay_CrashBetweenPublishAndAckDeliversExactlyOnce(t *testing.T) {
	tenantID := testkit.TenantID(1)
	source := &fakeSource{rows: []Row{outboxRow(t, "e1", tenantID, "a1"), outboxRow(t, "e2", tenantID, "a2")}, crashBeforeAck: true}
	broker := &fakeBroker{}
	relay := newRelay(source, broker)
	inbox := &fakeInbox{seen: map[string]bool{}}

	if _, err := relay.Drain(context.Background(), 10); err == nil {
		t.Fatal("expected the crash to surface")
	}
	if len(source.rows) != 2 {
		t.Fatalf("crash before ack must keep rows in the outbox, have %d", len(source.rows))
	}

	source.crashBeforeAck = false
	if n, err := relay.Drain(context.Background(), 10); err != nil || n != 2 {
		t.Fatalf("restart drain = %d, %v; want 2, nil", n, err)
	}
	if len(source.rows) != 0 {
		t.Fatalf("outbox must be empty after restart, have %d", len(source.rows))
	}

	applied := map[string]int{}
	for _, msg := range broker.messages {
		if _, err := inbox.Once(context.Background(), "test-consumer", msg.ID, func(pgx.Tx) error { applied[msg.ID]++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if len(broker.messages) != 4 {
		t.Fatalf("broker must see each event twice, saw %d messages", len(broker.messages))
	}
	if applied["e1"] != 1 || applied["e2"] != 1 {
		t.Fatalf("each event must be applied once, got %v", applied)
	}
}

func TestRelay_Drain(t *testing.T) {
	tenantA, tenantB := testkit.TenantID(1), testkit.TenantID(2)
	malformed := outboxRow(t, "bad", tenantA, "a3")
	malformed.Payload = []byte(`{"type":42}`)
	badType := outboxRow(t, "badtype", tenantA, "a4")
	badType.Type = "not-an-event-type"
	badTenant := outboxRow(t, "badtenant", tenantA, "a5")
	badTenant.TenantID = "not-a-uuid"
	nullPayload := outboxRow(t, "null", tenantA, "a6")
	nullPayload.Payload = []byte(`null`)

	tests := []struct {
		name          string
		rows          []Row
		failOn        string
		limit         int
		wantErr       bool
		wantPublished []string
		wantRemaining int
	}{
		{name: "empty outbox", limit: 5},
		{name: "zero limit", rows: []Row{outboxRow(t, "e1", tenantA, "a1")}, limit: 0, wantErr: true, wantRemaining: 1},
		{name: "negative limit", rows: []Row{outboxRow(t, "e1", tenantA, "a1")}, limit: -1, wantErr: true, wantRemaining: 1},
		{name: "publisher error keeps failed row", rows: []Row{outboxRow(t, "e1", tenantA, "a1"), outboxRow(t, "e2", tenantA, "a2")}, failOn: "e2", limit: 5, wantErr: true, wantPublished: []string{"e1"}, wantRemaining: 1},
		{name: "malformed payload fails closed", rows: []Row{outboxRow(t, "e1", tenantA, "a1"), malformed, outboxRow(t, "e2", tenantA, "a2")}, limit: 5, wantErr: true, wantPublished: []string{"e1"}, wantRemaining: 2},
		{name: "null payload fails closed", rows: []Row{nullPayload}, limit: 5, wantErr: true, wantRemaining: 1},
		{name: "invalid type fails closed", rows: []Row{badType}, limit: 5, wantErr: true, wantRemaining: 1},
		{name: "invalid tenant fails closed", rows: []Row{badTenant}, limit: 5, wantErr: true, wantRemaining: 1},
		{name: "two tenants keep their own keys", rows: []Row{outboxRow(t, "e1", tenantA, "agg"), outboxRow(t, "e2", tenantB, "agg")}, limit: 5, wantPublished: []string{"e1", "e2"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source := &fakeSource{rows: tc.rows}
			broker := &fakeBroker{failOn: tc.failOn}
			_, err := newRelay(source, broker).Drain(context.Background(), tc.limit)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			var published []string
			for _, m := range broker.messages {
				published = append(published, m.ID)
			}
			if len(published) != len(tc.wantPublished) {
				t.Fatalf("published %v, want %v", published, tc.wantPublished)
			}
			if len(source.rows) != tc.wantRemaining {
				t.Fatalf("remaining rows = %d, want %d", len(source.rows), tc.wantRemaining)
			}
		})
	}
}

func TestRelay_MessageKeyAndTopicCarryTenant(t *testing.T) {
	tenantA, tenantB := testkit.TenantID(1), testkit.TenantID(2)
	source := &fakeSource{rows: []Row{outboxRow(t, "e1", tenantA, "agg"), outboxRow(t, "e2", tenantB, "agg")}}
	broker := &fakeBroker{}
	if _, err := newRelay(source, broker).Drain(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if broker.messages[0].Key != tenantA+":agg" || broker.messages[1].Key != tenantB+":agg" {
		t.Fatalf("keys = %q, %q", broker.messages[0].Key, broker.messages[1].Key)
	}
	if broker.messages[0].Topic != "passage.platform.legalentity" {
		t.Fatalf("topic = %q", broker.messages[0].Topic)
	}
}
