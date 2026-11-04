package consumer

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

const (
	tenantID = "00000000-0000-0000-0000-000000000001"
	ledger   = "00000000-0000-0000-0000-0000000000a1"
	entry    = "00000000-0000-0000-0000-0000000000b1"
	document = "00000000-0000-0000-0000-0000000000c1"
	entity   = "00000000-0000-0000-0000-0000000000d1"
	original = "00000000-0000-0000-0000-0000000000e1"
)

type fakeApplier struct {
	got       []domain.PostedEntry
	principal tenant.Principal
	err       error
}

func (f *fakeApplier) ApplyEntry(ctx context.Context, e domain.PostedEntry) (int, error) {
	f.principal, _ = tenant.FromContext(ctx)
	f.got = append(f.got, e)
	if f.err != nil {
		return 0, f.err
	}
	return 1, nil
}

type fakeSource struct {
	lines     []domain.SourceLine
	err       error
	asked     [3]string
	principal tenant.Principal
}

func (f *fakeSource) EntryLines(ctx context.Context, l, d, e string) ([]domain.SourceLine, error) {
	f.asked = [3]string{l, d, e}
	f.principal, _ = tenant.FromContext(ctx)
	return f.lines, f.err
}

// actorCtx is the context the platform eventing consumer hands a handler: the
// consumer's service principal for the event's tenant, with no legal entity.
func actorCtx(t *testing.T) context.Context {
	t.Helper()
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: tenant.ServiceActor(Name),
		Duties: map[string]struct{}{dutyEntryLookup: {}}})
}

func twoLines() []domain.SourceLine {
	return []domain.SourceLine{
		{LineNumber: 1, Account: "1000", DebitCredit: "D", AmountAcct: decimal.RequireFromString("5")},
		{LineNumber: 2, Account: "2000", DebitCredit: "C", AmountAcct: decimal.RequireFromString("5")},
	}
}

