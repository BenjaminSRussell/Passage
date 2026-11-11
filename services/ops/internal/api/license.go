package api

import (
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
)

func (a *API) now() time.Time {
	if a.cfg.Now != nil {
		return a.cfg.Now()
	}
	return time.Now().UTC()
}

func problem(status int, code, detail string) *httpx.Problem {
	return httpx.NewProblem(status, code, detail)
}

type installLicenseRequest struct {
	// Payload is the base64 of the licence JSON exactly as signed; Signature is the base64 Ed25519 signature over those bytes.
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// installLicense verifies and stores a signed licence (LIC-1804 on a bad signature). If-Match carries the installed version, 0
// when none is installed.
func (a *API) installLicense(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validation("id", "must be the licensed tenant's id"))
		return
	}
	ifMatch, err := strconv.ParseInt(strings.Trim(r.Header.Get("If-Match"), `" `), 10, 64)
	if err != nil || ifMatch < 0 {
		httpx.Fail(w, r, problem(http.StatusPreconditionRequired, domain.CodeLicenseStale, "If-Match with the installed licence version (0 when none is installed) is required"))
		return
	}
	var req installLicenseRequest
	if err := httpx.DecodeLimit(r, &req, 64<<10); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	payload, err1 := base64.StdEncoding.DecodeString(req.Payload)
	sig, err2 := base64.StdEncoding.DecodeString(req.Signature)
	if err1 != nil || err2 != nil || len(payload) == 0 || len(payload) > 16<<10 {
		httpx.Fail(w, r, validation("payload", "payload and signature must be base64"))
		return
	}
	lic, err := domain.VerifyLicense(a.cfg.LicensePublicKey, payload, sig)
	if err != nil {
		httpx.Fail(w, r, store.Problem(err))
		return
	}
	if !strings.EqualFold(lic.TenantID, id) {
		httpx.Fail(w, r, store.Problem(&domain.Error{Code: domain.CodeLicenseInvalid, Detail: "the licence was issued to another tenant"}))
		return
	}
	lic.TenantID = strings.ToLower(lic.TenantID)
	rec, err := a.store.InstallLicense(r.Context(), lic, payload, sig, p.UserID, ifMatch)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	slog.Warn("licence installed", "licensee", lic.TenantID, "version", rec.Version, "expiresAt", lic.ExpiresAt, "installedBy", p.UserID, "correlationId", middleware.GetReqID(r.Context()))
	w.Header().Set("ETag", `"`+strconv.FormatInt(rec.Version, 10)+`"`)
	httpx.JSON(w, http.StatusOK, a.licenseView(rec, -1))
}

type licenseView struct {
	store.LicenseRecord
	Status   domain.LicenseStatus `json:"status"`
	Messages []string             `json:"messages"`
}

// licenseView evaluates the licence today. activeFull is the caller's count of active full users; -1 means unknown and
// counts as within the licence.
func (a *API) licenseView(rec store.LicenseRecord, activeFull int) licenseView {
	count := activeFull
	if count < 0 {
		count = 0
	}
	st, _ := domain.EvaluateLicense(rec.License, count, a.now())
	v := licenseView{LicenseRecord: rec, Status: st, Messages: []string{}}
	for _, c := range st.Codes {
		v.Messages = append(v.Messages, c+": "+domain.LicenseMessage(c, rec.License, count, st))
	}
	return v
}

// getLicense reads the licence and its state. activeFull (query) is the tenant's count of active full users, which the ops
// service does not hold; without it only the date rules apply.
func (a *API) getLicense(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validation("id", "must be the licensed tenant's id"))
		return
	}
	active := -1
	if v := r.URL.Query().Get("activeFull"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 1<<24 {
			httpx.Fail(w, r, validation("activeFull", "must be a non-negative integer"))
			return
		}
		active = n
	}
	rec, err := a.store.License(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(rec.Version, 10)+`"`)
	httpx.JSON(w, http.StatusOK, a.licenseView(rec, active))
}

// writeLicenseMetrics emits passage_license_days_to_expiry per licensed tenant, which the licence expiry alerts read.
func (a *API) writeLicenseMetrics(w io.Writer, r *http.Request) {
	list, err := a.store.Licenses(r.Context())
	if err != nil {
		slog.Error("licence metrics", "err", err)
		return
	}
	_, _ = fmt.Fprint(w, "# HELP passage_license_days_to_expiry Days until a tenant's licence expires (negative once expired).\n# TYPE passage_license_days_to_expiry gauge\n")
	for _, rec := range list {
		st, err := domain.EvaluateLicense(rec.License, 0, a.now())
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(w, "passage_license_days_to_expiry{tenant_id=%q} %d\n", rec.License.TenantID, st.DaysToExpiry)
	}
}

// scorecard measures a failover against the RTO and RPO targets (AC-50.3) from its recorded steps.
func (a *API) scorecard(w http.ResponseWriter, r *http.Request) {
	d, err := a.store.FailoverByEvent(r.Context(), chi.URLParam(r, "eventId"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	steps := make([]domain.StepTiming, 0, len(d.Steps))
	for _, s := range d.Steps {
		steps = append(steps, domain.StepTiming{Action: s.Action, Status: s.Status, StartedAt: s.StartedAt, FinishedAt: s.FinishedAt})
	}
	httpx.JSON(w, http.StatusOK, domain.ScoreFailover(d.Plan, steps, a.cfg.RTOTargetMinutes, a.cfg.RPOTargetSeconds))
}
