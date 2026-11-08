package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	operatingUnitsImport     = "/v1/org/operating-units:import"
	maxOperatingUnitImport   = 1000
	codeOUImportTooLarge     = "ORG-OU-IMPORT-TOO-LARGE"
	operatingUnitImportLimit = "an import takes at most 1000 rows"
)

type importOperatingUnitRow struct {
	Type        string `json:"type"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	LegalEntity string `json:"legalEntity"`
}

type importOperatingUnitsRequest struct {
	Rows []importOperatingUnitRow `json:"rows"`
}

// OperatingUnitImportRowReport is one row of the import report.
type OperatingUnitImportRowReport struct {
	Line   int               `json:"line"`
	Type   string            `json:"type"`
	Code   string            `json:"code"`
	Status string            `json:"status"`
	ID     string            `json:"id,omitempty"`
	Errors map[string]string `json:"errors,omitempty"`
}

// OperatingUnitImportReport is the body of the import response.
type OperatingUnitImportReport struct {
	DryRun    bool                           `json:"dryRun"`
	Applied   bool                           `json:"applied"`
	Created   int                            `json:"created"`
	Unchanged int                            `json:"unchanged"`
	Failed    int                            `json:"failed"`
	Rows      []OperatingUnitImportRowReport `json:"rows"`
}

// importOperatingUnits validates every row, then asks the store to apply them all or none.
func (a *API) importOperatingUnits(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	dryRun := r.URL.Query().Get("dryRun")
	if dryRun != "" && dryRun != "true" && dryRun != "false" {
		badQuery(w, r, "dryRun", "must be true or false")
		return
	}
	var req importOperatingUnitsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(req.Rows) == 0 {
		validationFailed(w, r, map[string]string{"rows": "must hold at least one row"})
		return
	}
	if len(req.Rows) > maxOperatingUnitImport {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeOUImportTooLarge, operatingUnitImportLimit))
		return
	}
	report := OperatingUnitImportReport{DryRun: dryRun == "true", Rows: make([]OperatingUnitImportRowReport, 0, len(req.Rows))}
	var valid []store.OperatingUnitImportRow
	invalid := 0
	for i, in := range req.Rows {
		row := store.OperatingUnitImportRow{Line: i + 1, Type: strings.TrimSpace(in.Type), Code: strings.ToUpper(strings.TrimSpace(in.Code)),
			Name: strings.TrimSpace(in.Name), LegalEntityCode: strings.ToUpper(strings.TrimSpace(in.LegalEntity))}
		if fields := validateImportRow(row); len(fields) > 0 {
			invalid++
			report.Rows = append(report.Rows, OperatingUnitImportRowReport{Line: row.Line, Type: row.Type, Code: row.Code, Status: store.ImportFailed, Errors: fields})
			continue
		}
		valid = append(valid, row)
	}
	results, err := a.store.ImportOperatingUnits(r.Context(), valid, report.DryRun || invalid > 0)
	if err != nil {
		slog.Error("import operating units failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	for _, res := range results {
		report.Rows = append(report.Rows, OperatingUnitImportRowReport{Line: res.Line, Type: res.Type, Code: res.Code, Status: res.Status, ID: res.ID, Errors: nilIfNoErrors(res.Errors)})
	}
	slices.SortFunc(report.Rows, func(x, y OperatingUnitImportRowReport) int { return x.Line - y.Line })
	for _, row := range report.Rows {
		switch row.Status {
		case store.ImportCreated:
			report.Created++
		case store.ImportUnchanged:
			report.Unchanged++
		default:
			report.Failed++
		}
	}
	report.Applied = !report.DryRun && report.Failed == 0
	if report.Applied {
		a.ouCreated.Add(int64(report.Created))
	}
	if report.Failed > 0 || report.DryRun {
		// Nothing was written: a created row is only what the file would create.
		for i := range report.Rows {
			if report.Rows[i].Status == store.ImportCreated {
				report.Rows[i].ID = ""
			}
		}
	}
	httpx.JSON(w, http.StatusOK, report)
}

func nilIfNoErrors(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

func validateImportRow(row store.OperatingUnitImportRow) map[string]string {
	fields := map[string]string{}
	if !slices.Contains(operatingUnitTypes, row.Type) {
		fields["type"] = "must be department, cost_center, business_unit or value_stream"
	}
	if !operatingUnitCode.MatchString(row.Code) {
		fields["code"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9"
	}
	if row.Name == "" || utf8.RuneCountInString(row.Name) > maxNameLength {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxNameLength)
	}
	return fields
}
