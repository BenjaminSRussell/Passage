package store

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	"github.com/BenjaminSRussell/passage/services/eam/migrations"
)

const glLegalEntity = "00000000-0000-0000-0000-0000000000e1"

type glFixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	s      *Postgres
	ctx    context.Context
	asset  string
	ledger string
	fake   *ledgerclient.Fake
	n      int
}

func newGLFixture(t *testing.T, tenantN int) *glFixture {
	t.Helper()
	pool := testkit.DB(t)
	ctx := testkit.Ctx(testkit.TenantID(tenantN), "EamPlanner", "EamTechnician")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := NewWithClock(pool, func() time.Time { return time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC) })
	a, err := s.CreateAsset(ctx, Asset{Name: "Pump", AssetType: "PUMP", SiteID: testkit.TenantID(9), Criticality: 3}, "gl-asset")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateAsset(ctx, a.ID, a.Version); err != nil {
		t.Fatal(err)
	}
	return &glFixture{t: t, pool: pool, s: s, ctx: ctx, asset: a.ID, ledger: ids.New(), fake: ledgerclient.NewFake()}
}

func (f *glFixture) order(typ, parts, hours, rate string) WorkOrder {
	f.t.Helper()
	f.n++
	key := "gl-wo-" + typ + string(rune('a'+f.n))
	wo, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: typ, Priority: 3, Currency: "USD", JobType: "LUBE"}, key)
	if err != nil {
		f.t.Fatal(err)
	}
	if hours != "" {
		if wo, err = f.s.RecordLabor(f.ctx, wo.ID, decimal.RequireFromString(hours), decimal.RequireFromString(rate), nil, key+"-labor"); err != nil {
			f.t.Fatal(err)
		}
	}
	f.setParts(wo.ID, parts)
	v := wo.Version
	for _, st := range []domain.Status{domain.StatusScheduled, domain.StatusReleased, domain.StatusInProgress} {
		got, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: st, Version: v}, "")
		if err != nil {
			f.t.Fatal(err)
		}
		v = got.Version
	}
	begin, end := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC), time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	done, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusCompleted, Version: v, ActualStart: &begin, ActualEnd: &end}, "")
	if err != nil {
		f.t.Fatal(err)
	}
	return done
}

