package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
)

func outbound(partnerID, set string) Outbound {
	return Outbound{PartnerID: partnerID, TransactionSet: set, Canonical: []byte(`{"doc":1}`),
		Build: func(n int64) (string, error) { return fmt.Sprintf("X12-%d", n), nil }}
}

// receive997 records one inbound interchange carrying a single parsed 997.
func receive997(t *testing.T, st *Store, ctx context.Context, partnerID, isa13 string, ack domain.FunctionalAck) Received {
	t.Helper()
	res, err := st.ReceiveInterchange(ctx, Inbound{PartnerID: partnerID, Version: "00401", ISAControl: isa13, Raw: "997", AckStatus: "ACCEPTED",
		Messages: []Message{{ID: ids.New(), TransactionSet: "997", STControl: "0001", Status: "RECEIVED", Ack: &ack}},
		BuildAck: func(func() (int64, error)) (string, error) { return "", nil }})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func ackedEvents(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, ediev.MessageAcked).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateOutbound_SharesTheGapFreeCounterAndRollsBack(t *testing.T) {
	pool := migrated(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "edi.operate")
	st := New(pool, 30)
	p, _, err := st.CreatePartner(ctx, "p", "h", profile("DIST"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReceiveInterchange(ctx, Inbound{PartnerID: p.ID, Version: "00401", ISAControl: "000000001", Raw: "850",
		AckStatus: "ACCEPTED", BuildAck: counting997}); err != nil {
		t.Fatal(err)
	}
	// A failed build rolls back: its number and its key are not consumed.
	failing := outbound(p.ID, "856")
	failing.Build = func(int64) (string, error) { return "", errors.New("serializer refused") }
	if _, _, err := st.CreateOutbound(ctx, "k1", "h1", failing); err == nil {
		t.Fatal("a failing build must fail the call")
	}
	first, created, err := st.CreateOutbound(ctx, "k1", "h1", outbound(p.ID, "856"))
	if err != nil || !created || first.ControlNumber != "000000002" || first.X12 != "X12-2" {
		t.Fatalf("first outbound after the 997 that took 1: %+v created=%v err=%v", first, created, err)
	}
	again, created, err := st.CreateOutbound(ctx, "k1", "h1", outbound(p.ID, "856"))
	if err != nil || created || again != first {
		t.Fatalf("replay must return the stored result: %+v created=%v err=%v", again, created, err)
	}
	if _, _, err := st.CreateOutbound(ctx, "k1", "other", outbound(p.ID, "810")); !errors.Is(err, ErrIdempotencyReuse) {
		t.Fatalf("same key, other request: %v", err)
	}
	second, _, err := st.CreateOutbound(ctx, "k2", "h2", outbound(p.ID, "810"))
	if err != nil || second.ControlNumber != "000000003" {
		t.Fatalf("second outbound: %+v %v", second, err)
	}
	list, err := st.ListInterchanges(ctx, InterchangeFilter{Direction: "OUT", Limit: 10})
	if err != nil || len(list) != 2 || list[0].AckStatus != "PENDING" {
		t.Fatalf("outbound list: %+v %v", list, err)
	}
	if in, err := st.ListInterchanges(ctx, InterchangeFilter{Direction: "IN", Limit: 10}); err != nil || len(in) != 1 {
		t.Fatalf("inbound list: %+v %v", in, err)
	}
}

func TestMatchAck_RejectionDuplicatesAndMismatches(t *testing.T) {
	pool := migrated(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "edi.operate")
	st := New(pool, 30)
	p, _, err := st.CreatePartner(ctx, "p", "h", profile("DIST"))
	if err != nil {
		t.Fatal(err)
	}
	asn, _, err := st.CreateOutbound(ctx, "k1", "h1", outbound(p.ID, "856"))
	if err != nil {
		t.Fatal(err)
	}
	inv, _, err := st.CreateOutbound(ctx, "k2", "h2", outbound(p.ID, "810"))
	if err != nil {
		t.Fatal(err)
	}

	// AK1 names SH but group 2 carried an 810 (GS01 IN): not a match.
	res := receive997(t, st, ctx, p.ID, "000000101", domain.FunctionalAck{FunctionalID: "SH", GroupControl: "2", Code: "A"})
	if m := res.Messages[0]; m.Status != "FAILED" || !strings.Contains(m.Error, domain.CodeUnmatchedAck) || res.AckStatus != "ACCEPTED_WITH_ERRORS" {
		t.Fatalf("functional id mismatch: %+v %s", m, res.AckStatus)
	}

	// The 856 is rejected by AK5 R: FAILED, interchange REJECTED, one event.
	res = receive997(t, st, ctx, p.ID, "000000102", domain.FunctionalAck{FunctionalID: "SH", GroupControl: "1", Code: "R",
		Sets: []domain.AckedSet{{SetID: "856", Control: "0001", Code: "R"}, {SetID: "856", Control: "0009", Code: "A"}}})
	if m := res.Messages[0]; m.Status != "POSTED" || !strings.Contains(m.Error, "AK2 856 0009") {
		t.Fatalf("rejecting 997: %+v", m)
	}
	d, err := st.GetInterchange(ctx, asn.InterchangeID)
	if err != nil || d.AckStatus != "REJECTED" || d.Messages[0].Status != "FAILED" || !strings.Contains(d.Messages[0].Error, "AK5 R") {
		t.Fatalf("856 after rejection: %+v %v", d, err)
	}
	if n := ackedEvents(t, pool); n != 1 {
		t.Fatalf("message.acked events = %d, want 1", n)
	}

	// The same 997 again changes nothing and publishes nothing.
	res = receive997(t, st, ctx, p.ID, "000000103", domain.FunctionalAck{FunctionalID: "SH", GroupControl: "1", Code: "A",
		Sets: []domain.AckedSet{{SetID: "856", Control: "0001", Code: "A"}}})
	if m := res.Messages[0]; m.Status != "POSTED" || !strings.Contains(m.Error, "already acknowledged") {
		t.Fatalf("repeated 997: %+v", m)
	}
	if d, _ := st.GetInterchange(ctx, asn.InterchangeID); d.AckStatus != "REJECTED" || ackedEvents(t, pool) != 1 {
		t.Fatalf("a repeated 997 must not change the interchange or publish: %s", d.AckStatus)
	}

	// A group-level 997 (no AK2) accepts every message of group 2 with AK9 E.
	receive997(t, st, ctx, p.ID, "000000104", domain.FunctionalAck{FunctionalID: "IN", GroupControl: "2", Code: "E"})
	d, err = st.GetInterchange(ctx, inv.InterchangeID)
	if err != nil || d.AckStatus != "ACCEPTED_WITH_ERRORS" || d.Messages[0].Status != "ACKED" {
		t.Fatalf("810 after group-level 997: %+v %v", d, err)
	}
	if n := ackedEvents(t, pool); n != 2 {
		t.Fatalf("message.acked events = %d, want 2", n)
	}

	// Another tenant's 997 never reaches this tenant's interchanges.
	other := testkit.Ctx(testkit.TenantID(2), "edi.operate")
	q, _, err := st.CreatePartner(other, "p", "h", profile("DIST"))
	if err != nil {
		t.Fatal(err)
	}
	res = receive997(t, st, other, q.ID, "000000001", domain.FunctionalAck{FunctionalID: "SH", GroupControl: "1", Code: "A"})
	if res.Messages[0].Status != "FAILED" {
		t.Fatalf("tenant 2 matched tenant 1's outbound: %+v", res.Messages[0])
	}
}
