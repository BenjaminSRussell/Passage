package store_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/target"
	"github.com/BenjaminSRussell/passage/services/migration/internal/target/targettest"
	"github.com/BenjaminSRussell/passage/services/migration/internal/testdb"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

const (
	axSystem     = "ax2012"
	axDemoDir    = "../domain/testdata/ax"
	axRunEntity  = "00000000-0000-0000-0000-0000000000e1"
	axTermID     = "00000000-0000-7000-8000-0000000000f1"
	axProfileID  = "00000000-0000-7000-8000-0000000000f2"
	axCustGroups = `{"10":"00000000-0000-7000-8000-0000000000a1","20":"00000000-0000-7000-8000-0000000000a2","30":"00000000-0000-7000-8000-0000000000a3"}`
	axOtherGroup = "00000000-0000-7000-8000-0000000000a9"
	axVendGroups = `{"10":"00000000-0000-7000-8000-0000000000b1","20":"00000000-0000-7000-8000-0000000000b2","30":"00000000-0000-7000-8000-0000000000b3"}`
	axOtherVend  = "00000000-0000-7000-8000-0000000000b9"
	axCompanies  = `{"usmf":"00000000-0000-0000-0000-0000000000e1","demf":"00000000-0000-0000-0000-0000000000e2","gbsi":"00000000-0000-0000-0000-0000000000e3"}`
	axLegalOnly  = `{"usmf":"USMF","demf":"DEMF","gbsi":"GBSI"}`
	axModified   = `{"source":"ModifiedDateTime","target":"_modifiedAt","type":"timestamp","fn":"ax_datetime"}`
)

var axBlockedEnum = `{"0":"none","1":"invoice","2":"all","3":"payment","4":"requisition","5":"never"}`

type axLoaderEntity struct {
	entity, table string
	keys          []string
	mapping       string
}

