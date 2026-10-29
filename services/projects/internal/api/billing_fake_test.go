package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

// This file gives fakeStore the category, transaction and invoice proposal
// methods of store.Store. The rules come from the domain package and the
// idempotency comparisons from the store package, as in the Postgres store;
// the fake only keeps state.

func (f *fakeStore) UpsertCategory(_ context.Context, c domain.Category, expected int) (domain.Category, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, exists := f.cats[c.ID]
	if err := store.CheckExpected(expected, exists, cur.Version); err != nil {
		return domain.Category{}, false, err
	}
	now := time.Now().UTC()
	if exists {
		if err := domain.CheckCategoryType(cur.TxnType, c.TxnType, c.ID); err != nil {
			return domain.Category{}, false, err
		}
		cur.Name, cur.Version, cur.UpdatedAt = c.Name, cur.Version+1, now
		f.cats[c.ID] = cur
		return cur, false, nil
	}
	c.Version, c.CreatedAt, c.UpdatedAt = 1, now, now
	f.cats[c.ID] = c
	return c, true, nil
}

func (f *fakeStore) ListCategories(context.Context) ([]domain.Category, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.Category{}
	for _, c := range f.cats {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) CreateTransaction(ctx context.Context, in domain.Transaction, key string) (domain.Transaction, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, _ := tenant.FromContext(ctx)
	project, ok := f.projects[in.ProjectID]
	if !ok {
		return domain.Transaction{}, false, notFoundProblem("project")
	}
	if id, ok := f.txnKeys[key]; ok {
		if !store.SameTransaction(f.txns[id], in) {
			return domain.Transaction{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "key reused")
		}
		return f.txns[id], false, nil
	}
	if err := domain.CheckTransactionProject(project.Stage, project.Currency, in); err != nil {
		return domain.Transaction{}, false, err
	}
	cat, found := f.cats[in.CategoryID]
	if err := domain.CheckCategory(found, cat.TxnType, in); err != nil {
		return domain.Transaction{}, false, err
	}
	if in.WBSNodeID != nil {
		code, ok := f.nodeCode(in.ProjectID, *in.WBSNodeID)
		if !ok {
			prob := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeValidation, "unknown WBS node")
			prob.Errors = map[string]string{"wbsNodeId": "is not a WBS node of this project"}
			return domain.Transaction{}, false, prob
		}
		in.WBSCode = &code
	}
	in.ID, in.CreatedBy, in.CreatedAt = ids.New(), p.UserID, time.Now().UTC()
	f.txns[in.ID] = in
	f.txnKeys[key] = in.ID
	return in, true, nil
}

