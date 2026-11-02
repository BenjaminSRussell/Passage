package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/api"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/target"
)

const (
	obPath      = "/v1/integration/outbound-mappings"
	obWrite     = "migration.run,migration.project.write,tenant.administer"
	obRead      = "migration.run,tenant.administer"
	hubLegalEnt = "00000000-0000-7000-8000-0000000000a1"
)

const obDefinition = `{
  "eventType": "dev.passage.sales.order.*.v1",
  "subject": {"entity": "salesOrder", "idPath": "data.order_id"},
  "xrefs": [{"name": "customer", "entity": "customer", "idPath": "data.customer_id"}],
  "mapping": {"entity": "SalesOrder", "fields": [
    {"source": "xref.customer", "target": "CustAccount", "type": "string", "required": true},
    {"source": "data.number", "target": "Header.CustomerRef", "type": "string"}
  ]}
}`

// obRouter serves the API with the integration hub's service principal injected, as the verified service token
// would: one legal entity, only the render and record duties, issuer integration-hub.
func obRouter(s store.Store, hub tenant.Principal) *chi.Mux {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get("X-Hub") != "" {
				next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), hub)))
				return
			}
			p := tenant.Principal{TenantID: testkit.TenantID(1), UserID: "ben", Duties: map[string]struct{}{}}
			for _, d := range strings.Split(req.Header.Get("X-Passage-Duties"), ",") {
				p.Duties[d] = struct{}{}
			}
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), p)))
		})
	})
	api.New(s, api.NotConfigured).Register(r)
	return r
}

func hubPrincipal(tenantNum int) tenant.Principal {
	return tenant.Principal{TenantID: testkit.TenantID(tenantNum), LegalEntityID: hubLegalEnt, UserID: "service:integration-hub", Service: "integration-hub",
		Duties: map[string]struct{}{"migration.outbound.render": {}, "migration.xref.record": {}}}
}

func hubCall(r http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("X-Hub", "1")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

type obBody struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Version    int             `json:"version"`
	Status     string          `json:"status"`
	EventType  string          `json:"eventType"`
	Definition json.RawMessage `json:"definition"`
}

