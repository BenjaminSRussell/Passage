// Package ac1load is the Wave 10 AC1 seeded load: 50k customers, 20k vendors, 100k items
// and 10k BOMs through the extract-map-validate-load-reconcile pipeline, with the
// cross-reference in Postgres (or any XrefStore) and an in-process target so the run
// can reconcile without the owning services' batch APIs.
package ac1load

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const (
	SourceSystem = "ax2012"
	Budget       = 30 * time.Minute
)

// FullSize is the AC1 volume in issue #10.
var FullSize = Volumes{Customers: 50_000, Vendors: 20_000, Items: 100_000, BOMs: 10_000}

var entities = []string{"customer", "vendor", "item", "bom"}

type Volumes struct {
	Customers int `json:"customers"`
	Vendors   int `json:"vendors"`
	Items     int `json:"items"`
	BOMs      int `json:"boms"`
}

func (v Volumes) Count(entity string) int {
	switch entity {
	case "customer":
		return v.Customers
	case "vendor":
		return v.Vendors
	case "item":
		return v.Items
	case "bom":
		return v.BOMs
	}
	return 0
}

type EntityResult struct {
	Entity  string
	Elapsed time.Duration
	Report  domain.Report
}

type Result struct {
	Volumes  Volumes
	Elapsed  time.Duration
	Entities []EntityResult
	Rerun    map[string]domain.Report
}

func (r Result) OK() bool {
	if r.Elapsed >= Budget || len(r.Entities) == 0 {
		return false
	}
	for _, e := range r.Entities {
		n := r.Volumes.Count(e.Entity)
		if n == 0 || e.Report.Status != domain.StatusSucceeded || e.Report.Created != n || len(e.Report.Rejects) != 0 {
			return false
		}
		if e.Report.Reconciliation == nil || !e.Report.Reconciliation.OK() || e.Report.Reconciliation.Source != n || e.Report.Reconciliation.Target != n {
			return false
		}
		again := r.Rerun[e.Entity]
		if again.Created != 0 || again.Updated != 0 || again.Unchanged != n {
			return false
		}
	}
	return true
}

type recordsExtractor struct{ rows []domain.Record }

func (e recordsExtractor) Extract(context.Context, string) ([]domain.Record, error) {
	return e.rows, nil
}

func Dataset(v Volumes) map[string][]domain.Record {
	out := map[string][]domain.Record{
		"customer": make([]domain.Record, v.Customers),
		"vendor":   make([]domain.Record, v.Vendors),
		"item":     make([]domain.Record, v.Items),
		"bom":      make([]domain.Record, v.BOMs),
	}
	for i := 0; i < v.Customers; i++ {
		id := fmt.Sprintf("C%06d", i)
		out["customer"][i] = domain.Record{Key: id, Fields: map[string]any{
			"AccountNum": id, "Name": fmt.Sprintf("Customer %d", i),
			"CustGroup": []string{"10", "20", "30"}[i%3], "CreditMax": fmt.Sprintf("%d.50", 1000+i),
			"Blocked": []string{"0", "1"}[i%2], "CreatedDate": "2019-03-14",
		}}
	}
	for i := 0; i < v.Vendors; i++ {
		id := fmt.Sprintf("V%06d", i)
		out["vendor"][i] = domain.Record{Key: id, Fields: map[string]any{
			"AccountNum": id, "Name": fmt.Sprintf("Vendor %d", i), "Currency": "USD",
		}}
	}
	for i := 0; i < v.Items; i++ {
		id := fmt.Sprintf("I%06d", i)
		out["item"][i] = domain.Record{Key: id, Fields: map[string]any{
			"ItemId": id, "ItemName": fmt.Sprintf("Item %d", i), "Weight": int64(i%50) + 1,
		}}
	}
	items := v.Items
	if items < 1 {
		items = 1
	}
	for i := 0; i < v.BOMs; i++ {
		id := fmt.Sprintf("B%06d", i)
		out["bom"][i] = domain.Record{Key: id, Fields: map[string]any{
			"BOMId": id, "ItemId": fmt.Sprintf("I%06d", i%items), "Qty": "2.5",
		}}
	}
	return out
}

func mappingJSON(entity string) string {
	switch entity {
	case "customer":
		return `{"entity":"customer","version":1,"fields":[
 {"source":"AccountNum","target":"accountNumber","type":"string","required":true},
 {"source":"Name","target":"name","type":"string","required":true},
 {"source":"CustGroup","target":"segment","type":"string","enum":{"10":"retail","20":"wholesale","30":"oem"}},
 {"source":"CreditMax","target":"creditLimit","type":"decimal","default":"0"},
 {"source":"Blocked","target":"blocked","type":"bool"},
 {"source":"CreatedDate","target":"createdOn","type":"date"}]}`
	case "vendor":
		return `{"entity":"vendor","version":1,"fields":[{"source":"AccountNum","target":"accountNumber","type":"string","required":true},{"source":"Name","target":"name","type":"string"},{"source":"Currency","target":"currency","type":"string","required":true}]}`
	case "item":
		return `{"entity":"item","version":1,"fields":[{"source":"ItemId","target":"sku","type":"string","required":true},{"source":"ItemName","target":"name","type":"string"},{"source":"Weight","target":"weight","type":"int"}]}`
	case "bom":
		return `{"entity":"bom","version":1,"fields":[{"source":"BOMId","target":"bomNumber","type":"string","required":true},{"source":"ItemId","target":"parentSku","type":"string","required":true},{"source":"Qty","target":"quantity","type":"decimal","required":true}]}`
	}
	return ""
}

func Mapping(entity string) (domain.Mapping, error) {
	raw := mappingJSON(entity)
	if raw == "" {
		return domain.Mapping{}, fmt.Errorf("unknown entity %s", entity)
	}
	return domain.ParseMapping([]byte(raw))
}

