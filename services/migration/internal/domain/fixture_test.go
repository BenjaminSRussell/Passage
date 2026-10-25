package domain_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

type axSet struct {
	Customers, Vendors, Items, BOMs []domain.Record
}

func axDataset(customers, vendors, items, boms int) axSet {
	var s axSet
	for i := 0; i < customers; i++ {
		s.Customers = append(s.Customers, domain.Record{
			Key: fmt.Sprintf("C%06d", i),
			Fields: map[string]any{
				"AccountNum":  fmt.Sprintf("C%06d", i),
				"Name":        fmt.Sprintf("Customer %d", i),
				"CustGroup":   []string{"10", "20", "30"}[i%3],
				"CreditMax":   fmt.Sprintf("%d.50", 1000+i),
				"Blocked":     []string{"0", "1"}[i%2],
				"CreatedDate": "2019-03-14",
			},
		})
	}
	for i := 0; i < vendors; i++ {
		s.Vendors = append(s.Vendors, domain.Record{
			Key:    fmt.Sprintf("V%06d", i),
			Fields: map[string]any{"AccountNum": fmt.Sprintf("V%06d", i), "Name": fmt.Sprintf("Vendor %d", i), "Currency": "USD"},
		})
	}
	for i := 0; i < items; i++ {
		s.Items = append(s.Items, domain.Record{
			Key:    fmt.Sprintf("I%06d", i),
			Fields: map[string]any{"ItemId": fmt.Sprintf("I%06d", i), "ItemName": fmt.Sprintf("Item %d", i), "Weight": int64(i%50) + 1},
		})
	}
	for i := 0; i < boms; i++ {
		s.BOMs = append(s.BOMs, domain.Record{
			Key:    fmt.Sprintf("B%06d", i),
			Fields: map[string]any{"BOMId": fmt.Sprintf("B%06d", i), "ItemId": fmt.Sprintf("I%06d", i%max(items, 1)), "Qty": "2.5"},
		})
	}
	return s
}

const customerMappingJSON = `{"entity":"customer","version":1,"fields":[
 {"source":"AccountNum","target":"accountNumber","type":"string","required":true},
 {"source":"Name","target":"name","type":"string","required":true},
 {"source":"CustGroup","target":"segment","type":"string","enum":{"10":"retail","20":"wholesale","30":"oem"}},
 {"source":"CreditMax","target":"creditLimit","type":"decimal","default":"0"},
 {"source":"Blocked","target":"blocked","type":"bool"},
 {"source":"CreatedDate","target":"createdOn","type":"date"}]}`

func customerMapping(t testing.TB) domain.Mapping {
	t.Helper()
	m, err := domain.ParseMapping([]byte(customerMappingJSON))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

type sliceExtractor struct {
	rows []domain.Record
	err  error
}

func (e sliceExtractor) Extract(_ context.Context, _ string) ([]domain.Record, error) {
	return e.rows, e.err
}

func counterIDs() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("00000000-0000-7000-8000-%012d", n)
	}
}

var entityMappingJSON = map[string]string{
	"customer": customerMappingJSON,
	"vendor":   `{"entity":"vendor","version":1,"fields":[{"source":"AccountNum","target":"accountNumber","type":"string","required":true},{"source":"Name","target":"name","type":"string"},{"source":"Currency","target":"currency","type":"string","required":true}]}`,
	"item":     `{"entity":"item","version":1,"fields":[{"source":"ItemId","target":"sku","type":"string","required":true},{"source":"ItemName","target":"name","type":"string"},{"source":"Weight","target":"weight","type":"int"}]}`,
	"bom":      `{"entity":"bom","version":1,"fields":[{"source":"BOMId","target":"bomNumber","type":"string","required":true},{"source":"ItemId","target":"parentSku","type":"string","required":true},{"source":"Qty","target":"quantity","type":"decimal","required":true}]}`,
}

func newPipelines(set axSet) map[string]*domain.Pipeline {
	rows := map[string][]domain.Record{"customer": set.Customers, "vendor": set.Vendors, "item": set.Items, "bom": set.BOMs}
	pipelines := map[string]*domain.Pipeline{}
	for entity, r := range rows {
		pipelines[entity], _, _ = newPipeline(r)
	}
	return pipelines
}

func loadAll(t testing.TB, pipelines map[string]*domain.Pipeline) map[string]domain.Report {
	t.Helper()
	reports := map[string]domain.Report{}
	for entity, p := range pipelines {
		m, err := domain.ParseMapping([]byte(entityMappingJSON[entity]))
		if err != nil {
			t.Fatal(err)
		}
		rep, err := p.Run(context.Background(), domain.RunSpec{SourceSystem: system, Entity: entity, Mapping: m})
		if err != nil {
			t.Fatalf("%s: %v", entity, err)
		}
		reports[entity] = rep
	}
	return reports
}
