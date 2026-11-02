package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	ErasureConsumerName   = "receiving-erasure"
	ErasureRequestedType  = "dev.passage.privacy.erasure.requested.v1"
	ErasureRequestedTopic = "passage.privacy.erasure"
	dutyPayeeErase        = "receiving.payee.erase"
	erasureTimeout        = 5 * time.Minute
	maxSubjectRef         = 200
)

var errNoLegalHolds = errors.New("the compliance hold check is not configured")

type erasePayeesRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	SubjectRef    string `json:"subjectRef"`
}

type erasureRequestedData struct {
	ErasureID     string   `json:"erasure_id"`
	LegalEntityID string   `json:"legal_entity_id"`
	SubjectRef    string   `json:"subject_ref"`
	DataClasses   []string `json:"data_classes"`
}

func (a *API) ErasureConsumerHandlers() map[string]eventing.Handler {
	return map[string]eventing.Handler{ErasureRequestedType: eventing.HandlerFunc(a.erasePayeesOnEvent)}
}

func (a *API) erasePayees(ctx context.Context, legalEntityID, subjectRef, erasureID, actor string) (store.ErasePayeesResult, error) {
	if a.ports.LegalHolds == nil {
		return store.ErasePayeesResult{}, errNoLegalHolds
	}
	res, err := store.ErasePayees(ctx, a.store, a.ports.LegalHolds, store.ErasePayeesInput{LegalEntityID: legalEntityID, SubjectRef: subjectRef, ErasureID: erasureID, Now: time.Now().UTC()})
	if err != nil {
		slog.ErrorContext(ctx, "one-time payee erasure failed", "err", err, "legalEntityId", legalEntityID, "erasureId", erasureID, "actor", actor, "correlationId", middleware.GetReqID(ctx))
		return res, err
	}
	a.payeesErased.Add(int64(res.Erased))
	a.payeesRetained.Add(int64(res.Retained))
	a.payeesHeld.Add(int64(res.Held))
	slog.InfoContext(ctx, "one-time payee erasure run", "erased", res.Erased, "retained", res.Retained, "held", res.Held, "more", res.More,
		"legalEntityId", legalEntityID, "erasureId", erasureID, "actor", actor, "correlationId", middleware.GetReqID(ctx))
	return res, nil
}

func (a *API) eraseOneTimePayees(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req erasePayeesRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request body is not valid JSON for a payee erasure"))
		return
	}
	req.SubjectRef = strings.TrimSpace(req.SubjectRef)
	fields := map[string]string{}
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	if len(req.SubjectRef) > maxSubjectRef {
		fields["subjectRef"] = fmt.Sprintf("must be at most %d characters", maxSubjectRef)
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := a.erasePayees(r.Context(), req.LegalEntityID, req.SubjectRef, "", p.UserID)
	if errors.Is(err, errNoLegalHolds) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "RCV-HLD-001", "the compliance hold check is not configured; nothing was erased"))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (a *API) erasePayeesOnEvent(ctx context.Context, _ pgx.Tx, evt events.Event) error {
	var data erasureRequestedData
	if err := json.Unmarshal(evt.Data, &data); err != nil {
		return eventing.Permanent(fmt.Errorf("erasure.requested data: %w", err))
	}
	if !ids.Valid(data.ErasureID) || !ids.Valid(data.LegalEntityID) || strings.TrimSpace(data.SubjectRef) == "" {
		return eventing.Permanent(errors.New("erasure.requested needs erasure_id, legal_entity_id and subject_ref"))
	}
	if len(data.DataClasses) > 0 && !slices.Contains(data.DataClasses, domain.PayeeDataClass) {
		return nil
	}
	actor, err := tenant.ServicePrincipal(ErasureConsumerName, evt.TenantID, data.LegalEntityID)
	if err != nil {
		return eventing.Permanent(err)
	}
	ctx, cancel := context.WithTimeout(tenant.With(ctx, actor), erasureTimeout)
	defer cancel()
	if _, err := a.erasePayees(ctx, data.LegalEntityID, strings.TrimSpace(data.SubjectRef), data.ErasureID, actor.UserID); err != nil {
		return fmt.Errorf("erase one-time payees: %w", err)
	}
	return nil
}
