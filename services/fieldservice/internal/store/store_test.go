package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/migrations"
)

// principalCtx carries a tenant with no legal-entity scope, so the restrictive
// policy sees '*' and every row of the tenant is visible.
func principalCtx(tenantID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, Duties: map[string]struct{}{}})
}

// AC-45.1 and AC-45.6 against Postgres: a case resolves to the customer's
// agreement and starts the SLA clock; the completed work order posts its
// labor split and consumes included hours once.
func TestStore_CaseResolvesAgreementAndPostsBilling(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := principalCtx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	customer, le, territory := ids.New(), ids.New(), ids.New()
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}

	prof, _, err := s.CreateSLAProfile(ctx, SLAProfile{Code: "GOLD", Name: "Gold", TimeZone: "America/Chicago", OpenMinute: 480, CloseMinute: 1080,
		Targets: []domain.Target{{Priority: 2, ResponseMinutes: 60, ResolutionMinutes: 480}}}, "profile-1", "hash-profile")
	if err != nil {
		t.Fatal(err)
	}
	agr, _, err := s.CreateAgreement(ctx, Agreement{CustomerPartyID: customer, LegalEntityID: le, ValidFrom: "2026-01-01",
		SLAProfileID: prof.ID, BillingModel: "PREPAID", PriorityRank: 5, Status: "ACTIVE",
		Lines: []domain.AgreementLine{{IncludedHours: decimal.NewFromInt(1), LaborCovered: true}}}, "agreement-1", "hash-agreement")
	if err != nil {
		t.Fatal(err)
	}

	opened := time.Date(2026, 10, 23, 16, 0, 0, 0, chicago)
	c, _, err := s.OpenCase(ctx, OpenCaseInput{CustomerPartyID: customer, LegalEntityID: le, Channel: "PHONE", Priority: 2, OpenedAt: opened},
		"case-1", "hash-case")
	if err != nil {
		t.Fatal(err)
	}
	if c.AgreementID == nil || *c.AgreementID != agr.ID {
		t.Fatalf("case resolved to %v, want agreement %s", c.AgreementID, agr.ID)
	}
	// Resolution: 480 business minutes from Fri 16:00 is Mon 14:00 (no pause yet).
	if want := time.Date(2026, 10, 26, 14, 0, 0, 0, chicago); !c.SLAResolutionDue.Equal(want) {
		t.Fatalf("resolution due %s, want %s", c.SLAResolutionDue, want)
	}

	wo, _, err := s.CreateWorkOrder(ctx, WorkOrder{CaseID: &c.ID, AgreementID: &agr.ID, CustomerPartyID: customer, LegalEntityID: le,
		TerritoryID: territory, EstDurationMinutes: 180, Currency: "USD",
		Lines: []domain.WorkLine{{ID: ids.New(), Kind: domain.KindLabor, Qty: decimal.NewFromInt(3), UnitPrice: decimal.NewFromInt(95)}}},
		"wo-1", "hash-wo")
	if err != nil {
		t.Fatal(err)
	}
	version := wo.Version
	for _, to := range []domain.WOStatus{domain.WOScheduled, domain.WODispatched, domain.WOTraveling, domain.WOInProgress, domain.WOCompleted} {
		if wo, err = s.TransitionWorkOrder(ctx, wo.ID, version, to); err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
		version = wo.Version
	}

	posted, replayed, err := s.PostWorkOrder(ctx, wo.ID, version, "post-1")
	if err != nil || replayed {
		t.Fatalf("post: %v replayed=%v", err, replayed)
	}
	if posted.Status != string(domain.WOPosted) || posted.Posting == nil {
		t.Fatalf("status %s posting %v", posted.Status, posted.Posting)
	}
	if !posted.Posting.BillableTotal.Equal(decimal.NewFromInt(190)) || !posted.Posting.EntitlementConsumed.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("posting %+v, want 190 billable and 1 hour consumed", posted.Posting)
	}

	again, replayed, err := s.PostWorkOrder(ctx, wo.ID, 0, "post-1")
	if err != nil || !replayed || again.Posting == nil {
		t.Fatalf("replay: %v replayed=%v", err, replayed)
	}
}

