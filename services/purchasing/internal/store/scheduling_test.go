package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// weeklyAgreement is the AC-17.11 agreement: 100 EA weekly for 12 weeks from
// 2027-01-04 under the default profile (firm zone 14 days).
func weeklyAgreement(t *testing.T) domain.SchedulingAgreement {
	start := day(t, "2027-01-04")
	sa := domain.SchedulingAgreement{
		LegalEntityID: legalEntity, VendorID: ids.New(), ProductID: ids.New(), SiteID: ids.New(),
		Currency: "USD", Unit: "EA", UnitPrice: dec("2.50"), TargetQty: dec("1200"), ValidFrom: start,
		Profile: domain.DefaultReleaseProfile(),
	}
	for i := 0; i < 12; i++ {
		sa.Lines = append(sa.Lines, domain.ScheduleLine{DeliveryDate: domain.Day{Time: start.AddDate(0, 0, 7*i)}, Qty: dec("100")})
	}
	return sa
}

// keepAll is a schedule replacement that leaves every line as it is.
func keepAll(sa domain.SchedulingAgreement) []domain.ScheduleChange {
	out := make([]domain.ScheduleChange, 0, len(sa.Lines))
	for _, l := range sa.Lines {
		out = append(out, domain.ScheduleChange{LineNo: l.LineNo, DeliveryDate: l.DeliveryDate, Qty: l.Qty})
	}
	return out
}

