package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/dq"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

// stagedRecordDTO is one line of a wip_prod, fixed_assets or payroll_ytd extract. Each pack reads
// its own fields; amounts are decimal strings and a blank amount is zero.
type stagedRecordDTO struct {
	LegacyID string `json:"legacyId"`
	Currency string `json:"currency"`
	// wip_prod
	ProductionOrder string `json:"productionOrder,omitempty"`
	Item            string `json:"item,omitempty"`
	Material        string `json:"material,omitempty"`
	Labor           string `json:"labor,omitempty"`
	Overhead        string `json:"overhead,omitempty"`
	// fixed_assets
	Asset                   string `json:"asset,omitempty"`
	Book                    string `json:"book,omitempty"`
	Method                  string `json:"method,omitempty"`
	UsefulLifeMonths        int    `json:"usefulLifeMonths,omitempty"`
	RemainingLifeMonths     int    `json:"remainingLifeMonths,omitempty"`
	AcquisitionCost         string `json:"acquisitionCost,omitempty"`
	AccumulatedDepreciation string `json:"accumulatedDepreciation,omitempty"`
	SalvageValue            string `json:"salvageValue,omitempty"`
	NextDepreciation        string `json:"nextDepreciation,omitempty"`
	// payroll_ytd
	Worker     string `json:"worker,omitempty"`
	TaxYear    string `json:"taxYear,omitempty"`
	Gross      string `json:"gross,omitempty"`
	Tax        string `json:"tax,omitempty"`
	Deductions string `json:"deductions,omitempty"`
	Net        string `json:"net,omitempty"`
}

// stagedRunRequest starts a staged pack run. Master is the list the referential rule checks:
// items for wip_prod, depreciation books for fixed_assets, workers for payroll_ytd.
type stagedRunRequest struct {
	SourceSystem     string            `json:"sourceSystem"`
	SourceCompany    string            `json:"sourceCompany"`
	CutoffTS         time.Time         `json:"cutoffTs"`
	FreezeTS         time.Time         `json:"freezeTs"`
	QuarantineMaxPct string            `json:"quarantineMaxPct,omitempty"`
	Master           []string          `json:"master"`
	Records          []stagedRecordDTO `json:"records"`
}

// stagedAmounts parses the named amounts of one record; a blank amount is zero.
func stagedAmounts(legacyID string, fields map[string]string) (map[string]decimal.Decimal, error) {
	out := make(map[string]decimal.Decimal, len(fields))
	for name, v := range fields {
		if strings.TrimSpace(v) == "" {
			out[name] = decimal.Zero
			continue
		}
		d, err := parseMoney(v)
		if err != nil {
			return nil, fmt.Errorf("%w: %s %q of %q %s", domain.ErrInvalid, name, v, legacyID, err)
		}
		out[name] = d
	}
	return out, nil
}

