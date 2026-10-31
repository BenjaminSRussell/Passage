package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

type openingLineDTO struct {
	LegacyID        string            `json:"legacyId"`
	Category        string            `json:"category"`
	TransactionDate string            `json:"transactionDate"`
	Qty             string            `json:"qty"`
	CostAmount      string            `json:"costAmount"`
	SalesAmount     string            `json:"salesAmount"`
	Currency        string            `json:"currency"`
	WorkerID        string            `json:"workerId,omitempty"`
	ActivityID      string            `json:"activityId,omitempty"`
	Dimensions      map[string]string `json:"dimensions,omitempty"`
	BillingStatus   string            `json:"billingStatus"`
	WIPCost         string            `json:"wipCost"`
	WIPSales        string            `json:"wipSales"`
}

type legacyTotalDTO struct {
	Category string `json:"category"`
	Cost     string `json:"cost"`
	Sales    string `json:"sales"`
	WIPCost  string `json:"wipCost"`
	WIPSales string `json:"wipSales"`
}

type projectExtractDTO struct {
	ProjectID    string           `json:"projectId"`
	LegacyTotals []legacyTotalDTO `json:"legacyTotals"`
	Lines        []openingLineDTO `json:"lines"`
}

type projectRunRequest struct {
	SourceSystem     string              `json:"sourceSystem"`
	SourceCompany    string              `json:"sourceCompany"`
	CutoffTS         time.Time           `json:"cutoffTs"`
	FreezeTS         time.Time           `json:"freezeTs"`
	QuarantineMaxPct string              `json:"quarantineMaxPct,omitempty"`
	Projects         []projectExtractDTO `json:"projects"`
}

// decimals parses named decimal strings; a blank value is zero.
func decimals(what string, named ...[2]string) ([]decimal.Decimal, error) {
	out := make([]decimal.Decimal, len(named))
	for i, n := range named {
		raw := strings.TrimSpace(n[1])
		if raw == "" {
			out[i] = decimal.Zero
			continue
		}
		d, err := parseMoney(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %s %s %q %s", domain.ErrInvalid, what, n[0], n[1], err)
		}
		out[i] = d
	}
	return out, nil
}

// parseOpeningLine converts a wire line. Malformed values (enums, decimals, dates) reject the
// request; business-rule failures quarantine the line.
func parseOpeningLine(projectID string, l openingLineDTO) (domain.OpeningLine, error) {
	what := fmt.Sprintf("line %q", l.LegacyID)
	cat, err := domain.ParseCategory(l.Category)
	if err != nil {
		return domain.OpeningLine{}, fmt.Errorf("%s: %w", what, err)
	}
	bill, err := domain.ParseBilling(l.BillingStatus)
	if err != nil {
		return domain.OpeningLine{}, fmt.Errorf("%s: %w", what, err)
	}
	var date time.Time
	if strings.TrimSpace(l.TransactionDate) != "" {
		if date, err = time.Parse("2006-01-02", strings.TrimSpace(l.TransactionDate)); err != nil {
			return domain.OpeningLine{}, fmt.Errorf("%w: %s transactionDate %q is not YYYY-MM-DD", domain.ErrInvalid, what, l.TransactionDate)
		}
	}
	n, err := decimals(what, [2]string{"qty", l.Qty}, [2]string{"costAmount", l.CostAmount}, [2]string{"salesAmount", l.SalesAmount},
		[2]string{"wipCost", l.WIPCost}, [2]string{"wipSales", l.WIPSales})
	if err != nil {
		return domain.OpeningLine{}, err
	}
	return domain.OpeningLine{LegacyID: strings.TrimSpace(l.LegacyID), ProjectID: projectID, Category: cat, TransactionDate: date,
		Qty: n[0], CostAmount: n[1], SalesAmount: n[2], Currency: strings.TrimSpace(l.Currency), WorkerID: strings.TrimSpace(l.WorkerID),
		ActivityID: strings.TrimSpace(l.ActivityID), Dimensions: l.Dimensions, BillingStatus: bill, WIPCost: n[3], WIPSales: n[4]}, nil
}

