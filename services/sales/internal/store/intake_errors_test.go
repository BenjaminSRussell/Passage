package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// TestPostgres_IntakeErrorLog covers the intake error log (PRD 5.27.14): a
// refusal is stored with the event, the same payload failing again raises
// attempts instead of adding a row, another code or payload is its own row,
// resolve needs the current version and an OPEN row, and a retry reopens it.
func TestPostgres_IntakeErrorLog(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	in := NewIntakeError{LegalEntityID: le, Channel: "EDI", ExternalRef: "PO-7001", RequestHash: "h1", ErrorCode: CodeProductNotListed, Message: "product not listed"}
	first, err := s.RecordIntakeError(ctx, in, now)
	if err != nil || first.Attempts != 1 || first.Status != IntakeErrorOpen || first.ExternalRef == nil || *first.ExternalRef != "PO-7001" || first.Version != 1 {
		t.Fatalf("record: %v %+v", err, first)
	}
	again, err := s.RecordIntakeError(ctx, in, now.Add(time.Hour))
	if err != nil || again.ID != first.ID || again.Attempts != 2 || !again.LastSeenAt.After(again.FirstSeenAt) || again.Version != 2 {
		t.Fatalf("a retry of the same payload raises attempts: %v %+v", err, again)
	}
	if got := outboxTypes(t, pool, first.ID); len(got) != 2 || got[0] != salesevents.IntakeErrorRecorded {
		t.Fatalf("one recorded event per attempt: %v", got)
	}
	other := in
	other.ErrorCode, other.RequestHash = CodeDuplicateIntake, "h2"
	if _, err := s.RecordIntakeError(ctx, other, now); err != nil {
		t.Fatal(err)
	}
	noRef := NewIntakeError{LegalEntityID: le, Channel: "PORTAL", RequestHash: "h3", ErrorCode: "COM-0001", Message: "lines must have 1 to 2000 lines"}
	bare, err := s.RecordIntakeError(ctx, noRef, now)
	if err != nil || bare.ExternalRef != nil {
		t.Fatalf("a request without a reference: %v %+v", err, bare)
	}
	if again, err := s.RecordIntakeError(ctx, noRef, now); err != nil || again.ID != bare.ID || again.Attempts != 2 {
		t.Fatalf("a missing reference dedups too: %v %+v", err, again)
	}

	all, err := s.ListIntakeErrors(ctx, IntakeErrorFilter{LegalEntityID: le, Limit: 50})
	if err != nil || len(all) != 3 {
		t.Fatalf("list: %v %+v", err, all)
	}
	if edi, err := s.ListIntakeErrors(ctx, IntakeErrorFilter{LegalEntityID: le, Channel: "EDI", Limit: 50}); err != nil || len(edi) != 2 {
		t.Fatalf("channel filter: %v %+v", err, edi)
	}
	if byCode, err := s.ListIntakeErrors(ctx, IntakeErrorFilter{Status: IntakeErrorOpen, ErrorCode: CodeDuplicateIntake, Limit: 50}); err != nil || len(byCode) != 1 {
		t.Fatalf("code filter: %v %+v", err, byCode)
	}
	if none, err := s.ListIntakeErrors(ctx, IntakeErrorFilter{LegalEntityID: ids.New(), Limit: 50}); err != nil || len(none) != 0 {
		t.Fatalf("another legal entity: %v %+v", err, none)
	}

	if _, err := s.ResolveIntakeError(ctx, again.ID, 1, "fixed", "ops", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("a stale version: %v", err)
	}
	done, err := s.ResolveIntakeError(ctx, again.ID, again.Version, "mapping corrected", "ops", now.Add(2*time.Hour))
	if err != nil || done.Status != IntakeErrorResolvedStatus || done.ResolvedBy == nil || *done.ResolvedBy != "ops" || done.ResolvedAt == nil || done.Version != again.Version+1 {
		t.Fatalf("resolve: %v %+v", err, done)
	}
	if _, err := s.ResolveIntakeError(ctx, again.ID, done.Version, "again", "ops", now); problemCode(t, err) != CodeIntakeErrorResolved {
		t.Fatalf("resolving twice: %v", err)
	}
	if _, err := s.ResolveIntakeError(ctx, ids.New(), 1, "x", "ops", now); problemCode(t, err) != CodeIntakeErrorNotFound {
		t.Fatalf("unknown id: %v", err)
	}
	reopened, err := s.RecordIntakeError(ctx, in, now.Add(3*time.Hour))
	if err != nil || reopened.ID != again.ID || reopened.Status != IntakeErrorOpen || reopened.Attempts != 3 || reopened.ResolvedAt != nil {
		t.Fatalf("a retry of a resolved payload reopens it: %v %+v", err, reopened)
	}
	if resolvedOnes, err := s.ListIntakeErrors(ctx, IntakeErrorFilter{Status: IntakeErrorResolvedStatus, Limit: 50}); err != nil || len(resolvedOnes) != 0 {
		t.Fatalf("nothing stays resolved: %v %+v", err, resolvedOnes)
	}
	if got := outboxTypes(t, pool, done.ID); len(got) != 4 || got[2] != salesevents.IntakeErrorResolved {
		t.Fatalf("resolved event: %v", got)
	}
	// The events carry the hash and the code, never the message or a payload.
	var data map[string]any
	if err := json.Unmarshal(outboxData(t, pool, done.ID, salesevents.IntakeErrorResolved), &data); err != nil {
		t.Fatal(err)
	}
	if data["request_hash"] != "h1" || data["actor"] != "ops" || data["message"] != nil || data["payload"] != nil {
		t.Fatalf("resolved data: %v", data)
	}
	// The workspace tile counts the OPEN ones of the legal entity.
	if sum, err := s.Summary(ctx, SummaryFilter{LegalEntityID: le, Today: now, WindowDays: 90}); err != nil || sum.OpenIntakeErrors != 3 {
		t.Fatalf("open intake errors tile: %v %d", err, sum.OpenIntakeErrors)
	}
	if sum, err := s.Summary(ctx, SummaryFilter{LegalEntityID: ids.New(), Today: now, WindowDays: 90}); err != nil || sum.OpenIntakeErrors != 0 {
		t.Fatalf("another legal entity's tile: %v %d", err, sum.OpenIntakeErrors)
	}
}

