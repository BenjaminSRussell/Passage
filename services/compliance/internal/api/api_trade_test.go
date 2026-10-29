package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/store"
)

// fakeTrade is the screening half of fakeStore: it runs the real matcher, keeps
// one hit in memory and applies the real decision rules.
type fakeTrade struct {
	threshold float64
	subject   domain.Subject
	hit       *store.ScreeningHit
	lastKey   string
	lastList  store.HitFilter
}

func (f *fakeStore) Screen(_ context.Context, s domain.Subject, entries []domain.ListEntry, threshold float64, at time.Time) (store.ScreeningResult, error) {
	f.trade.threshold, f.trade.subject = threshold, s
	hits, err := domain.Screen(domain.ScreenInput{Subject: s, Entries: entries, Threshold: threshold})
	if err != nil {
		return store.ScreeningResult{}, err
	}
	out := store.ScreeningResult{Hits: []store.ScreeningHit{}}
	for _, h := range hits {
		out.Hits = append(out.Hits, store.ScreeningHit{HitID: ids.New(), SubjectType: s.Type, SubjectID: s.ID, ListCode: h.ListCode,
			ListEntryID: h.EntryID, Score: h.Score, MatchedFields: h.MatchedFields, State: h.State, DetectedAt: at, Version: 1})
	}
	out.Blocked = len(out.Hits) > 0
	return out, nil
}

func (f *fakeStore) ListHits(_ context.Context, fl store.HitFilter) (store.HitPage, error) {
	f.trade.lastList = fl
	if f.trade.hit == nil {
		return store.HitPage{Items: []store.ScreeningHit{}}, nil
	}
	return store.HitPage{Items: []store.ScreeningHit{*f.trade.hit}}, nil
}

func (f *fakeStore) GetHit(_ context.Context, id string) (store.ScreeningHit, error) {
	if f.trade.hit == nil || f.trade.hit.HitID != id {
		return store.ScreeningHit{}, httpx.NewProblem(http.StatusNotFound, store.CodeHitNotFound, "no screening hit with that id")
	}
	return *f.trade.hit, nil
}

func (f *fakeStore) DecideHit(ctx context.Context, id string, expected int, in store.DecisionInput, key string, at time.Time) (store.ScreeningHit, error) {
	f.trade.lastKey = key
	h, err := f.GetHit(ctx, id)
	if err != nil {
		return h, err
	}
	if h.Version != expected {
		return h, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeHitVersion, "stale")
	}
	p, _ := tenant.FromContext(ctx)
	next, err := domain.DecideHit(h.State, in.Decision, in.Reason, p.UserID)
	if err != nil {
		return h, err
	}
	h.State, h.DecidedBy, h.Reason, h.DecidedAt, h.Version = next, p.UserID, in.Reason, &at, h.Version+1
	f.trade.hit = &h
	return h, nil
}

const officer = "6f7d3c1e-2a4b-4c5d-8e9f-0a1b2c3d4e5f"

func sdnEntries() []domain.ListEntry {
	return []domain.ListEntry{{ListCode: "OFAC_SDN", EntryID: "SDN-7001", Version: 2, Name: "Volga Maritime Holdings"}}
}

