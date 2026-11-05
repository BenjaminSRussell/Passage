package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/store"
)

func (f *fakeStore) CreateComplianceList(_ context.Context, l domain.ComplianceList, _ string) (domain.ComplianceList, bool, error) {
	l.ID, l.Version = ids.New(), 1
	for _, cur := range f.lists {
		if cur.Code == l.Code && cur.Version >= l.Version {
			l.Version = cur.Version + 1
		}
	}
	f.lists = append(f.lists, l)
	return l, true, nil
}

func (f *fakeStore) ListComplianceLists(context.Context, int) ([]domain.ComplianceList, error) {
	return append([]domain.ComplianceList{}, f.lists...), nil
}

func (f *fakeStore) CurrentComplianceLists(context.Context) ([]domain.ComplianceList, error) {
	return domain.LatestLists(f.lists), nil
}

func (f *fakeStore) SaveComposition(_ context.Context, c domain.Constituent, _ string) (domain.Composition, bool, error) {
	comp := domain.Composition{Version: f.comps[c.ItemID].Version + 1, Constituent: c}
	f.comps[c.ItemID] = comp
	return comp, true, nil
}

func (f *fakeStore) GetComposition(_ context.Context, itemID string) (domain.Composition, error) {
	c, ok := f.comps[itemID]
	if !ok {
		return c, httpx.NewProblem(http.StatusNotFound, domain.CodeCompositionMissing, "no composition is recorded for this item")
	}
	return c, nil
}

// LoadCompositionTree walks the fake's compositions like the Postgres store does.
func (f *fakeStore) LoadCompositionTree(_ context.Context, root string) (store.CompositionTree, error) {
	rc, ok := f.comps[root]
	if !ok {
		return store.CompositionTree{}, httpx.NewProblem(http.StatusNotFound, domain.CodeCompositionMissing, "no composition is recorded for this item")
	}
	tree := store.CompositionTree{RootVersion: rc.Version, Nodes: map[string]domain.Constituent{}}
	queue := []string{root}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, seen := tree.Nodes[id]; seen {
			continue
		}
		c, ok := f.comps[id]
		if !ok {
			tree.Undeclared = append(tree.Undeclared, id)
			c = domain.Composition{Constituent: domain.Constituent{ItemID: id}}
		}
		tree.Nodes[id] = c.Constituent
		for _, p := range c.Parts {
			queue = append(queue, p.ItemID)
		}
	}
	return tree, nil
}

// RecordComplianceResults versions results on change, like the Postgres store.
func (f *fakeStore) RecordComplianceResults(_ context.Context, itemID string, _ int, results []domain.ComplianceResult) ([]domain.ComplianceResult, error) {
	out := append([]domain.ComplianceResult{}, results...)
	for i := range out {
		key := fmt.Sprintf("%s/%s/%d/%s", itemID, out[i].ListCode, out[i].ListVersion, out[i].Region)
		hist := f.evals[key]
		if n := len(hist); n > 0 && hist[n-1].SameOutcome(out[i]) {
			out[i].Version = hist[n-1].Version
			continue
		}
		out[i].Version = len(hist) + 1
		f.evals[key] = append(hist, out[i])
	}
	return out, nil
}

func (f *fakeStore) CreateStudy(_ context.Context, st domain.Study, _ string) (domain.Study, bool, error) {
	st.CreatedAt = time.Now().UTC()
	f.studies[st.ID] = st
	return st, true, nil
}

func (f *fakeStore) GetStudy(_ context.Context, id string) (domain.Study, error) {
	st, ok := f.studies[id]
	if !ok {
		return st, httpx.NewProblem(http.StatusNotFound, domain.CodeStudyMissing, "stability study not found")
	}
	return st, nil
}

func (f *fakeStore) ListStudies(_ context.Context, filter store.StudyFilter) ([]domain.Study, error) {
	out := []domain.Study{}
	for _, st := range f.studies {
		if (filter.Status == "" || st.Status == filter.Status) && (filter.ItemID == "" || st.ItemID == filter.ItemID) {
			out = append(out, st)
		}
	}
	return out, nil
}

func (f *fakeStore) ListSPCObservations(_ context.Context, itemID, testID string, _ int) ([]domain.Observation, error) {
	return append([]domain.Observation{}, f.obs[itemID+"/"+testID]...), nil
}

const (
	dehp = "117-81-7"
	lead = "7439-92-1"
)

func postList(t *testing.T, h http.Handler, body string) *domain.ComplianceList {
	t.Helper()
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/compliance-lists", body: body, duties: dutyCompliance, key: ids.New()})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create list: %d %s", rec.Code, rec.Body.String())
	}
	var l domain.ComplianceList
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	return &l
}