var axLoaderEntities = []axLoaderEntity{
	{"party", "DirParty", []string{"RecId"}, `{"entity":"party","version":1,"fields":[
 {"source":"Name","target":"legalName","type":"string","required":true,"fn":"codepage_normalize"},
 {"rule":"const","value":"organization","target":"partyType","type":"string"},
 {"source":"PartyNumber","target":"_partyNumber","type":"string","required":true},` + axModified + `]}`},
	{"product", "EcoResProduct", []string{"DisplayProductNumber"}, `{"entity":"product","version":1,"fields":[
 {"source":"DisplayProductNumber","target":"productNumber","type":"string","required":true},
 {"source":"DisplayProductNumber","target":"name","type":"string","required":true},
 {"rule":"const","value":"ea","target":"baseUom","type":"string"},
 {"rule":"const","value":"product","target":"subtype","type":"string"},
 {"source":"ProductType","target":"productType","type":"string","required":true,"enum":{"1":"item","2":"service"}},
 {"source":"RecId","target":"_recId","type":"int","required":true},` + axModified + `]}`},
	{"item", "InventTable", []string{"DataAreaId", "ItemId"}, `{"entity":"item","version":1,"fields":[
 {"source":"ItemId","target":"productNumber","type":"string","required":true},
 {"source":"ItemId","target":"name","type":"string","required":true},
 {"rule":"const","value":"ea","target":"baseUom","type":"string"},
 {"rule":"const","value":"product","target":"subtype","type":"string"},
 {"source":"ItemType","target":"productType","type":"string","required":true,"enum":{"0":"item","1":"item","2":"service"}},
 {"source":"DataAreaId","target":"_legalEntity","type":"string","required":true,"enum":` + axLegalOnly + `},
 {"source":"Product","target":"_productRecId","type":"int","required":true},
 {"source":"NetWeight","target":"_netWeight","type":"decimal","default":"0"},` + axModified + `]}`},
	{"customer", "CustTable", []string{"DataAreaId", "AccountNum"}, `{"entity":"customer","version":1,"fields":[
 {"source":"AccountNum","target":"accountNumber","type":"string","required":true},
 {"source":"DataAreaId","target":"legalEntityId","type":"string","required":true,"enum":` + axCompanies + `},
 {"source":"Party","target":"partyKey","type":"string","required":true},
 {"source":"CustGroup","target":"customerGroupId","type":"string","enum":` + axCustGroups + `,"enumDefault":"` + axOtherGroup + `","default":"` + axOtherGroup + `"},
 {"rule":"const","value":"` + axTermID + `","target":"paymentTermId","type":"string"},
 {"rule":"const","value":"` + axProfileID + `","target":"postingProfileId","type":"string"},
 {"source":"Currency","target":"currencyCode","type":"string","default":"USD"},
 {"source":"CreditMax","target":"creditLimit","type":"decimal","scale":2,"default":"0"},
 {"source":"Blocked","target":"blocked","type":"string","required":true,"enum":` + axBlockedEnum + `},` + axModified + `],
 "rejectWhen":[{"field":"currencyCode","op":"notIn","values":["USD","EUR","CAD"],"code":"CUST-CURRENCY-UNKNOWN"}]}`},
	{"vendor", "VendTable", []string{"DataAreaId", "AccountNum"}, `{"entity":"vendor","version":1,"fields":[
 {"source":"AccountNum","target":"accountNumber","type":"string","required":true},
 {"source":"DataAreaId","target":"legalEntityId","type":"string","required":true,"enum":` + axCompanies + `},
 {"source":"Party","target":"partyKey","type":"string","required":true},
 {"source":"VendGroup","target":"vendorGroupId","type":"string","enum":` + axVendGroups + `,"enumDefault":"` + axOtherVend + `","default":"` + axOtherVend + `"},
 {"source":"VendGroup","target":"_vendorGroup","type":"string"},
 {"source":"Currency","target":"currencyCode","type":"string","required":true},
 {"source":"Blocked","target":"_blocked","type":"string","required":true,"enum":` + axBlockedEnum + `},` + axModified + `],
 "rejectWhen":[{"field":"_vendorGroup","op":"eq","value":"INTERCO","code":"VEND-INTERCOMPANY"}]}`},
	{"bom", "BOM", []string{"DataAreaId", "BOMId"}, `{"entity":"bom","version":1,"fields":[
 {"source":"productKey","target":"productKey","type":"string","required":true},
 {"source":"lines","target":"lines","type":"string","required":true},
 {"rule":"const","value":"S1","target":"siteId","type":"string"},
 {"rule":"const","value":"2012-01-01","target":"effectiveFrom","type":"string"},
 {"source":"BOMId","target":"_bomNumber","type":"string","required":true},
 {"source":"DataAreaId","target":"_legalEntity","type":"string","required":true,"enum":` + axLegalOnly + `},
 {"source":"Name","target":"_name","type":"string","required":true,"fn":"codepage_normalize"},
 {"source":"Approved","target":"_approved","type":"bool","required":true,"fn":"ax_noyes"},` + axModified + `]}`},
	{"route", "Route", []string{"DataAreaId", "RouteId"}, `{"entity":"route","version":1,"fields":[
 {"source":"productKey","target":"productKey","type":"string","required":true},
 {"source":"operations","target":"operations","type":"string","required":true},
 {"source":"RouteId","target":"routeCode","type":"string","required":true},
 {"rule":"const","value":"S1","target":"siteId","type":"string"},
 {"rule":"const","value":"2012-01-01","target":"effectiveFrom","type":"string"},
 {"source":"DataAreaId","target":"_legalEntity","type":"string","required":true,"enum":` + axLegalOnly + `},
 {"source":"Name","target":"_name","type":"string","required":true,"fn":"codepage_normalize"},
 {"source":"Approved","target":"_approved","type":"bool","required":true,"fn":"ax_noyes"},` + axModified + `]}`},
}

type axRows []domain.Record

