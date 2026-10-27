package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

// This file gives fakeStore the change order and risk register methods of
// store.Store. The rules come from the domain package, as in the Postgres
// store; the fake only keeps state.

func notFoundProblem(what string) error {
	return httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, what+" not found")
}

func (f *fakeStore) CreateChangeOrder(ctx context.Context, in domain.ChangeOrder, key string) (domain.ChangeOrder, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, _ := tenant.FromContext(ctx)
	project, ok := f.projects[in.ProjectID]
	if !ok {
		return domain.ChangeOrder{}, false, notFoundProblem("project")
	}
	lines, err := domain.RoundLines(in.Lines, project.Currency)
	if err != nil {
		return domain.ChangeOrder{}, false, err
	}
	in.Lines = lines
	if id, ok := f.coKeys[key]; ok {
		if !store.SameChangeOrder(f.cos[id], in) {
			return domain.ChangeOrder{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "key reused")
		}
		return f.cos[id], false, nil
	}
	for i, l := range in.Lines {
		if l.WBSNodeID == nil {
			continue
		}
		code, ok := f.nodeCode(in.ProjectID, *l.WBSNodeID)
		if !ok {
			prob := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeValidation, "unknown WBS node")
			prob.Errors = map[string]string{fmt.Sprintf("lines[%d].wbsNodeId", i): "is not a WBS node of this project"}
			return domain.ChangeOrder{}, false, prob
		}
		in.Lines[i].WBSCode = &code
	}
	n := 1
	for _, co := range f.cos {
		if co.ProjectID == in.ProjectID {
			n++
		}
	}
	co := in
	co.ID = ids.New()
	co.Number = fmt.Sprintf("CO-%04d", n)
	co.Status = domain.COStatusDraft
	co.CreatedBy = p.UserID
	co.Version = 1
	co.CreatedAt = time.Now().UTC()
	co.UpdatedAt = co.CreatedAt
	if co.CostImpact, co.RevenueImpact, err = domain.Totals(co.Lines, project.Currency); err != nil {
		return domain.ChangeOrder{}, false, err
	}
	f.cos[co.ID] = co
	f.coKeys[key] = co.ID
	return co, true, nil
}

// nodeCode finds a node of any plan of the project (lock held).
func (f *fakeStore) nodeCode(projectID, nodeID string) (string, bool) {
	for planID, nodes := range f.nodes {
		if f.plans[planID].ProjectID != projectID {
			continue
		}
		for _, n := range nodes {
			if n.ID == nodeID {
				return n.WBSCode, true
			}
		}
	}
	return "", false
}

func (f *fakeStore) GetChangeOrder(_ context.Context, id string) (domain.ChangeOrder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	co, ok := f.cos[id]
	if !ok {
		return domain.ChangeOrder{}, notFoundProblem("change order")
	}
	return co, nil
}

