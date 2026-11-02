// Package targettest fakes the party, product and receivables import APIs for tests: it verifies the service token
// the loader signs (issuer, audience, actor, duties, legal entity, single use), keeps what was created and can fail
// or refuse calls on demand. It follows the documented request and answer shapes of the three services.
package targettest

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Call is one request a fake received.
type Call struct {
	Service, Method, Path string
	Body                  map[string]any
	Header                http.Header
	Claims                tenant.ServiceClaims
}

// Fakes is the three services.
type Fakes struct {
	Signer                               *tenant.ServiceSigner
	PartyURL, ProductURL, ReceivablesURL string

	mu    sync.Mutex
	calls []Call

	// Fail503 makes the next n calls whose "METHOD path" equals the key answer 503 before it behaves normally.
	Fail503 map[string]int
	// Always503 services answer 503 to everything ("party", "product", "receivables").
	Always503 map[string]bool
	// RequireAttribute is an attribute a product needs before it can become active (BR-8.9).
	RequireAttribute string
	// SettledKeys are open-item legacy keys the receivables fake refuses as AR-OPENING-SETTLED.
	SettledKeys map[string]bool

	Parties    map[string]*Party
	Customers  map[string]map[string]any // role id -> body
	Vendors    map[string]map[string]any
	Products   map[string]*Product
	OpenItems  map[string]map[string]any // legacyKey -> row
	itemIDs    map[string]string
	idem       map[string]string // service|key -> answer body
	accounts   map[string]bool
	nextRoleID int
}

type Party struct {
	ID      string
	Version int
	Body    map[string]any
}

type Product struct {
	ID         string
	Version    int
	State      string
	Body       map[string]any
	Attributes map[string]string
}

// Start serves the fakes; the servers stop with the test.
func Start(t testing.TB) *Fakes {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("migration", priv)
	if err != nil {
		t.Fatal(err)
	}
	f := &Fakes{
		Signer: signer, Fail503: map[string]int{}, Always503: map[string]bool{}, SettledKeys: map[string]bool{},
		Parties: map[string]*Party{}, Customers: map[string]map[string]any{}, Vendors: map[string]map[string]any{},
		Products: map[string]*Product{}, OpenItems: map[string]map[string]any{}, itemIDs: map[string]string{}, idem: map[string]string{}, accounts: map[string]bool{},
	}
	serve := func(service string, h func(w http.ResponseWriter, r *http.Request, c Call)) string {
		verifier := tenant.NewServiceVerifier(service, map[string]ed25519.PublicKey{"migration": pub})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			c := Call{Service: service, Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone()}
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &c.Body)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.calls = append(f.calls, c)
			if f.Always503[service] {
				problem(w, http.StatusServiceUnavailable, "COM-0503", "unavailable", nil)
				return
			}
			if key := r.Method + " " + r.URL.Path; f.Fail503[key] > 0 {
				f.Fail503[key]--
				problem(w, http.StatusServiceUnavailable, "COM-0503", "unavailable", nil)
				return
			}
			claims, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
			if err != nil {
				problem(w, http.StatusUnauthorized, "AUTH-0001", "invalid service token: "+err.Error(), nil)
				return
			}
			if claims.Act != tenant.ServiceActor("migration-loader") || claims.Le == "" {
				problem(w, http.StatusForbidden, "AUTH-0003", "not the migration loader", nil)
				return
			}
			f.calls[len(f.calls)-1].Claims = claims
			c.Claims = claims
			h(w, r, c)
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	f.PartyURL = serve("party", f.party)
	f.ProductURL = serve("product", f.product)
	f.ReceivablesURL = serve("receivables", f.receivables)
	return f
}

// Calls returns the requests received so far, optionally only those to one service.
func (f *Fakes) Calls(service string) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
	for _, c := range f.calls {
		if service == "" || c.Service == service {
			out = append(out, c)
		}
	}
	return out
}