func (r axRows) Extract(context.Context, string) ([]domain.Record, error) { return r, nil }

func axDemoRows(t *testing.T, e axLoaderEntity) axRows {
	t.Helper()
	f, err := os.Open(filepath.Join(axDemoDir, "demo", e.table+".csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	table, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var out axRows
	for _, row := range table[1:] {
		fields := make(map[string]any, len(row))
		for i, name := range table[0] {
			fields[strings.TrimSpace(name)] = row[i]
		}
		parts := make([]string, len(e.keys))
		for i, k := range e.keys {
			parts[i] = strings.TrimSpace(fields[k].(string))
		}
		out = append(out, domain.Record{Key: strings.Join(parts, "|"), Fields: fields})
	}
	return out
}

func axExpectedDemo(t *testing.T) map[string]struct {
	Loaded  int
	Rejects map[string][]string
} {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(axDemoDir, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Demo map[string]struct {
			Loaded  int
			Rejects map[string][]string
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Demo
}

func axRejectCodes(rep domain.Report) map[string][]string {
	out := map[string][]string{}
	for _, r := range rep.Rejects {
		for _, x := range r.Rejects {
			out[r.SourceKey] = append(out[r.SourceKey], x.Code)
		}
	}
	for k := range out {
		slices.Sort(out[k])
	}
	return out
}

func axJoin(rows axRows, productKeys []string, extra func(i int, product, next string) map[string]any) axRows {
	out := make(axRows, len(rows))
	for i, r := range rows {
		fields := map[string]any{}
		for k, v := range r.Fields {
			fields[k] = v
		}
		fields["productKey"] = productKeys[i%len(productKeys)]
		for k, v := range extra(i, productKeys[i%len(productKeys)], productKeys[(i+1)%len(productKeys)]) {
			fields[k] = v
		}
		out[i] = domain.Record{Key: r.Key, Fields: fields}
	}
	return out
}

// P4a: the demo manufacturer's seven AX tables go through the pipeline and the real target.Loader (signed service
// tokens over HTTP to the owning services' import routes, then the read-back reconciliation) with the cross-reference
// in Postgres, and the outcome matches the independent reference's loaded and rejected rows.
func TestPostgres_AXDemoExtractMigratesThroughTheRealLoader(t *testing.T) {
	pool, app := testkit.DBWithApp(t)
	ctx := principalContext(1)
	testdb.Fresh(t, pool)
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	xref := store.New(app).Xref()
	fakes := targettest.Start(t)
	cfg := target.Config{PartyURL: fakes.PartyURL, ProductURL: fakes.ProductURL, EngineeringURL: fakes.EngineeringURL, Signer: fakes.Signer, Concurrency: 4,
		Sleep: func(context.Context, time.Duration) error { return nil }}
	inLegalEntity := target.New(cfg, target.Run{SourceSystem: axSystem, LegalEntityID: axRunEntity, Xref: xref})
	ownLegalEntity := target.New(cfg, target.Run{SourceSystem: axSystem, Xref: xref})
	expected := axExpectedDemo(t)

	var items []string
	deadParties := map[string]bool{}
	created := map[string]int{}
	for _, e := range axLoaderEntities {
		rows := axDemoRows(t, e)
		switch e.entity {
		case "bom":
			rows = axJoin(rows, items, func(i int, _, next string) map[string]any {
				return map[string]any{"lines": fmt.Sprintf(`[{"lineNo":10,"componentKey":%q,"quantity":"2.5"}]`, next)}
			})
		case "route":
			rows = axJoin(rows, items, func(int, string, string) map[string]any {
				return map[string]any{"operations": `[{"no":10,"nextNo":20},{"no":20}]`}
			})
		}
		loader := inLegalEntity
		if e.entity == "customer" || e.entity == "vendor" {
			loader = ownLegalEntity
		}
		mapping, err := domain.ParseMapping([]byte(e.mapping))
		if err != nil {
			t.Fatalf("%s mapping: %v", e.entity, err)
		}
		p := &domain.Pipeline{Extractor: rows, Loader: loader, Reader: loader.Reader(), Xref: xref, IDGen: ids.New}
		spec := domain.RunSpec{SourceSystem: axSystem, Entity: e.entity, Mapping: mapping}
		rep, err := p.Run(ctx, spec)
		if err != nil {
			t.Fatalf("%s: %v", e.entity, err)
		}
		want := expected[e.entity]
		orphans := map[string]bool{}
		for _, r := range rows {
			party, _ := r.Fields["Party"].(string)
			if _, queued := want.Rejects[r.Key]; !queued && deadParties[strings.TrimSpace(party)] {
				orphans[r.Key] = true
			}
		}
		got := axRejectCodes(rep)
		for key, codes := range got {
			for _, c := range codes {
				refused := strings.HasPrefix(c, "MIG-TARGET-") || strings.HasPrefix(c, "MIG-LOAD-") || strings.HasPrefix(c, "MIG-REF-")
				if refused && (c != "MIG-REF-UNRESOLVED" || !orphans[key]) {
					t.Errorf("%s %s: the real loader refused the row: %s", e.entity, key, c)
				}
			}
		}
		if rep.Created != want.Loaded-len(orphans) || len(got) != len(want.Rejects)+len(orphans) {
			t.Errorf("%s: created %d rejected %d, the reference loads %d and rejects %d plus %d rows whose party it rejected", e.entity, rep.Created, len(got), want.Loaded, len(want.Rejects), len(orphans))
		}
		recon := rep.Reconciliation
		if recon == nil || recon.OK() != (len(orphans) == 0) || recon.Source != rep.Created+len(orphans) || recon.Target != rep.Created {
			t.Fatalf("%s: read-back reconciliation %+v, want source %d target %d and a failed tie-out only when the loader refused rows", e.entity, recon, rep.Created+len(orphans), rep.Created)
		}
		keys, err := xref.Keys(ctx, axSystem, e.entity)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := xref.GetMany(ctx, axSystem, e.entity, keys)
		if err != nil {
			t.Fatal(err)
		}
		loaded := 0
		for _, entry := range entries {
			if entry.ContentHash != "" && entry.TargetID != "" {
				loaded++
			}
		}
		if loaded != rep.Created {
			t.Fatalf("%s: Postgres cross-reference holds %d loaded rows, want %d", e.entity, loaded, rep.Created)
		}
		again, err := p.Run(ctx, spec)
		if err != nil {
			t.Fatalf("%s re-run: %v", e.entity, err)
		}
		if again.Created != 0 || again.Updated != 0 || again.Unchanged != rep.Created || len(again.Rejects) != len(rep.Rejects) {
			t.Fatalf("%s: re-run created=%d updated=%d unchanged=%d (want %d) rejects %d->%d", e.entity, again.Created, again.Updated, again.Unchanged, rep.Created, len(rep.Rejects), len(again.Rejects))
		}
		created[e.entity] = rep.Created
		if e.entity == "party" {
			for key := range got {
				deadParties[key] = true
			}
		}
		if e.entity == "item" {
			for _, entry := range rep.Ledger {
				items = append(items, entry.SourceKey)
			}
			slices.Sort(items)
		}
	}

	fakes.Lock()
	defer fakes.Unlock()
	held := map[string]int{"party": len(fakes.Parties), "customer": len(fakes.Customers), "vendor": len(fakes.Vendors),
		"product": len(fakes.Products), "bom": len(fakes.Boms), "route": len(fakes.Routes)}
	want := map[string]int{"party": created["party"], "customer": created["customer"], "vendor": created["vendor"],
		"product": created["product"] + created["item"], "bom": created["bom"], "route": created["route"]}
	for name, n := range want {
		if held[name] != n || n == 0 {
			t.Errorf("target services hold %d %s records, the loads created %d", held[name], name, n)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
