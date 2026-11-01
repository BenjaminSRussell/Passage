package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	ErsConsumerName     = "receiving-ers"
	ReceiptPostedType   = "dev.passage.receiving.receipt.posted.v1"
	ReceiptPostedTopic  = "passage.receiving.receipt"
	ersSettleTimeout    = 30 * time.Second
	ersConsumerSkipNote = "receipt.posted did not settle"
)

func (a *API) ErsConsumerHandlers() map[string]eventing.Handler {
	return map[string]eventing.Handler{ReceiptPostedType: eventing.HandlerFunc(a.settlePostedReceipt)}
}

func (a *API) settlePostedReceipt(ctx context.Context, _ pgx.Tx, evt events.Event) error {
	var data receiptPostedData
	if err := json.Unmarshal(evt.Data, &data); err != nil {
		return eventing.Permanent(fmt.Errorf("receipt.posted data: %w", err))
	}
	postingDate, dateErr := time.Parse(dateLayout, data.PostingDate)
	if !ids.Valid(data.ReceiptID) || !ids.Valid(data.LegalEntityID) || dateErr != nil {
		return eventing.Permanent(errors.New("receipt.posted needs receipt_id, legal_entity_id and posting_date"))
	}
	actor, err := tenant.ServicePrincipal(ErsConsumerName, evt.TenantID, data.LegalEntityID)
	if err != nil {
		return eventing.Permanent(err)
	}
	ctx, cancel := context.WithTimeout(tenant.With(ctx, actor), ersSettleTimeout)
	defer cancel()
	fiscalPeriodID, err := a.resolveFiscalPeriod(ctx, data.LegalEntityID, postingDate, "")
	if err != nil {
		return fmt.Errorf("resolve fiscal period: %w", err)
	}
	run := &ersRun{
		api: a, ctx: ctx, principal: actor,
		base:    settlementRunBase{legalEntityID: data.LegalEntityID, ledgerID: a.ports.Profile.LedgerID, fiscalPeriodID: fiscalPeriodID, postingDate: postingDate},
		orders:  map[string]ersOrder{},
		vendors: map[string]bool{},
		groups:  groupMemo{},
		body:    ersRunBody{Settled: []ersSettledBody{}, Skipped: []skippedBody{}},
	}
	if err := run.receipt(data.ReceiptID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return eventing.Permanent(err)
		}
		return fmt.Errorf("settle receipt: %w", err)
	}
	for _, skipped := range run.body.Skipped {
		slog.InfoContext(ctx, ersConsumerSkipNote, "reason", skipped.Reason, "detail", skipped.Detail, "receiptId", data.ReceiptID, "tenantId", evt.TenantID, "eventId", evt.ID)
	}
	for _, settled := range run.body.Settled {
		slog.InfoContext(ctx, "receipt settled by evaluated receipt settlement", "invoiceId", settled.InvoiceID, "receiptId", settled.ReceiptID, "status", settled.Status, "tenantId", evt.TenantID, "eventId", evt.ID, "actor", actor.UserID)
	}
	return nil
}
