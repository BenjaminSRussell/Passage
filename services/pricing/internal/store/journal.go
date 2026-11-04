package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// CodeJournalNotFound is raised when a journal id does not exist.
const CodeJournalNotFound = "PRC-0034"

// JournalFilter narrows a journal list (Price journal log, PRD 5.26.14).
type JournalFilter struct {
	LegalEntityID string
	Status        string
	Limit         int
}

// Journal is a stored price journal: header, lifecycle fields and lines.
type Journal struct {
	domain.Journal
	Status       string
	LineCount    int
	ErrorCount   int
	ReversesID   string
	ReversedByID string
	PostedAt     *time.Time
	CreatedAt    time.Time
	// CreatedBy and PostedBy are the users who made and posted the journal (maker/checker);
	// '' when not recorded (journals created before migration 0012, or not yet posted). A
	// reversal records its reverser as both.
	CreatedBy string
	PostedBy  string
}

// JournalInput is a journal to create. CREATE lines get their record ids in the store.
type JournalInput struct {
	LegalEntityID string
	Name          string
	SmartRound    bool
	Lines         []domain.JournalLine
}

// CreateJournal stores an OPEN journal. A repeated Idempotency-Key returns the original
// journal when the body matches and is rejected otherwise; the header insert uses
// ON CONFLICT so two concurrent requests with one key cannot both create a journal.
func (s *Postgres) CreateJournal(ctx context.Context, in JournalInput, idempotencyKey string) (Journal, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out Journal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		jid := ids.New()
		var inserted string
		err := tx.QueryRow(ctx, `insert into price_journal (tenant_id, id, legal_entity_id, name, status, smart_round, line_count, idempotency_key, created_by)
			values ($1, $2, $3, $4, 'OPEN', $5, $6, $7, $8) on conflict (tenant_id, idempotency_key) do nothing returning id::text`,
			p.TenantID, jid, in.LegalEntityID, in.Name, in.SmartRound, len(in.Lines), key, p.UserID).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) && key != nil {
			var existing string
			if err := tx.QueryRow(ctx, `select id::text from price_journal where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key).Scan(&existing); err != nil {
				return err
			}
			replay, err := loadJournal(ctx, tx, existing, false, true)
			if err != nil {
				return err
			}
			if !sameJournalRequest(replay, in) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
			}
			out = replay
			return nil
		}
		if err != nil {
			return err
		}
		lines := make([]domain.JournalLine, len(in.Lines))
		for i, l := range in.Lines {
			l.No = i + 1
			if l.Op == domain.OpCreate {
				l.RecordID = ids.New()
			}
			lines[i] = l
		}
		if err := writeLines(ctx, tx, p.TenantID, jid, lines); err != nil {
			return err
		}
		out, err = loadJournal(ctx, tx, jid, false, true)
		return err
	})
	return out, err
}

// ListJournals returns journal headers, newest first, without their lines.
func (s *Postgres) ListJournals(ctx context.Context, f JournalFilter) ([]Journal, error) {
	if f.Limit <= 0 || f.Limit > MaxListLimit {
		f.Limit = MaxListLimit
	}
	out := []Journal{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, journalHeader+` where ($1::text = '' or legal_entity_id::text = $1) and ($2::text = '' or status = $2)
			order by created_at desc, id limit $3`, f.LegalEntityID, f.Status, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJournal(rows)
			if err != nil {
				return err
			}
			out = append(out, j)
		}
		return rows.Err()
	})
	return out, err
}

// GetJournal reads a journal with its lines.
func (s *Postgres) GetJournal(ctx context.Context, id string) (Journal, error) {
	var out Journal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadJournal(ctx, tx, id, false, true)
		return err
	})
	return out, err
}

// ValidateJournal checks every line against the current records and stores the result:
// per-line errors, and VALIDATED when the journal is clean (FR-26.9).
func (s *Postgres) ValidateJournal(ctx context.Context, id string) (Journal, error) {
	p, _ := tenant.FromContext(ctx)
	var out Journal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, err := loadJournal(ctx, tx, id, true, true)
		if err != nil {
			return err
		}
		if j.Status != domain.JournalOpen && j.Status != domain.JournalValidated {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "only OPEN or VALIDATED journals can be validated")
		}
		clean, err := checkJournal(ctx, tx, &j.Journal)
		if err != nil {
			return err
		}
		j.Status, j.ErrorCount = domain.JournalOpen, 0
		for _, l := range j.Lines {
			if len(l.Errors) > 0 {
				j.ErrorCount++
			}
		}
		if clean {
			j.Status = domain.JournalValidated
		}
		if err := writeLines(ctx, tx, p.TenantID, id, j.Lines); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update price_journal set status = $2, error_count = $3 where id = $1`, id, j.Status, j.ErrorCount); err != nil {
			return err
		}
		out = j
		return nil
	})
	return out, err
}