func (f *glFixture) setParts(id, parts string) {
	f.t.Helper()
	if err := dbx.WithTenantTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(f.ctx, `update work_order set parts_cost = $2::numeric where id = $1`, id, parts)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *glFixture) post() GLPostResult {
	f.t.Helper()
	res, err := f.s.PostWorkOrderCosts(f.ctx, f.fake, GLPostRequest{LegalEntityID: glLegalEntity, LedgerID: f.ledger, Currency: "USD"})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func (f *glFixture) net(account string) decimal.Decimal {
	f.t.Helper()
	b, err := f.fake.Balance(f.ctx, ledgerclient.BalanceQuery{LedgerID: f.ledger, Account: account})
	if err != nil {
		f.t.Fatal(err)
	}
	return b.Net()
}

func dd(v string) decimal.Decimal { return decimal.RequireFromString(v) }

func TestGLPosting_PlaceholderProfileThenTypeSpecificProfile(t *testing.T) {
	f := newGLFixture(t, 7101)
	f.order(domain.TypeInspection, "100.00", "2", "30.00")
	res := f.post()
	if len(res.Booked) != 1 || !res.Booked[0].Placeholder || res.Booked[0].Lines != 4 {
		t.Fatalf("placeholder booking: %+v", res)
	}
	p := PlaceholderProfile
	for acct, want := range map[string]decimal.Decimal{p.PartsExpense: dd("100"), p.PartsOffset: dd("-100"), p.LaborExpense: dd("60"), p.LaborOffset: dd("-60")} {
		if got := f.net(acct); !got.Equal(want) {
			t.Errorf("account %s nets %s, want %s", acct, got, want)
		}
	}
	e := f.fake.Entries()[0]
	if e.PostingDate != "2026-10-20" || e.DocumentID == "" || e.SourceType != "work-order" {
		t.Fatalf("entry %+v", e)
	}

	if _, _, err := f.s.SavePostingProfile(f.ctx, PostingProfile{LegalEntityID: glLegalEntity, WorkOrderType: domain.TypeInspection, PartsExpense: "6201", LaborExpense: "6211", PartsOffset: "1401", LaborOffset: "5901"}, 0, "admin"); err != nil {
		t.Fatal(err)
	}
	f.order(domain.TypeInspection, "10.00", "", "")
	res = f.post()
	if len(res.Booked) != 1 || res.Booked[0].Placeholder || !f.net("6201").Equal(dd("10")) || !f.net("1401").Equal(dd("-10")) {
		t.Fatalf("typed profile booking: %+v", res)
	}
}

func TestGLPosting_ARefusedEntryStaysUnbookedAndRetriesToTheSameKey(t *testing.T) {
	f := newGLFixture(t, 7102)
	f.order(domain.TypeInspection, "50.00", "", "")
	f.fake.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	res := f.post()
	if len(res.Booked) != 0 || len(res.Failed) != 1 || res.Failed[0].Code != ledgerclient.CodePeriodClosed {
		t.Fatalf("refused: %+v", res)
	}
	rec, err := f.s.ReconcileGL(f.ctx, f.fake, GLReconcileRequest{LegalEntityID: glLegalEntity, LedgerID: f.ledger, Currency: "USD"})
	if err != nil || rec.Reconciled || rec.Unbooked.Count != 1 || len(rec.Accounts) != 0 {
		t.Fatalf("the refused order is reported as unbooked: %+v %v", rec, err)
	}
	f.fake.FailPosts(nil)
	if res = f.post(); len(res.Booked) != 1 || len(res.Failed) != 0 || len(f.fake.Entries()) != 1 {
		t.Fatalf("retry: %+v", res)
	}
	if res = f.post(); len(res.Booked) != 0 {
		t.Fatalf("a booked order is not booked again: %+v", res)
	}
}

func TestGLPosting_ACostChangedAfterBookingIsBookedAsADelta(t *testing.T) {
	f := newGLFixture(t, 7103)
	wo := f.order(domain.TypeInspection, "100.00", "", "")
	f.post()
	p := PlaceholderProfile
	f.setParts(wo.ID, "130.50")
	if res := f.post(); len(res.Booked) != 1 || res.Booked[0].Lines != 2 || !f.net(p.PartsExpense).Equal(dd("130.50")) {
		t.Fatalf("increase: %+v expense %s", res, f.net(p.PartsExpense))
	}
	f.setParts(wo.ID, "90.00")
	if res := f.post(); len(res.Booked) != 1 || !f.net(p.PartsExpense).Equal(dd("90")) || !f.net(p.PartsOffset).Equal(dd("-90")) {
		t.Fatalf("decrease: %+v expense %s", res, f.net(p.PartsExpense))
	}
	rec, err := f.s.ReconcileGL(f.ctx, f.fake, GLReconcileRequest{LegalEntityID: glLegalEntity, LedgerID: f.ledger, Currency: "USD"})
	if err != nil || !rec.Reconciled || len(rec.Accounts) != 2 || !rec.Accounts[0].Subledger.Abs().Equal(dd("90")) {
		t.Fatalf("reconciliation: %+v %v", rec, err)
	}
	if n := len(f.fake.Entries()); n != 3 {
		t.Fatalf("entries %d, want 3", n)
	}
}

func TestGLPosting_SubCentCostIsBookedRoundedAndOnlyOnce(t *testing.T) {
	f := newGLFixture(t, 7104)
	f.order(domain.TypeInspection, "10.004", "", "")
	if res := f.post(); len(res.Booked) != 1 || !f.net(PlaceholderProfile.PartsExpense).Equal(dd("10.00")) {
		t.Fatalf("rounded booking: %+v", res)
	}
	if res := f.post(); len(res.Booked) != 0 || len(f.fake.Entries()) != 1 {
		t.Fatalf("rebooked: %+v", res)
	}
}

func TestGLPosting_OnlyCompletedOrdersOfTheCurrencyAndSiteAreBooked(t *testing.T) {
	f := newGLFixture(t, 7105)
	open, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: domain.TypeInspection, Priority: 3, Currency: "USD"}, "gl-open")
	if err != nil {
		t.Fatal(err)
	}
	f.setParts(open.ID, "75.00")
	f.order(domain.TypeInspection, "20.00", "", "")
	if res := f.post(); len(res.Booked) != 1 || !f.net(PlaceholderProfile.PartsExpense).Equal(dd("20")) {
		t.Fatalf("open order must wait: %+v", res)
	}
	f.order(domain.TypeInspection, "5.00", "", "")
	other, err := f.s.PostWorkOrderCosts(f.ctx, f.fake, GLPostRequest{LegalEntityID: glLegalEntity, LedgerID: f.ledger, Currency: "USD", SiteID: ids.New()})
	if err != nil || len(other.Booked) != 0 {
		t.Fatalf("other site: %+v %v", other, err)
	}
	eur, err := f.s.PostWorkOrderCosts(f.ctx, f.fake, GLPostRequest{LegalEntityID: glLegalEntity, LedgerID: f.ledger, Currency: "EUR"})
	if err != nil || len(eur.Booked) != 0 {
		t.Fatalf("other currency: %+v %v", eur, err)
	}
}

