// Package outputs renders a stored run as a file. The HTTP download of a run's
// output and the scheduled firing both use it, so the same run gives the same
// bytes however it is asked for (FR-15.6).
package outputs

import (
	"fmt"
	"strings"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

// Format is one rendered file format of a run.
type Format struct {
	ContentType string
	Render      func(domain.Definition, []domain.Cell, domain.Stamp) ([]byte, error)
}

// Formats are the formats rendered so far (FR-15.6, PRD §2.15.15). All of them
// render the same stored cells and stamp, so the files of one run agree. pdf is
// not here: it needs the Chromium renderer sidecar (NOT-IMPLEMENTED.md).
var Formats = map[string]Format{
	"csv":  {"text/csv; charset=utf-8", domain.RenderCSV},
	"html": {"text/html; charset=utf-8", domain.RenderHTML},
	"xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", domain.RenderXLSX},
}

// Stamp is the header and footer data of a run's output files.
func Stamp(run store.Run) domain.Stamp {
	return domain.Stamp{
		RunID: run.ID, DefinitionCode: run.DefinitionCode, DefinitionVersion: run.DefinitionVersion,
		LedgerID: stampLedger(run), Period: fmt.Sprintf("%04d-%02d", run.PeriodYear, run.PeriodNo), CurrencyType: run.CurrencyType,
		HWM: run.HWMByLedger, ResultHash: run.ResultHash, GeneratedAt: run.StartedAt, GeneratedBy: run.RequestedBy,
	}
}

// stampLedger is the ledger line of an output's header: the run's ledger, or
// every ledger of a consolidated run with its label, in column order.
func stampLedger(run store.Run) string {
	if len(run.Consolidation) == 0 {
		return run.LedgerID
	}
	parts := make([]string, len(run.Consolidation))
	for i, l := range run.Consolidation {
		parts[i] = fmt.Sprintf("%s (%s)", l.LedgerID, l.Label)
	}
	return strings.Join(parts, ", ")
}
