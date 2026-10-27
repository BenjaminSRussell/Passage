package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/services/transport/internal/api"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/optimizer"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

const planID = "0190f0a0-0000-7000-8000-0000000000f2"

func (f *fakeStore) FindRoutePlan(_ context.Context, hash string) (domain.RoutePlan, bool, error) {
	p, ok := f.plans[hash]
	return p, ok, f.findPlanErr
}

func (f *fakeStore) SaveRoutePlan(_ context.Context, plan domain.RoutePlan) (domain.RoutePlan, bool, error) {
	if f.plans == nil {
		f.plans = map[string]domain.RoutePlan{}
	}
	f.savedPlans++
	plan.ID = planID
	f.plans[plan.RequestHash] = plan
	return plan, true, nil
}

func (f *fakeStore) GetRoutePlan(_ context.Context, id string) (domain.RoutePlan, error) {
	for _, p := range f.plans {
		if p.ID == id {
			return p, nil
		}
	}
	return domain.RoutePlan{}, store.NotFound("route plan")
}

func (f *fakeStore) ListRoutePlans(_ context.Context) ([]domain.RoutePlanSummary, error) {
	out := []domain.RoutePlanSummary{}
	for _, p := range f.plans {
		out = append(out, p.RoutePlanSummary)
	}
	return out, nil
}

// fakeOptimizer answers with a fixed solution or error and records the calls.
type fakeOptimizer struct {
	sol      domain.RouteSolution
	plan     domain.LoadPlan
	err      error
	calls    int
	got      domain.RouteRequest
	deadline time.Duration
}

func (f *fakeOptimizer) Solve(ctx context.Context, req domain.RouteRequest) (domain.RouteSolution, error) {
	f.calls++
	f.got = req
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	return f.sol, f.err
}

func (f *fakeOptimizer) Stack(_ context.Context, _ domain.LoadPlanRequest) (domain.LoadPlan, error) {
	f.calls++
	return f.plan, f.err
}

func optimizerRouter(st store.Store, o api.Optimizer) http.Handler {
	r := chi.NewRouter()
	a := api.New(st)
	if o != nil {
		a.WithOptimizer(o)
	}
	a.Register(r)
	return r
}

const routeBody = `{"depot":{"x":0,"y":0},
	"stops":[{"id":" S1 ","point":{"x":5,"y":0},"earliest":0,"latest":480,"serviceMin":10,"weightKg":100,"volumeL":0,"pallets":1},
	         {"id":"S2","point":{"x":0,"y":5},"earliest":0,"latest":480,"serviceMin":10,"weightKg":100,"volumeL":0,"pallets":1}],
	"vehicles":[{"id":"V1","capacityKg":1000,"capacityL":5000,"pallets":10}],
	"timeLimitS":10}`

func twoStopSolution(dropped ...string) domain.RouteSolution {
	route := domain.RouteSolutionRoute{VehicleID: "V1", DistanceKm: 14.2, EndMin: 40}
	for _, id := range []string{"S1", "S2"} {
		if len(dropped) == 0 || dropped[0] != id {
			route.Stops = append(route.Stops, domain.RouteSolutionStop{StopID: id, ArrivalMin: 5})
		}
	}
	return domain.RouteSolution{Routes: []domain.RouteSolutionRoute{route}, Dropped: append([]string{}, dropped...)}
}

type planBody struct {
	ID          string               `json:"id"`
	RequestHash string               `json:"requestHash"`
	Replayed    bool                 `json:"replayed"`
	RouteCount  int                  `json:"routeCount"`
	Solution    domain.RouteSolution `json:"solution"`
	Request     domain.RouteRequest  `json:"request"`
	Warnings    []struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	} `json:"warnings"`
}

func decodePlan(t *testing.T, body []byte) planBody {
	t.Helper()
	var p planBody
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode plan: %v (%s)", err, body)
	}
	return p
}

