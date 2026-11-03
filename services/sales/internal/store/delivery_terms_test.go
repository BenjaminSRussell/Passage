package store

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

func termsIn(le, customer string, transport int, days ...int) NewDeliveryTerms {
	key := ids.New()
	return NewDeliveryTerms{LegalEntityID: le, CustomerID: customer, Terms: domain.DeliveryTerms{TransportDays: transport, ReceivingDays: days},
		IdempotencyKey: key, RequestHash: key, Actor: "sales-admin"}
}

// TestPostgres_DeliveryTermsLifecycle covers create with Idempotency-Key, the
// replay, key reuse with another body, one row per customer (SLS-0056), the
// If-Match replace, an unchanged replace that keeps its version, the events
// and the list filters.
func TestPostgres_DeliveryTermsLifecycle(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, c1, c2 := ids.New(), ids.New(), ids.New()
	in := termsIn(le, c1, 2, 5, 1, 3)
	first, created, err := s.CreateDeliveryTerms(ctx, in)
	if err != nil || !created || first.Version != 1 || first.TransportDays != 2 {
		t.Fatalf("create: %+v %v %v", first, created, err)
	}
	if got := first.ReceivingDays; len(got) != 3 || got[0] != 1 || got[1] != 3 || got[2] != 5 {
		t.Fatalf("receiving days must be stored sorted: %v", got)
	}
	replay, created, err := s.CreateDeliveryTerms(ctx, in)
	if err != nil || created || replay.ID != first.ID {
		t.Fatalf("replay must return the stored terms: %+v %v %v", replay, created, err)
	}
	reused := in
	reused.RequestHash = "other"
	if _, _, err := s.CreateDeliveryTerms(ctx, reused); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if _, _, err := s.CreateDeliveryTerms(ctx, termsIn(le, c1, 1, 2)); problemCode(t, err) != CodeDeliveryTermsExist {
		t.Fatalf("second terms for one customer: %v", err)
	}
	if _, _, err := s.CreateDeliveryTerms(ctx, termsIn(le, c2, 0, 1, 2, 3, 4, 5)); err != nil {
		t.Fatal(err)
	}
	if got := outboxTypes(t, pool, first.ID); len(got) != 1 || got[0] != salesevents.DeliveryTermsCreated {
		t.Fatalf("created event: %v", got)
	}

	if _, err := s.ReplaceDeliveryTerms(ctx, first.ID, first.Version+1, domain.DeliveryTerms{TransportDays: 3, ReceivingDays: []int{2}}, "admin", time.Now()); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale replace: %v", err)
	}
	same, err := s.ReplaceDeliveryTerms(ctx, first.ID, first.Version, domain.DeliveryTerms{TransportDays: 2, ReceivingDays: []int{3, 1, 5}}, "admin", time.Now())
	if err != nil || same.Version != first.Version {
		t.Fatalf("unchanged replace must keep the version: %+v %v", same, err)
	}
	changed, err := s.ReplaceDeliveryTerms(ctx, first.ID, first.Version, domain.DeliveryTerms{TransportDays: 4, ReceivingDays: []int{2}}, "admin", time.Now())
	if err != nil || changed.Version != 2 || changed.TransportDays != 4 || changed.UpdatedBy != "admin" {
		t.Fatalf("replace: %+v %v", changed, err)
	}
	if got := outboxTypes(t, pool, first.ID); len(got) != 2 || got[1] != salesevents.DeliveryTermsChanged {
		t.Fatalf("changed event: %v", got)
	}

	one, err := s.ListDeliveryTerms(ctx, DeliveryTermsFilter{LegalEntityID: le, CustomerID: c1, Limit: 50})
	if err != nil || len(one) != 1 || one[0].ID != first.ID {
		t.Fatalf("list by customer: %+v %v", one, err)
	}
	all, err := s.ListDeliveryTerms(ctx, DeliveryTermsFilter{Limit: 50})
	if err != nil || len(all) != 2 {
		t.Fatalf("list all: %+v %v", all, err)
	}
	if _, err := s.GetDeliveryTerms(ctx, ids.New()); problemCode(t, err) != CodeDeliveryTermsNotFound {
		t.Fatalf("missing terms: %v", err)
	}
}

// delivery fixtures: 2027-03-01 is a Monday.
func dateParams(control string, lead int) domain.Parameters {
	p := domain.DefaultParameters()
	p.DeliveryDateControlDefault, p.SalesLeadTimeDays = control, lead
	return p
}

