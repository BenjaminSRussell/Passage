package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// The fake store below mirrors the store's contracts for buffer profiles,
// stored buffers and quota arrangements: unique codes, idempotent creates,
// If-Match versions, one buffer per scope and non-overlapping validity.

func (f *fakeStore) ListBufferProfiles(_ context.Context, limit int) ([]store.BufferProfile, error) {
	f.lastLimit = limit
	return append([]store.BufferProfile{}, f.profiles...), nil
}

func (f *fakeStore) GetBufferProfile(_ context.Context, id string) (store.BufferProfile, error) {
	for _, p := range f.profiles {
		if p.ID == id {
			return p, nil
		}
	}
	return store.BufferProfile{}, httpx.NewProblem(http.StatusNotFound, store.CodeProfileNotFound, "buffer profile not found")
}

func (f *fakeStore) profileCodeTaken(self, code string) bool {
	for _, p := range f.profiles {
		if p.ID != self && p.Spec.Code == code {
			return true
		}
	}
	return false
}

func (f *fakeStore) CreateBufferProfile(_ context.Context, req store.BufferProfileCreate) (store.BufferProfile, bool, error) {
	if hash, ok := f.profileKeys[req.IdempotencyKey]; ok && req.IdempotencyKey != "" {
		if hash != req.InputHash {
			return store.BufferProfile{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
		}
		return f.profiles[len(f.profiles)-1], true, nil
	}
	if f.profileCodeTaken("", req.Spec.Code) {
		return store.BufferProfile{}, false, httpx.NewProblem(http.StatusConflict, store.CodeProfileExists, "exists")
	}
	if req.IdempotencyKey != "" {
		if f.profileKeys == nil {
			f.profileKeys = map[string]string{}
		}
		f.profileKeys[req.IdempotencyKey] = req.InputHash
	}
	p := store.BufferProfile{ID: testkit.TenantID(len(f.profiles) + 40), Version: 1, Spec: req.Spec}
	f.profiles = append(f.profiles, p)
	return p, false, nil
}

func (f *fakeStore) UpdateBufferProfile(_ context.Context, id string, expected int, spec domain.BufferProfileSpec) (store.BufferProfile, error) {
	for i, p := range f.profiles {
		if p.ID != id {
			continue
		}
		if p.Version != expected {
			return store.BufferProfile{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
		}
		if f.profileCodeTaken(id, spec.Code) {
			return store.BufferProfile{}, httpx.NewProblem(http.StatusConflict, store.CodeProfileExists, "exists")
		}
		f.profiles[i] = store.BufferProfile{ID: id, Version: p.Version + 1, Spec: spec}
		return f.profiles[i], nil
	}
	return store.BufferProfile{}, httpx.NewProblem(http.StatusNotFound, store.CodeProfileNotFound, "buffer profile not found")
}

// UpsertDdmrpBuffer replaces the stored buffer of the same item, site and warehouse scope.
func (f *fakeStore) UpsertDdmrpBuffer(_ context.Context, b store.DdmrpBuffer) (store.DdmrpBuffer, error) {
	b.CalculatedAt = "2026-11-02T00:15:00Z"
	for _, p := range f.profiles {
		if p.ID == b.ProfileID {
			b.ProfileCode = p.Spec.Code
		}
	}
	for i, prev := range f.buffers {
		if prev.ItemID == b.ItemID && prev.SiteID == b.SiteID && prev.WarehouseID == b.WarehouseID {
			b.ID = prev.ID
			f.buffers[i] = b
			return b, nil
		}
	}
	b.ID = testkit.TenantID(len(f.buffers) + 60)
	f.buffers = append(f.buffers, b)
	return b, nil
}

func (f *fakeStore) ListDdmrpBuffers(_ context.Context, q store.BufferQuery) ([]store.DdmrpBuffer, error) {
	f.bufferQuery = q
	out := []store.DdmrpBuffer{}
	for _, b := range f.buffers {
		if (q.SiteID == "" || b.SiteID == q.SiteID) && (q.Zone == "" || b.Status.Zone == q.Zone) &&
			(q.MaxPriority == nil || b.Status.Priority.LessThanOrEqual(*q.MaxPriority)) {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeStore) ListQuotaArrangements(_ context.Context, q store.QuotaQuery) ([]store.QuotaArrangement, error) {
	f.quotaQuery = q
	out := []store.QuotaArrangement{}
	for _, a := range f.quotas {
		if (q.ItemID == "" || a.Spec.ItemID == q.ItemID) && (q.SiteID == "" || a.Spec.SiteID == q.SiteID) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeStore) GetQuotaArrangement(_ context.Context, id string) (store.QuotaArrangement, error) {
	for _, a := range f.quotas {
		if a.ID == id {
			return a, nil
		}
	}
	return store.QuotaArrangement{}, httpx.NewProblem(http.StatusNotFound, store.CodeQuotaNotFound, "quota arrangement not found")
}

func (f *fakeStore) quotaOverlaps(self string, spec domain.QuotaArrangementSpec) bool {
	for _, a := range f.quotas {
		if a.ID != self && a.Spec.ItemID == spec.ItemID && a.Spec.SiteID == spec.SiteID &&
			domain.ArrangementsOverlap(a.Spec.ValidFrom, a.Spec.ValidTo, spec.ValidFrom, spec.ValidTo) {
			return true
		}
	}
	return false
}

func withSourceIDs(spec domain.QuotaArrangementSpec, base int) domain.QuotaArrangementSpec {
	sources := make([]domain.QuotaSourceSpec, len(spec.Sources))
	for i, s := range spec.Sources {
		s.ID = testkit.TenantID(base + i)
		sources[i] = s
	}
	spec.Sources = sources
	return spec
}

func (f *fakeStore) CreateQuotaArrangement(_ context.Context, req store.QuotaCreate) (store.QuotaArrangement, bool, error) {
	if hash, ok := f.quotaKeys[req.IdempotencyKey]; ok && req.IdempotencyKey != "" {
		if hash != req.InputHash {
			return store.QuotaArrangement{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
		}
		return f.quotas[len(f.quotas)-1], true, nil
	}
	if f.quotaOverlaps("", req.Spec) {
		return store.QuotaArrangement{}, false, httpx.NewProblem(http.StatusConflict, store.CodeQuotaOverlap, "overlap")
	}
	if req.IdempotencyKey != "" {
		if f.quotaKeys == nil {
			f.quotaKeys = map[string]string{}
		}
		f.quotaKeys[req.IdempotencyKey] = req.InputHash
	}
	a := store.QuotaArrangement{ID: testkit.TenantID(len(f.quotas) + 80), Version: 1, Spec: withSourceIDs(req.Spec, 100+10*len(f.quotas))}
	f.quotas = append(f.quotas, a)
	return a, false, nil
}

func (f *fakeStore) UpdateQuotaArrangement(_ context.Context, id string, expected int, spec domain.QuotaArrangementSpec) (store.QuotaArrangement, error) {
	for i, a := range f.quotas {
		if a.ID != id {
			continue
		}
		if a.Version != expected {
			return store.QuotaArrangement{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
		}
		if f.quotaOverlaps(id, spec) {
			return store.QuotaArrangement{}, httpx.NewProblem(http.StatusConflict, store.CodeQuotaOverlap, "overlap")
		}
		f.quotas[i] = store.QuotaArrangement{ID: id, Version: a.Version + 1, Spec: withSourceIDs(spec, 200+10*i)}
		return f.quotas[i], nil
	}
	return store.QuotaArrangement{}, httpx.NewProblem(http.StatusNotFound, store.CodeQuotaNotFound, "quota arrangement not found")
}

const (
	profilePath = "/v1/planning/buffer-profiles"
	bufferPath  = "/v1/planning/ddmrp/buffers"
	quotaPath   = "/v1/planning/quota-arrangements"
	dutyRunPlan = "planning.run"

	// br2211Profile is the BR-22.11 profile: LTF 0.5, VF 0.5, MOQ 0, order cycle 7 days.
	br2211Profile = `{"code":"BUF-MFG","itemType":"manufactured","leadTimeFactor":"0.5","variabilityFactor":"0.5","orderCycleDays":7}`
	vendorA       = "00000000-0000-0000-0000-0000000000e1"
	vendorB       = "00000000-0000-0000-0000-0000000000e2"
)

func decodeProfile(t *testing.T, rec *httptest.ResponseRecorder) bufferProfileRecordDTO {
	t.Helper()
	var p bufferProfileRecordDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return p
}

func decodeBuffer(t *testing.T, rec *httptest.ResponseRecorder) ddmrpBufferDTO {
	t.Helper()
	var b ddmrpBufferDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return b
}

func decodeQuota(t *testing.T, rec *httptest.ResponseRecorder) quotaArrangementDTO {
	t.Helper()
	var a quotaArrangementDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return a
}

func seedProfile(t *testing.T, st *fakeStore) string {
	t.Helper()
	rec := do(t, st, http.MethodPost, profilePath, br2211Profile, dutySetup, true, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed profile: %d %s", rec.Code, rec.Body.String())
	}
	return decodeProfile(t, rec).ID
}

// TestCreateBufferProfile_AppliesDefaults: omitted spike and ADU-window fields
// take the 4.22.13 defaults (50 percent, 90 past days, 0 future days) and the
// factors come back as the exact decimal strings sent.
func TestCreateBufferProfile_AppliesDefaults(t *testing.T) {
	st := &fakeStore{}
	rec := do(t, st, http.MethodPost, profilePath, br2211Profile, dutySetup, true, "")
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	got := decodeProfile(t, rec)
	if got.SpikeThresholdPct != "50" || got.AduWindowPastDays != 90 || got.AduWindowFutureDays != 0 || got.MinOrderQty != "0" ||
		got.SpikeHorizonDays != nil || got.LeadTimeFactor != "0.5" || got.VariabilityFactor != "0.5" || got.Version != 1 {
		t.Fatalf("defaults not applied: %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"spikeHorizonDays":null`) {
		t.Fatalf("an absent spike horizon (DLT) must be sent as null: %s", rec.Body.String())
	}
}

func TestCreateBufferProfile_Refusals(t *testing.T) {
	cases := []struct {
		name, body, duties string
		status             int
		fields             []string
	}{
		{"code and item type", `{"code":"x","itemType":"service","leadTimeFactor":"0.5","variabilityFactor":"0.5"}`, dutySetup,
			http.StatusBadRequest, []string{"code", "itemType"}},
		{"factors beyond numeric(5,3)", `{"code":"B1","itemType":"purchased","leadTimeFactor":"0.1234","variabilityFactor":"100"}`, dutySetup,
			http.StatusBadRequest, []string{"leadTimeFactor", "variabilityFactor"}},
		{"factor that is not a decimal", `{"code":"B1","itemType":"purchased","leadTimeFactor":"half","variabilityFactor":"0.5"}`, dutySetup,
			http.StatusBadRequest, []string{"leadTimeFactor"}},
		{"spike threshold below 1 percent", `{"code":"B1","itemType":"purchased","leadTimeFactor":"0.5","variabilityFactor":"0.5","spikeThresholdPct":"0"}`,
			dutySetup, http.StatusBadRequest, []string{"spikeThresholdPct"}},
		{"ADU window shorter than 7 days", `{"code":"B1","itemType":"purchased","leadTimeFactor":"0.5","variabilityFactor":"0.5","aduWindowPastDays":3,"aduWindowFutureDays":3}`,
			dutySetup, http.StatusBadRequest, []string{"aduWindowPastDays"}},
		{"factors are strings, not JSON numbers", `{"code":"B1","itemType":"purchased","leadTimeFactor":0.5,"variabilityFactor":"0.5"}`, dutySetup,
			http.StatusBadRequest, nil},
		{"read duty cannot create", br2211Profile, dutyRead, http.StatusForbidden, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeStore{}
			rec := do(t, st, http.MethodPost, profilePath, tc.body, tc.duties, true, "")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if len(st.profiles) != 0 {
				t.Fatalf("a refused create was stored: %+v", st.profiles)
			}
			p := problemOf(t, rec)
			for _, f := range tc.fields {
				if _, ok := p.Errors[f]; !ok {
					t.Fatalf("field %s not named: %+v", f, p.Errors)
				}
			}
		})
	}
}

func TestCreateBufferProfile_DuplicateAndIdempotency(t *testing.T) {
	st := &fakeStore{}
	first := do(t, st, http.MethodPost, profilePath, br2211Profile, dutySetup, true, "k-1")
	if first.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", first.Code, first.Body.String())
	}
	replay := do(t, st, http.MethodPost, profilePath, br2211Profile, dutySetup, true, "k-1")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" ||
		decodeProfile(t, replay).ID != decodeProfile(t, first).ID || len(st.profiles) != 1 {
		t.Fatalf("replay: %d %s (%d stored)", replay.Code, replay.Body.String(), len(st.profiles))
	}
	other := strings.Replace(br2211Profile, `"orderCycleDays":7`, `"orderCycleDays":14`, 1)
	if rec := do(t, st, http.MethodPost, profilePath, other, dutySetup, true, "k-1"); rec.Code != http.StatusUnprocessableEntity ||
		problemOf(t, rec).Code != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse with another body: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, st, http.MethodPost, profilePath, br2211Profile, dutySetup, true, ""); rec.Code != http.StatusConflict ||
		problemOf(t, rec).Code != store.CodeProfileExists {
		t.Fatalf("duplicate code: %d %s", rec.Code, rec.Body.String())
	}
}

func TestBufferProfile_ListGetAndUpdate(t *testing.T) {
	st := &fakeStore{}
	id := seedProfile(t, st)
	list := do(t, st, http.MethodGet, profilePath+"?limit=10", "", dutyRead, true, "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"code":"BUF-MFG"`) || st.lastLimit != 10 {
		t.Fatalf("list: %d %s (limit %d)", list.Code, list.Body.String(), st.lastLimit)
	}
	get := do(t, st, http.MethodGet, profilePath+"/"+id, "", dutyRead, true, "")
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d etag %q", get.Code, get.Header().Get("ETag"))
	}
	if rec := do(t, st, http.MethodGet, profilePath+"/not-a-uuid", "", dutyRead, true, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("ill-formed id: %d", rec.Code)
	}
	update := `{"code":"BUF-MFG","itemType":"manufactured","leadTimeFactor":"0.7","variabilityFactor":"0.25","spikeHorizonDays":5}`
	if rec := doIfMatch(t, st, http.MethodPut, profilePath+"/"+id, update, "", dutySetup); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("update without If-Match: %d", rec.Code)
	}
	if rec := doIfMatch(t, st, http.MethodPut, profilePath+"/"+id, update, `"1"`, dutyRead); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty update: %d", rec.Code)
	}
	rec := doIfMatch(t, st, http.MethodPut, profilePath+"/"+id, update, `"1"`, dutySetup)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if p := decodeProfile(t, rec); p.LeadTimeFactor != "0.7" || p.SpikeHorizonDays == nil || *p.SpikeHorizonDays != 5 || p.OrderCycleDays != 0 {
		t.Fatalf("updated profile (omitted order cycle resets to 0): %+v", p)
	}
	if stale := doIfMatch(t, st, http.MethodPut, profilePath+"/"+id, update, `"1"`, dutySetup); stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale update: %d", stale.Code)
	}
}

// br2211Recalc is the BR-22.11 state: ADU 10, DLT 10 days, on-hand 90, open
// supply 60 and 20 due today (qualified demand).
func br2211Recalc(profileID string) string {
	return `{"itemId":"` + fgID + `","siteId":"` + siteID + `","profileId":"` + profileID + `","adu":"10","dltDays":"10",
		"onHand":"90","openSupply":"60","demand":[{"day":0,"qty":"20"}]}`
}

// TestRecalculateBuffer_BR2211WorkedExample is AC-22.12 on the stored path:
// zones 50/25/75/100/175/70/245, NFP 130, order 115, priority 0.5306, yellow;
// a second recalculation replaces the buffer of the same scope.
func TestRecalculateBuffer_BR2211WorkedExample(t *testing.T) {
	st := &fakeStore{}
	id := seedProfile(t, st)
	rec := do(t, st, http.MethodPost, bufferPath+":recalculate", br2211Recalc(id), dutyRunPlan, true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("recalculate: %d %s", rec.Code, rec.Body.String())
	}
	b := decodeBuffer(t, rec)
	z := b.Zones
	if z.RedBase != "50" || z.RedSafety != "25" || z.TopOfRed != "75" || z.Yellow != "100" || z.TopOfYellow != "175" ||
		z.Green != "70" || z.TopOfGreen != "245" {
		t.Fatalf("zones: %+v", z)
	}
	if b.NetFlow != "130" || b.QualifiedDemand != "20" || b.PlannedQty != "115" || b.Priority != "0.5306" || b.Zone != domain.ZoneYellow ||
		b.ProfileCode != "BUF-MFG" || b.WarehouseID != nil {
		t.Fatalf("buffer: %+v", b)
	}
	again := do(t, st, http.MethodPost, bufferPath+":recalculate", strings.Replace(br2211Recalc(id), `"onHand":"90"`, `"onHand":"300"`, 1), dutyRunPlan, true, "")
	if again.Code != http.StatusOK || len(st.buffers) != 1 || decodeBuffer(t, again).ID != b.ID {
		t.Fatalf("second recalculation must replace the scope's buffer: %d %s (%d stored)", again.Code, again.Body.String(), len(st.buffers))
	}
	if g := decodeBuffer(t, again); g.Zone != domain.ZoneGreen || g.PlannedQty != "0" {
		t.Fatalf("NFP 340 above TOY is green with no order: %+v", g)
	}
}

func TestRecalculateBuffer_Refusals(t *testing.T) {
	st := &fakeStore{}
	id := seedProfile(t, st)
	unknown := testkit.TenantID(99)
	cases := []struct {
		name, body, duties string
		status             int
		code               string
	}{
		{"setup duty cannot recalculate", br2211Recalc(id), dutySetup, http.StatusForbidden, ""},
		{"unknown profile is 422", br2211Recalc(unknown), dutyRunPlan, http.StatusUnprocessableEntity, store.CodeProfileNotFound},
		{"ill-formed ids", `{"itemId":"x","siteId":"y","profileId":"z","adu":"1"}`, dutyRunPlan, http.StatusBadRequest, codeValidation},
		{"DLT beyond numeric(8,2)", strings.Replace(br2211Recalc(id), `"dltDays":"10"`, `"dltDays":"10.125"`, 1), dutyRunPlan,
			http.StatusBadRequest, codeValidation},
		{"neither ADU nor history", strings.Replace(br2211Recalc(id), `"adu":"10",`, ``, 1), dutyRunPlan, http.StatusBadRequest, codeValidation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, st, http.MethodPost, bufferPath+":recalculate", tc.body, tc.duties, true, "")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.code != "" && problemOf(t, rec).Code != tc.code {
				t.Fatalf("code %q, want %q", problemOf(t, rec).Code, tc.code)
			}
			if len(st.buffers) != 0 {
				t.Fatalf("a refused recalculation was stored: %+v", st.buffers)
			}
		})
	}
}

