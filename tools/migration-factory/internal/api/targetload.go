package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target"
)

// Problem codes of the target load.
const (
	CodeTargetNotConfigured = "MIG-1520"
	CodeTargetDenied        = "MIG-1521"
	CodeLoadBusy            = "MIG-1522"
	CodeNoTarget            = "MIG-1523"
	CodeTargetRefusedBatch  = "MIG-1524"
	CodeLoadSuperseded      = "MIG-1525"
	CodeImportSourceTaken   = "MIG-1526"
)

// loadLease is how long a load holds its run; every stored batch extends it.
const loadLease = 10 * time.Minute

// Target is the service open items are loaded into; *target.Receivables implements it.
type Target interface {
	Configured() bool
	LoadBatch(ctx context.Context, tenantID, legalEntityID, importSource, idemKey string, rows []domain.ReceivablesRow) ([]target.RowResult, error)
	ImportTotals(ctx context.Context, tenantID, legalEntityID, importSource string) ([]domain.ImportTotals, error)
}

// WithTarget sets the receivables target. Without one (or with an unconfigured one) the load step answers 503 MIG-1520.
func (s *Server) WithTarget(t Target) *Server { s.target = t; return s }

// parseLoad reads the run's load request and the receivables fields of its records. It returns nil when the
// run does not ask for a load.
func parseLoad(l *loadRequestDTO, records []extractRecordDTO, sys domain.SourceSystem, company string, p tenant.Principal) (*domain.TargetConfig, map[string]domain.TargetExtra, error) {
	if l == nil {
		for _, r := range records {
			if r.Ledger != "" || r.VendorID != "" || r.DocumentNumber != "" || r.CustomerID != "" || r.Kind != "" || r.DocumentDate != "" || r.DueDate != "" || r.OriginalAmount != "" {
				return nil, nil, invalid("records carry receivables fields, but the request has no load object")
			}
		}
		return nil, nil, nil
	}
	if l.Target != domain.TargetReceivables {
		return nil, nil, invalid("load.target must be receivables")
	}
	if !ids.Valid(l.LegalEntityID) {
		return nil, nil, invalid("load.legalEntityId must be a UUID")
	}
	le, _ := ids.Canonical(l.LegalEntityID)
	if !tenant.InLegalEntityScope(p, le) {
		return nil, nil, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "load.legalEntityId is outside the caller's legal-entity scope")
	}
	source := strings.TrimSpace(l.ImportSource)
	if source == "" {
		source = domain.DefaultImportSource(sys, company)
	}
	if err := domain.ValidateImportSource(source); err != nil {
		return nil, nil, err
	}
	customers := make(map[string]string, len(l.CustomerIDs))
	for party, id := range l.CustomerIDs {
		if !ids.Valid(id) {
			return nil, nil, invalid(fmt.Sprintf("load.customerIds[%q] must be a UUID", party))
		}
		customers[party] = id
	}
	var payables *domain.PayablesConfig
	if l.Payables != nil {
		payables = &domain.PayablesConfig{Vendors: make(map[string]string, len(l.Payables.VendorIDs))}
		for party, id := range l.Payables.VendorIDs {
			if !ids.Valid(id) {
				return nil, nil, invalid(fmt.Sprintf("load.payables.vendorIds[%q] must be a UUID", party))
			}
			payables.Vendors[party] = id
		}
	}
	extras := make(map[string]domain.TargetExtra, len(records))
	for _, r := range records {
		x := domain.TargetExtra{Ledger: strings.ToUpper(strings.TrimSpace(r.Ledger)), DocumentNumber: strings.TrimSpace(r.DocumentNumber), CustomerID: strings.TrimSpace(r.CustomerID),
			Kind: strings.TrimSpace(r.Kind), DocumentDate: strings.TrimSpace(r.DocumentDate), DueDate: strings.TrimSpace(r.DueDate), CashDiscountDate: r.CashDiscountDate}
		if x.Ledger == domain.LedgerAP && strings.TrimSpace(r.VendorID) != "" {
			x.CustomerID = strings.TrimSpace(r.VendorID)
		}
		if x.Ledger != "" && x.Ledger != domain.LedgerAR && x.Ledger != domain.LedgerAP {
			return nil, nil, invalid(fmt.Sprintf("record %q ledger must be AR or AP", r.LegacyID))
		}
		if r.OriginalAmount != "" {
			v, err := parseMoney(r.OriginalAmount)
			if err != nil {
				return nil, nil, invalid(fmt.Sprintf("record %q originalAmount %q %s", r.LegacyID, r.OriginalAmount, err))
			}
			x.OriginalAmount = &v
		}
		if r.CashDiscountAmount != "" {
			v, err := parseMoney(r.CashDiscountAmount)
			if err != nil {
				return nil, nil, invalid(fmt.Sprintf("record %q cashDiscountAmount %q %s", r.LegacyID, r.CashDiscountAmount, err))
			}
			x.CashDiscountAmount = v
		} else {
			x.CashDiscountAmount = decimal.Zero
		}
		side := domain.LedgerAR
		if x.Ledger == domain.LedgerAP {
			side = domain.LedgerAP
		}
		extras[domain.OpenItemKey(side, strings.TrimSpace(r.LegacyID))] = x
	}
	return &domain.TargetConfig{LegalEntityID: le, ImportSource: source, Customers: customers, Payables: payables}, extras, nil
}

