package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const auditLedger = "0190f5a2-7c1e-7d00-8000-000000000001"

func TestAuditFilter_Validate(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		f       AuditFilter
		wantErr string
	}{
		{"period alone narrows the range", AuditFilter{Period: "2026-03"}, ""},
		{"opening period", AuditFilter{Period: "2026-00", LedgerID: auditLedger}, ""},
		{"adjustment period 13", AuditFilter{Period: "2026-13"}, ""},
		{"window of 366 days", AuditFilter{From: t0, To: t0.Add(AuditMaxWindow)}, ""},
		{"ledger kind and actor with a window", AuditFilter{LedgerID: auditLedger, Kind: AuditReopenApproved, ActorID: "u1", From: t0, To: t0.Add(time.Hour)}, ""},
		{"no range at all", AuditFilter{LedgerID: auditLedger}, "name a period"},
		{"from without to", AuditFilter{From: t0}, "name a period"},
		{"window over 366 days", AuditFilter{From: t0, To: t0.Add(AuditMaxWindow + time.Second)}, "366 days"},
		{"to before from", AuditFilter{Period: "2026-03", From: t0, To: t0.Add(-time.Hour)}, "after from"},
		{"to equal to from", AuditFilter{From: t0, To: t0}, "after from"},
		{"ledger not a UUID", AuditFilter{Period: "2026-03", LedgerID: "L1"}, "not a UUID"},
		{"period as a calendar month without padding", AuditFilter{Period: "2026-3"}, "fiscal year"},
		{"period as a date", AuditFilter{Period: "2026-03-31"}, "fiscal year"},
		{"unknown kind", AuditFilter{Period: "2026-03", Kind: "deleted"}, "unknown kind"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.f.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrAuditFilter) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want ErrAuditFilter mentioning %q", err, tc.wantErr)
			}
		})
	}
}

func TestAuditEntry_Validate(t *testing.T) {
	ok := AuditEntry{OccurredAt: time.Now(), Kind: AuditPosted, ActorID: "u1", ObjectRef: "journal:J-1", LedgerID: auditLedger, Period: "2026-03"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid entry refused: %v", err)
	}
	for name, mutate := range map[string]func(*AuditEntry){
		"unknown kind":     func(e *AuditEntry) { e.Kind = "edited" },
		"no time":          func(e *AuditEntry) { e.OccurredAt = time.Time{} },
		"blank actor":      func(e *AuditEntry) { e.ActorID = "  " },
		"no object":        func(e *AuditEntry) { e.ObjectRef = "" },
		"ledger not uuid":  func(e *AuditEntry) { e.LedgerID = "GL" },
		"period not label": func(e *AuditEntry) { e.Period = "March" },
		"long trace id":    func(e *AuditEntry) { e.TraceID = strings.Repeat("a", 129) },
	} {
		e := ok
		mutate(&e)
		var de *Error
		if err := e.Validate(); !errors.As(err, &de) || de.Code != CodeAuditInvalid {
			t.Errorf("%s: Validate() = %v, want %s", name, err, CodeAuditInvalid)
		}
	}
}

func TestValidAuditKind_CoversThePRDKinds(t *testing.T) {
	// PRD §2.15.4 AuditTrailEntry.kind, in the PRD's order.
	prd := "posted, replayed, reversed, batch_submitted, batch_approved, batch_rejected, period_changed, reopen_approved, rate_override, tax_fallback, allocation_run, year_end_run, rebuild"
	if got := strings.Join(AuditLedgerKinds, ", "); got != prd {
		t.Fatalf("ledger kinds = %s\nwant %s", got, prd)
	}
	for _, k := range append(append([]string{}, AuditLedgerKinds...), AuditReportingKinds...) {
		if !ValidAuditKind(k) {
			t.Errorf("%s should be valid", k)
		}
	}
	for _, k := range []string{"", "POSTED", "statement.generated"} {
		if ValidAuditKind(k) {
			t.Errorf("%q should be invalid", k)
		}
	}
}

func TestAuditCursor_RoundTripAndRejectsForgeries(t *testing.T) {
	c := AuditCursor{OccurredAt: time.Date(2026, 3, 31, 9, 0, 0, 123456000, time.UTC), ID: "0190f5a2-7c1e-7d00-8000-0000000000aa"}
	got, err := DecodeAuditCursor(c.Encode())
	if err != nil || !got.OccurredAt.Equal(c.OccurredAt) || got.ID != c.ID {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "!!", "bm9waXBl", c.Encode()[:10]} {
		if _, err := DecodeAuditCursor(bad); !errors.Is(err, ErrBadCursor) {
			t.Errorf("DecodeAuditCursor(%q) = %v, want ErrBadCursor", bad, err)
		}
	}
}

func TestEncodeAuditCSV_StableExactAndSafe(t *testing.T) {
	at := time.Date(2026, 3, 31, 9, 0, 0, 500000000, time.FixedZone("EST", -5*3600))
	rows := []AuditEntry{
		{OccurredAt: at, Kind: AuditReopenApproved, LedgerID: auditLedger, Period: "2026-03", ActorID: "u-1", ObjectRef: "period:2026-03",
			Details: map[string]any{"approvers": []any{"u-2", "u-3"}, "amount": json.Number("1234567890123.450000")}, TraceID: "4bf92f3577b34da6"},
		{OccurredAt: at.Add(time.Minute), Kind: AuditDefinitionPublished, ActorID: "=HYPERLINK(\"x\")", ObjectRef: "definition:BS-STD@2"},
	}
	body, err := EncodeAuditCSV(rows)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header plus 2 rows, got %d:\n%s", len(lines), body)
	}
	if lines[0] != "occurred_at,kind,ledger_id,period,actor_id,object_ref,details,trace_id" {
		t.Fatalf("header = %q", lines[0])
	}
	// UTC time, details as JSON with sorted keys and the amount digit for digit.
	want := `2026-03-31T14:00:00.5Z,reopen_approved,` + auditLedger + `,2026-03,u-1,period:2026-03,"{""amount"":1234567890123.450000,""approvers"":[""u-2"",""u-3""]}",4bf92f3577b34da6`
	if lines[1] != want {
		t.Fatalf("row 1 = %s\nwant    %s", lines[1], want)
	}
	if !strings.Contains(lines[2], `'=HYPERLINK`) || !strings.HasSuffix(lines[2], `,{},`) {
		t.Fatalf("row 2 must neutralize the formula and write empty details as {}: %s", lines[2])
	}
	again, _ := EncodeAuditCSV(rows)
	if AuditDigest(again) != AuditDigest(body) {
		t.Fatal("equal entries must give an equal digest")
	}
}

func TestAuditDigest(t *testing.T) {
	// SHA-256 of the empty string.
	if got := AuditDigest(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("AuditDigest(nil) = %s", got)
	}
	if AuditDigest([]byte("a")) == AuditDigest([]byte("b")) {
		t.Fatal("different bodies must hash differently")
	}
}
