package features

import (
	"fmt"
	"sort"
	"strings"
)

type Feature struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Module      string   `json:"module"`
	DependsOn   []string `json:"dependsOn"`
}

type Template struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Features    []string `json:"features"`
}

var catalog = []Feature{
	{Key: "finance", Name: "Finance", Module: "finance", Description: "General ledger, journals and period close."},
	{Key: "multi-currency", Name: "Multiple currencies", Module: "finance", DependsOn: []string{"finance"}, Description: "Post and report in more than one currency, with exchange rates."},
	{Key: "intercompany", Name: "Intercompany", Module: "intercompany", DependsOn: []string{"finance"}, Description: "Trade and settle between your own legal entities."},
	{Key: "sales", Name: "Sales and orders", Module: "sales", DependsOn: []string{"finance"}, Description: "Customers, quotes, sales orders and invoicing."},
	{Key: "purchasing", Name: "Purchasing", Module: "purchasing", DependsOn: []string{"finance"}, Description: "Vendors, purchase orders and vendor invoices."},
	{Key: "landed-cost", Name: "Landed cost", Module: "purchasing", DependsOn: []string{"purchasing", "inventory"}, Description: "Spread freight, duty and handling over the cost of received goods."},
	{Key: "inventory", Name: "Inventory", Module: "inventory", DependsOn: []string{"finance"}, Description: "Items, stock levels, valuation and counts."},
	{Key: "batch-tracking", Name: "Batch tracking", Module: "inventory", DependsOn: []string{"inventory"}, Description: "Track stock by batch or lot, including expiry."},
	{Key: "serial-tracking", Name: "Serial number tracking", Module: "inventory", DependsOn: []string{"inventory"}, Description: "Track each unit by its own serial number."},
	{Key: "catch-weight", Name: "Catch weight", Module: "inventory", DependsOn: []string{"inventory"}, Description: "Sell and stock items whose weight varies from unit to unit."},
	{Key: "consignment", Name: "Consignment stock", Module: "inventory", DependsOn: []string{"inventory", "purchasing"}, Description: "Hold stock that a vendor still owns until you use it."},
	{Key: "warehouse", Name: "Warehouse operations", Module: "warehouse", DependsOn: []string{"inventory"}, Description: "Receiving, put-away, picking and shipping work."},
	{Key: "manufacturing", Name: "Discrete manufacturing", Module: "manufacturing", DependsOn: []string{"inventory"}, Description: "Bills of material, routings and production orders."},
	{Key: "process-manufacturing", Name: "Process manufacturing", Module: "process-manufacturing", DependsOn: []string{"inventory", "batch-tracking"}, Description: "Formulas, batch orders, co-products and batch records."},
	{Key: "projects", Name: "Projects and services", Module: "projects", DependsOn: []string{"finance"}, Description: "Project budgets, time, expenses and billing."},
	{Key: "payroll", Name: "Payroll", Module: "payroll", DependsOn: []string{"finance"}, Description: "Pay runs, deductions and payslips."},
	{Key: "edi", Name: "Electronic data interchange", Module: "edi", DependsOn: []string{"sales"}, Description: "Exchange orders and invoices with trading partners."},
	{Key: "edifact", Name: "EDIFACT messages", Module: "edi", DependsOn: []string{"edi"}, Description: "Use the EDIFACT message standard with trading partners."},
}

var distributionSet = []string{"finance", "multi-currency", "sales", "purchasing", "landed-cost", "inventory", "batch-tracking", "serial-tracking", "consignment", "warehouse"}

var templates = []Template{
	{Key: "distribution", Name: "Distribution", Description: "Buy, stock and sell goods. No manufacturing, payroll or EDI.", Features: distributionSet},
	{Key: "manufacturing", Name: "Manufacturing", Description: "Distribution plus discrete manufacturing.", Features: append(append([]string{}, distributionSet...), "manufacturing")},
	{Key: "process-manufacturing", Name: "Process manufacturing", Description: "Manufacturing plus formulas, batches and catch weight.", Features: append(append([]string{}, distributionSet...), "manufacturing", "process-manufacturing", "catch-weight")},
	{Key: "services", Name: "Services", Description: "Sell time and projects. No stock, manufacturing or EDI.", Features: []string{"finance", "multi-currency", "sales", "projects", "payroll"}},
}

func Catalog() []Feature {
	out := make([]Feature, len(catalog))
	copy(out, catalog)
	return out
}

func Templates() []Template {
	out := make([]Template, len(templates))
	copy(out, templates)
	return out
}

func Lookup(key string) (Feature, bool) {
	for _, f := range catalog {
		if f.Key == key {
			return f, true
		}
	}
	return Feature{}, false
}

func Resolve(stored map[string]bool) map[string]bool {
	out := make(map[string]bool, len(catalog))
	for _, f := range catalog {
		on, ok := stored[f.Key]
		out[f.Key] = !ok || on
	}
	return out
}

func TemplateState(key string) (map[string]bool, bool) {
	for _, t := range templates {
		if t.Key != key {
			continue
		}
		out := make(map[string]bool, len(catalog))
		for _, f := range catalog {
			out[f.Key] = false
		}
		for _, k := range t.Features {
			out[k] = true
		}
		return out, true
	}
	return nil, false
}

type Error struct{ Fields map[string]string }

func (e *Error) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	msgs := make([]string, len(keys))
	for i, k := range keys {
		msgs[i] = e.Fields[k]
	}
	return strings.Join(msgs, " ")
}

func Apply(current, changes map[string]bool) (map[string]bool, error) {
	unknown := map[string]string{}
	for k := range changes {
		if _, ok := Lookup(k); !ok {
			unknown[k] = "is not a feature"
		}
	}
	if len(unknown) > 0 {
		return nil, &Error{Fields: unknown}
	}
	next := Resolve(current)
	for k, on := range changes {
		next[k] = on
	}
	blocked := map[string]string{}
	dependents := map[string][]string{}
	for _, f := range catalog {
		if !next[f.Key] {
			continue
		}
		for _, dep := range f.DependsOn {
			if next[dep] {
				continue
			}
			depFeature, _ := Lookup(dep)
			if _, touched := changes[f.Key]; touched {
				blocked[f.Key] = fmt.Sprintf("Turn on %s first: %s needs it.", depFeature.Name, f.Name)
			} else {
				dependents[dep] = append(dependents[dep], f.Name)
			}
		}
	}
	for dep, names := range dependents {
		depFeature, _ := Lookup(dep)
		blocked[dep] = fmt.Sprintf("%s cannot be turned off while %s use it. Turn those off first.", depFeature.Name, strings.Join(names, ", "))
	}
	if len(blocked) > 0 {
		return nil, &Error{Fields: blocked}
	}
	return next, nil
}