// PostJournal applies a VALIDATED journal in one transaction (FR-26.9). It validates
// again under the legal entity's journal lock, so a change made since validation cannot
// slip through. Any failure leaves every record and line unchanged. The poster is refused
// when it ran the commodity calculation that made the journal (PRC-0056) or, under
// journal_four_eyes, when it created the journal (PRC-0057), and is recorded as posted_by.
// A repeated Idempotency-Key replays the first response.
func (s *Postgres) PostJournal(ctx context.Context, id, idempotencyKey string) (Journal, error) {
	p, _ := tenant.FromContext(ctx)
	var out Journal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		j, err := loadJournal(ctx, tx, id, true, true)
		if err != nil {
			return err
		}
		hash := commandHash("journal-post", id)
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if j.Status != domain.JournalValidated {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "only VALIDATED journals can post; validate first")
		}
		if err := refuseOwnCommodityRun(ctx, tx, id, p.UserID); err != nil {
			return err
		}
		if err := refuseOwnJournal(ctx, tx, p.TenantID, j, p.UserID); err != nil {
			return err
		}
		if err := lockEntity(ctx, tx, p.TenantID, j.LegalEntityID); err != nil {
			return err
		}
		clean, err := checkJournal(ctx, tx, &j.Journal)
		if err != nil {
			return err
		}
		if !clean {
			return invalidProblem(&j.Journal)
		}
		if err := applyJournal(ctx, tx, p.TenantID, &j.Journal); err != nil {
			return err
		}
		if err := writeLines(ctx, tx, p.TenantID, id, j.Lines); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update price_journal set status = 'POSTED', posted_at = now(), error_count = 0, posted_by = $2 where id = $1`, id, p.UserID); err != nil {
			return err
		}
		if err := enqueueJournal(ctx, tx, p.TenantID, id, pevents.JournalPostedData{JournalID: id, LegalEntityID: j.LegalEntityID, LineCount: len(j.Lines)}); err != nil {
			return err
		}
		if out, err = loadJournal(ctx, tx, id, false, false); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// ReverseJournal posts a new journal that undoes a POSTED journal and marks the original
// REVERSED (FR-26.9). The reversal validates against current state; a record changed
// since posting fails it and nothing is written. A repeated Idempotency-Key replays the
// first response.
func (s *Postgres) ReverseJournal(ctx context.Context, id, idempotencyKey string) (Journal, error) {
	p, _ := tenant.FromContext(ctx)
	var out Journal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		orig, err := loadJournal(ctx, tx, id, true, true)
		if err != nil {
			return err
		}
		hash := commandHash("journal-reverse", id)
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if orig.Status != domain.JournalPosted {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "only POSTED journals can be reversed")
		}
		if err := lockEntity(ctx, tx, p.TenantID, orig.LegalEntityID); err != nil {
			return err
		}
		rev := domain.Reverse(orig.Journal)
		rev.ID = ids.New()
		rev.Name = truncate("Reversal of "+orig.Name, 120)
		if _, err := tx.Exec(ctx, `insert into price_journal (tenant_id, id, legal_entity_id, name, status, smart_round, line_count, reverses_journal_id, created_by)
			values ($1, $2, $3, $4, 'OPEN', $5, $6, $7, $8)`, p.TenantID, rev.ID, rev.LegalEntityID, rev.Name, rev.SmartRound, len(rev.Lines), id, p.UserID); err != nil {
			return err
		}
		clean, err := checkJournal(ctx, tx, &rev)
		if err != nil {
			return err
		}
		if !clean {
			return invalidProblem(&rev)
		}
		if err := applyJournal(ctx, tx, p.TenantID, &rev); err != nil {
			return err
		}
		if err := writeLines(ctx, tx, p.TenantID, rev.ID, rev.Lines); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update price_journal set status = 'POSTED', posted_at = now(), posted_by = $2 where id = $1`, rev.ID, p.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update price_journal set status = 'REVERSED', reversed_by_journal_id = $2 where id = $1`, id, rev.ID); err != nil {
			return err
		}
		if err := enqueueJournal(ctx, tx, p.TenantID, rev.ID, pevents.JournalPostedData{JournalID: rev.ID, LegalEntityID: rev.LegalEntityID,
			LineCount: len(rev.Lines), ReversedJournalID: id}); err != nil {
			return err
		}
		if out, err = loadJournal(ctx, tx, rev.ID, false, false); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// refuseOwnJournal is the maker/checker rule (PricingMaintain vs PricingJournalPost, PRD
// 5.26.17): when the legal entity's journal_four_eyes parameter is on, the user who created a
// journal cannot post it (403 PRC-0057), and a post without a user identity is refused too,
// since the rule cannot be checked. A journal with no recorded creator (created before
// migration 0012) may be posted by any identified user. PostJournal checks PRC-0056 first, so
// the runner of a commodity calculation, who is also its journal's creator, always gets
// PRC-0056 whatever the parameter says.
func refuseOwnJournal(ctx context.Context, tx pgx.Tx, tenantID string, j Journal, userID string) error {
	if userID != "" && j.CreatedBy != userID {
		return nil
	}
	par, err := loadParameters(ctx, tx, tenantID, j.LegalEntityID)
	if err != nil || !par.JournalFourEyes {
		return err
	}
	if userID == "" {
		return httpx.NewProblem(http.StatusForbidden, domain.CodeFourEyes,
			"journal_four_eyes is on for this legal entity: a journal post needs an identified user")
	}
	return httpx.NewProblem(http.StatusForbidden, domain.CodeFourEyes,
		"journal_four_eyes is on for this legal entity: the user who created the journal cannot post it; another pricing manager posts it")
}

// refuseOwnCommodityRun enforces the PRD 5.26.17 conflict between CommodityPriceRun and
// PricingJournalPost per user: the journal a commodity run created cannot be posted by the
// user who ran it (403 PRC-0056). A run without a recorded user (a service call) and every
// other journal are not affected.
func refuseOwnCommodityRun(ctx context.Context, tx pgx.Tx, journalID, userID string) error {
	if userID == "" {
		return nil
	}
	var runBy string
	err := tx.QueryRow(ctx, `select run_by from commodity_run where journal_id = $1`, journalID).Scan(&runBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if runBy == userID {
		return httpx.NewProblem(http.StatusForbidden, domain.CodeRunOwnJournal,
			"the user who ran the commodity price calculation cannot post its journal; another pricing manager posts it")
	}
	return nil
}

// entityLockKey names the advisory lock that guards the ACTIVE records of one legal entity.
func entityLockKey(tenantID, legalEntityID string) string {
	return "pricing-records:" + tenantID + ":" + legalEntityID
}

// lockEntity takes the legal entity's lock exclusively: journal posts and reversals run one
// at a time per entity, and no single-record activation runs while one of them validates.
func lockEntity(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, entityLockKey(tenantID, legalEntityID))
	return err
}

// lockEntityShared takes the legal entity's lock in shared mode: activations run side by
// side (each also locks its own key) but wait for a journal post of the same entity.
func lockEntityShared(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock_shared(hashtextextended($1, 0))`, entityLockKey(tenantID, legalEntityID))
	return err
}

