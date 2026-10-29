package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

// Duties for the slice-2 screens (§9.46.17).
const (
	DutyAuthor       = "AnalyticsAuthor"
	DutyCatalogSteer = "CatalogSteward"
	DutyArchiveAdmin = "ArchiveAdmin"
)

const (
	headerIdempotency = "Idempotency-Key"
	maxKeyLength      = 255
	defaultScoreLimit = 90
	maxScoreLimit     = 366
)

// registerSlice2 mounts data-quality rules and scores, archive partitions and
// report schedules.
func (a *API) registerSlice2(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireDuty(DutyCatalogSteer))
		g.Get("/v1/catalog/dq-rules", a.listDQRules)
		g.Get("/v1/catalog/dq-rules/scores", a.listDQScores)
		g.Get("/v1/catalog/dq-rules/{ruleId}", a.getDQRule)
		g.Put("/v1/catalog/dq-rules/{ruleId}", a.putDQRule)
	})
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireDuty(DutyArchiveAdmin))
		g.Get("/v1/archive/partitions", a.listPartitions)
		g.Get("/v1/archive/partitions/{id}", a.getPartition)
		g.Post("/v1/archive/partitions/{id}/restore", a.restorePartition)
	})
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireDuty(DutyAuthor))
		g.Get("/v1/analytics/schedules", a.listSchedules)
		g.Post("/v1/analytics/schedules", a.createSchedule)
		g.Get("/v1/analytics/schedules/{id}", a.getSchedule)
	})
}

// fail answers with the domain problem when err carries one.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		httpx.Fail(w, r, problemOf(de))
		return
	}
	httpx.Fail(w, r, err)
}

func refuse(w http.ResponseWriter, r *http.Request, detail string, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, CodeBadRequest, detail)
	if len(fields) > 0 {
		p.Errors = fields
	}
	httpx.Fail(w, r, p)
}

func etag(version int) string { return `"` + strconv.Itoa(version) + `"` }

// ifMatch reads If-Match as a version; 0 means the header is absent.
func ifMatch(r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(strings.Trim(raw, `"`))
	if err != nil || n < 1 || !strings.HasPrefix(raw, `"`) || !strings.HasSuffix(raw, `"`) {
		return 0, false
	}
	return n, true
}

// --- data-quality rules (FR-46.16) -----------------------------------------

// DQRulesResponse lists rules.
type DQRulesResponse struct {
	Items []domain.DQRule `json:"items"`
}

// DQScoresResponse lists score history, newest first.
type DQScoresResponse struct {
	Items []domain.Score `json:"items"`
}

// DQRuleRequest is the body of PUT /catalog/dq-rules/{ruleId}. ThresholdPct is
// a decimal string (default 98); Status defaults to ACTIVE; Muted opens or
// keeps a 7-day mute window, false clears it.
type DQRuleRequest struct {
	AssetFQN     string  `json:"assetFqn"`
	RuleType     string  `json:"ruleType"`
	Expression   string  `json:"expression"`
	ThresholdPct *string `json:"thresholdPct,omitempty"`
	Severity     string  `json:"severity"`
	OwnerUserID  string  `json:"ownerUserId"`
	Status       string  `json:"status,omitempty"`
	Muted        bool    `json:"muted,omitempty"`
}

func (a *API) listDQRules(w http.ResponseWriter, r *http.Request) {
	asset := r.URL.Query().Get("asset")
	if asset != "" && !domain.IsAssetFQN(asset) {
		refuse(w, r, "asset must be a dotted lowercase asset name", map[string]string{"asset": "dotted lowercase asset name"})
		return
	}
	items, err := a.store.ListDQRules(r.Context(), asset)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, DQRulesResponse{Items: items})
}

func (a *API) getDQRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "ruleId")
	if !domain.IsRuleID(id) {
		fail(w, r, domain.NotFound("data-quality rule "+id))
		return
	}
	rule, err := a.store.GetDQRule(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rule.Version))
	httpx.JSON(w, http.StatusOK, rule)
}

func (a *API) putDQRule(w http.ResponseWriter, r *http.Request) {
	var req DQRuleRequest
	if err := httpx.Decode(r, &req); err != nil {
		refuse(w, r, "request body is not valid JSON", nil)
		return
	}
	version, ok := ifMatch(r)
	if !ok {
		refuse(w, r, `If-Match must be the rule version as an entity tag, for example "3"`, map[string]string{"If-Match": "entity tag such as \"3\""})
		return
	}
	threshold := domain.DefaultThreshold()
	if req.ThresholdPct != nil {
		d, err := decimal.NewFromString(*req.ThresholdPct)
		if err != nil {
			refuse(w, r, "request failed validation", map[string]string{"thresholdPct": "decimal string between 0 and 100"})
			return
		}
		threshold = d
	}
	status := req.Status
	if status == "" {
		status = domain.RuleActive
	}
	rule := domain.DQRule{
		RuleID: chi.URLParam(r, "ruleId"), AssetFQN: req.AssetFQN, Type: req.RuleType, Expression: req.Expression,
		ThresholdPct: threshold, Severity: req.Severity, OwnerUserID: req.OwnerUserID, Status: status,
	}
	if err := rule.Validate(); err != nil {
		fail(w, r, err)
		return
	}
	saved, created, err := a.store.PutDQRule(r.Context(), rule, version, req.Muted, a.now())
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(saved.Version))
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	httpx.JSON(w, code, saved)
}