func TestScreenUsesTenantThresholdAndReportsBlock(t *testing.T) {
	f := &fakeStore{}
	a := New(f, Config{ScreeningThreshold: 0.9})
	a.now = func() time.Time { return now }
	subject := ids.New()
	rec := serve(t, router(a), call{method: http.MethodPost, path: "/v1/trade/screening/check", duties: []string{DutyTradeView},
		body: map[string]any{"subject": map[string]any{"type": "PARTY", "id": subject, "name": "Volga Maritime Holdings LLC", "country": "ru"},
			"entries": sdnEntries()}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res store.ScreeningResult
	decodeInto(t, rec.Body.Bytes(), &res)
	if !res.Blocked || len(res.Hits) != 1 || res.Hits[0].State != domain.HitOpen {
		t.Fatalf("a near-exact SDN name must block with an OPEN hit: %+v", res)
	}
	if f.trade.threshold != 0.9 || f.trade.subject.Country != "RU" || f.trade.subject.ID != subject {
		t.Fatalf("store got threshold %v and subject %+v", f.trade.threshold, f.trade.subject)
	}
	// screening.threshold is a tenant setting: a caller cannot raise it to dodge a hit.
	rec = serve(t, router(a), call{method: http.MethodPost, path: "/v1/trade/screening/check", duties: []string{DutyTradeView},
		body: map[string]any{"subject": map[string]any{"type": "PARTY", "id": subject, "name": "Volga Maritime Holdings"},
			"entries": sdnEntries(), "threshold": 0.99}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("caller threshold accepted: %d", rec.Code)
	}
}

func TestScreenRefusals(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	for name, tc := range map[string]struct {
		body  any
		code  string
		field string
	}{
		"subject id is not a UUID": {map[string]any{"subject": map[string]any{"type": "PARTY", "id": "P-1", "name": "X"}}, codeInvalid, "subject.id"},
		"subject type":             {map[string]any{"subject": map[string]any{"type": "CONTACT", "id": ids.New(), "name": "X"}}, codeInvalid, "subject.type"},
		"no name":                  {map[string]any{"subject": map[string]any{"type": "PARTY", "id": ids.New()}}, codeInvalid, "subject.name"},
		"country":                  {map[string]any{"subject": map[string]any{"type": "PARTY", "id": ids.New(), "name": "X", "country": "RUS"}}, codeInvalid, "subject.country"},
		"entry without name": {map[string]any{"subject": map[string]any{"type": "PARTY", "id": ids.New(), "name": "X"},
			"entries": []map[string]any{{"listCode": "UN_SC", "entryId": "1", "version": 1}}}, domain.CodeScreenInput, ""},
	} {
		rec := serve(t, h, call{method: http.MethodPost, path: "/v1/trade/screening/check", duties: []string{DutyTradeView}, body: tc.body})
		if got := problemCode(t, rec); got != tc.code {
			t.Errorf("%s: code %s (%d), want %s", name, got, rec.Code, tc.code)
			continue
		}
		if tc.field != "" {
			var p httpx.Problem
			decodeInto(t, rec.Body.Bytes(), &p)
			if _, ok := p.Errors[tc.field]; !ok {
				t.Errorf("%s: no error on %s: %v", name, tc.field, p.Errors)
			}
		}
	}
}

func TestListHitsValidatesFilters(t *testing.T) {
	f := &fakeStore{}
	h := router(newAPI(f, true))
	subject := ids.New()
	rec := serve(t, h, call{method: http.MethodGet, path: "/v1/trade/screening/hits?state=OPEN&subjectType=PARTY&listCode=OFAC_SDN&subjectId=" + subject,
		duties: []string{DutyTradeView}})
	if rec.Code != http.StatusOK || f.trade.lastList.State != "OPEN" || f.trade.lastList.SubjectID != subject || f.trade.lastList.ListCode != "OFAC_SDN" {
		t.Fatalf("status %d, filter %+v", rec.Code, f.trade.lastList)
	}
	for _, q := range []string{"state=CLEARED", "subjectType=BANK", "subjectId=42", "limit=0", "cursor=not-a-cursor"} {
		rec := serve(t, h, call{method: http.MethodGet, path: "/v1/trade/screening/hits?" + q, duties: []string{DutyTradeView}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
}

func openHit() *store.ScreeningHit {
	return &store.ScreeningHit{HitID: ids.New(), SubjectType: "PARTY", SubjectID: ids.New(), ListCode: "OFAC_SDN", ListEntryID: "SDN-7001",
		Score: 0.92, MatchedFields: []string{"name"}, State: domain.HitOpen, DetectedAt: now, Version: 1}
}

func decisionCall(id, etag, key string, body any, duties ...string) call {
	headers := map[string]string{}
	if etag != "" {
		headers["If-Match"] = etag
	}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	return call{method: http.MethodPost, path: "/v1/trade/screening/hits/" + id + "/decision", user: officer, duties: duties, headers: headers, body: body}
}

func TestDecideHitWorkflow(t *testing.T) {
	f := &fakeStore{trade: fakeTrade{hit: openHit()}}
	h := router(newAPI(f, true))
	id := f.trade.hit.HitID

	get := serve(t, h, call{method: http.MethodGet, path: "/v1/trade/screening/hits/" + id, duties: []string{DutyTradeView}})
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d etag %q", get.Code, get.Header().Get("ETag"))
	}
	clear := map[string]string{"decision": "CLEAR_FALSE_POSITIVE", "reason": "different date of birth and nationality"}
	// A viewer cannot decide.
	if rec := serve(t, h, decisionCall(id, `"1"`, "k1", clear, DutyTradeView)); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer decided: %d", rec.Code)
	}
	if rec := serve(t, h, decisionCall(id, "", "k1", clear, DutyTradeOfficer)); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: %d", rec.Code)
	}
	if rec := serve(t, h, decisionCall(id, `"1"`, "", clear, DutyTradeOfficer)); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key: %d", rec.Code)
	}
	for _, bad := range []map[string]string{{"decision": "ESCALATE", "reason": "x"}, {"decision": "CONFIRM", "reason": "  "}} {
		if rec := serve(t, h, decisionCall(id, `"1"`, "k1", bad, DutyTradeOfficer)); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: status %d", bad, rec.Code)
		}
	}
	if rec := serve(t, h, decisionCall(id, `"7"`, "k1", clear, DutyTradeOfficer)); rec.Code != http.StatusPreconditionFailed || problemCode(t, rec) != store.CodeHitVersion {
		t.Fatalf("stale version: %d %s", rec.Code, rec.Body)
	}
	rec := serve(t, h, decisionCall(id, `"1"`, "k1", clear, DutyTradeOfficer))
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("decide: %d %s", rec.Code, rec.Body)
	}
	var got store.ScreeningHit
	decodeInto(t, rec.Body.Bytes(), &got)
	if got.State != domain.HitFalsePositive || got.DecidedBy != officer || f.trade.lastKey != "k1" {
		t.Fatalf("decided hit %+v key %q", got, f.trade.lastKey)
	}
	// A decided hit cannot be decided again.
	again := serve(t, h, decisionCall(id, `"2"`, "k2", map[string]string{"decision": "CONFIRM", "reason": "second thoughts"}, DutyTradeOfficer))
	if again.Code != http.StatusUnprocessableEntity || problemCode(t, again) != domain.CodeHitState {
		t.Fatalf("second decision: %d %s", again.Code, again.Body)
	}
	if rec := serve(t, h, decisionCall(ids.New(), `"1"`, "k3", clear, DutyTradeOfficer)); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown hit: %d", rec.Code)
	}
}

