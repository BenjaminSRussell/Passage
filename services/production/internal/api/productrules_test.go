package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/productclient"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
	"github.com/shopspring/decimal"
)

func qty(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// productServer returns a server whose product fake holds one default for (released, site).
func productServer(t *testing.T, f *fakeStore, released, site string, def productclient.SiteDefault) (http.Handler, *productclient.Fake) {
	t.Helper()
	fake := &productclient.Fake{Defaults: map[[2]string]productclient.SiteDefault{{released, site}: def}}
	a := New(f, Config{})
	a.WithProducts(fake)
	r := httpx.NewRouter()
	a.Register(r)
	return r, fake
}

func TestCreate_ReadsTheQuantityRulesFromTheReleasedProduct(t *testing.T) {
	released, site := ids.New(), ids.New()
	def := productclient.SiteDefault{MinQty: qty("50"), MultipleQty: qty("25"), StdQty: qty("75")}
	cases := []struct {
		name   string
		mut    map[string]any
		status int
		code   string
		wantQ  string
	}{
		{"meets the product's rules", map[string]any{"quantity": "100"}, http.StatusCreated, "", "100"},
		{"below the product's minimum", map[string]any{"quantity": "25"}, http.StatusUnprocessableEntity, domain.CodeOrderQuantity, ""},
		{"not a multiple of the product's", map[string]any{"quantity": "60"}, http.StatusUnprocessableEntity, domain.CodeOrderQuantity, ""},
		{"quantity omitted takes the standard quantity", map[string]any{"quantity": nil}, http.StatusCreated, "", "75"},
		{"a smaller request minimum does not loosen the product's", map[string]any{"quantity": "25", "minimumQty": "5"}, http.StatusUnprocessableEntity, domain.CodeOrderQuantity, ""},
		{"another request multiple does not loosen the product's", map[string]any{"quantity": "60", "minimumQty": "10", "multiple": "20"}, http.StatusUnprocessableEntity, domain.CodeOrderQuantity, ""},
		{"a request minimum can tighten the product's", map[string]any{"quantity": "75", "minimumQty": "100"}, http.StatusUnprocessableEntity, domain.CodeOrderQuantity, ""},
		{"request and product rules both met", map[string]any{"quantity": "100", "minimumQty": "80", "multiple": "50"}, http.StatusCreated, "", "100"},
		{"the maximum comes from the request", map[string]any{"quantity": "100", "maximumQty": "90"}, http.StatusUnprocessableEntity, domain.CodeOrderQuantity, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeStore{}
			h, _ := productServer(t, f, released, site, def)
			rec := do(h, call{method: http.MethodPost, path: "/v1/production/orders", duties: []string{DutyWrite},
				headers: map[string]string{idempotencyName: "pr-" + c.name},
				body: createBody(func(m map[string]any) {
					m["siteId"], m["releasedProductId"] = site, released
					for k, v := range c.mut {
						if v == nil {
							delete(m, k)
						} else {
							m[k] = v
						}
					}
				})})
			if c.code != "" {
				expectProblem(t, rec, c.status, c.code)
				if len(f.created) != 0 {
					t.Fatal("a refused order must not reach the store")
				}
				return
			}
			if rec.Code != c.status || len(f.created) != 1 || !f.created[0].Quantity.Equal(qty(c.wantQ)) {
				t.Fatalf("got %d, created %+v: %s", rec.Code, f.created, rec.Body.String())
			}
		})
	}
}