// fixture is a migrated store with one SLA profile and one ACTIVE agreement
// granting one included labor hour to a customer in a legal entity.
type fixture struct {
	s                  *Postgres
	ctx                context.Context
	customer, le, terr string
	profile            SLAProfile
	agreement          Agreement
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	admin, pool := testkit.DBWithApp(t)
	ctx := principalCtx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	f := fixture{s: New(pool), ctx: ctx, customer: ids.New(), le: ids.New(), terr: ids.New()}
	var err error
	f.profile, _, err = f.s.CreateSLAProfile(ctx, SLAProfile{Code: "STD", Name: "Standard", TimeZone: "America/Chicago", OpenMinute: 480, CloseMinute: 1080,
		Holidays: []string{"2026-10-26"}, Targets: []domain.Target{{Priority: 2, ResponseMinutes: 60, ResolutionMinutes: 480}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	f.agreement, _, err = f.s.CreateAgreement(ctx, Agreement{CustomerPartyID: f.customer, LegalEntityID: f.le, ValidFrom: "2026-01-01",
		SLAProfileID: f.profile.ID, BillingModel: "PREPAID", PriorityRank: 5, Status: "ACTIVE",
		Lines: []domain.AgreementLine{{IncludedHours: decimal.NewFromInt(1), LaborCovered: true}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f fixture) completedOrder(t *testing.T, wo WorkOrder) WorkOrder {
	t.Helper()
	created, _, err := f.s.CreateWorkOrder(f.ctx, wo, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []domain.WOStatus{domain.WOScheduled, domain.WODispatched, domain.WOTraveling, domain.WOInProgress, domain.WOCompleted} {
		if created, err = f.s.TransitionWorkOrder(f.ctx, created.ID, created.Version, to); err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
	}
	return created
}

func laborHours(n int64) []domain.WorkLine {
	return []domain.WorkLine{{ID: ids.New(), Kind: domain.KindLabor, Qty: decimal.NewFromInt(n), UnitPrice: decimal.NewFromInt(100)}}
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

// BR-45.2 on the stored profile: the Monday holiday carries no business time,
// so 480 minutes from Fri 16:00 end Tue 14:00 rather than Mon 14:00.
func TestStore_ProfileHolidaysMoveTheDue(t *testing.T) {
	f := newFixture(t)
	chicago, _ := time.LoadLocation("America/Chicago")
	c, _, err := f.s.OpenCase(f.ctx, OpenCaseInput{CustomerPartyID: f.customer, LegalEntityID: f.le, Channel: "PHONE", Priority: 2,
		OpenedAt: time.Date(2026, 10, 23, 16, 0, 0, 0, chicago)}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 27, 14, 0, 0, 0, chicago); !c.SLAResolutionDue.Equal(want) {
		t.Fatalf("resolution due %s, want %s", c.SLAResolutionDue, want)
	}
	got, err := f.s.GetSLAProfile(f.ctx, f.profile.ID)
	if err != nil || len(got.Holidays) != 1 || got.Holidays[0] != "2026-10-26" {
		t.Fatalf("stored holidays %v, err %v", got.Holidays, err)
	}
}

// A case opened in another legal entity does not resolve to this entity's
// agreement; with no fallback profile it is refused (SVC-ENT-002).
func TestStore_EntitlementStaysInsideTheLegalEntity(t *testing.T) {
	f := newFixture(t)
	_, _, err := f.s.OpenCase(f.ctx, OpenCaseInput{CustomerPartyID: f.customer, LegalEntityID: ids.New(), Channel: "EMAIL", Priority: 2,
		OpenedAt: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}, "", "")
	if problemCode(err) != CodeNoEntitlement {
		t.Fatalf("err %v, want %s", err, CodeNoEntitlement)
	}
	fallback := f.profile.ID
	c, _, err := f.s.OpenCase(f.ctx, OpenCaseInput{CustomerPartyID: f.customer, LegalEntityID: ids.New(), Channel: "EMAIL", Priority: 2,
		OpenedAt: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC), FallbackSLAProfileID: &fallback}, "", "")
	if err != nil || c.AgreementID != nil || c.SLAProfileID != fallback {
		t.Fatalf("case %+v err %v, want the fallback profile and no agreement", c, err)
	}
}

// FR-45.6: a work order from a case inherits the agreement the case resolved
// to; a case or agreement of another customer is refused.
func TestStore_WorkOrderFromCaseInheritsAgreement(t *testing.T) {
	f := newFixture(t)
	c, _, err := f.s.OpenCase(f.ctx, OpenCaseInput{CustomerPartyID: f.customer, LegalEntityID: f.le, Channel: "PHONE", Priority: 2,
		OpenedAt: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	wo, _, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{CaseID: &c.ID, CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 60, Currency: "USD"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if wo.AgreementID == nil || *wo.AgreementID != f.agreement.ID {
		t.Fatalf("work order agreement %v, want the case's %s", wo.AgreementID, f.agreement.ID)
	}
	other := ids.New()
	if _, _, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{CaseID: &c.ID, CustomerPartyID: other, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 60, Currency: "USD"}, "", ""); problemCode(err) != CodeCaseCustomer {
		t.Fatalf("other customer's case: err %v, want %s", err, CodeCaseCustomer)
	}
	if _, _, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AgreementID: &f.agreement.ID, CustomerPartyID: other, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 60, Currency: "USD"}, "", ""); problemCode(err) != CodeAgreementCustomer {
		t.Fatalf("other customer's agreement: err %v, want %s", err, CodeAgreementCustomer)
	}
	missing := ids.New()
	if _, _, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{CaseID: &missing, CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 60, Currency: "USD"}, "", ""); problemCode(err) != CodeCaseUnknown {
		t.Fatalf("unknown case: err %v, want %s", err, CodeCaseUnknown)
	}
}

// BR-45.4: included hours are consumed once across work orders. The first
// order uses the single included hour; the second bills all its labor. The
// agreement reports the consumed hour, and the posted event carries the lines
// and customer that wave-29 invoicing needs.
func TestStore_IncludedHoursAreConsumedAcrossOrders(t *testing.T) {
	f := newFixture(t)
	first := f.completedOrder(t, WorkOrder{AgreementID: &f.agreement.ID, CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 120, Currency: "USD", Lines: laborHours(2)})
	second := f.completedOrder(t, WorkOrder{AgreementID: &f.agreement.ID, CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 120, Currency: "USD", Lines: laborHours(2)})

	p1, _, err := f.s.PostWorkOrder(f.ctx, first.ID, first.Version, "post-a")
	if err != nil {
		t.Fatal(err)
	}
	if !p1.Posting.BillableTotal.Equal(decimal.NewFromInt(100)) || !p1.Posting.EntitlementConsumed.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("first posting %+v, want 100 billable and 1 hour consumed", p1.Posting)
	}
	p2, _, err := f.s.PostWorkOrder(f.ctx, second.ID, second.Version, "post-b")
	if err != nil {
		t.Fatal(err)
	}
	if !p2.Posting.BillableTotal.Equal(decimal.NewFromInt(200)) || !p2.Posting.EntitlementConsumed.IsZero() {
		t.Fatalf("second posting %+v, want 200 billable and nothing consumed", p2.Posting)
	}
	agr, err := f.s.GetAgreement(f.ctx, f.agreement.ID)
	if err != nil || !agr.HoursConsumed.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("agreement consumed %s, err %v, want 1", agr.HoursConsumed, err)
	}

	// The key of the first post cannot post another order.
	third := f.completedOrder(t, WorkOrder{CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 60, Currency: "USD", Lines: laborHours(1)})
	if _, _, err := f.s.PostWorkOrder(f.ctx, third.ID, third.Version, "post-a"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("reused post key: err %v, want %s", err, CodeIdempotencyReuse)
	}

	var payload []byte
	if err := dbx.WithTenantTx(f.ctx, f.s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `select payload from outbox where aggregate_id = $1 and type = 'dev.passage.service.workorder.posted.v1'`, first.ID).Scan(&payload)
	}); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data struct {
			CustomerID    string `json:"customerId"`
			LegalEntityID string `json:"legalEntityId"`
			BillableTotal string `json:"billableTotal"`
			Lines         []struct {
				Coverage string `json:"coverage"`
				Qty      string `json:"qty"`
			} `json:"lines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Data.CustomerID != f.customer || evt.Data.LegalEntityID != f.le || evt.Data.BillableTotal != "100" || len(evt.Data.Lines) != 2 {
		t.Fatalf("posted event %+v", evt.Data)
	}
}

// A DRAFT agreement covers nothing at posting (BR-45.1 candidates are ACTIVE only).
func TestStore_InactiveAgreementCoversNothing(t *testing.T) {
	f := newFixture(t)
	draft, _, err := f.s.CreateAgreement(f.ctx, Agreement{CustomerPartyID: f.customer, LegalEntityID: f.le, ValidFrom: "2026-01-01",
		SLAProfileID: f.profile.ID, BillingModel: "PER_VISIT", PriorityRank: 1, Status: "DRAFT",
		Lines: []domain.AgreementLine{{IncludedHours: decimal.NewFromInt(5), LaborCovered: true, PartsCovered: true}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	wo := f.completedOrder(t, WorkOrder{AgreementID: &draft.ID, CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 60, Currency: "USD", Lines: laborHours(2)})
	posted, _, err := f.s.PostWorkOrder(f.ctx, wo.ID, wo.Version, "post-draft")
	if err != nil {
		t.Fatal(err)
	}
	if !posted.Posting.BillableTotal.Equal(decimal.NewFromInt(200)) || !posted.Posting.EntitlementConsumed.IsZero() {
		t.Fatalf("posting %+v, want all 200 billable", posted.Posting)
	}
}

// Optimistic versioning: a stale If-Match version is refused with 412.
func TestStore_StaleVersionIsRefused(t *testing.T) {
	f := newFixture(t)
	wo, _, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
		EstDurationMinutes: 60, Currency: "USD"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, wo.Version, domain.WOScheduled); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, wo.Version, domain.WOCanceled); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: err %v, want %s", err, CodeVersionConflict)
	}
	if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, wo.Version+1, domain.WOPosted); problemCode(err) != CodeWOInvalid {
		t.Fatalf("SCHEDULED to POSTED: err %v, want %s", err, CodeWOInvalid)
	}
}

// Lists are newest first, filtered, and page through NextCursor without gaps or repeats.
func TestStore_ListsPageNewestFirst(t *testing.T) {
	f := newFixture(t)
	var created []string
	for i := 0; i < 5; i++ {
		wo, _, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{CustomerPartyID: f.customer, LegalEntityID: f.le, TerritoryID: f.terr,
			EstDurationMinutes: 30, Currency: "USD"}, "", "")
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, wo.ID)
	}
	if _, err := f.s.TransitionWorkOrder(f.ctx, created[0], 1, domain.WOScheduled); err != nil {
		t.Fatal(err)
	}
	var seen []string
	cursor := ""
	for {
		page, err := f.s.ListWorkOrders(f.ctx, ListFilter{Limit: 2, Before: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range page.Items {
			seen = append(seen, w.ID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 || seen[0] != created[4] || seen[4] != created[0] {
		t.Fatalf("paged %v, want the 5 orders newest first", seen)
	}
	scheduled, err := f.s.ListWorkOrders(f.ctx, ListFilter{Limit: 10, Status: "SCHEDULED"})
	if err != nil || len(scheduled.Items) != 1 || scheduled.Items[0].ID != created[0] {
		t.Fatalf("SCHEDULED filter %+v, err %v", scheduled.Items, err)
	}
	agreements, err := f.s.ListAgreements(f.ctx, ListFilter{Limit: 10, Customer: f.customer})
	if err != nil || len(agreements.Items) != 1 || agreements.Items[0].ID != f.agreement.ID {
		t.Fatalf("agreements %+v, err %v", agreements.Items, err)
	}
	cases, err := f.s.ListCases(f.ctx, ListFilter{Limit: 10, SLAState: "BREACHED"})
	if err != nil || len(cases.Items) != 0 {
		t.Fatalf("no case is breached yet, got %d, err %v", len(cases.Items), err)
	}
	profiles, err := f.s.ListSLAProfiles(f.ctx)
	if err != nil || len(profiles) != 1 || profiles[0].Code != "STD" {
		t.Fatalf("profiles %+v, err %v", profiles, err)
	}
}

// Row-level security under the application role: another tenant sees none of
// tenant 1's rows, and a caller scoped to another legal entity sees none of
// its legal-entity rows.
func TestStore_TenantAndLegalEntityIsolation(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	t1, t2 := principalCtx(testkit.TenantID(1)), principalCtx(testkit.TenantID(2))
	customer, le := ids.New(), ids.New()
	prof, _, err := s.CreateSLAProfile(t1, SLAProfile{Code: "ISO", Name: "Isolation", TimeZone: "UTC", AllDay: true,
		Targets: []domain.Target{{Priority: 1, ResponseMinutes: 30, ResolutionMinutes: 240}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	agr, _, err := s.CreateAgreement(t1, Agreement{CustomerPartyID: customer, LegalEntityID: le, ValidFrom: "2026-01-01", SLAProfileID: prof.ID,
		BillingModel: "PREPAID", PriorityRank: 1, Status: "ACTIVE", Lines: []domain.AgreementLine{{}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := s.OpenCase(t1, OpenCaseInput{CustomerPartyID: customer, LegalEntityID: le, Channel: "PORTAL", Priority: 1, OpenedAt: time.Now().UTC()}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCase(t2, c.ID); problemCode(err) != CodeCaseUnknown {
		t.Fatalf("tenant 2 read tenant 1's case: err %v", err)
	}
	if _, err := s.GetAgreement(t2, agr.ID); problemCode(err) != CodeAgreementNotFound {
		t.Fatalf("tenant 2 read tenant 1's agreement: err %v", err)
	}
	if page, err := s.ListCases(t2, ListFilter{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 lists %d cases, err %v", len(page.Items), err)
	}
	if profiles, err := s.ListSLAProfiles(t2); err != nil || len(profiles) != 0 {
		t.Fatalf("tenant 2 lists %d profiles, err %v", len(profiles), err)
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: ids.New(), Duties: map[string]struct{}{}})
	if page, err := s.ListAgreements(scoped, ListFilter{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("a caller scoped to another legal entity lists %d agreements, err %v", len(page.Items), err)
	}
	if page, err := s.ListCases(t1, ListFilter{Limit: 10}); err != nil || len(page.Items) != 1 {
		t.Fatalf("tenant 1 lists %d cases, err %v", len(page.Items), err)
	}
	// Slice 2 under RLS: another tenant can neither move the case nor have the
	// monitor see it; the owning tenant can do both.
	later := time.Now().UTC().Add(48 * time.Hour)
	if _, _, err := s.TransitionCase(t2, c.ID, c.Version, domain.CaseInProgress, time.Now().UTC(), "", ""); problemCode(err) != CodeCaseUnknown {
		t.Fatalf("tenant 2 moved tenant 1's case: err %v", err)
	}
	if res, err := s.ScanSLA(t2, later, 100); err != nil || res.Scanned != 0 {
		t.Fatalf("tenant 2 monitor saw %+v, err %v", res, err)
	}
	if res, err := s.ScanSLA(t1, later, 100); err != nil || res.Scanned != 1 || res.Breaches != 2 {
		t.Fatalf("tenant 1 monitor: %+v, err %v; want the response and resolution breaches", res, err)
	}
	if k, err := s.KPIs(t2, KPIFilter{From: later.Add(-96 * time.Hour), To: later, AsOf: later}); err != nil || k.CasesOpened != 0 {
		t.Fatalf("tenant 2 KPIs counted %d cases, err %v", k.CasesOpened, err)
	}
}
