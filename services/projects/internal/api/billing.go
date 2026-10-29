package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

// Duties for transactions and billing (PRD §8.41.17).
const (
	DutyJournalMaintain = "ProjJournalMaintain"
	DutyBillingMaintain = "ProjBillingMaintain"
)

// categoryBody is the wire shape of PUT /v1/projects/categories/{categoryId}.
type categoryBody struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	TxnType string `json:"txnType"`
}

type categoryList struct {
	Items []domain.Category `json:"items"`
}

// transactionRequest is the wire shape of POST /v1/projects/transactions. The
// caller gives the quantity and unit prices; the amounts are computed (BR-41.5).
type transactionRequest struct {
	ProjectID     string  `json:"projectId"`
	WBSNodeID     *string `json:"wbsNodeId,omitempty"`
	TxnType       string  `json:"txnType"`
	CategoryID    string  `json:"categoryId"`
	LineProperty  string  `json:"lineProperty"`
	TxnDate       string  `json:"txnDate"`
	Quantity      string  `json:"quantity"`
	CostPrice     string  `json:"costPrice"`
	SalesPrice    string  `json:"salesPrice"`
	Currency      string  `json:"currency"`
	WorkerID      *string `json:"workerId,omitempty"`
	ItemID        *string `json:"itemId,omitempty"`
	VendorPartyID *string `json:"vendorPartyId,omitempty"`
	SourceDocType string  `json:"sourceDocType,omitempty"`
	SourceDocID   *string `json:"sourceDocId,omitempty"`
	Description   string  `json:"description,omitempty"`
}

type transactionList struct {
	Items      []domain.Transaction `json:"items"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

// billingStatus is the "why not billed" explanation of one transaction.
type billingStatus struct {
	TransactionID string `json:"transactionId"`
	Billable      bool   `json:"billable"`
	Reason        string `json:"reason,omitempty"`
}

// buildProposalRequest is the selection of POST /v1/projects/invoice-proposals.
type buildProposalRequest struct {
	ProjectID string   `json:"projectId"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	TxnTypes  []string `json:"txnTypes,omitempty"`
}

type proposalList struct {
	Items      []domain.InvoiceProposal `json:"items"`
	NextCursor string                   `json:"nextCursor,omitempty"`
}

// registerBilling mounts categories, transactions and invoice proposals.
// Category setup needs ProjSetupAdmin, recording transactions
// ProjJournalMaintain, building and editing proposals ProjBillingMaintain and
// every read ProjInquire (§8.41.17).
func (a *API) registerBilling(r chi.Router) {
	r.With(authz.RequireDuty(DutyInquire)).Get("/categories", a.listCategories)
	r.With(authz.RequireDuty(DutySetup)).Put("/categories/{categoryId}", a.putCategory)

	r.With(authz.RequireDuty(DutyJournalMaintain)).Post("/transactions", a.createTransaction)
	r.With(authz.RequireDuty(DutyInquire)).Get("/transactions/{txnId}/billing", a.billingStatus)
	r.With(authz.RequireDuty(DutyInquire)).Get("/{id}/transactions", a.listTransactions)

	r.With(authz.RequireDuty(DutyBillingMaintain)).Post("/invoice-proposals", a.buildProposal)
	r.With(authz.RequireDuty(DutyInquire)).Get("/invoice-proposals", a.listProposals)
	r.With(authz.RequireDuty(DutyInquire)).Get("/invoice-proposals/{proposalId}", a.getProposal)
	r.With(authz.RequireDuty(DutyBillingMaintain)).Post("/invoice-proposals/{proposalId}/approve", a.decideProposal(domain.ProposalApproved))
	r.With(authz.RequireDuty(DutyBillingMaintain)).Post("/invoice-proposals/{proposalId}/cancel", a.decideProposal(domain.ProposalCanceled))
	r.With(authz.RequireDuty(DutyBillingMaintain)).Delete("/invoice-proposals/{proposalId}/lines/{txnId}", a.removeProposalLine)
}