func TestListBuffers_FiltersBySiteZoneAndPriority(t *testing.T) {
	st := &fakeStore{}
	id := seedProfile(t, st)
	if rec := do(t, st, http.MethodPost, bufferPath+":recalculate", br2211Recalc(id), dutyRunPlan, true, ""); rec.Code != http.StatusOK {
		t.Fatalf("recalculate: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(t, st, http.MethodGet, bufferPath+"?siteId="+siteID+"&zone=yellow&maxPriority=0.6&limit=50", "", dutyRead, true, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"priority":"0.5306"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	q := st.bufferQuery
	if q.SiteID != siteID || q.Zone != domain.ZoneYellow || q.MaxPriority == nil || q.MaxPriority.String() != "0.6" || q.Limit != 50 {
		t.Fatalf("query passed to the store: %+v", q)
	}
	if rec := do(t, st, http.MethodGet, bufferPath+"?maxPriority=0.5", "", dutyRead, true, ""); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("priority 0.5306 is above 0.5: %s", rec.Body.String())
	}
	bad := do(t, st, http.MethodGet, bufferPath+"?zone=blue&siteId=x&maxPriority=high", "", dutyRead, true, "")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad filters: %d", bad.Code)
	}
	for _, f := range []string{"zone", "siteId", "maxPriority"} {
		if _, ok := problemOf(t, bad).Errors[f]; !ok {
			t.Fatalf("filter %s not named: %s", f, bad.Body.String())
		}
	}
}