func TestCreate_ReleasedProductReadFailures(t *testing.T) {
	released, site := ids.New(), ids.New()
	body := func(m map[string]any) { m["siteId"], m["releasedProductId"] = site, released }
	post := func(h http.Handler, key string) *httptest.ResponseRecorder {
		return do(h, call{method: http.MethodPost, path: "/v1/production/orders", duties: []string{DutyWrite},
			headers: map[string]string{idempotencyName: key}, body: createBody(body)})
	}
	t.Run("product service not configured", func(t *testing.T) {
		_, h := newServer(t, &fakeStore{})
		expectProblem(t, post(h, "np-1"), http.StatusServiceUnavailable, CodeProductUnavailable)
	})
	t.Run("unknown released product", func(t *testing.T) {
		f := &fakeStore{}
		h, _ := productServer(t, f, ids.New(), site, productclient.SiteDefault{})
		expectProblem(t, post(h, "np-2"), http.StatusUnprocessableEntity, "PRODUCTION_INVALID_INPUT")
	})
	t.Run("product service down", func(t *testing.T) {
		f := &fakeStore{}
		h, fake := productServer(t, f, released, site, productclient.SiteDefault{})
		fake.Err = productclient.ErrUnavailable
		expectProblem(t, post(h, "np-3"), http.StatusServiceUnavailable, CodeProductUnavailable)
		if len(f.created) != 0 {
			t.Fatal("nothing may be created")
		}
	})
	t.Run("no default for the site means no rules", func(t *testing.T) {
		f := &fakeStore{}
		h, fake := productServer(t, f, released, site, productclient.SiteDefault{})
		fake.Err = productclient.ErrNoDefault
		if rec := post(h, "np-4"); rec.Code != http.StatusCreated {
			t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("malformed id", func(t *testing.T) {
		_, h := newServer(t, &fakeStore{})
		rec := do(h, call{method: http.MethodPost, path: "/v1/production/orders", duties: []string{DutyWrite},
			headers: map[string]string{idempotencyName: "np-5"}, body: createBody(func(m map[string]any) { m["releasedProductId"] = "x" })})
		expectProblem(t, rec, http.StatusBadRequest, validationCode)
	})
}

func TestCreate_OmittedQuantityWithoutAStandardQuantityIsInvalid(t *testing.T) {
	released, site := ids.New(), ids.New()
	f := &fakeStore{}
	h, _ := productServer(t, f, released, site, productclient.SiteDefault{MinQty: qty("5")})
	rec := do(h, call{method: http.MethodPost, path: "/v1/production/orders", duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "std-0"},
		body:    createBody(func(m map[string]any) { m["siteId"], m["releasedProductId"] = site, released; delete(m, "quantity") })})
	expectProblem(t, rec, http.StatusBadRequest, validationCode)
}

func TestPatchAndSplit_ReadTheRulesFromTheReleasedProduct(t *testing.T) {
	released, site := ids.New(), ids.New()
	def := productclient.SiteDefault{MinQty: qty("10"), MultipleQty: qty("5")}
	order := store.Order{ID: ids.New(), LegalEntityID: entityID, SiteID: site}
	t.Run("patch within the multiple", func(t *testing.T) {
		f := &fakeStore{order: order}
		h, _ := productServer(t, f, released, site, def)
		rec := do(h, call{method: http.MethodPatch, path: "/v1/production/orders/" + order.ID, duties: []string{DutyWrite},
			headers: map[string]string{"If-Match": `"3"`}, body: `{"quantity":"60","releasedProductId":"` + released + `"}`})
		if rec.Code != http.StatusOK || len(f.updates) != 1 {
			t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("patch breaking the multiple", func(t *testing.T) {
		f := &fakeStore{order: order}
		h, _ := productServer(t, f, released, site, def)
		rec := do(h, call{method: http.MethodPatch, path: "/v1/production/orders/" + order.ID, duties: []string{DutyWrite},
			headers: map[string]string{"If-Match": `"3"`}, body: `{"quantity":"62","releasedProductId":"` + released + `"}`})
		expectProblem(t, rec, http.StatusUnprocessableEntity, domain.CodeOrderQuantity)
	})
	t.Run("split passes the product's minimum and multiple to the store", func(t *testing.T) {
		f := &fakeStore{order: order}
		h, _ := productServer(t, f, released, site, def)
		rec := do(h, splitCall(order.ID, `{"splitQty":"40","releasedProductId":"`+released+`"}`, DutyRelease))
		if rec.Code != http.StatusCreated || len(f.splits) != 1 || !f.splits[0].MinQty.Equal(qty("10")) || !f.splits[0].Multiple.Equal(qty("5")) {
			t.Fatalf("got %d %+v: %s", rec.Code, f.splits, rec.Body.String())
		}
	})
}

// A retry of an order already created returns the stored order even while the product service is down: the
// replay never reads the product (and a reused key with another body is still refused).
func TestCreate_ReplayDoesNotReadTheProduct(t *testing.T) {
	released, site := ids.New(), ids.New()
	f := &fakeStore{}
	h, fake := productServer(t, f, released, site, productclient.SiteDefault{StdQty: qty("75")})
	body := createBody(func(m map[string]any) {
		m["siteId"], m["releasedProductId"] = site, released
		delete(m, "quantity")
	})
	post := func(body string) *httptest.ResponseRecorder {
		return do(h, call{method: http.MethodPost, path: "/v1/production/orders", duties: []string{DutyWrite},
			headers: map[string]string{idempotencyName: "replay-1"}, body: body})
	}
	if rec := post(body); rec.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", rec.Code, rec.Body.String())
	}
	fake.Err, fake.Calls = productclient.ErrUnavailable, 0
	rec := post(body)
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" || fake.Calls != 0 || len(f.created) != 1 {
		t.Fatalf("replay: %d replayed=%q product calls=%d created=%d: %s", rec.Code, rec.Header().Get("Idempotent-Replayed"), fake.Calls, len(f.created), rec.Body.String())
	}
	expectProblem(t, post(createBody(func(m map[string]any) { m["siteId"], m["releasedProductId"] = site, released })), http.StatusUnprocessableEntity, store.CodeIdempotencyReuse)
	if fake.Calls != 0 {
		t.Fatalf("a reused key must not read the product, got %d calls", fake.Calls)
	}
}

func TestSplit_ProductRulesCannotBeLoosenedAndReplaysSkipTheProduct(t *testing.T) {
	released, site := ids.New(), ids.New()
	def := productclient.SiteDefault{MinQty: qty("10"), MultipleQty: qty("5")}
	order := store.Order{ID: ids.New(), LegalEntityID: entityID, SiteID: site}
	cases := []struct {
		name, body, code  string
		wantMin, wantMult string
	}{
		{"a smaller minimum takes the product's", `{"splitQty":"40","minimumQty":"2","multiple":"5"}`, "", "10", "5"},
		{"a larger minimum tightens", `{"splitQty":"40","minimumQty":"20"}`, "", "20", "5"},
		{"another multiple still binds", `{"splitQty":"40","multiple":"15"}`, domain.CodeSplitQuantity, "", ""},
		{"a multiple of both", `{"splitQty":"30","multiple":"15"}`, "", "10", "5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeStore{order: order}
			h, _ := productServer(t, f, released, site, def)
			body := c.body[:len(c.body)-1] + `,"releasedProductId":"` + released + `"}`
			rec := do(h, splitCall(order.ID, body, DutyRelease))
			if c.code != "" {
				expectProblem(t, rec, http.StatusUnprocessableEntity, c.code)
				if len(f.splits) != 0 {
					t.Fatal("a refused split must not reach the store")
				}
				return
			}
			if rec.Code != http.StatusCreated || len(f.splits) != 1 || !f.splits[0].MinQty.Equal(qty(c.wantMin)) || !f.splits[0].Multiple.Equal(qty(c.wantMult)) {
				t.Fatalf("got %d %+v: %s", rec.Code, f.splits, rec.Body.String())
			}
		})
	}
	t.Run("replay with the product down", func(t *testing.T) {
		f := &fakeStore{order: order, replayHash: map[string]string{"split-1": "stored"}}
		h, fake := productServer(t, f, released, site, def)
		fake.Err = productclient.ErrUnavailable
		rec := do(h, splitCall(order.ID, `{"splitQty":"40","releasedProductId":"`+released+`"}`, DutyRelease))
		if rec.Code == http.StatusServiceUnavailable || fake.Calls != 0 || len(f.splits) != 1 {
			t.Fatalf("a replay must reach the store without reading the product: %d calls=%d: %s", rec.Code, fake.Calls, rec.Body.String())
		}
	})
	t.Run("releasedProductId is part of the request hash", func(t *testing.T) {
		f := &fakeStore{order: order}
		h, _ := productServer(t, f, released, site, def)
		_ = do(h, splitCall(order.ID, `{"splitQty":"40"}`, DutyRelease))
		_ = do(h, splitCall(order.ID, `{"splitQty":"40","releasedProductId":"`+released+`"}`, DutyRelease))
		if len(f.splits) != 2 || f.splits[0].RequestHash == f.splits[1].RequestHash {
			t.Fatalf("the hashes must differ: %+v", f.splits)
		}
	})
}

// An order remembers the released product it was created with, so the product's rules bind a later change
// even when the caller leaves the id out (FR-21.1), and a different product cannot be named to loosen them.
func TestCreate_StoresTheReleasedProduct(t *testing.T) {
	released, site := ids.New(), ids.New()
	f := &fakeStore{}
	h, _ := productServer(t, f, released, site, productclient.SiteDefault{MinQty: qty("5")})
	rec := do(h, call{method: http.MethodPost, path: "/v1/production/orders", duties: []string{DutyWrite},
		headers: map[string]string{idempotencyName: "keep-1"},
		body:    createBody(func(m map[string]any) { m["siteId"], m["releasedProductId"] = site, released })})
	if rec.Code != http.StatusCreated || len(f.created) != 1 || f.created[0].ReleasedProductID != released {
		t.Fatalf("got %d, created %+v: %s", rec.Code, f.created, rec.Body.String())
	}
}

func TestPatchAndSplit_StoredReleasedProductBindsWithoutTheId(t *testing.T) {
	released, site := ids.New(), ids.New()
	def := productclient.SiteDefault{MinQty: qty("10"), MultipleQty: qty("5")}
	order := store.Order{ID: ids.New(), LegalEntityID: entityID, SiteID: site, ReleasedProductID: &released}
	patch := func(h http.Handler, body string) *httptest.ResponseRecorder {
		return do(h, call{method: http.MethodPatch, path: "/v1/production/orders/" + order.ID, duties: []string{DutyWrite},
			headers: map[string]string{"If-Match": `"3"`}, body: body})
	}
	t.Run("patch is checked without the id in the body", func(t *testing.T) {
		f := &fakeStore{order: order}
		h, _ := productServer(t, f, released, site, def)
		expectProblem(t, patch(h, `{"quantity":"62"}`), http.StatusUnprocessableEntity, domain.CodeOrderQuantity)
		if len(f.updates) != 0 {
			t.Fatal("a refused quantity must not reach the store")
		}
		if rec := patch(h, `{"quantity":"60"}`); rec.Code != http.StatusOK || len(f.updates) != 1 {
			t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a delivery date change reads no product", func(t *testing.T) {
		f := &fakeStore{order: order}
		h, fake := productServer(t, f, released, site, def)
		if rec := patch(h, `{"deliveryDate":"2027-01-31"}`); rec.Code != http.StatusOK || fake.Calls != 0 {
			t.Fatalf("got %d, product calls %d: %s", rec.Code, fake.Calls, rec.Body.String())
		}
	})
	t.Run("another released product is refused", func(t *testing.T) {
		f := &fakeStore{order: order}
		h, _ := productServer(t, f, released, site, def)
		rec := patch(h, `{"quantity":"60","releasedProductId":"`+ids.New()+`"}`)
		expectProblem(t, rec, http.StatusBadRequest, validationCode)
		if !strings.Contains(rec.Body.String(), "releasedProductId") {
			t.Fatalf("body: %s", rec.Body.String())
		}
	})
	t.Run("split takes the stored product's rules", func(t *testing.T) {
		f := &fakeStore{order: order}
		h, _ := productServer(t, f, released, site, def)
		rec := do(h, splitCall(order.ID, `{"splitQty":"40"}`, DutyRelease))
		if rec.Code != http.StatusCreated || len(f.splits) != 1 || !f.splits[0].MinQty.Equal(qty("10")) || !f.splits[0].Multiple.Equal(qty("5")) {
			t.Fatalf("got %d %+v: %s", rec.Code, f.splits, rec.Body.String())
		}
	})
	t.Run("an order without a product keeps the request's rules", func(t *testing.T) {
		f := &fakeStore{order: store.Order{ID: order.ID, LegalEntityID: entityID, SiteID: site}}
		h, fake := productServer(t, f, released, site, def)
		if rec := patch(h, `{"quantity":"62"}`); rec.Code != http.StatusOK || fake.Calls != 0 {
			t.Fatalf("got %d, product calls %d: %s", rec.Code, fake.Calls, rec.Body.String())
		}
	})
}

func TestMerge_SummedQuantityMeetsTheProductRules(t *testing.T) {
	released, other, site := ids.New(), ids.New(), ids.New()
	a, b, c := ids.New(), ids.New(), ids.New()
	mk := func(id, q string, rp *string) store.Order {
		return store.Order{ID: id, OrderNumber: "PO-" + q, LegalEntityID: entityID, SiteID: site, Quantity: qty(q), ReleasedProductID: rp}
	}
	body := func(x, y string) string {
		return `{"orders":[{"orderId":"` + x + `","version":2},{"orderId":"` + y + `","version":1}]}`
	}
	t.Run("the sum meets the rules", func(t *testing.T) {
		f := &fakeStore{orders: map[string]store.Order{a: mk(a, "40", &released), b: mk(b, "35", &released)}}
		h, _ := productServer(t, f, released, site, productclient.SiteDefault{MinQty: qty("50"), MultipleQty: qty("5")})
		if rec := do(h, mergeCall(body(a, b), DutyRelease)); rec.Code != http.StatusOK || len(f.merges) != 1 {
			t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("the product's multiple changed since the orders were made", func(t *testing.T) {
		f := &fakeStore{orders: map[string]store.Order{a: mk(a, "40", &released), b: mk(b, "35", &released)}}
		h, _ := productServer(t, f, released, site, productclient.SiteDefault{MultipleQty: qty("20")})
		expectProblem(t, do(h, mergeCall(body(a, b), DutyRelease)), http.StatusUnprocessableEntity, domain.CodeOrderQuantity)
		if len(f.merges) != 0 {
			t.Fatal("a refused merge must not reach the store")
		}
	})
	t.Run("orders of different released products", func(t *testing.T) {
		f := &fakeStore{orders: map[string]store.Order{a: mk(a, "40", &released), b: mk(b, "35", &other)}}
		h, _ := productServer(t, f, released, site, productclient.SiteDefault{})
		expectProblem(t, do(h, mergeCall(body(a, b), DutyRelease)), http.StatusUnprocessableEntity, domain.CodeMergeRefused)
	})
	t.Run("an order without a product joins the one that has it", func(t *testing.T) {
		f := &fakeStore{orders: map[string]store.Order{a: mk(a, "40", nil), b: mk(b, "35", &released), c: mk(c, "5", nil)}}
		h, _ := productServer(t, f, released, site, productclient.SiteDefault{MultipleQty: qty("20")})
		expectProblem(t, do(h, mergeCall(body(a, b), DutyRelease)), http.StatusUnprocessableEntity, domain.CodeOrderQuantity)
		if rec := do(h, mergeCall(body(a, c), DutyRelease)); rec.Code != http.StatusOK {
			t.Fatalf("orders without a product are not checked: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("an order holding a product needs the product service", func(t *testing.T) {
		f := &fakeStore{orders: map[string]store.Order{a: mk(a, "40", nil), b: mk(b, "35", &released)}}
		_, h := newServer(t, f)
		expectProblem(t, do(h, mergeCall(body(a, b), DutyRelease)), http.StatusServiceUnavailable, CodeProductUnavailable)
		if len(f.merges) != 0 {
			t.Fatal("a merge whose rules could not be read must not reach the store")
		}
	})
	t.Run("a replay reads no product", func(t *testing.T) {
		f := &fakeStore{replayHash: map[string]string{"merge-1": "x"}, orders: map[string]store.Order{a: mk(a, "40", &released), b: mk(b, "35", &released)}}
		h, fake := productServer(t, f, released, site, productclient.SiteDefault{MultipleQty: qty("20")})
		if rec := do(h, mergeCall(body(a, b), DutyRelease)); rec.Code != http.StatusOK || fake.Calls != 0 {
			t.Fatalf("got %d, product calls %d: %s", rec.Code, fake.Calls, rec.Body.String())
		}
	})
}
