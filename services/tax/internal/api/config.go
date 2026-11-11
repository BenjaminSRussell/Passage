package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/store"
)

const (
	dutyConfigRead   = "tax.config.read"
	dutyConfigManage = "tax.config.manage"
	dutyVATReturn    = "tax.vatreturn.read"

	codeNotFound = "COM-0404"
)

// ConfigStore is the persisted tax configuration, the VAT return data and the Intrastat data.
type ConfigStore interface {
	ListJurisdictions(ctx context.Context) ([]domain.Jurisdiction, error)
	PutJurisdiction(ctx context.Context, j domain.Jurisdiction) error
	DeleteJurisdiction(ctx context.Context, code string) error
	ListCodes(ctx context.Context) ([]domain.ConfigCode, error)
	PutCode(ctx context.Context, c domain.ConfigCode) error
	DeleteCode(ctx context.Context, code string) error
	ListGroups(ctx context.Context, kind string) ([]domain.Group, error)
	PutGroup(ctx context.Context, g domain.Group) error
	DeleteGroup(ctx context.Context, kind, code string) error
	LoadConfig(ctx context.Context) (domain.Config, error)
	ListIntrastatProducts(ctx context.Context) ([]domain.IntrastatProduct, error)
	PutIntrastatProduct(ctx context.Context, p domain.IntrastatProduct) error
	DeleteIntrastatProduct(ctx context.Context, productID string) error
	StageMovements(ctx context.Context, movements []domain.Movement) (int, error)
	ListMovements(ctx context.Context, status string) ([]domain.Movement, error)
	TransferMovements(ctx context.Context, journalID string, d domain.MovementDefaults) (store.MovementTransfer, error)
	RecordWithholding(ctx context.Context, entries []domain.WithholdingEntry) (int, error)
	RunRemittance(ctx context.Context, period domain.Period) (store.RemittanceRun, error)
	ListRemittanceRuns(ctx context.Context) ([]store.RemittanceRun, error)
	GetRemittanceRun(ctx context.Context, id string) (store.RemittanceRun, error)
	ListWithholdingCertificates(ctx context.Context, partyRef string) ([]domain.WithholdingCertificate, error)
	ListRecoveryPolicies(ctx context.Context) ([]domain.RecoveryPolicy, error)
	PutRecoveryPolicy(ctx context.Context, r domain.RecoveryPolicy) error
	DeleteRecoveryPolicy(ctx context.Context, code, itemGroup string, validFrom domain.Date) error
	Reclaim(ctx context.Context, period domain.Period) (domain.Reclaim, error)
	ListCertificates(ctx context.Context) ([]domain.Certificate, error)
	PutCertificate(ctx context.Context, c domain.Certificate) error
	DeleteCertificate(ctx context.Context, number string) error

	GetEntitySetting(ctx context.Context) (store.EntitySetting, error)
	PutEntitySetting(ctx context.Context, e store.EntitySetting) error
	ListBoxes(ctx context.Context) ([]domain.Box, error)
	PutBoxes(ctx context.Context, boxes []domain.Box) error
	VATReturn(ctx context.Context, period domain.Period) (domain.VATReturn, error)
	ESL(ctx context.Context, period domain.Period) (domain.ESL, error)

	ListMaster(ctx context.Context, kind string) ([]domain.MasterEntry, error)
	PutMaster(ctx context.Context, e domain.MasterEntry) error
	DeleteMaster(ctx context.Context, kind, code string) error
	ImportMaster(ctx context.Context, entries []domain.MasterEntry, overwrite bool) (store.ImportResult, error)
	CreateJournal(ctx context.Context, flow string, periodFrom domain.Date) (store.Journal, error)
	ListJournals(ctx context.Context) ([]store.Journal, error)
	GetJournal(ctx context.Context, id string) (store.Journal, error)
	Transfer(ctx context.Context, id string, lines []domain.IntrastatLine) (store.TransferResult, error)
	Validate(ctx context.Context, id string) (store.Journal, error)
	Compress(ctx context.Context, id string) (store.Journal, error)
	PutLine(ctx context.Context, id string, lineNo int, l domain.IntrastatLine) error
	DeleteLine(ctx context.Context, id string, lineNo int) error
	Correct(ctx context.Context, id string) (store.Journal, error)
	Export(ctx context.Context, id string) (store.ExportData, error)
}

// WithConfigStore enables the configuration, VAT return and Intrastat routes.
func (a *API) WithConfigStore(c ConfigStore) *API {
	a.config = c
	return a
}

