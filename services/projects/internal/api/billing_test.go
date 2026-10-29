package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

var (
	accountant = userCtx("acct-1", DutyJournalMaintain, DutyBillingMaintain, DutyInquire)
	inquirer   = userCtx("viewer-1", DutyInquire)
)

const workerID = "0b4a4c43-6f3c-4c55-9f7c-2f7f5d1c0a11"

func hourBody(projectID, prop, date, qty string) string {
	return `{"projectId":"` + projectID + `","txnType":"HOUR","categoryId":"LABOR","lineProperty":"` + prop +
		`","txnDate":"` + date + `","quantity":"` + qty + `","costPrice":"50.00","salesPrice":"90.005","currency":"USD","workerId":"` + workerID + `"}`
}

// billingProject seeds an IN_PROCESS project of type pt and the LABOR (HOUR)
// and MILESTONE (ON_ACCOUNT) categories.
func billingProject(t *testing.T, f *fakeStore, id string, pt domain.ProjectType) {
	t.Helper()
	p := sampleProject(id)
	p.ProjectType, p.Stage = pt, domain.StageInProcess
	if _, _, err := f.CreateProject(context.Background(), p, ""); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	for _, c := range []domain.Category{{ID: "LABOR", Name: "Labour", TxnType: domain.TxnHour}, {ID: "MILESTONE", Name: "Milestone billing", TxnType: domain.TxnOnAccount}} {
		if _, _, err := f.UpsertCategory(context.Background(), c, 0); err != nil && f.cats[c.ID].ID == "" {
			t.Fatal(err)
		}
	}
}

func postTxn(t *testing.T, h http.Handler, body, k string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, accountant, http.MethodPost, "/v1/projects/transactions", body, key(k))
}

func txnOf(t *testing.T, rec *httptest.ResponseRecorder) domain.Transaction {
	t.Helper()
	var out domain.Transaction
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.ID == "" {
		t.Fatalf("decode transaction: %v (%d %s)", err, rec.Code, rec.Body.String())
	}
	return out
}

func proposalOf(t *testing.T, rec *httptest.ResponseRecorder) domain.InvoiceProposal {
	t.Helper()
	var out domain.InvoiceProposal
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode proposal: %v (%s)", err, rec.Body.String())
	}
	return out
}

func TestCategories_PutCreatesRenamesAndKeepsType(t *testing.T) {
	h := router(newFake())
	setup := []string{DutySetup}
	if rec := as(t, h, []string{DutyInquire}, http.MethodPut, "/v1/projects/categories/LABOR", `{"name":"Labour","txnType":"HOUR"}`, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("put without ProjSetupAdmin: %d", rec.Code)
	}
	rec := as(t, h, setup, http.MethodPut, "/v1/projects/categories/labor", `{"name":"","txnType":"PARTS"}`, nil)
	if rec.Code != http.StatusBadRequest || len(fieldErrors(t, rec)) != 3 {
		t.Fatalf("invalid category: %d %s", rec.Code, rec.Body.String())
	}
	if rec := as(t, h, setup, http.MethodPut, "/v1/projects/categories/LABOR", `{"id":"TRAVEL","name":"Labour","txnType":"HOUR"}`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("body id differs from path: %d", rec.Code)
	}
	rec = as(t, h, setup, http.MethodPut, "/v1/projects/categories/LABOR", `{"name":"Labour","txnType":"HOUR"}`, nil)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := as(t, h, setup, http.MethodPut, "/v1/projects/categories/LABOR", `{"name":"Labour","txnType":"HOUR"}`, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("replace without If-Match: %d", rec.Code)
	}
	if rec := as(t, h, setup, http.MethodPut, "/v1/projects/categories/LABOR", `{"name":"Labour","txnType":"COST"}`, etag(1)); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeCategoryType {
		t.Fatalf("type change: %d %s", rec.Code, rec.Body.String())
	}
	if rec := as(t, h, setup, http.MethodPut, "/v1/projects/categories/LABOR", `{"name":"Professional labour","txnType":"HOUR"}`, etag(1)); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	rec = as(t, h, []string{DutyInquire}, http.MethodGet, "/v1/projects/categories", "", nil)
	var list categoryList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].Name != "Professional labour" {
		t.Fatalf("list: %v %s", err, rec.Body.String())
	}
}

