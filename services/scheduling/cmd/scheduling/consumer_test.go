package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/api"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
	"github.com/BenjaminSRussell/passage/services/scheduling/migrations"
)

type fakeSource struct {
	queue  []eventing.Record
	cancel context.CancelFunc
	acked  []int64
}

func (s *fakeSource) Poll(ctx context.Context) ([]eventing.Record, error) {
	if len(s.queue) == 0 {
		s.cancel()
		return nil, ctx.Err()
	}
	batch := s.queue
	s.queue = nil
	return batch, nil
}

func (s *fakeSource) Commit(_ context.Context, r eventing.Record) error {
	s.acked = append(s.acked, r.Offset)
	return nil
}

type parked struct {
	topic, reason string
	offset        int64
}

type fakeDLQ struct{ sent []parked }

func (d *fakeDLQ) Send(_ context.Context, topic string, r eventing.Record, reason string) error {
	d.sent = append(d.sent, parked{topic, reason, r.Offset})
	return nil
}

func newRig(t *testing.T) (*store.Postgres, func(records ...eventing.Record) (*fakeSource, *fakeDLQ), func(sql string, args ...any) int) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(app)
	a := api.New(st, api.DefaultConfig)
	run := func(records ...eventing.Record) (*fakeSource, *fakeDLQ) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		src, dlq := &fakeSource{queue: records, cancel: cancel}, &fakeDLQ{}
		c, err := eventing.NewConsumer(src, dlq, eventing.NewPgInbox(app), eventing.Config{
			Name: api.DowntimeConsumerName, Idempotent: true, Handlers: a.DowntimeConsumerHandlers(),
			DeadLetterTopic: api.DowntimeRecordedTopic + deadLetterSuffix, MaxAttempts: 2, Backoff: func(int) time.Duration { return 0 },
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		return src, dlq
	}
	count := func(sql string, args ...any) int {
		var n int
		if err := admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return st, run, count
}

func downtimeRecord(t *testing.T, offset int64, eventID, tenantID, resourceID, downtimeID string, from, to time.Time) eventing.Record {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"downtimeId": downtimeID, "resourceId": resourceID, "workOrderId": ids.New(), "assetId": ids.New(),
		"from": from.Format(time.RFC3339Nano), "to": to.Format(time.RFC3339Nano), "minutes": int(to.Sub(from).Minutes()),
	})
	if err != nil {
		t.Fatal(err)
	}
	evt := events.Event{SpecVersion: "1.0", ID: eventID, Source: "/passage/eam", Type: api.DowntimeRecordedType, Time: time.Now().UTC(),
		DataContentType: "application/json", Data: data, TenantID: tenantID}
	value, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	return eventing.Record{Topic: api.DowntimeRecordedTopic, Offset: offset, Value: value, Headers: map[string]string{"tenantid": tenantID}}
}

func monday(hour int) time.Time { return time.Date(2031, 3, 3, hour, 0, 0, 0, time.UTC) }

