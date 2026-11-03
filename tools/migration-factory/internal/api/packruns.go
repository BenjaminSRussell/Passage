package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

type recordDTO struct {
	LegacyID string `json:"legacyId"`
	Party    string `json:"party"`
	Account  string `json:"account"`
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// extractRecordDTO is a record as an extract sends it: the control fields and, for a run that loads into its
// target, the fields of the receivables row. amount is signed (a credit is negative); the side follows its sign.
type extractRecordDTO struct {
	recordDTO
	Ledger             string  `json:"ledger,omitempty"`
	DocumentNumber     string  `json:"documentNumber,omitempty"`
	CustomerID         string  `json:"customerId,omitempty"`
	Kind               string  `json:"kind,omitempty"`
	DocumentDate       string  `json:"documentDate,omitempty"`
	DueDate            string  `json:"dueDate,omitempty"`
	OriginalAmount     string  `json:"originalAmount,omitempty"`
	CashDiscountDate   *string `json:"cashDiscountDate,omitempty"`
	CashDiscountAmount string  `json:"cashDiscountAmount,omitempty"`
}

// loadRequestDTO asks a run to be staged for loading into a target service.
type loadRequestDTO struct {
	Target        string            `json:"target"`
	LegalEntityID string            `json:"legalEntityId"`
	ImportSource  string            `json:"importSource,omitempty"`
	CustomerIDs   map[string]string `json:"customerIds,omitempty"`
}

type packRunRequest struct {
	SourceSystem     string             `json:"sourceSystem"`
	SourceCompany    string             `json:"sourceCompany"`
	CutoffTS         time.Time          `json:"cutoffTs"`
	FreezeTS         time.Time          `json:"freezeTs"`
	QuarantineMaxPct string             `json:"quarantineMaxPct,omitempty"`
	Parties          []string           `json:"parties"`
	Records          []extractRecordDTO `json:"records"`
	Load             *loadRequestDTO    `json:"load,omitempty"`
}

type totalDTO struct {
	Name            string            `json:"name"`
	Key             map[string]string `json:"key"`
	Source          string            `json:"source"`
	Target          string            `json:"target"`
	Delta           string            `json:"delta"`
	Tolerance       string            `json:"tolerance"`
	ToleranceReason string            `json:"toleranceReason,omitempty"`
	Status          string            `json:"status"`
}

type quarantineDTO struct {
	Seq        int        `json:"seq"`
	LegacyID   string     `json:"legacyId"`
	Rule       string     `json:"rule"`
	Reason     string     `json:"reason"`
	Status     string     `json:"status"`
	Party      string     `json:"party,omitempty"`
	Account    string     `json:"account,omitempty"`
	Dimensions string     `json:"dimensions,omitempty"`
	Currency   string     `json:"currency,omitempty"`
	Amount     string     `json:"amount"`
	Released   *recordDTO `json:"released,omitempty"`
	ReleasedBy string     `json:"releasedBy,omitempty"`
	ReleasedAt *time.Time `json:"releasedAt,omitempty"`
}

type runDTO struct {
	RunID              string    `json:"runId"`
	Pack               string    `json:"pack"`
	SourceSystem       string    `json:"sourceSystem"`
	SourceCompany      string    `json:"sourceCompany"`
	CutoffTS           time.Time `json:"cutoffTs"`
	InputHash          string    `json:"inputHash"`
	Status             string    `json:"status"`
	RecordsIn          int       `json:"recordsIn"`
	RecordsLoaded      int       `json:"recordsLoaded"`
	RecordsQuarantined int       `json:"recordsQuarantined"`
	AlreadyPresent     int       `json:"alreadyPresent"`
	Message            string    `json:"message,omitempty"`
	OpenItemsRunID     string    `json:"openItemsRunId,omitempty"`
	GLRunID            string    `json:"glRunId,omitempty"`
	Version            int       `json:"version"`
	CreatedAt          time.Time `json:"createdAt"`
}

// runDetailDTO is a run with its control totals and quarantined records. MismatchMessages holds
// MIG-1506 for every control account whose open items differ from the migrated GL balance, and
// MIG-1502 for an inventory account that differs from the loaded on-hand value.
type runDetailDTO struct {
	runDTO
	ControlTotals    []totalDTO      `json:"controlTotals"`
	Quarantined      []quarantineDTO `json:"quarantined"`
	MismatchMessages []string        `json:"mismatchMessages,omitempty"`
	// TargetLoad is set when the run was started with a load object (open_items into receivables).
	TargetLoad *targetLoadDTO `json:"targetLoad,omitempty"`
}

func toTotalDTO(t domain.ControlTotal) totalDTO {
	return totalDTO{Name: t.Name, Key: t.Key, Source: t.Source.StringFixed(6), Target: t.Target.StringFixed(6), Delta: t.Delta.StringFixed(6),
		Tolerance: t.Tolerance.Amount.StringFixed(6), ToleranceReason: t.Tolerance.Reason, Status: string(t.Status)}
}

func toRunSummary(run domain.PackRun) runDTO {
	return runDTO{
		RunID: run.RunID, Pack: string(run.Pack), SourceSystem: string(run.SourceSystem), SourceCompany: run.SourceCompany,
		CutoffTS: run.CutoffTS, InputHash: run.InputHash, Status: string(run.Status), RecordsIn: run.RecordsIn,
		RecordsLoaded: run.RecordsLoaded, RecordsQuarantined: run.RecordsQuarantined, AlreadyPresent: run.AlreadyPresent,
		Message: run.Message(), OpenItemsRunID: run.OpenItemsRunID, GLRunID: run.GLRunID, Version: run.Version, CreatedAt: run.CreatedAt,
	}
}

func toRecordDTO(r domain.Record) recordDTO {
	return recordDTO{LegacyID: r.LegacyID, Party: r.Party, Account: r.Account, Currency: r.Currency, Amount: r.Amount.StringFixed(6)}
}

func toQuarantineDTO(q domain.Quarantined) quarantineDTO {
	out := quarantineDTO{Seq: q.Seq, LegacyID: q.Record.LegacyID, Rule: q.Rule, Reason: q.Reason, Status: q.Status,
		Party: q.Record.Party, Account: q.Record.Account, Dimensions: q.Record.Dimensions, Currency: q.Record.Currency, Amount: q.Record.Amount.StringFixed(6),
		ReleasedBy: q.ReleasedBy, ReleasedAt: q.ReleasedAt}
	if q.Released != nil {
		rel := toRecordDTO(*q.Released)
		out.Released = &rel
	}
	return out
}

func toRunDTO(run domain.PackRun) runDetailDTO {
	out := runDetailDTO{runDTO: toRunSummary(run), ControlTotals: []totalDTO{}, Quarantined: []quarantineDTO{}}
	for _, t := range run.Totals {
		out.ControlTotals = append(out.ControlTotals, toTotalDTO(t))
		if t.Name == domain.ControlAccountName && t.Status == domain.StatusMismatch {
			out.MismatchMessages = append(out.MismatchMessages, domain.ControlAccountMessage(t))
		}
		if t.Name == domain.OnhandAccountName && t.Status == domain.StatusMismatch {
			out.MismatchMessages = append(out.MismatchMessages, domain.OnhandAccountMessage(t))
		}
	}
	for _, q := range run.Quarantined {
		out.Quarantined = append(out.Quarantined, toQuarantineDTO(q))
	}
	return out
}

var (
	moneyPattern   = regexp.MustCompile(`^-?\d{1,22}(\.\d{1,6})?$`)
	fineFraction   = regexp.MustCompile(`^-?\d{1,22}\.\d{7,}$`)
	errNotDecimal  = errors.New("is not a decimal of at most 22 integer and 6 decimal digits")
	errTooManyDecs = errors.New("has more than 6 decimal places")
)

// parseMoney validates the raw text before decimal parsing: decimal.NewFromString accepts
// exponent notation, and a huge exponent makes Round and Equal burn CPU and memory.
func parseMoney(raw string) (decimal.Decimal, error) {
	raw = strings.TrimSpace(raw)
	if !moneyPattern.MatchString(raw) {
		if fineFraction.MatchString(raw) {
			return decimal.Decimal{}, errTooManyDecs
		}
		return decimal.Decimal{}, errNotDecimal
	}
	return decimal.NewFromString(raw)
}

// toRecords parses wire records. Amounts are decimal strings; a bad amount is a rule violation.
func toRecords(in []extractRecordDTO) ([]domain.Record, error) {
	out := make([]domain.Record, 0, len(in))
	for _, r := range in {
		amt, err := parseMoney(r.Amount)
		if err != nil {
			return nil, fmt.Errorf("%w: record %q amount %q %s", domain.ErrInvalid, r.LegacyID, r.Amount, err)
		}
		out = append(out, domain.Record{LegacyID: strings.TrimSpace(r.LegacyID), Party: r.Party, Account: r.Account, Currency: r.Currency, Amount: amt})
	}
	return out, nil
}

func keyed(records []domain.Record) []domain.Keyed {
	out := make([]domain.Keyed, len(records))
	for i, r := range records {
		out[i] = domain.Keyed{LegacyID: r.LegacyID, Amount: r.Amount}
	}
	return out
}

// startPackRun starts a run of an available pack; gl_balances and project_wip have their own
// request shapes. For open_items it validates the extract, quarantines failing records, loads the rest
// idempotently by legacy_xref and reconciles (FR-49.2, FR-49.10, FR-49.11).
func (s *Server) startPackRun(w http.ResponseWriter, r *http.Request) {
	pack, err := domain.ParsePack(chi.URLParam(r, "pack"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if !domain.PackAvailable(pack) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnsupported, "pack "+string(pack)+" is not available yet; open_items, gl_balances, onhand, open_orders, bank_balances and project_wip run"))
		return
	}
	switch pack {
	case domain.PackProjectWIP:
		s.startProjectRun(w, r)
		return
	case domain.PackGLBalances:
		s.startGLRun(w, r)
		return
	case domain.PackOnhand:
		s.startOnhandRun(w, r)
		return
	case domain.PackBankBal:
		s.startBankRun(w, r)
		return
	case domain.PackOpenOrders:
		s.startOpenOrdersRun(w, r)
		return
	}
	var req packRunRequest
	idem, err := readCommand(r, &req, packRunBodyLimit)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	sys, err := domain.ParseSourceSystem(req.SourceSystem)
	if err != nil {
		fail(w, r, err)
		return
	}
	company := strings.TrimSpace(req.SourceCompany)
	if company == "" {
		fail(w, r, invalid("sourceCompany is required (DataAreaId or BUKRS)"))
		return
	}
	if req.CutoffTS.IsZero() || req.FreezeTS.IsZero() {
		fail(w, r, invalid("cutoffTs and freezeTs are required"))
		return
	}
	if err := domain.CheckCutoff(req.CutoffTS, req.FreezeTS); err != nil {
		fail(w, r, err)
		return
	}
	maxPct, err := domain.ParseQuarantineMaxPct(req.QuarantineMaxPct)
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(req.Records) == 0 {
		fail(w, r, invalid("records must hold at least one open item"))
		return
	}
	records, err := toRecords(req.Records)
	if err != nil {
		fail(w, r, err)
		return
	}
	loadCfg, extras, err := parseLoad(req.Load, req.Records, sys, company, p)
	if err != nil {
		fail(w, r, err)
		return
	}
	partyIDs := splitIDs(req.Parties)
	parties := make(map[string]struct{}, len(partyIDs))
	for _, id := range partyIDs {
		parties[id] = struct{}{}
	}
	accepted, quarantined := domain.Validate(records, parties)
	if err := domain.QuarantineShareExceeded(len(quarantined), len(records), maxPct); err != nil {
		fail(w, r, err)
		return
	}
	run := domain.PackRun{
		RunID: ids.New(), TenantID: p.TenantID, Pack: pack, SourceSystem: sys, SourceCompany: company,
		CutoffTS: req.CutoffTS.UTC(), InputHash: domain.HashRecords(keyed(records)), Status: domain.StatusExtracted,
		RecordsIn: len(records), RecordsQuarantined: len(quarantined), Quarantined: quarantined, Parties: partyIDs,
	}
	if err := run.Advance(domain.StatusValidated); err != nil {
		fail(w, r, err)
		return
	}
	in := store.OpenItemsRun{Run: run, Records: records, Accepted: accepted, StartedBy: p.UserID}
	if loadCfg != nil {
		in.Target = &store.TargetStage{LegalEntityID: loadCfg.LegalEntityID, ImportSource: loadCfg.ImportSource, Rows: domain.StageRows(accepted, extras, *loadCfg)}
	}
	out, replayed, err := s.store.RunOpenItems(r.Context(), idem, in)
	if err != nil {
		fail(w, r, err)
		return
	}
	dto := toRunDTO(out)
	if in.Target != nil {
		if l, err := s.store.GetTargetLoad(r.Context(), out.RunID); err == nil {
			ld := toTargetLoadDTO(l)
			dto.TargetLoad = &ld
		}
	}
	respond(w, http.StatusCreated, replayed, dto)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.RunFilter{Pack: q.Get("pack"), Status: q.Get("status")}
	if f.Pack != "" {
		if _, err := domain.ParsePack(f.Pack); err != nil {
			fail(w, r, invalid("pack is not one of the nine packs"))
			return
		}
	}
	if f.Status != "" && !validRunStatus(f.Status) {
		fail(w, r, invalid("status is not a pack run status"))
		return
	}
	var err error
	if f.Limit, err = listLimit(r); err != nil {
		fail(w, r, err)
		return
	}
	if f.After, err = after(r); err != nil {
		fail(w, r, err)
		return
	}
	runs, err := s.store.ListRuns(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]runDTO, 0, len(runs))
	for _, run := range runs {
		items = append(items, toRunSummary(run))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func validRunStatus(s string) bool {
	switch domain.RunStatus(s) {
	case domain.StatusExtracted, domain.StatusValidated, domain.StatusLoaded, domain.StatusReconciled, domain.StatusFailed, domain.StatusRolledBack:
		return true
	}
	return false
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := toRunDTO(run)
	if l, err := s.store.GetTargetLoad(r.Context(), id); err == nil {
		dto := toTargetLoadDTO(l)
		out.TargetLoad = &dto
	} else if !errors.Is(err, store.ErrNoTarget) {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (s *Server) controlTotals(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := []totalDTO{}
	for _, t := range run.Totals {
		out = append(out, toTotalDTO(t))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"runId": run.RunID, "status": run.Status, "controlTotals": out})
}

// splitIDs trims ids and drops blanks and repeats, keeping the first-seen order.
func splitIDs(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, id := range in {
		id = strings.TrimSpace(id)
		if _, dup := seen[id]; id == "" || dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
