package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
)

// receive records one inbound interchange with the given messages.
func receive(t *testing.T, st *Store, ctx context.Context, partnerID, isa13 string, msgs ...Message) Received {
	t.Helper()
	res, err := st.ReceiveInterchange(ctx, Inbound{PartnerID: partnerID, Version: "00401", ISAControl: isa13, Raw: "raw " + isa13,
		AckStatus: "ACCEPTED", Messages: msgs, BuildAck: counting997})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func msg(set, control, status, errText string) Message {
	return Message{ID: ids.New(), TransactionSet: set, STControl: control, Status: status, Error: errText, Canonical: []byte(`{"poNumber":"PO-1"}`)}
}

func outboxCount(t *testing.T, pool *pgxpool.Pool, typ string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestListMessages_FiltersAndKeysetPages(t *testing.T) {
	pool := migrated(t)
	ctx := testkit.Ctx(testkit.TenantID(11), "EDI_OPERATE")
	st := New(pool, 30)
	p, _, err := st.CreatePartner(ctx, "k", "h", profile("MON"))
	if err != nil {
		t.Fatal(err)
	}
	receive(t, st, ctx, p.ID, "000000001", msg("850", "0001", "MAPPED", ""))
	failedIn := receive(t, st, ctx, p.ID, "000000002", msg("850", "0001", "FAILED", "EDI-0103: CTT01 2 does not match PO1 count 1"))
	receive(t, st, ctx, p.ID, "000000003", msg("850", "0001", "MAPPED", ""))
	out, _, err := st.CreateOutbound(ctx, "out-1", "h-out", Outbound{PartnerID: p.ID, TransactionSet: "856", Canonical: []byte(`{"shipmentId":"S1"}`),
		Build: func(n int64) (string, error) { return "ISA 856", nil }})
	if err != nil {
		t.Fatal(err)
	}

	// Newest first over both directions, two at a time, no overlap and no gap.
	var seen []MessageRecord
	f := MessageFilter{Limit: 2}
	for page := 0; page < 3; page++ {
		rows, err := st.ListMessages(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, rows...)
		if len(rows) < f.Limit {
			break
		}
		last := rows[len(rows)-1]
		f.After = &Cursor{At: last.CreatedAt, ID: last.ID}
	}
	if len(seen) != 4 {
		t.Fatalf("pages returned %d messages, want 4", len(seen))
	}
	wantOrder := []string{out.ControlNumber, "000000003", "000000002", "000000001"}
	for i, m := range seen {
		if m.ControlNumber != wantOrder[i] {
			t.Fatalf("position %d: ISA13 %s, want %s", i, m.ControlNumber, wantOrder[i])
		}
		if m.PartnerName != "Partner MON" || m.Canonical != nil {
			t.Fatalf("list row = %+v", m)
		}
	}
	if seen[0].Direction != "OUT" || seen[0].AckStatus != "PENDING" || seen[0].TransactionSet != "856" {
		t.Fatalf("outbound row = %+v", seen[0])
	}

	for name, tc := range map[string]struct {
		f    MessageFilter
		want int
	}{
		"status":          {MessageFilter{Status: "FAILED", Limit: 10}, 1},
		"direction":       {MessageFilter{Direction: "OUT", Limit: 10}, 1},
		"transaction set": {MessageFilter{TransactionSet: "850", Limit: 10}, 3},
		"control number":  {MessageFilter{ControlNumber: "000000002", Limit: 10}, 1},
		"partner":         {MessageFilter{PartnerID: ids.New(), Limit: 10}, 0},
	} {
		rows, err := st.ListMessages(ctx, tc.f)
		if err != nil || len(rows) != tc.want {
			t.Errorf("%s: %d rows (want %d), err=%v", name, len(rows), tc.want, err)
		}
	}

	got, err := st.GetMessage(ctx, failedIn.Messages[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InterchangeID != failedIn.InterchangeID || got.Status != "FAILED" || got.Error == "" || string(got.Canonical) != `{"poNumber": "PO-1"}` {
		t.Fatalf("message = %+v canonical %s", got, got.Canonical)
	}
	if _, err := st.GetMessage(ctx, ids.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown message: %v", err)
	}
}

func TestReprocessInterchange(t *testing.T) {
	pool := migrated(t)
	ctx := testkit.Ctx(testkit.TenantID(13), "EDI_OPERATE")
	st := New(pool, 30)
	p, _, err := st.CreatePartner(ctx, "k", "h", profile("RP"))
	if err != nil {
		t.Fatal(err)
	}
	fixed := msg("850", "0001", "FAILED", "EDI-0105: no item cross-reference")
	same := msg("850", "0002", "FAILED", "EDI-0103: CTT01 2 does not match PO1 count 1")
	other := msg("850", "0003", "FAILED", "EDI-0160: BEG missing")
	ack := msg("997", "0004", "FAILED", "EDI-0161: no outbound interchange")
	ok := msg("850", "0005", "MAPPED", "")
	ic := receive(t, st, ctx, p.ID, "000000010", fixed, same, other, ack, ok)
	ackBefore := ic.AckStatus
	receivedBefore := outboxCount(t, pool, ediev.MessageReceived)
	failedBefore := outboxCount(t, pool, ediev.MessageFailed)

	calls := 0
	plan := func(raw string, failed []Message) ([]Message, error) {
		calls++
		if raw != "raw 000000010" {
			t.Fatalf("plan got raw %q", raw)
		}
		var out []Message
		for _, m := range failed {
			switch m.ID {
			case fixed.ID:
				out = append(out, Message{ID: m.ID, Status: "MAPPED", Canonical: []byte(`{"poNumber":"PO-FIXED"}`)})
			case same.ID:
				out = append(out, Message{ID: m.ID, Status: "FAILED", Error: m.Error})
			case other.ID:
				out = append(out, Message{ID: m.ID, Status: "FAILED", Error: "EDI-0103: CTT01 3 does not match PO1 count 2"})
			default:
				t.Fatalf("plan was given message %s %s, which is not a failed non-997 transaction", m.TransactionSet, m.STControl)
			}
		}
		return out, nil
	}
	hash := "h-" + ic.InterchangeID

	// A dry run reports the outcome and writes nothing.
	dry, _, err := st.ReprocessInterchange(ctx, "", hash, ic.InterchangeID, true, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || dry.Changed != 2 || len(dry.Messages) != 3 {
		t.Fatalf("dry run = %+v", dry)
	}
	if m, _ := st.GetMessage(ctx, fixed.ID); m.Status != "FAILED" {
		t.Fatalf("dry run wrote the message: %+v", m)
	}
	if outboxCount(t, pool, ediev.MessageReceived) != receivedBefore || outboxCount(t, pool, ediev.MessageFailed) != failedBefore {
		t.Fatal("dry run wrote events")
	}

	res, replayed, err := st.ReprocessInterchange(ctx, "rp-1", hash, ic.InterchangeID, false, plan)
	if err != nil || replayed {
		t.Fatalf("reprocess: replayed=%v err=%v", replayed, err)
	}
	if res.DryRun || res.Changed != 2 {
		t.Fatalf("result = %+v", res)
	}
	byID := map[string]ReprocessedMessage{}
	for _, m := range res.Messages {
		byID[m.MessageID] = m
	}
	if m := byID[fixed.ID]; m.Status != "MAPPED" || !m.Changed || m.PreviousError != fixed.Error {
		t.Fatalf("fixed = %+v", m)
	}
	if m := byID[same.ID]; m.Status != "FAILED" || m.Changed {
		t.Fatalf("unchanged = %+v", m)
	}
	if m, _ := st.GetMessage(ctx, fixed.ID); m.Status != "MAPPED" || m.Error != "" || string(m.Canonical) != `{"poNumber": "PO-FIXED"}` {
		t.Fatalf("stored fixed message = %+v %s", m, m.Canonical)
	}
	if m, _ := st.GetMessage(ctx, other.ID); m.Status != "FAILED" || m.Error != "EDI-0103: CTT01 3 does not match PO1 count 2" {
		t.Fatalf("stored other message = %+v", m)
	}
	if m, _ := st.GetMessage(ctx, ack.ID); m.Status != "FAILED" || m.Error != ack.Error {
		t.Fatalf("a received 997 must not be reprocessed: %+v", m)
	}
	// The fixed message is announced again as MAPPED; the one failing differently gets a failed event.
	if got := outboxCount(t, pool, ediev.MessageReceived) - receivedBefore; got != 1 {
		t.Fatalf("new message.received events = %d, want 1", got)
	}
	var payload []byte
	if err := pool.QueryRow(context.Background(), `select payload from outbox where type = $1 and aggregate_id = $2 and payload->'data'->>'reprocessed' = 'true'`,
		ediev.MessageFailed, other.ID).Scan(&payload); err != nil {
		t.Fatalf("reprocessed message.failed: %v", err)
	}
	var evt struct {
		Data ediev.MessageFailedData `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil || evt.Data.InterchangeID != ic.InterchangeID || evt.Data.ControlNumber != "000000010" {
		t.Fatalf("message.failed = %+v %v", evt.Data, err)
	}
	if got := outboxCount(t, pool, ediev.MessageFailed) - failedBefore; got != 1 {
		t.Fatalf("new message.failed events = %d, want 1", got)
	}
	// The interchange keeps the status of the 997 we sent.
	if d, _ := st.GetInterchange(ctx, ic.InterchangeID); d.AckStatus != ackBefore {
		t.Fatalf("ack status changed to %s", d.AckStatus)
	}

	// Replaying the key returns the first result without mapping again.
	before := calls
	again, replayed, err := st.ReprocessInterchange(ctx, "rp-1", hash, ic.InterchangeID, false, plan)
	if err != nil || !replayed || calls != before || again.Changed != 2 {
		t.Fatalf("replay: %+v replayed=%v calls=%d err=%v", again, replayed, calls-before, err)
	}
	// A second reprocess under a new key finds nothing new to change.
	second, _, err := st.ReprocessInterchange(ctx, "rp-2", hash, ic.InterchangeID, false, plan)
	if err != nil || second.Changed != 0 || len(second.Messages) != 2 {
		t.Fatalf("second reprocess = %+v err=%v", second, err)
	}
	if _, _, err := st.ReprocessInterchange(ctx, "rp-1", "another request", ic.InterchangeID, false, plan); !errors.Is(err, ErrIdempotencyReuse) {
		t.Fatalf("key reuse: %v", err)
	}

	if _, _, err := st.ReprocessInterchange(ctx, "rp-3", "h3", ids.New(), false, plan); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown interchange: %v", err)
	}
	out, _, err := st.CreateOutbound(ctx, "out", "h-out", Outbound{PartnerID: p.ID, TransactionSet: "856", Canonical: []byte(`{}`),
		Build: func(int64) (string, error) { return "ISA 856", nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReprocessInterchange(ctx, "rp-4", "h4", out.InterchangeID, false, plan); !errors.Is(err, ErrNotInbound) {
		t.Fatalf("outbound interchange: %v", err)
	}
	// The refused attempts released their keys.
	if _, _, err := st.ReprocessInterchange(ctx, "rp-3", "h-"+ic.InterchangeID, ic.InterchangeID, false, plan); err != nil {
		t.Fatalf("key of a refused attempt must be free again: %v", err)
	}
}

func TestPartnerSLAs(t *testing.T) {
	pool := migrated(t)
	ctx := testkit.Ctx(testkit.TenantID(14), "EDI_OPERATE")
	st := New(pool, 30)
	busy, _, err := st.CreatePartner(ctx, "k1", "h1", profile("BUSY"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreatePartner(ctx, "k2", "h2", profile("IDLE")); err != nil {
		t.Fatal(err)
	}
	receive(t, st, ctx, busy.ID, "000000001", msg("850", "0001", "MAPPED", ""), msg("850", "0002", "FAILED", "EDI-0103: x"))
	old := receive(t, st, ctx, busy.ID, "000000002", msg("850", "0001", "FAILED", "EDI-0103: y"))
	var outIDs []string
	for _, key := range []string{"o1", "o2", "o3"} {
		res, _, err := st.CreateOutbound(ctx, key, "h-"+key, Outbound{PartnerID: busy.ID, TransactionSet: "856", Canonical: []byte(`{}`),
			Build: func(int64) (string, error) { return "ISA 856", nil }})
		if err != nil {
			t.Fatal(err)
		}
		outIDs = append(outIDs, res.InterchangeID)
	}
	// o1 acked after 30 minutes, o2 after 90 (late against the 60-minute SLA),
	// o3 still pending after 2 hours (overdue). The second inbound is outside the window.
	bg := context.Background()
	for _, q := range []struct {
		sql string
		id  string
	}{
		{`update interchange set received_at = now() - interval '3 hours', acked_at = now() - interval '150 minutes', ack_status = 'ACCEPTED' where id = $1`, outIDs[0]},
		{`update interchange set received_at = now() - interval '3 hours', acked_at = now() - interval '90 minutes', ack_status = 'ACCEPTED' where id = $1`, outIDs[1]},
		{`update interchange set received_at = now() - interval '2 hours' where id = $1`, outIDs[2]},
		{`update message set created_at = now() - interval '40 days' where interchange_id = $1`, old.InterchangeID},
	} {
		if _, err := pool.Exec(bg, q.sql, q.id); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := st.PartnerSLAs(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("partners without traffic are omitted: %+v", rows)
	}
	r := rows[0]
	// 2 inbound in the window (1 failed) + 3 outbound messages.
	if r.PartnerID != busy.ID || r.AckSLAMinutes != 60 || r.Messages != 5 || r.Failed != 1 {
		t.Fatalf("volume = %+v", r)
	}
	if r.Outbound != 3 || r.Acked != 2 || r.LateAcks != 1 || r.OverdueAcks != 1 {
		t.Fatalf("acks = %+v", r)
	}
	// p95 of {30, 90} minutes by linear interpolation: 30 + 0.95 × 60 = 87 minutes.
	if r.P95AckSeconds == nil || *r.P95AckSeconds < 87*60-1 || *r.P95AckSeconds > 87*60+1 {
		t.Fatalf("p95 = %v", r.P95AckSeconds)
	}
	wide, err := st.PartnerSLAs(ctx, 60)
	if err != nil || len(wide) != 1 || wide[0].Messages != 6 || wide[0].Failed != 2 {
		t.Fatalf("60-day window = %+v err=%v", wide, err)
	}
}
