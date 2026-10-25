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
