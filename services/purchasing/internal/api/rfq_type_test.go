package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func (f *fakeStore) CreateRFQType(_ context.Context, in domain.RFQType, _ store.Idem) (domain.RFQType, error) {
	in.ID, in.Version = ids.New(), 1
	f.typeSaved = &in
	return in, nil
}

func (f *fakeStore) GetRFQType(_ context.Context, id string) (domain.RFQType, error) {
	if t, ok := f.rfqTypes[id]; ok {
		return t, nil
	}
	return domain.RFQType{}, httpx.NewProblem(http.StatusNotFound, store.CodeRFQTypeNotFound, "not found")
}

func (f *fakeStore) ListRFQTypes(context.Context, store.RFQTypeFilter, string, int) (store.Page[domain.RFQType], error) {
	out := []domain.RFQType{}
	for _, t := range f.rfqTypes {
		out = append(out, t)
	}
	return store.Page[domain.RFQType]{Items: out}, nil
}

func (f *fakeStore) UpdateRFQType(_ context.Context, id string, _ int, in domain.RFQType) (domain.RFQType, error) {
	in.ID, in.Version = id, 2
	f.typeSaved = &in
	return in, nil
}

func (f *fakeStore) EditRFQ(_ context.Context, id string, ifMatch int, in domain.RFQEdit, now time.Time) (domain.RFQ, error) {
	cur, err := f.checkVersion(id, ifMatch)
	if err != nil {
		return cur, err
	}
	if err := domain.CheckEdit(cur, in, now); err != nil {
		return cur, stateConflictOf(err)
	}
	f.edited = &in
	cur.Version++
	return cur, nil
}

func (f *fakeStore) AmendRFQ(_ context.Context, id string, ifMatch int, in domain.AmendmentInput, now time.Time, _ store.Idem) (domain.RFQ, error) {
	cur, err := f.checkVersion(id, ifMatch)
	if err != nil {
		return cur, err
	}
	if err := domain.CheckAmendment(cur, in, now); err != nil {
		return cur, stateConflictOf(err)
	}
	f.amended = &in
	cur.Version++
	cur.AmendmentNo++
	return cur, nil
}

func (f *fakeStore) AcknowledgeAmendment(_ context.Context, id string, no int, vendorID string, _ time.Time) (domain.RFQ, error) {
	f.acked = vendorID
	return f.GetRFQ(context.Background(), id)
}

// stateConflictOf mirrors the store: a PRC-RFQ-STATE error is a 409, other rule errors pass through.
func stateConflictOf(err error) error {
	if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeRFQState {
		return httpx.NewProblem(http.StatusConflict, de.Code, de.Detail)
	}
	return err
}

const rfqTypesPath = "/v1/procurement/rfq-types"

func (h *harness) seedRFQType(t domain.RFQType) domain.RFQType {
	if h.fake.rfqTypes == nil {
		h.fake.rfqTypes = map[string]domain.RFQType{}
	}
	if t.ID == "" {
		t.ID = ids.New()
	}
	if t.LegalEntityID == "" {
		t.LegalEntityID = leA
	}
	if t.Status == "" {
		t.Status = domain.RFQTypeActive
	}
	h.fake.rfqTypes[t.ID] = t
	return t
}

