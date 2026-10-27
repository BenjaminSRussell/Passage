package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

// CodeCaseNotFound answers a case id the caller cannot see.
const CodeCaseNotFound = "AR-CASE-NOT-FOUND"

// Case is a minimal collection case (FR-30.18). Its fields are a subset of the
// wave 45 generic case schema; /receivables/cases stays as the façade after the
// migration. SLABreached is computed at read time (AR-0018).
type Case struct {
	ID             string           `json:"id"`
	LegalEntityID  string           `json:"legalEntityId"`
	CustomerID     string           `json:"customerId"`
	Category       string           `json:"category"`
	Owner          string           `json:"owner"`
	Priority       string           `json:"priority"`
	Status         string           `json:"status"`
	Reason         string           `json:"reason"`
	Currency       string           `json:"currency"`
	OpenItemID     *string          `json:"openItemId,omitempty"`
	DocumentNumber *string          `json:"documentNumber,omitempty"`
	AttachedAmount *decimal.Decimal `json:"attachedAmount,omitempty"`
	SLAHours       int              `json:"slaHours"`
	SLADue         time.Time        `json:"slaDue"`
	SLABreached    bool             `json:"slaBreached"`
	CloseNote      *string          `json:"closeNote,omitempty"`
	ClosedAt       *time.Time       `json:"closedAt,omitempty"`
	CreatedAt      time.Time        `json:"createdAt"`
	Version        int              `json:"version"`
}

// CaseFilter narrows a case list.
type CaseFilter struct {
	Status     string
	Category   string
	CustomerID string
}

// The case reads join the linked item for its document number.
const caseColumns = `c.id, c.legal_entity_id, c.customer_id, c.category, c.owner, c.priority, c.status, c.reason, c.currency,
	c.open_item_id, oi.document_number, c.attached_amount::text, c.sla_hours, c.sla_due, c.close_note, c.closed_at, c.created_at, c.version`

const caseFrom = ` from collection_case c left join open_item oi on oi.tenant_id = c.tenant_id and oi.id = c.open_item_id`

func scanCase(row rowScanner) (Case, error) {
	var c Case
	var attached *string
	if err := row.Scan(&c.ID, &c.LegalEntityID, &c.CustomerID, &c.Category, &c.Owner, &c.Priority, &c.Status, &c.Reason,
		&c.Currency, &c.OpenItemID, &c.DocumentNumber, &attached, &c.SLAHours, &c.SLADue, &c.CloseNote, &c.ClosedAt, &c.CreatedAt,
		&c.Version); err != nil {
		return Case{}, err
	}
	if attached != nil {
		d, err := decimal.NewFromString(*attached)
		if err != nil {
			return Case{}, err
		}
		c.AttachedAmount = &d
	}
	c.SLABreached = domain.CaseSLABreached(c.Status, c.SLADue, time.Now())
	return c, nil
}

func readCase(ctx context.Context, tx pgx.Tx, where string, args ...any) (Case, error) {
	return scanCase(tx.QueryRow(ctx, `select `+caseColumns+caseFrom+` where `+where, args...))
}

// sameCase compares the fields a client controls, for idempotent replay.
func sameCase(stored, in Case) bool {
	return stored.LegalEntityID == in.LegalEntityID && stored.CustomerID == in.CustomerID && stored.Category == in.Category &&
		stored.Priority == in.Priority && stored.Owner == in.Owner && stored.Reason == in.Reason &&
		eqStrPtr(stored.OpenItemID, in.OpenItemID) && (in.OpenItemID != nil || stored.Currency == in.Currency) &&
		eqDecPtr(stored.AttachedAmount, in.AttachedAmount)
}

func eqDecPtr(a, b *decimal.Decimal) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// CreateCase opens a collection case and enqueues case.opened.v1. A linked open
// item must belong to the case's customer and legal entity; the case takes its
// currency. A repeated key returns the stored case when the request matches.
func (s *Postgres) CreateCase(ctx context.Context, in Case, key string) (Case, error) {
	p, _ := tenant.FromContext(ctx)
	var out Case
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay, err := readCase(ctx, tx, `c.idempotency_key = $1`, key)
		if err == nil {
			if !sameCase(replay, in) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out = replay
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if in.OpenItemID != nil {
			item, err := scanOpenItem(tx.QueryRow(ctx, `select `+openItemColumns+` from open_item where id = $1`, *in.OpenItemID))
			if err != nil {
				return notFound(err, CodeOpenItemNotFound, "open item not found")
			}
			if item.LegalEntityID != in.LegalEntityID || item.CustomerID != in.CustomerID {
				return httpx.NewProblem(http.StatusBadRequest, domain.CodeCaseMismatch, "open item "+item.DocumentNumber+" belongs to another customer or legal entity")
			}
			if in.Currency != "" && in.Currency != item.Currency {
				return httpx.NewProblem(http.StatusBadRequest, domain.CodeCaseMismatch, "open item "+item.DocumentNumber+" is in "+item.Currency)
			}
			in.Currency = item.Currency
		}
		out, err = insertCase(ctx, tx, p.TenantID, in, key)
		return err
	})
	return out, mapWriteErr(err)
}

