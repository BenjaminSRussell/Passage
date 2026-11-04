package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

// onhandLineDTO is one legacy on-hand line at (item, inventory dimension).
type onhandLineDTO struct {
	LegacyID  string `json:"legacyId"`
	Item      string `json:"item"`
	Site      string `json:"site"`
	Warehouse string `json:"warehouse"`
	Location  string `json:"location,omitempty"`
	Batch     string `json:"batch,omitempty"`
	Serial    string `json:"serial,omitempty"`
	Unit      string `json:"unit"`
	Currency  string `json:"currency"`
	Quantity  string `json:"quantity"`
	Value     string `json:"value"`
}

// onhandRunRequest starts an onhand run. Items is the item master the referential rule checks
// against. GLRunID and InventoryAccount optionally name the gl_balances run of the same company
// and the inventory GL account to reconcile with the loaded value (BR-49.3); they go together.
type onhandRunRequest struct {
	SourceSystem     string          `json:"sourceSystem"`
	SourceCompany    string          `json:"sourceCompany"`
	CutoffTS         time.Time       `json:"cutoffTs"`
	FreezeTS         time.Time       `json:"freezeTs"`
	QuarantineMaxPct string          `json:"quarantineMaxPct,omitempty"`
	Items            []string        `json:"items"`
	GLRunID          string          `json:"glRunId,omitempty"`
	InventoryAccount string          `json:"inventoryAccount,omitempty"`
	Records          []onhandLineDTO `json:"records"`
}

// toOnhandLines parses the wire lines. A quantity or value that is not a decimal, or has more
// than six decimal places, rejects the request; rule failures (blank id, unknown item, sign,
// serial, currency) quarantine the line.
func toOnhandLines(in []onhandLineDTO) ([]domain.OnhandLine, error) {
	out := make([]domain.OnhandLine, 0, len(in))
	for _, rec := range in {
		qty, err := parseMoney(rec.Quantity)
		if err != nil {
			return nil, fmt.Errorf("%w: quantity %q of %q %s", domain.ErrInvalid, rec.Quantity, rec.LegacyID, err)
		}
		val, err := parseMoney(rec.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: value %q of %q %s", domain.ErrInvalid, rec.Value, rec.LegacyID, err)
		}
		t := strings.TrimSpace
		out = append(out, domain.OnhandLine{LegacyID: t(rec.LegacyID), Item: t(rec.Item), Site: t(rec.Site), Warehouse: t(rec.Warehouse),
			Location: t(rec.Location), Batch: t(rec.Batch), Serial: t(rec.Serial), Unit: t(rec.Unit), Currency: t(rec.Currency),
			Quantity: qty, Value: val})
	}
	return out, nil
}

// startOnhandRun validates an onhand extract, quarantines failing lines, loads the rest
// idempotently by legacy_xref as opening stock and reconciles it, and the inventory account of
// the named gl_balances run (FR-49.1, FR-49.2, FR-49.10, FR-49.11, BR-49.3).
func (s *Server) startOnhandRun(w http.ResponseWriter, r *http.Request) {
	var req onhandRunRequest
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
		fail(w, r, invalid("sourceCompany is required (DataAreaId or WERKS company code)"))
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
	glRun, account := strings.TrimSpace(req.GLRunID), strings.TrimSpace(req.InventoryAccount)
	if glRun != "" && !ids.Valid(glRun) {
		fail(w, r, invalid("glRunId must be a UUID"))
		return
	}
	if (glRun == "") != (account == "") {
		fail(w, r, invalid("glRunId and inventoryAccount go together: the run reconciles that GL account (BR-49.3)"))
		return
	}
	master := splitIDs(req.Items)
	if len(master) == 0 {
		fail(w, r, invalid("items must list the item master the lines are checked against"))
		return
	}
	if len(req.Records) == 0 {
		fail(w, r, invalid("records must hold at least one line"))
		return
	}
	lines, err := toOnhandLines(req.Records)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make(map[string]struct{}, len(master))
	for _, it := range master {
		items[it] = struct{}{}
	}
	accepted, quarantined := domain.ValidateOnhand(lines, items)
	if err := domain.QuarantineShareExceeded(len(quarantined), len(lines), maxPct); err != nil {
		fail(w, r, err)
		return
	}
	run := domain.PackRun{
		RunID: ids.New(), TenantID: p.TenantID, Pack: domain.PackOnhand, SourceSystem: sys, SourceCompany: company,
		CutoffTS: req.CutoffTS.UTC(), InputHash: domain.HashOnhand(lines), Status: domain.StatusExtracted,
		RecordsIn: len(lines), RecordsQuarantined: len(quarantined), Quarantined: quarantined, Parties: master,
	}
	if err := run.Advance(domain.StatusValidated); err != nil {
		fail(w, r, err)
		return
	}
	out, replayed, err := s.store.RunOnhand(r.Context(), idem, store.OnhandRun{
		Run: run, Lines: lines, Accepted: accepted, GLRunID: glRun, Account: account, StartedBy: p.UserID,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusCreated, replayed, toRunDTO(out))
}
