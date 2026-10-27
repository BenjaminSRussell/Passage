package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

func createCall(body, key string, duties ...string) call {
	return call{method: http.MethodPost, path: "/v1/production/orders", body: body, duties: duties,
		headers: map[string]string{idempotencyName: key}}
}

// FR-21.20: a reference makes the order a rework order, and only the holder of
// the Create rework orders duty may create one.
func TestCreate_ReworkOrder(t *testing.T) {
	original := ids.New()
	withRef := createBody(func(m map[string]any) {
		m["bom"] = []any{}
		m["reworkReference"] = map[string]any{"type": "order", "id": original}
	})

	f := &fakeStore{}
	_, h := newServer(t, f)
	expectProblem(t, do(h, createCall(withRef, "rw-0", DutyWrite)), http.StatusForbidden, "AUTH-0003")
	if len(f.created) != 0 {
		t.Fatal("a rework order without the rework duty reached the store")
	}

	rec := do(h, createCall(withRef, "rw-1", DutyWrite, DutyRework))
	if rec.Code != http.StatusCreated {
		t.Fatalf("rework create: %d %s", rec.Code, rec.Body.String())
	}
	in := f.created[0]
	if in.OriginType != domain.OriginRework || in.ReworkReference == nil || in.ReworkReference.Type != domain.ReworkRefOrder ||
		in.ReworkReference.ID != original || len(in.Lines) != 0 {
		t.Fatalf("create input: origin %q reference %+v lines %d", in.OriginType, in.ReworkReference, len(in.Lines))
	}

	// A rework order may also stand alone or name a serial.
	bySerial := createBody(func(m map[string]any) {
		m["originType"] = "Rework"
		m["reworkReference"] = map[string]any{"type": "serial", "id": "S-0042"}
	})
	if rec := do(h, createCall(bySerial, "rw-2", DutyWrite, DutyRework)); rec.Code != http.StatusCreated {
		t.Fatalf("serial reference: %d %s", rec.Code, rec.Body.String())
	}
	// The reference is part of the request fingerprint.
	if f.created[0].RequestHash == f.created[1].RequestHash {
		t.Fatal("the request hash ignores the rework reference")
	}
}

func TestCreate_ReworkReferenceValidation(t *testing.T) {
	tests := []struct {
		name  string
		mut   func(m map[string]any)
		field string
	}{
		{"reference on a manual order", func(m map[string]any) {
			m["originType"] = "Manual"
			m["reworkReference"] = map[string]any{"type": "order", "id": ids.New()}
		}, "reworkReference"},
		{"unknown reference type", func(m map[string]any) {
			m["reworkReference"] = map[string]any{"type": "lot", "id": "L-1"}
		}, "reworkReference.type"},
		{"order reference by number", func(m map[string]any) {
			m["reworkReference"] = map[string]any{"type": "order", "id": "PO-000001"}
		}, "reworkReference.id"},
		{"serial with surrounding spaces", func(m map[string]any) {
			m["reworkReference"] = map[string]any{"type": "serial", "id": " S-1"}
		}, "reworkReference.id"},
		{"batch number over 100 characters", func(m map[string]any) {
			m["reworkReference"] = map[string]any{"type": "batch", "id": strings.Repeat("B", 101)}
		}, "reworkReference.id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			p := expectProblem(t, do(h, createCall(createBody(tt.mut), "k", DutyWrite, DutyRework)), http.StatusBadRequest, validationCode)
			if _, ok := p.Errors[tt.field]; !ok {
				t.Fatalf("no error for %s: %v", tt.field, p.Errors)
			}
			if len(f.created) != 0 {
				t.Fatal("an invalid rework order reached the store")
			}
		})
	}
}

func TestList_ReworkFilters(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	original := ids.New()
	rec := do(h, call{method: http.MethodGet, path: "/v1/production/orders?originType=Rework&reworkOf=" + original, duties: []string{DutyRead}})
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if f.listFilter.OriginType != domain.OriginRework || f.listFilter.ReworkOf != original {
		t.Fatalf("filter: %+v", f.listFilter)
	}
	p := expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/orders?originType=Scrap&reworkOf=PO-1",
		duties: []string{DutyRead}}), http.StatusBadRequest, validationCode)
	if p.Errors["originType"] == "" || p.Errors["reworkOf"] == "" {
		t.Fatalf("errors: %v", p.Errors)
	}
}