func (a *API) listDQScores(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	asset, rule := q.Get("asset"), q.Get("rule")
	fields := map[string]string{}
	if asset != "" && !domain.IsAssetFQN(asset) {
		fields["asset"] = "dotted lowercase asset name"
	}
	if rule != "" && !domain.IsRuleID(rule) {
		fields["rule"] = "lower snake case rule id"
	}
	limit := defaultScoreLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxScoreLimit {
			fields["limit"] = "1 to 366"
		}
		limit = n
	}
	if len(fields) > 0 {
		refuse(w, r, "request failed validation", fields)
		return
	}
	items, err := a.store.ListDQScores(r.Context(), asset, rule, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, DQScoresResponse{Items: items})
}

// --- archive partitions (FR-46.18) ------------------------------------------

// PartitionsResponse lists archive partitions.
type PartitionsResponse struct {
	Items []domain.ArchivePartition `json:"items"`
}

func (a *API) listPartitions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	domainName, status := q.Get("domain"), q.Get("status")
	fields := map[string]string{}
	if domainName != "" && !domain.IsArchiveName(domainName) {
		fields["domain"] = "lower snake case domain name"
	}
	if status != "" && !domain.IsArchiveStatus(status) {
		fields["status"] = "PENDING, VERIFIED, DROPPED or RESTORED"
	}
	if len(fields) > 0 {
		refuse(w, r, "request failed validation", fields)
		return
	}
	items, err := a.store.ListArchivePartitions(r.Context(), domainName, status)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, PartitionsResponse{Items: items})
}

func (a *API) getPartition(w http.ResponseWriter, r *http.Request) {
	p, err := a.store.GetArchivePartition(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

// restorePartition restores a DROPPED partition for the tenant's restore
// lifetime (30 days). Repeating the request while the restore is live returns
// the same partition: the call is idempotent by partition id.
func (a *API) restorePartition(w http.ResponseWriter, r *http.Request) {
	p, _, err := a.store.RestoreArchivePartition(r.Context(), chi.URLParam(r, "id"), domain.DefaultRestoreTTLDays, a.now())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

// --- report schedules (FR-46.10) --------------------------------------------

// SchedulesResponse lists schedules.
type SchedulesResponse struct {
	Items []domain.Schedule `json:"items"`
}

// ScheduleRequest is the body of POST /analytics/schedules.
type ScheduleRequest struct {
	Name        string             `json:"name"`
	TargetType  string             `json:"targetType"`
	TargetID    string             `json:"targetId"`
	Cron        string             `json:"cron"`
	Timezone    string             `json:"timezone"`
	Format      string             `json:"format"`
	Destination domain.Destination `json:"destination"`
}

func (a *API) listSchedules(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListSchedules(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, SchedulesResponse{Items: items})
}

func (a *API) getSchedule(w http.ResponseWriter, r *http.Request) {
	s, err := a.store.GetSchedule(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(s.Version))
	httpx.JSON(w, http.StatusOK, s)
}

func (a *API) createSchedule(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get(headerIdempotency))
	if key == "" || len(key) > maxKeyLength {
		refuse(w, r, "Idempotency-Key header is required (1 to 255 characters)", map[string]string{headerIdempotency: "required, at most 255 characters"})
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		refuse(w, r, "request body is too large", nil)
		return
	}
	r.Body = io.NopCloser(strings.NewReader(string(raw)))
	var req ScheduleRequest
	if err := httpx.Decode(r, &req); err != nil {
		refuse(w, r, "request body is not valid JSON", nil)
		return
	}
	if _, ok := tenant.FromContext(r.Context()); !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
		return
	}
	in := domain.Schedule{
		Name: req.Name, TargetType: req.TargetType, TargetID: req.TargetID, Cron: req.Cron,
		Timezone: req.Timezone, Format: req.Format, Destination: req.Destination,
	}
	now := a.now()
	if _, verr := domain.PrepareSchedule(in, now); verr != nil {
		fail(w, r, verr)
		return
	}
	sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), raw...))
	out, err := a.store.CreateSchedule(r.Context(), key, hex.EncodeToString(sum[:]), in, now)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if out.Replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
}
