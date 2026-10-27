package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	budgetevents "github.com/BenjaminSRussell/passage/services/budgeting/internal/events"
)

const (
	CodeReservationNotFound = "BUD-0005"
	CodeCheckFailed         = "CO-0601"
	reservationDocType      = "Reservation"
	maxExpireBatch          = 200
)

// ReservationLineInput is one amount to earmark on a control combination.
type ReservationLineInput struct {
	DimensionKey string
	SpanKey      string
	Amount       decimal.Decimal
}

// ReservationRequest creates an earmarked funds document (FR-38.20).
type ReservationRequest struct {
	RuleVersionID string
	Type          domain.ReservationType
	Description   string
	ExpiryDate    string // YYYY-MM-DD; empty means no expiry
	Lines         []ReservationLineInput
}

// ConsumeRequest draws on one reservation line from a follow-on document line.
type ConsumeRequest struct {
	ReservationID  string
	LineID         string
	DocumentType   string
	DocumentID     string
	DocumentLineID string
	Amount         decimal.Decimal
}

// ReservationLine is one line of a reservation as the API returns it.
type ReservationLine struct {
	LineID       string          `json:"lineId"`
	DimensionKey string          `json:"dimensionKey"`
	SpanKey      string          `json:"spanKey"`
	Amount       decimal.Decimal `json:"amount"`
	Consumed     decimal.Decimal `json:"consumed"`
	Released     decimal.Decimal `json:"released"`
	Open         decimal.Decimal `json:"open"`
}

// Consumption is one follow-on document line that drew on a reservation line.
type Consumption struct {
	ID             string          `json:"id"`
	LineID         string          `json:"lineId"`
	EncumbranceID  string          `json:"encumbranceId"`
	DocumentType   string          `json:"documentType"`
	DocumentID     string          `json:"documentId"`
	DocumentLineID string          `json:"documentLineId"`
	Amount         decimal.Decimal `json:"amount"`
	ConsumedAt     string          `json:"consumedAt"`
}

// Reservation is an earmarked funds document (FR-38.20).
type Reservation struct {
	ID            string                   `json:"id"`
	LegalEntityID string                   `json:"legalEntityId"`
	RuleVersionID string                   `json:"ruleVersionId"`
	Type          domain.ReservationType   `json:"type"`
	Number        string                   `json:"number"`
	Description   string                   `json:"description"`
	ExpiryDate    string                   `json:"expiryDate,omitempty"`
	Status        domain.ReservationStatus `json:"status"`
	Total         decimal.Decimal          `json:"total"`
	Consumed      decimal.Decimal          `json:"consumed"`
	Released      decimal.Decimal          `json:"released"`
	Open          decimal.Decimal          `json:"open"`
	CheckID       string                   `json:"checkId,omitempty"`
	Version       int64                    `json:"version"`
	CreatedAt     string                   `json:"createdAt"`
	ClosedAt      string                   `json:"closedAt,omitempty"`
	Lines         []ReservationLine        `json:"lines"`
	Consumptions  []Consumption            `json:"consumptions"`
	Check         *CheckResponse           `json:"check,omitempty"`
}

// ConsumeResponse reports one consumption and what is left on the document.
type ConsumeResponse struct {
	ReservationID string                   `json:"reservationId"`
	LineID        string                   `json:"lineId"`
	DocumentID    string                   `json:"documentId"`
	EncumbranceID string                   `json:"encumbranceId"`
	Consumed      decimal.Decimal          `json:"consumed"`
	LineOpen      decimal.Decimal          `json:"lineOpen"`
	Open          decimal.Decimal          `json:"open"`
	Status        domain.ReservationStatus `json:"status"`
}

// ReleaseResult reports the residual a close or an expiry released.
type ReleaseResult struct {
	ReservationID string                   `json:"reservationId"`
	Number        string                   `json:"number"`
	Released      decimal.Decimal          `json:"released"`
	Status        domain.ReservationStatus `json:"status"`
}

// ExpireResponse lists the reservations an expiry run moved to Expired.
type ExpireResponse struct {
	AsOf    string          `json:"asOf"`
	Expired []ReleaseResult `json:"expired"`
}