// TestPostgres_ConfirmAppliesDeliveryDateControl covers BR-27.2 at confirm:
// the requested date stands under NONE; a past date becomes today with SLS-0003;
// SALES_LEAD_TIME confirms no earlier than today plus the lead time (SLS-0057),
// a line's own control beats the legal entity default, and the confirmed
// receipt date adds the customer's transport days and rolls to a receiving day.
func TestPostgres_ConfirmAppliesDeliveryDateControl(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.PutParameters(ctx, SalesParameters{LegalEntityID: le, Parameters: dateParams(domain.DeliveryControlSalesLeadTime, 5)}, 0, "admin"); err != nil {
		t.Fatal(err)
	}
	// Transport 2 days, receives Monday to Friday: Friday ship lands on Monday.
	terms, _, err := s.CreateDeliveryTerms(ctx, termsIn(le, customer, 2, 1, 2, 3, 4, 5))
	if err != nil {
		t.Fatal(err)
	}
	none := domain.DeliveryControlNone
	line := func(no int, requested string, control *string) Line {
		l := oneLineOrder(le, customer, ids.New(), now).Lines[0]
		l.LineNo, l.RequestedShipDate, l.DeliveryDateControl = no, requested, control
		return l
	}
	in := oneLineOrder(le, customer, ids.New(), now)
	in.Lines = []Line{
		line(1, "2027-03-03", nil),   // lead time: pushed to 03-06
		line(2, "2027-03-20", nil),   // later than the lead time: kept
		line(3, "2027-03-03", &none), // own control NONE: kept
		line(4, "2027-02-20", &none), // past: today
		line(5, "2027-02-20", nil),   // past and lead time: 03-06
		line(6, "2027-03-03", nil),
	}
	in.Lines[5].RequestedShipDate = "2027-03-05"
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", now)
	if err != nil || !res.Confirmed {
		t.Fatalf("confirm: %v %+v", err, res.Evaluation)
	}
	want := map[int][2]string{ // line -> confirmed ship, confirmed receipt
		1: {"2027-03-06", "2027-03-08"}, // Saturday ship + 2 = Monday
		2: {"2027-03-20", "2027-03-22"}, // Saturday + 2 = Monday
		3: {"2027-03-03", "2027-03-05"},
		4: {"2027-03-01", "2027-03-03"},
		5: {"2027-03-06", "2027-03-08"},
		6: {"2027-03-06", "2027-03-08"},
	}
	for _, l := range res.Order.Lines {
		w := want[l.LineNo]
		if l.ConfirmedShipDate == nil || *l.ConfirmedShipDate != w[0] || l.ConfirmedReceiptDate == nil || *l.ConfirmedReceiptDate != w[1] {
			t.Errorf("line %d: ship %v receipt %v, want %v", l.LineNo, l.ConfirmedShipDate, l.ConfirmedReceiptDate, w)
		}
	}
	codes := map[string]int{}
	for _, w := range res.Order.Warnings {
		codes[w.Code]++
	}
	if codes[CodePastDate] != 2 || codes[CodeLeadTimeApplied] != 3 {
		t.Fatalf("notices: %+v", res.Order.Warnings)
	}
	if ct := res.Order.ConfirmedDeliveryTerms; ct == nil || ct.TransportDays != 2 || len(ct.ReceivingDays) != 5 {
		t.Fatalf("terms copied at confirm: %+v", ct)
	}

	// Changing the customer's terms afterwards does not move the confirmed
	// receipt dates: the order keeps the terms it was confirmed with.
	if _, err := s.ReplaceDeliveryTerms(ctx, terms.ID, terms.Version, domain.DeliveryTerms{TransportDays: 10, ReceivingDays: []int{3}}, "admin", now); err != nil {
		t.Fatal(err)
	}
	again, err := s.GetOrder(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range again.Lines {
		if w := want[l.LineNo]; l.ConfirmedReceiptDate == nil || *l.ConfirmedReceiptDate != w[1] {
			t.Errorf("line %d: receipt %v moved after the terms changed, want %s", l.LineNo, l.ConfirmedReceiptDate, w[1])
		}
	}
	// The stored line control.
	if again.Lines[2].DeliveryDateControl == nil || *again.Lines[2].DeliveryDateControl != none || again.Lines[0].DeliveryDateControl != nil {
		t.Fatalf("stored control: %+v", again.Lines)
	}
}

// TestPostgres_ConfirmWithoutTermsOrParametersKeepsRequestedDate checks the
// default: NONE, no transport time, every day a receiving day.
func TestPostgres_ConfirmWithoutTermsOrParametersKeepsRequestedDate(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	o, _, err := s.CreateOrder(ctx, oneLineOrder(le, customer, ids.New(), now))
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", now)
	if err != nil || !res.Confirmed {
		t.Fatalf("confirm: %v", err)
	}
	l := res.Order.Lines[0]
	if *l.ConfirmedShipDate != "2027-03-04" || *l.ConfirmedReceiptDate != "2027-03-04" || len(res.Order.Warnings) != 0 {
		t.Fatalf("line: %+v warnings %+v", l, res.Order.Warnings)
	}
	if ct := res.Order.ConfirmedDeliveryTerms; ct == nil || ct.TransportDays != 0 || len(ct.ReceivingDays) != 0 {
		t.Fatalf("default terms copied at confirm: %+v", ct)
	}
	// An order is not dated by terms before it is confirmed.
	draft, _, err := s.CreateOrder(ctx, oneLineOrder(le, customer, ids.New(), now))
	if err != nil || draft.ConfirmedDeliveryTerms != nil || draft.Lines[0].ConfirmedReceiptDate != nil {
		t.Fatalf("draft: %+v %v", draft, err)
	}
}

// TestPostgres_ChangeRepromisesThroughDateControl checks that a PATCH moving
// the requested date of a confirmed line confirms it through the same rule.
func TestPostgres_ChangeRepromisesThroughDateControl(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.PutParameters(ctx, SalesParameters{LegalEntityID: le, Parameters: dateParams(domain.DeliveryControlSalesLeadTime, 5)}, 0, "admin"); err != nil {
		t.Fatal(err)
	}
	// Transport 1 day, receives on Mondays only.
	terms, _, err := s.CreateDeliveryTerms(ctx, termsIn(le, customer, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := s.CreateOrder(ctx, oneLineOrder(le, customer, ids.New(), now))
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", now)
	if err != nil || !res.Confirmed {
		t.Fatalf("confirm: %v", err)
	}
	// The terms change after confirm; the re-promise below keeps dating the
	// receipt by the terms the order was confirmed with.
	if _, err := s.ReplaceDeliveryTerms(ctx, terms.ID, terms.Version, domain.DeliveryTerms{TransportDays: 0, ReceivingDays: []int{1, 2, 3, 4, 5, 6, 7}}, "admin", now); err != nil {
		t.Fatal(err)
	}
	if got := *res.Order.Lines[0].ConfirmedShipDate; got != "2027-03-06" {
		t.Fatalf("confirmed under the lead time: %s", got)
	}
	early := "2027-03-02"
	changed, err := s.ChangeOrder(ctx, o.ID, res.Order.Version, ChangeRequest{Reason: "pull in", Lines: []LineChange{{LineNo: 1, RequestedShipDate: &early}}}, "sales-rep", now)
	if err != nil {
		t.Fatal(err)
	}
	l := changed.Lines[0]
	if l.RequestedShipDate != early || *l.ConfirmedShipDate != "2027-03-06" {
		t.Fatalf("requested %s confirmed %s: the lead time must hold", l.RequestedShipDate, *l.ConfirmedShipDate)
	}
	late := "2027-03-15"
	changed, err = s.ChangeOrder(ctx, o.ID, changed.Version, ChangeRequest{Reason: "push out", Lines: []LineChange{{LineNo: 1, RequestedShipDate: &late}}}, "sales-rep", now)
	if err != nil || *changed.Lines[0].ConfirmedShipDate != late {
		t.Fatalf("a request after the lead time is kept: %+v %v", changed.Lines, err)
	}
	// Monday 03-15 plus one day is Tuesday; the next Monday is 03-22.
	if got := changed.Lines[0].ConfirmedReceiptDate; got == nil || *got != "2027-03-22" {
		t.Fatalf("re-promised receipt by the confirmed terms: %v", got)
	}
}

// TestPostgres_ChangeRequestDiffUsesDateControl checks that a change waiting
// for approval records the confirmed ship date the date control gives, not
// the requested date.
func TestPostgres_ChangeRequestDiffUsesDateControl(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.PutParameters(ctx, SalesParameters{LegalEntityID: le, Parameters: dateParams(domain.DeliveryControlSalesLeadTime, 5)}, 0, "admin"); err != nil {
		t.Fatal(err)
	}
	o, _, err := s.CreateOrder(ctx, oneLineOrder(le, customer, ids.New(), now))
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", now)
	if err != nil || !res.Confirmed {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := s.PutChangePolicy(ctx, ChangePolicy{LegalEntityID: le, CustomerID: customer, ApprovalRequired: true}, 0, "manager"); err != nil {
		t.Fatal(err)
	}
	// Confirmed for 03-06 under the lead time; pulling the request in to 03-02
	// leaves the confirmed date where the lead time holds it.
	early := "2027-03-02"
	pending, err := s.ChangeOrder(ctx, o.ID, res.Order.Version, ChangeRequest{Reason: "pull in", Lines: []LineChange{{LineNo: 1, RequestedShipDate: &early}}}, "sales-rep", now)
	if err != nil || pending.PendingChange == nil {
		t.Fatalf("change must wait for approval: %+v %v", pending.PendingChange, err)
	}
	fields := map[string]string{}
	for _, d := range pending.PendingChange.Diff {
		fields[d.Field] = d.New
	}
	if fields["requestedShipDate"] != early {
		t.Fatalf("diff must carry the requested date: %+v", pending.PendingChange.Diff)
	}
	if got, moved := fields["confirmedShipDate"]; moved {
		t.Fatalf("diff moves confirmedShipDate to %s; the lead time keeps it at 2027-03-06", got)
	}
}