func (a *API) listCategories(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	items, err := a.store.ListCategories(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if items == nil {
		items = []domain.Category{}
	}
	httpx.JSON(w, http.StatusOK, categoryList{Items: items})
}

// putCategory creates a category (no If-Match, 201) or renames it (If-Match
// with its version, 200); its transaction type cannot change (PROJ-CAT-001).
func (a *API) putCategory(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "categoryId")
	var req categoryBody
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.ID != "" && req.ID != id {
		httpx.Fail(w, r, validation(map[string]string{"id": "must match the path"}))
		return
	}
	c := domain.Category{ID: id, Name: strings.TrimSpace(req.Name), TxnType: domain.TxnType(req.TxnType)}
	if fields := domain.ValidateCategory(c); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	expected, ok := optionalIfMatch(w, r)
	if !ok {
		return
	}
	out, created, err := a.store.UpsertCategory(r.Context(), c, expected)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, createdStatus(created), out)
}

// createTransaction records an HOUR, COST, ITEM, FEE or ON_ACCOUNT transaction
// with its amounts priced per line (BR-41.5). Idempotency-Key is required; a
// replay returns the stored transaction with 200.
func (a *API) createTransaction(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok || !requireUser(w, r, p) {
		return
	}
	var req transactionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, fields := idempotencyKey(r)
	if fields == nil {
		fields = map[string]string{}
	}
	in := domain.Transaction{
		ProjectID: req.ProjectID, WBSNodeID: req.WBSNodeID, TxnType: domain.TxnType(req.TxnType),
		CategoryID: req.CategoryID, LineProperty: req.LineProperty, TxnDate: req.TxnDate,
		Quantity: req.Quantity, CostPrice: req.CostPrice, SalesPrice: req.SalesPrice, Currency: req.Currency,
		WorkerID: req.WorkerID, ItemID: req.ItemID, VendorPartyID: req.VendorPartyID,
		SourceDocType: req.SourceDocType, SourceDocID: req.SourceDocID, Description: strings.TrimSpace(req.Description),
	}
	if in.SourceDocType == "" {
		in.SourceDocType = domain.SourceManual
	}
	for k, v := range domain.ValidateTransaction(in) {
		fields[k] = v
	}
	if !domain.ValidProjectID(req.ProjectID) {
		fields["projectId"] = "must be a project id"
	}
	for name, v := range map[string]*string{"wbsNodeId": in.WBSNodeID, "workerId": in.WorkerID, "itemId": in.ItemID,
		"vendorPartyId": in.VendorPartyID, "sourceDocId": in.SourceDocID} {
		if v != nil && !ids.Valid(*v) {
			fields[name] = "must be a UUID"
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	if err := domain.PriceLine(&in); err != nil {
		httpx.Fail(w, r, validation(map[string]string{"quantity": "must be a decimal"}))
		return
	}
	out, created, err := a.store.CreateTransaction(r.Context(), in, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if !created {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, createdStatus(created), out)
}

// listTransactions returns one page of the transactions of one project, newest
// first: limit (1 to 100, default 100) and the opaque cursor from the previous
// page's nextCursor (D3e).
func (a *API) listTransactions(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, after, err := idPageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	page, err := a.store.ListTransactions(r.Context(), chi.URLParam(r, "id"), after, limit)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	body := transactionList{Items: page.Items, NextCursor: encodeCursor(page.NextID)}
	if body.Items == nil {
		body.Items = []domain.Transaction{}
	}
	httpx.JSON(w, http.StatusOK, body)
}

// billingStatus explains whether a transaction would be selected for a window
// and types, and if not, the first reason why (BR-41.3). The query carries
// from, to and an optional comma-separated txnTypes; the project's type
// narrows the types as a build does (PROJECT_TYPE when it bills none of them).
func (a *API) billingStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	txnID := chi.URLParam(r, "txnId")
	if !ids.Valid(txnID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "transaction not found"))
		return
	}
	q := r.URL.Query()
	in := domain.ProposalRequest{From: q.Get("from"), To: q.Get("to"), TxnTypes: splitTypes(q.Get("txnTypes"))}
	fields := domain.ValidateProposalRequest(in)
	delete(fields, "projectId") // the transaction names its project
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	got, err := a.store.GetTransactionBilling(r.Context(), txnID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	reason := domain.ReasonProjectType
	if types, err := domain.BillableTypes(got.ProjectType, in.TxnTypes); err == nil {
		from, _ := domain.ParseDate(in.From)
		to, _ := domain.ParseDate(in.To)
		reason = domain.Explain(got.Candidate, domain.Criteria{From: from, To: to, TxnTypes: domain.TypeNames(types),
			MilestonesOnly: domain.BillsMilestones(got.ProjectType)})
	}
	httpx.JSON(w, http.StatusOK, billingStatus{TransactionID: got.Transaction.ID, Billable: reason == "", Reason: reason})
}

func splitTypes(raw string) []domain.TxnType {
	var out []domain.TxnType
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, domain.TxnType(t))
		}
	}
	return out
}