type targetLoadDTO struct {
	RunID         string     `json:"runId"`
	Target        string     `json:"target"`
	LegalEntityID string     `json:"legalEntityId"`
	ImportSource  string     `json:"importSource"`
	Status        string     `json:"status"`
	Clean         bool       `json:"clean"`
	Staged        int        `json:"staged"`
	Created       int        `json:"created"`
	Updated       int        `json:"updated"`
	Unchanged     int        `json:"unchanged"`
	Skipped       int        `json:"skipped"`
	Rejected      int        `json:"rejected"`
	NotAvailable  int        `json:"notAvailable"`
	Pending       int        `json:"pending"`
	Attempts      int        `json:"attempts"`
	LastError     string     `json:"lastError,omitempty"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	Version       int        `json:"version"`
}

type targetRowDTO struct {
	Seq        int    `json:"seq"`
	LegacyID   string `json:"legacyId"`
	Ledger     string `json:"ledger"`
	Status     string `json:"status"`
	Outcome    string `json:"outcome,omitempty"`
	OpenItemID string `json:"openItemId,omitempty"`
	Code       string `json:"code,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Side       string `json:"side,omitempty"`
	Amount     string `json:"amount,omitempty"`
	Currency   string `json:"currency,omitempty"`
	CustomerID string `json:"customerId,omitempty"`
}

// targetLoadDetailDTO is a load with its reconciliation lines (the target.* control totals of the run) and the
// first page of its rejects.
type targetLoadDetailDTO struct {
	targetLoadDTO
	Reconciliation []totalDTO     `json:"reconciliation"`
	Rejects        []targetRowDTO `json:"rejects"`
}

func toTargetLoadDTO(l domain.TargetLoad) targetLoadDTO {
	return targetLoadDTO{RunID: l.RunID, Target: l.Target, LegalEntityID: l.LegalEntityID, ImportSource: l.ImportSource, Status: l.Status, Clean: l.Clean(),
		Staged: l.Staged, Created: l.Created, Updated: l.Updated, Unchanged: l.Unchanged, Skipped: l.Skipped, Rejected: l.Rejected, NotAvailable: l.NotAvailable,
		Pending: l.Pending, Attempts: l.Attempts, LastError: l.LastError, StartedAt: l.StartedAt, FinishedAt: l.FinishedAt, Version: l.Version}
}

func toTargetRowDTO(v domain.TargetRowView) targetRowDTO {
	return targetRowDTO{Seq: v.Seq, LegacyID: v.LegacyID, Ledger: v.Ledger, Status: v.Status, Outcome: v.Outcome, OpenItemID: v.PassageID, Code: v.Code, Detail: v.Detail,
		Side: v.Side, Amount: v.Amount, Currency: v.Currency, CustomerID: v.CustomerID}
}

func failTarget(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNoTarget):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoTarget, "the run was started without a load object, so it has nothing to load"))
	case errors.Is(err, store.ErrLoadBusy):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeLoadBusy,
			"another request is loading this run or another run of the same import source and legal entity; try again when it ends"))
	case errors.Is(err, store.ErrLoadSuperseded):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeLoadSuperseded,
			"a later run of this import source and legal entity has already been loaded; loading this older snapshot would overwrite it, so load the latest run"))
	default:
		fail(w, r, err)
	}
}

