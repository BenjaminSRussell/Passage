package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// Product proposal problem codes (README lists the full catalogue).
const (
	CodeProposalDuplicate = "SLS-0034"
	CodeProposalNotFound  = "SLS-0107"
)

// ProposalRuleFilter narrows the rule list. An empty LegalEntityID means every
// legal entity in the caller's scope; OrderedProductIDs empty means every
// ordered product.
type ProposalRuleFilter struct {
	LegalEntityID     string
	OrderedProductIDs []string
	Limit             int
}

// ProposalResponse is one user's accept or decline of a proposal at line
// commit (FR-27.21). Products and reason are copied from the rule.
type ProposalResponse struct {
	ID                string    `json:"id"`
	LegalEntityID     string    `json:"legalEntityId"`
	RuleID            string    `json:"ruleId"`
	OrderedProductID  string    `json:"orderedProductId"`
	ProposedProductID string    `json:"proposedProductId"`
	Reason            string    `json:"reason"`
	CustomerID        *string   `json:"customerId,omitempty"`
	OrderID           *string   `json:"orderId,omitempty"`
	Accepted          bool      `json:"accepted"`
	RespondedBy       string    `json:"respondedBy"`
	RespondedAt       time.Time `json:"respondedAt"`
}

// NewProposalResponse is a response to record under an Idempotency-Key.
type NewProposalResponse struct {
	LegalEntityID  string
	RuleID         string
	CustomerID     *string
	OrderID        *string
	Accepted       bool
	IdempotencyKey string
	RequestHash    string
	Actor          string
	At             time.Time
}

// proposalColumns selects a rule with its recorded accept and decline counts.
const proposalColumns = `r.id::text, r.legal_entity_id::text, r.ordered_product_id::text, r.proposed_product_id::text,
	r.customer_id::text, r.reason,
	(select count(*) from product_proposal_response x where x.tenant_id = r.tenant_id and x.rule_id = r.id and x.accepted)::int,
	(select count(*) from product_proposal_response x where x.tenant_id = r.tenant_id and x.rule_id = r.id and not x.accepted)::int`

func scanProposalRule(row pgx.Row) (domain.ProposalRule, error) {
	var r domain.ProposalRule
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.OrderedProductID, &r.ProposedProductID, &r.CustomerID, &r.Reason,
		&r.AcceptedCount, &r.DeclinedCount)
	return r, err
}

// CreateProposalRule stores a validated rule. A second rule for the same
// ordered product, customer (or all customers) and proposed product is refused
// with 409, so a product is never proposed twice from one ordered product.
func (s *Postgres) CreateProposalRule(ctx context.Context, in domain.ProposalRule, actor string) (domain.ProposalRule, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into product_proposal_rule (tenant_id, id, legal_entity_id, ordered_product_id,
				proposed_product_id, customer_id, reason, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			p.TenantID, in.ID, in.LegalEntityID, in.OrderedProductID, in.ProposedProductID, in.CustomerID, in.Reason, actor)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return domain.ProposalRule{}, problem(http.StatusConflict, CodeProposalDuplicate,
			"a proposal rule for this ordered product, customer and proposed product already exists")
	}
	if err != nil {
		return domain.ProposalRule{}, err
	}
	in.AcceptedCount, in.DeclinedCount = 0, 0
	return in, nil
}

// ListProposalRules returns rules in the caller's scope in creation order,
// each with its recorded accept and decline counts.
func (s *Postgres) ListProposalRules(ctx context.Context, f ProposalRuleFilter) ([]domain.ProposalRule, error) {
	p, _ := tenant.FromContext(ctx)
	products := f.OrderedProductIDs
	if products == nil {
		products = []string{}
	}
	out := []domain.ProposalRule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+proposalColumns+` from product_proposal_rule r
			where r.tenant_id = $1 and ($2 = '' or r.legal_entity_id::text = $2)
				and (cardinality($3::uuid[]) = 0 or r.ordered_product_id = any($3::uuid[]))
			order by r.created_at, r.id limit $4`, p.TenantID, f.LegalEntityID, products, f.Limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.ProposalRule, error) { return scanProposalRule(row) })
		return err
	})
	return out, err
}

// DeleteProposalRule removes a rule. Recorded responses are kept: they carry
// the rule's products and reason.
func (s *Postgres) DeleteProposalRule(ctx context.Context, id string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from product_proposal_rule where tenant_id = $1 and id = $2`, p.TenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return problem(http.StatusNotFound, CodeProposalNotFound, "product proposal rule not found")
		}
		return nil
	})
}

const responseColumns = `id::text, legal_entity_id::text, rule_id::text, ordered_product_id::text, proposed_product_id::text, reason,
	customer_id::text, order_id::text, accepted, responded_by, responded_at`

func scanProposalResponse(row pgx.Row) (ProposalResponse, error) {
	var r ProposalResponse
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.RuleID, &r.OrderedProductID, &r.ProposedProductID, &r.Reason,
		&r.CustomerID, &r.OrderID, &r.Accepted, &r.RespondedBy, &r.RespondedAt)
	return r, err
}

// RecordProposalResponse stores an accept or decline of the rule's proposal.
// The rule must exist in the given legal entity. A repeat of the same
// Idempotency-Key and body returns the stored response with created=false; the
// key with another body is refused, so a retried click is counted once.
func (s *Postgres) RecordProposalResponse(ctx context.Context, in NewProposalResponse) (ProposalResponse, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out ProposalResponse
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay := func() error {
			var hash string
			var err error
			row := tx.QueryRow(ctx, `select `+responseColumns+`, request_hash from product_proposal_response
				where tenant_id = $1 and idempotency_key = $2`, p.TenantID, in.IdempotencyKey)
			var r ProposalResponse
			if err = row.Scan(&r.ID, &r.LegalEntityID, &r.RuleID, &r.OrderedProductID, &r.ProposedProductID, &r.Reason,
				&r.CustomerID, &r.OrderID, &r.Accepted, &r.RespondedBy, &r.RespondedAt, &hash); err != nil {
				return err
			}
			if hash != in.RequestHash {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out = r
			return nil
		}
		err := replay()
		if err == nil || !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var ordered, proposed, reason string
		err = tx.QueryRow(ctx, `select ordered_product_id::text, proposed_product_id::text, reason from product_proposal_rule
			where tenant_id = $1 and id = $2 and legal_entity_id = $3`, p.TenantID, in.RuleID, in.LegalEntityID).Scan(&ordered, &proposed, &reason)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeProposalNotFound, "product proposal rule not found")
		}
		if err != nil {
			return err
		}
		out, err = scanProposalResponse(tx.QueryRow(ctx, `insert into product_proposal_response (tenant_id, id, legal_entity_id, rule_id,
				ordered_product_id, proposed_product_id, reason, customer_id, order_id, accepted, idempotency_key, request_hash,
				responded_by, responded_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			on conflict (tenant_id, idempotency_key) do nothing
			returning `+responseColumns,
			p.TenantID, ids.New(), in.LegalEntityID, in.RuleID, ordered, proposed, reason, in.CustomerID, in.OrderID, in.Accepted,
			in.IdempotencyKey, in.RequestHash, in.Actor, in.At))
		if errors.Is(err, pgx.ErrNoRows) {
			// A concurrent request with the same key committed first.
			return replay()
		}
		if err == nil {
			created = true
		}
		return err
	})
	return out, created, err
}
