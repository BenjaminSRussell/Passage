package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
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

// Problem codes of the opening load (H-S9-1, H-S10-1).
const (
	CodeOpeningNotConfigured = "MIG-1560"
	CodeOpeningDenied        = "MIG-1561"
	CodeOpeningRefusedBatch  = "MIG-1562"
	CodeNoOpeningLoad        = "MIG-1563"
	CodeOpeningNotLoadable   = "MIG-1564"
)

// StockTarget is the inventory ledger the staged opening stock is loaded into; *target.Inventory implements it.
type StockTarget interface {
	Configured() bool
	LoadBatch(ctx context.Context, tenantID, legalEntityID, batchKey, postingDate, currency string, items []domain.StockItem) (target.OpeningBatchResult, error)
	BatchTotals(ctx context.Context, tenantID, legalEntityID string, keep func(key string) bool) (domain.OpeningTotals, error)
	OnHand(ctx context.Context, tenantID, legalEntityID string, products []string) (map[string]decimal.Decimal, error)
}

// BankTarget is treasury, where the staged opening bank balances are loaded; *target.Treasury implements it.
type BankTarget interface {
	Configured() bool
	LoadBatch(ctx context.Context, tenantID, legalEntityID, batchKey, asOf string, items []domain.BankItem) (target.OpeningBatchResult, error)
	BatchTotals(ctx context.Context, tenantID, legalEntityID string, keep func(key string) bool) (domain.OpeningTotals, error)
}

// WithInventory sets the inventory target. Without one the onhand load answers 503 MIG-1560.
func (s *Server) WithInventory(t StockTarget) *Server { s.inventory = t; return s }

// WithTreasury sets the treasury target. Without one the bank_balances load answers 503 MIG-1560.
func (s *Server) WithTreasury(t BankTarget) *Server { s.treasury = t; return s }

type openingLoadRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	// PostingDate is the date of the opening journals (onhand), AsOf the date of the balances (bank_balances); both are
	// the cutover date and either may be sent for either pack.
	PostingDate string `json:"postingDate"`
	AsOf        string `json:"asOf"`
	// Products maps a legacy item to the Passage product id and BankAccounts a legacy bank account to the treasury bank
	// account id; an id that is already a UUID needs no entry.
	Products     map[string]string `json:"products"`
	BankAccounts map[string]string `json:"bankAccounts"`
}

