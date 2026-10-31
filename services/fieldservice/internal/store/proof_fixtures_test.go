package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

// The fixtures live with the domain tests, which hold their reference scripts.
var proofFixtures = filepath.Join("..", "domain", "testdata")

func readFixture(t *testing.T, dir, name string, into any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(proofFixtures, dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s/%s: %v", dir, name, err)
	}
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

type entProfile struct {
	TimeZone string   `json:"timeZone"`
	AllDay   bool     `json:"allDay"`
	Open     int      `json:"open"`
	Close    int      `json:"close"`
	Holidays []string `json:"holidays"`
	Targets  [][3]int `json:"targets"`
}

type entCase struct {
	ID          string    `json:"id"`
	DomainOnly  bool      `json:"domainOnly"`
	Customer    string    `json:"customer"`
	LegalEntity string    `json:"legalEntity"`
	Object      string    `json:"object"`
	Category    string    `json:"category"`
	OpenedAt    time.Time `json:"openedAt"`
	Priority    int       `json:"priority"`
	Fallback    string    `json:"fallback"`
	Agreements  []struct {
		ID           string `json:"id"`
		Customer     string `json:"customer"`
		LegalEntity  string `json:"legalEntity"`
		Status       string `json:"status"`
		ValidFrom    string `json:"validFrom"`
		ValidTo      string `json:"validTo"`
		PriorityRank int    `json:"priorityRank"`
		Profile      string `json:"profile"`
		Lines        []struct {
			Object   string `json:"object"`
			Category string `json:"category"`
		} `json:"lines"`
	} `json:"agreements"`
}

type entAnswer struct {
	Error            string  `json:"error"`
	Agreement        *string `json:"agreement"`
	Profile          string  `json:"profile"`
	ResponseDue      string  `json:"responseDue"`
	ResponseWarnAt   string  `json:"responseWarnAt"`
	ResolutionDue    string  `json:"resolutionDue"`
	ResolutionWarnAt string  `json:"resolutionWarnAt"`
}

// AC-45.1 against Postgres (as passage_app): every svc-entitlement-50 case that
// case open can express is opened through OpenCase with its own customer,
// legal entities, service objects and agreements, and lands on the reference
// agreement, SLA profile and the four clock instants, also after a re-read.
// The four domainOnly cases are covered by TestResolve_SvcEntitlement50: case
// open passes no category (service objects have none), and E12's tie is broken
// by the agreement id, which the store draws at random.
func TestStore_SvcEntitlement50OpensCases(t *testing.T) {
	var fx struct {
		Profiles map[string]entProfile `json:"profiles"`
		Cases    []entCase             `json:"cases"`
	}
	var want map[string]entAnswer
	readFixture(t, "svc-entitlement-50", "cases.json", &fx)
	readFixture(t, "svc-entitlement-50", "expected.json", &want)
	s, ctx := newSliceStore(t, 61)

	profileIDs := map[string]string{}
	codes := make([]string, 0, len(fx.Profiles))
	for code := range fx.Profiles {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	for _, code := range codes {
		p := fx.Profiles[code]
		in := SLAProfile{Code: code, Name: code, TimeZone: p.TimeZone, AllDay: p.AllDay, OpenMinute: p.Open, CloseMinute: p.Close, Holidays: p.Holidays}
		if p.AllDay {
			in.OpenMinute, in.CloseMinute = 0, 24*60
		}
		for _, tg := range p.Targets {
			in.Targets = append(in.Targets, domain.Target{Priority: tg[0], ResponseMinutes: tg[1], ResolutionMinutes: tg[2]})
		}
		created, _, err := s.CreateSLAProfile(ctx, in, "", "")
		if err != nil {
			t.Fatalf("profile %s: %v", code, err)
		}
		profileIDs[code] = created.ID
	}

	opened := 0
	for _, c := range fx.Cases {
		if c.DomainOnly {
			continue
		}
		opened++
		t.Run(c.ID, func(t *testing.T) {
			exp := want[c.ID]
			parties := map[string]string{}
			party := func(label string) string {
				if parties[label] == "" {
					parties[label] = ids.New()
				}
				return parties[label]
			}
			customer, le := party(or(c.Customer, "C1")), party(or(c.LegalEntity, "LE1"))
			objects := map[string]string{}
			object := func(label string) string {
				if objects[label] == "" {
					o, _, err := s.CreateServiceObject(ctx, ServiceObject{ItemID: ids.New(), Serial: c.ID + "-" + label, CustomerPartyID: customer,
						LegalEntityID: le, Warranty: domain.WarrantyTerm{StartTrigger: domain.StartShip}}, "", "")
					if err != nil {
						t.Fatal(err)
					}
					objects[label] = o.ID
				}
				return objects[label]
			}
			agreementLabel := map[string]string{}
			for _, a := range c.Agreements {
				in := Agreement{CustomerPartyID: party(or(a.Customer, "C1")), LegalEntityID: party(or(a.LegalEntity, "LE1")),
					ValidFrom: or(a.ValidFrom, "2026-01-01"), SLAProfileID: profileIDs[a.Profile], BillingModel: "PREPAID",
					PriorityRank: a.PriorityRank, Status: or(a.Status, domain.AgreementActive)}
				if a.ValidTo != "" {
					in.ValidTo = &a.ValidTo
				}
				for _, l := range a.Lines {
					line := domain.AgreementLine{CategoryID: l.Category}
					if l.Object != "" {
						line.ServiceObjectID = object(l.Object)
					}
					in.Lines = append(in.Lines, line)
				}
				created, _, err := s.CreateAgreement(ctx, in, "", "")
				if err != nil {
					t.Fatalf("agreement %s: %v", a.ID, err)
				}
				agreementLabel[created.ID] = a.ID
			}
			req := OpenCaseInput{CustomerPartyID: customer, LegalEntityID: le, Channel: "PHONE", Priority: c.Priority,
				Description: c.ID, OpenedAt: c.OpenedAt.UTC()} // the API stores openedAt in UTC
			if c.Object != "" {
				obj := object(c.Object)
				req.ServiceObjectID = &obj
			}
			if c.Fallback != "" {
				fb := profileIDs[c.Fallback]
				req.FallbackSLAProfileID = &fb
			}
			got, _, err := s.OpenCase(ctx, req, "", "")
			if exp.Error != "" {
				if code := problemCode(err); code != exp.Error {
					t.Fatalf("open = %v (case %+v), want %s", err, got, exp.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			reread, err := s.GetCase(ctx, got.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, cs := range []Case{got, reread} {
				gotAgreement := ""
				if cs.AgreementID != nil {
					gotAgreement = agreementLabel[*cs.AgreementID]
				}
				if wantAgreement := ptrOr(exp.Agreement); gotAgreement != wantAgreement || cs.SLAProfileID != profileIDs[exp.Profile] {
					t.Fatalf("case resolved to agreement %q profile %s, want %q profile %s (trace %v)", gotAgreement, cs.SLAProfileID,
						wantAgreement, exp.Profile, cs.EntitlementTrace)
				}
				if exp.Agreement == nil && (len(cs.EntitlementTrace) == 0 || cs.EntitlementTrace[0][:11] != "SVC-ENT-001") {
					t.Fatalf("fallback case trace %v, want SVC-ENT-001", cs.EntitlementTrace)
				}
				for _, pair := range [][3]any{
					{"responseDue", cs.SLAResponseDue, exp.ResponseDue}, {"responseWarnAt", cs.SLAResponseWarnAt, exp.ResponseWarnAt},
					{"resolutionDue", cs.SLAResolutionDue, exp.ResolutionDue}, {"resolutionWarnAt", cs.SLAResolutionWarnAt, exp.ResolutionWarnAt},
				} {
					if v := pair[1].(time.Time).UTC().Format(time.RFC3339); v != pair[2].(string) {
						t.Errorf("%s = %s, want %s", pair[0], v, pair[2])
					}
				}
			}
		})
	}
	if opened != 46 {
		t.Fatalf("opened %d fixture cases through the store, want 46 (50 less 4 domainOnly)", opened)
	}
}

func ptrOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type billCase struct {
	ID          string `json:"id"`
	Currency    string `json:"currency"`
	Agreement   string `json:"agreement"`
	CompletedOn string `json:"completedOn"`
	Warranty    *struct {
		StartsOn string `json:"startsOn"`
		Months   int    `json:"months"`
		Parts    bool   `json:"parts"`
		Labor    bool   `json:"labor"`
		Travel   bool   `json:"travel"`
	} `json:"warranty"`
	Lines []domain.WorkLine `json:"lines"`
}

type billAnswer struct {
	BillableTotal       decimal.Decimal `json:"billableTotal"`
	WarrantyCost        decimal.Decimal `json:"warrantyCost"`
	EntitlementConsumed decimal.Decimal `json:"entitlementConsumed"`
	Lines               [][6]string     `json:"lines"`
}

// Ticket AC3 against Postgres (as passage_app): each svc-billing-30 order is a
// real work order on a case (on a service object carrying the order's warranty
// when it has one, under the customer's agreement when it names one), walked
// to COMPLETED on its completion day and posted. The stored posting matches
// the reference line by line; the posted event carries the same totals and its
// BILLABLE lines sum to the billable total (what wave-29 invoicing reads); the
// agreement's usage rows add up to the hours consumed, carried across B20-B22.
func TestStore_SvcBilling30PostsWorkOrders(t *testing.T) {
	var fx struct {
		Agreements map[string]struct {
			Parts         bool            `json:"parts"`
			Labor         bool            `json:"labor"`
			Travel        bool            `json:"travel"`
			IncludedHours decimal.Decimal `json:"includedHours"`
		} `json:"agreements"`
		WorkOrders []billCase `json:"workOrders"`
	}
	var want map[string]billAnswer
	readFixture(t, "svc-billing-30", "cases.json", &fx)
	readFixture(t, "svc-billing-30", "expected.json", &want)
	if len(fx.WorkOrders) != 30 || len(want) != 30 {
		t.Fatalf("fixture has %d orders and %d answers, want 30", len(fx.WorkOrders), len(want))
	}
	s, ctx := newSliceStore(t, 62)
	prof, _, err := s.CreateSLAProfile(ctx, SLAProfile{Code: "BILL", Name: "Billing", TimeZone: "UTC", OpenMinute: 0, CloseMinute: 1440,
		Targets: []domain.Target{{Priority: 3, ResponseMinutes: 60, ResolutionMinutes: 480}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	type party struct{ customer, le, agreement string }
	byAgreement := map[string]party{}
	consumed := map[string]decimal.Decimal{}

	for _, b := range fx.WorkOrders {
		t.Run(b.ID, func(t *testing.T) {
			p, shared := byAgreement[b.Agreement]
			if b.Agreement == "" || !shared {
				p = party{customer: ids.New(), le: ids.New()}
				if b.Agreement != "" {
					a := fx.Agreements[b.Agreement]
					agr, _, err := s.CreateAgreement(ctx, Agreement{CustomerPartyID: p.customer, LegalEntityID: p.le, ValidFrom: "2020-01-01",
						SLAProfileID: prof.ID, BillingModel: "PREPAID", PriorityRank: 1, Status: domain.AgreementActive,
						Lines: []domain.AgreementLine{{IncludedHours: a.IncludedHours, PartsCovered: a.Parts, LaborCovered: a.Labor, TravelCovered: a.Travel}}}, "", "")
					if err != nil {
						t.Fatal(err)
					}
					p.agreement = agr.ID
					byAgreement[b.Agreement] = p
				}
			}
			req := OpenCaseInput{CustomerPartyID: p.customer, LegalEntityID: p.le, Channel: "PHONE", Priority: 3,
				OpenedAt: time.Date(2020, 1, 2, 9, 0, 0, 0, time.UTC), FallbackSLAProfileID: &prof.ID}
			if w := b.Warranty; w != nil {
				obj, _, err := s.CreateServiceObject(ctx, ServiceObject{ItemID: ids.New(), Serial: b.ID, CustomerPartyID: p.customer, LegalEntityID: p.le,
					ShippedOn: &w.StartsOn, Warranty: domain.WarrantyTerm{StartTrigger: domain.StartShip, Months: w.Months,
						PartsCovered: w.Parts, LaborCovered: w.Labor, TravelCovered: w.Travel}}, "", "")
				if err != nil {
					t.Fatal(err)
				}
				req.ServiceObjectID = &obj.ID
			}
			c, _, err := s.OpenCase(ctx, req, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if b.Agreement != "" && (c.AgreementID == nil || *c.AgreementID != p.agreement) {
				t.Fatalf("case agreement %v, want %s", c.AgreementID, p.agreement)
			}
			wo, _, err := s.CreateWorkOrder(ctx, WorkOrder{CaseID: &c.ID, CustomerPartyID: p.customer, LegalEntityID: p.le, TerritoryID: ids.New(),
				EstDurationMinutes: 60, Currency: b.Currency, Lines: b.Lines}, "", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, to := range []domain.WOStatus{domain.WOScheduled, domain.WODispatched, domain.WOTraveling, domain.WOInProgress, domain.WOCompleted} {
				if wo, err = s.TransitionWorkOrder(ctx, wo.ID, wo.Version, to); err != nil {
					t.Fatalf("transition to %s: %v", to, err)
				}
			}
			// The order was completed on the fixture's day (noon UTC), which decides warranty cover.
			completed := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
			if b.CompletedOn != "" {
				day, err := time.Parse(dateLayout, b.CompletedOn)
				if err != nil {
					t.Fatal(err)
				}
				completed = day.Add(12 * time.Hour)
			}
			if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `update service_work_order set completed_at = $2 where id = $1`, wo.ID, completed)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			posted, _, err := s.PostWorkOrder(ctx, wo.ID, wo.Version, "post-"+b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if posted.Status != string(domain.WOPosted) || posted.Posting == nil {
				t.Fatalf("status %s posting %v", posted.Status, posted.Posting)
			}
			checkPosting(t, *posted.Posting, want[b.ID])
			stored, err := s.GetWorkOrder(ctx, wo.ID)
			if err != nil || stored.Posting == nil {
				t.Fatalf("re-read: %v", err)
			}
			checkPosting(t, *stored.Posting, want[b.ID])
			checkPostedEvent(t, s, ctx, wo.ID, want[b.ID])
			if b.Agreement != "" {
				consumed[b.Agreement] = consumed[b.Agreement].Add(want[b.ID].EntitlementConsumed)
				agr, err := s.GetAgreement(ctx, p.agreement)
				if err != nil || !agr.HoursConsumed.Equal(consumed[b.Agreement]) {
					t.Fatalf("agreement consumed %s (err %v), want %s", agr.HoursConsumed, err, consumed[b.Agreement])
				}
			}
		})
	}
	if !consumed["AG-F"].Equal(decimal.NewFromInt(4)) {
		t.Fatalf("AG-F consumed %s over B20-B22, want all 4 included hours", consumed["AG-F"])
	}
}

func checkPosting(t *testing.T, got domain.Posting, want billAnswer) {
	t.Helper()
	if !got.BillableTotal.Equal(want.BillableTotal) || !got.WarrantyCost.Equal(want.WarrantyCost) || !got.EntitlementConsumed.Equal(want.EntitlementConsumed) {
		t.Errorf("totals billable %s warranty %s consumed %s, want %s %s %s", got.BillableTotal, got.WarrantyCost, got.EntitlementConsumed,
			want.BillableTotal, want.WarrantyCost, want.EntitlementConsumed)
	}
	wantLines := map[string][6]string{}
	for _, l := range want.Lines {
		wantLines[l[0]+"/"+l[1]] = l
	}
	if len(got.Lines) != len(wantLines) {
		t.Errorf("%d posted lines, want %d: %+v", len(got.Lines), len(wantLines), got.Lines)
	}
	for _, l := range got.Lines {
		key := l.LineID + "/" + string(l.Coverage)
		w, ok := wantLines[key]
		if !ok {
			t.Errorf("unexpected posted line %s: %+v", key, l)
			continue
		}
		for i, v := range []decimal.Decimal{l.Qty, l.Amount, l.WarrantyCost, l.AgreementHours} {
			if !v.Equal(decimal.RequireFromString(w[2+i])) {
				t.Errorf("line %s = qty %s amount %s warrantyCost %s hours %s, want %v", key, l.Qty, l.Amount, l.WarrantyCost, l.AgreementHours, w[2:])
				break
			}
		}
	}
}

// checkPostedEvent reads the order's workorder.posted event: its totals equal
// the reference and its BILLABLE lines sum to the billable total.
func checkPostedEvent(t *testing.T, s *Postgres, ctx context.Context, woID string, want billAnswer) {
	t.Helper()
	var payload []byte
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select payload from outbox where aggregate_id = $1 and type = 'dev.passage.service.workorder.posted.v1'`, woID).Scan(&payload)
	}); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data struct {
			BillableTotal       decimal.Decimal `json:"billableTotal"`
			WarrantyCost        decimal.Decimal `json:"warrantyCost"`
			EntitlementConsumed decimal.Decimal `json:"entitlementConsumed"`
			Lines               []struct {
				Coverage string          `json:"coverage"`
				Amount   decimal.Decimal `json:"amount"`
			} `json:"lines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		t.Fatal(err)
	}
	sum := decimal.Zero
	for _, l := range evt.Data.Lines {
		if l.Coverage == string(domain.CoverBillable) {
			sum = sum.Add(l.Amount)
		}
	}
	if !evt.Data.BillableTotal.Equal(want.BillableTotal) || !evt.Data.WarrantyCost.Equal(want.WarrantyCost) ||
		!evt.Data.EntitlementConsumed.Equal(want.EntitlementConsumed) || !sum.Equal(want.BillableTotal) || len(evt.Data.Lines) != len(want.Lines) {
		t.Errorf("posted event %+v (billable lines sum %s), want totals %s/%s/%s over %d lines", evt.Data, sum,
			want.BillableTotal, want.WarrantyCost, want.EntitlementConsumed, len(want.Lines))
	}
}
