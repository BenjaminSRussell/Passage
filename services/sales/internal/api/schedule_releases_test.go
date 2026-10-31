package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

type releaseStore struct {
	fakeStore
	created  *store.NewScheduleRelease
	resolved string
	version  int
	shipped  decimal.Decimal
	reset    *string
	fresh    bool
}

func (s *releaseStore) CreateScheduleRelease(_ context.Context, in store.NewScheduleRelease, _ time.Time) (store.ScheduleRelease, bool, error) {
	s.created = &in
	return store.ScheduleRelease{ID: ids.New(), Version: 1, Status: "HELD"}, s.fresh, nil
}

func (s *releaseStore) ResolveScheduleRelease(_ context.Context, _, _ string, version int, decision, _, _ string, _ time.Time) (store.ScheduleRelease, error) {
	s.resolved, s.version = decision, version
	return store.ScheduleRelease{Version: version + 1, Status: "ACCEPTED"}, nil
}

func (s *releaseStore) RecordShipped(_ context.Context, id string, version int, shipped decimal.Decimal, reset *string, _ string, _ time.Time) (store.Agreement, error) {
	s.shipped, s.reset, s.version = shipped, reset, version
	return store.Agreement{ID: id, Version: version + 1}, nil
}

const releaseBody = `{"releaseNo":"R-17","releaseType":"JIT","cumReceived":"1150","buckets":[{"date":"2027-04-15","cumRequired":"1500"}]}`

func TestCreateScheduleRelease(t *testing.T) {
	path := agreements + "/" + ids.New() + "/schedule-releases"
	s := &releaseStore{fresh: true}
	rec := serveBody(t, s, http.MethodPost, path, releaseBody, principal(dutyAgreement), map[string]string{idempotencyKey: "k1"})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d etag %s body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	c := s.created
	if c.ReleaseNo != "R-17" || c.ReleaseType != "JIT" || !c.CumReceived.Equal(decimal.NewFromInt(1150)) || len(c.Buckets) != 1 || c.IdempotencyKey != "k1" || c.Actor != "u1" || c.RequestHash == "" {
		t.Fatalf("store got %+v", c)
	}
	s.fresh = false
	if rec = serveBody(t, s, http.MethodPost, path, releaseBody, principal(dutyAgreement), map[string]string{idempotencyKey: "k1"}); rec.Code != http.StatusOK {
		t.Fatalf("replay status %d", rec.Code)
	}
	if rec = serveBody(t, s, http.MethodPost, path, releaseBody, principal(dutyRead), map[string]string{idempotencyKey: "k1"}); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty must not create: %d", rec.Code)
	}
	if rec = serveBody(t, s, http.MethodPost, path, releaseBody, principal(dutyAgreement), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key: %d", rec.Code)
	}
}

func TestBuildScheduleRelease_Table(t *testing.T) {
	good := createScheduleReleaseRequest{ReleaseNo: "R-1", ReleaseType: "FORECAST", CumReceived: "10",
		Buckets: []releaseBucketRequest{{Date: "2027-04-15", CumRequired: "20"}}}
	tests := []struct {
		name      string
		mutate    func(*createScheduleReleaseRequest)
		wantField string
	}{
		{"valid", func(*createScheduleReleaseRequest) {}, ""},
		{"no number", func(r *createScheduleReleaseRequest) { r.ReleaseNo = "" }, "releaseNo"},
		{"long number", func(r *createScheduleReleaseRequest) { r.ReleaseNo = strings.Repeat("x", 41) }, "releaseNo"},
		{"bad type", func(r *createScheduleReleaseRequest) { r.ReleaseType = "FIRM" }, "releaseType"},
		{"negative received", func(r *createScheduleReleaseRequest) { r.CumReceived = "-1" }, "cumReceived"},
		{"text received", func(r *createScheduleReleaseRequest) { r.CumReceived = "many" }, "cumReceived"},
		{"seven decimals", func(r *createScheduleReleaseRequest) { r.CumReceived = "1.0000001" }, "cumReceived"},
		{"no buckets", func(r *createScheduleReleaseRequest) { r.Buckets = nil }, "buckets"},
		{"bad date", func(r *createScheduleReleaseRequest) { r.Buckets[0].Date = "15.04.2027" }, "buckets[0].date"},
		{"negative required", func(r *createScheduleReleaseRequest) { r.Buckets[0].CumRequired = "-5" }, "buckets[0].cumRequired"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := good
			req.Buckets = append([]releaseBucketRequest(nil), good.Buckets...)
			tc.mutate(&req)
			_, fields := buildScheduleRelease(req)
			if tc.wantField == "" && len(fields) != 0 {
				t.Fatalf("unexpected %v", fields)
			}
			if _, ok := fields[tc.wantField]; tc.wantField != "" && !ok {
				t.Fatalf("want %q in %v", tc.wantField, fields)
			}
		})
	}
}