// stagedLines parses the records of pack and applies its suite against master. It returns every
// line, the accepted lines and the quarantine.
func stagedLines(pack domain.Pack, recs []stagedRecordDTO, master map[string]struct{}) ([]domain.StagedLine, []domain.StagedLine, []domain.Quarantined, error) {
	t := strings.TrimSpace
	var all, accepted []domain.StagedLine
	var quarantined []domain.Quarantined
	switch pack {
	case domain.PackWIPProd:
		in := make([]domain.WIPProdLine, 0, len(recs))
		for _, rec := range recs {
			m, err := stagedAmounts(rec.LegacyID, map[string]string{"material": rec.Material, "labor": rec.Labor, "overhead": rec.Overhead})
			if err != nil {
				return nil, nil, nil, err
			}
			w := domain.WIPProdLine{LegacyID: t(rec.LegacyID), ProductionOrder: t(rec.ProductionOrder), Item: t(rec.Item), Currency: t(rec.Currency),
				Material: m["material"], Labor: m["labor"], Overhead: m["overhead"]}
			in = append(in, w)
			all = append(all, w.Staged())
		}
		accepted, quarantined = domain.ValidateWIPProd(in, master)
		var err error
		if accepted, quarantined, err = gateSuite(pack, in, dq.WIPProdRows(in), map[string]map[string]bool{"items": dq.SetOf(master), "iso4217": dq.ISO4217()},
			func(w domain.WIPProdLine) string { return w.LegacyID }, stagedKey, accepted, quarantined, holdStaged); err != nil {
			return nil, nil, nil, err
		}
	case domain.PackFixedAssets:
		in := make([]domain.FixedAssetLine, 0, len(recs))
		for _, rec := range recs {
			m, err := stagedAmounts(rec.LegacyID, map[string]string{"acquisitionCost": rec.AcquisitionCost, "accumulatedDepreciation": rec.AccumulatedDepreciation,
				"salvageValue": rec.SalvageValue, "nextDepreciation": rec.NextDepreciation})
			if err != nil {
				return nil, nil, nil, err
			}
			a := domain.FixedAssetLine{LegacyID: t(rec.LegacyID), Asset: t(rec.Asset), Book: t(rec.Book), Currency: t(rec.Currency),
				Method: strings.ToUpper(t(rec.Method)), UsefulLifeMonths: rec.UsefulLifeMonths, RemainingLifeMonths: rec.RemainingLifeMonths,
				AcquisitionCost: m["acquisitionCost"], AccumulatedDepreciation: m["accumulatedDepreciation"], SalvageValue: m["salvageValue"],
				NextDepreciation: m["nextDepreciation"]}
			in = append(in, a)
			all = append(all, a.Staged())
		}
		accepted, quarantined = domain.ValidateFixedAssets(in, master)
	case domain.PackPayrollYTD:
		in := make([]domain.PayrollYTDLine, 0, len(recs))
		for _, rec := range recs {
			m, err := stagedAmounts(rec.LegacyID, map[string]string{"gross": rec.Gross, "tax": rec.Tax, "deductions": rec.Deductions, "net": rec.Net})
			if err != nil {
				return nil, nil, nil, err
			}
			p := domain.PayrollYTDLine{LegacyID: t(rec.LegacyID), Worker: t(rec.Worker), TaxYear: t(rec.TaxYear), Currency: t(rec.Currency),
				Gross: m["gross"], Tax: m["tax"], Deductions: m["deductions"], Net: m["net"]}
			in = append(in, p)
			all = append(all, p.Staged())
		}
		accepted, quarantined = domain.ValidatePayrollYTD(in, master)
		var err error
		if accepted, quarantined, err = gateSuite(pack, in, dq.PayrollYTDRows(in), map[string]map[string]bool{"workers": dq.SetOf(master), "iso4217": dq.ISO4217()},
			func(p domain.PayrollYTDLine) string { return p.LegacyID }, stagedKey, accepted, quarantined, holdStaged); err != nil {
			return nil, nil, nil, err
		}
	}
	return all, accepted, quarantined, nil
}

// startStagedRun validates a wip_prod, fixed_assets or payroll_ytd extract, quarantines failing
// lines, stages the rest idempotently by legacy_xref and reconciles it (FR-49.1, FR-49.2, FR-49.10,
// FR-49.11; BR-49.6 for fixed_assets). Nothing is posted to production, fixed assets or payroll.
func (s *Server) startStagedRun(w http.ResponseWriter, r *http.Request, pack domain.Pack) {
	spec, _ := domain.StagedSpecFor(pack)
	var req stagedRunRequest
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
		fail(w, r, invalid("sourceCompany is required (DataAreaId or company code)"))
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
	masterList := splitIDs(req.Master)
	if len(masterList) == 0 {
		fail(w, r, invalid("master must list the items, depreciation books or workers the lines are checked against"))
		return
	}
	if len(req.Records) == 0 {
		fail(w, r, invalid("records must hold at least one line"))
		return
	}
	master := make(map[string]struct{}, len(masterList))
	for _, m := range masterList {
		master[m] = struct{}{}
	}
	lines, accepted, quarantined, err := stagedLines(pack, req.Records, master)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := domain.QuarantineShareExceeded(len(quarantined), len(lines), maxPct); err != nil {
		fail(w, r, err)
		return
	}
	run := domain.PackRun{
		RunID: ids.New(), TenantID: p.TenantID, Pack: pack, SourceSystem: sys, SourceCompany: company,
		CutoffTS: req.CutoffTS.UTC(), InputHash: domain.HashStaged(spec, lines), Status: domain.StatusExtracted,
		RecordsIn: len(lines), RecordsQuarantined: len(quarantined), Quarantined: quarantined, Parties: masterList,
	}
	if err := run.Advance(domain.StatusValidated); err != nil {
		fail(w, r, err)
		return
	}
	out, replayed, err := s.store.RunStaged(r.Context(), idem, store.StagedRun{Spec: spec, Run: run, Lines: lines, Accepted: accepted, StartedBy: p.UserID})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusCreated, replayed, toRunDTO(out))
}
