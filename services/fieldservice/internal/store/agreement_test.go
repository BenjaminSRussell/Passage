package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

func (f fixture) newAgreement(t *testing.T, status string, validTo *string, visits int) Agreement {
	t.Helper()
	a, _, err := f.s.CreateAgreement(f.ctx, Agreement{CustomerPartyID: f.customer, LegalEntityID: f.le, ValidFrom: "2026-01-01", ValidTo: validTo,
		SLAProfileID: f.profile.ID, BillingModel: "PER_VISIT", PriorityRank: 5, Status: status,
		Lines: []domain.AgreementLine{{IncludedVisits: visits, IncludedHours: decimal.NewFromInt(2), LaborCovered: true}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func outboxCount(t *testing.T, f fixture, eventType string) int {
	t.Helper()
	var n int
	err := dbx.WithTenantTx(f.ctx, f.s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `select count(*) from outbox where type = $1`, eventType).Scan(&n)
	})
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	return n
}

// FR-45.3: DRAFT to ACTIVE to SUSPENDED and back, then CANCELED; each step bumps
// the version, is logged and writes one event; EXPIRED cannot be requested and a
// canceled agreement is final. A suspended agreement no longer entitles a case.
func TestStore_AgreementLifecycle(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	a := f.newAgreement(t, "DRAFT", nil, 2)
	if a.Version != 1 {
		t.Fatalf("new version %d", a.Version)
	}
	if _, _, err := f.s.TransitionAgreement(f.ctx, a.ID, a.Version, "SUSPENDED", "x", now, "", ""); problemCode(err) != CodeAgreementTransition {
		t.Fatalf("DRAFT to SUSPENDED: %v", err)
	}
	a, _, err := f.s.TransitionAgreement(f.ctx, a.ID, a.Version, "ACTIVE", "", now, "", "")
	if err != nil || a.Status != "ACTIVE" || a.Version != 2 {
		t.Fatalf("activate: %+v %v", a, err)
	}
	if _, _, err := f.s.TransitionAgreement(f.ctx, a.ID, 1, "SUSPENDED", "audit", now, "", ""); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	if _, _, err := f.s.TransitionAgreement(f.ctx, a.ID, a.Version, "EXPIRED", "", now, "", ""); problemCode(err) != CodeAgreementTransition {
		t.Fatalf("EXPIRED by hand: %v", err)
	}
	a, _, err = f.s.TransitionAgreement(f.ctx, a.ID, a.Version, "SUSPENDED", "credit hold", now, "", "")
	if err != nil || a.Status != "SUSPENDED" || a.StatusReason == nil || *a.StatusReason != "credit hold" {
		t.Fatalf("suspend: %+v %v", a, err)
	}
	a, _, err = f.s.TransitionAgreement(f.ctx, a.ID, a.Version, "ACTIVE", "", now, "", "")
	if err != nil || a.Status != "ACTIVE" {
		t.Fatalf("resume: %+v %v", a, err)
	}
	a, _, err = f.s.TransitionAgreement(f.ctx, a.ID, a.Version, "CANCELED", "customer left", now, "", "")
	if err != nil || a.Status != "CANCELED" || a.Version != 5 {
		t.Fatalf("cancel: %+v %v", a, err)
	}
	if _, _, err := f.s.TransitionAgreement(f.ctx, a.ID, a.Version, "ACTIVE", "", now, "", ""); problemCode(err) != CodeAgreementTransition {
		t.Fatalf("reactivate a canceled agreement: %v", err)
	}
	hist, err := f.s.AgreementHistory(f.ctx, a.ID)
	if err != nil || len(hist) != 4 || hist[0].From != "DRAFT" || hist[0].To != "ACTIVE" || hist[3].To != "CANCELED" || hist[3].Reason != "customer left" {
		t.Fatalf("history %+v err %v", hist, err)
	}
	if n := outboxCount(t, f, "dev.passage.service.agreement.changed.v1"); n != 4 {
		t.Fatalf("%d agreement.changed events, want 4", n)
	}
	if _, err := f.s.AgreementHistory(f.ctx, ids.New()); problemCode(err) != CodeAgreementNotFound {
		t.Fatalf("history of unknown: %v", err)
	}
}

// A suspended agreement is skipped by the entitlement resolver (BR-45.1).
func TestStore_SuspendedAgreementDoesNotEntitle(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	if _, _, err := f.s.TransitionAgreement(f.ctx, f.agreement.ID, f.agreement.Version, "SUSPENDED", "hold", now, "", ""); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.s.OpenCase(f.ctx, OpenCaseInput{CustomerPartyID: f.customer, LegalEntityID: f.le, Channel: "PHONE", Priority: 2, OpenedAt: now}, "", "")
	if problemCode(err) != CodeNoEntitlement {
		t.Fatalf("case on a suspended agreement: %v, want %s", err, CodeNoEntitlement)
	}
}

// Activating an agreement whose validTo has passed is refused; the retried
// transition under one Idempotency-Key returns the first answer, not a 412.
func TestStore_AgreementActivationNeedsLiveTermAndReplays(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	past := "2026-09-30"
	old := f.newAgreement(t, "DRAFT", &past, 1)
	if _, _, err := f.s.TransitionAgreement(f.ctx, old.ID, old.Version, "ACTIVE", "", now, "", ""); problemCode(err) != CodeAgreementEnded {
		t.Fatalf("activate an ended term: %v", err)
	}
	draft := f.newAgreement(t, "DRAFT", nil, 1)
	first, replayed, err := f.s.TransitionAgreement(f.ctx, draft.ID, draft.Version, "ACTIVE", "", now, "act-1", "h1")
	if err != nil || replayed {
		t.Fatalf("first: %v %v", err, replayed)
	}
	again, replayed, err := f.s.TransitionAgreement(f.ctx, draft.ID, draft.Version, "ACTIVE", "", now, "act-1", "h1")
	if err != nil || !replayed || again.Version != first.Version {
		t.Fatalf("replay: %+v %v %v", again, replayed, err)
	}
	if _, _, err := f.s.TransitionAgreement(f.ctx, draft.ID, draft.Version, "CANCELED", "r", now, "act-1", "other"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if n := outboxCount(t, f, "dev.passage.service.agreement.changed.v1"); n != 1 {
		t.Fatalf("%d events after a replay, want 1", n)
	}
}

// The expiry pass expires ACTIVE and SUSPENDED agreements past validTo once.
func TestStore_ExpireAgreements(t *testing.T) {
	f := newFixture(t)
	past, today, future := "2026-10-26", "2026-10-27", "2026-12-31"
	a1 := f.newAgreement(t, "ACTIVE", &past, 1)
	a2 := f.newAgreement(t, "ACTIVE", &today, 1)
	a3 := f.newAgreement(t, "ACTIVE", &future, 1)
	a4 := f.newAgreement(t, "ACTIVE", &past, 1)
	draft := f.newAgreement(t, "DRAFT", &past, 1)
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	if _, _, err := f.s.TransitionAgreement(f.ctx, a4.ID, a4.Version, "SUSPENDED", "hold", now, "", ""); err != nil {
		t.Fatal(err)
	}
	res, err := f.s.ExpireAgreements(f.ctx, now, 100)
	if err != nil || res.Expired != 2 || res.Scanned != 2 {
		t.Fatalf("pass: %+v %v", res, err)
	}
	for id, want := range map[string]string{a1.ID: "EXPIRED", a2.ID: "ACTIVE", a3.ID: "ACTIVE", a4.ID: "EXPIRED", draft.ID: "DRAFT"} {
		got, err := f.s.GetAgreement(f.ctx, id)
		if err != nil || got.Status != want {
			t.Fatalf("agreement %s is %s (%v), want %s", id, got.Status, err, want)
		}
	}
	again, err := f.s.ExpireAgreements(f.ctx, now, 100)
	if err != nil || again.Expired != 0 {
		t.Fatalf("second pass: %+v %v", again, err)
	}
	hist, _ := f.s.AgreementHistory(f.ctx, a1.ID)
	if len(hist) != 1 || hist[0].Reason != reasonExpired {
		t.Fatalf("history %+v", hist)
	}
}

// FR-45.3: visits consume the line's includedVisits; the last one is refused
// past the allowance, a work order consumes at most one, and a replay returns the
// stored visit.
func TestStore_RecordVisitConsumesIncludedVisits(t *testing.T) {
	f := newFixture(t)
	a := f.newAgreement(t, "ACTIVE", nil, 2)
	visit := func(date, key string, wo *string) (AgreementVisit, bool, error) {
		return f.s.RecordVisit(f.ctx, VisitInput{AgreementID: a.ID, LineIndex: 0, Kind: "PREVENTIVE", VisitDate: date, WorkOrderID: wo}, key, "h-"+key)
	}
	v1, _, err := visit("2026-03-01", "v1", nil)
	if err != nil || v1.VisitsLeft != 1 {
		t.Fatalf("first: %+v %v", v1, err)
	}
	again, replayed, err := visit("2026-03-01", "v1", nil)
	if err != nil || !replayed || again.ID != v1.ID || again.VisitsLeft != 1 {
		t.Fatalf("replay: %+v %v %v", again, replayed, err)
	}
	if _, _, err := f.s.RecordVisit(f.ctx, VisitInput{AgreementID: a.ID, LineIndex: 0, Kind: "PREVENTIVE", VisitDate: "2026-03-02"}, "v1", "another-body"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	wo := f.completedOrder(t, WorkOrder{AgreementID: &a.ID, CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 60, Currency: "USD", Lines: laborHours(1)})
	v2, _, err := visit("2026-04-01", "v2", &wo.ID)
	if err != nil || v2.VisitsLeft != 0 {
		t.Fatalf("second: %+v %v", v2, err)
	}
	if _, _, err := visit("2026-05-01", "v3", nil); problemCode(err) != CodeVisitsExhausted {
		t.Fatalf("third visit: %v", err)
	}
	got, _ := f.s.GetAgreement(f.ctx, a.ID)
	if got.VisitsConsumed != 2 {
		t.Fatalf("visitsConsumed %d", got.VisitsConsumed)
	}
	list, err := f.s.ListVisits(f.ctx, a.ID)
	if err != nil || len(list) != 2 || list[0].ID != v1.ID || list[1].WorkOrderID == nil {
		t.Fatalf("list %+v %v", list, err)
	}
	if n := outboxCount(t, f, "dev.passage.service.agreementvisit.recorded.v1"); n != 2 {
		t.Fatalf("%d visit events, want 2", n)
	}
}

func TestStore_RecordVisitRules(t *testing.T) {
	f := newFixture(t)
	end := "2026-06-30"
	a := f.newAgreement(t, "ACTIVE", &end, 5)
	rec := func(agr string, line int, date string, wo *string, key string) error {
		_, _, err := f.s.RecordVisit(f.ctx, VisitInput{AgreementID: agr, LineIndex: line, Kind: "CORRECTIVE", VisitDate: date, WorkOrderID: wo}, key, key)
		return err
	}
	if err := rec(a.ID, 0, "2026-07-01", nil, "k1"); problemCode(err) != CodeVisitOutsideTerm {
		t.Fatalf("after validTo: %v", err)
	}
	if err := rec(a.ID, 0, "2025-12-31", nil, "k2"); problemCode(err) != CodeVisitOutsideTerm {
		t.Fatalf("before validFrom: %v", err)
	}
	if err := rec(a.ID, 3, "2026-03-01", nil, "k3"); problemCode(err) != CodeVisitLineUnknown {
		t.Fatalf("unknown line: %v", err)
	}
	stranger := ids.New()
	if err := rec(a.ID, 0, "2026-03-01", &stranger, "k4"); problemCode(err) != CodeVisitWorkOrder {
		t.Fatalf("unknown work order: %v", err)
	}
	other := f.completedOrder(t, WorkOrder{CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr, EstDurationMinutes: 60, Currency: "USD", Lines: laborHours(1)})
	if err := rec(a.ID, 0, "2026-03-01", &other.ID, "k5"); problemCode(err) != CodeVisitWorkOrder {
		t.Fatalf("work order of no agreement: %v", err)
	}
	if err := rec(ids.New(), 0, "2026-03-01", nil, "k6"); problemCode(err) != CodeAgreementNotFound {
		t.Fatalf("unknown agreement: %v", err)
	}
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, _, err := f.s.TransitionAgreement(f.ctx, a.ID, a.Version, "SUSPENDED", "hold", now, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := rec(a.ID, 0, "2026-03-01", nil, "k7"); problemCode(err) != CodeAgreementNotActive {
		t.Fatalf("suspended: %v", err)
	}
}

// A work order consumes at most one included visit (SVC-AGR-012), and the visit
// event carries what wave 29 bills from: the customer and the billing model.
func TestStore_RecordVisitOncePerWorkOrderAndEventPayload(t *testing.T) {
	f := newFixture(t)
	a := f.newAgreement(t, "ACTIVE", nil, 3)
	wo := f.completedOrder(t, WorkOrder{AgreementID: &a.ID, CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 60, Currency: "USD", Lines: laborHours(1)})
	in := VisitInput{AgreementID: a.ID, LineIndex: 0, Kind: "CORRECTIVE", VisitDate: "2026-03-01", WorkOrderID: &wo.ID}
	v, _, err := f.s.RecordVisit(f.ctx, in, "w1", "w1")
	if err != nil {
		t.Fatal(err)
	}
	in.VisitDate = "2026-03-02"
	if _, _, err := f.s.RecordVisit(f.ctx, in, "w2", "w2"); problemCode(err) != CodeVisitWorkOrderUsed {
		t.Fatalf("second visit of one work order: %v", err)
	}
	var customer, billing, left string
	err = dbx.WithTenantTx(f.ctx, f.s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `select payload->'data'->>'customerId', payload->'data'->>'billingModel', payload->'data'->>'visitsLeft'
			from outbox where type = 'dev.passage.service.agreementvisit.recorded.v1' and payload->'data'->>'visitId' = $1`, v.ID).Scan(&customer, &billing, &left)
	})
	if err != nil || customer != f.customer || billing != "PER_VISIT" || left != "2" {
		t.Fatalf("visit event customer %q billing %q left %q: %v", customer, billing, left, err)
	}
}

// An ACTIVE agreement whose term ended before the work order was completed
// covers nothing, even before the expiry pass marks it EXPIRED.
func TestStore_EndedAgreementCoversNothingBeforeExpiry(t *testing.T) {
	f := newFixture(t)
	ended := "2026-01-31"
	a := f.newAgreement(t, "ACTIVE", &ended, 1)
	wo := f.completedOrder(t, WorkOrder{AgreementID: &a.ID, CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 60, Currency: "USD", Lines: laborHours(1)})
	posted, _, err := f.s.PostWorkOrder(f.ctx, wo.ID, wo.Version, "post-ended")
	if err != nil {
		t.Fatal(err)
	}
	if !posted.Posting.BillableTotal.Equal(decimal.NewFromInt(100)) || !posted.Posting.EntitlementConsumed.IsZero() {
		t.Fatalf("posting %+v, want all 100 billable", posted.Posting)
	}
}

// The status log and the visits carry the legal-entity policy of their own: a
// caller scoped to another legal entity reads none of their rows.
func TestStore_AgreementLogAndVisitsLegalEntityScope(t *testing.T) {
	f := newFixture(t)
	a := f.newAgreement(t, "DRAFT", nil, 1)
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, _, err := f.s.TransitionAgreement(f.ctx, a.ID, a.Version, "ACTIVE", "", now, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.RecordVisit(f.ctx, VisitInput{AgreementID: a.ID, LineIndex: 0, Kind: "PREVENTIVE", VisitDate: "2026-03-01"}, "s1", "s1"); err != nil {
		t.Fatal(err)
	}
	count := func(ctx context.Context) (logs, visits int) {
		t.Helper()
		err := dbx.WithTenantTx(ctx, f.s.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select (select count(*) from service_agreement_status_log where agreement_id = $1),
				(select count(*) from service_agreement_visit where agreement_id = $1)`, a.ID).Scan(&logs, &visits)
		})
		if err != nil {
			t.Fatal(err)
		}
		return logs, visits
	}
	if logs, visits := count(f.ctx); logs != 1 || visits != 1 {
		t.Fatalf("own scope sees %d log rows and %d visits, want 1 and 1", logs, visits)
	}
	other := tenant.With(context.Background(), tenant.Principal{TenantID: tenantOf(f.ctx), LegalEntityID: ids.New(), Duties: map[string]struct{}{}})
	if logs, visits := count(other); logs != 0 || visits != 0 {
		t.Fatalf("another legal entity sees %d log rows and %d visits", logs, visits)
	}
	if _, err := f.s.ListVisits(other, a.ID); problemCode(err) != CodeAgreementNotFound {
		t.Fatalf("visits under another legal entity: %v", err)
	}
}
