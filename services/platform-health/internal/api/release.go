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
	"github.com/BenjaminSRussell/passage/services/platform-health/internal/store"
)

const maxReasonLength = 280

var (
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	namePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	releaseActions = map[string]bool{store.ActionPromote: true, store.ActionRollback: true}
)

type recordReleaseRequest struct {
	Service        string `json:"service"`
	Environment    string `json:"environment"`
	Action         string `json:"action"`
	ImageDigest    string `json:"imageDigest"`
	PreviousDigest string `json:"previousDigest"`
	Signed         bool   `json:"signed"`
	Reason         string `json:"reason"`
}

func (a *API) recordRelease(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
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
	release, err := a.store.RecordRelease(r.Context(), store.Release{
		Service: req.Service, Environment: req.Environment, Action: req.Action, ImageDigest: req.ImageDigest,
		PreviousDigest: req.PreviousDigest, Signed: req.Signed, Reason: req.Reason,
	}, r.Header.Get(idempotencyKey))
	if errors.Is(err, store.ErrUnknownEnvironment) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "environment is not registered"))
		return
	}
	if err != nil {
		a.releasesFailed.Add(1)
		slog.Error("record release failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.releasesRecorded.Add(1)
	httpx.JSON(w, http.StatusCreated, release)
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
	case req.ImageDigest == req.PreviousDigest:
		return "imageDigest must differ from previousDigest"
	case utf8.RuneCountInString(req.Reason) > maxReasonLength:
		return fmt.Sprintf("reason must be at most %d characters", maxReasonLength)
	}
	return ""
}

func (a *API) listReleases(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	service, environment := r.URL.Query().Get("service"), r.URL.Query().Get("env")
	if (service != "" && !namePattern.MatchString(service)) || (environment != "" && !namePattern.MatchString(environment)) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "service and env must be lowercase names"))
		return
	}
	releases, err := a.store.ListReleases(r.Context(), service, environment)
	if err != nil {
		slog.Error("list releases failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, releases)
}