// br2212Quota is the BR-22.12 arrangement: vendor A quota 60 allocated 300,
// vendor B quota 40 allocated 100.
func br2212Quota(from, to string) string {
	return `{"itemId":"` + fgID + `","siteId":"` + siteID + `","validFrom":"` + from + `","validTo":"` + to + `","sources":[
		{"type":"vendor","vendorId":"` + vendorA + `","quota":"60","allocatedQty":"300","priority":1},
		{"type":"vendor","vendorId":"` + vendorB + `","quota":"40","allocatedQty":"100","priority":2}]}`
}

// TestCreateQuotaArrangement_BR2212RatingsAndOverlap: the stored sources carry
// their BR-22.12 ratings (A 5.00, B 2.50), and an arrangement for the same item
// and site whose validity touches the first one is 409, the next day is fine.
func TestCreateQuotaArrangement_BR2212RatingsAndOverlap(t *testing.T) {
	st := &fakeStore{}
	rec := do(t, st, http.MethodPost, quotaPath, br2212Quota("2026-11-01", "2026-12-31"), dutySetup, true, "")
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	a := decodeQuota(t, rec)
	if len(a.Sources) != 2 || a.MinSplitQty != "0" || a.Split || a.ValidFrom != "2026-11-01" {
		t.Fatalf("arrangement: %+v", a)
	}
	ratingA, ratingB := a.Sources[0].Rating, a.Sources[1].Rating
	if ratingA == nil || *ratingA != "5" || ratingB == nil || *ratingB != "2.5" || *a.Sources[1].VendorID != vendorB ||
		a.Sources[1].ProductionVersionID != nil {
		t.Fatalf("sources: %+v", a.Sources)
	}
	overlap := do(t, st, http.MethodPost, quotaPath, br2212Quota("2026-12-31", "2027-03-31"), dutySetup, true, "")
	if overlap.Code != http.StatusConflict || problemOf(t, overlap).Code != store.CodeQuotaOverlap {
		t.Fatalf("overlap on the last day: %d %s", overlap.Code, overlap.Body.String())
	}
	if next := do(t, st, http.MethodPost, quotaPath, br2212Quota("2027-01-01", "2027-03-31"), dutySetup, true, ""); next.Code != http.StatusCreated {
		t.Fatalf("adjacent period: %d %s", next.Code, next.Body.String())
	}
}

