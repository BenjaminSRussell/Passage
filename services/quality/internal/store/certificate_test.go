package store

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
)

// pendingOrderRequiring creates a one-test order that demands certRequired and
// records its passing result, so it waits for its usage decision.
func pendingOrderRequiring(t *testing.T, s *Postgres, ctx context.Context, certRequired string, lot int64) (domain.Order, domain.TestGroup) {
	t.Helper()
	outcome := ids.New()
	group, _, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "GRP-CERT", Tests: []domain.TestLine{
		{TestID: ids.New(), Name: "tensile", Kind: domain.KindAttribute, Required: true, PassingOutcomes: []string{outcome}},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	order := domain.Order{ID: ids.New(), Trigger: domain.TriggerPurchaseReceipt, SourceRef: domain.SourceRef{Type: "receipt_line", ID: ids.New()},
		ItemID: ids.New(), SiteID: ids.New(), WarehouseID: ids.New(), Dims: map[string]string{"batch": "B-1"}, LotQty: decimal.NewFromInt(lot),
		SampleQty: decimal.NewFromInt(1), UOM: "KG", TestGroupID: group.ID, TestGroupVersion: 1, SamplingMethod: domain.MethodFixed,
		Severity: domain.SeverityNormal, AcceptNo: 0, RejectNo: 1, CertificateRequired: certRequired}
	created, _, err := s.CreateOrder(ctx, order, "")
	if err != nil {
		t.Fatal(err)
	}
	if created.CertificateRequired != certRequired {
		t.Fatalf("the order must keep its certificate requirement: %q", created.CertificateRequired)
	}
	batch := []domain.Result{{TestID: group.Tests[0].TestID, SampleNo: 1, OutcomeID: outcome, Passed: true, RecordedBy: "qa-1"}}
	pending, _, err := s.RecordResults(ctx, created.ID, 1, domain.StatusPending, batch, "")
	if err != nil {
		t.Fatal(err)
	}
	return pending, group
}

func accept(lot int64) domain.Decision {
	lines := []domain.DecisionLine{{Decision: domain.DecisionAccept, Quantity: decimal.NewFromInt(lot)}}
	split, _ := domain.SplitDecision(lines, decimal.NewFromInt(lot))
	return domain.Decision{Lines: lines, Split: split, DecidedBy: "qa-2"}
}

// TestPostgres_CertificateGateAC3312 is AC-33.12 on the store: an order that
// demands an EN 10204 3.1 certificate refuses ACCEPT with QM-0125 inside the
// decision's transaction until an in-specification certificate is recorded. An
// out-of-specification certificate raises its VENDOR NCR and event in the same
// transaction; the certificate key replays and is refused on another order.
func TestPostgres_CertificateGateAC3312(t *testing.T) {
	s, pool, ctx := setup(t)
	order, _ := pendingOrderRequiring(t, s, ctx, domain.CertEN10204_31, 40)

	var de *domain.Error
	if _, _, err := s.PostUsageDecision(ctx, order.ID, order.Version, accept(40), "d-1"); !errors.As(err, &de) || de.Code != domain.CodeCertRequired {
		t.Fatalf("ACCEPT without the certificate must fail with QM-0125, got %v", err)
	}
	if cur, _ := s.GetOrder(ctx, order.ID); cur.Status != domain.StatusPending || cur.Version != order.Version {
		t.Fatalf("a refused decision must leave the order untouched: %+v", cur)
	}

	now := time.Now().UTC()
	nok := domain.CertRecord{ID: ids.New(), OrderID: order.ID, Type: domain.CertEN10204_31, DocumentRef: "EDI-4711",
		Values: map[string]domain.CertValue{}, Deviations: []string{"tensile"}, RecordedBy: "qa-1", RecordedAt: now}
	ncr, err := domain.CertificateNCR(order, nok, now)
	if err != nil {
		t.Fatal(err)
	}
	saved, inserted, err := s.RecordCertificate(ctx, nok, &ncr, "c-1")
	if err != nil || !inserted || saved.NonconformanceID == "" || saved.InSpec || saved.DocumentRef != "EDI-4711" || len(saved.Deviations) != 1 {
		t.Fatalf("NOK certificate: %+v %v", saved, err)
	}
	raised, err := s.GetNonconformance(ctx, saved.NonconformanceID)
	if err != nil || raised.Source != domain.SourceVendor || raised.ProblemType != domain.ProblemCertificateNOK || raised.QualityOrderID != order.ID ||
		!raised.Qty.Equal(decimal.NewFromInt(40)) || raised.UOM != "KG" {
		t.Fatalf("the NOK certificate's NCR: %+v %v", raised, err)
	}
	if types := outboxTypes(t, pool, raised.ID); len(types) != 1 || types[0] != "dev.passage.quality.nonconformance.created.v1" {
		t.Fatalf("the NCR's event goes out with the certificate: %v", types)
	}
	if again, inserted, err := s.RecordCertificate(ctx, nok, &ncr, "c-1"); err != nil || inserted || again.ID != saved.ID {
		t.Fatalf("a replayed key returns the stored record: %+v %v %v", again, inserted, err)
	}
	if list, _ := s.ListNonconformances(ctx, NCRFilter{QualityOrderID: order.ID, Limit: 10}); len(list) != 1 {
		t.Fatalf("a replay must not raise a second NCR: %d", len(list))
	}
	if _, _, err := s.PostUsageDecision(ctx, order.ID, order.Version, accept(40), "d-2"); !errors.As(err, &de) || de.Code != domain.CodeCertRequired {
		t.Fatalf("a NOK certificate keeps ACCEPT blocked, got %v", err)
	}

	ok := domain.CertRecord{ID: ids.New(), OrderID: order.ID, Type: domain.CertEN10204_31, InSpec: true, RecordedBy: "qa-1", RecordedAt: now.Add(time.Second),
		Values: map[string]domain.CertValue{}}
	if _, _, err := s.RecordCertificate(ctx, ok, nil, "c-2"); err != nil {
		t.Fatal(err)
	}
	recs, err := s.ListCertificates(ctx, order.ID)
	if err != nil || len(recs) != 2 || recs[0].ID != saved.ID || domain.CertificateStatusOf(order.CertificateRequired, recs) != domain.CertReceivedOK {
		t.Fatalf("certificates oldest first, latest decides: %+v %v", recs, err)
	}
	if theirs, _ := s.ListCertificates(testkit.Ctx(testkit.TenantID(2), "QM_VIEW"), order.ID); len(theirs) != 0 {
		t.Fatalf("another tenant must not see the certificates: %+v", theirs)
	}
	closed, _, err := s.PostUsageDecision(ctx, order.ID, order.Version, accept(40), "d-3")
	if err != nil || closed.Status != domain.StatusClosed {
		t.Fatalf("ACCEPT after an in-specification certificate: %+v %v", closed, err)
	}

	late := domain.CertRecord{ID: ids.New(), OrderID: order.ID, Type: domain.CertEN10204_31, InSpec: true, RecordedBy: "qa-1", RecordedAt: now}
	if _, _, err := s.RecordCertificate(ctx, late, nil, "c-3"); !errors.As(err, &de) || de.Code != domain.CodeClosed {
		t.Fatalf("a closed order takes no certificate, got %v", err)
	}
	other, _ := pendingOrderRequiring(t, s, ctx, domain.CertNone, 5)
	if _, _, err := s.RecordCertificate(ctx, domain.CertRecord{ID: ids.New(), OrderID: other.ID, Type: domain.CertCoC, InSpec: true,
		RecordedBy: "qa-1", RecordedAt: now}, nil, "c-1"); problemStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a key from another order's certificate must be refused, got %v", err)
	}
}

