// Package store is the Postgres implementation of the inventory ledger. Every
// call runs in a tenant-scoped transaction so row-level security applies.
package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// Service-specific problem codes. Shared codes come from the domain package.
const (
	CodeJournalNotFound  = "INV-JNL-NOT-FOUND"
	CodeJournalState     = "INV-JNL-004"
	CodeCurrencyMismatch = "INV-CUR-001"
	CodeAccountMissing   = "INV-JNL-005" // the posting profile has no offset account for the journal type
	CodeConflictVersion  = "conflict_version"
	CodeIdempotencyReuse = "idempotency_key_reuse"
	CodeOutOfScope       = "AUTH-0003"
	eventSource          = "/passage/inventory"
	uniqueViolation      = "23505"
	rlsViolation         = "42501"
	dateLayout           = "2006-01-02"
)

// Store is the persistence contract the HTTP layer depends on.
type Store interface {
	CreateJournal(ctx context.Context, j domain.Journal, idempotencyKey string) (domain.Journal, bool, error)
	AddJournalLines(ctx context.Context, journalID string, lines []domain.JournalLine) (domain.Journal, error)
	GetJournal(ctx context.Context, id string) (domain.Journal, error)
	ListJournals(ctx context.Context, f JournalFilter) (JournalPage, error)
	PostJournal(ctx context.Context, id string, version int, p PostParams) (domain.Journal, error)
	ValidateJournal(ctx context.Context, id string, p PostParams) (domain.Journal, error)
	TransitionJournal(ctx context.Context, id string, version int, to domain.JournalStatus) (domain.Journal, error)
	ListOnHand(ctx context.Context, f OnHandFilter) (OnHandResult, error)
	ListTransactions(ctx context.Context, f TransactionFilter) ([]TransactionRow, error)
	Reserve(ctx context.Context, in ReserveInput) (ReserveResult, error)
	ReleaseReservation(ctx context.Context, id string) (domain.Reservation, error)
	ListReservations(ctx context.Context, f ReservationFilter) ([]domain.Reservation, error)
	UpsertBatchExpiry(ctx context.Context, productID, batchID string, expiry *time.Time) error
	CreateBlocking(ctx context.Context, in BlockingInput) (domain.Blocking, bool, error)
	ReleaseBlocking(ctx context.Context, id string, version int, disposition string, date time.Time) (domain.Blocking, error)
	ListBlockings(ctx context.Context, f BlockingFilter) ([]domain.Blocking, error)
	CreateCountPlan(ctx context.Context, key string, p domain.CountPlan) (domain.CountPlan, bool, error)
	ListCountPlans(ctx context.Context) ([]domain.CountPlan, error)
	GenerateCountSheet(ctx context.Context, planID, key string) (domain.CountSheet, bool, error)
	ListCountSheets(ctx context.Context, f CountSheetFilter) ([]domain.CountSheet, error)
	GetCountSheet(ctx context.Context, id string) (domain.CountSheet, error)
	EnterCount(ctx context.Context, sheetID, lineID string, version int, counted decimal.Decimal) (domain.CountSheet, error)
	PostCountSheet(ctx context.Context, sheetID string, version int, canApprove bool, p PostParams) (domain.CountSheet, error)
	CreateTransfer(ctx context.Context, key string, o domain.TransferOrder) (domain.TransferOrder, bool, error)
	GetTransfer(ctx context.Context, id string) (domain.TransferOrder, error)
	ListTransfers(ctx context.Context, status string) ([]domain.TransferOrder, error)
	ShipTransfer(ctx context.Context, id string, version int, shipDate time.Time, neg domain.NegativePolicy) (domain.TransferOrder, error)
	ReceiveTransfer(ctx context.Context, id string, version int, receipts []TransferReceipt, receiptDate time.Time, neg domain.NegativePolicy) (domain.TransferOrder, error)
	CancelTransfer(ctx context.Context, id string, version int) (domain.TransferOrder, error)
	ListInTransit(ctx context.Context) ([]InTransitRow, error)
	RunABC(ctx context.Context, in AbcRunInput) (domain.AbcRun, bool, error)
	GetABCRun(ctx context.Context, id string) (domain.AbcRun, error)
	ListABCRuns(ctx context.Context) ([]AbcRunSummary, error)
	ReverseJournal(ctx context.Context, id string, postingDate time.Time) (domain.Journal, bool, error)
	RebuildOnHand(ctx context.Context, repair bool) (RebuildReport, error)
	GenerateTags(ctx context.Context, planID, key string, count int) ([]domain.CountTag, bool, error)
	ListTags(ctx context.Context, planID string, status domain.TagStatus) ([]domain.CountTag, error)
	UseTag(ctx context.Context, tagID string, version int, productID string, dims domain.Dimensions, qty decimal.Decimal) (domain.CountTag, error)
	VoidTag(ctx context.Context, tagID string, version int) (domain.CountTag, error)
	TransferTags(ctx context.Context, planID, key string, markOpenMissing, canApprove bool) (domain.CountSheet, bool, error)
	CreateConsignment(ctx context.Context, key string, o domain.ConsignmentOrder) (domain.ConsignmentOrder, bool, error)
	GetConsignment(ctx context.Context, id string) (domain.ConsignmentOrder, error)
	ListConsignments(ctx context.Context, f ConsignmentFilter) ([]domain.ConsignmentOrder, error)
	ReceiveConsignment(ctx context.Context, id string, version int, receipts []domain.ConsignmentReceipt, receiptDate time.Time) (domain.ConsignmentOrder, error)
	ListConsignmentStock(ctx context.Context, vendor string) ([]ConsignmentStockRow, error)
	Ping(ctx context.Context) error
}