func TestCreateQuotaArrangement_Refusals(t *testing.T) {
	wh := "00000000-0000-0000-0000-0000000000d1"
	source := func(s string) string {
		return `{"itemId":"` + fgID + `","siteId":"` + siteID + `","validFrom":"2026-11-01","validTo":"2026-12-31","sources":[` + s + `]}`
	}
	cases := []struct {
		name, body, duties string
		status             int
		fields             []string
	}{
		{"dates out of order", `{"itemId":"` + fgID + `","siteId":"` + siteID + `","validFrom":"2026-12-01","validTo":"2026-11-01","sources":[{"type":"vendor","vendorId":"` + vendorA + `","quota":"1"}]}`,
			dutySetup, http.StatusBadRequest, []string{"validTo"}},
		{"date not a date", `{"itemId":"` + fgID + `","siteId":"` + siteID + `","validFrom":"tomorrow","validTo":"2026-11-01","sources":[{"type":"vendor","vendorId":"` + vendorA + `","quota":"1"}]}`,
			dutySetup, http.StatusBadRequest, []string{"validFrom"}},
		{"no sources", source(``), dutySetup, http.StatusBadRequest, []string{"sources"}},
		{"quotas add up to 0", source(`{"type":"vendor","vendorId":"` + vendorA + `","quota":"0"}`), dutySetup, http.StatusBadRequest, []string{"sources"}},
		{"reference of the type missing, another set", source(`{"type":"transfer","vendorId":"` + vendorA + `","quota":"1"}`), dutySetup,
			http.StatusBadRequest, []string{"sources[0].fromWarehouseId", "sources[0].vendorId"}},
		{"same vendor twice", source(`{"type":"vendor","vendorId":"` + vendorA + `","quota":"1"},{"type":"vendor","vendorId":"` + vendorA + `","quota":"2"}`),
			dutySetup, http.StatusBadRequest, []string{"sources[1].vendorId"}},
		{"period maximum without period", source(`{"type":"transfer","fromWarehouseId":"` + wh + `","quota":"1","maxQtyPerPeriod":"100"}`), dutySetup,
			http.StatusBadRequest, []string{"sources[0].maxQtyPerPeriod"}},
		{"quota beyond numeric(9,4)", source(`{"type":"vendor","vendorId":"` + vendorA + `","quota":"0.12345"}`), dutySetup,
			http.StatusBadRequest, []string{"sources[0].quota"}},
		{"unknown source type", source(`{"type":"kanban","quota":"1"}`), dutySetup, http.StatusBadRequest, []string{"sources[0].type"}},
		{"read duty cannot create", br2212Quota("2026-11-01", "2026-12-31"), dutyRead, http.StatusForbidden, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeStore{}
			rec := do(t, st, http.MethodPost, quotaPath, tc.body, tc.duties, true, "")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if len(st.quotas) != 0 {
				t.Fatalf("a refused create was stored: %+v", st.quotas)
			}
			p := problemOf(t, rec)
			for _, f := range tc.fields {
				if _, ok := p.Errors[f]; !ok {
					t.Fatalf("field %s not named: %+v", f, p.Errors)
				}
			}
		})
	}
}

