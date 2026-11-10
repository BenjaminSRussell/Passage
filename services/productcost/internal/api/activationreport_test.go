package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/inventory"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

func reportActivation(qty string) *store.Activation {
	dec := decimal.RequireFromString
	line := domain.RevalPreviewLine{
		RevalLine: domain.RevalLine{ItemID: itemOne, SiteID: siteOne, Qty: dec(qty), OldStd: dec("1.00"), NewStd: dec("1.50")},
		Amount:    dec(qty).Mul(dec("0.50")),
	}
	return &store.Activation{
		ID: someID, LegalEntityID: rollLE, PostingDate: "2026-01-02", Currency: "USD", Total: line.Amount, Lines: []domain.RevalPreviewLine{line},
		JournalLines: []store.PostedLine{{Account: "1400", Amount: line.Amount}, {Account: "5900", Credit: true, Amount: line.Amount}},
	}
}

func reportPath(id string) string {
	return "/v1/product-costing/activations/" + id + "/revaluation-report"
}

func decodeReport(t *testing.T, body []byte) revaluationReport {
	t.Helper()
	var out revaluationReport
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRevaluationReport_TiesLinesToTheJournalAndToTheInventoryOnHand(t *testing.T) {
	f := &fakeStore{report: reportActivation("1000")}
	oh := onHandOf("1000")
	h, _ := activationRouter(f, nil, oh)
	rec := call{http.MethodGet, reportPath(someID), "", view, "", rollLE}.do(h)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got := decodeReport(t, rec.Body.Bytes())
	if !got.TiedToJournal || !*got.TiedToOnHand || got.LinesTotal.String() != "500" || got.Lines[0].OnHand.String() != "1000" || !*got.Lines[0].Tied {
		t.Fatalf("report %+v", got)
	}
	if oh.legal != rollLE || oh.asOf != "2026-01-02" || len(oh.asked) != 1 || oh.asked[0].ItemID != itemOne || f.reportID != someID {
		t.Fatalf("on-hand read for %q as of %q positions %v activation %q", oh.legal, oh.asOf, oh.asked, f.reportID)
	}
}

func TestRevaluationReport_FlagsAQuantityTheInventoryNoLongerHolds(t *testing.T) {
	f := &fakeStore{report: reportActivation("1000")}
	h, _ := activationRouter(f, nil, onHandOf("900"))
	rec := call{http.MethodGet, reportPath(someID), "", view, "", rollLE}.do(h)
	got := decodeReport(t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || *got.TiedToOnHand || *got.Lines[0].Tied || got.Lines[0].Difference.String() != "-100" || !got.TiedToJournal {
		t.Fatalf("%d report %+v", rec.Code, got)
	}
	empty := &fakeOnHand{positions: map[inventory.Position]decimal.Decimal{}}
	h, _ = activationRouter(f, nil, empty)
	got = decodeReport(t, call{http.MethodGet, reportPath(someID), "", view, "", rollLE}.do(h).Body.Bytes())
	if *got.TiedToOnHand || got.Lines[0].OnHand.String() != "0" {
		t.Fatalf("a position inventory has no row for is zero on hand: %+v", got)
	}
}

func TestRevaluationReport_NotTiedToTheJournalWhenTheStoredAmountsDisagree(t *testing.T) {
	act := reportActivation("1000")
	act.JournalLines[1].Amount = decimal.RequireFromString("499.99")
	h, _ := activationRouter(&fakeStore{report: act}, nil, onHandOf("1000"))
	got := decodeReport(t, call{http.MethodGet, reportPath(someID), "", view, "", rollLE}.do(h).Body.Bytes())
	if got.TiedToJournal || !*got.TiedToOnHand {
		t.Fatalf("%+v", got)
	}
	act = reportActivation("1000")
	act.Total = decimal.RequireFromString("501")
	h, _ = activationRouter(&fakeStore{report: act}, nil, onHandOf("1000"))
	if got = decodeReport(t, call{http.MethodGet, reportPath(someID), "", view, "", rollLE}.do(h).Body.Bytes()); got.TiedToJournal {
		t.Fatalf("a total that is not the sum of the lines: %+v", got)
	}
}

func TestRevaluationReport_RefusesAnActivationOfAnotherLegalEntityWithoutCallingInventory(t *testing.T) {
	for name, scope := range map[string]string{"other entity": ids.New(), "no entity": ""} {
		oh := onHandOf("1000")
		h, _ := activationRouter(&fakeStore{report: reportActivation("1000")}, nil, oh)
		rec := call{http.MethodGet, reportPath(someID), "", view, "", scope}.do(h)
		if code, _ := problemCode(t, rec); rec.Code != http.StatusForbidden || code != domain.CodeLegalEntityScope || oh.calls != 0 {
			t.Errorf("%s: %d %s inventory calls %d", name, rec.Code, rec.Body.String(), oh.calls)
		}
	}
	oh := onHandOf("1000")
	h, _ := activationRouter(&fakeStore{report: reportActivation("1000")}, nil, oh)
	if rec := (call{http.MethodGet, reportPath(someID), "", view, "", rollLE}).do(h); rec.Code != http.StatusOK || oh.calls != 1 {
		t.Errorf("own entity: %d", rec.Code)
	}
}

func TestRevaluationReport_RefusesWithoutDutyInventoryOrActivation(t *testing.T) {
	good := &fakeStore{report: reportActivation("1")}
	h, _ := activationRouter(good, nil, onHandOf("1"))
	if rec := (call{http.MethodGet, reportPath(someID), "", calculate, "", rollLE}).do(h); rec.Code != http.StatusForbidden {
		t.Errorf("without productcost.view: %d", rec.Code)
	}
	if rec := (call{http.MethodGet, reportPath("not-a-uuid"), "", view, "", rollLE}).do(h); rec.Code != http.StatusNotFound {
		t.Errorf("a malformed id: %d", rec.Code)
	}
	noInventory, _ := activationRouter(&fakeStore{report: reportActivation("1")}, nil, nil)
	rec := call{http.MethodGet, reportPath(someID), "", view, "", rollLE}.do(noInventory)
	if code, _ := problemCode(t, rec); rec.Code != http.StatusServiceUnavailable || code != domain.CodeOnHandUnavailable {
		t.Errorf("no inventory configured: %d %s", rec.Code, rec.Body.String())
	}
	down := &fakeOnHand{err: errors.New("inventory down")}
	h, _ = activationRouter(&fakeStore{report: reportActivation("1")}, nil, down)
	rec = call{http.MethodGet, reportPath(someID), "", view, "", rollLE}.do(h)
	if code, _ := problemCode(t, rec); rec.Code != http.StatusBadGateway || code != domain.CodeOnHandUnavailable {
		t.Errorf("inventory down: %d %s", rec.Code, rec.Body.String())
	}
	missing := &fakeStore{err: errors.New("boom")}
	h, _ = activationRouter(missing, nil, onHandOf("1"))
	if rec := (call{http.MethodGet, reportPath(ids.New()), "", view, "", rollLE}).do(h); rec.Code != http.StatusInternalServerError {
		t.Errorf("store failure: %d", rec.Code)
	}
}

func TestRevaluationReport_AReversalIsNotComparedWithTheStockHeldNow(t *testing.T) {
	act := reportActivation("1000")
	original := ids.New()
	act.ReversesActivationID = &original
	oh := onHandOf("0")
	h, _ := activationRouter(&fakeStore{report: act}, nil, oh)
	rec := call{http.MethodGet, reportPath(someID), "", view, "", rollLE}.do(h)
	got := decodeReport(t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || got.TiedToOnHand != nil || got.Lines[0].OnHand != nil || got.Lines[0].Tied != nil || !got.TiedToJournal || oh.calls != 0 {
		t.Fatalf("%d %s inventory calls %d", rec.Code, rec.Body.String(), oh.calls)
	}
	if !strings.Contains(rec.Body.String(), `"tiedToOnHand":null`) {
		t.Fatalf("tiedToOnHand must be an explicit null: %s", rec.Body.String())
	}
}
