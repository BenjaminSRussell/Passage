package api

import (
	"context"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	dutyPackageApply       = "migration.package.apply"
	packagesPath           = "/v1/integration/config-packages"
	CodePackageNotConfig   = "MIG-NOT-CONFIGURED"
	maxPackageBody         = 16 << 20
	maxPackageVersion      = 20
	packageNotConfigDetail = "no configuration package target or signing key is wired for this deployment"
)

var ErrPackagesNotConfigured = errors.New("configuration package backend is not configured")

// PackageBackend is what diff and apply need: the setup data to compare and write, and the tenant signing key.
type PackageBackend struct {
	Target domain.PackageTarget
	Signer domain.Signer
}

type PackageFactory func(ctx context.Context) (PackageBackend, error)

func PackagesNotConfigured(context.Context) (PackageBackend, error) {
	return PackageBackend{}, ErrPackagesNotConfigured
}

// WithPackages wires the package target and signer; the default answers 501 MIG-NOT-CONFIGURED.
func (a *API) WithPackages(f PackageFactory) *API {
	a.packages = f
	return a
}

type packageRequest struct {
	Package   *domain.Package `json:"package"`
	Signature []byte          `json:"signature"`
}

type packageView struct {
	ID            string                 `json:"id"`
	Version       string                 `json:"version"`
	SchemaVersion int                    `json:"schemaVersion"`
	FileCount     int                    `json:"fileCount"`
	Files         []domain.ManifestFile  `json:"files,omitempty"`
	Applied       []store.PackageApplied `json:"applied"`
	CreatedBy     string                 `json:"createdBy"`
	CreatedAt     time.Time              `json:"createdAt"`
}

type diffResponse struct {
	Version       string            `json:"version"`
	SchemaVersion int               `json:"schemaVersion"`
	Counts        store.Counts      `json:"counts"`
	Files         []domain.FileDiff `json:"files"`
}

type applyResponse struct {
	PackageID     string       `json:"packageId"`
	Version       string       `json:"version"`
	SchemaVersion int          `json:"schemaVersion"`
	AppliedAt     time.Time    `json:"appliedAt"`
	Counts        store.Counts `json:"counts"`
}

func (a *API) registerPackages(r chi.Router) {
	r.With(authz.RequireDuty(dutyRun)).Post(packagesPath+":diff", a.diffPackage)
	r.With(authz.RequireDuty(dutyPackageApply)).Post(packagesPath+":apply", a.applyPackage)
	r.With(authz.RequireDuty(dutyRun)).Get(packagesPath, a.listPackages)
	r.With(authz.RequireDuty(dutyRun)).Get(packagesPath+"/{id}", a.getPackage)
}

func diffCounts(d domain.DiffResult) store.Counts {
	return store.Counts{Create: d.Create, Update: d.Update, Unchanged: d.Unchanged}
}

// readPackage decodes and shape-checks the request before any backend is touched.
func (a *API) readPackage(w http.ResponseWriter, r *http.Request) (packageRequest, bool) {
	var req packageRequest
	if err := httpx.DecodeLimit(r, &req, maxPackageBody); err != nil {
		fail(w, r, err)
		return req, false
	}
	fields := map[string]string{}
	if req.Package == nil {
		fields["package"] = "is required"
	} else if v := req.Package.Version; v == "" || utf8.RuneCountInString(v) > maxPackageVersion {
		// checked before any write: config_package.version is varchar(20), so a longer version would fail only
		// after apply had already written to the target
		fields["package.version"] = "is required, at most 20 characters"
	}
	if len(req.Signature) == 0 {
		fields["signature"] = "is required (base64)"
	}
	if len(fields) > 0 {
		fail(w, r, validation(fields))
		return req, false
	}
	return req, true
}

func (a *API) packageBackend(w http.ResponseWriter, r *http.Request) (PackageBackend, bool) {
	b, err := a.packages(r.Context())
	if err != nil {
		a.logFailure(r, "package backend unavailable", err)
		if errors.Is(err, ErrPackagesNotConfigured) {
			fail(w, r, httpx.NewProblem(http.StatusNotImplemented, CodePackageNotConfig, packageNotConfigDetail))
		} else {
			fail(w, r, err)
		}
		return b, false
	}
	return b, true
}