func TestComplianceList_DutyVersioningAndValidation(t *testing.T) {
	h := newHandler(newFake())
	body := `{"code":"REACH-SVHC","name":"REACH Candidate List","kind":"REPORTING","blockMode":"WARN","regions":["EU"],"limits":[{"cas":"` + dehp + `","thresholdPct":"0.1"}]}`
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/compliance-lists", body: body, duties: "quality.view", key: "c0"}); rec.Code != http.StatusForbidden {
		t.Fatalf("quality.view must not create lists: %d", rec.Code)
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/compliance-lists", body: body, duties: dutyCompliance}); rec.Code != http.StatusBadRequest {
		t.Fatalf("Idempotency-Key is required: %d", rec.Code)
	}
	first, second := postList(t, h, body), postList(t, h, body)
	if first.Version != 1 || second.Version != 2 || first.ID == second.ID {
		t.Fatalf("versions %d and %d of the same code", first.Version, second.Version)
	}
	rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/compliance-lists", duties: "quality.view"})
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"code":"REACH-SVHC"`) != 2 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	bad := `{"code":"reach","kind":"OTHER","blockMode":"BLOCK","regions":["EU"],"limits":[{"cas":"7439-92-2","thresholdPct":"0.1"}]}`
	rec = call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/compliance-lists", body: bad, duties: dutyCompliance, key: ids.New()})
	for _, field := range []string{`"code"`, `"kind"`, `"limits[0].cas"`} {
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), field) {
			t.Errorf("invalid list must name %s: %d %s", field, rec.Code, rec.Body.String())
		}
	}
}

type evaluation struct {
	CompositionVersion int                       `json:"compositionVersion"`
	Blocked            bool                      `json:"blocked"`
	Results            []domain.ComplianceResult `json:"results"`
	UndeclaredItems    []string                  `json:"undeclaredItems"`
}

func evaluate(t *testing.T, h http.Handler, item, region string) evaluation {
	t.Helper()
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/items/" + item + ":evaluate-compliance", body: `{"region":"` + region + `"}`, duties: "quality.view"})
	if rec.Code != http.StatusOK {
		t.Fatalf("evaluate %s: %d %s", region, rec.Code, rec.Body.String())
	}
	var out evaluation
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func saveComp(t *testing.T, h http.Handler, item, body string) {
	t.Helper()
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/items/" + item + "/composition", body: body, duties: dutyCompliance, key: ids.New()})
	if rec.Code != http.StatusCreated {
		t.Fatalf("composition: %d %s", rec.Code, rec.Body.String())
	}
}

// AC-33.10 over HTTP through a BOM: an article of which 5 % by mass is a
// plasticiser with 2.4 % DEHP holds 0.12 % w/w and is a REACH reporting hit
// (QM-0121) in the EU; at 1.8 % DEHP (0.09 % w/w) nothing is reported. A RoHS
// lead hit under BLOCK blocks; no list applies in the US.
func TestCompliance_EvaluateRollsUpTheBOM(t *testing.T) {
	h := newHandler(newFake())
	postList(t, h, `{"code":"REACH-SVHC","kind":"REPORTING","blockMode":"WARN","regions":["EU"],"limits":[{"cas":"`+dehp+`","thresholdPct":"0.1"}]}`)
	postList(t, h, `{"code":"ROHS","kind":"RESTRICTED","blockMode":"BLOCK","regions":["EU","CN"],"limits":[{"cas":"`+lead+`","thresholdPct":"0.1"}]}`)
	for _, tc := range []struct {
		dehpInPart, wantCode string
	}{{"0.024", domain.CodeReportingHit}, {"0.018", ""}} {
		article, plasticiser, casing := ids.New(), ids.New(), ids.New()
		saveComp(t, h, plasticiser, `{"direct":{"`+dehp+`":"`+tc.dehpInPart+`"}}`)
		saveComp(t, h, article, `{"parts":[{"itemId":"`+plasticiser+`","massFraction":"0.05"},{"itemId":"`+casing+`","massFraction":"0.9"}]}`)
		eu := evaluate(t, h, article, "EU")
		if len(eu.Results) != 2 || eu.Results[0].ListCode != "REACH-SVHC" || eu.Results[0].Code != tc.wantCode || eu.Blocked {
			t.Errorf("DEHP %s: %+v", tc.dehpInPart, eu)
		}
		if tc.wantCode != "" && !eu.Results[0].Hits[0].PercentWW.Equal(decimal.RequireFromString("0.12")) {
			t.Errorf("rolled-up concentration %s, want 0.12 %%", eu.Results[0].Hits[0].PercentWW)
		}
		if len(eu.UndeclaredItems) != 1 || eu.UndeclaredItems[0] != casing {
			t.Errorf("the casing has no composition and must be reported: %v", eu.UndeclaredItems)
		}
		if us := evaluate(t, h, article, "US"); len(us.Results) != 0 {
			t.Errorf("no list applies in the US: %+v", us.Results)
		}
	}
	solder := ids.New()
	saveComp(t, h, solder, `{"direct":{"`+lead+`":"0.4"}}`)
	if eu := evaluate(t, h, solder, "EU"); !eu.Blocked || eu.Results[1].Code != domain.CodeRestricted || eu.Results[1].Severity != domain.SeverityError {
		t.Errorf("lead at 40 %% under a BLOCK list: %+v", eu)
	}
}

// BR-33.11: a re-evaluation with the same outcome keeps its result version; a new
// composition that changes the outcome stores the next version.
func TestCompliance_ResultVersionsChangeOnlyWithTheOutcome(t *testing.T) {
	h := newHandler(newFake())
	postList(t, h, `{"code":"ROHS","kind":"RESTRICTED","blockMode":"BLOCK","regions":["EU"],"limits":[{"cas":"`+lead+`","thresholdPct":"0.1"}]}`)
	item := ids.New()
	saveComp(t, h, item, `{"direct":{"`+lead+`":"0.0005"}}`)
	if first, again := evaluate(t, h, item, "EU"), evaluate(t, h, item, "EU"); first.Results[0].Version != 1 || again.Results[0].Version != 1 {
		t.Fatalf("unchanged outcome: versions %d and %d", first.Results[0].Version, again.Results[0].Version)
	}
	saveComp(t, h, item, `{"direct":{"`+lead+`":"0.002"}}`)
	changed := evaluate(t, h, item, "EU")
	if changed.Results[0].Version != 2 || !changed.Blocked || changed.CompositionVersion != 2 {
		t.Fatalf("changed outcome: %+v", changed)
	}
}

func TestCompliance_EvaluateAndCompositionErrors(t *testing.T) {
	h := newHandler(newFake())
	item := ids.New()
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/items/" + item + ":evaluate-compliance", body: `{"region":"EU"}`, duties: "quality.view"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), domain.CodeCompositionMissing) {
		t.Errorf("an item without a composition cannot be declared compliant: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/items/" + item + ":evaluate-compliance", body: `{"region":"europe"}`, duties: "quality.view"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"region"`) {
		t.Errorf("bad region: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/items/" + item + "/composition", duties: dutyCompliance, key: ids.New(),
		body: `{"parts":[{"itemId":"` + item + `","massFraction":"0.5"}]}`})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "parts[0].itemId") {
		t.Errorf("an item cannot contain itself: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/items/" + item + "/composition", duties: "quality.view", key: ids.New(),
		body: `{"direct":{"` + lead + `":"0.1"}}`})
	if rec.Code != http.StatusForbidden {
		t.Errorf("quality.view must not maintain compositions: %d", rec.Code)
	}
	rec = call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/items/" + item + "/composition", duties: "quality.view"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), domain.CodeCompositionMissing) {
		t.Errorf("missing composition: %d %s", rec.Code, rec.Body.String())
	}
	// A cycle across two items is only visible on roll-up: 422 QM-0165.
	other := ids.New()
	saveComp(t, h, item, `{"parts":[{"itemId":"`+other+`","massFraction":"0.5"}]}`)
	saveComp(t, h, other, `{"parts":[{"itemId":"`+item+`","massFraction":"0.5"}]}`)
	rec = call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/items/" + item + ":evaluate-compliance", body: `{"region":"EU"}`, duties: "quality.view"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeCompositionCycle) {
		t.Errorf("cycle: %d %s", rec.Code, rec.Body.String())
	}
}