// event is a journal.posted as the ledger writes it (services/ledger/internal/events)
// plus the legalEntityId the wave 11 handoff adds.
func event(t *testing.T, mutate func(map[string]any)) events.Event {
	t.Helper()
	data := map[string]any{"entryId": entry, "ledgerId": ledger, "legalEntityId": entity, "documentId": document, "voucher": "V1",
		"postingSequence": 3, "postingDate": "2026-03-31", "periodKey": "2026-03", "lineCount": 2,
		"totals": map[string]any{"currency": "USD", "debit": "5.00", "credit": "5.00"},
		"source": map[string]any{"system": "close", "type": "year_end_closing", "id": "Y1"}, "traceId": "0123456789abcdef0123456789abcdef"}
	if mutate != nil {
		mutate(data)
	}
	evt, err := events.New(JournalPostedType, LedgerSource, entry, tenantID, data)
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func TestHandle_AppliesEntryScopedToItsLegalEntity(t *testing.T) {
	ap, src := &fakeApplier{}, &fakeSource{lines: twoLines()}
	if err := HandleJournalPosted(actorCtx(t), ap, src, event(t, nil)); err != nil {
		t.Fatal(err)
	}
	if src.asked != [3]string{ledger, document, entry} {
		t.Fatalf("asked %v", src.asked)
	}
	if len(ap.got) != 1 {
		t.Fatalf("applied %d", len(ap.got))
	}
	g := ap.got[0]
	if g.PostingSequence != 3 || g.PeriodKey != "2026-03" || g.SourceSystem != "close" || g.SourceType != "year_end_closing" ||
		g.LegalEntityID != entity || len(g.Lines) != 2 || g.ReversalOfEntryID != "" || !g.Closing() {
		t.Fatalf("entry %+v", g)
	}
	// Both the ledger call and the write run as the service principal, scoped to
	// the entry's legal entity, never with the all-scope duty.
	for name, p := range map[string]tenant.Principal{"source": src.principal, "applier": ap.principal} {
		if p.UserID != tenant.ServiceActor(Name) || p.LegalEntityID != entity || p.HasDuty(tenant.DutyAllScope) {
			t.Fatalf("%s principal %+v", name, p)
		}
	}
}

func TestHandle_EntityFromTheCloudEventExtension(t *testing.T) {
	evt := event(t, func(d map[string]any) { delete(d, "legalEntityId") })
	evt.LegalEntityID = entity
	ap := &fakeApplier{}
	if err := HandleJournalPosted(actorCtx(t), ap, &fakeSource{lines: twoLines()}, evt); err != nil || ap.got[0].LegalEntityID != entity {
		t.Fatalf("err %v applied %+v", err, ap.got)
	}
}

func TestHandle_ReversalCarriesTheReversedEntry(t *testing.T) {
	lines := twoLines()
	for i := range lines {
		lines[i].ReversalOfEntryID = original
	}
	ap := &fakeApplier{}
	evt := event(t, func(d map[string]any) { d["source"] = map[string]any{"system": "ledger", "type": "reversal"} })
	if err := HandleJournalPosted(actorCtx(t), ap, &fakeSource{lines: lines}, evt); err != nil {
		t.Fatal(err)
	}
	if ap.got[0].ReversalOfEntryID != original || ap.got[0].Closing() {
		t.Fatalf("entry %+v", ap.got[0])
	}
	lines[1].ReversalOfEntryID = ""
	if err := HandleJournalPosted(actorCtx(t), &fakeApplier{}, &fakeSource{lines: lines}, evt); !errors.Is(err, ErrInvalidEvent) || !eventing.IsPermanent(err) {
		t.Fatalf("mixed reversal lines: %v", err)
	}
}

// TestHandle_InvalidEventsArePermanent: each is dead-lettered at once, with no
// ledger call and nothing applied.
func TestHandle_InvalidEventsArePermanent(t *testing.T) {
	cases := map[string]func(map[string]any){
		"entry id":     func(d map[string]any) { d["entryId"] = "nope" },
		"ledger id":    func(d map[string]any) { d["ledgerId"] = "" },
		"document id":  func(d map[string]any) { delete(d, "documentId") },
		"no entity":    func(d map[string]any) { delete(d, "legalEntityId") },
		"bad entity":   func(d map[string]any) { d["legalEntityId"] = "LE-1" },
		"sequence":     func(d map[string]any) { d["postingSequence"] = 0 },
		"line count":   func(d map[string]any) { d["lineCount"] = 0 },
		"period":       func(d map[string]any) { d["periodKey"] = "2026-3" },
		"total":        func(d map[string]any) { d["totals"] = map[string]any{"debit": "abc"} },
		"wrong shaped": func(d map[string]any) { d["postingSequence"] = "three" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ap, src := &fakeApplier{}, &fakeSource{lines: twoLines()}
			err := HandleJournalPosted(actorCtx(t), ap, src, event(t, mutate))
			if !errors.Is(err, ErrInvalidEvent) || !eventing.IsPermanent(err) || len(ap.got) != 0 || src.asked[0] != "" {
				t.Fatalf("err %v applied %d asked %v", err, len(ap.got), src.asked)
			}
		})
	}
	others := map[string]func(*events.Event){
		"type":   func(e *events.Event) { e.Type = "dev.passage.ledger.journal.reversed.v1" },
		"source": func(e *events.Event) { e.Source = "/passage/payables/payables" },
		"tenant": func(e *events.Event) { e.TenantID = "00000000-0000-0000-0000-000000000002" },
	}
	for name, mutate := range others {
		evt := event(t, nil)
		mutate(&evt)
		if err := HandleJournalPosted(actorCtx(t), &fakeApplier{}, &fakeSource{}, evt); !errors.Is(err, ErrInvalidEvent) || !eventing.IsPermanent(err) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestHandle_RefusesAnyOtherPrincipal(t *testing.T) {
	user := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "u1",
		Duties: map[string]struct{}{dutyEntryLookup: {}, tenant.DutyAllScope: {}}})
	for name, ctx := range map[string]context.Context{"none": context.Background(), "user": user} {
		ap := &fakeApplier{}
		if err := HandleJournalPosted(ctx, ap, &fakeSource{lines: twoLines()}, event(t, nil)); !eventing.IsPermanent(err) || len(ap.got) != 0 {
			t.Fatalf("%s: err %v applied %d", name, err, len(ap.got))
		}
	}
}

func TestHandle_RetriableAndPermanentFailures(t *testing.T) {
	ap := &fakeApplier{}
	err := HandleJournalPosted(actorCtx(t), ap, &fakeSource{lines: twoLines()[:1]}, event(t, nil))
	if !errors.Is(err, ErrLinesNotReady) || eventing.IsPermanent(err) || len(ap.got) != 0 {
		t.Fatalf("short lines: %v", err)
	}
	boom := errors.New("ledger down")
	err = HandleJournalPosted(actorCtx(t), ap, &fakeSource{err: boom}, event(t, nil))
	if !errors.Is(err, boom) || eventing.IsPermanent(err) {
		t.Fatalf("source error: %v", err)
	}
	db := errors.New("connection reset")
	ap.err = db
	if err := HandleJournalPosted(actorCtx(t), ap, &fakeSource{lines: twoLines()}, event(t, nil)); !errors.Is(err, db) || eventing.IsPermanent(err) {
		t.Fatalf("database error: %v", err)
	}
	for _, refusal := range []error{httpx.NewProblem(422, "REPORT-0032", "other entity"), &domain.Error{Code: domain.CodeLineInvalid}} {
		ap.err = refusal
		if err := HandleJournalPosted(actorCtx(t), ap, &fakeSource{lines: twoLines()}, event(t, nil)); !errors.Is(err, refusal) || !eventing.IsPermanent(err) {
			t.Fatalf("refusal %v: %v", refusal, err)
		}
	}
}
