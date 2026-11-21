package cdc_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/auditctx"
	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/audit/internal/cdc"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
	"github.com/BenjaminSRussell/depot/services/audit/internal/store"
	"github.com/BenjaminSRussell/depot/services/audit/migrations"
)

type source struct {
	queue     []eventing.Record
	cancel    context.CancelFunc
	committed int
}

func (s *source) Poll(ctx context.Context) ([]eventing.Record, error) {
	if len(s.queue) == 0 {
		s.cancel()
		return nil, ctx.Err()
	}
	batch := s.queue
	s.queue = nil
	return batch, nil
}

func (s *source) Commit(context.Context, eventing.Record) error { s.committed++; return nil }

type dlq struct{ parked []string }

func (d *dlq) Send(_ context.Context, _ string, r eventing.Record, reason string) error {
	d.parked = append(d.parked, reason+": "+r.Headers[eventing.HeaderDeadLetterDetail])
	return nil
}

type env struct {
	admin, app *pgxpool.Pool
	store      *store.Postgres
	handler    *cdc.Handler
}

func setup(t *testing.T) env {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "u"})
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	return env{admin: admin, app: app, store: s, handler: &cdc.Handler{Registry: cdc.NewRegistry(cdc.Tables), Sink: s, Verifier: trusted}}
}

// consume runs the real eventing consumer, with the platform inbox, over records as the Kafka source would hand them.
func (e env) consume(t *testing.T, records ...eventing.Record) *dlq {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src, parked := &source{queue: records, cancel: cancel}, &dlq{}
	c, err := eventing.NewConsumer(src, parked, eventing.NewPgInbox(e.app), eventing.Config{
		Name: cdc.ConsumerName, DeadLetterTopic: "depot.audit.cdc.dlq", Handlers: e.handler.Handlers(),
		Decode: cdc.Decoder{Registry: e.handler.Registry}.Decode, MaxAttempts: 1, Backoff: func(int) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if src.committed != len(records) {
		t.Fatalf("committed %d of %d records", src.committed, len(records))
	}
	return parked
}

func reader(n int, le string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: le, UserID: "auditor"})
}

