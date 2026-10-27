package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// lastEventData returns the data of the newest outbox event of eventType.
func lastEventData(t *testing.T, pool *pgxpool.Pool, tenantID, eventType string, into any) {
	t.Helper()
	var raw []byte
	inTenant(t, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select payload->'data' from outbox where type = $1 order by created_at desc, id desc limit 1`,
			eventType).Scan(&raw)
	})
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode %s data %s: %v", eventType, raw, err)
	}
}

func at(h, m, s int) time.Time { return time.Date(2026, 10, 20, h, m, s, 0, time.UTC) }

// FR-25.17 retry job through the database: a failed destination is published again
// only once its wait has elapsed, naming that destination alone; it is then leased
// until its next scheduled attempt, so a consumer that does not answer is published
// to on the schedule, not every minute; the consumer's next failure moves it on to
// the next wait (AC-25.13: 1, 5, 15, 60 minutes).
func TestProcessMessage_RetrySweepFollowsSchedule(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(34)
	ctx := testkit.Ctx(tenantID)
	raw, cat := qualityMessage(t, "sweep-1")
	in, err := s.IngestProcessMessage(ctx, raw, cat, at(9, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	id := in.Message.ID
	if claims, err := s.ClaimRetries(ctx, at(9, 30, 0), 10); err != nil || len(claims) != 0 {
		t.Fatalf("destinations that never reported were swept: %+v %v", claims, err)
	}
	if _, err := s.ReportDelivery(ctx, id, domain.DestEBR, domain.OutcomeDelivered, nil, at(10, 0, 0), idem("s-ebr")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReportDelivery(ctx, id, domain.DestQuality, domain.OutcomeFailed, ptr("QMS down"), at(10, 0, 0), idem("s-q1")); err != nil {
		t.Fatal(err)
	}

	if claims, err := s.ClaimRetries(ctx, at(10, 0, 59), 10); err != nil || len(claims) != 0 {
		t.Fatalf("swept before the 1 minute wait: %+v %v", claims, err)
	}
	claims, err := s.ClaimRetries(ctx, at(10, 1, 0), 10)
	want := []RetryClaim{{MessageID: id, Destination: domain.DestQuality, Attempts: 1, NextAttemptAt: at(10, 2, 0)}}
	if err != nil || !reflect.DeepEqual(claims, want) {
		t.Fatalf("due sweep = %+v %v, want %+v", claims, err, want)
	}
	var data sfevents.ProcessMessageReceivedData
	lastEventData(t, app, tenantID, sfevents.ProcessMessageReceived, &data)
	if data.MessageID != id || !reflect.DeepEqual(data.Destinations, []string{domain.DestQuality}) || data.SourceMessageID != "sweep-1" || len(data.Values) != 1 {
		t.Fatalf("retry event = %+v", data)
	}
	if claims, _ := s.ClaimRetries(ctx, at(10, 1, 30), 10); len(claims) != 0 {
		t.Fatalf("swept again during the lease: %+v", claims)
	}
	if claims, _ := s.ClaimRetries(ctx, at(10, 2, 0), 10); len(claims) != 1 {
		t.Fatalf("unanswered destination not swept after its lease: %+v", claims)
	}
	if n := countEvents(t, app, tenantID, sfevents.ProcessMessageReceived); n != 3 {
		t.Fatalf("received events = %d, want ingest plus two retries", n)
	}

	m, err := s.ReportDelivery(ctx, id, domain.DestQuality, domain.OutcomeFailed, ptr("QMS down"), at(10, 3, 0), idem("s-q2"))
	if err != nil {
		t.Fatal(err)
	}
	if q := findDelivery(t, m, domain.DestQuality); q.Attempts != 2 || !q.NextAttemptAt.Equal(at(10, 8, 0)) {
		t.Fatalf("after the second failure = %+v", q)
	}
	if claims, _ := s.ClaimRetries(ctx, at(10, 7, 59), 10); len(claims) != 0 {
		t.Fatalf("swept before the 5 minute wait: %+v", claims)
	}
	claims, _ = s.ClaimRetries(ctx, at(10, 8, 0), 10)
	if len(claims) != 1 || claims[0].Attempts != 2 || !claims[0].NextAttemptAt.Equal(at(10, 13, 0)) {
		t.Fatalf("second retry = %+v, want a 5 minute lease", claims)
	}
	m, err = s.ReportDelivery(ctx, id, domain.DestQuality, domain.OutcomeDelivered, nil, at(10, 8, 5), idem("s-q3"))
	if err != nil || m.Status != domain.MessageDelivered {
		t.Fatalf("delivered after the retry = %+v %v", m, err)
	}
	if claims, _ := s.ClaimRetries(ctx, at(12, 0, 0), 10); len(claims) != 0 {
		t.Fatalf("delivered destination swept: %+v", claims)
	}
}

// The sweep is one event per message for its due destinations, honours its limit,
// skips a message another transaction holds (skip locked) and sees only its own
// tenant (row-level security).
func TestProcessMessage_RetrySweepLimitLockAndTenant(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(35)
	ctx := testkit.Ctx(tenantID)
	cat, _ := domain.CategoryByCode("deviation") // ebr, quality, kafka, webhook
	var msgIDs []string
	for i, src := range []string{"dev-1", "dev-2"} {
		raw := domain.NewMessage{Source: domain.SourceKiosk, SourceMessageID: src, CategoryCode: "deviation", OrderID: ids.New(), OccurredAt: at(9, i, 0)}
		res, err := s.IngestProcessMessage(ctx, raw, cat, at(9, i, 0))
		if err != nil {
			t.Fatal(err)
		}
		msgIDs = append(msgIDs, res.Message.ID)
		for _, dest := range []string{domain.DestQuality, domain.DestWebhook} {
			if _, err := s.ReportDelivery(ctx, res.Message.ID, dest, domain.OutcomeFailed, nil, at(10, i, 0), idem(src+dest)); err != nil {
				t.Fatal(err)
			}
		}
	}
	other := testkit.Ctx(testkit.TenantID(36))
	if claims, err := s.ClaimRetries(other, at(11, 0, 0), 10); err != nil || len(claims) != 0 {
		t.Fatalf("another tenant swept these messages: %+v %v", claims, err)
	}

	// Hold the first message the way a report or a delete does; the sweep passes it by.
	inTenant(t, app, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `select 1 from process_message where id = $1::uuid for update`, msgIDs[0]); err != nil {
			return err
		}
		claims, err := s.ClaimRetries(ctx, at(11, 0, 0), 10)
		if err != nil {
			return err
		}
		if len(claims) != 2 || claims[0].MessageID != msgIDs[1] || claims[1].MessageID != msgIDs[1] {
			t.Errorf("sweep with the first message locked = %+v", claims)
		}
		return nil
	})
	var data sfevents.ProcessMessageReceivedData
	lastEventData(t, app, tenantID, sfevents.ProcessMessageReceived, &data)
	if data.MessageID != msgIDs[1] || !reflect.DeepEqual(data.Destinations, []string{domain.DestQuality, domain.DestWebhook}) {
		t.Fatalf("one event for both due destinations, got %+v", data)
	}

	claims, err := s.ClaimRetries(ctx, at(11, 0, 0), 1)
	if err != nil || len(claims) != 1 || claims[0].MessageID != msgIDs[0] {
		t.Fatalf("limit 1 sweep = %+v %v", claims, err)
	}
}

// FR-25.17 delete with reason: refused while every destination can still retry
// (SF-0119); allowed once a destination is in error; kept for audit with who and
// why; leaves the monitor; takes no report, reprocess or second delete (SF-0120);
// the sweep stops retrying its other destinations; the deleted event names the
// destinations that never received it; a repeated Idempotency-Key replays.
func TestProcessMessage_DeleteWithReason(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(37)
	ctx := testkit.Ctx(tenantID)
	cat, _ := domain.CategoryByCode("deviation")
	raw := domain.NewMessage{Source: domain.SourceEdge, SourceMessageID: "del-1", CategoryCode: "deviation", OrderID: ids.New(), OccurredAt: at(9, 0, 0)}
	res, err := s.IngestProcessMessage(ctx, raw, cat, at(9, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Message.ID
	if _, err := s.ReportDelivery(ctx, id, domain.DestEBR, domain.OutcomeDelivered, nil, at(9, 1, 0), idem("d-ebr")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReportDelivery(ctx, id, domain.DestWebhook, domain.OutcomeFailed, nil, at(9, 1, 0), idem("d-wh")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteProcessMessage(ctx, id, "duplicate", at(9, 2, 0), idem("d-early")); problemCode(err) != domain.CodeNotDeletable {
		t.Fatalf("delete while retrying: %v", err)
	}
	for i := 0; i < domain.MaxDeliveryAttempts; i++ {
		if _, err := s.ReportDelivery(ctx, id, domain.DestQuality, domain.OutcomeFailed, ptr("QMS down"), at(9, 10+i, 0), idem("d-q"+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}

	deleted, err := s.DeleteProcessMessage(ctx, id, "sample lost in the lab", at(11, 0, 0), idem("d-1"))
	if err != nil {
		t.Fatal(err)
	}
	if deleted.DeletedAt == nil || !deleted.DeletedAt.Equal(at(11, 0, 0)) || *deleted.DeletedBy != "user-test" || *deleted.DeleteReason != "sample lost in the lab" {
		t.Fatalf("deleted = %+v", deleted)
	}
	again, err := s.LookupReplay(ctx, idem("d-1"))
	if err != nil || again == nil || again.Status != 200 {
		t.Fatalf("stored outcome = %+v %v", again, err)
	}
	got, err := s.GetProcessMessage(ctx, id)
	if err != nil || got.DeletedAt == nil || *got.DeleteReason != "sample lost in the lab" {
		t.Fatalf("read back = %+v %v", got, err)
	}
	if list, _ := s.ListProcessMessages(ctx, ProcessFilter{Limit: 10}); len(list) != 0 {
		t.Fatalf("deleted message still in the monitor: %+v", list)
	}
	if _, err := s.DeleteProcessMessage(ctx, id, "again", at(11, 1, 0), idem("d-2")); problemCode(err) != CodeMessageDeleted {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := s.ReprocessDelivery(ctx, id, domain.DestQuality, "QMS back", at(11, 1, 0), idem("d-3")); problemCode(err) != CodeMessageDeleted {
		t.Fatalf("reprocess after delete: %v", err)
	}
	if _, err := s.ReportDelivery(ctx, id, domain.DestWebhook, domain.OutcomeDelivered, nil, at(11, 1, 0), idem("d-4")); problemCode(err) != CodeMessageDeleted {
		t.Fatalf("report after delete: %v", err)
	}
	if claims, _ := s.ClaimRetries(ctx, at(20, 0, 0), 10); len(claims) != 0 {
		t.Fatalf("sweep retried the webhook of a deleted message: %+v", claims)
	}
	if n := countEvents(t, app, tenantID, sfevents.ProcessMessageDeleted); n != 1 {
		t.Fatalf("deleted events = %d, want 1", n)
	}
	var data sfevents.ProcessMessageDeletedData
	lastEventData(t, app, tenantID, sfevents.ProcessMessageDeleted, &data)
	if data.MessageID != id || data.Reason != "sample lost in the lab" || data.DeletedBy != "user-test" ||
		!reflect.DeepEqual(data.Undelivered, []string{domain.DestQuality, domain.DestWebhook}) {
		t.Fatalf("deleted event = %+v", data)
	}
	if _, err := s.DeleteProcessMessage(testkit.Ctx(testkit.TenantID(38)), id, "x", at(11, 2, 0), idem("d-5")); problemCode(err) != CodeProcessNotFound {
		t.Fatalf("another tenant deleted the message: %v", err)
	}
}

// Keyset paging of the monitor walks every message once, newest first, with the id
// breaking ties between messages received in the same instant, and honours filters.
func TestProcessMessage_MonitorKeysetPaging(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(39))
	cat, _ := domain.CategoryByCode("custom")
	received := []time.Time{at(9, 0, 0), at(9, 1, 0), at(9, 1, 0), at(9, 1, 0), at(9, 2, 0)}
	for i, when := range received {
		raw := domain.NewMessage{Source: domain.SourceManual, SourceMessageID: "page-" + string(rune('a'+i)), CategoryCode: "custom", OrderID: ids.New(), OccurredAt: when}
		if _, err := s.IngestProcessMessage(ctx, raw, cat, when); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListProcessMessages(ctx, ProcessFilter{Limit: 10})
	if err != nil || len(all) != len(received) {
		t.Fatalf("full list = %d %v", len(all), err)
	}
	var walked []string
	var after *ProcessCursor
	for page := 0; page < 10; page++ {
		items, err := s.ListProcessMessages(ctx, ProcessFilter{Limit: 2, After: after})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 {
			break
		}
		for _, m := range items {
			walked = append(walked, m.ID)
		}
		last := items[len(items)-1]
		after = &ProcessCursor{ReceivedAt: last.ReceivedAt, ID: last.ID}
	}
	for i, m := range all {
		if i >= len(walked) || walked[i] != m.ID {
			t.Fatalf("paged order %v differs from the full list at %d", walked, i)
		}
		if i > 0 && (m.ReceivedAt.After(all[i-1].ReceivedAt) || (m.ReceivedAt.Equal(all[i-1].ReceivedAt) && m.ID > all[i-1].ID)) {
			t.Fatalf("list not newest first at %d", i)
		}
	}
	if len(walked) != len(all) {
		t.Fatalf("walked %d messages, want %d", len(walked), len(all))
	}
	if items, _ := s.ListProcessMessages(ctx, ProcessFilter{Status: domain.MessageError, Limit: 2}); len(items) != 0 {
		t.Fatalf("status filter with keyset = %+v", items)
	}
}