func TestRFQType_CreateValidatesAndNeedsTheDuty(t *testing.T) {
	h := newHarness(t)
	good := `{"legalEntityId":"` + leA + `","code":" sealed ","name":" Sealed bid ","sealed":true,"rankingRule":"WEIGHTED_SCORE","criteria":[{"name":"Quality","weight":"2"}]}`
	maintain := []string{dutyPolicyMaintain}
	for _, tc := range []struct {
		name   string
		c      call
		status int
		code   string
	}{
		{"rfq duty cannot write", call{method: "POST", path: rfqTypesPath, user: userA, duties: rfqMaintainDuty, body: good, headers: idem("k")}, 403, "AUTH-0003"},
		{"no Idempotency-Key", call{method: "POST", path: rfqTypesPath, user: userA, duties: maintain, body: good}, 400, codeIdemRequired},
		{"weighted score needs criteria", call{method: "POST", path: rfqTypesPath, user: userA, duties: maintain, headers: idem("k"), body: strings.Replace(good, `,"criteria":[{"name":"Quality","weight":"2"}]`, "", 1)}, 422, codeInvalid},
		{"bad code", call{method: "POST", path: rfqTypesPath, user: userA, duties: maintain, headers: idem("k"), body: strings.Replace(good, " sealed ", "se aled", 1)}, 422, codeInvalid},
		{"legal entity outside scope", call{method: "POST", path: rfqTypesPath, user: userA, duties: maintain, headers: idem("k"), body: strings.Replace(good, leA, "00000000-0000-7000-8000-0000000001e2", 1)}, 403, codeScope},
	} {
		rec := h.do(tc.c)
		if rec.Code != tc.status || problemCode(t, rec) != tc.code {
			t.Errorf("%s: status %d code %s: %s", tc.name, rec.Code, problemCode(t, rec), rec.Body)
		}
	}
	rec := h.do(call{method: "POST", path: rfqTypesPath, user: userA, duties: maintain, body: good, headers: idem("ok")})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: status %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	got := h.fake.typeSaved
	if got == nil || got.Code != "SEALED" || got.Name != "Sealed bid" || !got.Sealed || !got.AmendmentsAllowed || len(got.Criteria) != 1 {
		t.Fatalf("stored type: %+v", got)
	}
}