func domainProblem(err error) error {
	var de *domain.Error
	if !errors.As(err, &de) {
		return err
	}
	status := http.StatusUnprocessableEntity
	switch de.Code {
	case domain.CodeReservationNotOpen, domain.CodeReservationLapsed:
		status = http.StatusConflict
	}
	return httpx.NewProblem(status, de.Code, de.Detail)
}

// nextReservationNumber draws the next per-type number for a legal entity. The
// counter row is locked by the upsert until the transaction ends, so numbers
// are unique and gap-free per tenant, legal entity and type.
func nextReservationNumber(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, t domain.ReservationType) (string, error) {
	var n int64
	err := tx.QueryRow(ctx, `insert into budget_reservation_number (tenant_id, legal_entity_id, reservation_type, last_value) values ($1, $2, $3, 1)
		on conflict (tenant_id, legal_entity_id, reservation_type) do update set last_value = budget_reservation_number.last_value + 1
		returning last_value`, tenantID, legalEntityID, string(t)).Scan(&n)
	return fmt.Sprintf("%s-%06d", t.NumberPrefix(), n), err
}

// CreateReservation runs the FR-38.4 check on every line of a fund-holding
// document and holds the amounts in the balances' reservations column. The
// document is created only when no line fails; the check is recorded with
// document type Reservation. A RevenueForecast holds nothing and is not checked.
func (s *Postgres) CreateReservation(ctx context.Context, key, hash string, req ReservationRequest, override bool) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		rule, err := loadRule(ctx, tx, p.TenantID, req.RuleVersionID)
		if err != nil {
			return 0, nil, err
		}
		id := ids.New()
		checkLines := make([]CheckLine, len(req.Lines))
		for i, l := range req.Lines {
			checkLines[i] = CheckLine{LineID: ids.New(), DimensionKey: l.DimensionKey, SpanKey: l.SpanKey, Amount: l.Amount}
		}
		balances, err := lockBalances(ctx, tx, p.TenantID, rule, checkLines)
		if err != nil {
			return 0, nil, err
		}
		holds := req.Type.HoldsFunds()
		at := now()
		check := CheckResponse{ID: ids.New(), RuleVersionID: rule.ID, DocumentID: id, DocumentType: reservationDocType,
			Result: domain.Pass, CheckedAt: at, Lines: make([]LineResult, 0, len(checkLines))}
		total := decimal.Zero
		for _, l := range checkLines {
			total = total.Add(l.Amount)
			if !holds {
				continue
			}
			b := balances[balanceKey(l.DimensionKey, l.SpanKey)]
			out := domain.Check(b.bal, domain.Thresholds{WarnPct: rule.WarnPct, BlockPct: rule.BlockPct}, domain.CheckInput{Amount: l.Amount, Override: override})
			if out.Result == domain.Fail {
				return 0, nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeCheckFailed,
					fmt.Sprintf("Budget check failed on %s %s: available %s, requested %s", l.DimensionKey, l.SpanKey,
						out.Available.StringFixed(2), l.Amount.StringFixed(2)))
			}
			// Later lines on the same combination see this line's hold.
			b.bal.Reservations = b.bal.Reservations.Add(l.Amount)
			check.Lines = append(check.Lines, LineResult{LineID: l.LineID, DimensionKey: l.DimensionKey, SpanKey: l.SpanKey, Amount: l.Amount,
				Result: out.Result, Available: out.Available, Net: out.Net, OverPct: out.OverPct, Trace: out.Trace})
			if out.Result == domain.Warn {
				check.Result = domain.Warn
			}
			for _, step := range out.Trace {
				if step == "override" {
					check.Overridden = true
				}
			}
		}
		number, err := nextReservationNumber(ctx, tx, p.TenantID, rule.LegalEntityID, req.Type)
		if err != nil {
			return 0, nil, err
		}
		var checkID any
		if holds {
			checkID = check.ID
		}
		if _, err := tx.Exec(ctx, `insert into budget_reservation (tenant_id, id, legal_entity_id, rule_version_id, reservation_type, number,
			description, expiry_date, status, total, check_id, created_at)
			values ($1, $2, $3, $4, $5, $6, $7, nullif($8, '')::date, 'Open', $9, $10, $11::timestamptz)`,
			p.TenantID, id, rule.LegalEntityID, rule.ID, string(req.Type), number, req.Description, req.ExpiryDate, total, checkID, at); err != nil {
			return 0, nil, err
		}
		for _, l := range checkLines {
			b := balances[balanceKey(l.DimensionKey, l.SpanKey)]
			if _, err := tx.Exec(ctx, `insert into budget_reservation_line (tenant_id, reservation_id, line_id, legal_entity_id, balance_id, amount)
				values ($1, $2, $3, $4, $5, $6)`, p.TenantID, id, l.LineID, rule.LegalEntityID, b.id, l.Amount); err != nil {
				return 0, nil, err
			}
			if holds {
				if _, err := tx.Exec(ctx, `update budget_control_balance set reservations = reservations + $3, version = version + 1
					where tenant_id = $1 and id = $2`, p.TenantID, b.id, l.Amount); err != nil {
					return 0, nil, err
				}
			}
		}
		if holds {
			if err := recordCheck(ctx, tx, p.TenantID, rule, check); err != nil {
				return 0, nil, err
			}
		}
		r, err := loadReservation(ctx, tx, p.TenantID, id)
		if err != nil {
			return 0, nil, err
		}
		if holds {
			r.Check = &check
		}
		return http.StatusCreated, r, nil
	})
}