// Postgres implements Store.
type Postgres struct {
	pool  *pgxpool.Pool
	rules domain.JournalRules
}

// New returns a store using the default journal rules.
func New(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool, rules: domain.DefaultJournalRules}
}

// WithRules returns a copy with the given journal limits (INV_JOURNAL_MAX_LINES).
func (s *Postgres) WithRules(r domain.JournalRules) *Postgres {
	limited := *s
	limited.rules = r
	return &limited
}

// Ping checks the database for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func problem(status int, code, detail string) error {
	return httpx.NewProblem(status, code, detail)
}

// domainProblem maps a domain rule violation to a problem with its code: 400
// for a malformed request (COM-0001), 422 for a business rule.
func domainProblem(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		status := http.StatusUnprocessableEntity
		if de.Code == domain.CodeInvalid {
			status = http.StatusBadRequest
		}
		return problem(status, de.Code, de.Detail)
	}
	return err
}

// scopeProblem turns a row-level-security rejection of a write (a legal entity
// outside the caller's scope) into a 403 instead of a 500.
func scopeProblem(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == rlsViolation {
		return problem(http.StatusForbidden, CodeOutOfScope, "the legal entity is outside your scope")
	}
	return err
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, typ, tenantID string, data any) error {
	evt, err := events.New(typ, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	if p, ok := tenant.FromContext(ctx); ok {
		evt.Actor = p.UserID
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

// lockKey serializes requests that share an idempotency key within a scope
// for the rest of the transaction, so a concurrent retry waits for the first
// request and then replays it instead of failing on the unique key.
func lockKey(ctx context.Context, tx pgx.Tx, scope, tenantID, key string) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "inventory."+scope+"|"+tenantID+"|"+key)
	return err
}

// upsertDimensionSet stores a normalized dimension set once per tenant and
// returns its hash and canonical JSON.
func upsertDimensionSet(ctx context.Context, tx pgx.Tx, tenantID string, dims domain.Dimensions) ([]byte, []byte, error) {
	n, err := dims.Normalize()
	if err != nil {
		return nil, nil, domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: err.Error()})
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return nil, nil, err
	}
	hash := n.Hash()
	if _, err := tx.Exec(ctx, `insert into dimension_set (tenant_id, hash, dims) values ($1, $2, $3) on conflict do nothing`, tenantID, hash, raw); err != nil {
		return nil, nil, err
	}
	return hash, raw, nil
}

const journalColumns = `id::text, legal_entity_id::text, journal_type, status, to_char(posting_date, 'YYYY-MM-DD'), coalesce(offset_account_id, ''),
	currency::text, version, gl_entry, created_at, posted_at,
	(select count(*) from inventory_journal_line l where l.tenant_id = j.tenant_id and l.journal_id = j.id),
	coalesce(reverses_journal_id::text, ''),
	coalesce((select r.id::text from inventory_journal r where r.tenant_id = j.tenant_id and r.reverses_journal_id = j.id), '')`

func scanJournal(row pgx.Row) (domain.Journal, error) {
	var j domain.Journal
	var jt, st, dateText string
	var gl []byte
	err := row.Scan(&j.ID, &j.LegalEntityID, &jt, &st, &dateText, &j.OffsetAccountID, &j.Currency, &j.Version, &gl, &j.CreatedAt, &j.PostedAt, &j.LineCount,
		&j.ReversesJournalID, &j.ReversedByJournalID)
	if err != nil {
		return j, err
	}
	j.Type, j.Status = domain.JournalType(jt), domain.JournalStatus(st)
	if j.PostingDate, err = time.Parse(dateLayout, dateText); err != nil {
		return j, err
	}
	if len(gl) > 0 {
		var e domain.JournalEntry
		if err := json.Unmarshal(gl, &e); err != nil {
			return j, err
		}
		j.GLEntry = &e
	}
	return j, nil
}