func (f *fakeStore) ListChangeOrders(_ context.Context, flt store.ChangeOrderFilter) ([]domain.ChangeOrder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[flt.ProjectID]; !ok {
		return nil, notFoundProblem("project")
	}
	out := []domain.ChangeOrder{}
	for _, co := range f.cos {
		if co.ProjectID == flt.ProjectID && (flt.Type == "" || co.Type == flt.Type) && (flt.Status == "" || co.Status == flt.Status) {
			co.Lines = []domain.ChangeOrderLine{}
			out = append(out, co)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

func (f *fakeStore) DecideChangeOrder(ctx context.Context, id string, expected int, to domain.ChangeOrderStatus, approvalRef *string) (domain.ChangeOrder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, _ := tenant.FromContext(ctx)
	co, ok := f.cos[id]
	if !ok {
		return domain.ChangeOrder{}, notFoundProblem("change order")
	}
	if co.Version != expected {
		return domain.ChangeOrder{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale version")
	}
	if approvalRef != nil && to != domain.COStatusRejected {
		co.CustomerApprovalRef = approvalRef
	}
	if err := domain.CheckDecision(co, to, p.UserID, domain.DefaultCustomerApprovalRequired); err != nil {
		return domain.ChangeOrder{}, err
	}
	co.Status = to
	if to != domain.COStatusSubmitted {
		actor := p.UserID
		co.DecidedBy = &actor
	}
	co.Version++
	f.cos[id] = co
	return co, nil
}

func (f *fakeStore) ApplyChangeOrder(ctx context.Context, id string, expected int) (store.AppliedChangeOrder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, _ := tenant.FromContext(ctx)
	co, ok := f.cos[id]
	if !ok {
		return store.AppliedChangeOrder{}, notFoundProblem("change order")
	}
	if co.Status == domain.COStatusApplied {
		return store.AppliedChangeOrder{}, domain.CheckChangeOrderMove(co, domain.COStatusApplied)
	}
	if co.Version != expected {
		return store.AppliedChangeOrder{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale version")
	}
	if err := domain.CheckDecision(co, domain.COStatusApplied, p.UserID, domain.DefaultCustomerApprovalRequired); err != nil {
		return store.AppliedChangeOrder{}, err
	}
	var base *domain.PlanVersion
	for _, pv := range f.plans {
		if pv.ProjectID == co.ProjectID && pv.Kind == domain.KindBaseline && pv.SupersededByVersionID == nil {
			pv := pv
			base = &pv
		}
	}
	if base == nil {
		return store.AppliedChangeOrder{}, &domain.Error{Code: domain.CodeNoBaseline, Detail: "no baseline"}
	}
	project := f.projects[co.ProjectID]
	effect, err := domain.ApplyChangeOrder(co, project.ContractValue, project.Currency, f.nodes[base.ID])
	if err != nil {
		return store.AppliedChangeOrder{}, err
	}
	seq := 0
	for _, pv := range f.plans {
		if pv.ProjectID == co.ProjectID {
			seq++
		}
	}
	from := base.ID
	next := domain.PlanVersion{ID: ids.New(), ProjectID: co.ProjectID, Kind: domain.KindBaseline, Sequence: seq + 1, CopiedFromVersionID: &from, Locked: true, Version: 1}
	base.SupersededByVersionID = &next.ID
	f.plans[base.ID] = *base
	f.plans[next.ID] = next
	f.nodes[next.ID] = effect.Nodes
	f.links[next.ID] = append([]domain.WBSLink(nil), f.links[base.ID]...)
	project.ContractValue = effect.ContractValue
	project.Version++
	f.projects[project.ID] = project
	now := time.Now().UTC()
	actor := p.UserID
	co.Status, co.AppliedAt, co.AppliedBy, co.AppliedBaselineVersionID = domain.COStatusApplied, &now, &actor, &next.ID
	co.Version++
	f.cos[id] = co
	effect.Nodes = nil
	return store.AppliedChangeOrder{ChangeOrder: co, Effect: effect, Baseline: next, Project: project}, nil
}

func (f *fakeStore) CreateRisk(_ context.Context, in domain.RiskIssue, key string) (domain.RiskIssue, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[in.ProjectID]; !ok {
		return domain.RiskIssue{}, false, notFoundProblem("project")
	}
	if id, ok := f.riskKey[key]; ok {
		if !store.SameRisk(f.risks[id], in) {
			return domain.RiskIssue{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "key reused")
		}
		return f.risks[id], false, nil
	}
	r := in
	r.ID = ids.New()
	r.Status = domain.RiskOpen
	r.Score = domain.RiskScore(r.Probability, r.Impact)
	r.Version = 1
	r.CreatedAt = time.Now().UTC()
	r.UpdatedAt = r.CreatedAt
	f.risks[r.ID] = r
	f.riskKey[key] = r.ID
	return r, true, nil
}

func (f *fakeStore) ListRisks(_ context.Context, flt store.RiskFilter) ([]domain.RiskIssue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[flt.ProjectID]; !ok {
		return nil, notFoundProblem("project")
	}
	out := []domain.RiskIssue{}
	for _, r := range f.risks {
		if r.ProjectID == flt.ProjectID && (flt.Kind == "" || r.Kind == flt.Kind) && (flt.Status == "" || r.Status == flt.Status) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (f *fakeStore) UpdateRisk(_ context.Context, expected int, in domain.RiskIssue) (domain.RiskIssue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.risks[in.ID]
	if !ok || cur.ProjectID != in.ProjectID {
		return domain.RiskIssue{}, notFoundProblem("risk or issue")
	}
	if cur.Version != expected {
		return domain.RiskIssue{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale version")
	}
	if err := domain.CheckRiskMove(cur.Status, in.Status); err != nil {
		return domain.RiskIssue{}, err
	}
	in.Score = domain.RiskScore(in.Probability, in.Impact)
	in.Version = cur.Version + 1
	in.CreatedAt, in.UpdatedAt = cur.CreatedAt, time.Now().UTC()
	f.risks[in.ID] = in
	return in, nil
}