// lockBalanceIDs locks balance rows by id in sorted order, the same order the
// checks use, so a reservation command and a check cannot deadlock.
func lockBalanceIDs(ctx context.Context, tx pgx.Tx, tenantID string, balanceIDs []string) error {
	sorted := append([]string(nil), balanceIDs...)
	sort.Strings(sorted)
	_, err := tx.Exec(ctx, `select 1 from budget_control_balance where tenant_id = $1 and id = any($2::uuid[]) order by id for update`, tenantID, sorted)
	return err
}

// lineBalances reads the balance ids a reservation's lines hold funds on.
func lineBalances(ctx context.Context, tx pgx.Tx, tenantID string, reservationIDs []string) ([]string, error) {
	rows, err := tx.Query(ctx, `select distinct balance_id::text from budget_reservation_line where tenant_id = $1 and reservation_id = any($2::uuid[])`,
		tenantID, reservationIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ConsumeReservation relieves a reservation line and creates (or raises) the
// follow-on document line's encumbrance in one transaction (FR-38.20). The
// balance moves the amount from reservations to encumbrances, so available
// funds do not change (AC-38.12).
func (s *Postgres) ConsumeReservation(ctx context.Context, key, hash string, req ConsumeRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		var balanceID string
		err := tx.QueryRow(ctx, `select balance_id::text from budget_reservation_line where tenant_id = $1 and reservation_id = $2 and line_id = $3`,
			p.TenantID, req.ReservationID, req.LineID).Scan(&balanceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, httpx.NewProblem(http.StatusNotFound, CodeReservationNotFound, "reservation line not found")
		}
		if err != nil {
			return 0, nil, err
		}
		// Lock order matches CheckBudget: balance, then documents.
		if err := lockBalanceIDs(ctx, tx, p.TenantID, []string{balanceID}); err != nil {
			return 0, nil, err
		}
		head, err := lockHead(ctx, tx, p.TenantID, req.ReservationID)
		if err != nil {
			return 0, nil, err
		}
		var amountText, consumedText, releasedText string
		if err := tx.QueryRow(ctx, `select amount::text, consumed::text, released::text from budget_reservation_line
			where tenant_id = $1 and reservation_id = $2 and line_id = $3 for update`, p.TenantID, req.ReservationID, req.LineID).
			Scan(&amountText, &consumedText, &releasedText); err != nil {
			return 0, nil, err
		}
		line := domain.ReservationLine{Amount: parseDec(amountText), Consumed: parseDec(consumedText), Released: parseDec(releasedText)}
		if err := domain.CheckConsume(head.typ, head.status, head.expiry, time.Now(), line, req.Amount); err != nil {
			return 0, nil, domainProblem(err)
		}
		pr, err := lockPrior(ctx, tx, p.TenantID, req.DocumentLineID)
		if err != nil {
			return 0, nil, err
		}
		if pr != nil && pr.balanceID != balanceID {
			return 0, nil, httpx.NewProblem(http.StatusConflict, CodeLineMoved,
				fmt.Sprintf("document line %s is already reserved against another control combination or rule version", req.DocumentLineID))
		}
		at := now()
		hist, err := json.Marshal([]historyEntry{{Type: "Consume", Amount: req.Amount.String(), DocumentID: req.DocumentID, At: at}})
		if err != nil {
			return 0, nil, err
		}
		encumbranceID := ids.New()
		if pr != nil {
			encumbranceID = pr.id
			reserved := pr.reserved.Add(req.Amount)
			if _, err := tx.Exec(ctx, `update encumbrance set reserved = $3, status = $4, lines = lines || $5::jsonb where tenant_id = $1 and id = $2`,
				p.TenantID, pr.id, reserved, domain.EncumbranceStatus(reserved, pr.relieved, decimal.Zero), string(hist)); err != nil {
				return 0, nil, err
			}
		} else if _, err := tx.Exec(ctx, `insert into encumbrance (tenant_id, id, legal_entity_id, balance_id, source_type, source_line_id, source_document_id, reserved, status, lines)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)`,
			p.TenantID, encumbranceID, head.legalEntityID, balanceID, req.DocumentType, req.DocumentLineID, req.DocumentID, req.Amount, domain.StatusOpen, string(hist)); err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `update budget_control_balance set reservations = reservations - $3, encumbrances = encumbrances + $3, version = version + 1
			where tenant_id = $1 and id = $2`, p.TenantID, balanceID, req.Amount); err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `update budget_reservation_line set consumed = consumed + $4 where tenant_id = $1 and reservation_id = $2 and line_id = $3`,
			p.TenantID, req.ReservationID, req.LineID, req.Amount); err != nil {
			return 0, nil, err
		}
		consumed := head.consumed.Add(req.Amount)
		status := domain.StatusAfterConsume(head.total, consumed)
		if _, err := tx.Exec(ctx, `update budget_reservation set consumed = $3, status = $4, version = version + 1 where tenant_id = $1 and id = $2`,
			p.TenantID, req.ReservationID, consumed, string(status)); err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `insert into budget_reservation_consumption (tenant_id, id, legal_entity_id, reservation_id, line_id, encumbrance_id,
			document_type, document_id, document_line_id, amount, consumed_at) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::timestamptz)`,
			p.TenantID, ids.New(), head.legalEntityID, req.ReservationID, req.LineID, encumbranceID, req.DocumentType, req.DocumentID,
			req.DocumentLineID, req.Amount, at); err != nil {
			return 0, nil, err
		}
		open := head.total.Sub(consumed).Sub(head.released)
		if err := enqueue(ctx, tx, "budgetreservation", req.ReservationID, budgetevents.ReservationConsumed, p.TenantID, budgetevents.ReservationConsumedData{
			ReservationID: req.ReservationID, Number: head.number, LineID: req.LineID, DocumentType: req.DocumentType, DocumentID: req.DocumentID,
			EncumbranceID: encumbranceID, Amount: req.Amount.String(), Remaining: open.String(),
		}); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, ConsumeResponse{ReservationID: req.ReservationID, LineID: req.LineID, DocumentID: req.DocumentID, EncumbranceID: encumbranceID,
			Consumed: req.Amount, LineOpen: line.Open().Sub(req.Amount), Open: open, Status: status}, nil
	})
}