func TestGLPosting_ProfileSaveNeedsIfMatchToReplace(t *testing.T) {
	f := newGLFixture(t, 7106)
	in := PostingProfile{LegalEntityID: glLegalEntity, WorkOrderType: "ALL", PartsExpense: "6300", LaborExpense: "6310", PartsOffset: "1400", LaborOffset: "5900"}
	saved, created, err := f.s.SavePostingProfile(f.ctx, in, 0, "admin")
	if err != nil || !created || saved.Version != 1 {
		t.Fatalf("create: %+v %v %v", saved, created, err)
	}
	for _, c := range []struct {
		ifMatch int
		status  int
	}{{0, http.StatusPreconditionRequired}, {7, http.StatusPreconditionFailed}} {
		if _, _, err := f.s.SavePostingProfile(f.ctx, in, c.ifMatch, "admin"); err == nil {
			t.Fatalf("If-Match %d must be refused", c.ifMatch)
		}
	}
	in.PartsExpense = "6301"
	if saved, created, err = f.s.SavePostingProfile(f.ctx, in, 1, "admin"); err != nil || created || saved.Version != 2 || saved.PartsExpense != "6301" {
		t.Fatalf("replace: %+v %v %v", saved, created, err)
	}
	list, err := f.s.ListPostingProfiles(f.ctx, glLegalEntity)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if other, err := f.s.ListPostingProfiles(f.ctx, ids.New()); err != nil || len(other) != 0 {
		t.Fatalf("other entity: %+v %v", other, err)
	}
}