func (f *fakeStore) ListTransactions(_ context.Context, projectID string) ([]domain.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[projectID]; !ok {
		return nil, notFoundProblem("project")
	}
	out := []domain.Transaction{}
	for _, t := range f.txns {
		if t.ProjectID == projectID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TxnDate != out[j].TxnDate {
			return out[i].TxnDate > out[j].TxnDate
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// candidate is the selection view of a transaction (lock held), with the
// milestone of its WBS code in the project's WORKING plan.
func (f *fakeStore) candidate(t domain.Transaction) domain.Candidate {
	d, _ := domain.ParseDate(t.TxnDate)
	c := domain.Candidate{ID: t.ID, ProjectID: t.ProjectID, TxnType: string(t.TxnType), LineProperty: t.LineProperty,
		ProjectStage: f.projects[t.ProjectID].Stage, TxnDate: d, Proposed: t.ProposalID != nil}
	if t.WBSCode == nil {
		return c
	}
	for planID, nodes := range f.nodes {
		if pv := f.plans[planID]; pv.ProjectID != t.ProjectID || pv.Kind != domain.KindWorking {
			continue
		}
		for _, n := range nodes {
			if n.WBSCode == *t.WBSCode {
				c.Milestone = n.IsBillableMilestone
				if n.EarlyFinish != nil {
					c.MilestoneDate, _ = domain.ParseDate(*n.EarlyFinish)
				}
			}
		}
	}
	return c
}

func (f *fakeStore) GetTransactionBilling(_ context.Context, id string) (store.TransactionBilling, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.txns[id]
	if !ok {
		return store.TransactionBilling{}, notFoundProblem("transaction")
	}
	return store.TransactionBilling{Transaction: t, Candidate: f.candidate(t), ProjectType: f.projects[t.ProjectID].ProjectType}, nil
}

// withLines fills a proposal's lines and totals from f.propLines (lock held).
func (f *fakeStore) withLines(pr domain.InvoiceProposal, lines bool) domain.InvoiceProposal {
	cost, sales := decimal.Zero, decimal.Zero
	pr.Lines = []domain.ProposalLine{}
	for _, l := range f.propLines[pr.ID] {
		cost = cost.Add(decimal.RequireFromString(l.CostAmount))
		sales = sales.Add(decimal.RequireFromString(l.Amount))
		if lines {
			pr.Lines = append(pr.Lines, l)
		}
	}
	minor := money.MinorUnits(pr.Currency)
	pr.TotalCost, pr.TotalSales, pr.LineCount = cost.StringFixed(minor), sales.StringFixed(minor), len(f.propLines[pr.ID])
	return pr
}

func (f *fakeStore) CreateProposal(ctx context.Context, in domain.ProposalRequest, key string) (domain.InvoiceProposal, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, _ := tenant.FromContext(ctx)
	project, ok := f.projects[in.ProjectID]
	if !ok {
		return domain.InvoiceProposal{}, false, notFoundProblem("project")
	}
	types, err := domain.BillableTypes(project.ProjectType, in.TxnTypes)
	if err != nil {
		return domain.InvoiceProposal{}, false, err
	}
	if id, ok := f.propKey[key]; ok {
		if !store.SameProposalRequest(f.props[id], in, types) {
			return domain.InvoiceProposal{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "key reused")
		}
		return f.withLines(f.props[id], true), false, nil
	}
	from, _ := domain.ParseDate(in.From)
	to, _ := domain.ParseDate(in.To)
	var cands []domain.Candidate
	for _, t := range f.txns {
		if t.ProjectID == in.ProjectID {
			cands = append(cands, f.candidate(t))
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].TxnDate.Equal(cands[j].TxnDate) {
			return cands[i].TxnDate.Before(cands[j].TxnDate)
		}
		return f.txns[cands[i].ID].CreatedAt.Before(f.txns[cands[j].ID].CreatedAt)
	})
	selected, _ := domain.Select(domain.Criteria{From: from, To: to, TxnTypes: domain.TypeNames(types),
		ProjectIDs: []string{in.ProjectID}, MilestonesOnly: domain.BillsMilestones(project.ProjectType)}, cands)
	if len(selected) == 0 {
		return domain.InvoiceProposal{}, false, domain.EmptySelection(in.ProjectID, project.Stage, project.ProjectType, types)
	}
	now := time.Now().UTC()
	pr := domain.InvoiceProposal{ID: ids.New(), ProjectID: in.ProjectID, CustomerPartyID: project.CustomerPartyID,
		Status: domain.ProposalDraft, Currency: project.Currency, FromDate: in.From, ToDate: in.To, TxnTypes: types,
		CreatedBy: p.UserID, Version: 1, CreatedAt: now, UpdatedAt: now}
	for i, id := range selected {
		t := f.txns[id]
		t.ProposalID = &pr.ID
		f.txns[id] = t
		f.propLines[pr.ID] = append(f.propLines[pr.ID], domain.ProposalLine{LineNo: i + 1, TransactionID: id, TxnType: t.TxnType,
			TxnDate: t.TxnDate, CategoryID: t.CategoryID, Description: t.Description, Quantity: t.Quantity,
			UnitPrice: t.SalesPrice, CostAmount: t.CostAmount, Amount: t.SalesAmount})
	}
	f.props[pr.ID] = pr
	f.propKey[key] = pr.ID
	return f.withLines(pr, true), true, nil
}

func (f *fakeStore) GetProposal(_ context.Context, id string) (domain.InvoiceProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr, ok := f.props[id]
	if !ok {
		return domain.InvoiceProposal{}, notFoundProblem("invoice proposal")
	}
	return f.withLines(pr, true), nil
}

func (f *fakeStore) ListProposals(_ context.Context, flt store.ProposalFilter) ([]domain.InvoiceProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[flt.ProjectID]; !ok {
		return nil, notFoundProblem("project")
	}
	out := []domain.InvoiceProposal{}
	for _, pr := range f.props {
		if pr.ProjectID == flt.ProjectID && (flt.Status == "" || pr.Status == flt.Status) {
			out = append(out, f.withLines(pr, false))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// lockedProposal checks the If-Match version (lock held).
func (f *fakeStore) lockedProposal(id string, expected int) (domain.InvoiceProposal, error) {
	pr, ok := f.props[id]
	if !ok {
		return pr, notFoundProblem("invoice proposal")
	}
	if pr.Version != expected {
		return pr, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale version")
	}
	return pr, nil
}

// release returns a transaction to the pool (lock held).
func (f *fakeStore) release(txnID string) {
	t := f.txns[txnID]
	t.ProposalID = nil
	f.txns[txnID] = t
}

func (f *fakeStore) DecideProposal(ctx context.Context, id string, expected int, to domain.ProposalStatus) (domain.InvoiceProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, _ := tenant.FromContext(ctx)
	pr, err := f.lockedProposal(id, expected)
	if err != nil {
		return pr, err
	}
	if err := domain.CheckProposalMove(pr.Status, to, len(f.propLines[id])); err != nil {
		return domain.InvoiceProposal{}, err
	}
	if to == domain.ProposalCanceled {
		for _, l := range f.propLines[id] {
			f.release(l.TransactionID)
		}
	}
	if to == domain.ProposalApproved {
		user := p.UserID
		pr.ApprovedBy = &user
	}
	pr.Status, pr.Version, pr.UpdatedAt = to, pr.Version+1, time.Now().UTC()
	f.props[id] = pr
	return f.withLines(pr, true), nil
}

func (f *fakeStore) RemoveProposalLine(_ context.Context, id string, expected int, txnID string) (domain.InvoiceProposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr, err := f.lockedProposal(id, expected)
	if err != nil {
		return pr, err
	}
	next, err := domain.CheckLineRemoval(pr.Status)
	if err != nil {
		return domain.InvoiceProposal{}, err
	}
	lines := f.propLines[id]
	for i, l := range lines {
		if l.TransactionID == txnID {
			f.propLines[id] = append(lines[:i:i], lines[i+1:]...)
			f.release(txnID)
			if next == domain.ProposalDraft {
				pr.ApprovedBy = nil
			}
			pr.Status, pr.Version, pr.UpdatedAt = next, pr.Version+1, time.Now().UTC()
			f.props[id] = pr
			return f.withLines(pr, true), nil
		}
	}
	return domain.InvoiceProposal{}, notFoundProblem("proposal line")
}

// openProposals counts the DRAFT and APPROVED proposals of a project (lock held; BR-41.7).
func (f *fakeStore) openProposals(projectID string) int {
	n := 0
	for _, pr := range f.props {
		if pr.ProjectID == projectID && (pr.Status == domain.ProposalDraft || pr.Status == domain.ProposalApproved) {
			n++
		}
	}
	return n
}
