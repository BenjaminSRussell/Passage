package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func TestCreate_CoProducts(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	item := ids.New()
	body := createBody(func(m map[string]any) {
		m["coProducts"] = []any{map[string]any{"lineNo": "C1", "itemId": item, "qtyPer": "0.25"}}
	})
	rec := do(h, call{method: http.MethodPost, path: "/v1/production/orders", body: body, duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "co-1"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	cos := f.created[0].CoProducts
	if len(cos) != 1 || cos[0].ItemID != item || !cos[0].QtyPlanned.Equal(decimal.NewFromInt(25)) {
		t.Fatalf("planned co-products: %+v", cos)
	}

	for name, mut := range map[string]func(m map[string]any){
		"item": func(m map[string]any) {
			m["coProducts"] = []any{map[string]any{"lineNo": "C1", "itemId": "x", "qtyPer": "1"}}
		},
		"quantity": func(m map[string]any) {
			m["coProducts"] = []any{map[string]any{"lineNo": "C1", "itemId": item, "qtyPer": "0"}}
		},
		"line": func(m map[string]any) {
			m["coProducts"] = []any{map[string]any{"lineNo": "", "itemId": item, "qtyPer": "1"}}
		},
		"type": func(m map[string]any) {
			m["coProducts"] = []any{map[string]any{"lineNo": "C1", "type": "scrap", "itemId": item, "qtyPer": "1"}}
		},
		"product itself": func(m map[string]any) {
			m["coProducts"] = []any{map[string]any{"lineNo": "C1", "itemId": m["productId"], "qtyPer": "1"}}
		},
		"listed twice": func(m map[string]any) {
			c := map[string]any{"lineNo": "C1", "itemId": item, "qtyPer": "1"}
			m["coProducts"] = []any{c, c}
		},
		"on rework": func(m map[string]any) {
			m["originType"] = "Rework"
			m["coProducts"] = []any{map[string]any{"lineNo": "C1", "itemId": item, "qtyPer": "1"}}
		},
	} {
		rec := do(h, call{method: http.MethodPost, path: "/v1/production/orders", body: createBody(mut), duties: []string{DutyWrite, DutyRework},
			headers: map[string]string{idempotencyName: "co-bad-" + name}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d: %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(f.created) != 1 {
		t.Fatalf("a refused request reached the store: %d orders", len(f.created))
	}
	// A by-product keeps its type; an untyped line is a co-product.
	by := createBody(func(m map[string]any) {
		m["coProducts"] = []any{
			map[string]any{"lineNo": "B1", "type": "by-product", "itemId": item, "qtyPer": "0.1"},
			map[string]any{"lineNo": "C1", "itemId": ids.New(), "qtyPer": "0.1"},
		}
	})
	rec = do(h, call{method: http.MethodPost, path: "/v1/production/orders", body: by, duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "co-by"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("by-product: %d %s", rec.Code, rec.Body.String())
	}
	if cos := f.created[1].CoProducts; cos[0].Type != "by-product" || cos[1].Type != "co-product" {
		t.Fatalf("types: %+v", cos)
	}
}

// manyCoLines is n distinct co-product receipt lines as JSON array items.
func manyCoLines(n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf(`{"lineNo":"C%d","quantity":"1"}`, i)
	}
	return strings.Join(items, ",")
}

func TestJournal_CoProductLines(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	order := ids.New()
	rec := do(h, journalCall(order, `{"type":"report-as-finished","quantity":"40","coProducts":[{"lineNo":"C1","quantity":"8"}]}`, DutyJournalPost))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	in := f.journals[0]
	if len(in.CoProducts) != 1 || in.CoProducts[0].LineNo != "C1" || !in.CoProducts[0].Qty.Equal(decimal.NewFromInt(8)) {
		t.Fatalf("co-product input: %+v", in.CoProducts)
	}
	for name, body := range map[string]string{
		"not on a picking list": `{"type":"picking-list","coProducts":[{"lineNo":"C1","quantity":"1"}]}`,
		"zero quantity":         `{"type":"report-as-finished","quantity":"4","coProducts":[{"lineNo":"C1","quantity":"0"}]}`,
		"listed twice":          `{"type":"report-as-finished","quantity":"4","coProducts":[{"lineNo":"C1","quantity":"1"},{"lineNo":"C1","quantity":"1"}]}`,
		"too many lines":        `{"type":"report-as-finished","quantity":"4","coProducts":[` + manyCoLines(maxCoProducts+1) + `]}`,
	} {
		c := journalCall(order, body, DutyJournalPost)
		c.headers[idempotencyName] = "co-" + strings.ReplaceAll(name, " ", "-")
		if rec := do(h, c); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", name, rec.Code)
		}
	}
}