func TestDowntimeConsumer_ClosesCapacityOnceAcrossRedeliveries(t *testing.T) {
	st, run, count := newRig(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "planner", Duties: map[string]struct{}{}})
	res, _, err := st.CreateResource(ctx, store.Resource{Code: "PRESS-1", Name: "Press", Finite: true, TimeZone: "UTC",
		Shifts: []domain.Shift{{Weekday: time.Monday, StartMinute: 8 * 60, EndMinute: 16 * 60}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	downtimeID := ids.New()
	eventID := ids.New()
	first := downtimeRecord(t, 1, eventID, testkit.TenantID(1), res.ID, downtimeID, monday(10), monday(12))
	redelivered := downtimeRecord(t, 2, eventID, testkit.TenantID(1), res.ID, downtimeID, monday(10), monday(12))
	republished := downtimeRecord(t, 3, ids.New(), testkit.TenantID(1), res.ID, downtimeID, monday(10), monday(12))

	src, dlq := run(first, redelivered, republished)

	if len(dlq.sent) != 0 || len(src.acked) != 3 {
		t.Fatalf("dead-lettered %+v, acked %v; want everything handled", dlq.sent, src.acked)
	}
	if n := count(`select count(*) from scheduling_capacity_override where resource_id = $1::uuid and kind = 'downtime'`, res.ID); n != 1 {
		t.Fatalf("%d downtime overrides after three deliveries, want exactly one", n)
	}
	capacity, err := st.ResourceCapacity(ctx, res.ID, monday(0), monday(0).Add(24*time.Hour))
	if err != nil || len(capacity) != 2 || !capacity[0].End.Equal(monday(10)) || !capacity[1].Start.Equal(monday(12)) {
		t.Fatalf("capacity = %+v err=%v, want 08-10 and 12-16", capacity, err)
	}
	if n := count(`select count(*) from consumer_inbox where consumer = $1`, api.DowntimeConsumerName); n != 2 {
		t.Fatalf("%d inbox rows, want one per distinct event id (2)", n)
	}
}

func TestDowntimeConsumer_AnotherTenantsResourceIsNotFoundAndDeadLettered(t *testing.T) {
	st, run, count := newRig(t)
	owner := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "planner", Duties: map[string]struct{}{}})
	res, _, err := st.CreateResource(owner, store.Resource{Code: "PRESS-1", Name: "Press", Finite: true, TimeZone: "UTC",
		Shifts: []domain.Shift{{Weekday: time.Monday, StartMinute: 8 * 60, EndMinute: 16 * 60}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	foreign := downtimeRecord(t, 1, ids.New(), testkit.TenantID(2), res.ID, ids.New(), monday(10), monday(12))
	unknown := downtimeRecord(t, 2, ids.New(), testkit.TenantID(1), ids.New(), ids.New(), monday(10), monday(12))
	good := downtimeRecord(t, 3, ids.New(), testkit.TenantID(1), res.ID, ids.New(), monday(10), monday(12))

	src, dlq := run(foreign, unknown, good)

	if len(dlq.sent) != 2 || dlq.sent[0].offset != 1 || dlq.sent[1].offset != 2 {
		t.Fatalf("dead-lettered %+v, want offsets 1 and 2", dlq.sent)
	}
	for _, p := range dlq.sent {
		if p.topic != "passage.eam.downtime.dlq" || p.reason != "permanent" {
			t.Fatalf("parked %+v", p)
		}
	}
	if len(src.acked) != 3 {
		t.Fatalf("acked %v, want every offset so one poison event does not stall the partition", src.acked)
	}
	if n := count(`select count(*) from scheduling_capacity_override where tenant_id = $1::uuid`, testkit.TenantID(2)); n != 0 {
		t.Fatalf("the foreign tenant event wrote %d overrides", n)
	}
	if n := count(`select count(*) from scheduling_capacity_override where resource_id = $1::uuid`, res.ID); n != 1 {
		t.Fatalf("%d overrides on the resource, want only the good event's", n)
	}
}

func TestDowntimeConsumer_AFortyDayWindowIsSplitIntoSpansOfAtMost31Days(t *testing.T) {
	st, run, count := newRig(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "planner", Duties: map[string]struct{}{}})
	res, _, err := st.CreateResource(ctx, store.Resource{Code: "PRESS-1", Name: "Press", Finite: true, TimeZone: "UTC",
		Shifts: []domain.Shift{{Weekday: time.Monday, StartMinute: 8 * 60, EndMinute: 16 * 60}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	eventID, downtimeID := ids.New(), ids.New()
	long := downtimeRecord(t, 1, eventID, testkit.TenantID(1), res.ID, downtimeID, monday(0), monday(0).Add(40*24*time.Hour))
	again := downtimeRecord(t, 2, ids.New(), testkit.TenantID(1), res.ID, downtimeID, monday(0), monday(0).Add(40*24*time.Hour))

	_, dlq := run(long, again)

	if len(dlq.sent) != 0 {
		t.Fatalf("dead-lettered %+v", dlq.sent)
	}
	if n := count(`select count(*) from scheduling_capacity_override where resource_id = $1::uuid and end_at - start_at <= interval '31 days'`, res.ID); n != 2 {
		t.Fatalf("%d spans, want 2 (31 + 9 days) after a republish", n)
	}
	if n := count(`select count(*) from scheduling_capacity_override where resource_id = $1::uuid and idempotency_key in ($2, $3)`, res.ID, "eam-downtime:"+downtimeID, "eam-downtime:"+downtimeID+":1"); n != 2 {
		t.Fatalf("%d keyed spans, want the plain key and :1", n)
	}
}

func TestDowntimeConsumer_PrincipalHoldsOnlyTheDowntimeDuty(t *testing.T) {
	p, err := tenant.ServicePrincipal(api.DowntimeConsumerName, testkit.TenantID(1), "")
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != "service:scheduling-downtime" || len(p.Duties) != 1 || !p.HasDuty(api.DutyDowntimeApply) || tenant.IsTenantAdmin(p) || p.HasDuty(tenant.DutyAllScope) {
		t.Fatalf("principal = %+v", p)
	}
}

func TestKafkaConfig(t *testing.T) {
	dir := t.TempDir()
	passwordFile := filepath.Join(dir, "password")
	if err := os.WriteFile(passwordFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	cfg, err := kafkaConfig(env(map[string]string{"KAFKA_BROKERS": "a:9093", "KAFKA_USERNAME": "scheduling-consumer", "KAFKA_PASSWORD_FILE": passwordFile}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != "s3cret" || cfg.Group != "scheduling-downtime" || len(cfg.Topics) != 1 || cfg.Topics[0] != "passage.eam.downtime" {
		t.Fatalf("config = %+v", cfg)
	}
	if _, err := kafkaConfig(env(nil)); err == nil {
		t.Fatal("accepted a config without brokers")
	}
}

func TestRunRefusesUnknownSubcommand(t *testing.T) {
	err := run(context.Background(), func(string) string { return "" }, []string{"scheduling", "migrate"})
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("err = %v", err)
	}
}