func TestTransactions_ValidateKeyDutyStageCategoryAndPrice(t *testing.T) {
	f := newFake()
	h := router(f)
	billingProject(t, f, "ES-TM", domain.TypeTimeMaterial)
	created := sampleProject("ES-NEW")
	if _, _, err := f.CreateProject(context.Background(), created, ""); err != nil {
		t.Fatal(err)
	}

	if rec := do(t, h, accountant, http.MethodPost, "/v1/projects/transactions", hourBody("ES-TM", "CHARGEABLE", "2026-09-10", "8"), nil); rec.Code != http.StatusBadRequest || fieldErrors(t, rec)["Idempotency-Key"] == "" {
		t.Fatalf("missing key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, inquirer, http.MethodPost, "/v1/projects/transactions", hourBody("ES-TM", "CHARGEABLE", "2026-09-10", "8"), key("k0")); rec.Code != http.StatusForbidden {
		t.Fatalf("create without ProjJournalMaintain: %d", rec.Code)
	}
	bad := `{"projectId":"es tm","txnType":"HOUR","categoryId":"LABOR","lineProperty":"MAYBE","txnDate":"10/09/2026",
		"quantity":"1.2345","costPrice":"-1","salesPrice":"x","currency":"USD","itemId":"not-a-uuid"}`
	errs := fieldErrors(t, postTxn(t, h, bad, "k1"))
	for _, field := range []string{"projectId", "lineProperty", "txnDate", "quantity", "costPrice", "salesPrice", "workerId", "itemId"} {
		if errs[field] == "" {
			t.Errorf("expected a field error on %s, got %v", field, errs)
		}
	}
	if rec := postTxn(t, h, hourBody("NOPE", "CHARGEABLE", "2026-09-10", "8"), "k2"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", rec.Code)
	}
	if rec := postTxn(t, h, hourBody("ES-NEW", "CHARGEABLE", "2026-09-10", "8"), "k3"); errorCode(t, rec) != domain.CodeTxnStage {
		t.Fatalf("CREATED project: %d %s", rec.Code, rec.Body.String())
	}
	eur := `{"projectId":"ES-TM","txnType":"HOUR","categoryId":"LABOR","lineProperty":"CHARGEABLE","txnDate":"2026-09-10","quantity":"1","costPrice":"1","salesPrice":"1","currency":"EUR","workerId":"` + workerID + `"}`
	if rec := postTxn(t, h, eur, "k4"); errorCode(t, rec) != domain.CodeTxnCurrency {
		t.Fatalf("currency mismatch: %d %s", rec.Code, rec.Body.String())
	}
	fee := `{"projectId":"ES-TM","txnType":"FEE","categoryId":"LABOR","lineProperty":"CHARGEABLE","txnDate":"2026-09-10","quantity":"1","costPrice":"0","salesPrice":"100","currency":"USD"}`
	if rec := postTxn(t, h, fee, "k5"); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeTxnCategory {
		t.Fatalf("FEE on an HOUR category: %d %s", rec.Code, rec.Body.String())
	}
	withNode := `{"projectId":"ES-TM","wbsNodeId":"` + ids.New() + `","txnType":"HOUR","categoryId":"LABOR","lineProperty":"CHARGEABLE","txnDate":"2026-09-10","quantity":"1","costPrice":"1","salesPrice":"1","currency":"USD","workerId":"` + workerID + `"}`
	if rec := postTxn(t, h, withNode, "k6"); rec.Code != http.StatusUnprocessableEntity || fieldErrors(t, rec)["wbsNodeId"] == "" {
		t.Fatalf("unknown WBS node: %d %s", rec.Code, rec.Body.String())
	}

	rec := postTxn(t, h, hourBody("ES-TM", "CHARGEABLE", "2026-09-10", "7.5"), "k7")
	txn := txnOf(t, rec)
	// BR-41.5: 7.5 × 90.005 = 675.0375 → 675.04; 7.5 × 50 = 375.00.
	if rec.Code != http.StatusCreated || txn.SalesAmount != "675.04" || txn.CostAmount != "375.00" || txn.Quantity != "7.500" ||
		txn.SourceDocType != domain.SourceManual || txn.CreatedBy != "acct-1" || txn.JournalID != nil {
		t.Fatalf("create: %d %+v", rec.Code, txn)
	}
	replay := postTxn(t, h, hourBody("ES-TM", "CHARGEABLE", "2026-09-10", "7.50"), "k7")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || txnOf(t, replay).ID != txn.ID {
		t.Fatalf("replay (same decimal, other spelling): %d %s", replay.Code, replay.Body.String())
	}
	if rec := postTxn(t, h, hourBody("ES-TM", "CHARGEABLE", "2026-09-10", "8"), "k7"); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != "idempotency_key_reuse" {
		t.Fatalf("key reused with another body: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, inquirer, http.MethodGet, "/v1/projects/ES-TM/transactions", "", nil)
	var list transactionList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].ID != txn.ID {
		t.Fatalf("list: %v %s", err, rec.Body.String())
	}
	if rec := do(t, h, inquirer, http.MethodGet, "/v1/projects/NOPE/transactions", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("list of unknown project: %d", rec.Code)
	}
}

func TestProposals_BuildApproveRemoveCancelAndFinishGate(t *testing.T) {
	f := newFake()
	h := router(f)
	billingProject(t, f, "ES-TM", domain.TypeTimeMaterial)
	a := txnOf(t, postTxn(t, h, hourBody("ES-TM", "CHARGEABLE", "2026-09-02", "8"), "a"))
	b := txnOf(t, postTxn(t, h, hourBody("ES-TM", "CHARGEABLE", "2026-09-20", "2"), "b"))
	nonChargeable := txnOf(t, postTxn(t, h, hourBody("ES-TM", "NON_CHARGEABLE", "2026-09-05", "1"), "c"))
	outside := txnOf(t, postTxn(t, h, hourBody("ES-TM", "CHARGEABLE", "2026-10-01", "1"), "d"))

	build := func(body, k string) *httptest.ResponseRecorder {
		return do(t, h, accountant, http.MethodPost, "/v1/projects/invoice-proposals", body, key(k))
	}
	sept := `{"projectId":"ES-TM","from":"2026-09-01","to":"2026-09-30","txnTypes":["HOUR"]}`
	if rec := do(t, h, inquirer, http.MethodPost, "/v1/projects/invoice-proposals", sept, key("p0")); rec.Code != http.StatusForbidden {
		t.Fatalf("build without ProjBillingMaintain: %d", rec.Code)
	}
	if rec := build(`{"projectId":"ES-TM","from":"2026-09-30","to":"2026-09-01","txnTypes":["PARTS"]}`, "p1"); rec.Code != http.StatusBadRequest || fieldErrors(t, rec)["to"] == "" || fieldErrors(t, rec)["txnTypes"] == "" {
		t.Fatalf("bad selection: %d %s", rec.Code, rec.Body.String())
	}
	if rec := build(`{"projectId":"ES-TM","from":"2026-08-01","to":"2026-08-31"}`, "p2"); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeProposalEmpty {
		t.Fatalf("empty window: %d %s", rec.Code, rec.Body.String())
	}

	rec := build(sept, "p3")
	pr := proposalOf(t, rec)
	// 8 × 90.005 = 720.04, 2 × 90.005 = 180.01; the total is the sum of rounded lines.
	if rec.Code != http.StatusCreated || pr.Status != domain.ProposalDraft || len(pr.Lines) != 2 || pr.LineCount != 2 ||
		pr.Lines[0].TransactionID != a.ID || pr.Lines[1].TransactionID != b.ID || pr.TotalSales != "900.05" || pr.TotalCost != "500.00" || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("build: %d %+v", rec.Code, pr)
	}
	if replay := build(sept, "p3"); replay.Code != http.StatusOK || proposalOf(t, replay).ID != pr.ID {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	if rec := build(`{"projectId":"ES-TM","from":"2026-09-01","to":"2026-09-29","txnTypes":["HOUR"]}`, "p3"); errorCode(t, rec) != "idempotency_key_reuse" {
		t.Fatalf("key reused with another window: %d %s", rec.Code, rec.Body.String())
	}
	// Nothing is left to bill: a and b are on the proposal (BR-41.3 "not proposed").
	if rec := build(sept, "p4"); errorCode(t, rec) != domain.CodeProposalEmpty {
		t.Fatalf("second build: %d %s", rec.Code, rec.Body.String())
	}

	why := func(txnID, query string) billingStatus {
		rec := do(t, h, inquirer, http.MethodGet, "/v1/projects/transactions/"+txnID+"/billing?"+query, "", nil)
		var out billingStatus
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("billing status: %d %s", rec.Code, rec.Body.String())
		}
		return out
	}
	window := "from=2026-09-01&to=2026-09-30"
	for txnID, want := range map[string]string{a.ID: domain.ReasonProposed, nonChargeable.ID: domain.ReasonNotChargeable, outside.ID: domain.ReasonOutOfRange} {
		if got := why(txnID, window); got.Billable || got.Reason != want {
			t.Errorf("why not billed %s: %+v, want %s", txnID, got, want)
		}
	}
	if got := why(outside.ID, "from=2026-10-01&to=2026-10-31"); !got.Billable {
		t.Errorf("October line in an October window: %+v", got)
	}
	if got := why(outside.ID, "from=2026-10-01&to=2026-10-31&txnTypes=FEE"); got.Reason != domain.ReasonTxnType {
		t.Errorf("type filter: %+v", got)
	}
	if rec := do(t, h, inquirer, http.MethodGet, "/v1/projects/transactions/"+a.ID+"/billing?from=x&to=2026-09-30", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad window: %d", rec.Code)
	}
	if rec := do(t, h, inquirer, http.MethodGet, "/v1/projects/transactions/"+ids.New()+"/billing?"+window, "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown transaction: %d", rec.Code)
	}

	// BR-41.7: an open proposal blocks FINISHED.
	if rec := as(t, h, []string{DutyManage}, http.MethodPost, "/v1/projects/ES-TM/transitions", `{"to":"FINISHED"}`, etag(1)); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeFinish {
		t.Fatalf("finish with an open proposal: %d %s", rec.Code, rec.Body.String())
	}

	path := "/v1/projects/invoice-proposals/" + pr.ID
	if rec := do(t, h, accountant, http.MethodPost, path+"/approve", "", nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("approve without If-Match: %d", rec.Code)
	}
	if rec := do(t, h, accountant, http.MethodPost, path+"/approve", "", etag(9)); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("approve with a stale version: %d", rec.Code)
	}
	rec = do(t, h, accountant, http.MethodPost, path+"/approve", "", etag(1))
	if got := proposalOf(t, rec); rec.Code != http.StatusOK || got.Status != domain.ProposalApproved || got.ApprovedBy == nil || *got.ApprovedBy != "acct-1" || got.Version != 2 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, accountant, http.MethodPost, path+"/approve", "", etag(2)); errorCode(t, rec) != domain.CodeProposalState {
		t.Fatalf("approve twice: %d %s", rec.Code, rec.Body.String())
	}

	// Removing a line from an APPROVED proposal returns it to DRAFT and the
	// transaction to the pool (FR-41.6).
	if rec := do(t, h, accountant, http.MethodDelete, path+"/lines/"+ids.New(), "", etag(2)); rec.Code != http.StatusNotFound {
		t.Fatalf("remove a line that is not on the proposal: %d", rec.Code)
	}
	rec = do(t, h, accountant, http.MethodDelete, path+"/lines/"+b.ID, "", etag(2))
	if got := proposalOf(t, rec); rec.Code != http.StatusOK || got.Status != domain.ProposalDraft || got.ApprovedBy != nil || len(got.Lines) != 1 || got.TotalSales != "720.04" {
		t.Fatalf("remove line: %d %s", rec.Code, rec.Body.String())
	}
	if got := why(b.ID, window); !got.Billable {
		t.Fatalf("removed line is back in the pool: %+v", got)
	}

	rec = do(t, h, inquirer, http.MethodGet, "/v1/projects/invoice-proposals?projectId=ES-TM&status=DRAFT", "", nil)
	var list proposalList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].LineCount != 1 || len(list.Items[0].Lines) != 0 {
		t.Fatalf("list: %v %s", err, rec.Body.String())
	}
	if rec := do(t, h, inquirer, http.MethodGet, "/v1/projects/invoice-proposals?status=OPEN", "", nil); rec.Code != http.StatusBadRequest || fieldErrors(t, rec)["projectId"] == "" || fieldErrors(t, rec)["status"] == "" {
		t.Fatalf("list without project, bad status: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, accountant, http.MethodPost, path+"/cancel", "", etag(3))
	if got := proposalOf(t, rec); rec.Code != http.StatusOK || got.Status != domain.ProposalCanceled {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	if got := why(a.ID, window); !got.Billable {
		t.Fatalf("canceled proposal's line is back in the pool: %+v", got)
	}
	if rec := do(t, h, accountant, http.MethodDelete, path+"/lines/"+a.ID, "", etag(4)); errorCode(t, rec) != domain.CodeProposalState {
		t.Fatalf("remove from a canceled proposal: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, inquirer, http.MethodGet, "/v1/projects/invoice-proposals/not-a-uuid", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed id: %d", rec.Code)
	}
	if rec := as(t, h, []string{DutyManage}, http.MethodPost, "/v1/projects/ES-TM/transitions", `{"to":"FINISHED"}`, etag(1)); rec.Code != http.StatusOK {
		t.Fatalf("finish once no proposal is open: %d %s", rec.Code, rec.Body.String())
	}
}

// TestProposals_FixedPriceBillsDueMilestonesOnly is the BR-41.3 fixed-price
// condition: only ON_ACCOUNT lines on billable milestones scheduled to finish
// by the end date are selected, and other project types do not bill.
func TestProposals_FixedPriceBillsDueMilestonesOnly(t *testing.T) {
	f := newFake()
	h := router(f)
	billingProject(t, f, "ES-FP", domain.TypeFixedPrice)
	planID := ids.New()
	f.plans[planID] = domain.PlanVersion{ID: planID, ProjectID: "ES-FP", Kind: domain.KindWorking, Sequence: 1, Version: 1}
	due, late := "2026-09-25", "2026-10-15"
	f.nodes[planID] = []domain.WBSNode{
		{ID: ids.New(), PlanVersionID: planID, WBSCode: "1", Name: "Design approved", IsBillableMilestone: true, EarlyFinish: &due},
		{ID: ids.New(), PlanVersionID: planID, WBSCode: "2", Name: "Install complete", IsBillableMilestone: true, EarlyFinish: &late},
		{ID: ids.New(), PlanVersionID: planID, WBSCode: "3", Name: "Ordinary task", EarlyFinish: &due},
	}
	onAccount := func(node, k string) domain.Transaction {
		body := `{"projectId":"ES-FP","wbsNodeId":"` + node + `","txnType":"ON_ACCOUNT","categoryId":"MILESTONE","lineProperty":"CHARGEABLE",
			"txnDate":"2026-09-01","quantity":"1","costPrice":"0","salesPrice":"100000","currency":"USD"}`
		return txnOf(t, postTxn(t, h, body, k))
	}
	m1 := onAccount(f.nodes[planID][0].ID, "m1")
	m2 := onAccount(f.nodes[planID][1].ID, "m2")
	plain := onAccount(f.nodes[planID][2].ID, "m3")
	if m1.WBSCode == nil || *m1.WBSCode != "1" {
		t.Fatalf("WBS code not resolved: %+v", m1)
	}
	hours := txnOf(t, postTxn(t, h, hourBody("ES-FP", "CHARGEABLE", "2026-09-02", "8"), "h1"))

	build := func(body, k string) *httptest.ResponseRecorder {
		return do(t, h, accountant, http.MethodPost, "/v1/projects/invoice-proposals", body, key(k))
	}
	if rec := build(`{"projectId":"ES-FP","from":"2026-09-01","to":"2026-09-30","txnTypes":["HOUR"]}`, "f1"); errorCode(t, rec) != domain.CodeProposalType {
		t.Fatalf("fixed price HOUR billing: %d %s", rec.Code, rec.Body.String())
	}
	rec := build(`{"projectId":"ES-FP","from":"2026-09-01","to":"2026-09-30"}`, "f2")
	pr := proposalOf(t, rec)
	if rec.Code != http.StatusCreated || len(pr.Lines) != 1 || pr.Lines[0].TransactionID != m1.ID || pr.TotalSales != "100000.00" ||
		len(pr.TxnTypes) != 1 || pr.TxnTypes[0] != domain.TxnOnAccount {
		t.Fatalf("fixed-price build: %d %s", rec.Code, rec.Body.String())
	}
	why := func(txnID string) string {
		rec := do(t, h, inquirer, http.MethodGet, "/v1/projects/transactions/"+txnID+"/billing?from=2026-09-01&to=2026-09-30", "", nil)
		var out billingStatus
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Reason
	}
	if got := why(m2.ID); got != domain.ReasonMilestone {
		t.Errorf("milestone due in October: %q", got)
	}
	if got := why(plain.ID); got != domain.ReasonMilestone {
		t.Errorf("ON_ACCOUNT on an ordinary task: %q", got)
	}
	if got := why(hours.ID); got != domain.ReasonTxnType {
		t.Errorf("hours on a fixed-price project: %q", got)
	}

	billingProject(t, f, "ES-INT", domain.TypeInternal)
	internal := txnOf(t, postTxn(t, h, hourBody("ES-INT", "CHARGEABLE", "2026-09-02", "8"), "i1"))
	if rec := build(`{"projectId":"ES-INT","from":"2026-09-01","to":"2026-09-30"}`, "i2"); errorCode(t, rec) != domain.CodeProposalType {
		t.Fatalf("internal project billing: %d %s", rec.Code, rec.Body.String())
	}
	if got := why(internal.ID); got != domain.ReasonProjectType {
		t.Errorf("internal project line: %q", got)
	}
}