// checkJournal loads the records of the journal's legal entity that it touches, plus every
// ACTIVE record of that entity (the overlap pool), then runs the domain validation. A
// record of another legal entity is not loaded, so a line naming one fails as not found.
func checkJournal(ctx context.Context, tx pgx.Tx, j *domain.Journal) (bool, error) {
	touched := []string{}
	for _, l := range j.Lines {
		if l.Op != domain.OpCreate && l.RecordID != "" {
			touched = append(touched, l.RecordID)
		}
	}
	rows, err := tx.Query(ctx, `select `+recordColumns+` from condition_record
		where legal_entity_id::text = $1 and (status = 'ACTIVE' or id = any($2::uuid[]))`, j.LegalEntityID, touched)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	stored := map[string]domain.Record{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return false, err
		}
		stored[e.ID] = e.Record
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	// Smart rounding uses the legal entity's table (FR-26.7, smart_rounding_bands).
	tp, _ := tenant.FromContext(ctx)
	params, err := loadParameters(ctx, tx, tp.TenantID, j.LegalEntityID)
	if err != nil {
		return false, err
	}
	bands, faults := domain.ParseBands(params.SmartRoundingBands)
	if faults != nil {
		bands = domain.DefaultSmartBands()
	}
	return domain.ValidateJournal(j, stored, bands), nil
}