// Lock guards the fake's state while a test reads it.
func (f *Fakes) Lock()   { f.mu.Lock() }
func (f *Fakes) Unlock() { f.mu.Unlock() }

func problem(w http.ResponseWriter, status int, code, detail string, errs map[string]string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "code": code, "serviceCode": code, "detail": detail, "errors": errs})
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// replayed answers a repeated Idempotency-Key with the stored answer; it reports whether it did.
func (f *Fakes) replayed(w http.ResponseWriter, service string, r *http.Request) bool {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return false
	}
	if stored, ok := f.idem[service+"|"+r.URL.Path+"|"+key]; ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, stored)
		return true
	}
	return false
}

func (f *Fakes) remember(service string, r *http.Request, body any) {
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		raw, _ := json.Marshal(body)
		f.idem[service+"|"+r.URL.Path+"|"+key] = string(raw)
	}
}

// accepted are the JSON fields each route decodes; like httpx.Decode in the real services, any other field is a
// 400 COM-0001, so a loader that sends a field outside a contract fails here as it would there.
var accepted = map[string][]string{
	"party":         {"id", "partyType", "legalName", "tradeName", "registrationNumber", "industryCode", "givenName", "middleName", "familyName", "prefix", "suffix", "dob", "gender", "languageCode", "timeZone", "ownerUserId", "ownerTeamId"},
	"party-patch":   {"partyType", "status", "version", "legalName", "tradeName", "registrationNumber", "industryCode", "givenName", "middleName", "familyName", "prefix", "suffix", "dob", "gender", "languageCode", "timeZone", "ownerUserId", "ownerTeamId"},
	"customer":      {"accountNumber", "customerGroupId", "paymentTermId", "deliveryTermId", "deliveryModeId", "postingProfileId", "invoiceAccountPartyId", "currencyCode", "creditLimit", "creditCurrency", "creditRating", "creditHold", "blocked"},
	"vendor":        {"accountNumber", "vendorGroupId", "paymentTermId", "currencyCode"},
	"product":       {"productNumber", "subtype", "productType", "name", "description", "searchName", "baseUom", "dimensionGroupId", "lifecycleState", "service", "id"},
	"product-patch": {"name", "description", "searchName", "lifecycleState"},
	"batch":         {"legalEntityId", "importSource", "atomic", "items"},
	"opening-row":   {"legacyKey", "documentNumber", "customerId", "kind", "side", "documentDate", "dueDate", "currency", "originalAmount", "openAmount", "cashDiscountDate", "cashDiscountAmount"},
}

// strict answers 400 for a field the route does not decode and reports whether it did.
func strict(w http.ResponseWriter, route string, body map[string]any) bool {
	for k := range body {
		if !slices.Contains(accepted[route], k) {
			problem(w, http.StatusBadRequest, "COM-0001", "malformed JSON body: json: unknown field \""+k+"\"", nil)
			return true
		}
	}
	return false
}