func studyBody(item, group string, extra string) string {
	return `{"itemId":"` + item + `","batchId":"B-1","protocol":"ICH Q1A(R2)","condition":"25C/60RH","startDate":"2026-01-31",` +
		`"testGroupId":"` + group + `","pullMonths":[0,3,6,9,12,18,24,36]` + extra + `}`
}

// AC-33.13 over HTTP: pull dates from the start date with orders 7 days before,
// each pull tested with the study's test group unless it names its own.
func TestStudy_CreateListAndGet(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	group, _, _ := createGroup(t, h, true)
	accelerated, _, _ := createGroup(t, h, true)
	item := ids.New()
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/stability-studies", body: studyBody(item, group, ""), duties: "quality.view", key: "s0"}); rec.Code != http.StatusForbidden {
		t.Fatalf("quality.view must not plan studies: %d", rec.Code)
	}
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/stability-studies", duties: dutyStability, key: "s1",
		body: studyBody(item, group, `,"pullTestGroups":{"36":"`+accelerated+`"}`)})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var st domain.Study
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Status != domain.StudyPlanned || len(st.Pulls) != 8 || st.LeadDays != domain.DefaultStudyLeadDays || st.WindowDays != domain.DefaultStudyWindowDays {
		t.Fatalf("study: %+v", st)
	}
	for i, want := range []string{"2026-01-31", "2026-04-30", "2026-07-31"} {
		if got := st.Pulls[i].PullDate.Format(time.DateOnly); got != want {
			t.Errorf("pull %d on %s, want %s", st.Pulls[i].Months, got, want)
		}
	}
	if st.Pulls[1].OrderDate.Format(time.DateOnly) != "2026-04-23" || st.Pulls[1].TestGroupID != group || st.Pulls[7].TestGroupID != accelerated {
		t.Errorf("pull 3: order %s group %s; pull 36 group %s", st.Pulls[1].OrderDate.Format(time.DateOnly), st.Pulls[1].TestGroupID, st.Pulls[7].TestGroupID)
	}
	if rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/stability-studies/" + st.ID, duties: "quality.view"}); rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/stability-studies?status=PLANNED&itemId=" + item, duties: "quality.view"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), st.ID) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/stability-studies?status=OPEN", duties: "quality.view"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"status"`) {
		t.Fatalf("bad status filter: %d %s", rec.Code, rec.Body.String())
	}
}