// TestPostgres_IntakeErrorStaysInScope: under the application role, a caller
// scoped to one legal entity records and sees refusals of that entity only;
// row-level security refuses a refusal logged for another entity.
func TestPostgres_IntakeErrorStaysInScope(t *testing.T) {
	if os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s is needed for the row-level security test", testkit.EnvAppURL)
	}
	s, _, ctx := migrated(t)
	own, foreign := ids.New(), ids.New()
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.RecordIntakeError(ctx, NewIntakeError{LegalEntityID: foreign, Channel: "EDI", RequestHash: "f", ErrorCode: "COM-0001", Message: "x"}, now); err != nil {
		t.Fatal(err)
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "edi", LegalEntityID: own})
	if _, err := s.RecordIntakeError(scoped, NewIntakeError{LegalEntityID: own, Channel: "EDI", RequestHash: "o", ErrorCode: "COM-0001", Message: "x"}, now); err != nil {
		t.Fatalf("own legal entity: %v", err)
	}
	if _, err := s.RecordIntakeError(scoped, NewIntakeError{LegalEntityID: foreign, Channel: "EDI", RequestHash: "f", ErrorCode: "COM-0001", Message: "x"}, now); err == nil {
		t.Fatal("a refusal was logged for a legal entity outside the caller's scope")
	}
	if got, err := s.ListIntakeErrors(scoped, IntakeErrorFilter{Limit: 50}); err != nil || len(got) != 1 || got[0].LegalEntityID != own {
		t.Fatalf("scoped list: %v %+v", err, got)
	}
	other, err := s.ListIntakeErrors(ctx, IntakeErrorFilter{LegalEntityID: foreign, Limit: 50})
	if err != nil || len(other) != 1 || other[0].Attempts != 1 {
		t.Fatalf("the foreign row is untouched: %v %+v", err, other)
	}
	if _, err := s.GetIntakeError(scoped, other[0].ID); problemCode(t, err) != CodeIntakeErrorNotFound {
		t.Fatalf("read across legal entities: %v", err)
	}
	if _, err := s.ResolveIntakeError(scoped, other[0].ID, other[0].Version, "x", "edi", now); problemCode(t, err) != CodeIntakeErrorNotFound {
		t.Fatalf("resolve across legal entities: %v", err)
	}
}