// The request hash is the idempotency key (§6.34.5): the first call solves and
// stores the plan (201), the same request again returns it (200) without solving.
func TestOptimizeRoutes_SolvesOnceAndReplaysByRequestHash(t *testing.T) {
	st := &fakeStore{}
	opt := &fakeOptimizer{sol: twoStopSolution()}
	h := optimizerRouter(st, opt)
	rec := do(t, h, http.MethodPost, base+"/routes:optimize", routeBody, api.DutyPlan)
	if rec.Code != http.StatusCreated {
		t.Fatalf("optimize = %d %s", rec.Code, rec.Body.String())
	}
	first := decodePlan(t, rec.Body.Bytes())
	if first.ID != planID || len(first.RequestHash) != 64 || first.Replayed || first.RouteCount != 1 || len(first.Warnings) != 0 {
		t.Fatalf("plan = %+v", first)
	}
	if opt.got.Stops[0].ID != "S1" || *opt.got.LatenessPerMin != domain.DefaultLatenessPerMin || opt.got.Vehicles[0].MaxRouteMin != domain.DefaultMaxRouteMin {
		t.Fatalf("solver got an unnormalized request: %+v", opt.got)
	}
	if opt.deadline <= 10*time.Second || opt.deadline > 15*time.Second {
		t.Fatalf("solver deadline = %v, want the 10 s limit plus overhead", opt.deadline)
	}
	rec = do(t, h, http.MethodPost, base+"/routes:optimize", routeBody, api.DutyPlan)
	again := decodePlan(t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || !again.Replayed || again.ID != first.ID || opt.calls != 1 || st.savedPlans != 1 {
		t.Fatalf("replay = %d replayed %v, solver calls %d, saves %d", rec.Code, again.Replayed, opt.calls, st.savedPlans)
	}
	rec = do(t, h, http.MethodGet, base+"/routes/"+planID, "", api.DutyPlan)
	if rec.Code != http.StatusOK || decodePlan(t, rec.Body.Bytes()).Request.Stops[1].ID != "S2" {
		t.Fatalf("get plan = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, base+"/routes", "", api.DutyPlan); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), first.RequestHash) {
		t.Fatalf("list plans = %d %s", rec.Code, rec.Body.String())
	}
}

func TestOptimizeRoutes_DroppedStopsWarnTMS0104(t *testing.T) {
	h := optimizerRouter(&fakeStore{}, &fakeOptimizer{sol: twoStopSolution("S2")})
	rec := do(t, h, http.MethodPost, base+"/routes:optimize", routeBody, api.DutyPlan)
	p := decodePlan(t, rec.Body.Bytes())
	if rec.Code != http.StatusCreated || len(p.Warnings) != 1 || p.Warnings[0].Code != domain.CodeStopsDropped ||
		p.Warnings[0].Detail != "1 stops dropped as infeasible; see route report." || p.Solution.Dropped[0] != "S2" {
		t.Fatalf("dropped = %d %+v", rec.Code, p)
	}
}

// A failing solver, a reply that loses a stop and a missing optimizer are 503
// TMS-0123; nothing is stored.
func TestOptimizeRoutes_FailuresAreNeverAnEmptyPlan(t *testing.T) {
	lost := twoStopSolution()
	lost.Routes[0].Stops = lost.Routes[0].Stops[:1]
	cases := map[string]api.Optimizer{
		"not configured": nil,
		"unreachable":    &fakeOptimizer{err: optimizer.ErrUnavailable},
		"rejected":       &fakeOptimizer{err: optimizer.ErrRejected},
		"stop lost":      &fakeOptimizer{sol: lost},
		"empty reply":    &fakeOptimizer{sol: domain.RouteSolution{}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			st := &fakeStore{}
			rec := do(t, optimizerRouter(st, o), http.MethodPost, base+"/routes:optimize", routeBody, api.DutyPlan)
			if rec.Code != http.StatusServiceUnavailable || problem(t, rec).Code != domain.CodeOptimizerUnavailable || st.savedPlans != 0 {
				t.Fatalf("status %d %s, saves %d", rec.Code, rec.Body.String(), st.savedPlans)
			}
		})
	}
}

func TestOptimizeRoutes_ValidationAndStoreErrors(t *testing.T) {
	opt := &fakeOptimizer{sol: twoStopSolution()}
	dup := strings.Replace(routeBody, `"id":"S2"`, `"id":"S1"`, 1)
	rec := do(t, optimizerRouter(&fakeStore{}, opt), http.MethodPost, base+"/routes:optimize", dup, api.DutyPlan)
	if rec.Code != http.StatusBadRequest || problem(t, rec).Errors["stops[1].id"] == "" {
		t.Fatalf("duplicate stop = %d %s", rec.Code, rec.Body.String())
	}
	long := strings.Replace(routeBody, `"timeLimitS":10`, `"timeLimitS":60`, 1)
	rec = do(t, optimizerRouter(&fakeStore{}, opt), http.MethodPost, base+"/routes:optimize", long, api.DutyPlan)
	if rec.Code != http.StatusBadRequest || problem(t, rec).Errors["timeLimitS"] == "" {
		t.Fatalf("60 s limit = %d %s", rec.Code, rec.Body.String())
	}
	if opt.calls != 0 {
		t.Fatalf("solver called %d times for invalid requests", opt.calls)
	}
	st := &fakeStore{findPlanErr: errors.New("db down")}
	rec = do(t, optimizerRouter(st, opt), http.MethodPost, base+"/routes:optimize", routeBody, api.DutyPlan)
	if rec.Code != http.StatusInternalServerError || opt.calls != 0 {
		t.Fatalf("store failure = %d, solver calls %d", rec.Code, opt.calls)
	}
	if rec := do(t, optimizerRouter(&fakeStore{}, opt), http.MethodGet, base+"/routes/"+planID, "", api.DutyPlan); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown plan = %d", rec.Code)
	}
}