func exportBody(dest, region, ref string) map[string]any {
	return map[string]any{
		"destination": dest, "region": region, "officerReleaseLicenseRef": ref,
		"items": []domain.ExportItem{{ItemID: "SKU-5A002", Classification: domain.Classification{Regime: domain.RegimeUSEAR, Code: "5A002"},
			Qty: decimal.RequireFromString("2"), Value: decimal.RequireFromString("1800.00")}},
	}
}

func TestExportCheckBlocksMissingLicense(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	rec := serve(t, h, call{method: http.MethodPost, path: "/v1/trade/export-checks", duties: []string{DutyTradeView}, body: exportBody("CN", "", "")})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res domain.ExportResult
	decodeInto(t, rec.Body.Bytes(), &res)
	// AC-48.17: ECCN 5A002 to a licensable destination with no license is TRD-1003.
	if !res.Blocked || res.Items[0].Code != domain.CodeLicenseNeeded {
		t.Fatalf("want TRD-1003 block: %+v", res)
	}
	// The decision table, the license check mode and the clock are the server's:
	// an empty table, mode OFF or a past "now" from the caller would skip the check.
	for field, v := range map[string]any{"table": []domain.LicenseRule{}, "mode": "OFF", "now": "2001-01-01T00:00:00Z"} {
		body := exportBody("CN", "", "")
		body[field] = v
		if rec := serve(t, h, call{method: http.MethodPost, path: "/v1/trade/export-checks", duties: []string{DutyTradeView}, body: body}); rec.Code != http.StatusBadRequest {
			t.Errorf("caller %s accepted: %d", field, rec.Code)
		}
	}
}

func TestExportCheckWarnModeFromConfig(t *testing.T) {
	a := New(&fakeStore{}, Config{LicenseCheck: domain.LicenseCheckWarn})
	a.now = func() time.Time { return now }
	rec := serve(t, router(a), call{method: http.MethodPost, path: "/v1/trade/export-checks", duties: []string{DutyTradeView}, body: exportBody("CN", "", "")})
	var res domain.ExportResult
	decodeInto(t, rec.Body.Bytes(), &res)
	if res.Blocked || !res.Items[0].Warning || res.Items[0].Code != domain.CodeLicenseNeeded {
		t.Fatalf("WARN mode warns without blocking: %+v", res)
	}
}

func TestExportCheckEmbargoReleaseNeedsOfficer(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	rec := serve(t, h, call{method: http.MethodPost, path: "/v1/trade/export-checks", duties: []string{DutyTradeView}, body: exportBody("UA", "UA-43", "")})
	var res domain.ExportResult
	decodeInto(t, rec.Body.Bytes(), &res)
	if !res.Embargo || !res.Blocked || res.Items[0].Code != domain.CodeEmbargo {
		t.Fatalf("Crimea must be blocked with TRD-1002: %+v", res)
	}
	// BR-48.16: only a TradeComplianceOfficer with a license reference releases.
	rec = serve(t, h, call{method: http.MethodPost, path: "/v1/trade/export-checks", duties: []string{DutyTradeView}, body: exportBody("UA", "UA-43", "OFAC-GL-25")})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer released an embargoed delivery: %d %s", rec.Code, rec.Body)
	}
	rec = serve(t, h, call{method: http.MethodPost, path: "/v1/trade/export-checks", duties: []string{DutyTradeView, DutyTradeOfficer}, body: exportBody("UA", "UA-43", "OFAC-GL-25")})
	res = domain.ExportResult{}
	decodeInto(t, rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Blocked || res.Items[0].LicenseID != "OFAC-GL-25" {
		t.Fatalf("officer release: %d %+v", rec.Code, res)
	}
}

func TestExportCheckRejectsBadInput(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	rec := serve(t, h, call{method: http.MethodPost, path: "/v1/trade/export-checks", duties: []string{DutyTradeView}, body: exportBody("China", "", "")})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeExportInput {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	raw, _ := json.Marshal(map[string]any{"destination": "CN", "items": []any{}})
	rec = serve(t, h, call{method: http.MethodPost, path: "/v1/trade/export-checks", duties: []string{DutyTradeView}, body: json.RawMessage(raw)})
	if problemCode(t, rec) != domain.CodeExportInput {
		t.Fatalf("empty delivery: %d %s", rec.Code, rec.Body)
	}
}