func TestGLPosting_AppRoleBooksWithoutBypassingRLSOrRewritingTheSubledger(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	f := &glFixture{t: t, pool: app, ledger: ids.New(), fake: ledgerclient.NewFake()}
	f.s = NewWithClock(app, func() time.Time { return time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC) })
	f.ctx = testkit.Ctx(testkit.TenantID(7107), "EamPlanner", "EamTechnician")
	a, err := f.s.CreateAsset(f.ctx, Asset{Name: "Pump", AssetType: "PUMP", SiteID: testkit.TenantID(9), Criticality: 3}, "gl-app-asset")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ActivateAsset(f.ctx, a.ID, a.Version); err != nil {
		t.Fatal(err)
	}
	f.asset = a.ID
	f.order(domain.TypeInspection, "42.00", "1", "10.00")
	if res := f.post(); len(res.Booked) != 1 || len(res.Failed) != 0 {
		t.Fatalf("booking as passage_app: %+v", res)
	}
	if _, _, err := f.s.SavePostingProfile(f.ctx, PostingProfile{LegalEntityID: glLegalEntity, WorkOrderType: "ALL", PartsExpense: "6", LaborExpense: "7", PartsOffset: "8", LaborOffset: "9"}, 0, "admin"); err != nil {
		t.Fatalf("profile as passage_app: %v", err)
	}
	for _, stmt := range []string{`update eam_gl_posting set amount_dr = amount_dr`, `delete from eam_gl_posting`} {
		err := dbx.WithTenantTx(f.ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(f.ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
	other := testkit.Ctx(testkit.TenantID(7108), "EamPlanner")
	rec, err := f.s.ReconcileGL(other, f.fake, GLReconcileRequest{LegalEntityID: glLegalEntity, LedgerID: f.ledger, Currency: "USD"})
	if err != nil || len(rec.Accounts) != 0 {
		t.Fatalf("another tenant must see no subledger rows: %+v %v", rec, err)
	}
}

func TestGLPosting_OrdersTakeTheLegalEntityOfTheirAssetAndOnlyThatEntityBooksThem(t *testing.T) {
	f := newGLFixture(t, 7109)
	f.order(domain.TypeInspection, "20.00", "", "")
	other := "00000000-0000-0000-0000-0000000000e2"
	otherCtx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(7109), LegalEntityID: other, UserID: "u2", Duties: map[string]struct{}{"EamPlanner": {}}})
	a2, err := f.s.CreateAsset(otherCtx, Asset{Name: "Fan", AssetType: "FAN", SiteID: testkit.TenantID(9), Criticality: 3}, "gl-asset-e2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ActivateAsset(otherCtx, a2.ID, a2.Version); err != nil {
		t.Fatal(err)
	}
	g := &glFixture{t: t, pool: f.pool, s: f.s, ctx: otherCtx, asset: a2.ID, ledger: f.ledger, fake: f.fake, n: 50}
	g.order(domain.TypeInspection, "999.00", "", "")
	if res := f.post(); len(res.Booked) != 1 || !f.net(PlaceholderProfile.PartsExpense).Equal(dd("20")) {
		t.Fatalf("entity e1 must book only its own order: %+v", res)
	}
	res, err := f.s.PostWorkOrderCosts(otherCtx, f.fake, GLPostRequest{LegalEntityID: other, LedgerID: f.ledger, Currency: "USD"})
	if err != nil || len(res.Booked) != 1 || !f.net(PlaceholderProfile.PartsExpense).Equal(dd("1019")) {
		t.Fatalf("entity e2 books its own order: %+v %v", res, err)
	}
}

func TestGLPosting_AnAssetWithoutALegalEntityCannotTakeWorkOrders(t *testing.T) {
	f := newGLFixture(t, 7110)
	unscoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(7110), UserID: "u3", Duties: map[string]struct{}{"EamPlanner": {}}})
	a, err := f.s.CreateAsset(unscoped, Asset{Name: "Press", AssetType: "PRESS", SiteID: testkit.TenantID(9), Criticality: 3}, "gl-asset-none")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ActivateAsset(unscoped, a.ID, a.Version); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CreateWorkOrder(unscoped, WorkOrder{AssetID: a.ID, Type: domain.TypeInspection, Priority: 3, Currency: "USD"}, "gl-no-le")
	var prob *httpx.Problem
	if !errors.As(err, &prob) || prob.Code != CodeNoEntity || prob.Status != http.StatusUnprocessableEntity {
		t.Fatalf("err %v", err)
	}
}

