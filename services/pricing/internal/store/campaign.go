package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

const campaignColumns = `id::text, legal_entity_id::text, name, valid_from, valid_to, target_customer_group,
	budget::text, currency::text, killed, version`

func scanCampaign(row pgx.Row) (domain.Campaign, error) {
	var c domain.Campaign
	var budget string
	if err := row.Scan(&c.ID, &c.LegalEntityID, &c.Name, &c.ValidFrom, &c.ValidTo, &c.TargetCustomerGroup,
		&budget, &c.Currency, &c.Killed, &c.Version); err != nil {
		return domain.Campaign{}, err
	}
	c.Budget = decimal.RequireFromString(budget)
	return c, nil
}

func campaignNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeCampaignNotFound, "no campaign "+id)
}

// sameCampaign reports whether a replayed create carries the same content.
func sameCampaign(a, b domain.Campaign) bool {
	return a.LegalEntityID == b.LegalEntityID && a.Name == b.Name && dayStr(a.ValidFrom) == dayStr(b.ValidFrom) &&
		dayStr(a.ValidTo) == dayStr(b.ValidTo) && a.TargetCustomerGroup == b.TargetCustomerGroup &&
		a.Budget.Equal(b.Budget) && a.Currency == b.Currency
}

// CreateCampaign stores a live campaign at version 1. A repeated Idempotency-Key returns
// the original campaign, or idempotency_key_reuse when the body differs.
func (s *Postgres) CreateCampaign(ctx context.Context, c domain.Campaign, idempotencyKey string) (domain.Campaign, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out domain.Campaign
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		created, err := scanCampaign(tx.QueryRow(ctx, `insert into campaign (tenant_id, id, legal_entity_id, name, valid_from, valid_to,
			target_customer_group, budget, currency, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9, $10)
			on conflict (tenant_id, idempotency_key) do nothing
			returning `+campaignColumns,
			p.TenantID, ids.New(), c.LegalEntityID, c.Name, dayStr(c.ValidFrom), dayStr(c.ValidTo), c.TargetCustomerGroup,
			c.Budget.String(), c.Currency, key))
		if err == nil {
			out = created
			return nil
		}
		if key == nil || !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		replay, err := scanCampaign(tx.QueryRow(ctx, `select `+campaignColumns+` from campaign where idempotency_key = $1`, idempotencyKey))
		if err != nil {
			return err
		}
		if !sameCampaign(replay, c) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
		}
		out = replay
		return nil
	})
	return out, err
}

// GetCampaign reads one campaign. PRC-0041 when it does not exist or lies outside the
// caller's legal entities.
func (s *Postgres) GetCampaign(ctx context.Context, id string) (domain.Campaign, error) {
	var out domain.Campaign
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanCampaign(tx.QueryRow(ctx, `select `+campaignColumns+` from campaign where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return campaignNotFound(id)
		}
		return err
	})
	return out, err
}

// ListCampaigns returns campaigns of one legal entity (all the caller may see when empty),
// latest start first.
func (s *Postgres) ListCampaigns(ctx context.Context, legalEntityID string, limit int) ([]domain.Campaign, error) {
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	out := []domain.Campaign{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+campaignColumns+` from campaign
			where ($1::text = '' or legal_entity_id::text = $1) order by valid_from desc, name, id limit $2`, legalEntityID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCampaign(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// campaignsByID reads the named campaigns inside tx (RLS applies).
func campaignsByID(ctx context.Context, tx pgx.Tx, campaignIDs []string) ([]domain.Campaign, error) {
	rows, err := tx.Query(ctx, `select `+campaignColumns+` from campaign where id = any($1::uuid[])`, campaignIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Campaign
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// checkRecordCampaign refuses a record that names a campaign the caller cannot see
// (PRC-0041) or a campaign of another legal entity (422).
func checkRecordCampaign(ctx context.Context, tx pgx.Tx, in Entry) error {
	var le string
	err := tx.QueryRow(ctx, `select legal_entity_id::text from campaign where id = $1`, in.CampaignID).Scan(&le)
	if errors.Is(err, pgx.ErrNoRows) {
		return campaignNotFound(in.CampaignID)
	}
	if err != nil {
		return err
	}
	if le != in.LegalEntityID {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0001", "request is invalid")
		p.Errors = map[string]string{"campaignId": "belongs to another legal entity"}
		return p
	}
	return nil
}

// SetCampaignKilled sets (killed) or clears (restored) a campaign's kill switch at the
// expected version (412 conflict_version otherwise; 409 PRC-0031 when it is already in
// that state). The change takes effect on the next resolve and is announced through the
// outbox in the same transaction. A repeated Idempotency-Key replays the first response.
func (s *Postgres) SetCampaignKilled(ctx context.Context, id string, version int, killed bool, idempotencyKey string) (domain.Campaign, error) {
	p, _ := tenant.FromContext(ctx)
	verb, typ := "campaign-restore", pevents.CampaignRestored
	if killed {
		verb, typ = "campaign-kill", pevents.CampaignKilled
	}
	var out domain.Campaign
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanCampaign(tx.QueryRow(ctx, `select `+campaignColumns+` from campaign where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return campaignNotFound(id)
		}
		if err != nil {
			return err
		}
		hash := commandHash(verb, id, strconv.Itoa(version))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "campaign version does not match")
		}
		if cur.Killed == killed {
			state := "live"
			if killed {
				state = "killed"
			}
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "campaign is already "+state)
		}
		out, err = scanCampaign(tx.QueryRow(ctx, `update campaign set killed = $2, version = version + 1 where id = $1 returning `+campaignColumns, id, killed))
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "campaign", out.ID, typ, pevents.CampaignStatusData{CampaignID: out.ID,
			LegalEntityID: out.LegalEntityID, Killed: out.Killed, Version: out.Version}); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}