// head is the locked document row of a reservation.
type head struct {
	legalEntityID string
	number        string
	typ           domain.ReservationType
	status        domain.ReservationStatus
	expiry        *time.Time
	total         decimal.Decimal
	consumed      decimal.Decimal
	released      decimal.Decimal
}

func lockHead(ctx context.Context, tx pgx.Tx, tenantID, id string) (head, error) {
	var h head
	var typ, status, total, consumed, released string
	err := tx.QueryRow(ctx, `select legal_entity_id::text, number, reservation_type, status, expiry_date, total::text, consumed::text, released::text
		from budget_reservation where tenant_id = $1 and id = $2 for update`, tenantID, id).
		Scan(&h.legalEntityID, &h.number, &typ, &status, &h.expiry, &total, &consumed, &released)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, httpx.NewProblem(http.StatusNotFound, CodeReservationNotFound, "reservation not found")
	}
	h.typ, h.status = domain.ReservationType(typ), domain.ReservationStatus(status)
	h.total, h.consumed, h.released = parseDec(total), parseDec(consumed), parseDec(released)
	return h, err
}

// release frees every open line amount of a locked reservation and sets the
// final status (Closed or Expired). Balance rows must already be locked.
func release(ctx context.Context, tx pgx.Tx, tenantID, id string, h head, final domain.ReservationStatus) (ReleaseResult, error) {
	res := ReleaseResult{ReservationID: id, Number: h.number, Released: decimal.Zero, Status: final}
	// released is only ever set here, on the final transition, so before this
	// update it is zero and the new value is exactly the amount freed.
	rows, err := tx.Query(ctx, `update budget_reservation_line set released = amount - consumed
		where tenant_id = $1 and reservation_id = $2 and amount - consumed - released > 0
		returning balance_id::text, (amount - consumed)::text`, tenantID, id)
	if err != nil {
		return res, err
	}
	freed := map[string]decimal.Decimal{}
	for rows.Next() {
		var balanceID, amount string
		if err := rows.Scan(&balanceID, &amount); err != nil {
			rows.Close()
			return res, err
		}
		freed[balanceID] = freed[balanceID].Add(parseDec(amount))
		res.Released = res.Released.Add(parseDec(amount))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	if h.typ.HoldsFunds() {
		for balanceID, amount := range freed {
			if _, err := tx.Exec(ctx, `update budget_control_balance set reservations = reservations - $3, version = version + 1
				where tenant_id = $1 and id = $2`, tenantID, balanceID, amount); err != nil {
				return res, err
			}
		}
	}
	_, err = tx.Exec(ctx, `update budget_reservation set status = $3, released = released + $4, closed_at = now(), version = version + 1
		where tenant_id = $1 and id = $2`, tenantID, id, string(final), res.Released)
	return res, err
}

// CloseReservation releases the residual and marks the document Closed (BR-38.14).
func (s *Postgres) CloseReservation(ctx context.Context, key, hash, id string) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		balanceIDs, err := lineBalances(ctx, tx, p.TenantID, []string{id})
		if err != nil {
			return 0, nil, err
		}
		if err := lockBalanceIDs(ctx, tx, p.TenantID, balanceIDs); err != nil {
			return 0, nil, err
		}
		h, err := lockHead(ctx, tx, p.TenantID, id)
		if err != nil {
			return 0, nil, err
		}
		if err := domain.CheckClose(h.typ, h.status); err != nil {
			return 0, nil, domainProblem(err)
		}
		res, err := release(ctx, tx, p.TenantID, id, h, domain.ReservationClosed)
		return http.StatusOK, res, err
	})
}