// TestPostgres_JournalOrderTakesNoCreditCheckOrAllocation covers BR-27.7: a
// JOURNAL order confirms although it is over the credit limit (no hold), and
// draws nothing from the product's allocation quota, while the same order of
// type ORDER is held and an ORDER confirmed under quota draws it.
func TestPostgres_JournalOrderTakesNoCreditCheckOrAllocation(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer, product := ids.New(), ids.New(), ids.New()
	limits := domain.CreditProfile{Limit: d(100), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(0), ToleranceAmount: d(0), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits}, 0); err != nil {
		t.Fatal(err)
	}
	quota, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, nil, "2027-03-01", "2027-03-31", 5))
	if err != nil {
		t.Fatal(err)
	}
	in := orderOf(le, customer, product, "2027-03-10", 20) // 200 over the 100 limit and 20 over the quota of 5
	in.OrderType = domain.OrderTypeJournal
	journal := confirmed(t, s, ctx, in)
	if journal.Status != domain.OrderConfirmed || len(journal.Holds) != 0 {
		t.Fatalf("a JOURNAL order confirms without a credit hold: %+v", journal)
	}
	got, err := s.GetAllocationQuota(ctx, quota.ID)
	if err != nil || !got.ConsumedQty.IsZero() {
		t.Fatalf("a JOURNAL order draws no quota: %v %+v", err, got)
	}
	var event map[string]any
	if err := json.Unmarshal(outboxData(t, pool, journal.ID, salesevents.OrderConfirmed), &event); err != nil || event["order_type"] != domain.OrderTypeJournal {
		t.Fatalf("order.confirmed names the type so consumers reserve nothing: %v %v", err, event)
	}
	// Raising a confirmed JOURNAL order's total takes no credit check and
	// draws no quota either.
	more := decimal.NewFromInt(40)
	changed, err := s.ChangeOrder(ctx, journal.ID, journal.Version, ChangeRequest{Reason: "more", Lines: []LineChange{{LineNo: 1, Quantity: &more}}}, "sales-rep", time.Now())
	if err != nil || len(changed.Holds) != 0 || !changed.NetTotal.Equal(d(400)) {
		t.Fatalf("a raised JOURNAL order is not held: %v %+v", err, changed)
	}
	if got, err := s.GetAllocationQuota(ctx, quota.ID); err != nil || !got.ConsumedQty.IsZero() {
		t.Fatalf("a changed JOURNAL order draws no quota: %v %+v", err, got)
	}
	ord := orderOf(le, customer, product, "2027-03-10", 20)
	o, _, err := s.CreateOrder(ctx, ord)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", time.Now()); err != nil || res.Confirmed || len(res.Order.Holds) != 1 {
		t.Fatalf("the same ORDER is held: %v %+v", err, res)
	}
}