// loadJournal reads a journal and its lines. forUpdate locks the header row.
func loadJournal(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (domain.Journal, error) {
	p, _ := tenant.FromContext(ctx)
	q := `select ` + journalColumns + ` from inventory_journal j where tenant_id = $1 and id = $2`
	if forUpdate {
		q += " for update"
	}
	j, err := scanJournal(tx.QueryRow(ctx, q, p.TenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return j, problem(http.StatusNotFound, CodeJournalNotFound, "inventory journal not found")
	}
	if err != nil {
		return j, err
	}
	rows, err := tx.Query(ctx, `select id::text, line_no, coalesce(client_key, ''), coalesce(reverses_line_id::text, ''), product_id::text, dims, qty::text,
		entered_qty::text, entered_unit, cost_price::text from inventory_journal_line where tenant_id = $1 and journal_id = $2 order by line_no`, p.TenantID, id)
	if err != nil {
		return j, err
	}
	defer rows.Close()
	for rows.Next() {
		var l domain.JournalLine
		var raw []byte
		var qty, entered, cost string
		if err := rows.Scan(&l.ID, &l.LineNo, &l.ClientKey, &l.ReversesLineID, &l.ProductID, &raw, &qty, &entered, &l.EnteredUnit, &cost); err != nil {
			return j, err
		}
		if err := json.Unmarshal(raw, &l.Dims); err != nil {
			return j, err
		}
		l.Qty, l.EnteredQty, l.CostPrice = decimal.RequireFromString(qty), decimal.RequireFromString(entered), decimal.RequireFromString(cost)
		j.Lines = append(j.Lines, l)
	}
	return j, rows.Err()
}

// CreateJournal inserts a DRAFT journal. A repeated Idempotency-Key returns the
// original journal with inserted=false; a different body under the same key is
// idempotency_key_reuse.
func (s *Postgres) CreateJournal(ctx context.Context, j domain.Journal, idempotencyKey string) (domain.Journal, bool, error) {
	if err := domain.ValidateHeader(j); err != nil {
		return domain.Journal{}, false, domainProblem(err)
	}
	if !slices.Contains(domain.KeyedJournalTypes, j.Type) {
		return domain.Journal{}, false, domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: string(j.Type) + " journals are created by their source document"})
	}
	p, _ := tenant.FromContext(ctx)
	var out domain.Journal
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		var offset *string
		if j.OffsetAccountID != "" {
			offset = &j.OffsetAccountID
		}
		if err := tx.QueryRow(ctx, `insert into inventory_journal (tenant_id, legal_entity_id, id, journal_type, status, posting_date, offset_account_id, currency, idempotency_key)
			values ($1, $2, $3, $4, 'DRAFT', $5, $6, $7, $8)
			on conflict (tenant_id, idempotency_key) do update set version = inventory_journal.version
			returning id::text, (xmax = 0)`, p.TenantID, j.LegalEntityID, ids.New(), string(j.Type), j.PostingDate.Format(dateLayout), offset, j.Currency, idempotencyKey).
			Scan(&id, &inserted); err != nil {
			return scopeProblem(err)
		}
		var err error
		out, err = loadJournal(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return domain.Journal{}, false, err
	}
	if !inserted && (out.Type != j.Type || out.Currency != j.Currency || out.OffsetAccountID != j.OffsetAccountID ||
		!out.PostingDate.Equal(j.PostingDate) || out.LegalEntityID != j.LegalEntityID) {
		return domain.Journal{}, false, problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return out, inserted, nil
}

// AddJournalLines appends lines to a DRAFT journal. Each line is validated
// for the journal type (site, serial, precision, owner; an ownership change
// moves stock of one vendor) and its dimension set stored. A line whose client
// key already exists on the journal is a replay: it is skipped when identical
// and rejected as idempotency_key_reuse when it differs, so a retried bulk call
// never duplicates lines. The line-count limit applies to the journal total.
func (s *Postgres) AddJournalLines(ctx context.Context, journalID string, lines []domain.JournalLine) (domain.Journal, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Journal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, err := loadJournal(ctx, tx, journalID, true)
		if err != nil {
			return err
		}
		if j.Status != domain.JournalDraft {
			return problem(http.StatusConflict, CodeJournalState, "lines can only be added to a DRAFT journal")
		}
		if j.ReversesJournalID != "" {
			return problem(http.StatusConflict, CodeJournalState, "a reversal journal holds exactly the reversed lines; post a new journal for other movements")
		}
		for _, l := range lines {
			if err := domain.ValidateLineFor(j.Type, l, s.rules.MaxScale); err != nil {
				return domainProblem(err)
			}
		}
		if j.Type == domain.JournalOwnershipChange {
			if err := domain.CheckOwnershipLines(append(slices.Clone(j.Lines), lines...)); err != nil {
				return domainProblem(err)
			}
		}
		existing := map[string]domain.JournalLine{}
		for _, l := range j.Lines {
			if l.ClientKey != "" {
				existing[l.ClientKey] = l
			}
		}
		var fresh []domain.JournalLine
		for _, l := range lines {
			if l.ClientKey == "" {
				fresh = append(fresh, l)
				continue
			}
			if prior, ok := existing[l.ClientKey]; ok {
				if !prior.SameContent(l) {
					return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse,
						fmt.Sprintf("line key %q was already used with different values", l.ClientKey))
				}
				continue
			}
			existing[l.ClientKey] = l
			fresh = append(fresh, l)
		}
		if len(j.Lines)+len(fresh) > s.rules.MaxLines {
			return domainProblem(&domain.Error{Code: domain.CodeJournalMax, Detail: fmt.Sprintf("journal exceeds %d lines", s.rules.MaxLines)})
		}
		next := len(j.Lines)
		for _, l := range fresh {
			hash, raw, err := upsertDimensionSet(ctx, tx, p.TenantID, l.Dims)
			if err != nil {
				return err
			}
			next++
			var key *string
			if l.ClientKey != "" {
				key = &l.ClientKey
			}
			if _, err := tx.Exec(ctx, `insert into inventory_journal_line (tenant_id, journal_id, id, line_no, client_key, product_id, dimension_set_hash, dims, qty, entered_qty, entered_unit, cost_price)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
				p.TenantID, journalID, ids.New(), next, key, l.ProductID, hash, raw, l.Qty.String(), l.EnteredQty.String(), l.EnteredUnit, l.CostPrice.String()); err != nil {
				return err
			}
		}
		if len(fresh) > 0 {
			if _, err := tx.Exec(ctx, `update inventory_journal set version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, journalID); err != nil {
				return err
			}
		}
		out, err = loadJournal(ctx, tx, journalID, false)
		return err
	})
	return out, err
}