// ExpireReservations moves every Open or PartiallyConsumed reservation whose
// expiry date is on or before asOf to Expired and releases its residual
// (BR-38.14, job reservation-expiry). It processes at most 200 per call; a
// rerun picks up the rest and skips what is already expired.
func (s *Postgres) ExpireReservations(ctx context.Context, key, hash, asOf string) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		day, err := time.Parse("2006-01-02", asOf)
		if err != nil {
			return 0, nil, err
		}
		rows, err := tx.Query(ctx, `select id::text from budget_reservation where tenant_id = $1 and status in ('Open', 'PartiallyConsumed')
			and reservation_type <> 'FundsBlocking' and expiry_date <= $2::date order by expiry_date, id limit $3`, p.TenantID, asOf, maxExpireBatch)
		if err != nil {
			return 0, nil, err
		}
		var due []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, nil, err
			}
			due = append(due, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, nil, err
		}
		resp := ExpireResponse{AsOf: asOf, Expired: []ReleaseResult{}}
		if len(due) == 0 {
			return http.StatusOK, resp, nil
		}
		balanceIDs, err := lineBalances(ctx, tx, p.TenantID, due)
		if err != nil {
			return 0, nil, err
		}
		if err := lockBalanceIDs(ctx, tx, p.TenantID, balanceIDs); err != nil {
			return 0, nil, err
		}
		for _, id := range due {
			h, err := lockHead(ctx, tx, p.TenantID, id)
			if err != nil {
				return 0, nil, err
			}
			// Re-check under the lock: a concurrent close may have won.
			if !domain.Expirable(h.typ, h.status, h.expiry, day) {
				continue
			}
			res, err := release(ctx, tx, p.TenantID, id, h, domain.ReservationExpired)
			if err != nil {
				return 0, nil, err
			}
			resp.Expired = append(resp.Expired, res)
		}
		return http.StatusOK, resp, nil
	})
}

