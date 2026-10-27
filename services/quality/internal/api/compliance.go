package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/store"
)

// Duties and limits of slice 2 (FR-33.11, FR-33.13, FR-33.16).
const (
	dutyCompliance = "QM_COMPLIANCE_MAINTAIN"
	dutyStability  = "QM_STABILITY_MAINTAIN"
	// defaultSPCOrders and maxSPCOrders bound the quality orders (chart points)
	// one SPC chart reads; the control limits always use the last 25.
	defaultSPCOrders = 50
	maxSPCOrders     = 200
)

type complianceListRequest struct {
	Code      string             `json:"code"`
	Name      string             `json:"name"`
	Kind      string             `json:"kind"`
	BlockMode string             `json:"blockMode"`
	Regions   []string           `json:"regions"`
	Limits    []domain.LimitLine `json:"limits"`
}

// createComplianceList creates the next version of a list code (FR-33.13).
func (a *API) createComplianceList(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req complianceListRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := domain.ComplianceList{Code: req.Code, Name: req.Name, Kind: req.Kind, BlockMode: req.BlockMode, Regions: req.Regions, Limits: req.Limits}
	if fields := domain.ValidateComplianceList(in); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	list, inserted, err := a.store.CreateComplianceList(r.Context(), in, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, statusFor(inserted), list)
}

func (a *API) listComplianceLists(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, err := limitParam(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	lists, err := a.store.ListComplianceLists(r.Context(), limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list[domain.ComplianceList]{Items: lists})
}

// itemIDParam reads the {id} item route parameter; a malformed id is a 400.
func itemIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validation(map[string]string{"id": "must be a UUID"}))
		return "", false
	}
	return id, true
}

type compositionRequest struct {
	Direct map[string]decimal.Decimal `json:"direct"`
	Parts  []domain.MassPart          `json:"parts"`
}

// saveComposition appends a new version of the item's declared composition.
func (a *API) saveComposition(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	itemID, ok := itemIDParam(w, r)
	if !ok {
		return
	}
	var req compositionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	c := domain.Constituent{ItemID: itemID, Direct: req.Direct, Parts: req.Parts}
	if fields := domain.ValidateConstituent(c); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	comp, inserted, err := a.store.SaveComposition(r.Context(), c, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, statusFor(inserted), comp)
}

func (a *API) getComposition(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	itemID, ok := itemIDParam(w, r)
	if !ok {
		return
	}
	comp, err := a.store.GetComposition(r.Context(), itemID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, comp)
}

type evaluateRequest struct {
	Region string `json:"region"`
}

// evaluationView answers :evaluate-compliance. Blocked is true when any result is
// a restricted hit under a BLOCK list (QM-0122 as an error); UndeclaredItems are
// parts with no composition, rolled up as containing nothing.
type evaluationView struct {
	ItemID             string                    `json:"itemId"`
	Region             string                    `json:"region"`
	CompositionVersion int                       `json:"compositionVersion"`
	Blocked            bool                      `json:"blocked"`
	Results            []domain.ComplianceResult `json:"results"`
	UndeclaredItems    []string                  `json:"undeclaredItems"`
}