func TestGLPosting_ReconciliationNamesUnbookedAndEntitylessOrders(t *testing.T) {
	f := newGLFixture(t, 7111)
	booked := f.order(domain.TypeInspection, "20.00", "", "")
	f.post()
	late := f.order(domain.TypeInspection, "30.00", "", "")
	orphan := f.order(domain.TypeInspection, "40.00", "", "")
	if err := dbx.WithTenantTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(f.ctx, `update work_order set legal_entity_id = null where id = $1`, orphan.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := f.s.ReconcileGL(f.ctx, f.fake, GLReconcileRequest{LegalEntityID: glLegalEntity, LedgerID: f.ledger, Currency: "USD"})
	if err != nil || rec.Reconciled || rec.Unbooked.Count != 1 || rec.Unbooked.Numbers[0] != late.Number || rec.NoEntity.Count != 1 || rec.NoEntity.Numbers[0] != orphan.Number {
		t.Fatalf("%+v %v (booked %s)", rec, err, booked.Number)
	}
	if res := f.post(); len(res.Booked) != 1 {
		t.Fatalf("only the order with an entity is booked: %+v", res)
	}
	if rec, err = f.s.ReconcileGL(f.ctx, f.fake, GLReconcileRequest{LegalEntityID: glLegalEntity, LedgerID: f.ledger, Currency: "USD"}); err != nil || rec.Unbooked.Count != 0 || rec.Reconciled || rec.NoEntity.Count != 1 {
		t.Fatalf("an order without an entity keeps the reconciliation open: %+v %v", rec, err)
	}
}

type recordingLedger struct {
	ledgerclient.Ledger
	posts []ledgerclient.Entry
	err   error
}

func (r *recordingLedger) Post(ctx context.Context, e ledgerclient.Entry) (string, error) {
	r.posts = append(r.posts, e)
	if r.err != nil {
		return "", r.err
	}
	return r.Ledger.Post(ctx, e)
}

func TestGLPosting_ARetryResendsTheStoredPayloadEvenAfterTheProfileChanged(t *testing.T) {
	f := newGLFixture(t, 7112)
	wo := f.order(domain.TypeInspection, "50.00", "", "")
	rl := &recordingLedger{Ledger: f.fake, err: &ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable}}
	req := GLPostRequest{LegalEntityID: glLegalEntity, LedgerID: f.ledger, Currency: "USD"}
	if res, err := f.s.PostWorkOrderCosts(f.ctx, rl, req); err != nil || len(res.Failed) != 1 {
		t.Fatalf("outage: %+v %v", res, err)
	}
	var pending int
	if err := dbx.WithTenantTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `select count(*) from eam_pending_booking where work_order_id = $1`, wo.ID).Scan(&pending)
	}); err != nil || pending != 1 {
		t.Fatalf("the booking must be stored before the ledger answers: %d %v", pending, err)
	}
	if _, _, err := f.s.SavePostingProfile(f.ctx, PostingProfile{LegalEntityID: glLegalEntity, WorkOrderType: "ALL", PartsExpense: "6999", LaborExpense: "6998", PartsOffset: "1999", LaborOffset: "5999"}, 0, "admin"); err != nil {
		t.Fatal(err)
	}
	f.setParts(wo.ID, "75.00")
	rl.err = nil
	res, err := f.s.PostWorkOrderCosts(f.ctx, rl, req)
	if err != nil || len(res.Booked) != 1 || len(rl.posts) != 2 {
		t.Fatalf("retry: %+v %v posts %d", res, err, len(rl.posts))
	}
	first, second := rl.posts[0], rl.posts[1]
	if first.IdempotencyKey != second.IdempotencyKey || first.PostingDate != second.PostingDate || len(second.Lines) != 2 || second.Lines[0].Account != PlaceholderProfile.PartsExpense || !second.Lines[0].Amount.Equal(dd("50")) {
		t.Fatalf("the retry must resend the first payload: %+v vs %+v", first, second)
	}
	if res = f.post(); len(res.Booked) != 1 || !f.net("6999").Equal(dd("25")) || !f.net(PlaceholderProfile.PartsExpense).Equal(dd("50")) {
		t.Fatalf("the later cost change is booked as a delta on the new profile: %+v", res)
	}
}

func TestGLPosting_ADefinitiveRejectionDropsThePendingBookingSoItIsRebuilt(t *testing.T) {
	f := newGLFixture(t, 7113)
	f.order(domain.TypeInspection, "50.00", "", "")
	rl := &recordingLedger{Ledger: f.fake, err: &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "LEDGER_ACCOUNT_UNKNOWN"}}
	req := GLPostRequest{LegalEntityID: glLegalEntity, LedgerID: f.ledger, Currency: "USD"}
	if res, err := f.s.PostWorkOrderCosts(f.ctx, rl, req); err != nil || len(res.Failed) != 1 || res.Failed[0].Code != "LEDGER_ACCOUNT_UNKNOWN" {
		t.Fatalf("rejection: %+v %v", res, err)
	}
	if _, _, err := f.s.SavePostingProfile(f.ctx, PostingProfile{LegalEntityID: glLegalEntity, WorkOrderType: "ALL", PartsExpense: "6100", LaborExpense: "6110", PartsOffset: "1400", LaborOffset: "5900"}, 0, "admin"); err != nil {
		t.Fatal(err)
	}
	rl.err = nil
	if res, err := f.s.PostWorkOrderCosts(f.ctx, rl, req); err != nil || len(res.Booked) != 1 || !f.net("6100").Equal(dd("50")) {
		t.Fatalf("rebuilt from the corrected profile: %+v %v", res, err)
	}
}
