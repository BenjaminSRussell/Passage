package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// paramsFake records the parameter calls and answers with canned rows.
type paramsFake struct {
	rows  []store.StoredParameters
	saved []store.ParametersSave
	err   error
}

func (f *fakeStore) ListParameters(context.Context, string) ([]store.StoredParameters, error) {
	return f.params.rows, f.params.err
}

func (f *fakeStore) SaveParameters(_ context.Context, in store.ParametersSave) (store.StoredParameters, bool, error) {
	f.params.saved = append(f.params.saved, in)
	return store.StoredParameters{LegalEntityID: in.LegalEntityID, Parameters: in.Parameters, Version: in.IfMatch + 1}, in.IfMatch == 0, f.params.err
}

const fullParameters = `{"rafTolerancePct":"2.5","overConsumptionPct":"15","autoStartOnFeedback":false,"estimateOnCreate":true,"splitAllocation":"keep-on-original","triggerPointsEnabled":true,"asBuiltRequired":true}`

func TestParameters_SaveValidatesAndDefaultsToAll(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	put := func(body string, ifMatch string, duties ...string) int {
		c := call{method: http.MethodPut, path: "/v1/production/parameters", duties: duties, body: body}
		if ifMatch != "" {
			c.headers = map[string]string{"If-Match": ifMatch}
		}
		return do(h, c).Code
	}
	if code := put(fullParameters, "", DutySetupWrite); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	got := f.params.saved[0]
	if got.Parameters.SiteID != domain.AllKey || got.LegalEntityID != entityID || got.Actor != "planner-1" || !got.Parameters.RAFTolerancePct.Equal(decimal.RequireFromString("2.5")) ||
		got.Parameters.AutoStartOnFeedback || !got.Parameters.AsBuiltRequired || got.Parameters.SplitAllocation != domain.AllocKeepOnOriginal {
		t.Fatalf("saved %+v", got)
	}
	if code := put(fullParameters, `"4"`, DutySetupWrite); code != http.StatusOK || f.params.saved[1].IfMatch != 4 {
		t.Fatalf("replace: %d %+v", code, f.params.saved)
	}
	expectProblem(t, do(h, call{method: http.MethodPut, path: "/v1/production/parameters", duties: []string{DutyRead}, body: fullParameters}), http.StatusForbidden, "AUTH-0003")

	mutate := func(k string, v any) string {
		var m map[string]any
		_ = json.Unmarshal([]byte(fullParameters), &m)
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		raw, _ := json.Marshal(m)
		return string(raw)
	}
	for name, body := range map[string]string{
		"rafTolerancePct":      mutate("rafTolerancePct", "101"),
		"overConsumptionPct":   mutate("overConsumptionPct", "-1"),
		"splitAllocation":      mutate("splitAllocation", "sometimes"),
		"siteId":               mutate("siteId", "not-a-uuid"),
		"asBuiltRequired":      mutate("asBuiltRequired", nil),
		"autoStartOnFeedback":  mutate("autoStartOnFeedback", nil),
		"triggerPointsEnabled": mutate("triggerPointsEnabled", nil),
		"estimateOnCreate":     mutate("estimateOnCreate", nil),
	} {
		rec := do(h, call{method: http.MethodPut, path: "/v1/production/parameters", duties: []string{DutySetupWrite}, body: body})
		if p := expectProblem(t, rec, http.StatusBadRequest, validationCode); p.Errors[name] == "" {
			t.Errorf("%s: no field error in %v", name, p.Errors)
		}
	}
	if n := len(f.params.saved); n != 2 {
		t.Fatalf("invalid requests reached the store: %d saves", n)
	}
}

func TestParameters_ListResolvesForASite(t *testing.T) {
	site := ids.New()
	row := store.StoredParameters{LegalEntityID: entityID, Parameters: domain.Parameters{SiteID: site, RAFTolerancePct: decimal.NewFromInt(5), SplitAllocation: domain.AllocKeepOnOriginal}}
	f := &fakeStore{params: paramsFake{rows: []store.StoredParameters{row}}}
	srv, h := newServer(t, f)
	srv.cfg = Config{RAFTolerancePct: decimal.NewFromInt(1), AutoStartOnFeedback: true}
	get := func(query string) (int, struct {
		Defaults domain.Parameters `json:"defaults"`
		Resolved *struct {
			domain.Parameters
			Stored bool `json:"stored"`
		} `json:"resolved"`
	}) {
		rec := do(h, call{method: http.MethodGet, path: "/v1/production/parameters" + query, duties: []string{DutySetupRead}})
		var body struct {
			Defaults domain.Parameters `json:"defaults"`
			Resolved *struct {
				domain.Parameters
				Stored bool `json:"stored"`
			} `json:"resolved"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}
	code, body := get("?siteId=" + site)
	if code != 200 || body.Resolved == nil || !body.Resolved.Stored || !body.Resolved.RAFTolerancePct.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("stored site: %d %+v", code, body.Resolved)
	}
	// A site with no row falls back to the service defaults.
	code, body = get("?siteId=" + ids.New())
	if code != 200 || body.Resolved == nil || body.Resolved.Stored || !body.Resolved.RAFTolerancePct.Equal(decimal.NewFromInt(1)) || !body.Resolved.AutoStartOnFeedback ||
		body.Resolved.SplitAllocation != domain.AllocProportional || !body.Resolved.TriggerPointsEnabled {
		t.Fatalf("defaults: %d %+v", code, body.Resolved)
	}
	if code, body = get(""); code != 200 || body.Resolved != nil || !body.Defaults.TriggerPointsEnabled {
		t.Fatalf("no site: %d %+v", code, body)
	}
	expectProblem(t, do(h, call{method: http.MethodGet, path: "/v1/production/parameters?siteId=x", duties: []string{DutySetupRead}}), http.StatusBadRequest, validationCode)
}