// GetJournal returns a journal with its lines.
func (s *Postgres) GetJournal(ctx context.Context, id string) (domain.Journal, error) {
	var out domain.Journal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadJournal(ctx, tx, id, false)
		return err
	})
	return out, err
}

// JournalFilter selects journal headers for the list inquiry (PRD 3.16.14
// "Posted journals by type"). Cursor is the opaque value of a previous page.
type JournalFilter struct {
	Status domain.JournalStatus
	Type   domain.JournalType
	Limit  int
	Cursor string
}

// JournalPage is one page of journal headers, newest first. Lines are not
// loaded; LineCount carries their number.
type JournalPage struct {
	Items      []domain.Journal
	NextCursor string
}

// ErrBadCursor is returned for a cursor this store did not issue.
var ErrBadCursor = errors.New("inventory: malformed cursor")

func encodeCursor(j domain.Journal) string {
	return base64.RawURLEncoding.EncodeToString([]byte(j.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + j.ID))
}

func decodeCursor(c string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", ErrBadCursor
	}
	at, id, ok := strings.Cut(string(raw), "|")
	t, err := time.Parse(time.RFC3339Nano, at)
	if !ok || err != nil || !ids.Valid(id) {
		return time.Time{}, "", ErrBadCursor
	}
	return t, id, nil
}

// ListJournals returns journal headers newest first with keyset paging.
func (s *Postgres) ListJournals(ctx context.Context, f JournalFilter) (JournalPage, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var after *time.Time
	var afterID *string
	if f.Cursor != "" {
		t, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return JournalPage{}, problem(http.StatusBadRequest, domain.CodeInvalid, "cursor is malformed")
		}
		after, afterID = &t, &id
	}
	p, _ := tenant.FromContext(ctx)
	page := JournalPage{Items: []domain.Journal{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+journalColumns+` from inventory_journal j
			where tenant_id = $1 and ($2 = '' or status = $2) and ($3 = '' or journal_type = $3)
			and ($4::timestamptz is null or (created_at, id) < ($4, $5::uuid))
			order by created_at desc, id desc limit $6`,
			p.TenantID, string(f.Status), string(f.Type), after, afterID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJournal(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, j)
		}
		return rows.Err()
	})
	if err != nil {
		return JournalPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = encodeCursor(page.Items[limit-1])
	}
	return page, nil
}