// receive stands in for a wave 18 receipt against one schedule line.
func receive(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agreementID string, lineNo int, qty string) {
	t.Helper()
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update purchase_sa_schedule_line set received_qty = $3 where agreement_id = $1 and line_no = $2`,
			agreementID, lineNo, dec(qty))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// releaseEvents returns the sarelease.created.v1 payloads in the outbox for one agreement.
func releaseEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agreementID string) []purchasingevents.SAReleaseCreatedData {
	t.Helper()
	var out []purchasingevents.SAReleaseCreatedData
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select payload from outbox where aggregate_id = $1 and type = $2 order by created_at, id`,
			agreementID, purchasingevents.SAReleaseCreated)
		if err != nil {
			return err
		}
		payloads, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
		for _, raw := range payloads {
			var env struct {
				Data purchasingevents.SAReleaseCreatedData `json:"data"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				return err
			}
			out = append(out, env.Data)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// AC-17.11 against Postgres: a JIT release on day 0 shows cumulative released
// 1,200 and received 0 and writes sarelease.created.v1 in the same
// transaction; a repeat run on the unchanged schedule returns it; after 100 EA
// are received against week 1, release 2 on day 8 shows cumulative received
// 100; reducing week 2 to 50 needs a reason code and bumps the version; the
// monitor lists the agreement with its open quantity and next release due.
func TestSchedulingAgreement_AC17_11(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(70)

	created, err := s.CreateSchedulingAgreement(ctx, weeklyAgreement(t), store.Idem{Key: "sa-create", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	sa := created.Agreement
	if sa.Version != 1 || len(sa.Number) != 11 || sa.Number[:3] != "SA-" || len(sa.Lines) != 12 || sa.Lines[11].LineNo != 12 ||
		!sa.UnitPrice.Equal(dec("2.5")) || sa.Profile != domain.DefaultReleaseProfile() {
		t.Fatalf("created = %+v", sa)
	}
	replay, err := s.CreateSchedulingAgreement(ctx, weeklyAgreement(t), store.Idem{Key: "sa-create", Hash: "h"})
	if err != nil || replay.Agreement.ID != sa.ID {
		t.Fatalf("replay = %s, %v; want %s", replay.Agreement.ID, err, sa.ID)
	}
	if _, err := s.CreateSchedulingAgreement(ctx, weeklyAgreement(t), store.Idem{Key: "sa-create", Hash: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse = %v", err)
	}

	day0 := day(t, "2027-01-04")
	rel1, made, err := s.RunSchedulingRelease(ctx, sa.ID, domain.ReleaseJIT, day0, false, store.Idem{Key: "rel-1", Hash: "h"})
	if err != nil || !made {
		t.Fatalf("release 1: made %v, %v", made, err)
	}
	if rel1.ReleaseNo != 1 || rel1.Number != "REL-"+sa.Number+"-1" || !rel1.CumulativeReleased.Equal(dec("1200")) ||
		!rel1.CumulativeReceived.IsZero() || len(rel1.Lines) != 12 || rel1.AgreementVersion != 1 {
		t.Fatalf("release 1 = %+v", rel1)
	}
	again, made, err := s.RunSchedulingRelease(ctx, sa.ID, domain.ReleaseJIT, day0, false, store.Idem{Key: "rel-1b", Hash: "h"})
	if err != nil || made || again.ID != rel1.ID {
		t.Fatalf("repeat run on an unchanged schedule = %s made %v (%v), want %s", again.ID, made, err, rel1.ID)
	}
	if events := releaseEvents(t, ctx, pool, sa.ID); len(events) != 1 || events[0].CumulativeReleased != "1200" ||
		events[0].Kind != "JIT" || events[0].VendorID != sa.VendorID || len(events[0].Lines) != 12 || events[0].Lines[0].Zone != domain.ZoneFirm {
		t.Fatalf("outbox = %+v", events)
	}

	receive(t, ctx, pool, sa.ID, 1, "100")
	day8 := day(t, "2027-01-12")
	rel2, made, err := s.RunSchedulingRelease(ctx, sa.ID, domain.ReleaseJIT, day8, false, store.Idem{})
	if err != nil || !made {
		t.Fatalf("release 2: %v", err)
	}
	if rel2.ReleaseNo != 2 || !rel2.CumulativeReceived.Equal(dec("100")) || !rel2.CumulativeReleased.Equal(dec("1200")) {
		t.Fatalf("release 2 = %+v", rel2)
	}

	cur, err := s.GetSchedulingAgreement(ctx, sa.ID)
	if err != nil {
		t.Fatal(err)
	}
	cut := keepAll(cur.Agreement)
	cut[1].Qty = dec("50") // week 2, 2027-01-11: FIRM on day 8
	if _, _, err := s.ReplaceSchedule(ctx, sa.ID, cur.Agreement.Version, day8, cut); problemCode(err) != domain.CodeFirmZone {
		t.Fatalf("week 2 to 50 without a reason = %v, want %s", err, domain.CodeFirmZone)
	}
	if _, _, err := s.ReplaceSchedule(ctx, sa.ID, cur.Agreement.Version+1, day8, cut); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale If-Match = %v", err)
	}
	cut[1].Reason = "VENDOR_CAPACITY"
	cut[0].Qty = dec("90") // week 1 below its 100 received
	cut[0].Reason = "X"
	if _, _, err := s.ReplaceSchedule(ctx, sa.ID, cur.Agreement.Version, day8, cut); problemCode(err) != domain.CodeReceivedAboveRelease {
		t.Fatalf("week 1 below received = %v", err)
	}
	cut[0].Qty, cut[0].Reason = dec("100"), ""
	// Week 5 (2027-02-01, 20 days out) is TRADEOFF: removing it warns.
	cut = append(cut[:4], cut[5:]...)
	changed, warnings, err := s.ReplaceSchedule(ctx, sa.ID, cur.Agreement.Version, day8, cut)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Agreement.Version != 2 || len(changed.Agreement.Lines) != 11 || len(warnings) != 1 ||
		!changed.Agreement.Lines[1].Qty.Equal(dec("50")) || changed.Agreement.Lines[1].ReductionReason != "VENDOR_CAPACITY" ||
		!changed.Agreement.Lines[0].ReceivedQty.Equal(dec("100")) {
		t.Fatalf("changed = %+v, warnings %v", changed.Agreement, warnings)
	}

	// The schedule is now 1,050: a third release would lower cumulative released.
	if _, _, err := s.RunSchedulingRelease(ctx, sa.ID, domain.ReleaseJIT, day8, false, store.Idem{}); problemCode(err) != domain.CodeCumulativeDecrease {
		t.Fatalf("decrease without reset = %v, want %s", err, domain.CodeCumulativeDecrease)
	}
	rel3, made, err := s.RunSchedulingRelease(ctx, sa.ID, domain.ReleaseJIT, day8, true, store.Idem{})
	if err != nil || !made || rel3.ReleaseNo != 3 || !rel3.CumulativeReset || !rel3.CumulativeReleased.Equal(dec("1050")) || rel3.AgreementVersion != 2 {
		t.Fatalf("release 3 with reset = %+v, %v", rel3, err)
	}

	headers, err := s.ListSchedulingReleases(ctx, sa.ID)
	if err != nil || len(headers) != 3 || headers[2].Number != rel3.Number || headers[0].Lines != nil {
		t.Fatalf("releases = %+v, %v", headers, err)
	}
	doc, err := s.GetSchedulingRelease(ctx, sa.ID, 2)
	if err != nil || len(doc.Lines) != 12 || !doc.Lines[0].CumulativeReceived.Equal(dec("100")) || doc.Lines[0].Zone != domain.ZoneFirm {
		t.Fatalf("release 2 documentation = %+v, %v", doc, err)
	}
	if _, err := s.GetSchedulingRelease(ctx, sa.ID, 9); problemCode(err) != store.CodeSAReleaseNotFound {
		t.Fatalf("missing release = %v", err)
	}

	page, err := s.ListSchedulingAgreements(ctx, store.SchedulingAgreementFilter{VendorID: sa.VendorID}, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("monitor = %+v, %v", page, err)
	}
	m := page.Items[0]
	if !m.Scheduled.Equal(dec("1050")) || !m.Received.Equal(dec("100")) || !m.Open.Equal(dec("950")) || m.ReleaseCount != 3 ||
		m.LastReleaseNumber != rel3.Number || m.LastReleaseKind != "JIT" || m.LastRunDate.String() != "2027-01-12" ||
		m.NextReleaseDue.String() != "2027-01-19" || m.Version != 2 {
		t.Fatalf("monitor row = %+v", m)
	}
}

// Tenant and legal-entity isolation: another tenant sees nothing, and a caller
// scoped to another legal entity cannot read the agreement, its releases or
// release documentation.
func TestSchedulingAgreement_Isolation(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(71)
	created, err := s.CreateSchedulingAgreement(ctx, weeklyAgreement(t), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Agreement.ID
	if _, _, err := s.RunSchedulingRelease(ctx, id, domain.ReleaseFRC, day(t, "2027-01-04"), false, store.Idem{}); err != nil {
		t.Fatal(err)
	}

	otherTenant := principalContext(72)
	otherEntity := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(71), UserID: ids.New(), LegalEntityID: ids.New()})
	for name, c := range map[string]context.Context{"other tenant": otherTenant, "other legal entity": otherEntity} {
		if _, err := s.GetSchedulingAgreement(c, id); problemCode(err) != store.CodeSchedulingAgreementNotFound {
			t.Errorf("%s: get = %v", name, err)
		}
		if _, err := s.ListSchedulingReleases(c, id); problemCode(err) != store.CodeSchedulingAgreementNotFound {
			t.Errorf("%s: releases = %v", name, err)
		}
		if _, err := s.GetSchedulingRelease(c, id, 1); problemCode(err) != store.CodeSchedulingAgreementNotFound {
			t.Errorf("%s: release 1 = %v", name, err)
		}
		if _, _, err := s.RunSchedulingRelease(c, id, domain.ReleaseJIT, day(t, "2027-01-05"), false, store.Idem{}); problemCode(err) != store.CodeSchedulingAgreementNotFound {
			t.Errorf("%s: run = %v", name, err)
		}
		if page, err := s.ListSchedulingAgreements(c, store.SchedulingAgreementFilter{}, "", 50); err != nil || len(page.Items) != 0 {
			t.Errorf("%s: monitor = %+v, %v", name, page.Items, err)
		}
	}
	// A schedule line without a quantity is refused before any write.
	if _, err := s.CreateSchedulingAgreement(ctx, func() domain.SchedulingAgreement {
		sa := weeklyAgreement(t)
		sa.Lines[0].Qty = decimal.Zero
		return sa
	}(), store.Idem{}); problemCode(err) != domain.CodeValidation {
		t.Errorf("zero quantity line = %v", err)
	}
}
