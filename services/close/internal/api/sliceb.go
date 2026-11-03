package api

import (
	"context"
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

const (
	dutyReopen         = "period.reopen"
	dutyChecklistWrite = "close.checklist.write"
	dutyChecklistRead  = "close.checklist.read"
	dutyAllocWrite     = "close.allocation.write"
	dutyAllocRun       = "close.allocation.run"
	dutyAccrualWrite   = "close.accrual.write"
	dutyAccrualRun     = "close.accrual.run"
	dutyRegroupWrite   = "close.regrouping.write"
	dutyRegroupRun     = "close.regrouping.run"
	idempotencyHeader  = "Idempotency-Key"
)

type PeriodStore interface {
	PeriodMatrix(ctx context.Context, legalEntityID, ledgerID string) ([]domain.PeriodStatus, error)
	TransitionPeriod(ctx context.Context, in domain.PeriodTransition) (domain.PeriodStatus, error)
	RequestReopen(ctx context.Context, in domain.StoredReopen) (domain.StoredReopen, error)
	ApproveReopen(ctx context.Context, id, actor string) (domain.StoredReopen, error)
	RejectReopen(ctx context.Context, id, actor string) (domain.StoredReopen, error)
	ExpireReopened(ctx context.Context) (int, error)
}

type ChecklistStore interface {
	PutTemplate(ctx context.Context, in domain.Template) (domain.Template, error)
	GetTemplate(ctx context.Context, id string) (domain.Template, error)
	CreateRun(ctx context.Context, templateID, fiscalPeriodID, actor string, entities []string, key, hash string) (domain.CloseRun, error)
	ListTasks(ctx context.Context, runID, entity, status string) ([]domain.BoardTask, error)
	StartTask(ctx context.Context, taskID, actor string) (domain.BoardTask, error)
	CompleteTask(ctx context.Context, taskID, actor, evidence string) (domain.BoardTask, error)
	SkipTask(ctx context.Context, taskID, actor, reason string) (domain.BoardTask, error)
	ReassignTask(ctx context.Context, taskID, actor, assignee string) (domain.BoardTask, error)
}

type AllocationStore interface {
	PutAllocationRule(ctx context.Context, in domain.AllocationRule) (domain.AllocationRule, error)
	AllocationRule(ctx context.Context, id string) (domain.AllocationRule, error)
	SaveAllocationRun(ctx context.Context, in domain.AllocationRun, key, requestHash string) (domain.AllocationRun, error)
	AllocationRun(ctx context.Context, id string) (domain.AllocationRun, error)
	MarkAllocationPosted(ctx context.Context, runID, entryID string) (domain.AllocationRun, error)
	MarkAllocationReversed(ctx context.Context, runID, entryID string) (domain.AllocationRun, error)
}

type AccrualStore interface {
	PutAccrualScheme(ctx context.Context, in domain.AccrualDefinition) (domain.AccrualDefinition, error)
	AccrualScheme(ctx context.Context, ledgerID, code string) (domain.AccrualDefinition, error)
	SaveAccrualRun(ctx context.Context, in domain.AccrualRun, key, requestHash string) (domain.AccrualRun, error)
	AccrualRun(ctx context.Context, id string) (domain.AccrualRun, error)
}

type RegroupingStore interface {
	PutRegroupingRule(ctx context.Context, in domain.StoredRegroupingRule) (domain.StoredRegroupingRule, error)
	RegroupingRule(ctx context.Context, ledgerID, code string) (domain.StoredRegroupingRule, error)
	ListRegroupingRules(ctx context.Context, ledgerID string, codes []string) ([]domain.StoredRegroupingRule, error)
	SaveRegroupingRun(ctx context.Context, in domain.RegroupingRun, key, requestHash string) (domain.RegroupingRun, error)
	RegroupingRun(ctx context.Context, id string) (domain.RegroupingRun, error)
	MarkRegroupingPosted(ctx context.Context, runID, entryID string) (domain.RegroupingRun, error)
	MarkRegroupingReversed(ctx context.Context, runID, entryID string) (domain.RegroupingRun, error)
}

type OpenItemSource interface {
	List(ctx context.Context, legalEntityID, ledgerID, account string) ([]domain.OpenItem, error)
}

func (a *API) ExpireReopened(ctx context.Context) (int, error) {
	if a.periods == nil {
		return 0, nil
	}
	return a.periods.ExpireReopened(ctx)
}

func (a *API) requireOpenPeriod(r *http.Request, legalEntityID, ledgerID, date string) error {
	day, err := time.Parse(dateLayout, date)
	if err != nil {
		return &domain.Error{Code: domain.CodeValidation, Detail: "posting date must be an ISO date"}
	}
	spans, err := a.store.PeriodSpans(r.Context(), legalEntityID, ledgerID, day)
	if err != nil {
		return err
	}
	span, err := domain.SelectSpan(spans, day)
	if err != nil {
		return err
	}
	_, err = domain.Decide(span.Rows, domain.ModuleGL, "")
	return err
}

func missingSlice(name string) error {
	return httpx.NewProblem(503, "COM-0503", name+" is not configured")
}

func requireKey(key string) error {
	if n := len(key); n < 1 || n > 255 {
		return httpx.NewProblem(400, domain.CodeValidation, "Idempotency-Key header is required (1 to 255 characters)")
	}
	return nil
}

func parseModule(raw string) (domain.Module, error) {
	switch domain.Module(raw) {
	case domain.ModuleAll, domain.ModuleGL, domain.ModuleAP, domain.ModuleAR, domain.ModuleInventory, domain.ModuleAssets,
		domain.ModuleCash, domain.ModuleProduction, domain.ModuleProjects, domain.ModuleTax, domain.ModuleBank, domain.ModulePayroll:
		return domain.Module(raw), nil
	}
	return "", &domain.Error{Code: domain.CodeValidation, Detail: "unknown module"}
}

func parseStatus(raw string) (domain.Status, error) {
	switch domain.Status(raw) {
	case domain.StatusOpen, domain.StatusOnHold, domain.StatusClosed, domain.StatusPermanentlyClosed:
		return domain.Status(raw), nil
	}
	return "", &domain.Error{Code: domain.CodeValidation, Detail: "unknown status"}
}

func (a *API) lockWait() time.Duration {
	if a.cfg.LockWait > 0 {
		return a.cfg.LockWait
	}
	return 5 * time.Second
}

func (a *API) reopenDays() int {
	if a.cfg.ReopenDays > 0 {
		return a.cfg.ReopenDays
	}
	return 3
}

func (a *API) reopenApprovers() int {
	if a.cfg.Approvers >= 2 {
		return a.cfg.Approvers
	}
	return 2
}
