package target_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/target"
)

const (
	bomLines  = `[{"lineNo":10,"componentKey":"WHEEL","quantity":"2"},{"componentProductId":"0190f5a0-0000-7000-8000-0000000000c2","quantity":1}]`
	routeOps  = `[{"no":10,"nextNo":20,"runH":"0.5"},{"no":20}]`
	bikeID    = "0190f5a0-0000-7000-8000-0000000000b1"
	wheelID   = "0190f5a0-0000-7000-8000-0000000000b2"
	unloadedK = "SADDLE"
)

func loadedItems(t *testing.T, h *harness) {
	t.Helper()
	for key, id := range map[string]string{"BIKE": bikeID, "WHEEL": wheelID} {
		if err := h.xref.Put(context.Background(), domain.XrefKey{SourceSystem: system, Entity: "item", SourceKey: key}, domain.XrefEntry{TargetID: id, ContentHash: "h"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBom_CreatesADraftVersionWithItsProductAndComponentsResolved(t *testing.T) {
	h, l := newHarness(t, le1)
	loadedItems(t, h)
	res := load(t, l, "bom", item("B1", false, 0, map[string]any{"productKey": "BIKE", "effectiveFrom": "2026-01-01", "siteId": "S1", "lines": bomLines}))
	if res[0].Code != "" || res[0].TargetID == "" || res[0].TargetVersion != 1 {
		t.Fatalf("bom = %+v", res[0])
	}
	calls := h.f.Calls("engineering")
	if len(calls) != 1 || calls[0].Path != "/v1/engineering/bom-versions" || calls[0].Header.Get("Idempotency-Key") != "idem-B1" {
		t.Fatalf("calls = %+v", calls)
	}
	c := calls[0]
	if c.Claims.Aud != "engineering" || c.Claims.Le != le1 || strings.Join(c.Claims.Duties, ",") != "engineering.import.write,engineering.import.read" {
		t.Fatalf("claims = %+v", c.Claims)
	}
	if c.Body["productId"] != bikeID || c.Body["status"] != "draft" || c.Body["siteId"] != "S1" {
		t.Fatalf("body = %+v", c.Body)
	}
	lines := c.Body["lines"].([]any)
	first, second := lines[0].(map[string]any), lines[1].(map[string]any)
	if first["componentProductId"] != wheelID || first["lineNo"] != float64(10) || second["componentProductId"] != "0190f5a0-0000-7000-8000-0000000000c2" || len(first) != 3 {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestBom_RefusesRowsItCannotSendAndNeverChangesALoadedVersion(t *testing.T) {
	h, l := newHarness(t, le1)
	loadedItems(t, h)
	for name, c := range map[string]struct {
		it   domain.LoadItem
		code string
	}{
		"changed since its load": {item("B1", true, 1, map[string]any{"productKey": "BIKE", "effectiveFrom": "2026-01-01", "lines": bomLines}), target.CodeUpdateUnavailable},
		"no lines":               {item("B2", false, 0, map[string]any{"productKey": "BIKE", "effectiveFrom": "2026-01-01"}), target.CodeFieldRequired},
		"lines not JSON":         {item("B3", false, 0, map[string]any{"productKey": "BIKE", "effectiveFrom": "2026-01-01", "lines": "WHEEL x2"}), target.CodeFieldRequired},
		"product not loaded":     {item("B4", false, 0, map[string]any{"productKey": unloadedK, "effectiveFrom": "2026-01-01", "lines": bomLines}), target.CodeRefUnresolved},
		"component not loaded":   {item("B5", false, 0, map[string]any{"productKey": "BIKE", "effectiveFrom": "2026-01-01", "lines": `[{"componentKey":"` + unloadedK + `","quantity":1}]`}), target.CodeRefUnresolved},
	} {
		if res := load(t, l, "bom", c.it); res[0].Code != c.code {
			t.Errorf("%s = %+v, want %s", name, res[0], c.code)
		}
	}
	if n := len(h.f.Calls("engineering")); n != 0 {
		t.Fatalf("%d engineering calls for rows that cannot load", n)
	}
}

func TestRoute_CreatesADraftVersionAndPassesTheOperationsThrough(t *testing.T) {
	h, l := newHarness(t, le1)
	loadedItems(t, h)
	res := load(t, l, "route", item("R1", false, 0, map[string]any{"productKey": "BIKE", "siteId": "S1", "routeCode": "R-1", "versionNo": "2", "effectiveFrom": "2026-01-01", "operations": routeOps}))
	if res[0].Code != "" || res[0].TargetID == "" {
		t.Fatalf("route = %+v", res[0])
	}
	c := h.f.Calls("engineering")[0]
	if c.Path != "/v1/engineering/route-versions" || c.Body["productId"] != bikeID || c.Body["versionNo"] != float64(2) || len(c.Body["operations"].([]any)) != 2 {
		t.Fatalf("call = %+v", c)
	}
	if r := load(t, l, "route", item("R2", false, 0, map[string]any{"productKey": "BIKE", "siteId": "S1", "routeCode": "R-1", "versionNo": "two", "effectiveFrom": "2026-01-01", "operations": routeOps})); r[0].Code != target.CodeFieldRequired {
		t.Fatalf("bad version number = %+v", r[0])
	}
}

type rows []domain.Record

func (r rows) Extract(context.Context, string) ([]domain.Record, error) { return r, nil }

func mapping(t *testing.T, entity string, fields ...string) domain.Mapping {
	t.Helper()
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = fmt.Sprintf(`{"source":%q,"target":%q,"type":"string"}`, f, f)
	}
	m, err := domain.ParseMapping([]byte(fmt.Sprintf(`{"entity":%q,"version":1,"fields":[%s]}`, entity, strings.Join(parts, ","))))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func run(t *testing.T, h *harness, l *target.Loader, entity string, m domain.Mapping, extract rows) domain.Report {
	t.Helper()
	p := &domain.Pipeline{Extractor: extract, Loader: l, Reader: l.Reader(), Xref: h.xref, IDGen: ids.New}
	rep, err := p.Run(ctx(), domain.RunSpec{SourceSystem: system, Entity: entity, Mapping: m})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// Each entity loaded through the owning service's API is read back through its detail route and ties out, although
// the targets answer in their own form: an upper-cased currency and unit, Go field names, a signed credit amount,
// a resolved product id for a legacy key, and a masked date of birth.
func TestReader_EveryLoadedEntityReconcilesAgainstTheTargetsOwnRecords(t *testing.T) {
	h, l := newHarness(t, le1)
	reconciled := func(entity string, rep domain.Report, n int) {
		t.Helper()
		if rep.Reconciliation == nil || !rep.Reconciliation.OK() || rep.Reconciliation.Source != n || rep.Reconciliation.Target != n || rep.Status != domain.StatusSucceeded {
			t.Fatalf("%s: status %s rejects %+v reconciliation %+v", entity, rep.Status, rep.Rejects, rep.Reconciliation)
		}
	}
	reconciled("party", run(t, h, l, "party", mapping(t, "party", "legalName", "partyType", "dob"), rows{
		{Key: "P1", Fields: map[string]any{"legalName": "Acme GmbH", "partyType": "Organization"}},
		{Key: "P2", Fields: map[string]any{"legalName": "Ada", "partyType": "person", "dob": "1815-12-10"}},
	}), 2)
	reconciled("customer", run(t, h, l, "customer", mapping(t, "customer", "partyKey", "accountNumber", "customerGroupId", "paymentTermId", "postingProfileId", "currencyCode", "creditLimit"), rows{
		{Key: "C1", Fields: map[string]any{"partyKey": "P1", "accountNumber": "C-1", "customerGroupId": "g", "paymentTermId": "t", "postingProfileId": "p", "currencyCode": "eur", "creditLimit": "1500.50"}},
	}), 1)
	reconciled("item", run(t, h, l, "item", mapping(t, "item", "productNumber", "name", "baseUom", "lifecycleState"), rows{
		{Key: "BIKE", Fields: map[string]any{"productNumber": "BIKE", "name": "Bike", "baseUom": "ea", "lifecycleState": "draft"}},
		{Key: "WHEEL", Fields: map[string]any{"productNumber": "WHEEL", "name": "Wheel", "baseUom": "ea"}},
	}), 2)
	reconciled("ar_open_item", run(t, h, l, "ar_open_item", mapping(t, "ar_open_item", "customerKey", "documentNumber", "kind", "side", "documentDate", "dueDate", "currency", "originalAmount", "openAmount"), rows{
		{Key: "AR1", Fields: map[string]any{"customerKey": "C1", "documentNumber": "CN-1", "kind": "CREDIT_NOTE", "side": "CREDIT", "documentDate": "2026-01-01", "dueDate": "2026-02-01", "currency": "EUR", "originalAmount": "10.00", "openAmount": "10.00"}},
	}), 1)
	reconciled("bom", run(t, h, l, "bom", mapping(t, "bom", "productKey", "effectiveFrom", "siteId", "lines"), rows{
		{Key: "B1", Fields: map[string]any{"productKey": "BIKE", "effectiveFrom": "2026-01-01", "siteId": "S1", "lines": `[{"componentKey":"WHEEL","quantity":"2.0"}]`}},
	}), 1)
	reconciled("route", run(t, h, l, "route", mapping(t, "route", "productKey", "siteId", "routeCode", "effectiveFrom", "operations"), rows{
		{Key: "R1", Fields: map[string]any{"productKey": "BIKE", "siteId": "S1", "routeCode": "R-1", "effectiveFrom": "2026-01-01", "operations": routeOps}},
	}), 1)
	for _, c := range h.f.Calls("") {
		if c.Method == "GET" && !strings.HasSuffix(strings.Join(c.Claims.Duties, ","), ".import.read") {
			t.Fatalf("read %s under %v", c.Path, c.Claims.Duties)
		}
	}
}

// A record changed or deleted in the target after the load fails the tie-out of the next full run.
func TestReader_ATargetRecordChangedOrGoneFailsReconciliation(t *testing.T) {
	h, l := newHarness(t, le1)
	m := mapping(t, "party", "legalName")
	extract := rows{{Key: "P1", Fields: map[string]any{"legalName": "Acme"}}, {Key: "P2", Fields: map[string]any{"legalName": "Beta"}}}
	if rep := run(t, h, l, "party", m, extract); !rep.Reconciliation.OK() {
		t.Fatalf("first run = %+v", rep.Reconciliation)
	}
	x1, _, _ := h.xref.Get(context.Background(), domain.XrefKey{SourceSystem: system, Entity: "party", SourceKey: "P1"})
	h.f.Lock()
	h.f.Parties[x1.TargetID].Body["legalName"] = "Acme (edited in Passage)"
	h.f.Unlock()
	rep := run(t, h, l, "party", m, extract)
	if rep.Reconciliation.OK() || rep.Status != domain.StatusFailed {
		t.Fatalf("changed record: status %s reconciliation %+v", rep.Status, rep.Reconciliation)
	}
	h.f.Lock()
	delete(h.f.Parties, x1.TargetID)
	h.f.Unlock()
	rep = run(t, h, l, "party", m, extract)
	if rep.Reconciliation.OK() || rep.Reconciliation.Target != 1 {
		t.Fatalf("missing record: %+v", rep.Reconciliation)
	}
}

// An entity with no read-back route, or a target without an address, is not reconciled (and not failed for it).
func TestReader_EntitiesItCannotReadAreNotReconciled(t *testing.T) {
	h, _ := newHarness(t, le1)
	l := target.New(target.Config{Signer: h.f.Signer, PartyURL: h.f.PartyURL, Concurrency: 1}, target.Run{SourceSystem: system, LegalEntityID: le1, Xref: h.xref})
	r := l.Reader()
	if r.Reads("ap_open_item") || r.Reads("item") || !r.Reads("party") || !r.Reads("customer") {
		t.Fatal("Reads must follow the read specs and the configured targets")
	}
	noLE := target.New(target.Config{Signer: h.f.Signer, PartyURL: h.f.PartyURL}, target.Run{SourceSystem: system, Xref: h.xref}).Reader()
	if noLE.Reads("party") || !noLE.Reads("customer") {
		t.Fatal("a tenant-wide entity needs the run's legal entity to be read")
	}
	rep := run(t, h, l, "ap_open_item", mapping(t, "ap_open_item", "x"), rows{{Key: "A1", Fields: map[string]any{"x": "1"}}})
	if rep.Reconciliation != nil {
		t.Fatalf("unreadable entity reconciled: %+v", rep.Reconciliation)
	}
}

func TestReader_ProjectComparesInOneCanonicalForm(t *testing.T) {
	_, l := newHarness(t, le1)
	r := l.Reader()
	got := r.Project("item", map[string]any{"productNumber": " P-1 ", "baseUom": "ea", "name": "Bolt", "attr.colour": "red", "_local": "x"})
	if fmt.Sprint(got) != "map[baseUom:EA name:Bolt productNumber:P-1]" {
		t.Fatalf("item = %v", got)
	}
	got = r.Project("ar_open_item", map[string]any{"originalAmount": "-10.50", "openAmount": 10.5, "side": "credit", "kind": "CREDIT_NOTE"})
	if fmt.Sprint(got) != "map[openAmount:10.5 originalAmount:10.5 side:CREDIT]" {
		t.Fatalf("open item = %v", got)
	}
	got = r.Project("bom", map[string]any{"productId": "x", "lines": `[{"lineNo":20,"quantity":"1.50"},{"lineNo":10,"quantity":2}]`})
	if fmt.Sprint(got) != "map[lines:10x2,20x1.5 productId:x]" {
		t.Fatalf("bom = %v", got)
	}
}