// insertCase writes an OPEN case with its category's SLA and enqueues case.opened.v1.
func insertCase(ctx context.Context, tx pgx.Tx, tenantID string, in Case, key string) (Case, error) {
	hours, ok := domain.CaseSLAHours(in.Category)
	if !ok {
		return Case{}, httpx.NewProblem(http.StatusBadRequest, domain.CodeCaseState, "unknown case category "+in.Category)
	}
	var attached *string
	if in.AttachedAmount != nil {
		v := in.AttachedAmount.String()
		attached = &v
	}
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into collection_case (tenant_id, id, legal_entity_id, customer_id, category, owner, priority,
		status, reason, currency, open_item_id, attached_amount, sla_hours, sla_due, idempotency_key)
		values ($1, $2, $3, $4, $5, $6, $7, 'OPEN', $8, $9, $10, $11::numeric, $12, now() + make_interval(hours => $12), $13)`,
		tenantID, id, in.LegalEntityID, in.CustomerID, in.Category, in.Owner, in.Priority, in.Reason, in.Currency,
		in.OpenItemID, attached, hours, key); err != nil {
		return Case{}, err
	}
	out, err := readCase(ctx, tx, `c.id = $1`, id)
	if err != nil {
		return Case{}, err
	}
	return out, enqueue(ctx, tx, "case", out.ID, rcvevents.CaseOpened, tenantID, rcvevents.CaseOpenedData{
		CaseID: out.ID, LegalEntityID: out.LegalEntityID, CustomerID: out.CustomerID, Category: out.Category, Priority: out.Priority,
		Owner: out.Owner, Reason: out.Reason, Currency: out.Currency, OpenItemID: out.OpenItemID, AttachedAmount: out.AttachedAmount,
		SLADue: out.SLADue,
	})
}

// ListCases reads cases, open ones first and nearest SLA first, bounded by limit.
func (s *Postgres) ListCases(ctx context.Context, f CaseFilter, limit int) ([]Case, error) {
	out := []Case{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+caseColumns+caseFrom+`
			where ($1::text = '' or c.status = $1::text) and ($2::text = '' or c.category = $2::text)
			and ($3::text = '' or c.customer_id::text = $3::text)
			order by c.status = 'CLOSED', c.sla_due, c.id limit $4`, f.Status, f.Category, f.CustomerID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCase(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// GetCase reads one collection case.
func (s *Postgres) GetCase(ctx context.Context, id string) (Case, error) {
	var out Case
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = readCase(ctx, tx, `c.id = $1`, id)
		return notFound(err, CodeCaseNotFound, "collection case not found")
	})
	return out, err
}

// lockCase reads an open case under a row lock and checks the If-Match version.
func lockCase(ctx context.Context, tx pgx.Tx, id string, version int) (Case, error) {
	cur, err := readCase(ctx, tx, `c.id = $1 for update of c`, id)
	if err != nil {
		return Case{}, notFound(err, CodeCaseNotFound, "collection case not found")
	}
	if cur.Version != version {
		return Case{}, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the case changed since it was read")
	}
	if !domain.CaseCanChange(cur.Status) {
		return Case{}, domainProblem(domain.CodeCaseState, "the case is "+cur.Status+" and cannot change")
	}
	return cur, nil
}

// AssignCase hands an open case to owner. version is the If-Match value.
func (s *Postgres) AssignCase(ctx context.Context, id string, version int, owner string) (Case, error) {
	p, _ := tenant.FromContext(ctx)
	var out Case
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockCase(ctx, tx, id, version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update collection_case set owner = $3, version = version + 1 where tenant_id = $1 and id = $2`,
			p.TenantID, id, owner); err != nil {
			return err
		}
		var err error
		out, err = readCase(ctx, tx, `c.id = $1`, id)
		return err
	})
	return out, mapWriteErr(err)
}

// CloseCase closes an open case with an optional note and enqueues case.closed.v1.
func (s *Postgres) CloseCase(ctx context.Context, id string, version int, note *string) (Case, error) {
	p, _ := tenant.FromContext(ctx)
	var out Case
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockCase(ctx, tx, id, version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update collection_case set status = 'CLOSED', close_note = $3, closed_at = now(), version = version + 1
			where tenant_id = $1 and id = $2`, p.TenantID, id, note); err != nil {
			return err
		}
		var err error
		if out, err = readCase(ctx, tx, `c.id = $1`, id); err != nil {
			return err
		}
		return enqueue(ctx, tx, "case", out.ID, rcvevents.CaseClosed, p.TenantID, rcvevents.CaseClosedData{
			CaseID: out.ID, LegalEntityID: out.LegalEntityID, CustomerID: out.CustomerID, Category: out.Category,
			SLABreached: out.ClosedAt != nil && out.ClosedAt.After(out.SLADue), Note: out.CloseNote,
		})
	})
	return out, mapWriteErr(err)
}
