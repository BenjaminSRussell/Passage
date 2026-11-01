package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

const (
	DowntimeConsumerName  = "scheduling-downtime"
	DowntimeRecordedType  = "dev.passage.eam.downtime.recorded.v1"
	DowntimeRecordedTopic = "passage.eam.downtime"
	DutyDowntimeApply     = "scheduling.downtime.apply"

	downtimeApplyTimeout = 30 * time.Second
	downtimeKeyPrefix    = "eam-downtime:"
)

type downtimeRecordedData struct {
	DowntimeID  string `json:"downtimeId"`
	ResourceID  string `json:"resourceId"`
	WorkOrderID string `json:"workOrderId"`
	From        string `json:"from"`
	To          string `json:"to"`
}

func (a *API) DowntimeConsumerHandlers() map[string]eventing.Handler {
	return map[string]eventing.Handler{DowntimeRecordedType: eventing.HandlerFunc(a.applyRecordedDowntime)}
}

func (a *API) applyRecordedDowntime(ctx context.Context, _ pgx.Tx, evt events.Event) error {
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty(DutyDowntimeApply) || !strings.EqualFold(actor.TenantID, evt.TenantID) {
		return eventing.Permanent(errors.New("downtime consumer has no principal for the event tenant"))
	}
	var data downtimeRecordedData
	if err := json.Unmarshal(evt.Data, &data); err != nil {
		return eventing.Permanent(fmt.Errorf("downtime.recorded data: %w", err))
	}
	from, fromErr := time.Parse(time.RFC3339Nano, data.From)
	to, toErr := time.Parse(time.RFC3339Nano, data.To)
	if !ids.Valid(data.DowntimeID) || !ids.Valid(data.ResourceID) || fromErr != nil || toErr != nil {
		return eventing.Permanent(errors.New("downtime.recorded needs downtimeId, resourceId, from and to"))
	}
	start, end := from.UTC().Truncate(time.Minute), to.UTC().Add(time.Minute-time.Nanosecond).Truncate(time.Minute)
	if !end.After(start) || end.Sub(start) > maxOverrideSpan {
		return eventing.Permanent(errors.New("downtime window must be after its start and at most 31 days"))
	}
	ctx, cancel := context.WithTimeout(ctx, downtimeApplyTimeout)
	defer cancel()
	sum := sha256.Sum256([]byte(strings.ToLower(data.ResourceID) + "|" + start.Format(time.RFC3339) + "|" + end.Format(time.RFC3339)))
	res, created, err := a.store.CreateCapacityOverride(ctx, store.CapacityOverride{
		ResourceID: strings.ToLower(data.ResourceID), Kind: domain.OverrideDowntime, StartAt: start, EndAt: end, Reason: "EAM work order " + data.WorkOrderID,
	}, store.Idem{Key: downtimeKeyPrefix + strings.ToLower(data.DowntimeID), Fingerprint: hex.EncodeToString(sum[:])})
	if err != nil {
		var problem *httpx.Problem
		if errors.As(err, &problem) && (problem.Status == http.StatusNotFound || problem.Status == http.StatusUnprocessableEntity) {
			return eventing.Permanent(err)
		}
		return fmt.Errorf("apply downtime: %w", err)
	}
	slog.InfoContext(ctx, "eam downtime applied to capacity", "overrideId", res.ID, "resourceId", res.ResourceID, "created", created,
		"affectedJobs", len(res.AffectedJobs), "downtimeId", data.DowntimeID, "tenantId", evt.TenantID, "eventId", evt.ID, "actor", actor.UserID)
	return nil
}
