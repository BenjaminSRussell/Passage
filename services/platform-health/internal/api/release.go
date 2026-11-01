package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/platform-health/internal/store"
)

const (
	maxReasonLength = 280
	prodEnvironment = "prod"

	nextCursorHeader = "X-Next-Cursor"
)

var (
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	gitSHAPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	namePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	releaseActions = map[string]bool{store.ActionPromote: true, store.ActionRollback: true}
)

type recordReleaseRequest struct {
	Service        string `json:"service"`
	Environment    string `json:"environment"`
	Action         string `json:"action"`
	ImageDigest    string `json:"imageDigest"`
	PreviousDigest string `json:"previousDigest"`
	GitSHA         string `json:"gitSha"`
	Signed         bool   `json:"signed"`
	Reason         string `json:"reason"`
}

func (a *API) recordRelease(w http.ResponseWriter, r *http.Request) {
	p, ok := validPrincipal(w, r)
	if !ok || !a.isOperator(w, r, p) {
		return
	}
	var req recordReleaseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if detail := validateRelease(req); detail != "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", detail))
		return
	}
	if duty := requiredReleaseDuty(req); !p.HasDuty(duty) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+duty))
		return
	}
	release, err := a.store.RecordRelease(r.Context(), store.Release{
		Service: req.Service, Environment: req.Environment, Action: req.Action, ImageDigest: req.ImageDigest,
		PreviousDigest: req.PreviousDigest, GitSHA: req.GitSHA, Signed: req.Signed, Reason: req.Reason,
	}, r.Header.Get(idempotencyKey))
	if errors.Is(err, store.ErrUnknownEnvironment) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "environment is not registered"))
		return
	}
	if errors.Is(err, store.ErrRollbackTargetUnseen) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "PLH-0422", "rollback imageDigest was never promoted to this service and environment"))
		return
	}
	if errors.Is(err, store.ErrIdempotencyConflict) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, "COM-0409", "Idempotency-Key was already used for a different release"))
		return
	}
	if err != nil {
		a.releasesFailed.Add(1)
		slog.Error("record release failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if release.Replayed {
		a.releasesReplayed.Add(1)
	} else {
		a.releasesRecorded.Add(1)
		if release.ChainBreak {
			a.chainBreaks.Add(1)
			slog.Warn("release chain break", "service", release.Service, "environment", release.Environment, "previousDigest", release.PreviousDigest, "expectedPreviousDigest", release.ExpectedPreviousDigest, "releaseId", release.ID, "correlationId", middleware.GetReqID(r.Context()))
		}
	}
	httpx.JSON(w, http.StatusCreated, release)
}

func (a *API) isOperator(w http.ResponseWriter, r *http.Request, p tenant.Principal) bool {
	switch {
	case a.operatorTenant == "":
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "release writes are disabled: PLATFORM_OPERATOR_TENANT_ID is not configured"))
	case caller(p) != a.operatorTenant:
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "release writes are restricted to the platform operator tenant"))
	default:
		return true
	}
	return false
}

func caller(p tenant.Principal) string {
	canonical, _ := ids.Canonical(p.TenantID)
	return canonical
}

func requiredReleaseDuty(req recordReleaseRequest) string {
	switch {
	case req.Action == store.ActionRollback:
		return dutyRollback
	case req.Environment == prodEnvironment:
		return dutyApproveProd
	}
	return dutyReleaseWrite
}

func validateRelease(req recordReleaseRequest) string {
	switch {
	case !namePattern.MatchString(req.Service):
		return "service must be a lowercase chart name"
	case !namePattern.MatchString(req.Environment):
		return "environment must be a lowercase environment name"
	case !releaseActions[req.Action]:
		return "action must be promote or rollback"
	case !digestPattern.MatchString(req.ImageDigest), !digestPattern.MatchString(req.PreviousDigest):
		return "imageDigest and previousDigest must be sha256:<64 lowercase hex characters>"
	case !gitSHAPattern.MatchString(req.GitSHA):
		return "gitSha must be 40 lowercase hex characters"
	case req.ImageDigest == req.PreviousDigest:
		return "imageDigest must differ from previousDigest"
	case utf8.RuneCountInString(req.Reason) > maxReasonLength:
		return fmt.Sprintf("reason must be at most %d characters", maxReasonLength)
	}
	return ""
}

func (a *API) listReleases(w http.ResponseWriter, r *http.Request) {
	p, ok := validPrincipal(w, r)
	if !ok {
		return
	}
	service, environment := r.URL.Query().Get("service"), r.URL.Query().Get("env")
	if (service != "" && !namePattern.MatchString(service)) || (environment != "" && !namePattern.MatchString(environment)) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "service and env must be lowercase names"))
		return
	}
	after := r.URL.Query().Get("cursor")
	if after != "" {
		canonical, ok := ids.Canonical(after)
		if !ok {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "cursor must be a release id"))
			return
		}
		after = canonical
	}
	releases, next, err := a.store.ListReleases(r.Context(), service, environment, after)
	if errors.Is(err, store.ErrUnknownCursor) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "cursor must be a release id returned by this endpoint"))
		return
	}
	if err != nil {
		slog.Error("list releases failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if a.operatorTenant == "" || caller(p) != a.operatorTenant {
		for i := range releases {
			releases[i].Actor = ""
		}
	}
	if next != "" {
		w.Header().Set(nextCursorHeader, next)
	}
	httpx.JSON(w, http.StatusOK, releases)
}
