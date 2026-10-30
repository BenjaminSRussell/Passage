package store

import (
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	svcevents "github.com/BenjaminSRussell/passage/services/fieldservice/internal/events"
)

func problemStatus(err error) int {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Status
	}
	return 0
}

// warrantyOrder posts a work order on a case whose service object's warranty covers
// parts and labor: a 180.00 part and 1.5 labor hours, both WARRANTY (AC-45.13).
func warrantyOrder(t *testing.T, f fixture, serial string) (WorkOrder, ServiceObject) {
	t.Helper()
	obj, _, err := f.s.CreateServiceObject(f.ctx, ServiceObject{ItemID: ids.New(), Serial: serial, CustomerPartyID: f.customer,
		LegalEntityID: f.le, ShippedOn: ptr("2026-01-15"),
		Warranty: domain.WarrantyTerm{StartTrigger: domain.StartShip, Months: 120, PartsCovered: true, LaborCovered: true}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := f.s.OpenCase(f.ctx, OpenCaseInput{CustomerPartyID: f.customer, LegalEntityID: f.le, ServiceObjectID: &obj.ID,
		Channel: "PHONE", Priority: 2, OpenedAt: time.Now()}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	wo := f.completedOrder(t, WorkOrder{CaseID: &c.ID, CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 90, Currency: "USD", Lines: []domain.WorkLine{
			{ID: ids.New(), Kind: domain.KindPart, Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(250), UnitCost: decimal.RequireFromString("180.00")},
			{ID: ids.New(), Kind: domain.KindLabor, Qty: decimal.RequireFromString("1.5"), UnitPrice: decimal.NewFromInt(100)},
		}})
	posted, _, err := f.s.PostWorkOrder(f.ctx, wo.ID, wo.Version, "post-"+serial)
	if err != nil {
		t.Fatal(err)
	}
	return posted, obj
}

// AC-45.13 against Postgres as passage_app: a claim from the posted WARRANTY lines
// claims 180.00 + 1.5 × 60.00 = 270.00; the vendor accepts 250.00; the matched credit
// note settles the claim with recovered 250.00 and writes the settled event for the
// GL warranty recovery. Each step checks its BR-45.12 refusals and replays.
func TestStore_SupplierClaimSettlesAC45_13(t *testing.T) {
	f := newFixture(t)
	wo, obj := warrantyOrder(t, f, "PUMP-13")
	vendor := ids.New()
	at := time.Now().UTC()
	in := ClaimInput{VendorPartyID: vendor, SourceKind: domain.ClaimFromServiceWO, SourceIDs: []string{wo.ID},
		LaborRate: decimal.RequireFromString("60.00"), FailureCode: "BEARING", WindowDays: 30, ReturnRequired: true, At: at}
	claim, replayed, err := f.s.CreateClaim(f.ctx, in, "clm-1", "hash-clm-1")
	if err != nil || replayed {
		t.Fatalf("create = %v replayed=%v", err, replayed)
	}
	if claim.Status != domain.ClaimDraft || !claim.ClaimedTotal.Equal(decimal.NewFromInt(270)) || claim.LegalEntityID != f.le ||
		claim.Currency != "USD" || len(claim.Lines) != 1 || claim.Number == "" || claim.Version != 1 {
		t.Fatalf("claim = %+v", claim)
	}
	l := claim.Lines[0]
	if l.SourceID != wo.ID || l.ItemID != obj.ItemID || l.Serial != "PUMP-13" || l.FailureCode != "BEARING" ||
		!l.PartAmount.Equal(decimal.NewFromInt(180)) || !l.LaborHours.Equal(decimal.RequireFromString("1.5")) {
		t.Fatalf("line = %+v", l)
	}
	if want := wo.CompletedAt.UTC().AddDate(0, 0, 30).Format(dateLayout); claim.WindowEndsOn != want {
		t.Fatalf("window ends %s, want %s", claim.WindowEndsOn, want)
	}
	if again, replayed, err := f.s.CreateClaim(f.ctx, in, "clm-1", "hash-clm-1"); err != nil || !replayed || again.ID != claim.ID {
		t.Fatalf("create replay = %v replayed=%v", err, replayed)
	}
	// The work order is claimed once.
	if _, _, err := f.s.CreateClaim(f.ctx, in, "clm-2", "hash-clm-2"); problemCode(err) != CodeClaimClaimed || problemStatus(err) != http.StatusConflict {
		t.Fatalf("second claim on the work order = %v", err)
	}

	// The vendor requires the failed part back before submission (SVC-SWC-002).
	if _, _, err := f.s.SubmitClaim(f.ctx, ClaimMove{ID: claim.ID, Version: 1, At: at}, "sub-0", "hash-sub-0"); problemCode(err) != domain.CodeClaimReturn {
		t.Fatalf("submit without return = %v", err)
	}
	sub, _, err := f.s.SubmitClaim(f.ctx, ClaimMove{ID: claim.ID, Version: 1, ReturnOrderID: "RMA-77", VendorReference: "VR-1042", At: at}, "sub-1", "hash-sub-1")
	if err != nil || sub.Status != domain.ClaimSubmitted || sub.SubmittedOn == nil || sub.ReturnOrderID != "RMA-77" || sub.Version != 2 {
		t.Fatalf("submit = %+v, %v", sub, err)
	}
	if again, replayed, err := f.s.SubmitClaim(f.ctx, ClaimMove{ID: claim.ID, Version: 1, ReturnOrderID: "RMA-77", VendorReference: "VR-1042", At: at},
		"sub-1", "hash-sub-1"); err != nil || !replayed || again.Version != 2 {
		t.Fatalf("submit replay = %v replayed=%v", err, replayed)
	}
	if _, _, err := f.s.DecideClaim(f.ctx, ClaimMove{ID: claim.ID, Version: 1, Decision: domain.DecisionAccepted, At: at}, "dec-stale", "h"); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale decision = %v", err)
	}
	if _, _, err := f.s.DecideClaim(f.ctx, ClaimMove{ID: claim.ID, Version: 2, Decision: domain.DecisionPartial,
		Accepted: []decimal.Decimal{decimal.NewFromInt(300)}, At: at}, "dec-0", "hash-dec-0"); problemCode(err) != domain.CodeClaimOverAccept {
		t.Fatalf("accepting more than claimed = %v", err)
	}
	dec, _, err := f.s.DecideClaim(f.ctx, ClaimMove{ID: claim.ID, Version: 2, Decision: domain.DecisionPartial,
		Accepted: []decimal.Decimal{decimal.RequireFromString("250.00")}, At: at}, "dec-1", "hash-dec-1")
	if err != nil || dec.Status != domain.ClaimPartial || !dec.AcceptedTotal.Equal(decimal.NewFromInt(250)) || dec.DecidedAt == nil {
		t.Fatalf("decision = %+v, %v", dec, err)
	}

	if _, _, err := f.s.SettleClaim(f.ctx, ClaimMove{ID: claim.ID, Version: dec.Version, CreditNoteID: "CN-9", VendorReference: "VR-OTHER",
		CreditAmount: decimal.NewFromInt(250), CreditCurrency: "USD", At: at}, "set-0", "hash-set-0"); problemCode(err) != domain.CodeClaimReference {
		t.Fatalf("settle against another reference = %v", err)
	}
	if _, _, err := f.s.SettleClaim(f.ctx, ClaimMove{ID: claim.ID, Version: dec.Version, CreditNoteID: "CN-9", VendorReference: "VR-1042",
		CreditAmount: decimal.NewFromInt(240), CreditCurrency: "USD", At: at}, "set-1", "hash-set-1"); problemCode(err) != domain.CodeClaimCredit {
		t.Fatalf("settle with a short credit = %v", err)
	}
	settled, _, err := f.s.SettleClaim(f.ctx, ClaimMove{ID: claim.ID, Version: dec.Version, CreditNoteID: "CN-9", VendorReference: "VR-1042",
		CreditAmount: decimal.RequireFromString("250.00"), CreditCurrency: "USD", At: at}, "set-2", "hash-set-2")
	if err != nil || settled.Status != domain.ClaimSettled || !settled.RecoveredAmount.Equal(decimal.NewFromInt(250)) || settled.CreditNoteID != "CN-9" {
		t.Fatalf("settle = %+v, %v", settled, err)
	}
	if got := outboxTypes(t, f.s, f.ctx, claim.ID); !slices.Equal(got, []string{svcevents.SupplierClaimSettled}) {
		t.Fatalf("outbox = %v", got)
	}
	var recovered string
	if err := dbx.WithTenantTx(f.ctx, f.s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `select payload->'data'->>'recoveredAmount' from outbox where aggregate_id = $1`, claim.ID).Scan(&recovered)
	}); err != nil {
		t.Fatal(err)
	}
	if !decimal.RequireFromString(recovered).Equal(decimal.NewFromInt(250)) {
		t.Fatalf("event recoveredAmount = %s", recovered)
	}
	closed, _, err := f.s.CloseClaim(f.ctx, ClaimMove{ID: claim.ID, Version: settled.Version, At: at}, "cls-1", "hash-cls-1")
	if err != nil || closed.Status != domain.ClaimClosed || closed.ClosedAt == nil {
		t.Fatalf("close = %+v, %v", closed, err)
	}
	page, err := f.s.ListClaims(f.ctx, ClaimFilter{VendorPartyID: vendor, Status: domain.ClaimClosed, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != claim.ID {
		t.Fatalf("list = %+v, %v", page, err)
	}
}

// BR-45.12 refusals at creation and the claim window: a work order that is not
// posted or has no warranty line is no source (SVC-SWC-008); a claim submitted after
// the window is refused (SVC-SWC-001); a rejected claim closes without recovery.
func TestStore_SupplierClaimSourcesWindowAndRejection(t *testing.T) {
	f := newFixture(t)
	vendor := ids.New()
	at := time.Now().UTC()
	billable := f.completedOrder(t, WorkOrder{CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 60, Currency: "USD", Lines: laborHours(1)})
	if _, _, err := f.s.CreateClaim(f.ctx, ClaimInput{VendorPartyID: vendor, SourceKind: domain.ClaimFromServiceWO,
		SourceIDs: []string{billable.ID}, WindowDays: 30, At: at}, "c-1", "h-1"); problemCode(err) != CodeClaimSource {
		t.Fatalf("unposted source = %v", err)
	}
	posted, _, err := f.s.PostWorkOrder(f.ctx, billable.ID, billable.Version, "post-billable")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.CreateClaim(f.ctx, ClaimInput{VendorPartyID: vendor, SourceKind: domain.ClaimFromServiceWO,
		SourceIDs: []string{posted.ID}, WindowDays: 30, At: at}, "c-2", "h-2"); problemCode(err) != CodeClaimSource {
		t.Fatalf("source without warranty lines = %v", err)
	}
	if _, _, err := f.s.CreateClaim(f.ctx, ClaimInput{VendorPartyID: vendor, SourceKind: domain.ClaimFromServiceWO,
		SourceIDs: []string{ids.New()}, WindowDays: 30, At: at}, "c-3", "h-3"); problemCode(err) != CodeClaimSource {
		t.Fatalf("unknown source = %v", err)
	}

	// An EAM work order claim (the wave-44 claimable event stand-in) completed 40 days ago.
	completed := at.AddDate(0, 0, -40).Format(dateLayout)
	eam, _, err := f.s.CreateClaim(f.ctx, ClaimInput{VendorPartyID: vendor, SourceKind: domain.ClaimFromEAMWO, SourceIDs: []string{ids.New()},
		LegalEntityID: f.le, Currency: "EUR", CompletedOn: completed, WindowDays: 30, At: at,
		Lines: []domain.ClaimLine{{ItemID: ids.New(), Serial: "MTR-1", PartAmount: decimal.RequireFromString("99.999"), LaborHours: decimal.NewFromInt(2)}}},
		"c-4", "h-4")
	if err != nil || !eam.ClaimedTotal.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("EAM claim = %+v, %v (labor rate 0 claims the part only, rounded to cents)", eam, err)
	}
	if _, _, err := f.s.SubmitClaim(f.ctx, ClaimMove{ID: eam.ID, Version: 1, At: at}, "s-1", "hs-1"); problemCode(err) != domain.CodeClaimWindow {
		t.Fatalf("late submission = %v", err)
	}

	inside, _, err := f.s.CreateClaim(f.ctx, ClaimInput{VendorPartyID: vendor, SourceKind: domain.ClaimFromEAMWO, SourceIDs: []string{ids.New()},
		LegalEntityID: f.le, Currency: "EUR", CompletedOn: at.Format(dateLayout), WindowDays: 30, At: at,
		Lines: []domain.ClaimLine{{ItemID: ids.New(), PartAmount: decimal.NewFromInt(50)}}}, "c-5", "h-5")
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := f.s.SubmitClaim(f.ctx, ClaimMove{ID: inside.ID, Version: 1, At: at}, "s-2", "hs-2")
	if err != nil {
		t.Fatal(err)
	}
	rej, _, err := f.s.DecideClaim(f.ctx, ClaimMove{ID: inside.ID, Version: sub.Version, Decision: domain.DecisionRejected, Reason: "no fault found", At: at}, "d-1", "hd-1")
	if err != nil || rej.Status != domain.ClaimRejected || !rej.AcceptedTotal.IsZero() || rej.DecisionReason != "no fault found" {
		t.Fatalf("reject = %+v, %v", rej, err)
	}
	if _, _, err := f.s.SettleClaim(f.ctx, ClaimMove{ID: inside.ID, Version: rej.Version, CreditNoteID: "CN", VendorReference: "x",
		CreditCurrency: "EUR", At: at}, "st", "hst"); problemCode(err) != domain.CodeClaimState {
		t.Fatalf("settling a rejected claim = %v", err)
	}
	closed, _, err := f.s.CloseClaim(f.ctx, ClaimMove{ID: inside.ID, Version: rej.Version, At: at}, "cl", "hcl")
	if err != nil || closed.Status != domain.ClaimClosed || !closed.RecoveredAmount.IsZero() {
		t.Fatalf("close rejected = %+v, %v", closed, err)
	}
	if got := outboxTypes(t, f.s, f.ctx, inside.ID); len(got) != 0 {
		t.Fatalf("a rejected claim wrote %v", got)
	}
	if _, err := f.s.GetClaim(f.ctx, ids.New()); problemCode(err) != CodeClaimUnknown {
		t.Fatalf("unknown claim = %v", err)
	}
}

// AC-45.14 and BR-45.13 against Postgres as passage_app: an email to the mailbox
// without a token opens a case in the default category with one acknowledgment;
// "Re: [CS-n] pump noise" is logged on the open case without a new case or an
// acknowledgment; a reply by In-Reply-To attaches as well; a redelivery creates
// nothing; mail to a CLOSED case opens a new case linked by reopened_from; an
// auto-reply is not acknowledged; a restricted category's case is not matched for a
// caller without its duty.
func TestStore_EmailIntakeThreadsAC45_14(t *testing.T) {
	s, _ := newSliceStore(t, 51)
	hub := ids.New()
	ctx := userCtx(51, hub)
	at := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	cat, _, err := s.CreateCategory(ctx, Category{Code: "CUST-INQ", Name: "Inquiries", CategoryType: domain.CategoryInternal, DefaultPriority: 3,
		ReopenWindowDays: 14, ResolutionCodes: []string{"ANSWERED"}}, "cat-inq", "hash-cat-inq")
	if err != nil {
		t.Fatal(err)
	}
	le := ids.New()
	mb, created, err := s.UpsertMailbox(ctx, Mailbox{Address: "service@example.test", LegalEntityID: le, CategoryID: cat.ID}, at)
	if err != nil || !created || mb.CategoryCode != "CUST-INQ" {
		t.Fatalf("mailbox = %+v created=%v err=%v", mb, created, err)
	}
	if _, created, err := s.UpsertMailbox(ctx, Mailbox{Address: "service@example.test", LegalEntityID: le, CategoryID: cat.ID}, at); err != nil || created {
		t.Fatalf("mailbox update created=%v err=%v", created, err)
	}
	coll, _, err := s.CreateCategory(ctx, Category{Code: "COLL", Name: "Collections", CategoryType: domain.CategoryCollections, DefaultPriority: 3, ResolutionCodes: []string{}}, "cat-coll", "h-coll")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertMailbox(ctx, Mailbox{Address: "ar@example.test", LegalEntityID: le, CategoryID: coll.ID}, at); problemCode(err) != domain.CodeMailboxCat {
		t.Fatalf("collections mailbox = %v", err)
	}

	ingest := func(in InboundInput) (InboundMessage, bool) {
		t.Helper()
		at = at.Add(time.Minute)
		in.Mailbox, in.FromAddress, in.ReceivedAt, in.At = mb.Address, "pat@customer.test", at, at
		m, replayed, err := s.IngestInbound(ctx, in)
		if err != nil {
			t.Fatalf("ingest %s: %v", in.MessageID, err)
		}
		return m, replayed
	}
	first, _ := ingest(InboundInput{MessageID: "<m1@customer.test>", Subject: "pump noise", Body: "It rattles."})
	if first.Route != domain.RouteNewCase || !first.Acknowledge || first.CaseNumber == "" {
		t.Fatalf("first = %+v", first)
	}
	c, err := s.GetGenericCase(ctx, first.CaseID)
	if err != nil || c.CategoryID != cat.ID || c.Subject != "pump noise" || c.Description != "It rattles." || c.LegalEntityID != le {
		t.Fatalf("case = %+v, %v", c, err)
	}

	// AC-45.14: the token in the stored form or without padding finds the open case.
	short := "CS-" + trimZeros(c.Number[3:])
	reply, _ := ingest(InboundInput{MessageID: "<m2@customer.test>", Subject: "Re: [" + short + "] pump noise"})
	if reply.Route != domain.RouteAttached || reply.CaseID != c.ID || reply.Acknowledge {
		t.Fatalf("token reply = %+v", reply)
	}
	chained, _ := ingest(InboundInput{MessageID: "<m3@customer.test>", InReplyTo: "<m2@customer.test>", Subject: "more detail"})
	if chained.Route != domain.RouteAttached || chained.CaseID != c.ID {
		t.Fatalf("In-Reply-To reply = %+v", chained)
	}
	again, replayed := ingest(InboundInput{MessageID: "<m2@customer.test>", Subject: "Re: [" + short + "] pump noise"})
	if !replayed || again.ID != reply.ID {
		t.Fatalf("redelivery = %+v replayed=%v", again, replayed)
	}
	c, err = s.GetGenericCase(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	emails := 0
	for _, e := range c.Log {
		if e.Kind == logEmail {
			emails++
		}
	}
	if emails != 2 || c.Version != 3 {
		t.Fatalf("case log has %d email lines at version %d, want 2 at 3: %+v", emails, c.Version, c.Log)
	}
	page, err := s.ListGenericCases(ctx, GenericFilter{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("cases after the replies = %d, %v", len(page.Items), err)
	}

	// Mail to the case once CLOSED opens a new case linked by reopenedFrom.
	resolved, _, err := s.TransitionGenericCase(ctx, CaseMove{ID: c.ID, Version: c.Version, ToStatus: domain.GenericResolved, ResolutionCode: "ANSWERED", At: at}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.TransitionGenericCase(ctx, CaseMove{ID: c.ID, Version: resolved.Version, ToStatus: domain.GenericClosed, At: at}, "", ""); err != nil {
		t.Fatal(err)
	}
	late, _ := ingest(InboundInput{MessageID: "<m4@customer.test>", Subject: "[" + c.Number + "] still noisy", AutoSubmitted: "no"})
	if late.Route != domain.RouteNewCase || late.CaseID == c.ID || late.ReopenedFrom == nil || *late.ReopenedFrom != c.ID || !late.Acknowledge {
		t.Fatalf("mail to a closed case = %+v", late)
	}
	next, err := s.GetGenericCase(ctx, late.CaseID)
	if err != nil || next.ReopenedFrom == nil || *next.ReopenedFrom != c.ID || next.Status != domain.GenericOpen {
		t.Fatalf("linked case = %+v, %v", next, err)
	}

	// An auto-reply without a token opens a case but is not acknowledged.
	auto, _ := ingest(InboundInput{MessageID: "<m5@customer.test>", Subject: "Out of office", AutoSubmitted: "auto-replied"})
	if auto.Route != domain.RouteNewCase || auto.Acknowledge || !auto.Automated {
		t.Fatalf("auto-reply = %+v", auto)
	}

	// A case in a restricted category is not matched for a caller without its duty.
	hr, _, err := s.CreateCategory(ctx, Category{Code: "HR-MAIL", Name: "HR", CategoryType: domain.CategoryHR, DefaultPriority: 3, AccessDuty: "HrCaseAccess", ResolutionCodes: []string{}}, "cat-hr", "h-hr")
	if err != nil {
		t.Fatal(err)
	}
	hrCtx := userCtx(51, ids.New(), "HrCaseAccess")
	hrCase, _, err := s.OpenGenericCase(hrCtx, OpenGenericInput{CategoryID: hr.ID, LegalEntityID: le, Subject: "Grievance", OpenedAt: at}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	probe, _ := ingest(InboundInput{MessageID: "<m6@customer.test>", Subject: "Re: [" + hrCase.Number + "] grievance"})
	if probe.Route != domain.RouteNewCase || probe.CaseID == hrCase.ID || probe.ReopenedFrom != nil {
		t.Fatalf("restricted case matched for a caller without its duty: %+v", probe)
	}
	if _, _, err := s.UpsertMailbox(ctx, Mailbox{Address: "hr@example.test", LegalEntityID: le, CategoryID: hr.ID}, at); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.IngestInbound(ctx, InboundInput{Mailbox: "hr@example.test", MessageID: "<m7@x>", FromAddress: "a@b.test", ReceivedAt: at, At: at}); problemCode(err) != domain.CodeCaseSecurity {
		t.Fatalf("new case in a restricted category without its duty = %v", err)
	}
	if _, _, err := s.IngestInbound(ctx, InboundInput{Mailbox: "nobody@example.test", MessageID: "<m8@x>", FromAddress: "a@b.test", ReceivedAt: at, At: at}); problemCode(err) != domain.CodeMailbox {
		t.Fatalf("unknown mailbox = %v", err)
	}

	msgs, err := s.ListInboundMessages(ctx, InboundFilter{CaseID: c.ID, Limit: 10})
	if err != nil || len(msgs.Items) != 3 || msgs.Items[0].MessageID != "<m3@customer.test>" || msgs.Items[0].CaseNumber != c.Number {
		t.Fatalf("messages of the case = %+v, %v", msgs, err)
	}
	all, err := s.ListInboundMessages(ctx, InboundFilter{Route: domain.RouteNewCase, Limit: 2})
	if err != nil || len(all.Items) != 2 || all.NextCursor == "" {
		t.Fatalf("new-case messages page = %+v, %v", all, err)
	}
	boxes, err := s.ListMailboxes(ctx)
	if err != nil || len(boxes) != 2 || boxes[0].Address != "hr@example.test" {
		t.Fatalf("mailboxes = %+v, %v", boxes, err)
	}
}

func trimZeros(s string) string {
	for len(s) > 1 && s[0] == '0' {
		s = s[1:]
	}
	return s
}