// TestPostgres_CertificateWaiver covers the waiver leg of BR-33.13: a waiver of
// the required type opens the gate; an order without a requirement has nothing
// to waive (QM-0180); a decision that releases nothing is not gated.
func TestPostgres_CertificateWaiver(t *testing.T) {
	s, _, ctx := setup(t)
	order, _ := pendingOrderRequiring(t, s, ctx, domain.CertCoA, 10)
	waiver := domain.CertRecord{ID: ids.New(), OrderID: order.ID, Type: domain.CertCoA, Waiver: true, Reason: "supplier audit on file",
		RecordedBy: "qa-2", RecordedAt: time.Now().UTC()}
	if _, _, err := s.RecordCertificate(ctx, waiver, nil, "w-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PostUsageDecision(ctx, order.ID, order.Version, accept(10), "d-w"); err != nil {
		t.Fatalf("ACCEPT after a waiver: %v", err)
	}

	none, _ := pendingOrderRequiring(t, s, ctx, domain.CertNone, 10)
	var de *domain.Error
	waiver.ID, waiver.OrderID = ids.New(), none.ID
	if _, _, err := s.RecordCertificate(ctx, waiver, nil, "w-2"); !errors.As(err, &de) || de.Code != domain.CodeCertNotRequired {
		t.Fatalf("waiving on an order without a requirement must fail with QM-0180, got %v", err)
	}

	rejectOnly, _ := pendingOrderRequiring(t, s, ctx, domain.CertCoA, 10)
	lines := []domain.DecisionLine{{Decision: domain.DecisionReject, Quantity: decimal.NewFromInt(10)}}
	split, _ := domain.SplitDecision(lines, decimal.NewFromInt(10))
	if _, d, err := s.PostUsageDecision(ctx, rejectOnly.ID, rejectOnly.Version, domain.Decision{Lines: lines, Split: split, DecidedBy: "qa-2"}, "d-r"); err != nil || d.NonconformanceID == "" {
		t.Fatalf("a full reject is not gated by the certificate: %+v %v", d, err)
	}
}