// applyJournal writes each validated line to condition_record and enqueues the matching
// event in the same transaction. Each UPDATE, DELETE and RESTORE is guarded by the version
// it was validated against.
func applyJournal(ctx context.Context, tx pgx.Tx, tenantID string, j *domain.Journal) error {
	// BR-26.7: a record whose validity starts before the posting day is announced as retroactive.
	var today time.Time
	if err := tx.QueryRow(ctx, `select current_date`).Scan(&today); err != nil {
		return err
	}
	for i := range j.Lines {
		l := &j.Lines[i]
		if l.After == nil {
			return httpx.NewProblem(http.StatusInternalServerError, CodeStateInvalid, "journal line without an after-state")
		}
		a := *l.After
		v := recordValues(a)
		if l.Op == domain.OpCreate {
			if _, err := tx.Exec(ctx, `insert into condition_record (tenant_id, id, legal_entity_id, condition_type, customer_kind, customer_key,
				product_kind, product_key, variant, currency, unit, from_quantity, amount, disc_pct_1, disc_pct_2, valid_from, valid_to,
				search_next, exclusive, status, version, key_hash, journal_id)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::numeric, $13::numeric, $14::numeric, $15::numeric, $16, $17, $18, $19, $20, $21, $22, $23)`,
				tenantID, a.ID, j.LegalEntityID, v.ctype, v.ckind, v.ckey, v.pkind, v.pkey, v.variant, v.currency, v.unit,
				v.from, v.amount, v.p1, v.p2, v.validFrom, v.validTo, v.searchNext, v.exclusive, v.status, v.version, v.keyHash, j.ID); err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `update condition_record set condition_type = $2, customer_kind = $3, customer_key = $4, product_kind = $5,
				product_key = $6, variant = $7, currency = $8, unit = $9, from_quantity = $10::numeric, amount = $11::numeric,
				disc_pct_1 = $12::numeric, disc_pct_2 = $13::numeric, valid_from = $14, valid_to = $15, search_next = $16, exclusive = $17,
				status = $18, version = $19, key_hash = $20, journal_id = $21
				where id = $1 and version = $22`,
				a.ID, v.ctype, v.ckind, v.ckey, v.pkind, v.pkey, v.variant, v.currency, v.unit, v.from, v.amount, v.p1, v.p2,
				v.validFrom, v.validTo, v.searchNext, v.exclusive, v.status, v.version, v.keyHash, j.ID, l.ExpectedVersion)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, fmt.Sprintf("record %s changed while the journal was posting", a.ID))
			}
		}
		l.AfterVersion = a.Version
		e := Entry{Record: a, LegalEntityID: j.LegalEntityID}
		if a.Status == domain.StatusActive {
			if err := enqueue(ctx, tx, e, pevents.ConditionRecordActivated, pevents.ConditionRecordData{ID: a.ID, LegalEntityID: j.LegalEntityID,
				Type: string(a.Type), CustomerKind: string(a.CustomerKind), CustomerKey: a.CustomerKey, ProductKind: string(a.ProductKind),
				ProductKey: a.ProductKey, Variant: a.Variant, Currency: a.Currency, Unit: a.Unit, FromQuantity: a.FromQuantity.String(),
				ValidFrom: dayStr(a.ValidFrom), ValidTo: dayStr(a.ValidTo), Version: a.Version, JournalID: j.ID,
				Retroactive: dayStr(a.ValidFrom) < dayStr(today)}); err != nil {
				return err
			}
			continue
		}
		if err := enqueue(ctx, tx, e, pevents.ConditionRecordWithdrawn, pevents.ConditionRecordWithdrawnData{ID: a.ID, Version: a.Version,
			Reason: "price journal " + j.ID}); err != nil {
			return err
		}
	}
	return nil
}