func person(n int) auditctx.Message {
	return auditctx.Message{V: auditctx.Version, TenantID: testkit.TenantID(n), Actor: "ana", ActorKind: auditctx.ActorPerson,
		Reason: "vendor letter dated 3 Nov", TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", ChangeSetID: "cs-812"}
}

// TestCapturedChangeIsRecordedWithItsContext: a captured insert and update of a vendor bank account become one chain
// row per field, with the actor, reason and trace id of the depot.ctx message of the same source transaction, the
// row's legal entity and source party; a redelivered record is recorded once, and the chain verifies.
func TestCapturedChangeIsRecordedWithItsContext(t *testing.T) {
	e := setup(t)
	t1 := testkit.TenantID(1)
	before := account(t1, "b-1", le1, "Acme GmbH", "4821")
	after := account(t1, "b-1", le1, "Acme GmbH", "7710")
	billing := auditctx.Message{V: auditctx.Version, TenantID: t1, Actor: "service:billing", ActorKind: auditctx.ActorService, OnBehalfOf: "ana",
		Reason: "IBAN corrected", TraceID: "trace-820", ChangeSetID: "cs-820", EventType: "vendor.changed", EventID: "evt-1"}
	update := changeRecord(t, "u", 820, 2010, before, after)
	parked := e.consume(t,
		ctxRecord(t, 812, 1000, person(1)),
		changeRecord(t, "c", 812, 1010, nil, before),
		ctxRecord(t, 820, 2000, billing),
		update,
		update, // redelivered
	)
	if len(parked.parked) != 0 {
		t.Fatalf("dead-lettered %v", parked.parked)
	}
	got, err := e.store.Query(reader(1, le1), "vendor_bank_account", "b-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("want 5 insert fields and 1 update field, got %d: %+v", len(got), got)
	}
	wantFields := []string{"vendorId", "accountHolder", "bankCountry", "currencyCode", "accountLast4"}
	for i, f := range wantFields {
		r := got[i]
		if r.Field != f || r.OldValue != "" || r.NewValue == "" || r.UserID != "ana" || r.ActorKind != domain.ActorPerson || r.OnBehalfOf != "" ||
			r.Reason != "vendor letter dated 3 Nov" || r.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || r.ChangeSetID != "cs-812" ||
			r.LegalEntityID != le1 || r.Source != "party" || r.Flag != "" || !r.OccurredAt.Equal(commit) {
			t.Errorf("insert field %d: %+v", i, r)
		}
	}
	u := got[5]
	if u.Field != "accountLast4" || u.OldValue != "4821" || u.NewValue != "7710" || u.UserID != "service:billing" || u.ActorKind != domain.ActorService ||
		u.OnBehalfOf != "ana" || u.Reason != "IBAN corrected" || u.TraceID != "trace-820" || u.EventType != "vendor.changed" || u.EventID != "evt-1" {
		t.Fatalf("update: %+v", u)
	}
	chain, err := e.store.VerifyChanges(reader(1, ""), 0, 0)
	if err != nil || !chain.Verified || chain.Entries != 6 || chain.Head != u.Hash {
		t.Fatalf("verify %+v %v", chain, err)
	}
}

// TestChangeWithoutContextIsRecordedAsSystemAndFlagged: a change whose transaction carried no depot.ctx message, or
// only one of another tenant, is recorded with actor kind system, user system:party and the unattributed flag.
func TestChangeWithoutContextIsRecordedAsSystemAndFlagged(t *testing.T) {
	e := setup(t)
	t1 := testkit.TenantID(1)
	parked := e.consume(t,
		changeRecord(t, "c", 900, 3010, nil, account(t1, "b-2", le1, "Bolt AG", "1111")),
		ctxRecord(t, 901, 4000, person(2)), // another tenant's message for the same source transaction
		changeRecord(t, "d", 901, 4010, account(t1, "b-2", le1, "Bolt AG", "1111"), nil),
	)
	if len(parked.parked) != 0 {
		t.Fatalf("dead-lettered %v", parked.parked)
	}
	got, err := e.store.Query(reader(1, le1), "vendor_bank_account", "b-2")
	if err != nil || len(got) != 10 {
		t.Fatalf("query %d %v", len(got), err)
	}
	for _, r := range got {
		if r.UserID != "system:party" || r.ActorKind != domain.ActorSystem || r.Flag != domain.FlagUnattributed || r.Reason != "" || r.Source != "party" {
			t.Fatalf("unattributed row %+v", r)
		}
	}
	if got[0].TraceID != "cdc:party:900" || got[9].TraceID != "cdc:party:901" || got[9].OldValue != "1111" || got[9].NewValue != "" {
		t.Fatalf("trace and delete values: %+v / %+v", got[0], got[9])
	}
	if chain, err := e.store.VerifyChanges(reader(1, ""), 0, 0); err != nil || !chain.Verified {
		t.Fatalf("verify %+v %v", chain, err)
	}
}

// A depot.ctx message whose actor does not have its kind's shape (a "person" that is a service id) is not a verified
// actor: the change is recorded as unattributed.
func TestMalformedContextActorIsNotTrusted(t *testing.T) {
	e := setup(t)
	forged := person(1)
	forged.Actor = "service:billing"
	e.consume(t, ctxRecord(t, 950, 5000, forged), changeRecord(t, "c", 950, 5010, nil, account(testkit.TenantID(1), "b-3", le1, "Cobalt", "2222")))
	got, err := e.store.Query(reader(1, le1), "vendor_bank_account", "b-3")
	if err != nil || len(got) == 0 || got[0].Flag != domain.FlagUnattributed {
		t.Fatalf("%+v %v", got, err)
	}
}

// TestForgedContextIsNotTrusted (review finding 1): any session on party's database can emit a well-formed depot.ctx
// message, so only one party signed with its service key, in the change's own transaction, names the actor. An
// unsigned message, one signed by an unknown key, and a valid message replayed in another transaction all leave the
// change recorded as system, flag unattributed-bad-signature, never as the person the message names.
func TestForgedContextIsNotTrusted(t *testing.T) {
	e := setup(t)
	t1 := testkit.TenantID(1)
	cfo := person(1)
	cfo.Actor, cfo.Reason = "cfo", "approved"
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	impostor, err := tenant.NewServiceSigner("party", otherPriv)
	if err != nil {
		t.Fatal(err)
	}
	impostor = impostor.WithKeyID("2026-11")
	billing, err := tenant.NewServiceSigner("billing", otherPriv)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		ctx  func(txID, lsn int64) eventing.Record
		flag string
	}{
		"unsigned": {func(txID, lsn int64) eventing.Record {
			return ctxRecordWith(t, txID, lsn, cfo, nil, strconv.FormatInt(epoch+txID, 10), commit)
		}, domain.FlagBadSignature},
		"wrong key": {func(txID, lsn int64) eventing.Record {
			return ctxRecordWith(t, txID, lsn, cfo, impostor, strconv.FormatInt(epoch+txID, 10), commit)
		}, domain.FlagBadSignature},
		"another service's key": {func(txID, lsn int64) eventing.Record {
			return ctxRecordWith(t, txID, lsn, cfo, billing, strconv.FormatInt(epoch+txID, 10), commit)
		}, domain.FlagBadSignature},
		"valid message replayed in another transaction": {func(txID, lsn int64) eventing.Record {
			return ctxRecordWith(t, txID, lsn, cfo, partyKey, strconv.FormatInt(epoch+txID-1, 10), commit)
		}, domain.FlagBadSignature},
		"message of an older transaction that reused the id": {func(txID, lsn int64) eventing.Record {
			return ctxRecordWith(t, txID, lsn, cfo, partyKey, strconv.FormatInt(epoch+txID, 10), commit.Add(-2*cdc.ContextWindow))
		}, domain.FlagUnattributed},
	} {
		t.Run(name, func(t *testing.T) {
			offset += 100
			txID, record := 7000+offset, "b-f"+strconv.FormatInt(offset, 10)
			parked := e.consume(t, tc.ctx(txID, 10*offset), changeRecord(t, "c", txID, 10*offset+5, nil, account(t1, record, le1, "Forged", "6666")))
			if len(parked.parked) != 0 {
				t.Fatalf("dead-lettered %v", parked.parked)
			}
			got, err := e.store.Query(reader(1, le1), "vendor_bank_account", record)
			if err != nil || len(got) == 0 {
				t.Fatalf("%+v %v", got, err)
			}
			for _, r := range got {
				if r.UserID != "system:party" || r.ActorKind != domain.ActorSystem || r.Flag != tc.flag || r.Reason != "" {
					t.Fatalf("forged context attributed: %+v", r)
				}
			}
		})
	}
	// The happy path with the same key registry: party's own signature in the change's transaction.
	e.consume(t, ctxRecord(t, 7900, 79000, cfo), changeRecord(t, "c", 7900, 79005, nil, account(t1, "b-ok", le1, "Signed", "7777")))
	got, err := e.store.Query(reader(1, le1), "vendor_bank_account", "b-ok")
	if err != nil || len(got) == 0 || got[0].UserID != "cfo" || got[0].Flag != "" || got[0].Reason != "approved" {
		t.Fatalf("signed context: %+v %v", got, err)
	}
}

