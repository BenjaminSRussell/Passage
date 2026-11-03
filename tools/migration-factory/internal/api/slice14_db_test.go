package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Database tests for slice 14: releasing quarantined open_orders lines (S13-d). They skip without
// PASSAGE_TEST_DATABASE_URL and run as passage_app when PASSAGE_TEST_APP_DATABASE_URL is set.

func orderFiller(n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, orderRec(fmt.Sprintf("SL-F%d", i), "SALES", fmt.Sprintf("SO-F%d", i), "1", "NUT", "C2", "3", "1", "1"))
	}
	return out
}

func TestOpenOrders_ReleaseCorrectsItemAndMovesTheOrderCount(t *testing.T) {
	h, admin := dbServer(t)
	lines := append([]string{
		orderRec("SL-1", "SALES", "SO-1", "1", "BOLT", "C1", "10", "4", "2.5"),
		orderRec("SL-2", "SALES", "SO-2", "1", "GONE", "C1", "3", "0", "1.333333"), // SO-2 has no other line
		orderRec("SL-3", "SALES", "SO-1", "2", "GONE", "C1", "4", "1", "2"),        // SO-1 is on the loaded side already
	}, orderFiller(27)...)
	body := ordersBody(`,"quarantineMaxPct":"10"`, lines...)
	run := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: ordersPath, tenant: tenantA, duties: allDuties, user: "e", body: body}))
	if run.Status != "LOADED" || run.RecordsQuarantined != 2 {
		t.Fatalf("run %+v %+v", run.runDTO, run.Quarantined)
	}
	if n := count(t, admin, `select count(*) from pack_quarantine where run_id = $1 and line is not null`, run.RunID); n != 2 {
		t.Fatalf("quarantine rows holding the whole line = %d", n)
	}
	rel := func(seq int, body, key string) *httptest.ResponseRecorder {
		return do(t, h, request{method: "POST", path: releasePath(run.RunID, seq), tenant: tenantA, duties: allDuties, user: "ops", key: key, body: body})
	}
	// An item outside the master still fails; fields of other packs are refused; a stale version is a conflict.
	if r := rel(1, `{"item":"NOPE"}`, ""); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "REFERENTIAL") {
		t.Fatalf("uncorrected release: %d %s", r.Code, r.Body)
	}
	if r := rel(1, `{"item":"BOLT","site":"S1"}`, ""); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("site on an open_orders run: %d %s", r.Code, r.Body)
	}
	if r := rel(1, `{"item":"BOLT","account":"X"}`, ""); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("account on an open_orders run: %d %s", r.Code, r.Body)
	}
	if r := rel(1, `{"item":"BOLT","deliveryDate":"soon"}`, ""); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad delivery date: %d %s", r.Code, r.Body)
	}
	if r := rel(1, `{"item":"BOLT","version":9}`, ""); r.Code != http.StatusConflict {
		t.Fatalf("stale version: %d %s", r.Code, r.Body)
	}
	// An item added to the master since the run is sent with the release.
	r := rel(1, `{"item":"WASHER","parties":["WASHER"],"deliveryDate":"2027-02-01","version":1}`, "rel-1")
	if r.Code != http.StatusOK {
		t.Fatalf("release 1: %d %s", r.Code, r.Body)
	}
	mid := decode[runDetailDTO](t, r)
	if mid.Status != "LOADED" || mid.RecordsLoaded != 29 || mid.RecordsQuarantined != 1 || mid.Version != 2 {
		t.Fatalf("after release 1 %+v", mid.runDTO)
	}
	r = rel(2, `{"item":"NUT"}`, "rel-2")
	if r.Code != http.StatusOK {
		t.Fatalf("release 2: %d %s", r.Code, r.Body)
	}
	after := decode[runDetailDTO](t, r)
	if after.Status != "RECONCILED" || after.RecordsLoaded != 30 || after.RecordsQuarantined != 0 {
		t.Fatalf("after release 2 %+v", after.runDTO)
	}
	for _, c := range after.ControlTotals {
		if c.Status != "MATCH" {
			t.Fatalf("total %+v after the releases", c)
		}
	}
	if q := after.Quarantined[0]; q.Status != "RELEASED" || q.ReleasedBy != "ops" || q.Released == nil || q.Released.Account != "WASHER" {
		t.Fatalf("released row %+v", q)
	}
	if n := count(t, admin, `select count(*) from open_order_opening_line l join legacy_xref x on x.xref_id = l.legacy_xref_id and x.passage_id = l.line_id
		where l.run_id = $1 and l.legacy_id = 'SL-2' and l.item = 'WASHER' and l.delivery_date = '2027-02-01' and x.source_table = 'open_orders.sales'`, run.RunID); n != 1 {
		t.Fatalf("released line tied to its cross-reference = %d", n)
	}
	if n := count(t, admin, `select count(*) from pack_run_order where run_id = $1 and order_type = 'SALES' and order_number in ('SO-1', 'SO-2')`, run.RunID); n != 2 {
		t.Fatalf("orders kept with the run = %d", n)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.migration.pack.reconciled.v1' and aggregate_id = $1`, run.RunID); n != 2 {
		t.Fatalf("pack.reconciled events = %d, want one from the run and one when release 2 reconciles it", n)
	}
	// Idempotent by record; another tenant sees no run.
	if r := rel(1, `{"item":"WASHER"}`, "again"); r.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("re-release: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantB, duties: allDuties, user: "ops", body: `{}`}); r.Code != http.StatusNotFound {
		t.Fatalf("other tenant: %d", r.Code)
	}
}