func idGen() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("00000000-0000-7000-8000-%012d", n)
	}
}

// Run loads every AC1 entity through the pipeline, then re-runs the same extract.
// Loader and reader are in-process (MemLoader) so reconciliation can compare mapped
// output to the target; xref is the caller's store (Postgres in the measured proof).
func Run(ctx context.Context, xref domain.XrefStore, vols Volumes) (Result, error) {
	res := Result{Volumes: vols, Rerun: map[string]domain.Report{}}
	rows := Dataset(vols)
	ids := idGen()
	start := time.Now()
	for _, entity := range entities {
		m, err := Mapping(entity)
		if err != nil {
			return res, err
		}
		loader := domain.NewMemLoader()
		p := &domain.Pipeline{Extractor: recordsExtractor{rows[entity]}, Loader: loader, Reader: loader, Xref: xref, IDGen: ids}
		spec := domain.RunSpec{SourceSystem: SourceSystem, Entity: entity, Mapping: m}
		began := time.Now()
		rep, err := p.Run(ctx, spec)
		if err != nil {
			return res, fmt.Errorf("%s: %w", entity, err)
		}
		res.Entities = append(res.Entities, EntityResult{Entity: entity, Elapsed: time.Since(began), Report: rep})
		again, err := p.Run(ctx, spec)
		if err != nil {
			return res, fmt.Errorf("%s re-run: %w", entity, err)
		}
		res.Rerun[entity] = again
	}
	res.Elapsed = time.Since(start)
	return res, nil
}

type EntityDoc struct {
	Entity         string                  `json:"entity"`
	Elapsed        string                  `json:"elapsed"`
	ElapsedMs      int64                   `json:"elapsedMs"`
	Extracted      int                     `json:"extracted"`
	Created        int                     `json:"created"`
	Updated        int                     `json:"updated"`
	Unchanged      int                     `json:"unchanged"`
	Rejected       int                     `json:"rejected"`
	Status         string                  `json:"status"`
	Reconciliation *domain.ReconcileReport `json:"reconciliation"`
	RerunCreated   int                     `json:"rerunCreated"`
	RerunUpdated   int                     `json:"rerunUpdated"`
	RerunUnchanged int                     `json:"rerunUnchanged"`
}

type Doc struct {
	Criterion string      `json:"criterion"`
	Issue     int         `json:"issue"`
	Budget    string      `json:"budget"`
	Elapsed   string      `json:"elapsed"`
	ElapsedMs int64       `json:"elapsedMs"`
	OK        bool        `json:"ok"`
	Volumes   Volumes     `json:"volumes"`
	Entities  []EntityDoc `json:"entities"`
}

func Document(r Result) Doc {
	doc := Doc{
		Criterion: "AC1",
		Issue:     10,
		Budget:    Budget.String(),
		Elapsed:   r.Elapsed.String(),
		ElapsedMs: r.Elapsed.Milliseconds(),
		OK:        r.OK(),
		Volumes:   r.Volumes,
	}
	for _, e := range r.Entities {
		again := r.Rerun[e.Entity]
		doc.Entities = append(doc.Entities, EntityDoc{
			Entity: e.Entity, Elapsed: e.Elapsed.String(), ElapsedMs: e.Elapsed.Milliseconds(),
			Extracted: e.Report.Extracted, Created: e.Report.Created, Updated: e.Report.Updated,
			Unchanged: e.Report.Unchanged, Rejected: len(e.Report.Rejects), Status: e.Report.Status,
			Reconciliation: e.Report.Reconciliation,
			RerunCreated:   again.Created, RerunUpdated: again.Updated, RerunUnchanged: again.Unchanged,
		})
	}
	return doc
}

func Markdown(d Doc) string {
	var b strings.Builder
	b.WriteString("# Wave 10 AC1 reconciliation\n\n")
	b.WriteString("Seeded AX-shaped load of 50k customers, 20k vendors, 100k items and 10k BOMs ")
	b.WriteString("through the migration pipeline. Persistence is Postgres `migration.xref`; ")
	b.WriteString("the target is the in-process `MemLoader` (owning-service batch APIs are a later slice).\n\n")
	if d.OK {
		b.WriteString("**Result: PASS**\n\n")
	} else {
		b.WriteString("**Result: FAIL**\n\n")
	}
	fmt.Fprintf(&b, "- Elapsed: %s (budget %s)\n", d.Elapsed, d.Budget)
	fmt.Fprintf(&b, "- Volumes: customers %d, vendors %d, items %d, boms %d\n\n", d.Volumes.Customers, d.Volumes.Vendors, d.Volumes.Items, d.Volumes.BOMs)
	for _, e := range d.Entities {
		fmt.Fprintf(&b, "## %s\n\n", e.Entity)
		fmt.Fprintf(&b, "- status %s extracted %d created %d updated %d rejected %d in %s\n", e.Status, e.Extracted, e.Created, e.Updated, e.Rejected, e.Elapsed)
		if e.Reconciliation != nil {
			if e.Reconciliation.OK() {
				b.WriteString("- reconciliation: OK\n")
			} else {
				b.WriteString("- reconciliation: FAIL\n")
			}
			for _, c := range e.Reconciliation.Checks {
				fmt.Fprintf(&b, "  - %s: ok=%t %s\n", c.Name, c.OK, c.Detail)
			}
		} else {
			b.WriteString("- reconciliation: missing\n")
		}
		fmt.Fprintf(&b, "- re-run: created %d updated %d unchanged %d\n\n", e.RerunCreated, e.RerunUpdated, e.RerunUnchanged)
	}
	return b.String()
}