func ifMatch(r *http.Request) int {
	v, _ := strconv.Atoi(strings.Trim(r.Header.Get("If-Match"), `"`))
	return v
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func (f *Fakes) party(w http.ResponseWriter, r *http.Request, c Call) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/party/"), "/"), "/")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/party/parties":
		if f.replayed(w, "party", r) || strict(w, "party", c.Body) {
			return
		}
		id := str(c.Body, "id")
		if _, taken := f.Parties[id]; taken {
			problem(w, http.StatusConflict, "PTY-PARTY-ID-TAKEN", "the party id is taken", nil)
			return
		}
		if str(c.Body, "partyType") == "" {
			problem(w, http.StatusUnprocessableEntity, "PTY-PARTY-INVALID", "request failed validation", map[string]string{"partyType": "is required"})
			return
		}
		f.Parties[id] = &Party{ID: id, Version: 1, Body: c.Body}
		out := map[string]any{"id": id, "partyNumber": fmt.Sprintf("P-%04d", len(f.Parties)), "partyType": str(c.Body, "partyType"), "status": "active", "version": 1}
		f.remember("party", r, out)
		reply(w, http.StatusCreated, out)
	case r.Method == http.MethodPatch && len(parts) == 2 && parts[0] == "parties":
		if strict(w, "party-patch", c.Body) {
			return
		}
		p := f.Parties[parts[1]]
		switch {
		case p == nil:
			problem(w, http.StatusNotFound, "PTY-PARTY-NOT-FOUND", "party not found", nil)
		case ifMatch(r) != p.Version:
			problem(w, http.StatusConflict, "PTY-VERSION-CONFLICT", "the version is stale", nil)
		default:
			p.Version++
			for k, v := range c.Body {
				p.Body[k] = v
			}
			reply(w, http.StatusOK, map[string]any{"id": p.ID, "version": p.Version, "status": "active"})
		}
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "parties" && (parts[2] == "customer" || parts[2] == "vendor"):
		if f.replayed(w, "party", r) || strict(w, parts[2], c.Body) {
			return
		}
		if f.Parties[parts[1]] == nil {
			problem(w, http.StatusNotFound, "PTY-PARTY-NOT-FOUND", "party not found", nil)
			return
		}
		acct := parts[2] + "|" + c.Claims.Le + "|" + str(c.Body, "accountNumber")
		if f.accounts[acct] {
			problem(w, http.StatusConflict, "PTY-"+strings.ToUpper(parts[2])+"-EXISTS", "the account number is taken", nil)
			return
		}
		f.accounts[acct] = true
		f.nextRoleID++
		id := fmt.Sprintf("00000000-0000-7000-9000-%012d", f.nextRoleID)
		role := map[string]any{"id": id, "partyId": parts[1], "legalEntityId": c.Claims.Le, "version": 1}
		for k, v := range c.Body {
			role[k] = v
		}
		if parts[2] == "customer" {
			f.Customers[id] = role
		} else {
			f.Vendors[id] = role
		}
		f.remember("party", r, role)
		reply(w, http.StatusCreated, role)
	default:
		problem(w, http.StatusNotFound, "COM-0404", "no such route "+r.Method+" "+r.URL.Path, nil)
	}
}