func TestOpenOrders_ReleaseRefusesAnOrderLineAlreadyHeld(t *testing.T) {
	h, admin := dbServer(t)
	// The same order line under two legacy ids: the second is quarantined as a repeat of the key.
	lines := append([]string{
		orderRec("SL-1", "SALES", "SO-1", "1", "BOLT", "C1", "10", "4", "2.5"),
		orderRec("SL-9", "SALES", "SO-1", "1", "BOLT", "C1", "10", "4", "2.5"),
	}, orderFiller(18)...)
	run := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: ordersPath, tenant: tenantA, duties: allDuties, user: "e", body: ordersBody(`,"quarantineMaxPct":"10"`, lines...)}))
	if run.RecordsQuarantined != 1 || run.Quarantined[0].LegacyID != "SL-9" || run.Quarantined[0].Rule != "UNIQUENESS" {
		t.Fatalf("quarantine %+v", run.Quarantined)
	}
	r := do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{}`})
	if r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "already has an opening line") {
		t.Fatalf("duplicate order line: %d %s", r.Code, r.Body)
	}
	if n := count(t, admin, `select count(*) from open_order_opening_line where run_id = $1`, run.RunID); n != 19 {
		t.Fatalf("lines after the refused release = %d", n)
	}
}

// A purchase line is checked against the vendor master: the vendor kept with the run (V1) and one
// sent in vendors are found, a customer id is not; a vendor-only correction keeps the line's
// quantity key, and only the line of an order the loaded side lacks adds to the order count.
func TestOpenOrders_ReleaseCorrectsTheVendorOfAPurchaseLine(t *testing.T) {
	h, _ := dbServer(t)
	lines := append([]string{
		orderRec("PL-1", "PURCHASE", "PO-1", "1", "BOLT", "V1", "7", "0", "3"),
		orderRec("PL-2", "PURCHASE", "PO-1", "2", "NUT", "V9", "5", "0", "2"), // vendor not in the master; PO-1 is loaded
		orderRec("PL-3", "PURCHASE", "PO-2", "1", "NUT", "C1", "2", "0", "1"), // a customer on a purchase order; PO-2 is new
	}, orderFiller(18)...)
	run := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: ordersPath, tenant: tenantA, duties: allDuties, user: "e", body: ordersBody(`,"quarantineMaxPct":"10"`, lines...)}))
	if run.Status != "LOADED" || run.RecordsQuarantined != 2 {
		t.Fatalf("run %+v %+v", run.runDTO, run.Quarantined)
	}
	rel := func(seq int, body string) *httptest.ResponseRecorder {
		return do(t, h, request{method: "POST", path: releasePath(run.RunID, seq), tenant: tenantA, duties: allDuties, user: "ops", body: body})
	}
	for _, body := range []string{`{"party":"V7"}`, `{"party":"V7","customers":["V7"]}`, `{"party":"C1"}`} {
		if r := rel(1, body); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "vendor master") {
			t.Fatalf("release %s: %d %s", body, r.Code, r.Body)
		}
	}
	if r := rel(1, `{"party":"V7","vendors":[" V7 "]}`); r.Code != http.StatusOK {
		t.Fatalf("release with the vendor sent: %d %s", r.Code, r.Body)
	} else if mid := decode[runDetailDTO](t, r); mid.Status != "LOADED" || mid.RecordsQuarantined != 1 {
		t.Fatalf("after release 1 %+v", mid.runDTO)
	}
	r := rel(2, `{"party":"V1"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("release with the run's vendor: %d %s", r.Code, r.Body)
	}
	after := decode[runDetailDTO](t, r)
	if after.Status != "RECONCILED" || after.RecordsLoaded != 21 {
		t.Fatalf("after release 2 %+v", after.runDTO)
	}
	for _, c := range after.ControlTotals {
		if c.Status != "MATCH" {
			t.Fatalf("total %+v after the releases", c)
		}
	}
}

// customers and vendors belong to an open_orders release; another pack refuses them.
func TestRelease_CustomersAndVendorsAreRefusedOnOtherPacks(t *testing.T) {
	h, _ := dbServer(t)
	filler, accts := bankFiller(10)
	body := bankBodyWith(accts, append([]string{bankRec("BK-2", "TYPO-USD", "USD", "-20.25")}, filler...)...)
	run := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: bankPath, tenant: tenantA, duties: allDuties, body: body}))
	if run.Status != "LOADED" || run.RecordsQuarantined != 1 {
		t.Fatalf("run %+v", run.runDTO)
	}
	for _, body := range []string{`{"bankAccount":"SAV-USD","customers":["C1"]}`, `{"bankAccount":"SAV-USD","vendors":["V1"]}`, `{"bankAccount":"SAV-USD","deliveryDate":"2027-01-01"}`} {
		if r := do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: body}); r.Code != http.StatusUnprocessableEntity {
			t.Fatalf("release %s on a bank run: %d %s", body, r.Code, r.Body)
		}
	}
}