// evaluateCompliance rolls up the item's composition through its declared parts
// and evaluates it against the current version of every list for the region
// (BR-33.11). It is idempotent by value: an unchanged outcome stores nothing new.
func (a *API) evaluateCompliance(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	itemID, ok := itemIDParam(w, r)
	if !ok {
		return
	}
	var req evaluateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !domain.ValidRegion(req.Region) {
		httpx.Fail(w, r, validation(map[string]string{"region": "must be a region code such as EU, DE or US-CA"}))
		return
	}
	tree, err := a.store.LoadCompositionTree(r.Context(), itemID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	lists, err := a.store.CurrentComplianceLists(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	results, err := domain.EvaluateItem(itemID, tree.Nodes, lists, req.Region)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	stored, err := a.store.RecordComplianceResults(r.Context(), itemID, tree.RootVersion, results)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.evaluations.Add(1)
	view := evaluationView{ItemID: itemID, Region: req.Region, CompositionVersion: tree.RootVersion, Results: stored, UndeclaredItems: tree.Undeclared}
	if view.UndeclaredItems == nil {
		view.UndeclaredItems = []string{}
	}
	for _, res := range stored {
		view.Blocked = view.Blocked || (res.Restricted && res.Severity == domain.SeverityError)
	}
	httpx.JSON(w, http.StatusOK, view)
}

// studyRequest is the create body. Optional settings default to the site parameters.
type studyRequest struct {
	ItemID         string         `json:"itemId"`
	BatchID        string         `json:"batchId"`
	Protocol       string         `json:"protocol"`
	Condition      string         `json:"condition"`
	StartDate      string         `json:"startDate"`
	TestGroupID    string         `json:"testGroupId"`
	PullMonths     []int          `json:"pullMonths"`
	PullTestGroups map[int]string `json:"pullTestGroups"`
	LeadDays       *int           `json:"leadDays"`
	WindowDays     *int           `json:"windowDays"`
}

// createStudy plans a stability study and generates its pull schedule (FR-33.16, BR-33.14).
func (a *API) createStudy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req studyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	lead, window := domain.DefaultStudyLeadDays, domain.DefaultStudyWindowDays
	if req.LeadDays != nil {
		lead = *req.LeadDays
	}
	if req.WindowDays != nil {
		window = *req.WindowDays
	}
	start, dateErr := time.Parse(time.DateOnly, req.StartDate)
	spec := domain.StudySpec{ItemID: req.ItemID, BatchID: req.BatchID, Protocol: req.Protocol, Condition: req.Condition,
		StartDate: start, TestGroupID: req.TestGroupID, PullMonths: req.PullMonths, PullTestGroups: req.PullTestGroups,
		LeadDays: lead, WindowDays: window}
	fields := domain.ValidateStudy(spec)
	if dateErr != nil {
		fields["startDate"] = "must be a date as YYYY-MM-DD"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	for _, id := range spec.TestGroupIDs() {
		if _, err := a.store.GetTestGroup(r.Context(), id); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	st, err := domain.NewStudy(spec)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	st.ID = ids.New()
	saved, inserted, err := a.store.CreateStudy(r.Context(), st, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if inserted {
		a.studiesCreated.Add(1)
	}
	setETag(w, saved.Version)
	httpx.JSON(w, statusFor(inserted), saved)
}

// listStudies answers ?status=&itemId=&limit=, newest first.
func (a *API) listStudies(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.StudyFilter{Status: q.Get("status"), ItemID: q.Get("itemId")}
	fields := map[string]string{}
	if f.Status != "" && !domain.ValidStudyStatus(f.Status) {
		fields["status"] = "is not a study status"
	}
	if f.ItemID != "" && !ids.Valid(f.ItemID) {
		fields["itemId"] = "must be a UUID"
	}
	limit, err := limitParam(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	f.Limit = limit
	studies, err := a.store.ListStudies(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list[domain.Study]{Items: studies})
}

func (a *API) getStudy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeStudyMissing, "stability study not found"))
		return
	}
	st, err := a.store.GetStudy(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, st.Version)
	httpx.JSON(w, http.StatusOK, st)
}

// spcChart answers GET /v1/quality/spc?itemId=&testId=&orders= with the chart for
// that item and test over its newest orders (FR-33.11). No results yet is chart NONE.
func (a *API) spcChart(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	itemID, testID := q.Get("itemId"), q.Get("testId")
	fields := map[string]string{}
	if !ids.Valid(itemID) {
		fields["itemId"] = "must be a UUID"
	}
	if !ids.Valid(testID) {
		fields["testId"] = "must be a UUID"
	}
	orders := defaultSPCOrders
	if raw := q.Get("orders"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 2 || n > maxSPCOrders {
			fields["orders"] = "must be 2 to " + strconv.Itoa(maxSPCOrders)
		}
		orders = n
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	obs, err := a.store.ListSPCObservations(r.Context(), itemID, testID, orders)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	res, err := domain.ComputeSPC(obs)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if res.OrderIDs == nil {
		res.OrderIDs = []string{}
	}
	httpx.JSON(w, http.StatusOK, res)
}