func TestResolveScheduleRelease(t *testing.T) {
	path := agreements + "/" + ids.New() + "/schedule-releases/" + ids.New() + ":resolve"
	s := &releaseStore{}
	body := `{"decision":"ACCEPT","reason":"customer confirmed the cumulative by phone"}`
	if rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyAgreement), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("If-Match is required: %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyAgreement), map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || s.resolved != "ACCEPT" || s.version != 2 || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("status %d decision %q version %d etag %s", rec.Code, s.resolved, s.version, rec.Header().Get("ETag"))
	}
	for _, bad := range []string{`{"decision":"MAYBE","reason":"x"}`, `{"decision":"REJECT","reason":""}`} {
		if rec := serveBody(t, s, http.MethodPost, path, bad, principal(dutyAgreement), map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, rec.Code)
		}
	}
	if rec := serveBody(t, s, http.MethodPost, agreements+"/"+ids.New()+"/schedule-releases/nope:resolve", body, principal(dutyAgreement), map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusNotFound {
		t.Fatalf("bad release id: %d", rec.Code)
	}
}

func TestRecordShipped(t *testing.T) {
	path := agreements + "/" + ids.New() + ":record-shipped"
	s := &releaseStore{}
	rec := serveBody(t, s, http.MethodPost, path, `{"cumShipped":"1200","cumResetDate":"2027-01-01"}`, principal(dutyAgreement), map[string]string{"If-Match": `"4"`})
	if rec.Code != http.StatusOK || !s.shipped.Equal(decimal.NewFromInt(1200)) || s.reset == nil || *s.reset != "2027-01-01" || s.version != 4 {
		t.Fatalf("status %d shipped %s reset %v version %d", rec.Code, s.shipped, s.reset, s.version)
	}
	for _, bad := range []string{`{"cumShipped":"-1"}`, `{"cumShipped":"x"}`, `{"cumShipped":"1","cumResetDate":"yesterday"}`} {
		if rec := serveBody(t, s, http.MethodPost, path, bad, principal(dutyAgreement), map[string]string{"If-Match": `"4"`}); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, rec.Code)
		}
	}
}

func TestBuildAgreement_CumTolerance(t *testing.T) {
	req := createAgreementRequest{LegalEntityID: ids.New(), CustomerID: ids.New(), ProductID: ids.New(), Unit: "EA", UnitPrice: "1", Currency: "USD", CommitmentQty: "10"}
	in, f := buildAgreement(req)
	if len(f) != 0 || !in.CumTolerance.IsZero() {
		t.Fatalf("default tolerance: %v %s", f, in.CumTolerance)
	}
	req.CumTolerance = strPtr("5")
	if in, f = buildAgreement(req); len(f) != 0 || !in.CumTolerance.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("tolerance 5: %v %s", f, in.CumTolerance)
	}
	for _, bad := range []string{"-1", "0.0000001", "100000000000000", "some"} {
		req.CumTolerance = strPtr(bad)
		if _, f = buildAgreement(req); f["cumTolerance"] == "" {
			t.Fatalf("tolerance %q accepted: %v", bad, f)
		}
	}
}