func asBuiltCall(orderID, body, key string) call {
	return call{method: http.MethodPost, path: "/v1/production/orders/" + orderID + "/as-built", body: body,
		duties: []string{DutyJournalPost}, headers: map[string]string{idempotencyName: key}}
}

func TestAssignAsBuilt(t *testing.T) {
	orderID, motor := ids.New(), ids.New()
	body := `{"finishedSerialOrBatch":"S-1","componentItemId":"` + motor + `","tracking":"serial","componentSerialOrBatch":"M-1"}`
	f := &fakeStore{}
	_, h := newServer(t, f)

	rec := do(h, asBuiltCall(orderID, body, "ab-1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("assign: %d %s", rec.Code, rec.Body.String())
	}
	in := f.assigned[0]
	if in.OrderID != orderID || in.Actor != "planner-1" || in.IdempotencyKey != "ab-1" || in.RequestHash == "" ||
		in.AsBuilt.FinishedSerial != "S-1" || in.AsBuilt.ComponentSerial != "M-1" || in.AsBuilt.ComponentItemID != motor ||
		in.AsBuilt.Tracking != domain.TrackSerial || !in.AsBuilt.Qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("assign input: %+v", in)
	}

	replay := do(h, asBuiltCall(orderID, body, "ab-1"))
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d replayed=%q", replay.Code, replay.Header().Get("Idempotent-Replayed"))
	}
	other := strings.Replace(body, "M-1", "M-2", 1)
	expectProblem(t, do(h, asBuiltCall(orderID, other, "ab-1")), http.StatusUnprocessableEntity, store.CodeIdempotencyReuse)

	batch := `{"finishedSerialOrBatch":"S-1","componentItemId":"` + motor + `","tracking":"batch","componentSerialOrBatch":"B-7","qty":"2.5"}`
	if rec := do(h, asBuiltCall(orderID, batch, "ab-2")); rec.Code != http.StatusCreated {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body.String())
	}
	if !f.assigned[1].AsBuilt.Qty.Equal(decimal.RequireFromString("2.5")) {
		t.Fatalf("batch quantity: %s", f.assigned[1].AsBuilt.Qty)
	}
}

func TestAssignAsBuilt_Validation(t *testing.T) {
	motor := ids.New()
	tests := []struct {
		name, body, key, field string
	}{
		{"missing idempotency key", `{"finishedSerialOrBatch":"S-1","componentItemId":"` + motor + `","tracking":"serial","componentSerialOrBatch":"M-1"}`, "", idempotencyName},
		{"serial in quantity 2", `{"finishedSerialOrBatch":"S-1","componentItemId":"` + motor + `","tracking":"serial","componentSerialOrBatch":"M-1","qty":"2"}`, "k", "qty"},
		{"batch in quantity 0", `{"finishedSerialOrBatch":"S-1","componentItemId":"` + motor + `","tracking":"batch","componentSerialOrBatch":"B-1","qty":"0"}`, "k", "qty"},
		{"unknown tracking", `{"finishedSerialOrBatch":"S-1","componentItemId":"` + motor + `","tracking":"lot","componentSerialOrBatch":"M-1"}`, "k", "tracking"},
		{"component item by number", `{"finishedSerialOrBatch":"S-1","componentItemId":"MOTOR","tracking":"serial","componentSerialOrBatch":"M-1"}`, "k", "componentItemId"},
		{"missing finished serial", `{"componentItemId":"` + motor + `","tracking":"serial","componentSerialOrBatch":"M-1"}`, "k", "finishedSerialOrBatch"},
		{"component serial with a trailing space", `{"finishedSerialOrBatch":"S-1","componentItemId":"` + motor + `","tracking":"serial","componentSerialOrBatch":"M-1 "}`, "k", "componentSerialOrBatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			c := asBuiltCall(ids.New(), tt.body, tt.key)
			if tt.key == "" {
				c.headers = nil
			}
			p := expectProblem(t, do(h, c), http.StatusBadRequest, validationCode)
			if _, ok := p.Errors[tt.field]; !ok {
				t.Fatalf("no error for %s: %v", tt.field, p.Errors)
			}
			if len(f.assigned) != 0 {
				t.Fatal("an invalid assignment reached the store")
			}
		})
	}
	// A rule the store applies comes back with its status and code.
	f := &fakeStore{asBuiltErr: httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeSerialDuplicate, "taken")}
	_, h := newServer(t, f)
	body := `{"finishedSerialOrBatch":"S-1","componentItemId":"` + motor + `","tracking":"serial","componentSerialOrBatch":"M-1"}`
	expectProblem(t, do(h, asBuiltCall(ids.New(), body, "k")), http.StatusUnprocessableEntity, domain.CodeSerialDuplicate)
}