// TestPostgres_IntakeErrorReplay covers the replay bookkeeping (PRD 5.27.10):
// the refused body and key are kept on the OPEN row, a replay refused again
// counts an attempt and a recorded event, a replay that stored its order closes
// the row, clears the body and links the order, dead letters are left out of the
// bulk list, and a retry of the same payload keeps a new body for a later replay.
func TestPostgres_IntakeErrorReplay(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	body := []byte(`{"channel":"EDI","externalRef":"PO-1"}`)
	in := NewIntakeError{LegalEntityID: le, Channel: "EDI", ExternalRef: "PO-1", RequestHash: "h1", ErrorCode: CodeProductNotListed,
		Message: "not listed", Payload: body, IdempotencyKey: "key-1"}
	rec, err := s.RecordIntakeError(ctx, in, now)
	if err != nil || !rec.Replayable || rec.ReplayCount != 0 {
		t.Fatalf("record keeps the body: %v %+v", err, rec)
	}
	old := NewIntakeError{LegalEntityID: le, Channel: "EDI", RequestHash: "h0", ErrorCode: "COM-0001", Message: "old row"}
	oldRow, err := s.RecordIntakeError(ctx, old, now)
	if err != nil || oldRow.Replayable {
		t.Fatalf("a row without a body is not replayable: %v %+v", err, oldRow)
	}

	got, err := s.GetIntakeReplay(ctx, rec.ID)
	if err != nil || string(got.Payload) == "" || got.IdempotencyKey != "key-1" {
		t.Fatalf("replay read: %v %+v", err, got)
	}
	var sent map[string]string
	if err := json.Unmarshal(got.Payload, &sent); err != nil || sent["externalRef"] != "PO-1" {
		t.Fatalf("the kept body: %v %s", err, got.Payload)
	}
	if _, err := s.GetIntakeReplay(ctx, oldRow.ID); problemCode(t, err) != CodeIntakeNotReplayable {
		t.Fatalf("a row without a body: %v", err)
	}
	if _, err := s.GetIntakeReplay(ctx, ids.New()); problemCode(t, err) != CodeIntakeErrorNotFound {
		t.Fatalf("unknown id: %v", err)
	}

	rows, skipped, err := s.ListIntakeReplays(ctx, IntakeErrorFilter{LegalEntityID: le, Limit: 50})
	if err != nil || len(rows) != 1 || rows[0].ID != rec.ID || skipped != 0 || rows[0].IdempotencyKey != "key-1" {
		t.Fatalf("bulk list: %v %d %+v", err, skipped, rows)
	}
	if rows, _, err := s.ListIntakeReplays(ctx, IntakeErrorFilter{LegalEntityID: le, Channel: "PORTAL", Limit: 50}); err != nil || len(rows) != 0 {
		t.Fatalf("channel filter: %v %+v", err, rows)
	}

	failed, err := s.RecordReplayFailure(ctx, rec.ID, CodeProductNotListed, "still not listed", now.Add(time.Hour))
	if err != nil || failed.Attempts != 2 || failed.ReplayCount != 1 || failed.Status != IntakeErrorOpen || failed.Version != rec.Version+1 ||
		!failed.Replayable || failed.Message != CodeProductNotListed+": still not listed" {
		t.Fatalf("replay refused again: %v %+v", err, failed)
	}
	if got := outboxTypes(t, pool, rec.ID); len(got) != 2 || got[1] != salesevents.IntakeErrorRecorded {
		t.Fatalf("an attempt event: %v", got)
	}

	// Dead letters: ten attempts leave the bulk list and are counted.
	if _, err := pool.Exec(ctx, `update intake_error set attempts = $2 where id = $1`, rec.ID, MaxReplayAttempts); err != nil {
		t.Fatal(err)
	}
	if rows, skipped, err := s.ListIntakeReplays(ctx, IntakeErrorFilter{LegalEntityID: le, Limit: 50}); err != nil || len(rows) != 0 || skipped != 1 {
		t.Fatalf("dead letter: %v %d %+v", err, skipped, rows)
	}
	if _, err := s.GetIntakeReplay(ctx, rec.ID); err != nil {
		t.Fatalf("a dead letter can still be replayed by hand: %v", err)
	}

	orderID := ids.New()
	done, err := s.ResolveReplayed(ctx, rec.ID, orderID, "SO-77", "ops", now.Add(2*time.Hour))
	if err != nil || done.Status != IntakeErrorResolvedStatus || done.ReplayedOrderID == nil || *done.ReplayedOrderID != orderID || done.Replayable ||
		done.ResolutionNote == nil || *done.ResolutionNote != "Replayed as order SO-77" || done.ResolvedBy == nil || *done.ResolvedBy != "ops" || done.ReplayCount != 2 {
		t.Fatalf("replay stored the order: %v %+v", err, done)
	}
	if got := outboxTypes(t, pool, rec.ID); got[len(got)-1] != salesevents.IntakeErrorResolved {
		t.Fatalf("resolved event: %v", got)
	}
	var kept, key *string
	if err := pool.QueryRow(ctx, `select request_payload::text, idempotency_key from intake_error where id = $1`, rec.ID).Scan(&kept, &key); err != nil || kept != nil || key != nil {
		t.Fatalf("the body is cleared on resolution: %v %v %v", err, kept, key)
	}
	// A second resolution (a concurrent replay) is a no-op, not a second event.
	before := len(outboxTypes(t, pool, rec.ID))
	if again, err := s.ResolveReplayed(ctx, rec.ID, ids.New(), "SO-78", "other", now); err != nil || again.Version != done.Version || *again.ReplayedOrderID != orderID {
		t.Fatalf("resolving twice: %v %+v", err, again)
	}
	if len(outboxTypes(t, pool, rec.ID)) != before {
		t.Fatal("no second resolved event")
	}
	if _, err := s.GetIntakeReplay(ctx, rec.ID); problemCode(t, err) != CodeIntakeErrorResolved {
		t.Fatalf("a resolved row cannot be replayed: %v", err)
	}
	if _, err := s.RecordReplayFailure(ctx, rec.ID, "X", "y", now); problemCode(t, err) != CodeIntakeErrorResolved {
		t.Fatalf("a failure on a resolved row: %v", err)
	}

	// The same payload failing again reopens the row, clears the order link and
	// keeps the new body for the next replay.
	in.Payload = []byte(`{"channel":"EDI","externalRef":"PO-1","v":2}`)
	reopened, err := s.RecordIntakeError(ctx, in, now.Add(3*time.Hour))
	if err != nil || reopened.Status != IntakeErrorOpen || reopened.ReplayedOrderID != nil || !reopened.Replayable {
		t.Fatalf("reopened: %v %+v", err, reopened)
	}
}