// TestPostgres_PhysicalSamples covers FR-33.20 on the store: a retention sample's
// expiry, pooled samples naming primary samples of the same order (QM-0181), the
// lot limit for drawn samples (QM-0182), key replay and the due-for-disposal list.
func TestPostgres_PhysicalSamples(t *testing.T) {
	s, _, ctx := setup(t)
	order, _ := pendingOrderRequiring(t, s, ctx, domain.CertNone, 10)
	now := time.Now().UTC()
	newSample := func(in domain.SampleInput) domain.Sample {
		t.Helper()
		in.QualityOrderID = order.ID
		smp, err := domain.NewSample(ids.New(), in, order.UOM, "qa-1", now)
		if err != nil {
			t.Fatal(err)
		}
		return smp
	}
	primary, inserted, err := s.RegisterSample(ctx, newSample(domain.SampleInput{Kind: domain.SamplePrimary, Quantity: decimal.RequireFromString("2.5"),
		Container: "jar 250 ml"}), "s-1")
	if err != nil || !inserted || primary.UOM != "KG" || primary.BatchExpiresOn != "" || primary.RetentionExpiresOn != "" || len(primary.PrimaryIDs) != 0 {
		t.Fatalf("primary sample: %+v %v", primary, err)
	}
	const expired = "2020-01-15"
	reserve, _, err := s.RegisterSample(ctx, newSample(domain.SampleInput{Kind: domain.SampleReserve, Quantity: decimal.NewFromInt(1),
		BatchExpiresOn: expired, StorageLocation: "RET-01"}), "s-2")
	if err != nil || reserve.BatchExpiresOn != expired || reserve.RetentionExpiresOn != "2021-01-15" || reserve.RetentionYears != 1 {
		t.Fatalf("retention sample, one year past batch expiry: %+v %v", reserve, err)
	}
	if again, inserted, err := s.RegisterSample(ctx, newSample(domain.SampleInput{Kind: domain.SampleReserve, Quantity: decimal.NewFromInt(1),
		BatchExpiresOn: expired}), "s-2"); err != nil || inserted || again.ID != reserve.ID {
		t.Fatalf("a replayed key returns the stored sample: %+v %v %v", again, inserted, err)
	}
	pooled, _, err := s.RegisterSample(ctx, newSample(domain.SampleInput{Kind: domain.SamplePooled, PrimaryIDs: []string{primary.ID},
		Quantity: decimal.RequireFromString("2.5")}), "s-3")
	if err != nil || len(pooled.PrimaryIDs) != 1 || pooled.PrimaryIDs[0] != primary.ID {
		t.Fatalf("pooled sample: %+v %v", pooled, err)
	}
	if _, _, err := s.RegisterSample(ctx, newSample(domain.SampleInput{Kind: domain.SamplePooled, PrimaryIDs: []string{reserve.ID},
		Quantity: decimal.NewFromInt(1)}), "s-4"); problemStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a pooled sample of a reserve sample must be 422 QM-0181, got %v", err)
	}
	var de *domain.Error
	if _, _, err := s.RegisterSample(ctx, newSample(domain.SampleInput{Kind: domain.SamplePrimary, Quantity: decimal.RequireFromString("6.6")}), "s-5"); !errors.As(err, &de) || de.Code != domain.CodeSampleOverLot {
		t.Fatalf("2.5 + 1 + 6.6 drawn from a lot of 10 must fail with QM-0182, got %v", err)
	}

	asOf := "2021-01-15"
	due, err := s.ListSamples(ctx, SampleFilter{Status: domain.SampleDueForDisposal, AsOf: asOf, Limit: 10})
	if err != nil || len(due) != 1 || due[0].ID != reserve.ID {
		t.Fatalf("due for disposal on its expiry day (AC-33.18): %+v %v", due, err)
	}
	active, _ := s.ListSamples(ctx, SampleFilter{QualityOrderID: order.ID, Status: domain.SampleActive, AsOf: asOf, Limit: 10})
	if len(active) != 2 {
		t.Fatalf("primary and pooled are active: %+v", active)
	}
	if notYet, _ := s.ListSamples(ctx, SampleFilter{Status: domain.SampleDueForDisposal, AsOf: "2021-01-14", Limit: 10}); len(notYet) != 0 {
		t.Fatalf("not due the day before its expiry: %+v", notYet)
	}
	if theirs, _ := s.ListSamples(testkit.Ctx(testkit.TenantID(2), "QM_VIEW"), SampleFilter{Limit: 10}); len(theirs) != 0 {
		t.Fatalf("another tenant must not see the samples: %+v", theirs)
	}
}