// buildProposal selects the billable transactions of one project and writes a
// DRAFT invoice proposal (FR-41.6, BR-41.3). Idempotency-Key is required; a
// replay returns the stored proposal with 200. Nothing billable is PROJ-PROP-002.
func (a *API) buildProposal(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok || !requireUser(w, r, p) {
		return
	}
	var req buildProposalRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, fields := idempotencyKey(r)
	if fields == nil {
		fields = map[string]string{}
	}
	in := domain.ProposalRequest{ProjectID: req.ProjectID, From: req.From, To: req.To}
	for _, t := range req.TxnTypes {
		in.TxnTypes = append(in.TxnTypes, domain.TxnType(t))
	}
	for k, v := range domain.ValidateProposalRequest(in) {
		fields[k] = v
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	out, created, err := a.store.CreateProposal(r.Context(), in, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	if !created {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, createdStatus(created), out)
}

// listProposals returns one page of the proposals of one project (projectId is
// required), optionally of one status, newest first, paged like the
// transaction list (D3e).
func (a *API) listProposals(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	f := store.ProposalFilter{ProjectID: q.Get("projectId"), Status: domain.ProposalStatus(q.Get("status"))}
	fields := map[string]string{}
	if f.ProjectID == "" || len(f.ProjectID) > 20 {
		fields["projectId"] = "is required, at most 20 characters"
	}
	if f.Status != "" && !domain.ValidProposalStatus(f.Status) {
		fields["status"] = "must be DRAFT, APPROVED, POSTED or CANCELED"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	limit, after, err := idPageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f.AfterID, f.Limit = after, limit
	page, err := a.store.ListProposals(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	body := proposalList{Items: page.Items, NextCursor: encodeCursor(page.NextID)}
	if body.Items == nil {
		body.Items = []domain.InvoiceProposal{}
	}
	httpx.JSON(w, http.StatusOK, body)
}

// proposalID reads {proposalId}; a malformed id is 404 like an unknown one.
func proposalID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "proposalId")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "invoice proposal not found"))
		return "", false
	}
	return id, true
}

func (a *API) getProposal(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := proposalID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetProposal(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

// decideProposal returns the handler for approve and cancel. If-Match carries
// the proposal version (428 when absent, 412 when stale).
func (a *API) decideProposal(to domain.ProposalStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal(w, r)
		if !ok || !requireUser(w, r, p) {
			return
		}
		id, ok := proposalID(w, r)
		if !ok {
			return
		}
		version, ok := ifMatchVersion(w, r)
		if !ok {
			return
		}
		out, err := a.store.DecideProposal(r.Context(), id, version, to)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		setETag(w, out.Version)
		httpx.JSON(w, http.StatusOK, out)
	}
}

// removeProposalLine takes one transaction off a DRAFT or APPROVED proposal and
// returns it to the pool. If-Match carries the proposal version.
func (a *API) removeProposalLine(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := proposalID(w, r)
	if !ok {
		return
	}
	txnID := chi.URLParam(r, "txnId")
	if !ids.Valid(txnID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "proposal line not found"))
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	out, err := a.store.RemoveProposalLine(r.Context(), id, version, txnID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}
