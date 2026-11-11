package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/xlsx"
)

// Problem codes of the workbook round trip (FR-38.14).
// maxPlanAmount is the first amount a plan cell cannot hold (numeric(19,4)).
var maxPlanAmount = decimal.New(1, 15)

const (
	codeWorkbookOtherPlan = "BUD-0066" // the workbook was exported from another plan, scenario or fiscal year
	codeExportTooLarge    = "BUD-0067" // the grid holds more cells than one workbook carries
	maxWorkbookErrors     = 100

	xlsxContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	planSheet       = "Plan"
	infoSheet       = "Info"
)

// exportGrid answers the scenario's grid as a workbook (FR-38.14): sheet Plan has one row per account and cost center with the 12
// periods of the fiscal year across, sheet Info says which plan, scenario and version it came from. The grid's ETag is in the header,
// so the edited workbook can be imported with If-Match. The account and costCenter query parameters narrow the export.
func (a *API) exportGrid(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := planPathID(w, r)
	if !ok {
		return
	}
	sid, ok := scenarioPathID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	plan, err := a.plans.GetPlan(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	g, err := a.plans.GetGrid(r.Context(), id, sid, store.GridFilter{Account: q.Get("account"), CostCenter: q.Get("costCenter")})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if g.Truncated {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeExportTooLarge, "the grid holds more cells than one workbook carries: export one account or cost center at a time"))
		return
	}
	raw, err := buildWorkbook(plan, g)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	scenarioName := ""
	for _, s := range plan.Scenarios {
		if s.ID == sid {
			scenarioName = s.Name
		}
	}
	w.Header().Set("Content-Type", xlsxContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, safeFileName(fmt.Sprintf("budget-plan-%d-%s.xlsx", plan.FiscalYear, scenarioName))))
	w.Header().Set("ETag", etag(g.Scenario.Version))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func safeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

func buildWorkbook(plan store.PlanView, g store.Grid) ([]byte, error) {
	periods := domain.Periods(plan.FiscalYear)
	type key struct{ account, center string }
	byRow := map[key]map[string]decimal.Decimal{}
	var order []key
	for _, c := range g.Cells {
		k := key{c.Account, c.CostCenter}
		if byRow[k] == nil {
			byRow[k] = map[string]decimal.Decimal{}
			order = append(order, k)
		}
		byRow[k][c.Period] = c.Amount
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].account != order[j].account {
			return order[i].account < order[j].account
		}
		return order[i].center < order[j].center
	})
	header := []xlsx.Cell{xlsx.Str("Account"), xlsx.Str("Cost center")}
	for _, p := range periods {
		header = append(header, xlsx.Str(p))
	}
	header = append(header, xlsx.Str("Total"))
	rows := [][]xlsx.Cell{header}
	for _, k := range order {
		row := []xlsx.Cell{xlsx.Str(k.account), xlsx.Str(k.center)}
		total := decimal.Zero
		for _, p := range periods {
			v := byRow[k][p]
			total = total.Add(v)
			row = append(row, xlsx.Num(v.String()))
		}
		rows = append(rows, append(row, xlsx.Num(total.String())))
	}
	scenarioName := ""
	for _, s := range plan.Scenarios {
		if s.ID == g.Scenario.ID {
			scenarioName = s.Name
		}
	}
	info := [][]xlsx.Cell{
		{xlsx.Str("Plan"), xlsx.Str(plan.ID)},
		{xlsx.Str("Plan name"), xlsx.Str(plan.Name)},
		{xlsx.Str("Scenario"), xlsx.Str(g.Scenario.ID)},
		{xlsx.Str("Scenario name"), xlsx.Str(scenarioName)},
		{xlsx.Str("Version"), xlsx.Num(fmt.Sprint(g.Scenario.Version))},
		{xlsx.Str("Fiscal year"), xlsx.Num(fmt.Sprint(plan.FiscalYear))},
		{xlsx.Str("Currency"), xlsx.Str(plan.Currency)},
		{},
		{xlsx.Str("Edit the amounts on the Plan sheet and import the workbook again. Add a row for a new account and cost center. A blank amount counts as 0. The Total column is ignored. Rows removed from the workbook are left as they are in the plan; set their amounts to 0 to clear them.")},
	}
	return xlsx.Write([]xlsx.Sheet{{Name: planSheet, Rows: rows}, {Name: infoSheet, Rows: info}})
}

