package api

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

const (
	dutyIntrastatRead   = "tax.intrastat.read"
	dutyIntrastatMaster = "tax.intrastatmaster.manage"
	dutyIntrastatManage = "tax.intrastat.manage"
	dutyIntrastatExport = "tax.intrastat.export"
)

func (a *API) registerIntrastat(r chi.Router) {
	read, master, manage, export := authz.RequireDuty(dutyIntrastatRead), authz.RequireDuty(dutyIntrastatMaster),
		authz.RequireDuty(dutyIntrastatManage), authz.RequireDuty(dutyIntrastatExport)
	r.With(read).Get("/v1/tax/intrastat/master/{kind}", a.listMaster)
	r.With(master).Put("/v1/tax/intrastat/master/{kind}/{code}", a.putMaster)
	r.With(master).Delete("/v1/tax/intrastat/master/{kind}/{code}", a.deleteMaster)
	r.With(read).Get("/v1/tax/intrastat/journals", a.listJournals)
	r.With(manage).Post("/v1/tax/intrastat/journals", a.createJournal)
	r.With(read).Get("/v1/tax/intrastat/journals/{id}", a.getJournal)
	r.With(manage).Post("/v1/tax/intrastat/journals/{id}/transfer", a.transfer)
	r.With(manage).Post("/v1/tax/intrastat/journals/{id}/validate", a.validateJournal)
	r.With(manage).Post("/v1/tax/intrastat/journals/{id}/compress", a.compress)
	r.With(manage).Post("/v1/tax/intrastat/journals/{id}/correct", a.correct)
	r.With(manage).Put("/v1/tax/intrastat/journals/{id}/lines/{lineNo}", a.putLine)
	r.With(manage).Delete("/v1/tax/intrastat/journals/{id}/lines/{lineNo}", a.deleteLine)
	r.With(export).Get("/v1/tax/intrastat/journals/{id}/export", a.export)
}

func masterKind(r *http.Request) (string, error) {
	kind := chi.URLParam(r, "kind")
	if !domain.ValidMasterKind(kind) {
		return "", invalid("kind", "kind must be one of %v", domain.MasterKinds)
	}
	return kind, nil
}

func (a *API) listMaster(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	kind, err := masterKind(r)
	var out []domain.MasterEntry
	if err == nil {
		out, err = a.config.ListMaster(r.Context(), kind)
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) putMaster(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var e domain.MasterEntry
	if err := httpx.Decode(r, &e); err != nil {
		a.reject(w, r, err)
		return
	}
	kind, err := masterKind(r)
	if err == nil && e.Kind != "" && e.Kind != kind {
		err = invalid("kind", "the kind in the body must match the kind in the path")
	}
	if err == nil {
		var code string
		if code, err = pathCode(r, e.Code); err == nil {
			e.Kind, e.Code = kind, code
			if err = domain.ValidateMasterEntry(e); err == nil {
				err = a.config.PutMaster(r.Context(), e)
			}
		}
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, e)
}

func (a *API) deleteMaster(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	kind, err := masterKind(r)
	if err == nil {
		err = a.config.DeleteMaster(r.Context(), kind, chi.URLParam(r, "code"))
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listJournals(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.ListJournals(r.Context())
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

type createJournalRequest struct {
	Flow       string      `json:"flow"`
	PeriodFrom domain.Date `json:"period_from"`
}

func (a *API) createJournal(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var req createJournalRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	switch {
	case req.Flow != domain.FlowArrival && req.Flow != domain.FlowDispatch:
		a.reject(w, r, invalid("flow", "flow must be %q or %q", domain.FlowArrival, domain.FlowDispatch))
		return
	case req.PeriodFrom.IsZero() || req.PeriodFrom.Day() != 1:
		a.reject(w, r, invalid("period_from", "period_from must be the first day of a month"))
		return
	}
	out, err := a.config.CreateJournal(r.Context(), req.Flow, req.PeriodFrom)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) getJournal(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.GetJournal(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type transferRequest struct {
	Lines []domain.IntrastatLine `json:"lines"`
}

func (a *API) transfer(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var req transferRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	if err := domain.ValidateTransferLines(req.Lines); err != nil {
		a.reject(w, r, err)
		return
	}
	out, err := a.config.Transfer(r.Context(), chi.URLParam(r, "id"), req.Lines)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) validateJournal(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.Validate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) compress(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.Compress(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) correct(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.Correct(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func lineNo(r *http.Request) (int, error) {
	n, err := strconv.Atoi(chi.URLParam(r, "lineNo"))
	if err != nil || n < 1 {
		return 0, invalid("lineNo", "the line number must be 1 or more")
	}
	return n, nil
}

func (a *API) putLine(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var l domain.IntrastatLine
	if err := httpx.Decode(r, &l); err != nil {
		a.reject(w, r, err)
		return
	}
	n, err := lineNo(r)
	if err == nil {
		err = domain.CheckLineShape(l)
	}
	if err == nil {
		l.LineNo = n
		err = a.config.PutLine(r.Context(), chi.URLParam(r, "id"), n, l)
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, l)
}

func (a *API) deleteLine(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	n, err := lineNo(r)
	if err == nil {
		err = a.config.DeleteLine(r.Context(), chi.URLParam(r, "id"), n)
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) export(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	data, err := a.config.Export(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.reject(w, r, err)
		return
	}
	var buf bytes.Buffer
	j := data.Journal
	if err := domain.IntrastatCSV(&buf, j.Flow, data.HomeCountry, j.CorrectionOf != "", j.Lines, data.Master); err != nil {
		a.reject(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="intrastat-%s-%s.csv"`, j.Flow, j.PeriodFrom.Format("2006-01")))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
