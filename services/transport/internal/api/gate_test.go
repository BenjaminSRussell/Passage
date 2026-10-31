package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/transport/internal/api"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

const (
	visitID       = "0190f0a0-0000-7000-8000-0000000000f1"
	appointmentID = "0190f0a0-0000-7000-8000-0000000000f2"
	gitVoyageID   = "0190f0a0-0000-7000-8000-0000000000f3"
	gitID         = "0190f0a0-0000-7000-8000-0000000000f4"
)

type fakeCheckOut struct {
	id, seal string
	at       time.Time
	rate     decimal.Decimal
	idem     store.Idem
}

type fakeGITCreate struct {
	voyageID, incoterm, siteID string
	lines                      []int
	idem                       store.Idem
}

func (f *fakeStore) CheckIn(_ context.Context, in domain.GateVisit, idem store.Idem) (domain.GateVisit, error) {
	f.checkedIn, f.checkInIdem = in, idem
	if f.gateErr != nil {
		return domain.GateVisit{}, f.gateErr
	}
	in.ID, in.Version = visitID, 1
	return in, nil
}

func (f *fakeStore) RecordSeal(_ context.Context, id, seal string, _ time.Time) (domain.GateVisit, error) {
	f.sealed = seal
	v := f.visit
	v.SealNo = seal
	return v, f.gateErr
}

func (f *fakeStore) CheckOut(_ context.Context, id, seal string, at time.Time, rate decimal.Decimal, idem store.Idem) (domain.GateVisit, error) {
	f.checkOutCall = fakeCheckOut{id: id, seal: seal, at: at, rate: rate, idem: idem}
	if f.gateErr != nil {
		return domain.GateVisit{}, f.gateErr
	}
	v, err := f.visit.CheckOut(seal, at, rate)
	return v, asProblem(err)
}

// asProblem wraps a domain rule rejection as the real store does.
func asProblem(err error) error {
	var re *domain.Error
	if errors.As(err, &re) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, re.Code, re.Detail)
	}
	return err
}

func (f *fakeStore) GetGateVisit(_ context.Context, _ string) (domain.GateVisit, []domain.GateLogEntry, error) {
	return f.visit, f.gateLog, f.gateErr
}

func (f *fakeStore) ListGateVisits(_ context.Context, filter store.GateVisitFilter) ([]domain.GateVisit, error) {
	f.visitFilter = filter
	return []domain.GateVisit{f.visit}, nil
}

func (f *fakeStore) CreateGoodsInTransit(_ context.Context, voyage, incoterm, site, legalEntity string, lines []int, idem store.Idem) (domain.GoodsInTransit, error) {
	f.gitCall = fakeGITCreate{voyageID: voyage, incoterm: incoterm, siteID: site, lines: lines, idem: idem}
	return f.git, f.gitErr
}

func (f *fakeStore) GetGoodsInTransit(_ context.Context, _ string) (domain.GoodsInTransit, error) {
	return f.git, f.gitErr
}

func (f *fakeStore) ListGoodsInTransit(_ context.Context, filter store.GoodsInTransitFilter) ([]domain.GoodsInTransit, error) {
	f.gitFilter = filter
	return []domain.GoodsInTransit{f.git}, nil
}

func (f *fakeStore) ShipGoodsInTransit(_ context.Context, _ string, at time.Time) (domain.GoodsInTransit, error) {
	f.shippedAt = at
	if f.gitErr != nil {
		return domain.GoodsInTransit{}, f.gitErr
	}
	g, err := f.git.Ship(at)
	return g, asProblem(err)
}

var gateNow = time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)

func clockedRouter(st store.Store) http.Handler {
	r := chi.NewRouter()
	api.New(st).WithClock(func() time.Time { return gateNow }).Register(r)
	return r
}

const checkInBody = `{"siteId":"` + siteID + `","direction":"outbound","carrierScac":"abcd","trailerNo":" tr-1001 ",
	"driverName":"Dana Driver","driverLicenseId":"D1234567","appointmentId":"` + appointmentID + `",
	"scheduledAt":"2026-03-02T09:00:00Z","arrivedAt":"2026-03-02T10:35:00Z"}`

// A late arrival is classified LATE with TMS-0127 against the default 60-minute
// tolerance, and the visit opens with the default 2 h free time (BR-34.14).
func TestCheckIn_ClassifiesAndNormalizes(t *testing.T) {
	st := &fakeStore{}
	rec := doWith(t, clockedRouter(st), http.MethodPost, base+"/gate/check-in", checkInBody, map[string]string{"Idempotency-Key": "gate-1"}, api.DutyGate)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got domain.GateVisit
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	in := st.checkedIn
	if in.Direction != domain.DirectionOutbound || in.CarrierSCAC != "ABCD" || in.TrailerNo != "TR-1001" || in.FreeTimeMin != 120 {
		t.Fatalf("not normalised: %+v", in)
	}
	if got.ID != visitID || got.Arrival == nil || got.Arrival.Status != domain.ArrivalLate || got.Arrival.DeltaMin != 95 ||
		got.Arrival.Code != domain.CodeArrivalOutsideWindow || got.WalkIn {
		t.Fatalf("visit = %+v arrival %+v", got, got.Arrival)
	}
	if st.checkInIdem.Key != "gate-1" || st.checkInIdem.Fingerprint == "" {
		t.Fatalf("idempotency not passed: %+v", st.checkInIdem)
	}
}