// getTargetLoad serves GET /migration/runs/{id}/target-load.
func (s *Server) getTargetLoad(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	l, err := s.store.GetTargetLoad(r.Context(), id)
	if err != nil {
		failTarget(w, r, err)
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	rejects, err := s.store.ListTargetRows(r.Context(), id, store.TargetRowFilter{Status: domain.RowRejected, Limit: 50})
	if err != nil {
		fail(w, r, err)
		return
	}
	na, err := s.store.ListTargetRows(r.Context(), id, store.TargetRowFilter{Status: domain.RowNotAvailable, Limit: 50})
	if err != nil {
		fail(w, r, err)
		return
	}
	out := targetLoadDetailDTO{targetLoadDTO: toTargetLoadDTO(l), Reconciliation: []totalDTO{}, Rejects: []targetRowDTO{}}
	for _, t := range run.Totals {
		if domain.IsTargetTotal(t.Name) {
			out.Reconciliation = append(out.Reconciliation, toTotalDTO(t))
		}
	}
	for _, v := range append(rejects, na...) {
		out.Rejects = append(out.Rejects, toTargetRowDTO(v))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// listTargetRows serves GET /migration/runs/{id}/target-rows.
func (s *Server) listTargetRows(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	f := store.TargetRowFilter{Status: r.URL.Query().Get("status")}
	switch f.Status {
	case "", domain.RowPending, domain.RowLoaded, domain.RowUnchanged, domain.RowSkipped, domain.RowRejected, domain.RowNotAvailable:
	default:
		fail(w, r, invalid("status is not a staged row status"))
		return
	}
	if f.Limit, err = listLimit(r); err != nil {
		fail(w, r, err)
		return
	}
	if raw := r.URL.Query().Get("after"); raw != "" {
		if f.After, err = strconv.Atoi(raw); err != nil || f.After < 0 {
			fail(w, r, invalid("after must be a row sequence number"))
			return
		}
	}
	if _, err := s.store.GetTargetLoad(r.Context(), id); err != nil {
		failTarget(w, r, err)
		return
	}
	rows, err := s.store.ListTargetRows(r.Context(), id, f)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]targetRowDTO, 0, len(rows))
	for _, v := range rows {
		items = append(items, toTargetRowDTO(v))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// runTargetLoad serves POST /migration/runs/{id}/load: send the run's pending rows to receivables in batches of at
// most 1000, store each row's outcome and open-item id, then reconcile the target's totals. It can be repeated: rows
// the target already holds unchanged are not sent again, and rows still pending after an outage are sent next time.
func (s *Server) runTargetLoad(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if s.target == nil || !s.target.Configured() {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeTargetNotConfigured,
			"the receivables target is not configured: set RECEIVABLES_URL and the service signing key (PASSAGE_SERVICE_NAME=migration-factory, PASSAGE_SERVICE_SIGNING_KEY)"))
		return
	}
	ctx := r.Context()
	load, pending, err := s.store.ClaimTargetLoad(ctx, id, p.UserID, loadLease)
	if err != nil {
		failTarget(w, r, err)
		return
	}
	var fin store.TargetFinish
	fatalCode := CodeTargetDenied
	for start := 0; start < len(pending) && fin.Fatal == "" && fin.Unavailable == ""; start += domain.MaxBatchRows {
		chunk := pending[start:min(start+domain.MaxBatchRows, len(pending))]
		outs, err := s.sendChunk(ctx, p.TenantID, load, chunk)
		var terr *target.Error
		switch {
		case err == nil:
		case errors.As(err, &terr) && (errors.Is(terr.Kind, target.ErrUnavailable) || errors.Is(terr.Kind, target.ErrBadAnswer)):
			// Nothing is known about the batch (no answer, or one that does not describe it): its rows stay
			// pending and the next load sends them again; receivables answers rows it already holds UNCHANGED.
			fin.Unavailable = err.Error()
			continue
		case errors.As(err, &terr) && errors.Is(terr.Kind, target.ErrDenied):
			fin.Fatal = err.Error()
			continue
		case errors.As(err, &terr) && errors.Is(terr.Kind, target.ErrRefused):
			// The request as a whole was refused (a 4xx that is not a row result): a defect in the factory or its
			// configuration, not in the rows, so they stay pending for the load after the fix.
			fin.Fatal, fatalCode = err.Error(), CodeTargetRefusedBatch
			continue
		default:
			s.abandon(ctx, id, err)
			httpx.Fail(w, r, err)
			return
		}
		if err := s.store.RecordTargetChunk(ctx, id, loadLease, outs); err != nil {
			// The target holds the batch but the factory could not record it: the rows stay pending and the next
			// load resends them, which receivables answers UNCHANGED with the same open-item ids.
			s.abandon(ctx, id, err)
			httpx.Fail(w, r, err)
			return
		}
	}
	if fin.Fatal == "" && fin.Unavailable == "" {
		actual, err := s.target.ImportTotals(ctx, p.TenantID, load.LegalEntityID, load.ImportSource)
		if err != nil {
			fin.ActualErr = err.Error()
		}
		fin.Actual = actual
	}
	done, _, err := s.store.FinishTargetLoad(ctx, id, fin)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if done.Status == domain.LoadFailed {
		detail := "receivables refused the factory's service token: "
		if fatalCode == CodeTargetRefusedBatch {
			detail = "receivables refused the batch request; its rows stay pending: "
		}
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, fatalCode, detail+done.LastError))
		return
	}
	httpx.JSON(w, http.StatusOK, toTargetLoadDTO(done))
}

