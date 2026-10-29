package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
	"github.com/BenjaminSRussell/passage/services/edi/migrations"
)

func migrated(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return pool
}

func profile(id string) domain.PartnerProfile {
	return domain.PartnerProfile{Name: "Partner " + id, ISA: domain.Party{Qualifier: "ZZ", ID: id}, Environment: "PROD"}
}

// counting997 stands in for the 997 builder: it allocates one control number
// and returns it as the payload, so tests can see the sequence.
func counting997(next func() (int64, error)) (string, error) {
	n, err := next()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("997-%d", n), nil
}

func TestCreatePartner_IdempotencyKey(t *testing.T) {
	pool := migrated(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "EDI_ADMIN")
	st := New(pool, 30)
	prof := domain.PartnerProfile{Name: "Demo distributor", ISA: domain.Party{Qualifier: "ZZ", ID: "DISTRIBUTOR"}, Environment: "TEST"}

	first, created, err := st.CreatePartner(ctx, "key-A", "hash-1", prof)
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	if first.AckSLAMinutes != domain.DefaultAckSLAMinutes || first.Version != 1 || first.Status != "active" {
		t.Fatalf("defaults not applied: %+v", first)
	}
	again, created, err := st.CreatePartner(ctx, "key-A", "hash-1", prof)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay must return the stored partner: created=%v err=%v got %+v", created, err, again)
	}
	if _, _, err := st.CreatePartner(ctx, "key-A", "hash-2", prof); !errors.Is(err, ErrIdempotencyReuse) {
		t.Fatalf("same key with another request must be refused, got %v", err)
	}
	if _, _, err := st.CreatePartner(ctx, "key-B", "hash-1", prof); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("same ISA identity under a new key must be a duplicate, got %v", err)
	}
	// The failed duplicate must not have kept its key: key-B is free for a valid request.
	other := prof
	other.ISA.ID = "OTHER"
	if _, created, err := st.CreatePartner(ctx, "key-B", "hash-3", other); err != nil || !created {
		t.Fatalf("key of a rolled-back request must be reusable: created=%v err=%v", created, err)
	}
}