// importResult is the answer of a dry-run import.
type importResult struct {
	Valid  bool            `json:"valid"`
	DryRun bool            `json:"dryRun"`
	Rows   int             `json:"rows"`
	Cells  int             `json:"cells"`
	Total  decimal.Decimal `json:"total"`
}

// importGrid reads an edited workbook of the same plan and scenario and writes its amounts like a grid write (If-Match required,
// Draft plan only). Every problem of the workbook is reported with its cell, so it can be fixed in one pass; nothing is written when
// there is one. With dryRun=true the workbook is checked and nothing is written either way.
func (a *API) importGrid(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := planPathID(w, r)
	if !ok {
		return
	}
	sid, ok := scenarioPathID(w, r)
	if !ok {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if key == "" || utf8.RuneCountInString(key) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "is required and must be at most 255 characters"})
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "body must be an .xlsx workbook of at most 4 MiB"))
		return
	}
	dry := r.URL.Query().Get("dryRun") == "true"
	plan, err := a.plans.GetPlan(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	cells, rows, fields, other, stale := parseWorkbook(r.Context(), body, plan, sid, version)
	if other != "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeWorkbookOtherPlan, other))
		return
	}
	if stale != "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeGridStale, stale))
		return
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if dry {
		total := decimal.Zero
		for _, c := range cells {
			total = total.Add(c.Amount)
		}
		httpx.JSON(w, http.StatusOK, importResult{Valid: true, DryRun: true, Rows: rows, Cells: len(cells), Total: total})
		return
	}
	sum := sha256.Sum256(body)
	a.replayGrid(w, r)(a.plans.UpsertCells(r.Context(), key, hex.EncodeToString(sum[:]), id, sid, version, cells))
}