// recordFields is the column form of a record, shared by insert and update.
type recordFields struct {
	ctype, ckind, ckey, pkind, pkey, variant, currency, unit string

	from, amount, p1, p2 string

	validFrom, validTo time.Time

	searchNext, exclusive bool

	status string

	version int

	keyHash string
}

func recordValues(r domain.Record) recordFields {
	return recordFields{
		ctype: string(r.Type), ckind: string(r.CustomerKind), ckey: r.CustomerKey, pkind: string(r.ProductKind),
		pkey: r.ProductKey, variant: r.Variant, currency: r.Currency, unit: r.Unit,
		from: r.FromQuantity.String(), amount: r.Amount.String(), p1: r.DiscPct1.String(), p2: r.DiscPct2.String(),
		validFrom: r.ValidFrom, validTo: r.ValidTo, searchNext: r.SearchNext, exclusive: r.Exclusive,
		status: string(r.Status), version: r.Version, keyHash: KeyHash(r),
	}
}

// invalidProblem is the 422 for a journal that does not validate; it names the failing lines.
func invalidProblem(j *domain.Journal) error {
	var named []string
	count := 0
	for _, l := range j.Lines {
		if len(l.Errors) == 0 {
			continue
		}
		count++
		if len(named) < 5 {
			named = append(named, fmt.Sprintf("line %d: %s %s", l.No, l.Errors[0].Code, l.Errors[0].Detail))
		}
	}
	return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeJournalInvalid,
		fmt.Sprintf("Journal has %d validation errors; nothing was posted (%s)", count, strings.Join(named, "; ")))
}

// lineChunk is how many journal lines one INSERT carries. COPY is not allowed on a table
// with row-level security, so lines go in as unnest() batches.
const lineChunk = 5000

// writeLines replaces a journal's lines with the given state (delete, then batched insert).
func writeLines(ctx context.Context, tx pgx.Tx, tenantID, journalID string, lines []domain.JournalLine) error {
	if _, err := tx.Exec(ctx, `delete from price_journal_line where journal_id = $1`, journalID); err != nil {
		return err
	}
	for start := 0; start < len(lines); start += lineChunk {
		batch := lines[start:min(start+lineChunk, len(lines))]
		n := len(batch)
		nos, versions, afterVersions := make([]int32, n), make([]int32, n), make([]int32, n)
		ops, recordIDs, adjust, records := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		befores, afters, errs := make([]*string, n), make([]*string, n), make([]*string, n)
		for i, l := range batch {
			rec, err := json.Marshal(l.Record)
			if err != nil {
				return err
			}
			if befores[i], err = marshalRecordPtr(l.Before); err != nil {
				return err
			}
			if afters[i], err = marshalRecordPtr(l.After); err != nil {
				return err
			}
			if len(l.Errors) > 0 {
				body, err := json.Marshal(l.Errors)
				if err != nil {
					return err
				}
				text := string(body)
				errs[i] = &text
			}
			nos[i], ops[i], recordIDs[i], versions[i] = int32(l.No), string(l.Op), l.RecordID, int32(l.ExpectedVersion)
			adjust[i], records[i], afterVersions[i] = l.AdjustPct.String(), string(rec), int32(l.AfterVersion)
		}
		if _, err := tx.Exec(ctx, `insert into price_journal_line (tenant_id, journal_id, line_no, op, record_id, expected_version,
				adjust_pct, record, before, after, after_version, errors)
			select $1::uuid, $2::uuid, u.no, u.op, u.rid::uuid, u.ver, u.adj, u.rec, u.bef, u.aft, u.aver, u.errs
			from unnest($3::int[], $4::text[], $5::text[], $6::int[], $7::numeric[], $8::jsonb[], $9::jsonb[], $10::jsonb[], $11::int[], $12::jsonb[])
				as u(no, op, rid, ver, adj, rec, bef, aft, aver, errs)`,
			tenantID, journalID, nos, ops, recordIDs, versions, adjust, records, befores, afters, afterVersions, errs); err != nil {
			return err
		}
	}
	return nil
}