// startProjectRun loads project beginning balances (FR-49.12). Each project is reconciled to its
// legacy category totals; a mismatch marks it OPENING_MISMATCH (BR-49.12). Opening lines never
// post to the GL (BR-49.11). Re-running an extract loads 0 new lines (FR-49.10).
func (s *Server) startProjectRun(w http.ResponseWriter, r *http.Request) {
	var req projectRunRequest
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
	var all []domain.OpeningLine
	projects := make([]string, 0, len(req.Projects))
	legacy := make(map[string][]domain.LegacyCategoryTotal, len(req.Projects))
	for _, pr := range req.Projects {
		id := strings.TrimSpace(pr.ProjectID)
		if id == "" {
			fail(w, r, invalid("every project needs a projectId"))
			return
		}
		if _, dup := legacy[id]; dup {
			fail(w, r, invalid(fmt.Sprintf("project %s appears twice", id)))
			return
		}
		projects = append(projects, id)
		legacy[id] = []domain.LegacyCategoryTotal{}
		seen := map[domain.Category]bool{}
		for _, t := range pr.LegacyTotals {
			cat, err := domain.ParseCategory(t.Category)
			if err != nil {
				fail(w, r, fmt.Errorf("project %s legacy total: %w", id, err))
				return
			}
			if seen[cat] {
				fail(w, r, invalid(fmt.Sprintf("project %s has two legacy totals for %s", id, cat)))
				return
			}
			seen[cat] = true
			n, err := decimals("project "+id+" legacy total "+string(cat), [2]string{"cost", t.Cost}, [2]string{"sales", t.Sales},
				[2]string{"wipCost", t.WIPCost}, [2]string{"wipSales", t.WIPSales})
			if err != nil {
				fail(w, r, err)
				return
			}
			legacy[id] = append(legacy[id], domain.LegacyCategoryTotal{Category: cat, Cost: n[0], Sales: n[1], WIPCost: n[2], WIPSales: n[3]})
		}
		for _, l := range pr.Lines {
			line, err := parseOpeningLine(id, l)
			if err != nil {
				fail(w, r, err)
				return
			}
			all = append(all, line)
		}
	}
	if len(all) == 0 {
		fail(w, r, invalid("projects must hold at least one opening line"))
		return
	}
	accepted, issues := domain.ValidateOpeningLines(all, req.CutoffTS)
	if err := domain.QuarantineShareExceeded(len(issues), len(all), maxPct); err != nil {
		fail(w, r, err)
		return
	}
	hashed := make([]domain.Keyed, len(all))
	for i, l := range all {
		hashed[i] = domain.Keyed{LegacyID: l.ProjectID + "/" + l.LegacyID, Amount: l.CostAmount}
	}
	run := domain.PackRun{
		RunID: ids.New(), TenantID: p.TenantID, Pack: domain.PackProjectWIP, SourceSystem: sys, SourceCompany: company,
		CutoffTS: req.CutoffTS.UTC(), InputHash: domain.HashRecords(hashed), Status: domain.StatusExtracted,
		RecordsIn: len(all), RecordsQuarantined: len(issues),
	}
	if err := run.Advance(domain.StatusValidated); err != nil {
		fail(w, r, err)
		return
	}
	out, replayed, err := s.store.RunProjectWIP(r.Context(), idem, store.ProjectWIPRun{
		Run: run, Accepted: accepted, Issues: issues, Projects: projects, Legacy: legacy, StartedBy: p.UserID,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusCreated, replayed, toRunDTO(out))
}

type openingDTO struct {
	RunID     string           `json:"runId"`
	ProjectID string           `json:"projectId"`
	Status    string           `json:"status"`
	Totals    []totalDTO       `json:"totals"`
	LineCount int              `json:"lineCount"`
	Lines     []openingLineOut `json:"lines"`
}

type openingLineOut struct {
	openingLineDTO
	BeginningBalance bool `json:"beginningBalance"`
}

// projectOpening returns a project's opening lines and its reconciliation to legacy (FR-49.12,
// BR-49.12), lines paged by legacy id with limit and after.
func (s *Server) projectOpening(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	project := strings.TrimSpace(chi.URLParam(r, "project"))
	if project == "" {
		fail(w, r, invalid("project is required"))
		return
	}
	f := store.OpeningFilter{RunID: id, ProjectID: project, After: r.URL.Query().Get("after")}
	if f.Limit, err = listLimit(r); err != nil {
		fail(w, r, err)
		return
	}
	open, err := s.store.GetProjectOpening(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := openingDTO{RunID: open.Run.RunID, ProjectID: open.ProjectID, Status: open.Status, Totals: []totalDTO{},
		LineCount: open.LineCount, Lines: []openingLineOut{}}
	for _, t := range open.Totals {
		out.Totals = append(out.Totals, toTotalDTO(t))
	}
	for _, l := range open.Lines {
		out.Lines = append(out.Lines, openingLineOut{BeginningBalance: true, openingLineDTO: openingLineDTO{
			LegacyID: l.LegacyID, Category: string(l.Category), TransactionDate: l.TransactionDate.Format("2006-01-02"),
			Qty: l.Qty.StringFixed(6), CostAmount: l.CostAmount.StringFixed(6), SalesAmount: l.SalesAmount.StringFixed(6),
			Currency: l.Currency, WorkerID: l.WorkerID, ActivityID: l.ActivityID, Dimensions: l.Dimensions,
			BillingStatus: string(l.BillingStatus), WIPCost: l.WIPCost.StringFixed(6), WIPSales: l.WIPSales.StringFixed(6)}})
	}
	httpx.JSON(w, http.StatusOK, out)
}