func TestCheckIn_WalkInDefaultsToNow(t *testing.T) {
	st := &fakeStore{}
	body := `{"siteId":"` + siteID + `","direction":"INBOUND","carrierScac":"XPOL","trailerNo":"T9","sealNo":"555","driverName":"A","driverLicenseId":"L1"}`
	rec := do(t, clockedRouter(st), http.MethodPost, base+"/gate/check-in", body, api.DutyGate)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !st.checkedIn.WalkIn || st.checkedIn.Arrival != nil || !st.checkedIn.CheckedInAt.Equal(gateNow) || st.checkedIn.SealNo != "555" {
		t.Fatalf("walk-in = %+v", st.checkedIn)
	}
}

func TestCheckIn_Rejections(t *testing.T) {
	cases := []struct {
		name, body, field string
	}{
		{"site not a uuid", `{"siteId":"site-1","direction":"INBOUND","carrierScac":"ABCD","trailerNo":"T","driverName":"A","driverLicenseId":"L"}`, "siteId"},
		{"no direction", `{"siteId":"` + siteID + `","carrierScac":"ABCD","trailerNo":"T","driverName":"A","driverLicenseId":"L"}`, "direction"},
		{"bad SCAC", `{"siteId":"` + siteID + `","direction":"INBOUND","carrierScac":"A1","trailerNo":"T","driverName":"A","driverLicenseId":"L"}`, "carrierScac"},
		{"no driver", `{"siteId":"` + siteID + `","direction":"INBOUND","carrierScac":"ABCD","trailerNo":"T","driverLicenseId":"L"}`, "driverName"},
		{"appointment without time", `{"siteId":"` + siteID + `","direction":"INBOUND","carrierScac":"ABCD","trailerNo":"T","driverName":"A","driverLicenseId":"L","appointmentId":"` + appointmentID + `"}`, "appointmentId"},
		{"arrival in the future", `{"siteId":"` + siteID + `","direction":"INBOUND","carrierScac":"ABCD","trailerNo":"T","driverName":"A","driverLicenseId":"L","arrivedAt":"2026-03-02T13:00:00Z"}`, "arrivedAt"},
		{"bad timestamp", `{"siteId":"` + siteID + `","direction":"INBOUND","carrierScac":"ABCD","trailerNo":"T","driverName":"A","driverLicenseId":"L","arrivedAt":"yesterday"}`, "arrivedAt"},
		{"free time out of range", `{"siteId":"` + siteID + `","direction":"INBOUND","carrierScac":"ABCD","trailerNo":"T","driverName":"A","driverLicenseId":"L","freeTimeMin":2000}`, "freeTimeMin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &fakeStore{}
			rec := do(t, clockedRouter(st), http.MethodPost, base+"/gate/check-in", c.body, api.DutyGate)
			p := problem(t, rec)
			if rec.Code != http.StatusBadRequest || p.Code != "COM-0001" || p.Errors[c.field] == "" {
				t.Fatalf("status = %d errors %v, want 400 on %s", rec.Code, p.Errors, c.field)
			}
			if st.checkedIn.TrailerNo != "" {
				t.Fatal("store called for an invalid check-in")
			}
		})
	}
}