func createOutbound(t *testing.T, r http.Handler, name string) obBody {
	t.Helper()
	payload := `{"name":"` + name + `","sourceSystem":"custom_acme","definition":` + obDefinition + `}`
	rec := do(r, http.MethodPost, obPath, payload, obWrite, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var b obBody
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	return b
}

// The event exactly as the hub's cloudEventOf builds it (services/integration-hub-ts/src/migrationClient.ts).
func hubEvent(eventID, orderID, customerID string) string {
	return `{"specversion":"1.0","id":"` + eventID + `","source":"/passage/integration-hub","type":"dev.passage.sales.order.confirmed.v1","time":"2026-11-01T10:00:00.000Z","subject":"` + orderID + `","passagesequence":4,"legalentityid":"` + hubLegalEnt + `","data":{"order_id":"` + orderID + `","customer_id":"` + customerID + `","number":"SO-9"}}`
}

func seedCustomer(t *testing.T, s *store.Mem, legacyKey string) string {
	t.Helper()
	id := ids.New()
	if err := s.Xref().Put(principalCtx(), domain.XrefKey{SourceSystem: "custom_acme", Entity: "customer", SourceKey: legacyKey}, domain.XrefEntry{TargetID: id}); err != nil {
		t.Fatal(err)
	}
	return id
}

func principalCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

func TestOutbound_RenderHappyPathAndContract(t *testing.T) {
	s := store.NewMem()
	r := obRouter(s, hubPrincipal(1))
	m := createOutbound(t, r, "Orders to Acme")
	cust, order := seedCustomer(t, s, "C-1001"), ids.New()

	rec := hubCall(r, obPath+"/"+m.ID+":render", `{"event":`+hubEvent("evt-1", order, cust)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("render = %d %s", rec.Code, rec.Body)
	}
	// Decoded the way the hub's httpMigrationClient does: body, idempotencyKey as a string, refs passed on untouched.
	var out struct {
		Body           map[string]any `json:"body"`
		IdempotencyKey string         `json:"idempotencyKey"`
		Refs           []struct {
			Entity string `json:"entity"`
			ID     string `json:"id"`
		} `json:"refs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	header, _ := out.Body["Header"].(map[string]any)
	if out.Body["CustAccount"] != "C-1001" || header["CustomerRef"] != "SO-9" || out.IdempotencyKey == "" || len(out.Refs) != 1 || out.Refs[0].Entity != "salesOrder" || out.Refs[0].ID != order {
		t.Fatalf("rendered = %s", rec.Body)
	}

	// Determinism: the same event renders the same key and body, again and again; a new event changes the key.
	again := hubCall(r, obPath+"/"+m.ID+":render", `{"event":`+hubEvent("evt-1", order, cust)+`}`)
	if again.Body.String() != rec.Body.String() {
		t.Fatalf("render must repeat exactly:\n%s\n%s", rec.Body, again.Body)
	}
	other := hubCall(r, obPath+"/"+m.ID+":render", `{"event":`+hubEvent("evt-2", order, cust)+`}`)
	var o2 struct{ IdempotencyKey string }
	_ = json.Unmarshal(other.Body.Bytes(), &o2)
	if o2.IdempotencyKey == out.IdempotencyKey {
		t.Fatal("another event must get another idempotency key")
	}
	if puts := s.Xref().(*domain.MemXref).Puts; puts != 1 {
		t.Fatalf("render must not write the cross-reference: %d puts", puts)
	}

	// The legacy ERP answers with its key; xref:record stores it; the next render of the same order is an update.
	rec = hubCall(r, "/v1/integration/xref:record", `{"outboundMappingId":"`+m.ID+`","refs":[{"entity":"salesOrder","id":"`+order+`"}],"legacyKey":"SO-77","eventId":"evt-1"}`)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("record = %d %s", rec.Code, rec.Body)
	}
	if rec = hubCall(r, "/v1/integration/xref:record", `{"outboundMappingId":"`+m.ID+`","refs":[{"entity":"salesOrder","id":"`+order+`"}],"legacyKey":"SO-77","eventId":"evt-1"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("a repeat of the same event must also be 204: %d %s", rec.Code, rec.Body)
	}
	if keys, _ := s.LegacyKeys(principalCtx(), "custom_acme", "salesOrder", []string{order}); keys[order] != "SO-77" {
		t.Fatalf("recorded key does not resolve: %+v", keys)
	}
}

func TestOutbound_UnresolvedRefIs422WithTheList(t *testing.T) {
	s := store.NewMem()
	r := obRouter(s, hubPrincipal(1))
	m := createOutbound(t, r, "Orders")
	missing := ids.New() // a customer no load ever created
	rec := hubCall(r, obPath+"/"+m.ID+":render", `{"event":`+hubEvent("evt-1", ids.New(), missing)+`}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("render = %d %s", rec.Code, rec.Body)
	}
	var p struct {
		Code       string                 `json:"code"`
		Detail     string                 `json:"detail"`
		Errors     map[string]string      `json:"errors"`
		Unresolved []domain.UnresolvedRef `json:"unresolved"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.Code != "MIG-XREF-UNRESOLVED" || !strings.Contains(p.Detail, missing) || len(p.Unresolved) != 1 || p.Unresolved[0] != (domain.UnresolvedRef{Name: "customer", Entity: "customer", ID: missing, Reason: p.Unresolved[0].Reason}) || p.Errors["customer"] == "" {
		t.Fatalf("problem = %s", rec.Body)
	}
	if rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("content type %q", rec.Header().Get("Content-Type"))
	}
}

func TestOutbound_RenderRefusals(t *testing.T) {
	s := store.NewMem()
	r := obRouter(s, hubPrincipal(1))
	m := createOutbound(t, r, "Orders")
	cust := seedCustomer(t, s, "C-1")
	for name, c := range map[string]struct {
		path, body string
		status     int
		code       string
	}{
		// 404 carries a problem code: the hub reads a 404 without one as "the route is missing".
		"unknown mapping":       {obPath + "/" + ids.New() + ":render", `{"event":` + hubEvent("e", ids.New(), cust) + `}`, 404, store.CodeOutboundNotFound},
		"not a uuid":            {obPath + "/nope:render", `{"event":` + hubEvent("e", ids.New(), cust) + `}`, 404, store.CodeOutboundNotFound},
		"wrong event type":      {obPath + "/" + m.ID + ":render", `{"event":{"id":"e","type":"dev.passage.billing.invoice.posted.v1","legalentityid":"` + hubLegalEnt + `","data":{}}}`, 422, domain.CodeOutboundMismatch},
		"no event":              {obPath + "/" + m.ID + ":render", `{}`, 422, domain.CodeOutboundEvent},
		"unknown version":       {obPath + "/" + m.ID + ":render", `{"version":9,"event":` + hubEvent("e", ids.New(), cust) + `}`, 404, store.CodeOutboundVersionGone},
		"extra field":           {obPath + "/" + m.ID + ":render", `{"event":{},"x":1}`, 400, "COM-0001"},
		"optional field absent": {obPath + "/" + m.ID + ":render", `{"event":{"id":"e","type":"dev.passage.sales.order.confirmed.v1","legalentityid":"` + hubLegalEnt + `","data":{"order_id":"` + ids.New() + `","customer_id":"` + cust + `"}}}`, 200, ""},
		// The token names one legal entity; it renders only events of that legal entity (store reads are tenant-wide).
		"other legal entity":    {obPath + "/" + m.ID + ":render", `{"event":` + strings.Replace(hubEvent("e", ids.New(), cust), hubLegalEnt, "00000000-0000-7000-8000-0000000000b1", 1) + `}`, 403, api.CodeOutboundLegalEntity},
		"no legal entity":       {obPath + "/" + m.ID + ":render", `{"event":{"id":"e","type":"dev.passage.sales.order.confirmed.v1","data":{"order_id":"` + ids.New() + `","customer_id":"` + cust + `"}}}`, 403, api.CodeOutboundLegalEntity},
		"legal entity a number": {obPath + "/" + m.ID + ":render", `{"event":{"id":"e","type":"dev.passage.sales.order.confirmed.v1","legalentityid":7,"data":{}}}`, 403, api.CodeOutboundLegalEntity},
		"upper-case same LE":    {obPath + "/" + m.ID + ":render", `{"event":` + strings.Replace(hubEvent("e", ids.New(), cust), hubLegalEnt, strings.ToUpper(hubLegalEnt), 1) + `}`, 200, ""},
	} {
		rec := hubCall(r, c.path, c.body)
		if rec.Code != c.status || (c.code != "" && !strings.Contains(rec.Body.String(), `"code":"`+c.code+`"`)) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// An archived mapping is not rendered.
	rec := do(r, http.MethodPatch, obPath+"/"+m.ID, `{"version":1,"status":"archived"}`, obWrite, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive = %d %s", rec.Code, rec.Body)
	}
	if rec := hubCall(r, obPath+"/"+m.ID+":render", `{"event":`+hubEvent("e", ids.New(), cust)+`}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeOutboundArchived) {
		t.Fatalf("archived render = %d %s", rec.Code, rec.Body)
	}
}

func TestOutbound_OnlyTheHubMayRenderAndRecord(t *testing.T) {
	s := store.NewMem()
	r := obRouter(s, hubPrincipal(1))
	m := createOutbound(t, r, "Orders")
	body := `{"event":` + hubEvent("e", ids.New(), ids.New()) + `}`
	// A person without the duty, and a person who somehow holds it, are both refused: it is the hub's contract.
	if rec := do(r, http.MethodPost, obPath+"/"+m.ID+":render", body, obWrite, true); rec.Code != http.StatusForbidden {
		t.Fatalf("human without duty = %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, obPath+"/"+m.ID+":render", body, obWrite+",migration.outbound.render", true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "integration-hub") {
		t.Fatalf("human with duty = %d %s", rec.Code, rec.Body)
	}
	record := `{"outboundMappingId":"` + m.ID + `","refs":[{"entity":"salesOrder","id":"` + ids.New() + `"}],"legacyKey":"SO-1","eventId":"e"}`
	if rec := do(r, http.MethodPost, "/v1/integration/xref:record", record, obWrite+",migration.xref.record", true); rec.Code != http.StatusForbidden {
		t.Fatalf("human record = %d", rec.Code)
	}
	// The hub's token carries no view duty: it cannot browse mappings.
	if rec := hubCall(r, obPath, ``); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusForbidden {
		t.Fatalf("hub on a human route = %d", rec.Code)
	}
}

func TestOutbound_RecordXrefValidationConflictAndIsolation(t *testing.T) {
	s := store.NewMem()
	r := obRouter(s, hubPrincipal(1))
	m := createOutbound(t, r, "Orders")
	order := ids.New()
	good := `{"outboundMappingId":"` + m.ID + `","refs":[{"entity":"salesOrder","id":"` + order + `"}],"legacyKey":"SO-1","eventId":"evt-1"}`
	if rec := hubCall(r, "/v1/integration/xref:record", good); rec.Code != http.StatusNoContent {
		t.Fatalf("record = %d %s", rec.Code, rec.Body)
	}
	for name, c := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"other key, same event": {strings.Replace(good, `"SO-1"`, `"SO-2"`, 1), 409, store.CodeXrefEventConflict},
		"key of another record": {strings.Replace(strings.Replace(good, order, ids.New(), 1), "evt-1", "evt-2", 1), 409, store.CodeXrefConflict},
		"two refs":              {strings.Replace(good, `[{"entity":"salesOrder","id":"`+order+`"}]`, `[{"entity":"salesOrder","id":"`+order+`"},{"entity":"salesOrder","id":"`+ids.New()+`"}]`, 1), 400, "COM-0001"},
		// The hub's grant reaches only the cross-reference of its mappings' subjects, never an inbound entity.
		"not the subject entity": {strings.Replace(strings.Replace(good, `"salesOrder"`, `"customer"`, 1), "evt-1", "evt-9", 1), 422, store.CodeXrefRefMismatch},
		"no refs, same event":    {strings.Replace(good, `[{"entity":"salesOrder","id":"`+order+`"}]`, `[]`, 1), 409, store.CodeXrefEventConflict},
		"bad ref id":             {strings.Replace(good, order, "nope", 1), 400, "COM-0001"},
		"empty key":              {strings.Replace(good, `"SO-1"`, `""`, 1), 400, "COM-0001"},
		"no event id":            {strings.Replace(good, `"evt-1"`, `""`, 1), 400, "COM-0001"},
		"unknown mapping":        {strings.Replace(good, m.ID, ids.New(), 1), 404, store.CodeOutboundNotFound},
		"unknown field":          {strings.Replace(good, `"eventId"`, `"extra":1,"eventId"`, 1), 400, "COM-0001"},
		"mapping id not a uuid":  {strings.Replace(good, m.ID, "x", 1), 404, store.CodeOutboundNotFound},
	} {
		rec := hubCall(r, "/v1/integration/xref:record", c.body)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"code":"`+c.code+`"`) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestOutbound_VersionPinning(t *testing.T) {
	s := store.NewMem()
	r := obRouter(s, hubPrincipal(1))
	m := createOutbound(t, r, "Orders")
	cust, order := seedCustomer(t, s, "C-1001"), ids.New()
	// v2 maps the customer to a different field.
	v2 := strings.Replace(obDefinition, `"target": "CustAccount"`, `"target": "AccountNum"`, 1)
	rec := do(r, http.MethodPatch, obPath+"/"+m.ID, `{"version":1,"definition":`+v2+`}`, obWrite, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body)
	}
	event := `{"event":` + hubEvent("evt-1", order, cust)
	cur := hubCall(r, obPath+"/"+m.ID+":render", event+`}`)
	pinned := hubCall(r, obPath+"/"+m.ID+":render", event+`,"version":1}`)
	if !strings.Contains(cur.Body.String(), "AccountNum") || !strings.Contains(pinned.Body.String(), "CustAccount") || strings.Contains(pinned.Body.String(), "AccountNum") {
		t.Fatalf("current = %s\npinned = %s", cur.Body, pinned.Body)
	}
	var a, b struct{ IdempotencyKey string }
	_ = json.Unmarshal(cur.Body.Bytes(), &a)
	_ = json.Unmarshal(pinned.Body.Bytes(), &b)
	// A push retried after an edit must reach the legacy ERP under the same key, or it creates a second record.
	if a.IdempotencyKey == "" || a.IdempotencyKey != b.IdempotencyKey {
		t.Fatalf("the idempotency key must not change with the mapping version: %q %q", a.IdempotencyKey, b.IdempotencyKey)
	}
	// A stale writer loses.
	if rec := do(r, http.MethodPatch, obPath+"/"+m.ID, `{"version":1,"name":"x"}`, obWrite, true); rec.Code != http.StatusConflict {
		t.Fatalf("stale patch = %d %s", rec.Code, rec.Body)
	}
	// GET ?version= and the history.
	if rec := do(r, http.MethodGet, obPath+"/"+m.ID+"?version=1", ``, obRead, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "CustAccount") {
		t.Fatalf("get v1 = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, obPath+"/"+m.ID+"/versions", ``, obRead, true); rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"checksum"`) != 2 {
		t.Fatalf("versions = %d %s", rec.Code, rec.Body)
	}
}

func TestOutbound_CrudValidationAndDuties(t *testing.T) {
	s := store.NewMem()
	r := obRouter(s, hubPrincipal(1))
	m := createOutbound(t, r, "Orders")
	post := func(body, duties string) *httptest.ResponseRecorder {
		return do(r, http.MethodPost, obPath, body, duties, true)
	}
	if rec := post(`{"name":"x","sourceSystem":"custom_acme","definition":`+obDefinition+`}`, obRead); rec.Code != http.StatusForbidden {
		t.Fatalf("read-only create = %d", rec.Code)
	}
	if rec := post(`{"name":"Orders","sourceSystem":"custom_acme","definition":`+obDefinition+`}`, obWrite); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate name = %d %s", rec.Code, rec.Body)
	}
	for name, body := range map[string]string{
		"no name":        `{"name":"","sourceSystem":"custom_acme","definition":` + obDefinition + `}`,
		"bad system":     `{"name":"a","sourceSystem":"Acme!","definition":` + obDefinition + `}`,
		"no definition":  `{"name":"a","sourceSystem":"custom_acme"}`,
		"bad event type": `{"name":"a","sourceSystem":"custom_acme","definition":` + strings.Replace(obDefinition, "dev.passage.sales.order.*.v1", "a..b", 1) + `}`,
		"bad rule":       `{"name":"a","sourceSystem":"custom_acme","definition":` + strings.Replace(obDefinition, `"type": "string", "required": true`, `"type": "nope"`, 1) + `}`,
	} {
		if rec := post(body, obWrite); rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodGet, obPath, ``, obRead, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), m.ID) || strings.Contains(rec.Body.String(), `"definition"`) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, obPath+"/"+m.ID, ``, obRead, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"definition"`) {
		t.Fatalf("get = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPatch, obPath+"/"+m.ID, `{"version":1,"name":"y"}`, obRead, true); rec.Code != http.StatusForbidden {
		t.Fatalf("read-only patch = %d", rec.Code)
	}
	if rec := do(r, http.MethodPatch, obPath+"/"+m.ID, `{"name":"y"}`, obWrite, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("patch without version = %d", rec.Code)
	}
	// An idempotent create replays.
	payload := `{"name":"Invoices","sourceSystem":"custom_acme","definition":` + strings.Replace(obDefinition, "sales.order.*.v1", "billing.invoice.**", 1) + `}`
	first := do(r, http.MethodPost, obPath, payload, obWrite, true, "Idempotency-Key", "k1")
	second := do(r, http.MethodPost, obPath, payload, obWrite, true, "Idempotency-Key", "k1")
	if first.Code != http.StatusCreated || second.Code != http.StatusOK {
		t.Fatalf("create replay: %d %d", first.Code, second.Code)
	}
}

func TestOutbound_PreviewStoredAndDraft(t *testing.T) {
	s := store.NewMem()
	r := obRouter(s, hubPrincipal(1))
	m := createOutbound(t, r, "Orders")
	cust, order := seedCustomer(t, s, "C-1001"), ids.New()
	preview := func(path, body string) map[string]any {
		rec := do(r, http.MethodPost, path, body, obRead, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body)
		}
		var v map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return v
	}
	if v := preview(obPath+"/"+m.ID+":preview", `{"event":`+hubEvent("e", order, cust)+`}`); v["status"] != "ok" || v["body"].(map[string]any)["CustAccount"] != "C-1001" {
		t.Fatalf("preview = %+v", v)
	}
	if v := preview(obPath+"/"+m.ID+":preview", `{"event":`+hubEvent("e", order, ids.New())+`}`); v["status"] != "unresolved" || v["code"] != "MIG-XREF-UNRESOLVED" || len(v["unresolved"].([]any)) != 1 {
		t.Fatalf("unresolved preview = %+v", v)
	}
	if v := preview(obPath+":preview", `{"sourceSystem":"custom_acme","definition":`+obDefinition+`,"event":`+hubEvent("e", order, cust)+`}`); v["status"] != "ok" {
		t.Fatalf("draft preview = %+v", v)
	}
	if v := preview(obPath+":preview", `{"sourceSystem":"custom_acme","definition":`+strings.Replace(obDefinition, `"type": "string", "required": true`, `"type": "nope"`, 1)+`,"event":{}}`); v["status"] != "invalid" {
		t.Fatalf("invalid draft preview = %+v", v)
	}
	if v := preview(obPath+"/"+m.ID+":preview", `{"event":{"id":"e","type":"dev.passage.sales.order.confirmed.v1","data":{}}}`); v["status"] != "unresolved" {
		t.Fatalf("event without the customer id = %+v", v)
	}
	// Previews need the view duty, not the hub's.
	if rec := hubCall(r, obPath+"/"+m.ID+":preview", `{"event":{}}`); rec.Code != http.StatusForbidden {
		t.Fatalf("hub preview = %d", rec.Code)
	}
}

// Echo suppression: the hub skips events whose actor is service:migration-loader. The loader's calls to the owning
// services are signed as this principal, so the events those services emit for loaded rows carry that actor.
func TestOutbound_LoaderActorMatchesTheHubsEchoActor(t *testing.T) {
	const hubEchoActor = "service:migration-loader" // ECHO_ACTOR in services/integration-hub-ts/src/pgStore.ts
	if got := tenant.ServiceActor(target.Principal); got != hubEchoActor {
		t.Fatalf("loader actor %q != hub echo actor %q: round trips would loop", got, hubEchoActor)
	}
}

// A mapping without a subject renders refs [] and the hub sends them back as they are: the push is recorded for the
// event and nothing is written to the cross-reference.
func TestOutbound_RecordWithoutSubject(t *testing.T) {
	s := store.NewMem()
	r := obRouter(s, hubPrincipal(1))
	noSubject := strings.Replace(obDefinition, `"subject": {"entity": "salesOrder", "idPath": "data.order_id"},`, ``, 1)
	rec := do(r, http.MethodPost, obPath, `{"name":"Journal","sourceSystem":"custom_acme","definition":`+noSubject+`}`, obWrite, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var m obBody
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	cust := seedCustomer(t, s, "C-1")
	rendered := hubCall(r, obPath+"/"+m.ID+":render", `{"event":`+hubEvent("evt-1", ids.New(), cust)+`}`)
	if rendered.Code != http.StatusOK || !strings.Contains(rendered.Body.String(), `"refs":[]`) {
		t.Fatalf("render = %d %s", rendered.Code, rendered.Body)
	}
	before := len(s.Xref().(*domain.MemXref).Entries)
	for _, refs := range []string{`[]`, `null`} {
		if rec := hubCall(r, "/v1/integration/xref:record", `{"outboundMappingId":"`+m.ID+`","refs":`+refs+`,"legacyKey":"J-1","eventId":"evt-1"}`); rec.Code != http.StatusNoContent {
			t.Fatalf("record refs %s = %d %s", refs, rec.Code, rec.Body)
		}
	}
	if after := len(s.Xref().(*domain.MemXref).Entries); after != before {
		t.Fatalf("nothing may reach the cross-reference: %d entries, was %d", after, before)
	}
	// A ref against a mapping that never had a subject is refused.
	if rec := hubCall(r, "/v1/integration/xref:record", `{"outboundMappingId":"`+m.ID+`","refs":[{"entity":"salesOrder","id":"`+ids.New()+`"}],"legacyKey":"J-2","eventId":"evt-2"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ref without subject = %d %s", rec.Code, rec.Body)
	}
}
