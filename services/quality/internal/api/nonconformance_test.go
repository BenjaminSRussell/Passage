package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/store"
)

func (f *fakeStore) CreateNonconformance(_ context.Context, in domain.Nonconformance, key string) (domain.Nonconformance, bool, error) {
	if id, ok := f.ncrKeys[key]; ok && key != "" {
		return f.ncrs[id], false, nil
	}
	f.ncrs[in.ID] = in
	f.ncrKeys[key] = in.ID
	return in, true, nil
}

func (f *fakeStore) GetNonconformance(_ context.Context, id string) (domain.Nonconformance, error) {
	n, ok := f.ncrs[id]
	if !ok {
		return n, httpx.NewProblem(http.StatusNotFound, domain.CodeNCRMissing, "nonconformance not found")
	}
	return n, nil
}

func (f *fakeStore) ListNonconformances(_ context.Context, filter store.NCRFilter) ([]domain.Nonconformance, error) {
	f.ncrFilter = filter
	out := []domain.Nonconformance{}
	for _, n := range f.ncrs {
		if (filter.Status == "" || n.Status == filter.Status) && (filter.QualityOrderID == "" || n.QualityOrderID == filter.QualityOrderID) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateNonconformance(_ context.Context, id string, expected int, key string, change func(domain.Nonconformance) (domain.Nonconformance, error)) (domain.Nonconformance, bool, error) {
	if owner, ok := f.capaKeys[key]; ok && key != "" && owner == id {
		return f.ncrs[id], false, nil
	}
	cur, ok := f.ncrs[id]
	if !ok {
		return cur, false, httpx.NewProblem(http.StatusNotFound, domain.CodeNCRMissing, "nonconformance not found")
	}
	if cur.Version != expected {
		return cur, false, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	next, err := change(cur)
	if err != nil {
		return cur, false, err
	}
	next.Version++
	f.ncrs[id] = next
	if key != "" {
		f.capaKeys[key] = id
	}
	return next, true, nil
}

const (
	ncrItem   = "00000000-0000-0000-0000-0000000000aa"
	ncrVendor = "00000000-0000-0000-0000-0000000000bb"
	ncrBody   = `{"source":"VENDOR","itemId":"` + ncrItem + `","qty":"4","uom":"EA","problemType":"DIMENSION","severity":"MAJOR","vendorId":"` + ncrVendor + `"}`
)

const ncrDuties = "QM_VIEW,QM_NCR_MAINTAIN,QM_CAPA_MAINTAIN"

func decodeNCR(t *testing.T, body string) domain.Nonconformance {
	t.Helper()
	var n domain.Nonconformance
	if err := json.Unmarshal([]byte(body), &n); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return n
}

func createNCR(t *testing.T, h http.Handler) domain.Nonconformance {
	t.Helper()
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/nonconformances", body: ncrBody, duties: ncrDuties, key: ids.New()})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	return decodeNCR(t, rec.Body.String())
}

func TestCreateNonconformance_NewAtVersion1AndReplay(t *testing.T) {
	h := newHandler(newFake())
	key := ids.New()
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/nonconformances", body: ncrBody, duties: ncrDuties, key: key})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	n := decodeNCR(t, rec.Body.String())
	if n.Status != domain.NCRNew || n.Version != 1 || rec.Header().Get("ETag") != `"1"` || !n.Qty.Equal(decimal.NewFromInt(4)) || n.VendorID != ncrVendor {
		t.Fatalf("unexpected NCR %+v", n)
	}
	replay := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/nonconformances", body: ncrBody, duties: ncrDuties, key: key})
	if replay.Code != http.StatusOK || decodeNCR(t, replay.Body.String()).ID != n.ID {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
}

func TestCreateNonconformance_FieldErrorsAndDuty(t *testing.T) {
	h := newHandler(newFake())
	cases := []struct{ name, body, field string }{
		{"severity", strings.Replace(ncrBody, `"MAJOR"`, `"TRIVIAL"`, 1), "severity"},
		{"source", strings.Replace(ncrBody, `"VENDOR"`, `"PIGEON"`, 1), "source"},
		{"vendor link", strings.Replace(ncrBody, ncrVendor, "not-a-uuid", 1), "vendorId"},
		{"zero qty", strings.Replace(ncrBody, `"qty":"4"`, `"qty":"0"`, 1), "qty"},
		{"problem type", strings.Replace(ncrBody, `"DIMENSION"`, `"dim"`, 1), "problemType"},
		{"no unit", strings.Replace(ncrBody, `"uom":"EA",`, ``, 1), "uom"},
		{"no item", strings.Replace(ncrBody, `"itemId":"`+ncrItem+`",`, ``, 1), "itemId"},
	}
	for _, tc := range cases {
		rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/nonconformances", body: tc.body, duties: ncrDuties, key: ids.New()})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+tc.field+`"`) {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/nonconformances", body: ncrBody, duties: "QM_VIEW", key: ids.New()}); rec.Code != http.StatusForbidden {
		t.Fatalf("create without QM_NCR_MAINTAIN: %d", rec.Code)
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/nonconformances", body: ncrBody, duties: ncrDuties}); rec.Code != http.StatusBadRequest {
		t.Fatalf("create without Idempotency-Key: %d", rec.Code)
	}
}

// An NCR raised against a quality order (BR-33.4 amendment) takes the order's item
// and unit, and cannot exceed the lot.
func TestCreateNonconformance_AgainstQualityOrder(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	orderID, item := ids.New(), ids.New()
	f.orders[orderID] = domain.Order{ID: orderID, ItemID: item, UOM: "KG", LotQty: decimal.NewFromInt(10), Status: domain.StatusClosed,
		Dims: map[string]string{"batch": "B-7"}}
	body := `{"source":"INTERNAL","qualityOrderId":"` + orderID + `","qty":"10","problemType":"AMEND","severity":"MINOR"}`
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/nonconformances", body: body, duties: ncrDuties, key: ids.New()})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create against order: %d %s", rec.Code, rec.Body.String())
	}
	n := decodeNCR(t, rec.Body.String())
	if n.ItemID != item || n.UOM != "KG" || n.Dims["batch"] != "B-7" || n.QualityOrderID != orderID {
		t.Fatalf("order defaults not applied: %+v", n)
	}
	over := strings.Replace(body, `"qty":"10"`, `"qty":"11"`, 1)
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/nonconformances", body: over, duties: ncrDuties, key: ids.New()}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lot quantity") {
		t.Fatalf("qty over lot: %d %s", rec.Code, rec.Body.String())
	}
	other := strings.Replace(body, `"source"`, `"itemId":"`+ids.New()+`","source"`, 1)
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/nonconformances", body: other, duties: ncrDuties, key: ids.New()}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "itemId") {
		t.Fatalf("item of another order: %d %s", rec.Code, rec.Body.String())
	}
	missing := strings.Replace(body, orderID, ids.New(), 1)
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/nonconformances", body: missing, duties: ncrDuties, key: ids.New()}); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), domain.CodeOrderMissing) {
		t.Fatalf("unknown order: %d %s", rec.Code, rec.Body.String())
	}
}

func TestApproveRefuse_IfMatchAndLifecycle(t *testing.T) {
	h := newHandler(newFake())
	id := createNCR(t, h).ID
	approve := "/v1/quality/nonconformances/" + id + ":approve"
	if rec := call(h, reqSpec{method: http.MethodPost, path: approve, duties: ncrDuties}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: %d", rec.Code)
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: approve, duties: ncrDuties, ifMatch: `"7"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d", rec.Code)
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: approve, duties: "QM_VIEW", ifMatch: `"1"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("approve without QM_NCR_MAINTAIN: %d", rec.Code)
	}
	rec := call(h, reqSpec{method: http.MethodPost, path: approve, duties: ncrDuties, ifMatch: `"1"`})
	if rec.Code != http.StatusOK || decodeNCR(t, rec.Body.String()).Status != domain.NCRApproved || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	refuse := "/v1/quality/nonconformances/" + id + ":refuse"
	rec = call(h, reqSpec{method: http.MethodPost, path: refuse, body: `{"reason":"duplicate"}`, duties: ncrDuties, ifMatch: `"2"`})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), domain.CodeNCRStatus) {
		t.Fatalf("refuse after approve: %d %s", rec.Code, rec.Body.String())
	}

	other := createNCR(t, h).ID
	refuse = "/v1/quality/nonconformances/" + other + ":refuse"
	if rec := call(h, reqSpec{method: http.MethodPost, path: refuse, body: `{"reason":"  "}`, duties: ncrDuties, ifMatch: `"1"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("refuse without a reason: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, reqSpec{method: http.MethodPost, path: refuse, body: `{"reason":"not a defect"}`, duties: ncrDuties, ifMatch: `"1"`})
	if n := decodeNCR(t, rec.Body.String()); rec.Code != http.StatusOK || n.Status != domain.NCRRefused || n.StatusReason != "not a defect" {
		t.Fatalf("refuse: %d %s", rec.Code, rec.Body.String())
	}
}

// TestCorrection_8DOverHTTP walks an approved NCR through its 8D: start with a key,
// steps in order, and the effectiveness check guarded by its due date.
func TestCorrection_8DOverHTTP(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	id := createNCR(t, h).ID
	base := "/v1/quality/nonconformances/" + id
	call(h, reqSpec{method: http.MethodPost, path: base + ":approve", duties: ncrDuties, ifMatch: `"1"`})

	start := reqSpec{method: http.MethodPost, path: base + "/corrections", body: `{"effectivenessCheckDays":30}`, duties: ncrDuties, ifMatch: `"2"`}
	if rec := call(h, reqSpec{method: http.MethodPost, path: base + "/corrections", body: `{}`, duties: "QM_VIEW,QM_NCR_MAINTAIN", key: "c-1", ifMatch: `"2"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("start without QM_CAPA_MAINTAIN: %d", rec.Code)
	}
	if rec := call(h, start); rec.Code != http.StatusBadRequest {
		t.Fatalf("start without Idempotency-Key: %d", rec.Code)
	}
	start.key = "c-1"
	rec := call(h, start)
	n := decodeNCR(t, rec.Body.String())
	if rec.Code != http.StatusCreated || n.Status != domain.NCRInCorrection || n.Correction == nil || n.Correction.CurrentStep != 1 ||
		n.Correction.EffectivenessDays != 30 || len(n.Correction.Steps) != 8 || n.Correction.Steps[2].DueAt == nil {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	if replay := call(h, start); replay.Code != http.StatusOK {
		t.Fatalf("replayed start: %d %s", replay.Code, replay.Body.String())
	}

	step := func(no int, version int) *httptest.ResponseRecorder {
		return call(h, reqSpec{method: http.MethodPost, path: base + ":complete-step", duties: ncrDuties, user: "engineer",
			body: `{"step":` + strconv.Itoa(no) + `,"notes":"D` + strconv.Itoa(no) + ` done"}`, ifMatch: `"` + strconv.Itoa(version) + `"`})
	}
	if r := step(2, 3); r.Code != http.StatusConflict || !strings.Contains(r.Body.String(), domain.CodeCAPAStep) {
		t.Fatalf("D2 before D1: %d %s", r.Code, r.Body.String())
	}
	version := 3
	for no := 1; no <= 7; no++ {
		if r := step(no, version); r.Code != http.StatusOK {
			t.Fatalf("D%d: %d %s", no, r.Code, r.Body.String())
		}
		version++
	}
	n = f.ncrs[id]
	if n.Correction.Status != domain.CAPAEffectivenessPending || n.Correction.EffectivenessDueAt == nil || n.Correction.Steps[6].CompletedBy != "engineer" {
		t.Fatalf("after D7: %+v", n.Correction)
	}
	check := reqSpec{method: http.MethodPost, path: base + ":check-effectiveness", body: `{"result":"EFFECTIVE","note":"no recurrence"}`,
		duties: ncrDuties, user: "reviewer", ifMatch: `"` + strconv.Itoa(version) + `"`}
	if rec := call(h, check); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeEffectivenessNotDue) {
		t.Fatalf("check before due: %d %s", rec.Code, rec.Body.String())
	}
	// Let the 30 days pass.
	due := time.Now().Add(-time.Hour)
	n.Correction.EffectivenessDueAt = &due
	f.ncrs[id] = n
	if rec := call(h, reqSpec{method: http.MethodGet, path: base, duties: "QM_VIEW"}); !strings.Contains(rec.Body.String(), `"effectivenessCheckDue":true`) {
		t.Fatalf("effectiveness check due on read: %s", rec.Body.String())
	}
	self := check
	self.user = "engineer"
	if rec := call(h, self); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeEffectivenessSoD) {
		t.Fatalf("D7 owner checks own action: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, check)
	n = decodeNCR(t, rec.Body.String())
	if rec.Code != http.StatusOK || n.Status != domain.NCRClosed || n.Correction.Status != domain.CAPAClosed || n.Correction.EffectivenessResult != domain.Effective {
		t.Fatalf("effective check: %d %s", rec.Code, rec.Body.String())
	}
}

func TestListAndGetNonconformance(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	id := createNCR(t, h).ID
	rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/nonconformances?status=APPROVED", duties: "QM_VIEW"})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), id) {
		t.Fatalf("approved filter should be empty: %d %s", rec.Code, rec.Body.String())
	}
	order := ids.New()
	rec = call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/nonconformances?status=NEW&severity=MAJOR&source=VENDOR&qualityOrderId=" + order + "&limit=5", duties: "QM_VIEW"})
	if rec.Code != http.StatusOK || f.ncrFilter != (store.NCRFilter{Status: "NEW", Source: "VENDOR", Severity: "MAJOR", QualityOrderID: order, Limit: 5}) {
		t.Fatalf("filter: %d %+v", rec.Code, f.ncrFilter)
	}
	for _, q := range []string{"status=BOGUS", "source=X", "severity=LOW", "qualityOrderId=nope", "limit=0"} {
		if rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/nonconformances?" + q, duties: "QM_VIEW"}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	rec = call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/nonconformances/" + id, duties: "QM_VIEW"})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d", rec.Code)
	}
	for _, missing := range []string{ids.New(), "not-a-uuid"} {
		rec = call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/nonconformances/" + missing, duties: "QM_VIEW"})
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), domain.CodeNCRMissing) {
			t.Fatalf("missing NCR %s: %d %s", missing, rec.Code, rec.Body.String())
		}
	}
}

func TestCorrection_ServiceDutyReachesOnlyTheCAPARoutes(t *testing.T) {
	h := newHandler(newFake())
	id := createNCR(t, h).ID
	base := "/v1/quality/nonconformances/" + id
	call(h, reqSpec{method: http.MethodPost, path: base + ":approve", duties: ncrDuties, ifMatch: `"1"`})
	const service = "quality.capa.execute"

	if rec := call(h, reqSpec{method: http.MethodGet, path: base, duties: service}); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("read with the service duty: %d %s", rec.Code, rec.Body.String())
	}
	rec := call(h, reqSpec{method: http.MethodPost, path: base + "/corrections", body: `{"effectivenessCheckDays":30}`, duties: service, key: "capa:" + id, ifMatch: `"2"`})
	if rec.Code != http.StatusCreated || decodeNCR(t, rec.Body.String()).Status != domain.NCRInCorrection {
		t.Fatalf("start with the service duty: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, reqSpec{method: http.MethodPost, path: base + ":complete-step", body: `{"step":1,"notes":"team named"}`, duties: service, user: "engineer", ifMatch: `"3"`})
	if rec.Code != http.StatusOK || decodeNCR(t, rec.Body.String()).Correction.CurrentStep != 2 {
		t.Fatalf("step with the service duty: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: base + ":check-effectiveness", body: `{"result":"EFFECTIVE","note":"n"}`, duties: service, user: "reviewer", ifMatch: `"4"`}); rec.Code != http.StatusConflict {
		t.Fatalf("check with the service duty reaches the domain rules: %d %s", rec.Code, rec.Body.String())
	}

	for name, spec := range map[string]reqSpec{
		"create":  {method: http.MethodPost, path: "/v1/quality/nonconformances", body: ncrBody, key: ids.New()},
		"approve": {method: http.MethodPost, path: base + ":approve", ifMatch: `"4"`},
		"refuse":  {method: http.MethodPost, path: base + ":refuse", body: `{"reason":"x"}`, ifMatch: `"4"`},
		"list":    {method: http.MethodGet, path: "/v1/quality/nonconformances"},
	} {
		spec.duties = service
		if rec := call(h, spec); rec.Code != http.StatusForbidden {
			t.Errorf("%s with the service duty: %d, want 403", name, rec.Code)
		}
	}
}