// AC-21.14: the inquiry runs both ways, as-built of a finished serial and
// where-used of a component serial; it never runs without a filter.
func TestListAsBuilt_Filters(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	for _, path := range []string{
		"/v1/production/as-built?finishedSerialOrBatch=S-2",
		"/v1/production/as-built?componentSerialOrBatch=M-1",
	} {
		if rec := do(h, call{method: http.MethodGet, path: path, duties: []string{DutyRead}}); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if f.asBuiltF[0].FinishedSerialOrBatch != "S-2" || f.asBuiltF[1].ComponentSerialOrBatch != "M-1" {
		t.Fatalf("filters: %+v", f.asBuiltF)
	}
	orderID := ids.New()
	if rec := do(h, call{method: http.MethodGet, path: "/v1/production/orders/" + orderID + "/as-built", duties: []string{DutyRead}}); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("order as-built: %d %s", rec.Code, rec.Body.String())
	}
	if f.asBuiltF[2].OrderID != orderID {
		t.Fatalf("order filter: %+v", f.asBuiltF[2])
	}
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/as-built", duties: []string{DutyRead}}),
		http.StatusBadRequest, validationCode)
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/as-built?orderId=PO-1", duties: []string{DutyRead}}),
		http.StatusBadRequest, validationCode)
	if len(f.asBuiltF) != 3 {
		t.Fatalf("invalid inquiries reached the store: %d", len(f.asBuiltF))
	}
}

func TestDetachAsBuilt(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	id := ids.New()
	rec := do(h, call{method: http.MethodPost, path: "/v1/production/as-built/" + id + ":detach", duties: []string{DutyJournalPost}})
	if rec.Code != http.StatusOK || len(f.detached) != 1 || f.detached[0] != id {
		t.Fatalf("detach: %d %s %v", rec.Code, rec.Body.String(), f.detached)
	}
	expectProblem(t, do(h, call{method: http.MethodPost, path: "/v1/production/as-built/" + id + ":delete", duties: []string{DutyJournalPost}}),
		http.StatusNotFound, unknownActionCode)
	expectProblem(t, do(h, call{method: http.MethodPost, path: "/v1/production/as-built/" + id, duties: []string{DutyJournalPost}}),
		http.StatusNotFound, unknownActionCode)
	if len(f.detached) != 1 {
		t.Fatal("an unknown action reached the store")
	}
}

func TestSaveProfile_ReworkAccountIsOptionalButBounded(t *testing.T) {
	base := `{"itemGroup":"All","siteId":"All","wipAccount":"1400","inventoryAccount":"1300","finishedGoodsAccount":"1350",
		"laborAbsorptionAccount":"5100","overheadAbsorptionAccount":"5200","lotSizeVarianceAccount":"5810","quantityVarianceAccount":"5820",
		"priceVarianceAccount":"5830","substitutionVarianceAccount":"5840"`
	f := &fakeStore{}
	_, h := newServer(t, f)
	put := func(body string) call {
		return call{method: http.MethodPut, path: "/v1/production/posting-profiles", body: body, duties: []string{DutySetupWrite}}
	}
	if rec := do(h, put(base+`}`)); rec.Code != http.StatusCreated {
		t.Fatalf("without rework account: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, put(base+`,"reworkVarianceAccount":"5850"}`)); rec.Code != http.StatusCreated || f.saved[1].Profile.VarRework != "5850" {
		t.Fatalf("with rework account: %d %s", rec.Code, rec.Body.String())
	}
	p := expectProblem(t, do(h, put(base+`,"reworkVarianceAccount":"`+strings.Repeat("9", 65)+`"}`)), http.StatusBadRequest, validationCode)
	if p.Errors["reworkVarianceAccount"] == "" {
		t.Fatalf("errors: %v", p.Errors)
	}
}