// Without the source service's public key configured nothing verifies: changes are flagged, never attributed.
func TestNoKeysConfiguredAttributesNothing(t *testing.T) {
	e := setup(t)
	e.handler.Verifier = nil
	e.consume(t, ctxRecord(t, 8100, 81000, person(1)), changeRecord(t, "c", 8100, 81005, nil, account(testkit.TenantID(1), "b-n", le1, "Nokeys", "8888")))
	got, err := e.store.Query(reader(1, le1), "vendor_bank_account", "b-n")
	if err != nil || len(got) == 0 || got[0].Flag != domain.FlagBadSignature {
		t.Fatalf("%+v %v", got, err)
	}
}

// The consumer purges expired depot.ctx messages itself (at most hourly per tenant), so retention does not depend on
// the nightly job (review finding 3).
func TestConsumerPurgesExpiredContexts(t *testing.T) {
	e := setup(t)
	e.consume(t, ctxRecord(t, 8200, 82000, person(1)))
	count := func() int {
		var n int
		if err := e.admin.QueryRow(context.Background(), `select count(*) from cdc_tx_context where tx_id = 8200`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 1 {
		t.Fatal("the message is stored")
	}
	later := &cdc.Handler{Registry: e.handler.Registry, Sink: e.store, Verifier: trusted, Now: func() time.Time { return time.Now().Add(cdc.ContextRetention + time.Hour) }}
	e.handler = later
	e.consume(t, ctxRecord(t, 8201, 82010, person(1)))
	if count() != 0 {
		t.Fatal("a message older than the retention is purged by the consumer")
	}
}

// A message attaches to the changes after it in its transaction: one written after the change (a later position) does
// not, so auditctx.Emit goes before the first audited change.
func TestContextAfterTheChangeDoesNotAttach(t *testing.T) {
	e := setup(t)
	e.consume(t, ctxRecord(t, 960, 6020, person(1)), changeRecord(t, "c", 960, 6010, nil, account(testkit.TenantID(1), "b-4", le1, "Delta", "3333")))
	got, err := e.store.Query(reader(1, le1), "vendor_bank_account", "b-4")
	if err != nil || len(got) == 0 || got[0].Flag != domain.FlagUnattributed {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestDecoderSkipsAndDeadLetters(t *testing.T) {
	e := setup(t)
	snapshot := changeRecord(t, "r", 1, 1, nil, account(testkit.TenantID(1), "b-9", le1, "Snap", "9999"))
	other := rec(t, map[string]any{"prefix": "other"}, map[string]any{"op": "m", "source": src(1, 2, "", commit), "message": map[string]any{"prefix": "other", "content": "e30="}})
	unknown := rec(t, map[string]any{"id": "x"}, map[string]any{"op": "c", "after": map[string]any{"tenant_id": testkit.TenantID(1), "id": "x"},
		"source": map[string]any{"name": server, "schema": "public", "table": "vendor_role", "txId": 1, "lsn": 3}})
	tombstone := eventing.Record{Topic: topic, Offset: 99}
	// The connector routes its heartbeat records into the audit topic too (connect.yaml): the consumer skips them.
	heartbeat := rec(t, map[string]any{"serverName": server}, map[string]any{"ts_ms": commit.UnixMilli()})
	parked := e.consume(t, snapshot, other, unknown, tombstone, heartbeat)
	if len(parked.parked) != 1 {
		t.Fatalf("only the unregistered table is dead-lettered: %v", parked.parked)
	}
	if got, _ := e.store.Query(reader(1, le1), "vendor_bank_account", "b-9"); len(got) != 0 {
		t.Fatalf("a snapshot read is not a change: %+v", got)
	}
}

// TestTamperedRowFailsVerification: an attacker with the table owner's rights disables the append-only trigger and
// changes a recorded value, or removes a row; :verify names the first break.
func TestTamperedRowFailsVerification(t *testing.T) {
	e := setup(t)
	t1 := testkit.TenantID(1)
	e.consume(t, ctxRecord(t, 812, 1000, person(1)), changeRecord(t, "c", 812, 1010, nil, account(t1, "b-1", le1, "Acme GmbH", "4821")))
	rows, err := e.store.Query(reader(1, le1), "vendor_bank_account", "b-1")
	if err != nil || len(rows) != 5 {
		t.Fatalf("%d %v", len(rows), err)
	}
	ctx := reader(1, "")
	tamper := func(sql string, args ...any) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, e.admin, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `alter table audit_entry disable trigger audit_entry_no_mutation`); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, sql, args...); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `alter table audit_entry enable trigger audit_entry_no_mutation`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	var seq3 int64
	if err := e.admin.QueryRow(ctx, `select seq from audit_entry where id = $1`, rows[2].ID).Scan(&seq3); err != nil {
		t.Fatal(err)
	}
	tamper(`update audit_entry set new_value = 'XX' where id = $1`, rows[2].ID)
	chain, err := e.store.VerifyChanges(ctx, 0, 0)
	if err != nil || chain.Verified || chain.Breaks != 1 || chain.FirstBreak == nil ||
		*chain.FirstBreak != (domain.Break{Seq: seq3, ID: rows[2].ID, Kind: domain.BreakHash}) {
		t.Fatalf("tampered value: %+v %+v %v", chain, chain.FirstBreak, err)
	}
	tamper(`delete from audit_entry where id = $1`, rows[3].ID)
	if chain, err := e.store.VerifyChanges(ctx, 0, 0); err != nil || chain.Breaks != 2 || chain.FirstBreak.Seq != seq3 {
		t.Fatalf("removed row: %+v %v", chain, err)
	}
	if chain, err := e.store.VerifyChanges(ctx, seq3-1, 0); err != nil || chain.Verified || chain.Breaks != 2 || chain.FromSeq != seq3-1 {
		t.Fatalf("range over the changed and the removed row: %+v %v", chain, err)
	}
	// A range trusts its first row's prevHash: one that starts after the damage verifies.
	if chain, err := e.store.VerifyChanges(ctx, seq3+2, 0); err != nil || !chain.Verified || chain.Entries != 1 {
		t.Fatalf("range after the removed row: %+v %v", chain, err)
	}
}

func TestApprovalOfPendingAccountIsRecordedAsStatusChangeByApprover(t *testing.T) {
	e := setup(t)
	t1 := testkit.TenantID(1)
	pending := account(t1, "b-p", le1, "Acme GmbH", "4821")
	pending["status"] = "pending"
	active := account(t1, "b-p", le1, "Acme GmbH", "4821")
	active["status"] = "active"
	approver := auditctx.Message{V: auditctx.Version, TenantID: t1, Actor: "bea", ActorKind: auditctx.ActorPerson,
		Reason: "second approver", TraceID: "trace-830", ChangeSetID: "cs-830"}
	parked := e.consume(t,
		ctxRecord(t, 812, 1000, person(1)),
		changeRecord(t, "c", 812, 1010, nil, pending),
		ctxRecord(t, 830, 3000, approver),
		changeRecord(t, "u", 830, 3010, pending, active),
	)
	if len(parked.parked) != 0 {
		t.Fatalf("dead-lettered %v", parked.parked)
	}
	got, err := e.store.Query(reader(1, le1), "vendor_bank_account", "b-p")
	if err != nil {
		t.Fatal(err)
	}
	last := got[len(got)-1]
	if last.Field != "status" || last.OldValue != "pending" || last.NewValue != "active" || last.UserID != "bea" || last.Reason != "second approver" {
		t.Fatalf("approval: %+v", last)
	}
}