func TestPartners_ListAndGet(t *testing.T) {
	pool := migrated(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "EDI_ADMIN")
	st := New(pool, 30)
	a, _, err := st.CreatePartner(ctx, "k1", "h1", domain.PartnerProfile{Name: "Beta", ISA: domain.Party{Qualifier: "ZZ", ID: "B"}, Environment: "TEST"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreatePartner(ctx, "k2", "h2", domain.PartnerProfile{Name: "Alpha", ISA: domain.Party{Qualifier: "ZZ", ID: "A"}, Environment: "PROD"}); err != nil {
		t.Fatal(err)
	}
	all, err := st.ListPartners(ctx, "")
	if err != nil || len(all) != 2 || all[0].Name != "Alpha" || all[1].Name != "Beta" {
		t.Fatalf("list by name: %+v %v", all, err)
	}
	test, err := st.ListPartners(ctx, "TEST")
	if err != nil || len(test) != 1 || test[0].ID != a.ID {
		t.Fatalf("environment filter: %+v %v", test, err)
	}
	got, err := st.GetPartner(ctx, a.ID)
	if err != nil || got.ISAID != "B" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := st.GetPartner(ctx, "00000000-0000-0000-0000-0000000000ff"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown partner: %v", err)
	}
}

func TestReceiveInterchange_DuplicateWindowAndGapFreeControls(t *testing.T) {
	pool := migrated(t)
	tid := testkit.TenantID(2)
	ctx := testkit.Ctx(tid, "EDI_OPERATE")
	st := New(pool, 30)
	p, _, err := st.CreatePartner(ctx, "key-C", "h", profile("P1"))
	if err != nil {
		t.Fatal(err)
	}
	in := Inbound{PartnerID: p.ID, Version: "00401", ISAControl: "000000042", Raw: "raw", AckStatus: "ACCEPTED", BuildAck: counting997,
		Messages: []Message{{ID: "0195f0a0-0000-7000-8000-000000000001", TransactionSet: "850", STControl: "0001", Status: "MAPPED", Canonical: []byte(`{"poNumber":"PO-1"}`)}}}
	first, err := st.ReceiveInterchange(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.AckPayload != "997-1" {
		t.Fatalf("first 997 should use outbound control 1, got %q", first.AckPayload)
	}

	// AC-35.5: the same ISA13 ten days later is still a duplicate, and nothing is written.
	if _, err := pool.Exec(context.Background(), `update interchange set received_at = now() - interval '10 days'`); err != nil {
		t.Fatal(err)
	}
	in.Messages[0].ID = "0195f0a0-0000-7000-8000-000000000002"
	if _, err := st.ReceiveInterchange(ctx, in); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second receipt of ISA13 42 inside the window must be a duplicate, got %v", err)
	}
	var interchanges, messages, events int
	if err := pool.QueryRow(context.Background(), `select (select count(*) from interchange), (select count(*) from message), (select count(*) from outbox)`).
		Scan(&interchanges, &messages, &events); err != nil {
		t.Fatal(err)
	}
	if interchanges != 1 || messages != 1 || events != 1 {
		t.Fatalf("a duplicate must write nothing: interchanges=%d messages=%d events=%d", interchanges, messages, events)
	}

	// The rolled-back duplicate must not consume an outbound number.
	in.ISAControl = "000000043"
	in.Messages[0].ID = "0195f0a0-0000-7000-8000-000000000003"
	second, err := st.ReceiveInterchange(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if second.AckPayload != "997-2" {
		t.Fatalf("control numbers must stay gap-free, got %q", second.AckPayload)
	}

	// Outside the window (BR-35.5) the partner may reuse a control number.
	if _, err := pool.Exec(context.Background(), `update interchange set received_at = now() - interval '31 days' where control_number = '000000042'`); err != nil {
		t.Fatal(err)
	}
	in.ISAControl = "000000042"
	in.Messages[0].ID = "0195f0a0-0000-7000-8000-000000000004"
	if _, err := st.ReceiveInterchange(ctx, in); err != nil {
		t.Fatalf("ISA13 42 after 31 days must be accepted, got %v", err)
	}
}

func TestReceiveInterchange_WritesMessagesAndEventInOneTransaction(t *testing.T) {
	pool := migrated(t)
	tid := testkit.TenantID(3)
	ctx := testkit.Ctx(tid, "EDI_OPERATE")
	st := New(pool, 30)
	p, _, err := st.CreatePartner(ctx, "k", "h", profile("P3"))
	if err != nil {
		t.Fatal(err)
	}
	msgs := []Message{
		{ID: "0195f0a0-0000-7000-8000-000000000011", TransactionSet: "850", STControl: "0001", Status: "MAPPED", Canonical: []byte(`{"poNumber":"PO-9"}`)},
		{ID: "0195f0a0-0000-7000-8000-000000000012", TransactionSet: "810", STControl: "0002", Status: "FAILED", Error: "transaction set 810 is not supported yet"},
	}
	got, err := st.ReceiveInterchange(ctx, Inbound{PartnerID: p.ID, Version: "00401", ISAControl: "000000100", Raw: "raw",
		AckStatus: "ACCEPTED_WITH_ERRORS", Messages: msgs, BuildAck: counting997})
	if err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(context.Background(), `select type, aggregate_id, payload from outbox where type = $1 order by aggregate_id`, ediev.MessageReceived)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var typ, agg string
		var payload []byte
		if err := rows.Scan(&typ, &agg, &payload); err != nil {
			t.Fatal(err)
		}
		var evt struct {
			Type     string                    `json:"type"`
			Source   string                    `json:"source"`
			TenantID string                    `json:"tenantid"`
			Data     ediev.MessageReceivedData `json:"data"`
		}
		if err := json.Unmarshal(payload, &evt); err != nil {
			t.Fatal(err)
		}
		want := msgs[n]
		if typ != ediev.MessageReceived || evt.Type != ediev.MessageReceived || evt.Source != "edi" || evt.TenantID != tid || agg != want.ID {
			t.Fatalf("event envelope = %s %s %s %s", typ, evt.Source, evt.TenantID, agg)
		}
		if evt.Data.InterchangeID != got.InterchangeID || evt.Data.Status != want.Status || evt.Data.TransactionSet != want.TransactionSet || evt.Data.ControlNumber != "000000100" {
			t.Fatalf("event data = %+v", evt.Data)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("want one event per message, got %d", n)
	}
	// The failed 810 also gets one message.failed with its error (FR-35.10 monitor and tasks).
	var failed struct {
		Data ediev.MessageFailedData `json:"data"`
	}
	var payload []byte
	if err := pool.QueryRow(context.Background(), `select payload from outbox where type = $1`, ediev.MessageFailed).Scan(&payload); err != nil {
		t.Fatalf("one message.failed expected: %v", err)
	}
	if err := json.Unmarshal(payload, &failed); err != nil {
		t.Fatal(err)
	}
	if f := failed.Data; f.MessageID != msgs[1].ID || f.InterchangeID != got.InterchangeID || f.Error != msgs[1].Error || f.Reprocessed {
		t.Fatalf("message.failed data = %+v", f)
	}

	detail, err := st.GetInterchange(ctx, got.InterchangeID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.PartnerName != "Partner P3" || detail.MessageCount != 2 || detail.FailedCount != 1 || detail.AckPayload != "997-1" || len(detail.Messages) != 2 {
		t.Fatalf("detail = %+v", detail)
	}
	if string(detail.Messages[0].Canonical) != `{"poNumber": "PO-9"}` || detail.Messages[1].Error == "" {
		t.Fatalf("messages = %+v", detail.Messages)
	}
	list, err := st.ListInterchanges(ctx, InterchangeFilter{AckStatus: "ACCEPTED_WITH_ERRORS", Limit: 10})
	if err != nil || len(list) != 1 || list[0].ID != got.InterchangeID {
		t.Fatalf("list by status: %+v %v", list, err)
	}
	none, err := st.ListInterchanges(ctx, InterchangeFilter{AckStatus: "REJECTED", Limit: 10})
	if err != nil || len(none) != 0 {
		t.Fatalf("status filter must exclude other statuses: %+v %v", none, err)
	}
}

// TestRowLevelSecurity runs the store as the application role, which cannot
// bypass RLS, and checks one tenant never sees another's partners or
// interchanges. It needs PASSAGE_TEST_APP_DATABASE_URL as well.
func TestRowLevelSecurity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL and PASSAGE_TEST_APP_DATABASE_URL are needed for the RLS test")
	}
	admin, app := testkit.DBWithApp(t)
	bg := context.Background()
	if err := dbx.Migrate(bg, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app, 30)
	one := testkit.Ctx(testkit.TenantID(1), "EDI_ADMIN")
	two := testkit.Ctx(testkit.TenantID(2), "EDI_ADMIN")
	p, _, err := st.CreatePartner(one, "k", "h", profile("SHARED"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.ReceiveInterchange(one, Inbound{PartnerID: p.ID, Version: "00401", ISAControl: "000000001", Raw: "raw",
		AckStatus: "ACCEPTED", BuildAck: counting997, Messages: []Message{{ID: "0195f0a0-0000-7000-8000-000000000031",
			TransactionSet: "850", STControl: "0001", Status: "FAILED", Error: "EDI-0103: x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateOutbound(one, "out", "h", outbound(p.ID, "856")); err != nil {
		t.Fatal(err)
	}
	if list, err := st.ListInterchanges(two, InterchangeFilter{Direction: "OUT", Limit: 10}); err != nil || len(list) != 0 {
		t.Fatalf("tenant 2 sees tenant 1 outbound interchanges: %+v %v", list, err)
	}
	if list, err := st.ListPartners(two, ""); err != nil || len(list) != 0 {
		t.Fatalf("tenant 2 sees tenant 1 partners: %+v %v", list, err)
	}
	if _, err := st.GetPartner(two, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant 2 reads tenant 1 partner: %v", err)
	}
	if list, err := st.ListInterchanges(two, InterchangeFilter{Limit: 10}); err != nil || len(list) != 0 {
		t.Fatalf("tenant 2 sees tenant 1 interchanges: %+v %v", list, err)
	}
	// The message monitor, SLA dashboard and reprocess see only the caller's tenant.
	if list, err := st.ListMessages(two, MessageFilter{Limit: 10}); err != nil || len(list) != 0 {
		t.Fatalf("tenant 2 sees tenant 1 messages: %+v %v", list, err)
	}
	if _, err := st.GetMessage(two, in.Messages[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant 2 reads tenant 1 message: %v", err)
	}
	if rows, err := st.PartnerSLAs(two, 30); err != nil || len(rows) != 0 {
		t.Fatalf("tenant 2 sees tenant 1 SLA rows: %+v %v", rows, err)
	}
	noPlan := func(string, []Message) ([]Message, error) {
		t.Fatal("plan must not run for another tenant")
		return nil, nil
	}
	if _, _, err := st.ReprocessInterchange(two, "rp", "h", in.InterchangeID, false, noPlan); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant 2 reprocesses tenant 1 interchange: %v", err)
	}
	if list, err := st.ListMessages(one, MessageFilter{Limit: 10}); err != nil || len(list) != 2 {
		t.Fatalf("tenant 1 must see its own messages: %d %v", len(list), err)
	}
	// The same ISA identity and idempotency key are independent per tenant.
	if _, created, err := st.CreatePartner(two, "k", "h", profile("SHARED")); err != nil || !created {
		t.Fatalf("tenant 2 must create its own partner: created=%v err=%v", created, err)
	}
	if _, err := st.FindPartnerBySender(two, domain.Party{Qualifier: "ZZ", ID: "SHARED"}, "PROD"); err != nil {
		t.Fatal(err)
	}
}