func marshalRecordPtr(r *domain.Record) (*string, error) {
	if r == nil {
		return nil, nil
	}
	body, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	text := string(body)
	return &text, nil
}

const journalHeader = `select id::text, legal_entity_id::text, name, status, smart_round, line_count, error_count,
	coalesce(reverses_journal_id::text, ''), coalesce(reversed_by_journal_id::text, ''), posted_at, created_at, created_by, posted_by
	from price_journal`

func scanJournal(row pgx.Row) (Journal, error) {
	var j Journal
	err := row.Scan(&j.ID, &j.LegalEntityID, &j.Name, &j.Status, &j.SmartRound, &j.LineCount, &j.ErrorCount,
		&j.ReversesID, &j.ReversedByID, &j.PostedAt, &j.CreatedAt, &j.CreatedBy, &j.PostedBy)
	return j, err
}

// loadJournal reads a journal header, optionally locking it and optionally its lines.
func loadJournal(ctx context.Context, tx pgx.Tx, id string, lock, withLines bool) (Journal, error) {
	q := journalHeader + ` where id = $1`
	if lock {
		q += ` for update`
	}
	j, err := scanJournal(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Journal{}, httpx.NewProblem(http.StatusNotFound, CodeJournalNotFound, "no price journal "+id)
	}
	if err != nil {
		return Journal{}, err
	}
	if !withLines {
		return j, nil
	}
	rows, err := tx.Query(ctx, `select line_no, op, record_id::text, expected_version, adjust_pct::text, record, before, after, after_version, errors
		from price_journal_line where journal_id = $1 order by line_no`, id)
	if err != nil {
		return Journal{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var l domain.JournalLine
		var op, adj string
		var rec, before, after, errs []byte
		if err := rows.Scan(&l.No, &op, &l.RecordID, &l.ExpectedVersion, &adj, &rec, &before, &after, &l.AfterVersion, &errs); err != nil {
			return Journal{}, err
		}
		l.Op = domain.JournalOp(op)
		l.AdjustPct = decimal.RequireFromString(adj)
		if err := json.Unmarshal(rec, &l.Record); err != nil {
			return Journal{}, err
		}
		if before != nil {
			var b domain.Record
			if err := json.Unmarshal(before, &b); err != nil {
				return Journal{}, err
			}
			l.Before = &b
		}
		if after != nil {
			var a domain.Record
			if err := json.Unmarshal(after, &a); err != nil {
				return Journal{}, err
			}
			l.After = &a
		}
		if errs != nil {
			if err := json.Unmarshal(errs, &l.Errors); err != nil {
				return Journal{}, err
			}
		}
		j.Lines = append(j.Lines, l)
	}
	return j, rows.Err()
}

// sameJournalRequest reports whether a replayed create carries the same request.
func sameJournalRequest(stored Journal, in JournalInput) bool {
	if stored.LegalEntityID != in.LegalEntityID || stored.Name != in.Name || stored.SmartRound != in.SmartRound || len(stored.Lines) != len(in.Lines) {
		return false
	}
	for i, l := range stored.Lines {
		want := in.Lines[i]
		if l.Op != want.Op || (l.Op != domain.OpCreate && l.RecordID != want.RecordID) || l.ExpectedVersion != want.ExpectedVersion ||
			!l.AdjustPct.Equal(want.AdjustPct) || (l.Op != domain.OpDelete && KeyHash(l.Record) != KeyHash(want.Record)) || !l.Record.Amount.Equal(want.Record.Amount) {
			return false
		}
	}
	return true
}

// enqueueJournal writes a journal-level event (posted or reversed) into the outbox.
func enqueueJournal(ctx context.Context, tx pgx.Tx, tenantID, journalID string, data any) error {
	evt, err := events.New(pevents.JournalPosted, eventSource, journalID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "pricejournal", journalID, evt)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