func (a *API) registerConfig(r chi.Router) {
	read, manage, vat := authz.RequireDuty(dutyConfigRead), authz.RequireDuty(dutyConfigManage), authz.RequireDuty(dutyVATReturn)
	r.With(read).Get("/v1/tax/jurisdictions", a.listJurisdictions)
	r.With(manage).Put("/v1/tax/jurisdictions/{code}", a.putJurisdiction)
	r.With(manage).Delete("/v1/tax/jurisdictions/{code}", a.deleteJurisdiction)
	r.With(read).Get("/v1/tax/codes", a.listCodes)
	r.With(manage).Put("/v1/tax/codes/{code}", a.putCode)
	r.With(manage).Delete("/v1/tax/codes/{code}", a.deleteCode)
	r.With(read).Get("/v1/tax/groups/{kind}", a.listGroups)
	r.With(manage).Put("/v1/tax/groups/{kind}/{code}", a.putGroup)
	r.With(manage).Delete("/v1/tax/groups/{kind}/{code}", a.deleteGroup)
	r.With(read).Get("/v1/tax/config", a.getConfig)
	r.With(read).Get("/v1/tax/recovery-policies", a.listRecovery)
	r.With(manage).Put("/v1/tax/recovery-policies/{code}", a.putRecovery)
	r.With(manage).Delete("/v1/tax/recovery-policies/{code}", a.deleteRecovery)
	r.With(vat).Get("/v1/tax/vat-reclaim", a.vatReclaim)
	r.With(read).Get("/v1/tax/exemption-certificates", a.listCertificates)
	r.With(manage).Put("/v1/tax/exemption-certificates/{number}", a.putCertificate)
	r.With(manage).Delete("/v1/tax/exemption-certificates/{number}", a.deleteCertificate)
	r.With(read).Get("/v1/tax/entity-settings", a.getEntitySettings)
	r.With(manage).Put("/v1/tax/entity-settings", a.putEntitySettings)
	r.With(read).Get("/v1/tax/vat-boxes", a.listBoxes)
	r.With(manage).Put("/v1/tax/vat-boxes", a.putBoxes)
	r.With(vat).Get("/v1/tax/vat-return", a.vatReturn)
	r.With(vat).Get("/v1/tax/esl", a.esl)
	r.With(read).Get("/v1/tax/vat-layouts", a.listLayouts)
	r.With(manage).Post("/v1/tax/vat-layouts/{country}/apply", a.applyLayout)
	a.registerIntrastat(r)
	a.registerWithholdingRemittance(r)
}

func (a *API) configReady(w http.ResponseWriter, r *http.Request) bool {
	if !validTenant(w, r) {
		return false
	}
	if a.config == nil {
		fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, codeNotConfigured, "the tax configuration store is not configured for this service"))
		return false
	}
	return true
}

// pathCode reads the code of the path and refuses a body that names a different one.
func pathCode(r *http.Request, bodyCode string) (string, error) {
	code := chi.URLParam(r, "code")
	if bodyCode != "" && bodyCode != code {
		return "", invalid("code", "the code in the body must match the code in the path")
	}
	return code, nil
}

func (a *API) listJurisdictions(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.ListJurisdictions(r.Context())
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) putJurisdiction(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var j domain.Jurisdiction
	if err := httpx.Decode(r, &j); err != nil {
		a.reject(w, r, err)
		return
	}
	code, err := pathCode(r, j.Code)
	if err == nil {
		j.Code = code
		err = a.config.PutJurisdiction(r.Context(), j)
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, j)
}