func TestRFQType_ReadByRFQDutyAndUpdateKeepsCode(t *testing.T) {
	h := newHarness(t)
	made := h.seedRFQType(domain.RFQType{Code: "STD", Name: "Standard", AmendmentsAllowed: true, RankingRule: domain.RankLowestPrice})
	rec := h.do(call{method: "GET", path: rfqTypesPath + "/" + made.ID, user: userA, duties: rfqAwardDuty})
	if rec.Code != http.StatusOK {
		t.Fatalf("read with an RFQ duty: %d %s", rec.Code, rec.Body)
	}
	rec = h.do(call{method: "GET", path: rfqTypesPath, user: userA, duties: []string{dutyPurchaseOrderMaintain}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("list without an RFQ or policy duty: %d", rec.Code)
	}
	body := `{"code":"CHANGED","name":"Standard bid","amendmentsAllowed":false}`
	rec = h.do(call{method: "PUT", path: rfqTypesPath + "/" + made.ID, user: userA, duties: []string{dutyPolicyMaintain}, body: body, headers: map[string]string{"If-Match": `"1"`}})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if got := h.fake.typeSaved; got.Code != "STD" || got.AmendmentsAllowed || got.Name != "Standard bid" {
		t.Fatalf("update kept the code and set the flag: %+v", got)
	}
}

func TestRFQ_CreateTakesSealedAndCriteriaFromItsType(t *testing.T) {
	h := newHarness(t)
	sealed := h.seedRFQType(domain.RFQType{Code: "SEALED", Name: "Sealed", Sealed: true, AmendmentsAllowed: false, RankingRule: domain.RankWeightedScore,
		Criteria: []domain.ScoringCriterion{{Name: "Quality", Weight: decimal.NewFromInt(3)}}})
	inactive := h.seedRFQType(domain.RFQType{Code: "OLD", Name: "Old", Status: domain.RFQTypeInactive})
	other := h.seedRFQType(domain.RFQType{Code: "OTH", Name: "Other", LegalEntityID: "00000000-0000-7000-8000-0000000001e2"})
	line := `"lines":[{"description":"Bolts","qty":"10","unit":"EA"}]`
	create := func(typeID string) *httptest422 {
		rec := h.do(call{method: "POST", path: "/v1/procurement/rfqs", duties: rfqMaintainDuty, headers: idem("k-" + typeID),
			body: `{"legalEntityId":"` + leA + `","replyDeadline":"2026-03-05T17:00:00Z","rfqTypeId":"` + typeID + `",` + line + `}`})
		out := &httptest422{code: rec.Code, body: rec.Body.String()}
		if rec.Code >= 400 {
			out.problem = problemCode(t, rec)
		}
		return out
	}
	if r := create(sealed.ID); r.code != http.StatusCreated {
		t.Fatalf("create from a type: %d %s", r.code, r.body)
	}
	in := h.fake.rfqLog.created
	if in == nil || !in.Sealed || !in.SealedRequired || in.AmendmentsAllowed || in.RFQTypeID != sealed.ID ||
		in.RankingRule != domain.RankWeightedScore || len(in.Criteria) != 1 || in.Criteria[0].Name != "Quality" {
		t.Fatalf("RFQ built from the type: %+v", in)
	}
	for name, id := range map[string]string{"inactive type": inactive.ID, "type of another legal entity": other.ID} {
		if r := create(id); r.code != http.StatusUnprocessableEntity || r.problem != domain.CodeRFQTypeInvalid {
			t.Errorf("%s: %d %s %s", name, r.code, r.problem, r.body)
		}
	}
	if r := create(ids.New()); r.code != http.StatusNotFound {
		t.Errorf("unknown type: %d %s", r.code, r.body)
	}
}

type httptest422 struct {
	code    int
	problem string
	body    string
}

func draftRFQ(h *harness) {
	h.fake.rfq = rfqFixture()
	h.fake.rfq.Status = domain.RFQDraft
	h.fake.rfq.Replies = nil
	h.fake.rfq.Invitations = nil
}

func TestRFQ_EditIsDraftOnly(t *testing.T) {
	h := newHarness(t)
	draftRFQ(h)
	hdr := map[string]string{"If-Match": `"3"`}
	body := `{"replyDeadline":"2026-04-01T17:00:00Z","sealed":true,"rankingRule":"WEIGHTED_SCORE","criteria":[{"name":"Price","weight":"1"},{"name":"Quality","weight":"2"}]}`
	if rec := h.do(call{method: "PUT", path: rfqPath(""), duties: rfqAwardDuty, headers: hdr, body: body}); rec.Code != http.StatusForbidden {
		t.Fatalf("award duty cannot edit: %d", rec.Code)
	}
	if rec := h.do(call{method: "PUT", path: rfqPath(""), duties: rfqMaintainDuty, headers: hdr, body: strings.Replace(body, `,"criteria"`, `,"x":1,"criteria"`, 1)}); rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec := h.do(call{method: "PUT", path: rfqPath(""), duties: rfqMaintainDuty, headers: hdr, body: `{"replyDeadline":"2026-03-01T14:00:00Z"}`}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("deadline in the past: %d %s", rec.Code, rec.Body)
	}
	rec := h.do(call{method: "PUT", path: rfqPath(""), duties: rfqMaintainDuty, headers: hdr, body: body})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	if e := h.fake.edited; e == nil || !e.Sealed || len(e.Criteria) != 2 || e.RankingRule != domain.RankWeightedScore {
		t.Fatalf("edit reached the store as %+v", e)
	}
	h.fake.rfq.Status = domain.RFQSent
	h.fake.rfq.Version = 3
	rec = h.do(call{method: "PUT", path: rfqPath(""), duties: rfqMaintainDuty, headers: hdr, body: body})
	if rec.Code != http.StatusConflict || problemCode(t, rec) != domain.CodeRFQState {
		t.Fatalf("a sent RFQ is not edited: %d %s", rec.Code, rec.Body)
	}
	h.fake.rfq.Status, h.fake.rfq.SealedRequired = domain.RFQDraft, true
	rec = h.do(call{method: "PUT", path: rfqPath(""), duties: rfqMaintainDuty, headers: hdr, body: strings.Replace(body, `"sealed":true`, `"sealed":false`, 1)})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeRFQEdit {
		t.Fatalf("a type that requires sealing keeps it: %d %s", rec.Code, rec.Body)
	}
}

func TestRFQ_AmendRulesAndAcknowledgement(t *testing.T) {
	h := newHarness(t)
	h.fake.rfq = rfqFixture()
	h.fake.rfq.AmendmentsAllowed = true
	hdr := map[string]string{"If-Match": `"3"`, "Idempotency-Key": "am-1"}
	path := rfqPath("/amendments")
	body := `{"summary":"Delivery moves to April","replyDeadline":"2026-03-12T17:00:00Z","criteria":[{"name":"Delivery","weight":"1"}]}`
	if rec := h.do(call{method: "POST", path: path, duties: rfqAwardDuty, headers: hdr, body: body}); rec.Code != http.StatusForbidden {
		t.Fatalf("award duty cannot amend: %d", rec.Code)
	}
	if rec := h.do(call{method: "POST", path: path, duties: rfqMaintainDuty, headers: map[string]string{"If-Match": `"3"`}, body: body}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no Idempotency-Key: %d", rec.Code)
	}
	if rec := h.do(call{method: "POST", path: path, duties: rfqMaintainDuty, headers: hdr, body: `{"summary":" "}`}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("summary is required: %d", rec.Code)
	}
	// The fixture is not sealed and has replies the buyer can read: re-weighting the criteria now could steer
	// the award, so it is refused.
	rec := h.do(call{method: "POST", path: path, duties: rfqMaintainDuty, headers: hdr, body: body})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeRFQAmend || h.fake.amended != nil {
		t.Fatalf("criteria replaced on an unsealed RFQ with replies: %d %s", rec.Code, rec.Body)
	}
	// A sealed RFQ before its deadline may replace them, and the answer keeps the replies hidden.
	h.fake.rfq.Sealed = true
	rec = h.do(call{method: "POST", path: path, duties: rfqMaintainDuty, headers: hdr, body: body})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("amend: %d %s", rec.Code, rec.Body)
	}
	var out domain.RFQ
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.AmendmentNo != 1 {
		t.Fatalf("amendment number: %v %+v", err, out)
	}
	if !out.RepliesSealed || len(out.Replies) != 0 || out.ReplyCount != 4 || strings.Contains(rec.Body.String(), "9.50") {
		t.Fatalf("an amendment of a sealed RFQ revealed its replies: %s", rec.Body)
	}
	if in := h.fake.amended; in == nil || !in.ReplaceCriteria || len(in.Criteria) != 1 || in.ReplyDeadline == nil {
		t.Fatalf("amendment reached the store as %+v", in)
	}
	// An earlier deadline is refused, a type without amendments too, a closed RFQ with 409.
	h.fake.rfq = rfqFixture()
	h.fake.rfq.AmendmentsAllowed = true
	rec = h.do(call{method: "POST", path: path, duties: rfqMaintainDuty, headers: hdr, body: `{"summary":"x","replyDeadline":"2026-03-04T17:00:00Z"}`})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeRFQAmend {
		t.Fatalf("earlier deadline: %d %s", rec.Code, rec.Body)
	}
	h.fake.rfq.AmendmentsAllowed = false
	rec = h.do(call{method: "POST", path: path, duties: rfqMaintainDuty, headers: hdr, body: body})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeRFQAmend {
		t.Fatalf("type without amendments: %d %s", rec.Code, rec.Body)
	}
	h.fake.rfq.AmendmentsAllowed, h.fake.rfq.Status = true, domain.RFQClosed
	rec = h.do(call{method: "POST", path: path, duties: rfqMaintainDuty, headers: hdr, body: body})
	if rec.Code != http.StatusConflict || problemCode(t, rec) != domain.CodeRFQState {
		t.Fatalf("closed RFQ: %d %s", rec.Code, rec.Body)
	}
	ack := rfqPath("/amendments/1/acknowledgements/" + rfqVendorA)
	if rec := h.do(call{method: "PUT", path: ack, duties: rfqAwardDuty}); rec.Code != http.StatusForbidden {
		t.Fatalf("award duty cannot acknowledge: %d", rec.Code)
	}
	if rec := h.do(call{method: "PUT", path: ack, duties: rfqMaintainDuty}); rec.Code != http.StatusOK || h.fake.acked != rfqVendorA {
		t.Fatalf("acknowledge: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "PUT", path: rfqPath("/amendments/0/acknowledgements/" + rfqVendorA), duties: rfqMaintainDuty}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("amendment 0: %d", rec.Code)
	}
	h.fake.acked = ""
	if rec := h.do(call{method: "PUT", path: rfqPath("/amendments/1/acknowledgements/not-a-vendor"), duties: rfqMaintainDuty}); rec.Code != http.StatusUnprocessableEntity || h.fake.acked != "" {
		t.Fatalf("malformed vendor id: %d", rec.Code)
	}
}
