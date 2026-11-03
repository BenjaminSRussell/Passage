package store

import (
	"slices"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

func TestDescriptionLanguages(t *testing.T) {
	cases := map[string][]string{
		"":      {"en"},
		"en":    {"en"},
		"de":    {"de", "en"},
		"de-DE": {"de-DE", "de", "en"},
		"en-US": {"en-US", "en"},
		"zz zz": {"en"},
	}
	for in, want := range cases {
		if got := domain.DescriptionLanguages(in); !slices.Equal(got, want) {
			t.Errorf("DescriptionLanguages(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPostgres_JournalNameControlsDefaultsAndRoundTrip(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	n, err := e.store.GetJournalName(e.ctx, "GJ")
	if err != nil || n.PostingRestriction != "none" || n.DescriptionLanguage != "legal_entity" {
		t.Fatalf("defaults = %+v (%v)", n, err)
	}
	putName(t, e, "GJ", JournalName{Active: true, PostingRestriction: "own_journals_only", DescriptionLanguage: "user"})
	n, err = e.store.GetJournalName(e.ctx, "GJ")
	if err != nil || n.PostingRestriction != "own_journals_only" || n.DescriptionLanguage != "user" {
		t.Fatalf("round trip = %+v (%v)", n, err)
	}
	if _, _, err := e.store.PutJournalName(e.ctx, "BAD", JournalName{Name: "Bad", LinesLimit: 10, Active: true, PostingRestriction: "by_user_group"}); err == nil {
		t.Fatal("an unknown posting restriction must be refused by the database check")
	}
}

// FR-11.9: a name restricted to own journals lets only the creator post.
func TestPostgres_PostingRestrictionOwnJournalsOnly(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "OWN", JournalName{Active: true, PostingRestriction: "own_journals_only"})
	putName(t, e, "ANY", JournalName{Active: true})

	own, err := e.store.CreateBatch(e.ctx, batchOf(e, "OWN", "10.00")) // created by test-user
	if err != nil {
		t.Fatal(err)
	}
	own, err = e.store.SubmitBatch(e.ctx, own.ID, 0, testToday)
	if err != nil {
		t.Fatal(err)
	}
	other := asUser(e, "someone-else")
	if _, err := postBatch(e, other, own, "OWN-1", true); !isProblem(err, 403, CodePostingRestricted) {
		t.Fatalf("another user must be refused with %s, got %v", CodePostingRestricted, err)
	}
	if got, _ := e.store.GetBatch(e.ctx, own.ID); got.Status != domain.BatchValidated || got.PostedEntryID != "" {
		t.Fatalf("a refused post must leave the batch unposted: %+v", got)
	}
	if _, err := postBatch(e, e.ctx, own, "OWN-2", true); err != nil {
		t.Fatalf("the creator posts: %v", err)
	}

	// Without the restriction anyone with the duty posts someone else's batch.
	free, _ := e.store.CreateBatch(e.ctx, batchOf(e, "ANY", "11.00"))
	free, _ = e.store.SubmitBatch(e.ctx, free.ID, 0, testToday)
	if _, err := postBatch(e, other, free, "ANY-1", true); err != nil {
		t.Fatalf("unrestricted name: %v", err)
	}
}

// FR-11.17: description_language = user looks the template up in the poster's
// language, falls back to the base language and then English; legal_entity
// ignores the poster's language.
func TestPostgres_DescriptionLanguageUser(t *testing.T) {
	e, _ := setup(t)
	tw := tenantWide(e)
	putTemplate(t, tw, DescriptionTemplate{Scope: ScopeTenant, SourceType: "manual_journal", Language: "en",
		Template: "Journal %1", FieldBindings: map[string]string{"1": "voucher"}})
	putTemplate(t, tw, DescriptionTemplate{Scope: ScopeTenant, SourceType: "manual_journal", Language: "de",
		Template: "Buchung %1", FieldBindings: map[string]string{"1": "voucher"}})
	putName(t, e, "USR", JournalName{Active: true, DescriptionLanguage: "user"})
	putName(t, e, "LE", JournalName{Active: true})

	post := func(name, lang, key string) Entry {
		t.Helper()
		in := batchOf(e, name, "5.00")
		in.Description = ""
		d := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
		in.DocumentDate = &d
		b, err := e.store.CreateBatch(e.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if b, err = e.store.SubmitBatch(e.ctx, b.ID, 0, testToday); err != nil {
			t.Fatal(err)
		}
		res, err := e.store.PostBatch(e.ctx, PostBatchInput{ID: b.ID, Key: key, Today: testToday, TraceID: testTrace,
			CanPostDirect: true, Language: lang})
		if err != nil {
			t.Fatal(err)
		}
		return res.Entries[0]
	}
	if en := post("USR", "de-DE", "L-1"); en.Description != "Buchung "+en.Voucher {
		t.Fatalf("de-DE falls back to de: %q", en.Description)
	}
	if fr := post("USR", "fr", "L-2"); fr.Description != "Journal "+fr.Voucher {
		t.Fatalf("an unknown language falls back to English: %q", fr.Description)
	}
	if none := post("USR", "", "L-3"); none.Description != "Journal "+none.Voucher {
		t.Fatalf("no language means English: %q", none.Description)
	}
	if le := post("LE", "de", "L-4"); le.Description != "Journal "+le.Voucher {
		t.Fatalf("legal_entity ignores the poster's language: %q", le.Description)
	}
}

// FR-11.8: allow_reinstate = false refuses the reversal of a reversing entry.
func TestPostgres_AllowReinstateFlag(t *testing.T) {
	e, _ := setup(t)
	// setup creates the ledger without setting AllowReinstate: the store must default it to true.
	if e.ledger.AllowReinstate == nil || !*e.ledger.AllowReinstate {
		t.Fatalf("a ledger created without the flag must allow reinstatement: %v", e.ledger.AllowReinstate)
	}
	orig, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "AR-1", "40.00"))
	if err != nil {
		t.Fatal(err)
	}
	rev, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: orig.Entries[0].ID, Key: "AR-R1", Hash: make([]byte, 32), TraceID: testTrace, Today: testToday})
	if err != nil {
		t.Fatal(err)
	}
	no := false
	cur, err := e.store.UpdateLedgerSettings(e.ctx, e.ledger.ID, e.ledger.Version, LedgerSettings{AllowReinstate: &no})
	if err != nil || cur.ReinstateAllowed() || cur.Version != e.ledger.Version+1 {
		t.Fatalf("turn off: %+v %v", cur, err)
	}
	_, err = e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: rev.Entries[0].ID, Key: "AR-R2", Hash: make([]byte, 32), TraceID: testTrace, Today: testToday})
	if codes := rejectionCodes(err); !slices.Equal(codes, []string{CodeReinstatementNotAllowed}) {
		t.Fatalf("reinstatement must be refused, got %v", err)
	}
	// An ordinary entry is still reversible, and a retry of the refused key leaves nothing behind.
	other, _ := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "AR-2", "7.00"))
	if _, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: other.Entries[0].ID, Key: "AR-R3", Hash: make([]byte, 32), TraceID: testTrace, Today: testToday}); err != nil {
		t.Fatalf("ordinary reversal: %v", err)
	}
	yes := true
	if cur, err = e.store.UpdateLedgerSettings(e.ctx, e.ledger.ID, cur.Version, LedgerSettings{AllowReinstate: &yes}); err != nil || !cur.ReinstateAllowed() {
		t.Fatalf("turn on: %+v %v", cur, err)
	}
	if _, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: rev.Entries[0].ID, Key: "AR-R2", Hash: make([]byte, 32), TraceID: testTrace, Today: testToday}); err != nil {
		t.Fatalf("reinstatement allowed again: %v", err)
	}
	// A ledger created with the flag off keeps it off.
	off, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "NOREIN", Name: "No reinstatement", ChartCode: "STD",
		Currency: "USD", FuturePostingDays: 366, AllowReinstate: &no})
	if err != nil || off.AllowReinstate == nil || *off.AllowReinstate {
		t.Fatalf("create with allowReinstate false: %+v %v", off.AllowReinstate, err)
	}
	if got, err := e.store.GetLedger(e.ctx, off.ID); err != nil || got.ReinstateAllowed() {
		t.Fatalf("read back: %+v %v", got.AllowReinstate, err)
	}
}