// abandon ends a pass that stopped on an unexpected error so the lease does not block the next try.
func (s *Server) abandon(ctx context.Context, runID string, cause error) {
	_, _, _ = s.store.FinishTargetLoad(context.WithoutCancel(ctx), runID, store.TargetFinish{Unavailable: cause.Error(), ActualErr: cause.Error()})
}

// sendChunk sends one batch. The Idempotency-Key is derived from the run and the rows, so the same rows are the
// same request on every try.
func (s *Server) sendChunk(ctx context.Context, tenantID string, load domain.TargetLoad, chunk []store.PendingRow) ([]store.RowOutcome, error) {
	rows := make([]domain.ReceivablesRow, len(chunk))
	hashes := make([]string, len(chunk))
	for i, c := range chunk {
		rows[i], hashes[i] = c.Row, c.Hash
	}
	key := "mf-load-" + load.RunID + "-" + store.HashRequest("chunk", load.ImportSource, []byte(strings.Join(hashes, ",")))[:32]
	results, err := s.target.LoadBatch(ctx, tenantID, load.LegalEntityID, load.ImportSource, key, rows)
	if err != nil {
		return nil, err
	}
	outs := make([]store.RowOutcome, len(chunk))
	for i, res := range results {
		o := store.RowOutcome{Pending: chunk[i], Outcome: res.Outcome, OpenItemID: res.OpenItemID, Code: res.Code, Detail: res.Detail}
		switch res.Outcome {
		case target.OutcomeCreated, target.OutcomeUpdated, target.OutcomeUnchanged:
			if !ids.Valid(res.OpenItemID) {
				o.Outcome, o.Code, o.Detail = target.OutcomeRejected, domain.CodeTargetRefused, "the target accepted the row but sent no open-item id"
			}
		default:
			o.Outcome = target.OutcomeRejected
			if o.Code == "" {
				o.Code = domain.CodeTargetRefused
			}
		}
		outs[i] = o
	}
	return outs, nil
}

// XrefResolver resolves legacy keys through the migration service's cross-reference; *target.Xref implements it.
type XrefResolver interface {
	Configured() bool
	Resolve(ctx context.Context, tenantID, legalEntityID, system, entity string, keys []string) (map[string]string, error)
}

// WithXref sets the cross-reference the open-items load resolves legacy customers through (H-S8-6). Without one, a customer that is
// neither on the record, in load.customerIds nor a UUID is rejected MIG-REF-UNRESOLVED at load, as before.
func (s *Server) WithXref(x XrefResolver) *Server { s.xref = x; return s }

// CodeXrefUnavailable is the cross-reference of the party migration not answering when a run needs it.
const CodeXrefUnavailable = "MIG-1565"

// resolveCustomers fills load.customerIds for the AR records whose legacy party has no Passage customer yet (not on the record,
// not in the map, not a UUID) from the customers the party migration loaded into the run's legal entity: the key is the legacy
// company and the party (company|party, as the migration service keys a customer), else the party alone.
func (s *Server) resolveCustomers(ctx context.Context, tenantID string, cfg *domain.TargetConfig, records []domain.Record, extras map[string]domain.TargetExtra, sys domain.SourceSystem, company string) error {
	if s.xref == nil || !s.xref.Configured() || cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	var keys []string
	for _, rec := range records {
		x := extras[domain.OpenItemKey(rec.Side(), strings.TrimSpace(rec.LegacyID))]
		party := strings.TrimSpace(rec.Party)
		if rec.Side() == domain.LedgerAP || party == "" || x.CustomerID != "" || cfg.Customers[party] != "" || ids.Valid(party) || seen[party] {
			continue
		}
		seen[party] = true
		keys = append(keys, company+"|"+party, party)
	}
	if len(keys) == 0 {
		return nil
	}
	found, err := s.xref.Resolve(ctx, tenantID, cfg.LegalEntityID, target.MigrationSystem(string(sys)), "customer", keys)
	if err != nil {
		return httpx.NewProblem(http.StatusBadGateway, CodeXrefUnavailable, "the party migration's cross-reference could not be read to resolve the legacy customers: "+err.Error())
	}
	for party := range seen {
		if id := found[company+"|"+party]; ids.Valid(id) {
			cfg.Customers[party] = id
		} else if id := found[party]; ids.Valid(id) {
			cfg.Customers[party] = id
		}
	}
	return nil
}