func TestStudy_RejectsBadInput(t *testing.T) {
	h := newHandler(newFake())
	group, _, _ := createGroup(t, h, true)
	item := ids.New()
	for name, tc := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"bad date":              {strings.Replace(studyBody(item, group, ""), "2026-01-31", "31/01/2026", 1), http.StatusBadRequest, "startDate"},
		"no pulls":              {strings.Replace(studyBody(item, group, ""), "[0,3,6,9,12,18,24,36]", "[]", 1), http.StatusBadRequest, "pullMonths"},
		"descending pulls":      {strings.Replace(studyBody(item, group, ""), "[0,3,6,9,12,18,24,36]", "[6,3]", 1), http.StatusBadRequest, "pullMonths"},
		"lead too large":        {studyBody(item, group, `,"leadDays":40`), http.StatusBadRequest, "leadDays"},
		"override not a pull":   {studyBody(item, group, `,"pullTestGroups":{"48":"`+group+`"}`), http.StatusBadRequest, "pullTestGroups.48"},
		"unknown test group":    {studyBody(item, ids.New(), ""), http.StatusNotFound, domain.CodeTestGroupMissing},
		"unknown pull override": {studyBody(item, group, `,"pullTestGroups":{"3":"`+ids.New()+`"}`), http.StatusNotFound, domain.CodeTestGroupMissing},
	} {
		rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/stability-studies", body: tc.body, duties: dutyStability, key: ids.New()})
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/stability-studies/" + ids.New(), duties: "quality.view"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), domain.CodeStudyMissing) {
		t.Fatalf("missing study: %d %s", rec.Code, rec.Body.String())
	}
}

func spcObs(f *fakeStore, item, test string, values ...[]string) {
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	for i, group := range values {
		order := ids.New()
		for s, v := range group {
			f.obs[item+"/"+test] = append(f.obs[item+"/"+test], domain.Observation{QualityOrderID: order, SampleNo: s + 1,
				Value: decimal.RequireFromString(v), HasValue: true, Passed: true, RecordedAt: t0.Add(time.Duration(i) * time.Hour)})
		}
	}
}

func TestSPC_ChartEmptyStateAndErrors(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	item, test := ids.New(), ids.New()
	var groups [][]string
	for i := 0; i < 5; i++ {
		groups = append(groups, []string{"10.0", "10.2", "9.8", "10.1"})
	}
	spcObs(f, item, test, groups...)
	rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/spc?itemId=" + item + "&testId=" + test, duties: "quality.view"})
	var res domain.SPCResult
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &res) != nil || res.Chart != domain.ChartXbarR || res.XBarR.SubgroupSize != 4 || len(res.OrderIDs) != 5 {
		t.Fatalf("spc: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/spc?itemId=" + ids.New() + "&testId=" + ids.New(), duties: "quality.view"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"chart":"NONE"`) || !strings.Contains(rec.Body.String(), `"orderIds":[]`) {
		t.Fatalf("no results yet is an empty chart: %d %s", rec.Code, rec.Body.String())
	}
	for _, q := range []string{"?itemId=nope&testId=" + test, "?itemId=" + item + "&testId=" + test + "&orders=1", "?itemId=" + item + "&testId=" + test + "&orders=500"} {
		if rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/spc" + q, duties: "quality.view"}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	single := ids.New()
	spcObs(f, single, test, []string{"1"}, []string{"2"})
	rec = call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/spc?itemId=" + single + "&testId=" + test, duties: "quality.view"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeSPCUnsupported) {
		t.Fatalf("one sample per order cannot form an X-bar/R chart: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/spc?itemId=" + item + "&testId=" + test, duties: "quality.result.record"}); rec.Code != http.StatusForbidden {
		t.Fatalf("SPC needs quality.view: %d", rec.Code)
	}
}