// TestPostgres_IntakeReplayKeepsOneTimeBuyersSealed: the body of a refused
// one-time customer order is personal data, so it is kept only as a fieldcrypt
// envelope (never in clear), opened for the replay, cleared on resolution, and
// not kept at all by a service without field encryption keys.
func TestPostgres_IntakeReplayKeepsOneTimeBuyersSealed(t *testing.T) {
	s, pool, ctx := sealedStore(t)
	le := ids.New()
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	body := []byte(`{"channel":"WEB","externalRef":"PO-OT","oneTime":{"name":"Ada Buyer","taxId":"DE123456789"}}`)
	in := NewIntakeError{LegalEntityID: le, Channel: "WEB", ExternalRef: "PO-OT", RequestHash: "h-ot", ErrorCode: CodeProductNotListed,
		Message: "not listed", Payload: body, IdempotencyKey: "key-ot", PersonalData: true}
	rec, err := s.RecordIntakeError(ctx, in, now)
	if err != nil || !rec.Replayable {
		t.Fatalf("a sealed body is replayable: %v %+v", err, rec)
	}
	var clear, sealed *string
	if err := pool.QueryRow(ctx, `select request_payload::text, encode(request_payload_sealed, 'escape') from intake_error where id = $1`,
		rec.ID).Scan(&clear, &sealed); err != nil || clear != nil || sealed == nil {
		t.Fatalf("kept sealed only: %v %v %v", err, clear, sealed)
	}
	for _, secret := range []string{"Ada Buyer", "DE123456789"} {
		if strings.Contains(*sealed, secret) {
			t.Fatalf("%q is readable in the stored envelope", secret)
		}
	}
	got, err := s.GetIntakeReplay(ctx, rec.ID)
	if err != nil || string(got.Payload) != string(body) || got.IdempotencyKey != "key-ot" {
		t.Fatalf("the replay opens the body: %v %s", err, got.Payload)
	}
	rows, _, err := s.ListIntakeReplays(ctx, IntakeErrorFilter{LegalEntityID: le, Limit: 10})
	if err != nil || len(rows) != 1 || string(rows[0].Payload) != string(body) {
		t.Fatalf("the bulk list opens the body: %v %+v", err, rows)
	}
	// A service without keys cannot open it, and does not keep a new one.
	plain := New(s.pool)
	if _, err := plain.GetIntakeReplay(ctx, rec.ID); problemCode(t, err) != CodeFieldCryptMissing {
		t.Fatalf("no keys: %v", err)
	}
	in2 := in
	in2.RequestHash, in2.ExternalRef = "h-ot2", "PO-OT2"
	unkept, err := plain.RecordIntakeError(ctx, in2, now)
	if err != nil || unkept.Replayable {
		t.Fatalf("without keys a one-time body is not kept: %v %+v", err, unkept)
	}

	if _, err := s.ResolveReplayed(ctx, rec.ID, ids.New(), "SO-9", "ops", now); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select request_payload::text, encode(request_payload_sealed, 'escape') from intake_error where id = $1`,
		rec.ID).Scan(&clear, &sealed); err != nil || clear != nil || sealed != nil {
		t.Fatalf("cleared on resolution: %v %v %v", err, clear, sealed)
	}
}