func TestCheckIn_TrailerAlreadyInYardPassesThrough(t *testing.T) {
	st := &fakeStore{gateErr: httpx.NewProblem(http.StatusConflict, domain.CodeVisitState, "trailer is already in the yard")}
	rec := do(t, clockedRouter(st), http.MethodPost, base+"/gate/check-in", checkInBody, api.DutyGate)
	if rec.Code != http.StatusConflict || problem(t, rec).Code != domain.CodeVisitState {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func inYard() domain.GateVisit {
	return domain.GateVisit{ID: visitID, SiteID: siteID, Direction: domain.DirectionOutbound, CarrierSCAC: "ABCD", TrailerNo: "TR-1001",
		SealNo: "778812", CheckedInAt: time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC), FreeTimeMin: 120, Status: domain.VisitInYard}
}

// AC-34.11 and AC-34.12 through the API: the matching seal checks out with
// 2 h detention; the wrong seal is 422 TMS-0129.
func TestCheckOut_DetentionAndSeal(t *testing.T) {
	st := &fakeStore{visit: inYard()}
	h := clockedRouter(st)
	body := `{"visitId":"` + visitID + `","sealNo":" 778812 ","checkOutAt":"2026-03-02T11:20:00Z","hourlyRate":"85.00"}`
	rec := doWith(t, h, http.MethodPost, base+"/gate/check-out", body, map[string]string{"Idempotency-Key": "out-1"}, api.DutyGate)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got domain.GateVisit
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.VisitCheckedOut || got.Detention == nil || got.Detention.Hours != 2 || !got.Detention.Charge.Equal(dec("170")) {
		t.Fatalf("visit = %+v detention %+v", got, got.Detention)
	}
	if st.checkOutCall.seal != "778812" || st.checkOutCall.idem.Key != "out-1" || !st.checkOutCall.rate.Equal(dec("85")) {
		t.Fatalf("store call = %+v", st.checkOutCall)
	}

	wrong := `{"visitId":"` + visitID + `","sealNo":"778813","checkOutAt":"2026-03-02T11:20:00Z"}`
	rec = do(t, h, http.MethodPost, base+"/gate/check-out", wrong, api.DutyGate)
	if rec.Code != http.StatusUnprocessableEntity || problem(t, rec).Code != domain.CodeSealMismatch {
		t.Fatalf("wrong seal status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestCheckOut_DefaultsAndRejections(t *testing.T) {
	st := &fakeStore{visit: inYard()}
	h := clockedRouter(st)
	rec := do(t, h, http.MethodPost, base+"/gate/check-out", `{"visitId":"`+visitID+`","sealNo":"778812"}`, api.DutyGate)
	if rec.Code != http.StatusOK || !st.checkOutCall.at.Equal(gateNow) || !st.checkOutCall.rate.IsZero() {
		t.Fatalf("defaults: status %d call %+v", rec.Code, st.checkOutCall)
	}
	for name, body := range map[string]string{
		"visitId":    `{"visitId":"v1","sealNo":"1"}`,
		"checkOutAt": `{"visitId":"` + visitID + `","checkOutAt":"2026-03-03T00:00:00Z"}`,
		"hourlyRate": `{"visitId":"` + visitID + `","hourlyRate":"-1"}`,
	} {
		rec := do(t, h, http.MethodPost, base+"/gate/check-out", body, api.DutyGate)
		if p := problem(t, rec); rec.Code != http.StatusBadRequest || p.Errors[name] == "" {
			t.Fatalf("%s: status %d errors %v", name, rec.Code, p.Errors)
		}
	}
}

func TestRecordSealAndGateLog(t *testing.T) {
	st := &fakeStore{visit: inYard(), gateLog: []domain.GateLogEntry{{Kind: domain.GateLogCheckIn}, {Kind: domain.GateLogBlocked}}}
	h := clockedRouter(st)
	rec := do(t, h, http.MethodPost, base+"/gate/visits/"+visitID+":seal", `{"sealNo":" 900100 "}`, api.DutyGate)
	if rec.Code != http.StatusOK || st.sealed != "900100" {
		t.Fatalf("seal status = %d sealed %q", rec.Code, st.sealed)
	}
	if rec := do(t, h, http.MethodPost, base+"/gate/visits/"+visitID+":seal", `{"sealNo":""}`, api.DutyGate); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty seal = %d", rec.Code)
	}
	rec = do(t, h, http.MethodGet, base+"/gate/visits/"+visitID, "", api.DutyGate)
	var got struct {
		ID  string                `json:"id"`
		Log []domain.GateLogEntry `json:"log"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.ID != visitID || len(got.Log) != 2 || got.Log[1].Kind != domain.GateLogBlocked {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, base+"/gate/visits/nope", "", api.DutyGate); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed id = %d", rec.Code)
	}
}

func TestListGateVisits_Filters(t *testing.T) {
	st := &fakeStore{visit: inYard()}
	h := clockedRouter(st)
	rec := do(t, h, http.MethodGet, base+"/gate/visits?siteId="+siteID+"&status=IN_YARD&carrierScac=abcd&date=2026-03-02", "", api.DutyGate)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if st.visitFilter != (store.GateVisitFilter{SiteID: siteID, Status: "IN_YARD", CarrierSCAC: "ABCD", Date: "2026-03-02"}) {
		t.Fatalf("filter = %+v", st.visitFilter)
	}
	for _, q := range []string{"status=GONE", "date=03/02/2026", "siteId=x", "carrierScac=TOOLONG"} {
		if rec := do(t, h, http.MethodGet, base+"/gate/visits?"+q, "", api.DutyGate); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d", q, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodGet, base+"/gate/visits", "", api.DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("gate log with TMS_VIEW only = %d, want 403 (driver PII)", rec.Code)
	}
}

func pendingGIT() domain.GoodsInTransit {
	return domain.GoodsInTransit{ID: gitID, VoyageID: gitVoyageID, SiteID: siteID, Incoterm: "FOB", TransferEvent: domain.TransferOriginShipment,
		Currency: "USD", Status: domain.GITPending, EstimatedTotal: dec("1112.50")}
}

func TestCreateGoodsInTransit(t *testing.T) {
	st := &fakeStore{git: pendingGIT()}
	body := `{"voyageId":"` + gitVoyageID + `","incoterm":" fob ","siteId":"` + siteID + `","receiptLines":[3,1]}`
	rec := doWith(t, clockedRouter(st), http.MethodPost, base+"/goods-in-transit", body, map[string]string{"Idempotency-Key": "git-1"}, api.DutyLandedCost)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	c := st.gitCall
	if c.voyageID != gitVoyageID || c.incoterm != "FOB" || c.siteID != siteID || len(c.lines) != 2 || c.idem.Key != "git-1" {
		t.Fatalf("store call = %+v", c)
	}
	for field, b := range map[string]string{
		"incoterm":     `{"voyageId":"` + gitVoyageID + `","incoterm":"FOO","siteId":"` + siteID + `"}`,
		"voyageId":     `{"voyageId":"v","incoterm":"FOB","siteId":"` + siteID + `"}`,
		"siteId":       `{"voyageId":"` + gitVoyageID + `","incoterm":"FOB"}`,
		"receiptLines": `{"voyageId":"` + gitVoyageID + `","incoterm":"FOB","siteId":"` + siteID + `","receiptLines":[1,1]}`,
	} {
		rec := do(t, clockedRouter(&fakeStore{}), http.MethodPost, base+"/goods-in-transit", b, api.DutyLandedCost)
		if p := problem(t, rec); rec.Code != http.StatusBadRequest || p.Errors[field] == "" {
			t.Fatalf("%s: status %d errors %v", field, rec.Code, p.Errors)
		}
	}
	st = &fakeStore{gitErr: httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeNoGoodsInTransit, "DAP transfers at destination")}
	body = `{"voyageId":"` + gitVoyageID + `","incoterm":"DAP","siteId":"` + siteID + `"}`
	if rec := do(t, clockedRouter(st), http.MethodPost, base+"/goods-in-transit", body, api.DutyLandedCost); problem(t, rec).Code != domain.CodeNoGoodsInTransit {
		t.Fatalf("DAP = %d %s", rec.Code, rec.Body.String())
	}
}

func TestShipGoodsInTransit(t *testing.T) {
	st := &fakeStore{git: pendingGIT()}
	h := clockedRouter(st)
	rec := do(t, h, http.MethodPost, base+"/goods-in-transit/"+gitID+":ship", `{"shippedAt":"2026-03-01T22:30:00+08:00"}`, api.DutyLandedCost)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got domain.GoodsInTransit
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.GITInTransit || got.Code != domain.CodeOwnershipMoved || !st.shippedAt.Equal(time.Date(2026, 3, 1, 14, 30, 0, 0, time.UTC)) {
		t.Fatalf("shipped = %+v at %v", got, st.shippedAt)
	}
	if rec := do(t, h, http.MethodPost, base+"/goods-in-transit/"+gitID+":ship", `{}`, api.DutyLandedCost); rec.Code != http.StatusOK || !st.shippedAt.Equal(gateNow) {
		t.Fatalf("default time: %d at %v", rec.Code, st.shippedAt)
	}
	if rec := do(t, h, http.MethodPost, base+"/goods-in-transit/"+gitID+":ship", `{"shippedAt":"2026-03-05T00:00:00Z"}`, api.DutyLandedCost); rec.Code != http.StatusBadRequest {
		t.Fatalf("future ship = %d", rec.Code)
	}
}

func TestListAndGetGoodsInTransit(t *testing.T) {
	st := &fakeStore{git: pendingGIT()}
	h := clockedRouter(st)
	rec := do(t, h, http.MethodGet, base+"/goods-in-transit?voyageId="+gitVoyageID+"&status=PENDING", "", api.DutyLandedCost)
	if rec.Code != http.StatusOK || st.gitFilter.VoyageID != gitVoyageID || st.gitFilter.Status != domain.GITPending {
		t.Fatalf("list = %d filter %+v", rec.Code, st.gitFilter)
	}
	if rec := do(t, h, http.MethodGet, base+"/goods-in-transit?status=LOST", "", api.DutyLandedCost); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, base+"/goods-in-transit/"+gitID, "", api.DutyLandedCost); rec.Code != http.StatusOK {
		t.Fatalf("get = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, base+"/goods-in-transit/x", "", api.DutyLandedCost); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed id = %d", rec.Code)
	}
}