type openingLoadDTO struct {
	RunID         string     `json:"runId"`
	Target        string     `json:"target"`
	LegalEntityID string     `json:"legalEntityId"`
	Status        string     `json:"status"`
	Clean         bool       `json:"clean"`
	Staged        int        `json:"staged"`
	Loaded        int        `json:"loaded"`
	Rejected      int        `json:"rejected"`
	Pending       int        `json:"pending"`
	Attempts      int        `json:"attempts"`
	LastError     string     `json:"lastError,omitempty"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	Version       int        `json:"version"`
}

func toOpeningLoadDTO(l domain.OpeningLoad) openingLoadDTO {
	return openingLoadDTO{RunID: l.RunID, Target: l.Target, LegalEntityID: l.LegalEntityID, Status: l.Status, Clean: l.Clean(), Staged: l.Staged, Loaded: l.Loaded,
		Rejected: l.Rejected, Pending: l.Pending, Attempts: l.Attempts, LastError: l.LastError, StartedAt: l.StartedAt, FinishedAt: l.FinishedAt, Version: l.Version}
}

type openingRejectDTO struct {
	LegacyID string `json:"legacyId"`
	Code     string `json:"code"`
	Detail   string `json:"detail"`
}

type openingLoadDetailDTO struct {
	openingLoadDTO
	Reconciliation []totalDTO         `json:"reconciliation"`
	Rejects        []openingRejectDTO `json:"rejects"`
}

func failOpeningLoad(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNoOpeningLoad):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoOpeningLoad, "the run has not been loaded: post to opening-load first"))
	case errors.Is(err, store.ErrNotLoadable):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeOpeningNotLoadable, err.Error()))
	case errors.Is(err, store.ErrLoadBusy):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeLoadBusy, "another request is loading this run or another run of the same pack and company; try again when it ends"))
	default:
		fail(w, r, err)
	}
}

// getOpeningLoad serves GET /migration/runs/{id}/opening-load.
func (s *Server) getOpeningLoad(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	l, err := s.store.GetOpeningLoad(r.Context(), id)
	if err != nil {
		failOpeningLoad(w, r, err)
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	rejects, err := s.store.ListOpeningRejects(r.Context(), id, 50)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := openingLoadDetailDTO{openingLoadDTO: toOpeningLoadDTO(l), Reconciliation: []totalDTO{}, Rejects: make([]openingRejectDTO, 0, len(rejects))}
	for _, t := range run.Totals {
		if strings.HasPrefix(t.Name, "target.onhand.") || strings.HasPrefix(t.Name, "target.bank_balances.") {
			out.Reconciliation = append(out.Reconciliation, toTotalDTO(t))
		}
	}
	for _, v := range rejects {
		out.Rejects = append(out.Rejects, openingRejectDTO(v))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// openingChunk is one batch to send: the staged balances in it, their items and the key it goes under.
type openingChunk struct {
	pending  []store.PendingOpening
	stock    []domain.StockItem
	bank     []domain.BankItem
	hashes   []string
	currency string
	// planned chunks come from opening_load_batch: they keep their key and date, and are not planned again.
	planned  bool
	fixedKey string
	date     string
}

func (c openingChunk) key(run store.OpeningRun) string {
	if c.fixedKey != "" {
		return c.fixedKey
	}
	return domain.OpeningBatchKey(run.Pack, run.System, run.Company, c.currency, c.hashes)
}

// plannedChunks rebuilds the chunks of planned batches from the items stored with the plan.
func plannedChunks(planned []store.PlannedOpening) ([]openingChunk, error) {
	var out []openingChunk
	for _, p := range planned {
		c := openingChunk{currency: p.Currency, planned: true, fixedKey: p.Key, date: p.PostingDate}
		var err error
		if p.Currency != "" {
			err = json.Unmarshal(p.Payload, &c.stock)
		} else {
			err = json.Unmarshal(p.Payload, &c.bank)
		}
		if err != nil {
			return nil, fmt.Errorf("planned batch %s: %w", p.Key, err)
		}
		for i := range c.stock {
			c.stock[i].Currency = p.Currency
		}
		for _, id := range p.BalanceIDs {
			c.pending = append(c.pending, store.PendingOpening{BalanceID: id})
		}
		if len(c.pending) != len(c.stock)+len(c.bank) {
			return nil, fmt.Errorf("planned batch %s holds %d rows for %d items", p.Key, len(c.pending), len(c.stock)+len(c.bank))
		}
		for range c.pending {
			c.hashes = append(c.hashes, "")
		}
		out = append(out, c)
	}
	return out, nil
}

func (c openingChunk) without(drop map[int]bool) openingChunk {
	out := openingChunk{currency: c.currency}
	for i, p := range c.pending {
		if drop[i] {
			continue
		}
		out.pending = append(out.pending, p)
		if len(c.stock) > 0 {
			out.stock = append(out.stock, c.stock[i])
			out.hashes = append(out.hashes, c.stock[i].Hash())
		}
		if len(c.bank) > 0 {
			out.bank = append(out.bank, c.bank[i])
			out.hashes = append(out.hashes, c.bank[i].Hash())
		}
	}
	return out
}

// runOpeningLoad serves POST /migration/runs/{id}/opening-load: send the staged balances of an onhand run to the
// inventory ledger (one OPENING_BALANCE batch per currency and 1000 lines) or of a bank_balances run to treasury (one
// batch per 1000 accounts), then read the target's batches back and reconcile their totals. A target takes a batch all or
// nothing, so a refusal that names rows marks those rows REJECTED and the rest of the chunk is sent again at once. The
// call can be repeated: balances the target already holds are not sent again, balances still pending after an outage
// are sent next time (the same rows under the same key are a replay, not a second batch).
func (s *Server) runOpeningLoad(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req openingLoadRequest
	if err := httpx.DecodeLimit(r, &req, packRunBodyLimit); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	date := req.PostingDate
	if date == "" {
		date = req.AsOf
	}
	switch {
	case !ids.Valid(req.LegalEntityID):
		fail(w, r, invalid("legalEntityId must be a UUID"))
		return
	case func() bool { _, err := time.Parse(time.DateOnly, date); return err != nil }():
		fail(w, r, invalid("postingDate (or asOf) must be the cutover date, YYYY-MM-DD"))
		return
	case !tenant.InLegalEntityScope(p, req.LegalEntityID):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside your scope"))
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if (domain.TargetOfPack(run.Pack) == domain.TargetInventory && (s.inventory == nil || !s.inventory.Configured())) ||
		(domain.TargetOfPack(run.Pack) == domain.TargetTreasury && (s.treasury == nil || !s.treasury.Configured())) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeOpeningNotConfigured,
			"the "+domain.TargetOfPack(run.Pack)+" target is not configured: set INVENTORY_URL or TREASURY_URL and the service signing key (PASSAGE_SERVICE_NAME=migration-factory, PASSAGE_SERVICE_SIGNING_KEY)"))
		return
	}
	ctx := r.Context()
	load, opened, planned, pending, err := s.store.ClaimOpeningLoad(ctx, id, req.LegalEntityID, p.UserID, loadLease)
	if err != nil {
		failOpeningLoad(w, r, err)
		return
	}
	chunks, unmapped := buildOpeningChunks(opened, pending, req)
	// A batch planned by an earlier call whose outcome is unknown goes first, unchanged (same key, same items, same date): the target
	// answers it as a replay if it took it, so rows it already holds are never sent under a new key.
	resend, err := plannedChunks(planned)
	if err != nil {
		s.abandonOpening(ctx, id, err)
		httpx.Fail(w, r, err)
		return
	}
	chunks = append(resend, chunks...)
	var fin store.OpeningFinish
	fatalCode := CodeOpeningDenied
	if len(unmapped) > 0 {
		if err := s.store.RejectOpeningRows(ctx, id, load.LegalEntityID, loadLease, unmapped); err != nil {
			s.abandonOpening(ctx, id, err)
			httpx.Fail(w, r, err)
			return
		}
	}
	for _, c := range chunks {
		if fin.Fatal != "" || fin.Unavailable != "" {
			break
		}
		code, err := s.sendOpeningChunk(ctx, p.TenantID, load, opened, date, c, &fin)
		if err != nil {
			s.abandonOpening(ctx, id, err)
			httpx.Fail(w, r, err)
			return
		}
		if code != "" {
			fatalCode = code
		}
	}
	if fin.Fatal == "" && fin.Unavailable == "" {
		fin.Actual, err = s.openingTotals(ctx, p.TenantID, load.LegalEntityID, opened)
		if err != nil {
			fin.ActualErr = err.Error()
		} else if opened.Pack == domain.PackOnhand {
			// BR-49.3 against the ledger's own inquiry (H-S9-2): read back what is on hand for every product the batches loaded.
			if fin.OnHand, err = s.onHandReadBack(ctx, p.TenantID, load.LegalEntityID); err != nil {
				fin.ActualErr = "the on-hand read-back failed: " + err.Error()
			}
		}
	}
	done, _, err := s.store.FinishOpeningLoad(ctx, id, fin)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if done.Status == domain.LoadFailed {
		detail := "the " + load.Target + " service refused the factory's service token: "
		if fatalCode == CodeOpeningRefusedBatch {
			detail = "the " + load.Target + " service refused the batch request; its rows stay pending: "
		}
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, fatalCode, detail+done.LastError))
		return
	}
	httpx.JSON(w, http.StatusOK, toOpeningLoadDTO(done))
}

func (s *Server) abandonOpening(ctx context.Context, runID string, cause error) {
	_, _, _ = s.store.FinishOpeningLoad(context.WithoutCancel(ctx), runID, store.OpeningFinish{Unavailable: cause.Error(), ActualErr: cause.Error()})
}

func (s *Server) onHandReadBack(ctx context.Context, tenantID, legalEntityID string) (map[string]decimal.Decimal, error) {
	loaded, err := s.store.OpeningOnHandExpected(ctx, legalEntityID)
	if err != nil {
		return nil, err
	}
	products := make([]string, 0, len(loaded))
	for p := range loaded {
		products = append(products, p)
	}
	sort.Strings(products)
	return s.inventory.OnHand(ctx, tenantID, legalEntityID, products)
}

func (s *Server) openingTotals(ctx context.Context, tenantID, legalEntityID string, run store.OpeningRun) (domain.OpeningTotals, error) {
	keep := func(key string) bool { return domain.IsOpeningBatchKey(run.Pack, run.System, run.Company, key) }
	if run.Pack == domain.PackOnhand {
		return s.inventory.BatchTotals(ctx, tenantID, legalEntityID, keep)
	}
	return s.treasury.BatchTotals(ctx, tenantID, legalEntityID, keep)
}

// buildOpeningChunks maps the pending balances onto batch items (balances with no mapping come back as rejects) and
// groups them: an inventory batch holds one currency, both targets at most 1000 items.
func buildOpeningChunks(run store.OpeningRun, pending []store.PendingOpening, req openingLoadRequest) ([]openingChunk, []store.OpeningReject) {
	var rejects []store.OpeningReject
	byCurrency := map[string]*openingChunk{}
	var order []string
	var chunks []openingChunk
	flush := func(cur string) {
		if c := byCurrency[cur]; c != nil && len(c.pending) > 0 {
			chunks = append(chunks, *c)
			delete(byCurrency, cur)
		}
	}
	for _, pb := range pending {
		var cur, hash, code, why string
		var stock domain.StockItem
		var bank domain.BankItem
		if pb.Onhand != nil {
			stock, code, why = domain.BuildStockItem(*pb.Onhand, req.Products)
			cur, hash = stock.Currency, stock.Hash()
		} else {
			bank, code, why = domain.BuildBankItem(*pb.Bank, req.BankAccounts)
			cur, hash = "", bank.Hash()
		}
		if code != "" {
			rejects = append(rejects, store.OpeningReject{BalanceID: pb.BalanceID, Code: code, Detail: why})
			continue
		}
		c := byCurrency[cur]
		if c == nil {
			c = &openingChunk{currency: cur}
			byCurrency[cur] = c
			order = append(order, cur)
		}
		c.pending = append(c.pending, pb)
		c.hashes = append(c.hashes, hash)
		if pb.Onhand != nil {
			c.stock = append(c.stock, stock)
		} else {
			c.bank = append(c.bank, bank)
		}
		if len(c.pending) == domain.MaxOpeningBatchItems {
			flush(cur)
		}
	}
	sort.Strings(order)
	seen := map[string]bool{}
	for _, cur := range order {
		if !seen[cur] {
			seen[cur] = true
			flush(cur)
		}
	}
	return chunks, rejects
}

// sendOpeningChunk sends one chunk, resending it without the rows the target refuses. It records its outcome in fin
// (Fatal or Unavailable stop the load) and returns the problem code to answer a fatal stop with. A non-nil error is
// unexpected and abandons the load.
func (s *Server) sendOpeningChunk(ctx context.Context, tenantID string, load domain.OpeningLoad, run store.OpeningRun, date string, c openingChunk, fin *store.OpeningFinish) (string, error) {
	for round := 0; len(c.pending) > 0; round++ {
		if round >= maxRefusalRounds {
			fin.Fatal = fmt.Sprintf("the %s service kept refusing rows after %d attempts", load.Target, maxRefusalRounds)
			return CodeOpeningRefusedBatch, nil
		}
		key := c.key(run)
		sendDate := date
		if c.date != "" {
			sendDate = c.date
		}
		if !c.planned {
			payload := any(c.bank)
			if run.Pack == domain.PackOnhand {
				payload = c.stock
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				return "", err
			}
			plan := store.PlannedOpening{Key: key, Currency: c.currency, PostingDate: sendDate, Payload: raw}
			for _, p := range c.pending {
				plan.BalanceIDs = append(plan.BalanceIDs, p.BalanceID)
			}
			if err := s.store.PlanOpeningBatch(ctx, load.RunID, load.LegalEntityID, loadLease, plan); err != nil {
				return "", err
			}
			c.planned = true
		}
		var res target.OpeningBatchResult
		var err error
		if run.Pack == domain.PackOnhand {
			res, err = s.inventory.LoadBatch(ctx, tenantID, load.LegalEntityID, key, sendDate, c.currency, c.stock)
		} else {
			res, err = s.treasury.LoadBatch(ctx, tenantID, load.LegalEntityID, key, sendDate, c.bank)
		}
		var terr *target.Error
		var refused *target.RowsRefused
		switch {
		case err == nil:
			sent := store.OpeningBatchSent{BatchKey: key, Replayed: res.Replayed}
			for _, p := range c.pending {
				sent.BalanceIDs = append(sent.BalanceIDs, p.BalanceID)
			}
			return "", s.store.RecordOpeningBatch(ctx, load.RunID, load.LegalEntityID, loadLease, sent)
		case errors.As(err, &refused):
			bad := make([]store.OpeningReject, 0, len(refused.Rows))
			drop := map[int]bool{}
			for row, why := range refused.Rows {
				bad = append(bad, store.OpeningReject{BalanceID: c.pending[row-1].BalanceID, Code: refused.Code, Detail: why})
				drop[row-1] = true
			}
			sort.Slice(bad, func(i, j int) bool { return bad[i].BalanceID < bad[j].BalanceID })
			if err := s.store.RejectOpeningRows(ctx, load.RunID, load.LegalEntityID, loadLease, bad); err != nil {
				return "", err
			}
			// The target stored nothing (all or nothing): the plan is dropped and the rest is planned again under its own key.
			if err := s.store.DropOpeningPlan(ctx, load.RunID, key); err != nil {
				return "", err
			}
			c = c.without(drop)
			c.planned, c.fixedKey = false, ""
		case errors.As(err, &terr) && (errors.Is(terr.Kind, target.ErrUnavailable) || errors.Is(terr.Kind, target.ErrBadAnswer)):
			fin.Unavailable = err.Error()
			return "", nil
		case errors.As(err, &terr) && errors.Is(terr.Kind, target.ErrDenied):
			fin.Fatal = err.Error()
			return CodeOpeningDenied, nil
		case errors.As(err, &terr) && errors.Is(terr.Kind, target.ErrRefused):
			fin.Fatal = err.Error()
			return CodeOpeningRefusedBatch, nil
		default:
			return "", err
		}
	}
	return "", nil
}