func (a *API) deleteJurisdiction(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	if err := a.config.DeleteJurisdiction(r.Context(), chi.URLParam(r, "code")); err != nil {
		a.reject(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listCodes(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.ListCodes(r.Context())
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) putCode(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var c domain.ConfigCode
	if err := httpx.Decode(r, &c); err != nil {
		a.reject(w, r, err)
		return
	}
	code, err := pathCode(r, c.Code.Code)
	if err == nil {
		c.Code.Code = code
		if err = domain.ValidateConfigCode(c); err == nil {
			err = a.config.PutCode(r.Context(), c)
		}
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (a *API) deleteCode(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	if err := a.config.DeleteCode(r.Context(), chi.URLParam(r, "code")); err != nil {
		a.reject(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func groupKind(r *http.Request) (string, error) {
	kind := chi.URLParam(r, "kind")
	if kind != domain.KindParty && kind != domain.KindItem {
		return "", invalid("kind", "kind must be %q or %q", domain.KindParty, domain.KindItem)
	}
	return kind, nil
}

func (a *API) listGroups(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	kind, err := groupKind(r)
	var out []domain.Group
	if err == nil {
		out, err = a.config.ListGroups(r.Context(), kind)
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) putGroup(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var g domain.Group
	if err := httpx.Decode(r, &g); err != nil {
		a.reject(w, r, err)
		return
	}
	kind, err := groupKind(r)
	if err == nil && g.Kind != "" && g.Kind != kind {
		err = invalid("kind", "the kind in the body must match the kind in the path")
	}
	if err == nil {
		var code string
		if code, err = pathCode(r, g.Code); err == nil {
			g.Kind, g.Code = kind, code
			err = a.config.PutGroup(r.Context(), g)
		}
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, g)
}

func (a *API) deleteGroup(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	kind, err := groupKind(r)
	if err == nil {
		err = a.config.DeleteGroup(r.Context(), kind, chi.URLParam(r, "code"))
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getConfig returns the stored configuration in the shape the calculate endpoint takes inline.
func (a *API) getConfig(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	cfg, err := a.config.LoadConfig(r.Context())
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, cfg)
}

func (a *API) getEntitySettings(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.GetEntitySetting(r.Context())
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) putEntitySettings(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var e store.EntitySetting
	if err := httpx.Decode(r, &e); err != nil {
		a.reject(w, r, err)
		return
	}
	switch {
	case !domain.ValidCountry(e.HomeCountry):
		a.reject(w, r, invalid("home_country", "home_country must be a 2-letter ISO 3166 code in capitals"))
		return
	case len(e.VATID) > 20:
		a.reject(w, r, invalid("vat_id", "vat_id is at most 20 characters"))
		return
	}
	if err := a.config.PutEntitySetting(r.Context(), e); err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, e)
}

func (a *API) listBoxes(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.ListBoxes(r.Context())
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

type boxesRequest struct {
	Boxes []domain.Box `json:"boxes"`
}

func (a *API) putBoxes(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var req boxesRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	if err := domain.ValidateBoxes(req.Boxes); err != nil {
		a.reject(w, r, err)
		return
	}
	if err := a.config.PutBoxes(r.Context(), req.Boxes); err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": req.Boxes})
}

func periodParam(r *http.Request) (domain.Period, error) {
	var from, to domain.Date
	if err := parseDate(r, "period_from", &from); err != nil {
		return domain.Period{}, err
	}
	if err := parseDate(r, "period_to", &to); err != nil {
		return domain.Period{}, err
	}
	if to.Before(from.Time) {
		return domain.Period{}, invalid("period_to", "period_to may not precede period_from")
	}
	return domain.Period{From: from, To: to}, nil
}

func parseDate(r *http.Request, name string, out *domain.Date) error {
	raw := r.URL.Query().Get(name)
	t, err := time.Parse(dateLayout, raw)
	if err != nil {
		return invalid(name, "%s is required as YYYY-MM-DD", name)
	}
	*out = domain.Date{Time: t}
	return nil
}

func (a *API) vatReturn(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	period, err := periodParam(r)
	var out domain.VATReturn
	if err == nil {
		out, err = a.config.VATReturn(r.Context(), period)
	}
	if err == nil {
		if format := r.URL.Query().Get("format"); format != "" {
			var file domain.FilingFile
			if file, err = domain.FormatVATReturn(format, out, r.URL.Query().Get("period_key")); err == nil {
				writeFile(w, file)
				return
			}
		}
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func writeFile(w http.ResponseWriter, f domain.FilingFile) {
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, f.Filename))
	_, _ = w.Write(f.Body)
}

func (a *API) esl(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	period, err := periodParam(r)
	var out domain.ESL
	if err == nil {
		out, err = a.config.ESL(r.Context(), period)
	}
	if err == nil {
		if format := r.URL.Query().Get("format"); format != "" {
			var file domain.FilingFile
			if file, err = domain.FormatESL(format, out); err == nil {
				writeFile(w, file)
				return
			}
		}
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listCertificates(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.ListCertificates(r.Context())
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) putCertificate(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var c domain.Certificate
	if err := httpx.Decode(r, &c); err != nil {
		a.reject(w, r, err)
		return
	}
	number := chi.URLParam(r, "number")
	if c.Number != "" && c.Number != number {
		a.reject(w, r, invalid("number", "the number in the body must match the number in the path"))
		return
	}
	c.Number = number
	if err := a.config.PutCertificate(r.Context(), c); err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (a *API) deleteCertificate(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	if err := a.config.DeleteCertificate(r.Context(), chi.URLParam(r, "number")); err != nil {
		a.reject(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listLayouts(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": domain.Layouts()})
}

type applyLayoutRequest struct {
	// Mapping gives, per box of the layout, the tax codes and directions that feed it; a payable box takes none.
	Mapping map[string][]domain.BoxCode `json:"mapping"`
}

// applyLayout replaces the legal entity's boxes with a national layout, the tenant's own tax codes mapped to its boxes.
func (a *API) applyLayout(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var req applyLayoutRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	layout, ok := domain.LayoutFor(chi.URLParam(r, "country"))
	if !ok {
		a.reject(w, r, invalid("country", "no layout ships for %q", chi.URLParam(r, "country")))
		return
	}
	boxes, err := domain.BoxesFromLayout(layout, req.Mapping)
	if err == nil {
		err = a.config.PutBoxes(r.Context(), boxes)
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": boxes})
}

func (a *API) listRecovery(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.ListRecoveryPolicies(r.Context())
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) putRecovery(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var p domain.RecoveryPolicy
	if err := httpx.Decode(r, &p); err != nil {
		a.reject(w, r, err)
		return
	}
	code, err := pathCode(r, p.Code)
	if err == nil {
		p.Code = code
		err = a.config.PutRecoveryPolicy(r.Context(), p)
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (a *API) deleteRecovery(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var from domain.Date
	err := parseDate(r, "valid_from", &from)
	if err == nil {
		err = a.config.DeleteRecoveryPolicy(r.Context(), chi.URLParam(r, "code"), r.URL.Query().Get("item_group"), from)
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) vatReclaim(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	period, err := periodParam(r)
	var out domain.Reclaim
	if err == nil {
		out, err = a.config.Reclaim(r.Context(), period)
	}
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