const reservationColumns = `id::text, legal_entity_id::text, rule_version_id::text, reservation_type, number, description,
	coalesce(to_char(expiry_date, 'YYYY-MM-DD'), ''), status, total::text, consumed::text, released::text, coalesce(check_id::text, ''),
	version, created_at, closed_at`

func scanReservation(row pgx.Row) (Reservation, error) {
	var r Reservation
	var typ, status, total, consumed, released string
	var created time.Time
	var closed *time.Time
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.RuleVersionID, &typ, &r.Number, &r.Description, &r.ExpiryDate, &status,
		&total, &consumed, &released, &r.CheckID, &r.Version, &created, &closed); err != nil {
		return r, err
	}
	r.Type, r.Status = domain.ReservationType(typ), domain.ReservationStatus(status)
	r.Total, r.Consumed, r.Released = parseDec(total), parseDec(consumed), parseDec(released)
	r.Open = r.Total.Sub(r.Consumed).Sub(r.Released)
	r.CreatedAt = stamp(created)
	if closed != nil {
		r.ClosedAt = stamp(*closed)
	}
	r.Lines, r.Consumptions = []ReservationLine{}, []Consumption{}
	return r, nil
}

// loadReservation reads one document with its lines and consumption history.
func loadReservation(ctx context.Context, tx pgx.Tx, tenantID, id string) (Reservation, error) {
	r, err := scanReservation(tx.QueryRow(ctx, `select `+reservationColumns+` from budget_reservation where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, httpx.NewProblem(http.StatusNotFound, CodeReservationNotFound, "reservation not found")
	}
	if err != nil {
		return r, err
	}
	rows, err := tx.Query(ctx, `select l.line_id::text, b.control_dimension_key, b.time_span_key, l.amount::text, l.consumed::text, l.released::text
		from budget_reservation_line l join budget_control_balance b on b.tenant_id = l.tenant_id and b.id = l.balance_id
		where l.tenant_id = $1 and l.reservation_id = $2 order by b.control_dimension_key, b.time_span_key, l.line_id`, tenantID, id)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var l ReservationLine
		var amount, consumed, released string
		if err := rows.Scan(&l.LineID, &l.DimensionKey, &l.SpanKey, &amount, &consumed, &released); err != nil {
			rows.Close()
			return r, err
		}
		l.Amount, l.Consumed, l.Released = parseDec(amount), parseDec(consumed), parseDec(released)
		l.Open = l.Amount.Sub(l.Consumed).Sub(l.Released)
		r.Lines = append(r.Lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return r, err
	}
	rows, err = tx.Query(ctx, `select id::text, line_id::text, encumbrance_id::text, document_type, document_id::text, document_line_id::text, amount::text, consumed_at
		from budget_reservation_consumption where tenant_id = $1 and reservation_id = $2 order by consumed_at, id`, tenantID, id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Consumption
		var amount string
		var at time.Time
		if err := rows.Scan(&c.ID, &c.LineID, &c.EncumbranceID, &c.DocumentType, &c.DocumentID, &c.DocumentLineID, &amount, &at); err != nil {
			return r, err
		}
		c.Amount, c.ConsumedAt = parseDec(amount), stamp(at)
		r.Consumptions = append(r.Consumptions, c)
	}
	return r, rows.Err()
}

// ReservationFilter narrows the earmarked funds list; empty fields match all.
type ReservationFilter struct {
	RuleVersionID string
	Type          string
	Status        string
}

// ListReservations lists earmarked funds documents, newest first, without
// their lines (the earmarked funds list inquiry, PRD 7.38.14).
func (s *Postgres) ListReservations(ctx context.Context, f ReservationFilter) ([]Reservation, error) {
	out := []Reservation{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select `+reservationColumns+` from budget_reservation
			where tenant_id = $1 and ($2 = '' or rule_version_id::text = $2) and ($3 = '' or reservation_type = $3) and ($4 = '' or status = $4)
			order by created_at desc, id limit $5`, tenantID, f.RuleVersionID, f.Type, f.Status, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReservation(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetReservation returns one document with its lines and consumptions, or BUD-0005.
func (s *Postgres) GetReservation(ctx context.Context, id string) (Reservation, error) {
	var r Reservation
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		r, err = loadReservation(ctx, tx, tenantID, id)
		return err
	})
	return r, err
}
