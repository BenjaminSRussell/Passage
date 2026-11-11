package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target"
)

// Problem codes of the payables load (49-3).
const (
	CodePayablesNotConfigured = "MIG-1550"
	CodePayablesDenied        = "MIG-1551"
	CodePayablesRefusedBatch  = "MIG-1552"
	CodeNoPayablesLoad        = "MIG-1553"
)

// maxRefusalRounds bounds how often one chunk is resent after payables refused rows of it; every round removes at
// least one row, so this only stops a payables that keeps naming new ones.
const maxRefusalRounds = 50

// PayablesTarget is the service the AP lines are loaded into; *target.Payables implements it.
type PayablesTarget interface {
	Configured() bool
	LoadBatch(ctx context.Context, tenantID, legalEntityID, batchKey string, items []domain.PayablesRow) (target.BatchResult, error)
	BatchTotals(ctx context.Context, tenantID, legalEntityID, importSource string) (domain.PayablesTotals, error)
}

// WithPayables sets the payables target. Without one (or an unconfigured one) the AP load answers 503 MIG-1550.
func (s *Server) WithPayables(t PayablesTarget) *Server { s.payables = t; return s }

type payablesLoadDTO struct {
	RunID         string     `json:"runId"`
	Target        string     `json:"target"`
	LegalEntityID string     `json:"legalEntityId"`
	ImportSource  string     `json:"importSource"`
	Status        string     `json:"status"`
	Clean         bool       `json:"clean"`
	Staged        int        `json:"staged"`
	Loaded        int        `json:"loaded"`
	Unchanged     int        `json:"unchanged"`
	Skipped       int        `json:"skipped"`
	Rejected      int        `json:"rejected"`
	Pending       int        `json:"pending"`
	Attempts      int        `json:"attempts"`
	LastError     string     `json:"lastError,omitempty"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	Version       int        `json:"version"`
}

func toPayablesLoadDTO(l domain.PayablesLoad) payablesLoadDTO {
	return payablesLoadDTO{RunID: l.RunID, Target: domain.TargetPayables, LegalEntityID: l.LegalEntityID, ImportSource: l.ImportSource, Status: l.Status, Clean: l.Clean(),
		Staged: l.Staged, Loaded: l.Loaded, Unchanged: l.Unchanged, Skipped: l.Skipped, Rejected: l.Rejected, Pending: l.Pending, Attempts: l.Attempts,
		LastError: l.LastError, StartedAt: l.StartedAt, FinishedAt: l.FinishedAt, Version: l.Version}
}

// payablesLoadDetailDTO is the AP load with its reconciliation lines (the target.payables.* control totals of the run)
// and the first page of its rejected rows.
type payablesLoadDetailDTO struct {
	payablesLoadDTO
	Reconciliation []totalDTO     `json:"reconciliation"`
	Rejects        []targetRowDTO `json:"rejects"`
}

func failPayables(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNoPayablesLoad) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoPayablesLoad, "the run was started without load.payables, so it has no AP lines to load"))
		return
	}
	failTarget(w, r, err)
}

// getPayablesLoad serves GET /migration/runs/{id}/payables-load.
func (s *Server) getPayablesLoad(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	l, err := s.store.GetPayablesLoad(r.Context(), id)
	if err != nil {
		failPayables(w, r, err)
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	rows, err := s.store.ListTargetRows(r.Context(), id, store.TargetRowFilter{Status: domain.RowRejected, Limit: 50})
	if err != nil {
		fail(w, r, err)
		return
	}
	out := payablesLoadDetailDTO{payablesLoadDTO: toPayablesLoadDTO(l), Reconciliation: []totalDTO{}, Rejects: []targetRowDTO{}}
	for _, t := range run.Totals {
		if strings.HasPrefix(t.Name, "target.payables.") {
			out.Reconciliation = append(out.Reconciliation, toTotalDTO(t))
		}
	}
	for _, v := range rows {
		if v.Ledger == domain.LedgerAP {
			out.Rejects = append(out.Rejects, toTargetRowDTO(v))
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

// runPayablesLoad serves POST /migration/runs/{id}/load-payables: send the run's pending AP rows to payables as
// opening batches of at most 1000, store each batch, then read the batches back and reconcile their totals. Payables
// takes a batch all or nothing, so a refusal that names rows marks those rows REJECTED and the rest of the chunk is
// sent again at once. It can be repeated: rows payables already holds are not sent again, and rows still pending
// after an outage are sent next time (the same rows under the same key are a replay, not a second batch).
func (s *Server) runPayablesLoad(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if s.payables == nil || !s.payables.Configured() {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodePayablesNotConfigured,
			"the payables target is not configured: set PAYABLES_URL and the service signing key (PASSAGE_SERVICE_NAME=migration-factory, PASSAGE_SERVICE_SIGNING_KEY)"))
		return
	}
	ctx := r.Context()
	load, pending, err := s.store.ClaimPayablesLoad(ctx, id, p.UserID, loadLease)
	if err != nil {
		failPayables(w, r, err)
		return
	}
	var fin store.PayablesFinish
	fatalCode := CodePayablesDenied
	for start := 0; start < len(pending) && fin.Fatal == "" && fin.Unavailable == ""; start += domain.MaxPayablesBatchItems {
		chunk := pending[start:min(start+domain.MaxPayablesBatchItems, len(pending))]
		code, err := s.sendPayablesChunk(ctx, p.TenantID, load, chunk, &fin)
		if err != nil {
			s.abandonPayables(ctx, id, err)
			httpx.Fail(w, r, err)
			return
		}
		if code != "" {
			fatalCode = code
		}
	}
	if fin.Fatal == "" && fin.Unavailable == "" {
		actual, err := s.payables.BatchTotals(ctx, p.TenantID, load.LegalEntityID, load.ImportSource)
		if err != nil {
			fin.ActualErr = err.Error()
		}
		fin.Actual = actual
	}
	done, _, err := s.store.FinishPayablesLoad(ctx, id, fin)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if done.Status == domain.LoadFailed {
		detail := "payables refused the factory's service token: "
		if fatalCode == CodePayablesRefusedBatch {
			detail = "payables refused the batch request; its rows stay pending: "
		}
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, fatalCode, detail+done.LastError))
		return
	}
	httpx.JSON(w, http.StatusOK, toPayablesLoadDTO(done))
}

func (s *Server) abandonPayables(ctx context.Context, runID string, cause error) {
	_, _, _ = s.store.FinishPayablesLoad(context.WithoutCancel(ctx), runID, store.PayablesFinish{Unavailable: cause.Error(), ActualErr: cause.Error()})
}

// sendPayablesChunk sends one chunk, resending it without the rows payables refuses. It records its outcome in fin
// (Fatal or Unavailable stop the load) and returns the problem code to answer a fatal stop with. A non-nil error is
// unexpected and abandons the load.
func (s *Server) sendPayablesChunk(ctx context.Context, tenantID string, load domain.PayablesLoad, chunk []store.PendingPayable, fin *store.PayablesFinish) (string, error) {
	rows := chunk
	for round := 0; len(rows) > 0; round++ {
		if round >= maxRefusalRounds {
			fin.Fatal = fmt.Sprintf("payables kept refusing rows after %d attempts", maxRefusalRounds)
			return CodePayablesRefusedBatch, nil
		}
		items := make([]domain.PayablesRow, len(rows))
		hashes := make([]string, len(rows))
		for i, c := range rows {
			items[i], hashes[i] = c.Row, c.Hash
		}
		key := domain.PayablesBatchKey(load.ImportSource, hashes)
		res, err := s.payables.LoadBatch(ctx, tenantID, load.LegalEntityID, key, items)
		var terr *target.Error
		var refused *target.RowsRefused
		switch {
		case err == nil:
			if err := s.store.RecordPayablesBatch(ctx, load.RunID, loadLease, store.PayablesBatchSent{BatchKey: key, BatchID: res.ID, Replayed: res.Replayed, Rows: rows}); err != nil {
				return "", err
			}
			return "", nil
		case errors.As(err, &refused):
			bad := make([]store.PayablesReject, 0, len(refused.Rows))
			drop := map[int]bool{}
			for row, why := range refused.Rows {
				bad = append(bad, store.PayablesReject{Seq: rows[row-1].Seq, Code: refused.Code, Detail: why})
				drop[row-1] = true
			}
			sort.Slice(bad, func(i, j int) bool { return bad[i].Seq < bad[j].Seq })
			if err := s.store.RejectPayablesRows(ctx, load.RunID, loadLease, bad); err != nil {
				return "", err
			}
			kept := make([]store.PendingPayable, 0, len(rows)-len(drop))
			for i, c := range rows {
				if !drop[i] {
					kept = append(kept, c)
				}
			}
			rows = kept
		case errors.As(err, &terr) && (errors.Is(terr.Kind, target.ErrUnavailable) || errors.Is(terr.Kind, target.ErrBadAnswer)):
			// Nothing is known about the batch: its rows stay pending and the next load sends them again; payables
			// answers the same key and content as a replay.
			fin.Unavailable = err.Error()
			return "", nil
		case errors.As(err, &terr) && errors.Is(terr.Kind, target.ErrDenied):
			fin.Fatal = err.Error()
			return CodePayablesDenied, nil
		case errors.As(err, &terr) && errors.Is(terr.Kind, target.ErrRefused):
			// The whole request was refused (control totals that do not add up, a changed batch under an old key):
			// a defect in the factory, so the rows stay pending for the load after the fix.
			fin.Fatal = err.Error()
			return CodePayablesRefusedBatch, nil
		default:
			return "", err
		}
	}
	return "", nil
}
