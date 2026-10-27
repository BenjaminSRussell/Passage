package store

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

func findDelivery(t *testing.T, m ProcessMessage, dest string) domain.Delivery {
	t.Helper()
	for _, d := range m.Deliveries {
		if d.Destination == dest {
			return d
		}
	}
	t.Fatalf("no delivery to %s in %+v", dest, m.Deliveries)
	return domain.Delivery{}
}

func ptr(s string) *string { return &s }

func qualityMessage(t *testing.T, sourceID string) (domain.NewMessage, domain.Category) {
	t.Helper()
	raw := domain.NewMessage{Source: domain.SourceEdge, SourceMessageID: sourceID, CategoryCode: "quality-result", OrderID: ids.New(),
		PhasePath: "/proc/unit/op/phase", OccurredAt: day(9, 0),
		Values: []domain.Characteristic{{Name: "temp_c", Type: "numeric", Value: "13", Max: ptr("12")}}}
	cat, _ := domain.CategoryByCode(raw.CategoryCode)
	values, err := domain.ApplyLimits(cat, raw.Values)
	if err != nil {
		t.Fatal(err)
	}
	raw.Values = values
	return raw, cat
}

// AC-25.13 through the database: a quality result routes to quality, EBR and Kafka;
// Kafka is delivered at commit; the w33 quality destination fails five times (retry
// schedule 1, 5, 15, 60 min) into error while EBR is delivered once, so the message
// is partially delivered; a reprocess with a reason returns quality to pending and
// re-publishes for quality alone; the message is then delivered exactly once.
func TestProcessMessage_RouteRetryAndReprocess(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(30)
	ctx := testkit.Ctx(tenantID)
	raw, cat := qualityMessage(t, "pcs-1")

	first, err := s.IngestProcessMessage(ctx, raw, cat, day(9, 0))
	if err != nil {
		t.Fatal(err)
	}
	m := first.Message
	if first.Duplicate || m.Status != domain.MessagePartial || !m.OutOfLimit || !m.Values[0].OutOfLimit {
		t.Fatalf("first ingest = %+v", m)
	}
	if findDelivery(t, m, domain.DestKafka).Status != domain.DeliveryDelivered {
		t.Fatal("kafka not delivered at commit")
	}

	replay, err := s.IngestProcessMessage(ctx, raw, cat, day(9, 1))
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Duplicate || replay.Message.ID != m.ID || !replay.Message.ReceivedAt.Equal(day(9, 0)) {
		t.Fatalf("replay = %+v", replay)
	}

	m, err = s.ReportDelivery(ctx, m.ID, domain.DestEBR, domain.OutcomeDelivered, nil, day(9, 2), idem("ebr-ok"))
	if err != nil {
		t.Fatal(err)
	}
	waits := []int{1, 5, 15, 60}
	for i := 0; i < domain.MaxDeliveryAttempts; i++ {
		at := day(10, i)
		m, err = s.ReportDelivery(ctx, m.ID, domain.DestQuality, domain.OutcomeFailed, ptr("QMS down"), at, idem("q-fail-"+string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		q := findDelivery(t, m, domain.DestQuality)
		if i < len(waits) && (q.NextAttemptAt == nil || q.NextAttemptAt.Sub(at).Minutes() != float64(waits[i])) {
			t.Fatalf("failure %d: next attempt %v, want +%d min", i+1, q.NextAttemptAt, waits[i])
		}
	}
	if q := findDelivery(t, m, domain.DestQuality); q.Status != domain.DeliveryError || q.Attempts != domain.MaxDeliveryAttempts || q.LastError == nil || *q.LastError != "QMS down" {
		t.Fatalf("quality after five failures = %+v", q)
	}
	if m.Status != domain.MessagePartial {
		t.Fatalf("status with quality in error = %s", m.Status)
	}

	m, err = s.ReprocessDelivery(ctx, m.ID, domain.DestQuality, "QMS restored", day(11, 0), idem("reprocess-1"))
	if err != nil {
		t.Fatal(err)
	}
	if q := findDelivery(t, m, domain.DestQuality); q.Status != domain.DeliveryPending || q.Attempts != 0 {
		t.Fatalf("quality after reprocess = %+v", q)
	}

	m, err = s.ReportDelivery(ctx, m.ID, domain.DestQuality, domain.OutcomeDelivered, nil, day(11, 1), idem("q-ok"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != domain.MessageDelivered {
		t.Fatalf("status after every destination delivered = %s", m.Status)
	}
	late, err := s.ReportDelivery(ctx, m.ID, domain.DestQuality, domain.OutcomeFailed, ptr("late"), day(11, 2), idem("q-late"))
	if err != nil {
		t.Fatal(err)
	}
	if got := findDelivery(t, late, domain.DestQuality); got.Status != domain.DeliveryDelivered || late.Status != domain.MessageDelivered {
		t.Fatalf("a delivered destination changed: %+v", late)
	}
	stored, err := s.GetProcessMessage(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ebr := findDelivery(t, stored, domain.DestEBR); ebr.Attempts != 0 || ebr.Status != domain.DeliveryDelivered {
		t.Fatalf("EBR was touched again: %+v", ebr)
	}

	if n := countEvents(t, app, tenantID, sfevents.ProcessMessageReceived); n != 2 {
		t.Fatalf("processmessage.received rows = %d, want 2 (ingest and reprocess)", n)
	}
	if n := countEvents(t, app, tenantID, sfevents.ProcessMessageDelivered); n != 1 {
		t.Fatalf("processmessage.delivered rows = %d, want 1", n)
	}
	// The reprocess event names only the reprocessed destination, so EBR does not
	// write a second entry (AC-25.13), and it carries the out-of-limit flag.
	var payloads []sfevents.ProcessMessageReceivedData
	inTenant(t, app, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `select payload->'data' from outbox where type = $1 order by created_at, id`, sfevents.ProcessMessageReceived)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			var d sfevents.ProcessMessageReceivedData
			if err := json.Unmarshal(raw, &d); err != nil {
				return err
			}
			payloads = append(payloads, d)
		}
		return rows.Err()
	})
	if len(payloads) != 2 || len(payloads[0].Destinations) != 3 || len(payloads[1].Destinations) != 1 || payloads[1].Destinations[0] != domain.DestQuality ||
		!payloads[1].OutOfLimit || !payloads[1].OccurredAt.Equal(raw.OccurredAt) {
		t.Fatalf("received payloads = %+v", payloads)
	}

	list, err := s.ListProcessMessages(ctx, ProcessFilter{Status: domain.MessageDelivered, Destination: domain.DestQuality, OrderID: raw.OrderID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != m.ID {
		t.Fatalf("delivered list = %+v", list)
	}
	if list, err := s.ListProcessMessages(ctx, ProcessFilter{Destination: domain.DestJournal}); err != nil || len(list) != 0 {
		t.Fatalf("journal list = %+v %v", list, err)
	}
	if _, err := s.GetProcessMessage(ctx, ids.New()); problemCode(err) != CodeProcessNotFound {
		t.Fatalf("unknown message: %v", err)
	}
	// Another tenant sees nothing (row-level security).
	if other, err := s.ListProcessMessages(testkit.Ctx(testkit.TenantID(31)), ProcessFilter{}); err != nil || len(other) != 0 {
		t.Fatalf("tenant 31 sees %+v %v", other, err)
	}
}

// A report replayed with the same Idempotency-Key does not count a failure twice.
func TestProcessMessage_ReportIsIdempotentByKey(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(32))
	raw, cat := qualityMessage(t, "pcs-2")
	res, err := s.IngestProcessMessage(ctx, raw, cat, day(9, 0))
	if err != nil {
		t.Fatal(err)
	}
	key := idem("q-fail")
	if _, err := s.ReportDelivery(ctx, res.Message.ID, domain.DestQuality, domain.OutcomeFailed, ptr("down"), day(9, 1), key); err != nil {
		t.Fatal(err)
	}
	rep, err := s.LookupReplay(ctx, key)
	if err != nil || rep == nil || rep.Status != http.StatusOK {
		t.Fatalf("stored outcome = %+v %v", rep, err)
	}
	if _, err := s.ReportDelivery(ctx, res.Message.ID, domain.DestQuality, domain.OutcomeFailed, ptr("down"), day(9, 2), key); problemCode(err) != CodeIdempotencyConflict {
		t.Fatalf("second use of the key in the store: %v", err)
	}
	m, err := s.GetProcessMessage(ctx, res.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if q := findDelivery(t, m, domain.DestQuality); q.Attempts != 1 {
		t.Fatalf("quality attempts = %d, want 1", q.Attempts)
	}
}

// A destination the category does not route to is SF-0118, a reprocess of a
// destination that is not in error is SF-0116, and an unknown message is SF-0117.
func TestProcessMessage_UnknownDestinationAndNotReprocessable(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(33))
	raw := domain.NewMessage{Source: domain.SourceKiosk, SourceMessageID: "k-9", CategoryCode: "custom", OrderID: ids.New(), OccurredAt: day(9, 0)}
	cat, _ := domain.CategoryByCode("custom")
	res, err := s.IngestProcessMessage(ctx, raw, cat, day(9, 0))
	if err != nil {
		t.Fatal(err)
	}
	if res.Message.Status != domain.MessageDelivered || len(res.Message.Values) != 0 {
		t.Fatalf("custom message with only kafka = %+v", res.Message)
	}
	if _, err := s.ReportDelivery(ctx, res.Message.ID, domain.DestWebhook, domain.OutcomeDelivered, nil, day(9, 1), idem("w")); problemCode(err) != CodeDeliveryNotFound {
		t.Fatalf("webhook report for a custom message: %v", err)
	}
	if _, err := s.ReprocessDelivery(ctx, res.Message.ID, domain.DestKafka, "retry", day(9, 1), idem("r")); problemCode(err) != domain.CodeNotReprocessable {
		t.Fatalf("delivered kafka reprocessed: %v", err)
	}
	if _, err := s.ReprocessDelivery(ctx, ids.New(), domain.DestKafka, "retry", day(9, 1), idem("r2")); problemCode(err) != CodeProcessNotFound {
		t.Fatalf("unknown message reprocessed: %v", err)
	}
}