func TestQuotaArrangement_IdempotencyListGetAndUpdate(t *testing.T) {
	st := &fakeStore{}
	body := br2212Quota("2026-11-01", "2026-12-31")
	first := do(t, st, http.MethodPost, quotaPath, body, dutySetup, true, "q-1")
	replay := do(t, st, http.MethodPost, quotaPath, body, dutySetup, true, "q-1")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || len(st.quotas) != 1 {
		t.Fatalf("replay: %d (%d stored)", replay.Code, len(st.quotas))
	}
	if rec := do(t, st, http.MethodPost, quotaPath, br2212Quota("2027-01-01", "2027-01-31"), dutySetup, true, "q-1"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("key reuse with another body: %d", rec.Code)
	}
	id := decodeQuota(t, first).ID

	list := do(t, st, http.MethodGet, quotaPath+"?itemId="+fgID+"&siteId="+siteID+"&validOn=2026-11-15", "", dutyRead, true, "")
	q := st.quotaQuery
	if list.Code != http.StatusOK || q.ItemID != fgID || q.SiteID != siteID || q.ValidOn == nil || q.ValidOn.Format(dateLayout) != "2026-11-15" ||
		q.Limit != store.MaxPageSize {
		t.Fatalf("list: %d query %+v", list.Code, q)
	}
	if rec := do(t, st, http.MethodGet, quotaPath+"?validOn=15.11.2026", "", dutyRead, true, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad validOn: %d", rec.Code)
	}
	if rec := do(t, st, http.MethodGet, quotaPath+"/"+id, "", dutyRead, true, ""); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec := do(t, st, http.MethodGet, quotaPath+"/"+testkit.TenantID(77), "", dutyRead, true, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}

	split := strings.Replace(body, `"sources"`, `"split":true,"minSplitQty":"25","sources"`, 1)
	if rec := doIfMatch(t, st, http.MethodPut, quotaPath+"/"+id, split, "", dutySetup); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("update without If-Match: %d", rec.Code)
	}
	rec := doIfMatch(t, st, http.MethodPut, quotaPath+"/"+id, split, `"1"`, dutySetup)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if a := decodeQuota(t, rec); !a.Split || a.MinSplitQty != "25" || a.Version != 2 {
		t.Fatalf("updated arrangement: %+v", a)
	}
	if stale := doIfMatch(t, st, http.MethodPut, quotaPath+"/"+id, split, `"1"`, dutySetup); stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale update: %d", stale.Code)
	}
}