// parseWorkbook turns a workbook into plan cells. It returns the cells and the number of data rows, the problems keyed by cell
// reference (Plan!C5), or other when the workbook belongs to another plan, scenario or fiscal year.
func parseWorkbook(ctx context.Context, data []byte, plan store.PlanView, scenarioID string, version int64) (cells []domain.PlanCell, rows int, fields map[string]string, other, stale string) {
	fields = map[string]string{}
	sheets, err := xlsx.Read(ctx, data, planSheet, infoSheet)
	if err != nil {
		fields["file"] = "is not a readable .xlsx workbook (" + err.Error() + "); export the plan and edit that file"
		return nil, 0, fields, "", ""
	}
	var info, grid *xlsx.Sheet
	for i := range sheets {
		switch sheets[i].Name {
		case infoSheet:
			info = &sheets[i]
		case planSheet:
			grid = &sheets[i]
		}
	}
	if grid == nil || info == nil {
		fields["file"] = "must have the sheets Plan and Info of an exported workbook"
		return nil, 0, fields, "", ""
	}
	meta := map[string]string{}
	for r := range info.Rows {
		if k := strings.TrimSpace(info.At(r, 0).Text); k != "" {
			meta[k] = strings.TrimSpace(info.At(r, 1).Text)
		}
	}
	switch {
	case meta["Plan"] != plan.ID:
		return nil, 0, nil, "the workbook was exported from another plan: export this plan's scenario and edit that file", ""
	case meta["Scenario"] != scenarioID:
		return nil, 0, nil, "the workbook was exported from another scenario of this plan: import it into its own scenario", ""
	case meta["Fiscal year"] != fmt.Sprint(plan.FiscalYear):
		return nil, 0, nil, fmt.Sprintf("the workbook is for fiscal year %s and the plan for %d", meta["Fiscal year"], plan.FiscalYear), ""
	}
	if meta["Version"] != fmt.Sprint(version) {
		return nil, 0, nil, "", fmt.Sprintf("the workbook was exported at grid version %s and the grid is read for version %d: export it again, or import it with the version it was exported at", meta["Version"], version)
	}
	periods := domain.Periods(plan.FiscalYear)
	if strings.TrimSpace(grid.At(0, 0).Text) != "Account" || strings.TrimSpace(grid.At(0, 1).Text) != "Cost center" {
		fields[planSheet+"!A1"] = "the first row must be Account, Cost center and the 12 periods, as exported"
	}
	for i, p := range periods {
		if strings.TrimSpace(grid.At(0, 2+i).Text) != p {
			fields[planSheet+"!"+xlsx.Ref(0, 2+i)] = "must be the period " + p
		}
	}
	if len(fields) > 0 {
		return nil, 0, fields, "", ""
	}
	seen := map[string]int{}
	add := func(ref, msg string) {
		if len(fields) < maxWorkbookErrors {
			fields[planSheet+"!"+ref] = msg
		} else {
			fields["more"] = fmt.Sprintf("more than %d problems; fix these and import again", maxWorkbookErrors)
		}
	}
	for r := 1; r < len(grid.Rows); r++ {
		account, center := strings.TrimSpace(grid.At(r, 0).Text), strings.TrimSpace(grid.At(r, 1).Text)
		blank := account == "" && center == ""
		for c := 2; c < 2+len(periods); c++ {
			blank = blank && grid.At(r, c).Blank()
		}
		if blank {
			continue
		}
		rows++
		if n := len([]rune(account)); n < 1 || n > 100 {
			add(xlsx.Ref(r, 0), "must be 1 to 100 characters")
		}
		if n := len([]rune(center)); n < 1 || n > 100 {
			add(xlsx.Ref(r, 1), "must be 1 to 100 characters")
		}
		if first, dup := seen[account+"\x00"+center]; dup {
			add(xlsx.Ref(r, 0), fmt.Sprintf("the account and cost center are already in row %d", first))
		}
		seen[account+"\x00"+center] = r + 1
		for i, p := range periods {
			cell := grid.At(r, 2+i)
			amount := decimal.Zero
			if !cell.Blank() {
				if !cell.IsNumber {
					add(xlsx.Ref(r, 2+i), "must be a number")
					continue
				}
				v, err := decimal.NewFromString(cell.Text)
				if err != nil {
					add(xlsx.Ref(r, 2+i), "must be a number")
					continue
				}
				// A spreadsheet stores a typed 0.1 as a double that can print with trailing noise: four decimals plus noise is accepted.
				rounded := v.Round(4)
				if v.Sub(rounded).Abs().GreaterThan(decimal.New(1, -7)) {
					add(xlsx.Ref(r, 2+i), "must have at most 4 decimal places")
					continue
				}
				if rounded.GreaterThanOrEqual(maxPlanAmount) {
					add(xlsx.Ref(r, 2+i), "must be below 1,000,000,000,000,000")
					continue
				}
				amount = rounded
			}
			if amount.Sign() < 0 {
				add(xlsx.Ref(r, 2+i), "must not be negative")
				continue
			}
			cells = append(cells, domain.PlanCell{Account: account, CostCenter: center, Period: p, Amount: amount})
		}
	}
	switch {
	case len(fields) > 0:
		return nil, rows, fields, "", ""
	case rows == 0:
		fields[planSheet] = "holds no rows"
		return nil, 0, fields, "", ""
	case len(cells) > maxGridCells:
		fields[planSheet] = fmt.Sprintf("holds %d cells; one import takes at most %d", len(cells), maxGridCells)
		return nil, rows, fields, "", ""
	}
	return cells, rows, nil, "", ""
}