const stackBody = `{"trailer":{"lengthMm":13600,"widthMm":2480,"maxHeightMm":2700,"maxWeightKg":20000},
	"pallets":[{"id":"P1","lengthMm":1200,"widthMm":1000,"heightMm":900,"weightKg":500,"stackable":true,"stopSeq":2},
	           {"id":"P2","lengthMm":1200,"widthMm":1000,"heightMm":900,"weightKg":500,"stackable":false,"stopSeq":1}]}`

func TestStackLoadPlan(t *testing.T) {
	good := domain.LoadPlan{Placements: []domain.Placement{{PalletIDs: []string{"P1", "P2"}}}, FloorPositionsUsed: 1,
		Unplaced: []string{}, LoadSequence: []string{"P1", "P2"}}
	opt := &fakeOptimizer{plan: good}
	rec := do(t, optimizerRouter(&fakeStore{}, opt), http.MethodPost, base+"/load-plans:stack", stackBody, api.DutyPlan)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"floorPositionsUsed":1`) {
		t.Fatalf("stack = %d %s", rec.Code, rec.Body.String())
	}
	// AC-34.18: a reply that stacks on the non-stackable pallet is refused.
	bad := &fakeOptimizer{plan: domain.LoadPlan{Placements: []domain.Placement{{PalletIDs: []string{"P2", "P1"}}}, FloorPositionsUsed: 1}}
	rec = do(t, optimizerRouter(&fakeStore{}, bad), http.MethodPost, base+"/load-plans:stack", stackBody, api.DutyPlan)
	if rec.Code != http.StatusServiceUnavailable || problem(t, rec).Code != domain.CodeOptimizerUnavailable {
		t.Fatalf("bad stack = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, optimizerRouter(&fakeStore{}, opt), http.MethodPost, base+"/load-plans:stack", `{"trailer":{},"pallets":[]}`, api.DutyPlan)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty request = %d", rec.Code)
	}
	if rec := do(t, optimizerRouter(&fakeStore{}, nil), http.MethodPost, base+"/load-plans:stack", stackBody, api.DutyPlan); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no optimizer = %d", rec.Code)
	}
}

// The rate shop is cached per tenant and input for the window (FR-34.4); a new
// rate card is used at once, and a zero window turns the cache off.
func TestRateShipment_CacheHitsAndInvalidation(t *testing.T) {
	st := &fakeStore{cards: []domain.RateCard{card(carrierID, "1.00")}}
	now := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	a := api.New(st).WithClock(func() time.Time { return now })
	r := chi.NewRouter()
	a.Register(r)
	path := base + "/shipments/" + loadID + ":rate"
	if rec := do(t, r, http.MethodPost, path, rateBody, api.DutyRate); rec.Header().Get("X-Passage-Cache") != "MISS" {
		t.Fatalf("first rate shop cache = %q", rec.Header().Get("X-Passage-Cache"))
	}
	st.cards = nil // a hit must not read the cards again
	rec := do(t, r, http.MethodPost, path, rateBody, api.DutyRate)
	if rec.Header().Get("X-Passage-Cache") != "HIT" || !strings.Contains(rec.Body.String(), `"chosen"`) {
		t.Fatalf("second rate shop = %q %s", rec.Header().Get("X-Passage-Cache"), rec.Body.String())
	}
	now = now.Add(10 * time.Minute)
	if rec := do(t, r, http.MethodPost, path, rateBody, api.DutyRate); rec.Header().Get("X-Passage-Cache") != "MISS" || strings.Contains(rec.Body.String(), `"chosen"`) {
		t.Fatalf("after the window = %q %s", rec.Header().Get("X-Passage-Cache"), rec.Body.String())
	}
	cardBody := `{"carrierId":"` + carrierID + `","serviceId":"` + serviceID + `","basis":"LANE","currency":"USD","validFrom":"2026-01-01",
		"minimumCharge":"0","fuelPct":"0","rows":[{"origin":"*","destination":"*","breakFrom":"0","rate":"2.00","unit":"KG"}],"accessorials":[]}`
	if rec := do(t, r, http.MethodPost, base+"/rate-cards", cardBody, api.DutyRateApprove); rec.Code != http.StatusCreated {
		t.Fatalf("create card = %d %s", rec.Code, rec.Body.String())
	}
	st.cards = []domain.RateCard{card(carrierID, "2.00")}
	if rec := do(t, r, http.MethodPost, path, rateBody, api.DutyRate); rec.Header().Get("X-Passage-Cache") != "MISS" || !strings.Contains(rec.Body.String(), `"total":"200"`) {
		t.Fatalf("after a new card = %q %s", rec.Header().Get("X-Passage-Cache"), rec.Body.String())
	}
	off := chi.NewRouter()
	api.New(st).WithRatingCacheMinutes(0).Register(off)
	for range 2 {
		if rec := do(t, off, http.MethodPost, path, rateBody, api.DutyRate); rec.Header().Get("X-Passage-Cache") != "MISS" {
			t.Fatal("a zero window must never serve from the cache")
		}
	}
}