func (f *Fakes) product(w http.ResponseWriter, r *http.Request, c Call) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/product/"), "/"), "/")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/product/products":
		// Like the product store: a repeated Idempotency-Key answers the product as it is now (whatever the body),
		// and a fresh create answers Version 0 because the store does not read the new row back.
		if key := r.Header.Get("Idempotency-Key"); key != "" {
			if id, ok := f.idem["product|"+key]; ok {
				p := f.Products[id]
				reply(w, http.StatusCreated, map[string]any{"ID": p.ID, "Version": p.Version, "LifecycleState": p.State, "Name": str(p.Body, "name")})
				return
			}
		}
		if strict(w, "product", c.Body) {
			return
		}
		id := str(c.Body, "id")
		if _, taken := f.Products[id]; taken {
			problem(w, http.StatusConflict, "PRD-ID-TAKEN", "the product id is taken", nil)
			return
		}
		for _, p := range f.Products {
			if str(p.Body, "productNumber") == str(c.Body, "productNumber") {
				problem(w, http.StatusConflict, "COM-0409", "product number already exists", map[string]string{"existingId": p.ID})
				return
			}
		}
		if str(c.Body, "lifecycleState") != "draft" {
			problem(w, http.StatusUnprocessableEntity, "PRD-LC-ILLEGAL", "the loader creates drafts", nil)
			return
		}
		p := &Product{ID: id, Version: 1, State: "draft", Body: c.Body, Attributes: map[string]string{}}
		f.Products[id] = p
		if key := r.Header.Get("Idempotency-Key"); key != "" {
			f.idem["product|"+key] = id
		}
		reply(w, http.StatusCreated, map[string]any{"ID": id, "Version": 0, "LifecycleState": "draft", "Name": str(c.Body, "name")})
	case len(parts) == 3 && parts[0] == "products" && parts[2] == "attributes" && r.Method == http.MethodPost:
		p := f.Products[parts[1]]
		if p == nil {
			problem(w, http.StatusNotFound, "COM-0001", "product not found", nil)
			return
		}
		for k, v := range c.Body {
			p.Attributes[k], _ = v.(string)
		}
		reply(w, http.StatusOK, map[string]string{"status": "ok"})
	case len(parts) == 2 && parts[0] == "products" && r.Method == http.MethodPatch:
		if strict(w, "product-patch", c.Body) {
			return
		}
		p := f.Products[parts[1]]
		switch {
		case p == nil:
			problem(w, http.StatusNotFound, "COM-0001", "product not found", nil)
		case ifMatch(r) != p.Version:
			problem(w, http.StatusPreconditionFailed, "PRD-VERSION-CONFLICT", "product changed since it was read", nil)
		default:
			to := str(c.Body, "lifecycleState")
			if to == "active" && p.State == "draft" && f.RequireAttribute != "" && p.Attributes[f.RequireAttribute] == "" {
				problem(w, http.StatusUnprocessableEntity, "PRD-ATTR-REQUIRED", "required attribute "+f.RequireAttribute+" is missing", nil)
				return
			}
			changed := false
			for k, v := range c.Body {
				if k != "lifecycleState" && p.Body[k] != v {
					p.Body[k], changed = v, true
				}
			}
			if to != "" && to != p.State {
				p.State, changed = to, true
			}
			if changed {
				p.Version++
			}
			reply(w, http.StatusOK, map[string]any{"ID": p.ID, "Version": p.Version, "LifecycleState": p.State})
		}
	default:
		problem(w, http.StatusNotFound, "COM-0404", "no such route "+r.Method+" "+r.URL.Path, nil)
	}
}

func (f *Fakes) receivables(w http.ResponseWriter, r *http.Request, c Call) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/receivables/open-items:batch" {
		problem(w, http.StatusNotFound, "COM-0404", "no such route", nil)
		return
	}
	if str(c.Body, "legalEntityId") != c.Claims.Le {
		problem(w, http.StatusForbidden, "AUTH-0003", "legal entity outside the token", nil)
		return
	}
	if strict(w, "batch", c.Body) {
		return
	}
	rows, _ := c.Body["items"].([]any)
	for _, raw := range rows {
		if row, _ := raw.(map[string]any); strict(w, "opening-row", row) {
			return
		}
	}
	if len(rows) == 0 || len(rows) > 1000 {
		problem(w, http.StatusBadRequest, "COM-0001", "request failed validation", map[string]string{"items": "1 to 1000 rows"})
		return
	}
	results := make([]map[string]any, len(rows))
	for i, raw := range rows {
		row, _ := raw.(map[string]any)
		key := str(row, "legacyKey")
		res := map[string]any{"index": i, "legacyKey": key}
		prev, known := f.OpenItems[key]
		if !known {
			f.itemIDs[key] = fmt.Sprintf("00000000-0000-7000-a000-%012d", len(f.itemIDs)+1)
		}
		switch {
		case f.SettledKeys[key]:
			res["outcome"], res["code"], res["detail"] = "REJECTED", "AR-OPENING-SETTLED", "the item was settled in Passage"
		case known && fmt.Sprint(prev) == fmt.Sprint(row):
			res["outcome"], res["openItemId"] = "UNCHANGED", f.itemIDs[key]
		default:
			res["outcome"] = map[bool]string{true: "UPDATED", false: "CREATED"}[known]
			f.OpenItems[key] = row
			res["openItemId"] = f.itemIDs[key]
		}
		results[i] = res
	}
	reply(w, http.StatusOK, map[string]any{"batchId": "b", "legalEntityId": c.Claims.Le, "importSource": str(c.Body, "importSource"), "results": results})
}