func (a *API) diffPackage(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	req, ok := a.readPackage(w, r)
	if !ok {
		return
	}
	b, ok := a.packageBackend(w, r)
	if !ok {
		return
	}
	d, err := domain.Diff(r.Context(), *req.Package, req.Signature, b.Signer, b.Target, domain.SupportedPackageSchema)
	if err != nil {
		a.packageError(w, r, "package diff failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, diffResponse{Version: req.Package.Version, SchemaVersion: req.Package.SchemaVersion, Counts: diffCounts(d), Files: d.Files})
}

func (a *API) applyPackage(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	key := r.Header.Get(idempotencyHeader)
	if key == "" {
		fail(w, r, validation(map[string]string{idempotencyHeader: "header is required"}))
		return
	}
	req, ok := a.readPackage(w, r)
	if !ok {
		return
	}
	candidate := store.ConfigPackage{Version: req.Package.Version, SchemaVersion: req.Package.SchemaVersion, Manifest: req.Package.Manifest, Signature: req.Signature}
	// A repeated key replays the original answer; the same key with another package is a conflict.
	prior, found, err := a.store.ConfigPackageByKey(r.Context(), key)
	if err != nil {
		a.logFailure(r, "package idempotency lookup failed", err)
		fail(w, r, err)
		return
	}
	if found {
		if replay, ok := replayApply(prior, candidate); ok {
			httpx.JSON(w, http.StatusOK, replay)
			return
		} else if !samePackageRequest(prior, candidate) {
			fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeKeyReuse, "Idempotency-Key was already used with a different request"))
			return
		}
	}
	b, ok := a.packageBackend(w, r)
	if !ok {
		return
	}
	d, err := domain.Apply(r.Context(), *req.Package, req.Signature, b.Signer, b.Target, domain.ApplyOptions{SupportedSchema: domain.SupportedPackageSchema})
	if err != nil {
		a.packageError(w, r, "package apply failed", err)
		return
	}
	saved, created, err := a.store.SaveConfigPackage(r.Context(), candidate, key)
	if err != nil {
		a.logFailure(r, "saving configuration package failed", err)
		fail(w, r, err)
		return
	}
	// a concurrent request with the same key recorded its apply first: answer with that original response
	if replay, ok := replayApply(saved, candidate); !created && ok {
		httpx.JSON(w, http.StatusOK, replay)
		return
	}
	now := time.Now().UTC()
	if saved, err = a.store.AppendPackageApplied(r.Context(), saved.ID, store.PackageApplied{AppliedAt: now, Key: key, DiffCounts: diffCounts(d)}); err != nil {
		a.logFailure(r, "recording package apply failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, applyResponse{PackageID: saved.ID, Version: saved.Version, SchemaVersion: saved.SchemaVersion, AppliedAt: now, Counts: diffCounts(d)})
}

func samePackageRequest(prior, candidate store.ConfigPackage) bool {
	return prior.Version == candidate.Version && prior.SchemaVersion == candidate.SchemaVersion && string(prior.Signature) == string(candidate.Signature)
}

func replayApply(prior, candidate store.ConfigPackage) (applyResponse, bool) {
	if !samePackageRequest(prior, candidate) || len(prior.Applied) == 0 {
		return applyResponse{}, false
	}
	first := prior.Applied[0]
	return applyResponse{PackageID: prior.ID, Version: prior.Version, SchemaVersion: prior.SchemaVersion, AppliedAt: first.AppliedAt, Counts: first.DiffCounts}, true
}

func (a *API) packageError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		// the log carries the code only: package details can name files but never record bodies
		a.logFailure(r, msg+": "+de.Code, nil)
		fail(w, r, problemFrom(err))
		return
	}
	a.logFailure(r, msg, err)
	fail(w, r, err)
}

func (a *API) listPackages(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	pkgs, err := a.store.ListConfigPackages(r.Context(), limit)
	if err != nil {
		a.logFailure(r, "list configuration packages failed", err)
		fail(w, r, err)
		return
	}
	items := make([]packageView, 0, len(pkgs))
	for _, p := range pkgs {
		items = append(items, toPackageView(p, false))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getPackage(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	p, err := a.store.GetConfigPackage(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "get configuration package failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPackageView(p, true))
}

func toPackageView(p store.ConfigPackage, withFiles bool) packageView {
	v := packageView{ID: p.ID, Version: p.Version, SchemaVersion: p.SchemaVersion, FileCount: len(p.Manifest.Files), Applied: p.Applied, CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt}
	if v.Applied == nil {
		v.Applied = []store.PackageApplied{}
	}
	if withFiles {
		v.Files = p.Manifest.Files
	}
	return v
}
