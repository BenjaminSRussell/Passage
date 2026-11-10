package eventing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	tenantA   = "11111111-1111-4111-8111-111111111111"
	eventType = "dev.passage.receiving.receipt.posted.v1"
)

type fakeSource struct {
	queue     []eventing.Record
	cancel    context.CancelFunc
	committed []int64
	commitErr error
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
	if s.commitErr != nil {
		return s.commitErr
	}
	s.committed = append(s.committed, r.Offset)
	return nil
}

type parked struct {
	topic, reason string
	offset        int64
}

type fakeDLQ struct {
	sent    []parked
	details []string
	err     error
}

func (d *fakeDLQ) Send(_ context.Context, topic string, r eventing.Record, reason string) error {
	if d.err != nil {
		return d.err
	}
	d.sent = append(d.sent, parked{topic, reason, r.Offset})
	d.details = append(d.details, r.Headers[eventing.HeaderDeadLetterDetail])
	return nil
}

type fakeInbox struct {
	seen  map[string]bool
	calls int
}

func (i *fakeInbox) Once(ctx context.Context, consumer, eventID string, effect func(pgx.Tx) error) (bool, error) {
	i.calls++
	if _, ok := tenant.FromContext(ctx); !ok {
		return false, eventing.ErrInvalidArgument
	}
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

func (i *fakeInbox) Seen(_ context.Context, consumer, eventID string) (bool, error) {
	return i.seen[consumer+"/"+eventID], nil
}

func (i *fakeInbox) Record(_ context.Context, consumer, eventID string) error {
	i.seen[consumer+"/"+eventID] = true
	return nil
}

type harness struct {
	src   *fakeSource
	dlq   *fakeDLQ
	inbox *fakeInbox
	ran   []events.Event
	who   []tenant.Principal
}

func newHarness(t *testing.T, handle func(h *harness, attempt int) error) (*harness, *eventing.Consumer, context.Context) {
	t.Helper()
	return newHarnessWith(t, false, handle)
}

func newHarnessWith(t *testing.T, idempotent bool, handle func(h *harness, attempt int) error) (*harness, *eventing.Consumer, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &harness{src: &fakeSource{cancel: cancel}, dlq: &fakeDLQ{}, inbox: &fakeInbox{seen: map[string]bool{}}}
	attempt := 0
	c, err := eventing.NewConsumer(h.src, h.dlq, h.inbox, eventing.Config{
		Name: "receiving-ers", Idempotent: idempotent, DeadLetterTopic: "passage.receiving.receipt.dlq", MaxAttempts: 3, Backoff: func(int) time.Duration { return 0 },
		Handlers: map[string]eventing.Handler{eventType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			attempt++
			p, _ := tenant.FromContext(ctx)
			h.who = append(h.who, p)
			h.ran = append(h.ran, evt)
			if handle == nil {
				return nil
			}
			return handle(h, attempt)
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, c, ctx
}

func record(t *testing.T, offset int64, id, typ, tenantID string) eventing.Record {
	t.Helper()
	evt := events.Event{SpecVersion: "1.0", ID: id, Source: "/passage/receiving", Type: typ, Time: time.Now().UTC(), DataContentType: "application/json",
		Data: json.RawMessage(`{"receipt_id":"r1","vendor_iban":"DE00SECRET"}`), TenantID: tenantID}
	value, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	return eventing.Record{Topic: "passage.receiving.receipt", Partition: 1, Offset: offset, Value: value, Headers: map[string]string{"tenantid": tenantID}}
}

func run(t *testing.T, c *eventing.Consumer, ctx context.Context) error {
	t.Helper()
	err := c.Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func TestConsumerAppliesOnceAsServicePrincipalForTheEventsTenant(t *testing.T) {
	h, c, ctx := newHarness(t, nil)
	h.src.queue = []eventing.Record{record(t, 1, "e1", eventType, tenantA), record(t, 2, "e1", eventType, tenantA)}
	if err := run(t, c, ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.ran) != 1 {
		t.Fatalf("handler ran %d times for a duplicate event id, want 1", len(h.ran))
	}
	if got := h.who[0]; got.UserID != "service:receiving-ers" || got.TenantID != tenantA || !got.HasDuty("receiving.vendor-invoice.post") || got.HasDuty(tenant.DutyAllScope) {
		t.Fatalf("handler principal = %+v", got)
	}
	if len(h.src.committed) != 2 || len(h.dlq.sent) != 0 {
		t.Fatalf("committed %v, dead-lettered %v; want both offsets committed and none parked", h.src.committed, h.dlq.sent)
	}
}

func TestConsumerRetriesThenSucceeds(t *testing.T) {
	h, c, ctx := newHarness(t, func(_ *harness, attempt int) error {
		if attempt < 3 {
			return errors.New("ledger unavailable")
		}
		return nil
	})
	h.src.queue = []eventing.Record{record(t, 7, "e1", eventType, tenantA)}
	if err := run(t, c, ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.ran) != 3 || len(h.dlq.sent) != 0 || len(h.src.committed) != 1 {
		t.Fatalf("attempts %d, parked %v, committed %v", len(h.ran), h.dlq.sent, h.src.committed)
	}
}

func TestConsumerDeadLettersAfterBoundedRetries(t *testing.T) {
	h, c, ctx := newHarness(t, func(*harness, int) error { return errors.New("always down") })
	h.src.queue = []eventing.Record{record(t, 7, "e1", eventType, tenantA), record(t, 8, "e2", eventType, tenantA)}
	if err := run(t, c, ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.ran) != 6 {
		t.Fatalf("attempts = %d, want 3 per event", len(h.ran))
	}
	want := []parked{{"passage.receiving.receipt.dlq", "retries_exhausted", 7}, {"passage.receiving.receipt.dlq", "retries_exhausted", 8}}
	if len(h.dlq.sent) != 2 || h.dlq.sent[0] != want[0] || h.dlq.sent[1] != want[1] {
		t.Fatalf("parked = %v, want %v", h.dlq.sent, want)
	}
	if len(h.src.committed) != 2 {
		t.Fatalf("poison records must be committed past, committed %v", h.src.committed)
	}
}

func TestConsumerDeadLettersPermanentFailureWithoutRetry(t *testing.T) {
	h, c, ctx := newHarness(t, func(*harness, int) error { return eventing.Permanent(errors.New("receipt id is not a UUID")) })
	h.src.queue = []eventing.Record{record(t, 3, "e1", eventType, tenantA)}
	if err := run(t, c, ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.ran) != 1 || len(h.dlq.sent) != 1 || h.dlq.sent[0].reason != "permanent" {
		t.Fatalf("attempts %d, parked %v", len(h.ran), h.dlq.sent)
	}
}

func TestConsumerRetriesTransientFailureBeyondTheAttemptLimit(t *testing.T) {
	h, c, ctx := newHarness(t, func(_ *harness, attempt int) error {
		if attempt < 8 {
			return eventing.Transient(errors.New("connection refused"))
		}
		return nil
	})
	h.src.queue = []eventing.Record{record(t, 7, "e1", eventType, tenantA)}
	if err := run(t, c, ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.ran) != 8 || len(h.dlq.sent) != 0 || len(h.src.committed) != 1 {
		t.Fatalf("attempts %d, parked %v, committed %v; a transient failure must outlast MaxAttempts 3", len(h.ran), h.dlq.sent, h.src.committed)
	}
}

func TestConsumerDeadLetterCarriesTheErrorTextBounded(t *testing.T) {
	h, c, ctx := newHarness(t, func(*harness, int) error {
		return eventing.Permanent(errors.New("invoice INV-1: " + strings.Repeat("x", 2000)))
	})
	rec := record(t, 3, "e1", eventType, tenantA)
	h.src.queue = []eventing.Record{rec}
	if err := run(t, c, ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.dlq.details) != 1 || !strings.HasPrefix(h.dlq.details[0], "invoice INV-1: ") || len(h.dlq.details[0]) != 512 {
		t.Fatalf("detail %q (%d bytes), want the error text cut to 512 bytes", h.dlq.details, len(h.dlq.details[0]))
	}
	if _, leaked := rec.Headers[eventing.HeaderDeadLetterDetail]; leaked {
		t.Fatal("the detail header was written onto the source record")
	}
}

func TestConsumerDeadLettersUndecodableRecords(t *testing.T) {
	h, c, ctx := newHarness(t, nil)
	bad := record(t, 4, "e1", eventType, tenantA)
	bad.Value = []byte("{not json")
	wrongTenant := record(t, 5, "e2", eventType, tenantA)
	wrongTenant.Headers["tenantid"] = "22222222-2222-4222-8222-222222222222"
	notUUID := record(t, 6, "e3", eventType, "tenant-x")
	h.src.queue = []eventing.Record{bad, wrongTenant, notUUID}
	if err := run(t, c, ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.ran) != 0 || len(h.dlq.sent) != 3 || len(h.src.committed) != 3 {
		t.Fatalf("handler ran %d, parked %v, committed %v", len(h.ran), h.dlq.sent, h.src.committed)
	}
	if h.inbox.calls != 0 {
		t.Fatal("an undecodable record reached the inbox")
	}
}

func TestConsumerStopsWithoutCommitWhenDeadLetterFails(t *testing.T) {
	h, c, ctx := newHarness(t, func(*harness, int) error { return eventing.Permanent(errors.New("bad")) })
	h.dlq.err = errors.New("broker down")
	h.src.queue = []eventing.Record{record(t, 9, "e1", eventType, tenantA)}
	err := run(t, c, ctx)
	if err == nil || !strings.Contains(err.Error(), "dead-letter") {
		t.Fatalf("err = %v, want a dead-letter failure", err)
	}
	if len(h.src.committed) != 0 {
		t.Fatalf("committed %v although the record was neither handled nor parked", h.src.committed)
	}
}

func TestConsumerSkipsUnhandledTypesAndReportsCommitFailure(t *testing.T) {
	h, c, ctx := newHarness(t, nil)
	h.src.queue = []eventing.Record{record(t, 1, "e1", "dev.passage.receiving.receipt.corrected.v1", tenantA)}
	if err := run(t, c, ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.ran) != 0 || len(h.src.committed) != 1 {
		t.Fatalf("ran %d committed %v", len(h.ran), h.src.committed)
	}

	h, c, ctx = newHarness(t, nil)
	h.src.commitErr = errors.New("coordinator gone")
	h.src.queue = []eventing.Record{record(t, 1, "e1", eventType, tenantA)}
	if err := run(t, c, ctx); err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("err = %v, want a commit failure", err)
	}
}

func TestConsumerMetricsCarryNoPayload(t *testing.T) {
	h, c, ctx := newHarness(t, nil)
	h.src.queue = []eventing.Record{record(t, 1, "e1", eventType, tenantA), record(t, 2, "e1", eventType, tenantA)}
	if err := run(t, c, ctx); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c.Metrics().Write(&out, "receiving-ers")
	for _, want := range []string{`event_consumer_applied_total{consumer="receiving-ers"} 1`, `event_consumer_duplicates_total{consumer="receiving-ers"} 1`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("metrics missing %q:\n%s", want, out.String())
		}
	}
}

func TestNewConsumerRefusesIncompleteOrUnknownPrincipal(t *testing.T) {
	handlers := map[string]eventing.Handler{eventType: eventing.HandlerFunc(func(context.Context, pgx.Tx, events.Event) error { return nil })}
	src, dlq, inbox := &fakeSource{}, &fakeDLQ{}, &fakeInbox{}
	if _, err := eventing.NewConsumer(src, dlq, inbox, eventing.Config{Name: "receiving-ers", Handlers: handlers}); !errors.Is(err, eventing.ErrInvalidArgument) {
		t.Fatalf("no dead-letter topic err = %v", err)
	}
	if _, err := eventing.NewConsumer(src, dlq, inbox, eventing.Config{Name: "root", DeadLetterTopic: "x", Handlers: handlers}); !errors.Is(err, tenant.ErrUnknownServicePrincipal) {
		t.Fatalf("unknown principal err = %v", err)
	}
}

func TestIdempotentModeRunsTheHandlerOutsideATransactionAndRecordsAfterSuccess(t *testing.T) {
	h, c, ctx := newHarnessWith(t, true, func(_ *harness, attempt int) error {
		if attempt == 1 {
			return errors.New("ledger unavailable")
		}
		return nil
	})
	h.src.queue = []eventing.Record{record(t, 1, "e1", eventType, tenantA), record(t, 2, "e1", eventType, tenantA)}
	if err := run(t, c, ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.ran) != 2 {
		t.Fatalf("handler ran %d times, want the failed attempt and one success (the duplicate is skipped)", len(h.ran))
	}
	if !h.inbox.seen["receiving-ers/e1"] || len(h.dlq.sent) != 0 || len(h.src.committed) != 2 {
		t.Fatalf("seen %v parked %v committed %v", h.inbox.seen, h.dlq.sent, h.src.committed)
	}
}
