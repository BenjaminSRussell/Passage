package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
)

// Problem codes of the electronic batch record (FR-24.21).
const (
	CodeProfileNotFound   = "PMF-0115"
	CodeProfileDuplicate  = "PMF-0116"
	CodeProfileRetired    = "PMF-0117"
	CodeRecordNotFound    = "PMF-0118"
	CodeRecordNoProfile   = "PMF-0119"
	CodeRecordState       = "PMF-0120"
	CodeRecordStale       = "PMF-0121"
	CodeExceptionNotFound = "PMF-0122"
	CodeRecordNoOrder     = "PMF-0123"
	CodeExceptionDecided  = "PMF-0125"
)

// Record statuses.
const (
	RecordInReview = "in-review"
	RecordApproved = "approved"
	RecordSealed   = "sealed"
)

// RecordProfile says that an item's batches get an electronic batch record and
// whether releasing a batch needs the record approved (FR-24.21).
type RecordProfile struct {
	ID                         string     `json:"id"`
	LegalEntityID              string     `json:"legalEntityId"`
	Code                       string     `json:"code"`
	ItemID                     string     `json:"itemId"`
	ApprovalRequiredForRelease bool       `json:"approvalRequiredForRelease"`
	RetiredAt                  *time.Time `json:"retiredAt,omitempty"`
	RetiredBy                  *string    `json:"retiredBy,omitempty"`
	Version                    int        `json:"version"`
	CreatedBy                  string     `json:"createdBy"`
	CreatedAt                  time.Time  `json:"createdAt"`
	UpdatedAt                  time.Time  `json:"updatedAt"`
}

// ProfileFilter narrows the profile list; ActiveOnly drops retired profiles.
type ProfileFilter struct {
	ItemID     string
	ActiveOnly bool
	Limit      int
}

// RecordException is one exception of a batch record: a recorded value outside
// its limits (SourceType attribute) or one a reviewer raised (manual). It is
// open until it has a disposition and the signature that backs it.
type RecordException struct {
	ID                 string     `json:"id"`
	SourceType         string     `json:"sourceType"`
	SourceID           string     `json:"sourceId"`
	Description        string     `json:"description"`
	Severity           string     `json:"severity"`
	Disposition        string     `json:"disposition,omitempty"`
	DispositionComment string     `json:"dispositionComment,omitempty"`
	SignatureID        *string    `json:"signatureId,omitempty"`
	DispositionedBy    *string    `json:"dispositionedBy,omitempty"`
	DispositionedAt    *time.Time `json:"dispositionedAt,omitempty"`
	CreatedBy          string     `json:"createdBy"`
	CreatedAt          time.Time  `json:"createdAt"`
}

// Record is an electronic batch record. List results carry the counts but no
// content, exceptions or sealed document.
type Record struct {
	ID             string            `json:"id"`
	LegalEntityID  string            `json:"legalEntityId"`
	BatchOrderID   string            `json:"batchOrderId"`
	BatchID        string            `json:"batchId"`
	BatchNumber    string            `json:"batchNumber"`
	ItemID         string            `json:"itemId"`
	ProfileID      string            `json:"profileId"`
	Status         string            `json:"status"`
	CompiledAt     time.Time         `json:"compiledAt"`
	ApprovedBy     *string           `json:"approvedBy,omitempty"`
	ApprovedAt     *time.Time        `json:"approvedAt,omitempty"`
	SealedBy       *string           `json:"sealedBy,omitempty"`
	SealedAt       *time.Time        `json:"sealedAt,omitempty"`
	ContentHash    *string           `json:"contentHash,omitempty"`
	Version        int               `json:"version"`
	ExceptionCount int               `json:"exceptionCount"`
	OpenExceptions int               `json:"openExceptions"`
	CreatedBy      string            `json:"createdBy"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
	Content        json.RawMessage   `json:"content,omitempty"`
	SealedDocument *string           `json:"sealedDocument,omitempty"`
	Exceptions     []RecordException `json:"exceptions,omitempty"`
}

// RecordFilter narrows the record list.
type RecordFilter struct {
	Status  string
	BatchID string
	ItemID  string
	Limit   int
}

// NewException is the reviewer's command to raise an exception.
type NewException struct {
	Description string
	Severity    string
}

// ExceptionDisposition is the reviewer's decision on one exception, backed by
// the signature the caller obtained (FR-24.22; the signature service is not
// built yet, so the id is stored as given).
type ExceptionDisposition struct {
	Disposition string
	Comment     string
	SignatureID string
}

// ---- profiles ----

const profileCols = `id, legal_entity_id, code, item_id, approval_required_for_release, retired_at, retired_by, version, created_by, created_at, updated_at`

func scanProfile(row pgx.Row) (RecordProfile, error) {
	var p RecordProfile
	err := row.Scan(&p.ID, &p.LegalEntityID, &p.Code, &p.ItemID, &p.ApprovalRequiredForRelease, &p.RetiredAt, &p.RetiredBy,
		&p.Version, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func profileEvent(p RecordProfile) pmfevents.BatchRecordProfileData {
	return pmfevents.BatchRecordProfileData{ProfileID: p.ID, LegalEntityID: p.LegalEntityID, Code: p.Code, ItemID: p.ItemID,
		ApprovalRequiredForRelease: p.ApprovalRequiredForRelease, Version: p.Version}
}

// DefineRecordProfile stores a profile: one active profile per item and legal
// entity, and a code unique among the active ones.
func (s *Postgres) DefineRecordProfile(ctx context.Context, in RecordProfile, idem Idem) (RecordProfile, bool, error) {
	var out RecordProfile
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		p := principal(ctx)
		row, err := scanProfile(tx.QueryRow(ctx, `insert into batch_record_profile (tenant_id, id, legal_entity_id, code, item_id, approval_required_for_release, created_by)
			values ($1, $2, $3, $4, $5, $6, $7) returning `+profileCols,
			p.TenantID, ids.New(), in.LegalEntityID, in.Code, in.ItemID, in.ApprovalRequiredForRelease, p.UserID))
		if err != nil {
			return mapDuplicate(err, CodeProfileDuplicate, fmt.Sprintf("item %s already has an active batch record profile, or the code %s is taken", in.ItemID, in.Code))
		}
		if err := enqueue(ctx, tx, "batchrecordprofile", row.ID, pmfevents.BatchRecordProfileDefined, profileEvent(row)); err != nil {
			return err
		}
		out, created = row, true
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, created, err
}

// ListRecordProfiles returns profiles by item, then code.
func (s *Postgres) ListRecordProfiles(ctx context.Context, f ProfileFilter) ([]RecordProfile, error) {
	out := []RecordProfile{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+profileCols+` from batch_record_profile
			where ($1 = '' or item_id::text = $1) and (not $2 or retired_at is null) order by item_id, code, created_at limit $3`,
			f.ItemID, f.ActiveOnly, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProfile(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

func loadProfile(ctx context.Context, tx pgx.Tx, id, lock string) (RecordProfile, error) {
	p, err := scanProfile(tx.QueryRow(ctx, `select `+profileCols+` from batch_record_profile where id = $1`+lock, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, notFound(CodeProfileNotFound, "batch record profile", id)
	}
	return p, err
}

// GetRecordProfile returns one profile.
func (s *Postgres) GetRecordProfile(ctx context.Context, id string) (RecordProfile, error) {
	var out RecordProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadProfile(ctx, tx, id, "")
		return err
	})
	return out, err
}

// RetireRecordProfile ends a profile under the version the caller read. Records
// already compiled keep their profile.
func (s *Postgres) RetireRecordProfile(ctx context.Context, id string, version int, idem Idem) (RecordProfile, bool, error) {
	var out RecordProfile
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := loadProfile(ctx, tx, id, " for update")
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
				fmt.Sprintf("batch record profile %s is at version %d, not %d; reload and try again", cur.Code, cur.Version, version))
		}
		if cur.RetiredAt != nil {
			return httpx.NewProblem(http.StatusConflict, CodeProfileRetired, fmt.Sprintf("batch record profile %s is already retired", cur.Code))
		}
		if _, err := tx.Exec(ctx, `update batch_record_profile set retired_at = now(), retired_by = $2, version = version + 1, updated_at = now() where id = $1`,
			id, principal(ctx).UserID); err != nil {
			return err
		}
		if out, err = loadProfile(ctx, tx, id, ""); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "batchrecordprofile", id, pmfevents.BatchRecordProfileRetired, profileEvent(out)); err != nil {
			return err
		}
		changed = true
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, changed, err
}

// releaseGate applies the profile rule to batches about to be available
// (PMF-0015): when the item's active profile requires an approved record for
// release, every batch in batchIDs needs one. A batch made by merging stands on
// its sources: it passes when every batch it was merged from (followed back
// through earlier merges) has an approved or sealed record, so a merge cannot
// launder an unapproved batch into a releasable one. batchIDs is empty for a
// batch being registered, which has no record yet; batchNumber names it then.
func releaseGate(ctx context.Context, tx pgx.Tx, legalEntityID, itemID string, batchIDs []string, batchNumber string) error {
	var required bool
	err := tx.QueryRow(ctx, `select approval_required_for_release from batch_record_profile
		where legal_entity_id = $1 and item_id = $2 and retired_at is null`, legalEntityID, itemID).Scan(&required)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !required {
		return nil
	}
	if len(batchIDs) == 0 {
		return domain.CheckRelease(true, false, batchNumber)
	}
	var unapproved string
	err = tx.QueryRow(ctx, `with recursive src(id) as (
			select unnest($1::uuid[])
			union
			select g.parent_batch_id from batch_genealogy g join src on g.child_batch_id = src.id where g.relation = 'merge'
		)
		select b.batch_number from src join batch_record b on b.id = src.id
		where not exists (select 1 from batch_genealogy g where g.child_batch_id = src.id and g.relation = 'merge')
		  and not exists (select 1 from electronic_batch_record r where r.batch_id = src.id and r.status in ('approved', 'sealed'))
		order by b.batch_number limit 1`, batchIDs).Scan(&unapproved)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return domain.CheckRelease(true, false, unapproved)
}

// ---- records ----

const recordCols = `r.id, r.legal_entity_id, r.batch_order_id, r.batch_id, b.batch_number, b.item_id, r.profile_id, r.status, r.compiled_at,
	r.approved_by, r.approved_at, r.sealed_by, r.sealed_at, encode(r.content_hash, 'hex'), r.version,
	(select count(*) from ebr_exception e where e.record_id = r.id),
	(select count(*) from ebr_exception e where e.record_id = r.id and e.disposition is null),
	r.created_by, r.created_at, r.updated_at`

const recordFrom = ` from electronic_batch_record r join batch_record b on b.tenant_id = r.tenant_id and b.id = r.batch_id`

func scanRecord(row pgx.Row) (Record, error) {
	var r Record
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.BatchOrderID, &r.BatchID, &r.BatchNumber, &r.ItemID, &r.ProfileID, &r.Status, &r.CompiledAt,
		&r.ApprovedBy, &r.ApprovedAt, &r.SealedBy, &r.SealedAt, &r.ContentHash, &r.Version, &r.ExceptionCount, &r.OpenExceptions,
		&r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func loadExceptions(ctx context.Context, tx pgx.Tx, recordID string) ([]RecordException, error) {
	rows, err := tx.Query(ctx, `select id, source_type, source_id, description, severity, coalesce(disposition, ''), coalesce(disposition_comment, ''),
		signature_id::text, dispositioned_by, dispositioned_at, created_by, created_at
		from ebr_exception where record_id = $1 order by created_at, id`, recordID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecordException{}
	for rows.Next() {
		var e RecordException
		if err := rows.Scan(&e.ID, &e.SourceType, &e.SourceID, &e.Description, &e.Severity, &e.Disposition, &e.DispositionComment,
			&e.SignatureID, &e.DispositionedBy, &e.DispositionedAt, &e.CreatedBy, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// loadRecord reads a record with its content, exceptions and sealed document;
// lock is "" or " for update of r".
func loadRecord(ctx context.Context, tx pgx.Tx, id, lock string) (Record, error) {
	r, err := scanRecord(tx.QueryRow(ctx, `select `+recordCols+recordFrom+` where r.id = $1`+lock, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, notFound(CodeRecordNotFound, "batch record", id)
	}
	if err != nil {
		return r, err
	}
	if err := tx.QueryRow(ctx, `select content, sealed_document from electronic_batch_record where id = $1`, id).Scan(&r.Content, &r.SealedDocument); err != nil {
		return r, err
	}
	r.Exceptions, err = loadExceptions(ctx, tx, id)
	return r, err
}

func recordEvent(r Record) pmfevents.BatchRecordData {
	d := pmfevents.BatchRecordData{RecordID: r.ID, LegalEntityID: r.LegalEntityID, BatchOrderID: r.BatchOrderID, BatchID: r.BatchID,
		BatchNumber: r.BatchNumber, ItemID: r.ItemID, Status: r.Status, Version: r.Version, Exceptions: r.ExceptionCount, OpenExceptions: r.OpenExceptions}
	if r.ContentHash != nil {
		d.ContentHash = *r.ContentHash
	}
	return d
}

// ListRecords returns records newest first, without content or exceptions.
func (s *Postgres) ListRecords(ctx context.Context, f RecordFilter) ([]Record, error) {
	out := []Record{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+recordCols+recordFrom+`
			where ($1 = '' or r.status = $1) and ($2 = '' or r.batch_id::text = $2) and ($3 = '' or b.item_id::text = $3)
			order by r.created_at desc, r.id desc limit $4`, f.Status, f.BatchID, f.ItemID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRecord(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetRecord returns one record with its content and exceptions.
func (s *Postgres) GetRecord(ctx context.Context, id string) (Record, error) {
	var out Record
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadRecord(ctx, tx, id, "")
		return err
	})
	return out, err
}

// compileContent is the document of the record: the batch order as it stands
// (header, scaled lines, cost split) and the batch (attributes, dispositions).
func compileContent(ctx context.Context, tx pgx.Tx, b Batch) ([]byte, error) {
	order, err := loadOrder(ctx, tx, *b.BatchOrderID, "")
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"batchOrder": order, "batch": b})
}

func staleRecord(r Record, version int) error {
	return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
		fmt.Sprintf("batch record of batch %s is at version %d, not %d; reload and try again", r.BatchNumber, r.Version, version))
}

func recordNotInReview(r Record) error {
	return httpx.NewProblem(http.StatusConflict, CodeRecordState,
		fmt.Sprintf("batch record of batch %s is %s; only a record in review can change", r.BatchNumber, r.Status))
}

// lockRecord locks a record and returns it with the replay of a command key
// done first, so a retry never sees a later state.
func lockRecord(ctx context.Context, tx pgx.Tx, id string) (Record, error) {
	return loadRecord(ctx, tx, id, " for update of r")
}

// CompileRecord builds the electronic batch record of a batch (FR-24.21), or
// refreshes it while it is in review: the content is read again and one
// exception is raised for each recorded value outside its limits that has none
// yet (BR-24.15). The batch needs a batch order and an active profile of its
// item. A record that is approved or sealed does not change.
func (s *Postgres) CompileRecord(ctx context.Context, batchID string, idem Idem) (Record, bool, error) {
	var out Record
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		b, err := loadBatch(ctx, tx, batchID, " for update")
		if err != nil {
			return err
		}
		if b.BatchOrderID == nil {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRecordNoOrder,
				fmt.Sprintf("batch %s has no batch order; a batch record is compiled from the order", b.BatchNumber))
		}
		var profileID string
		err = tx.QueryRow(ctx, `select id::text from batch_record_profile where legal_entity_id = $1 and item_id = $2 and retired_at is null`,
			b.LegalEntityID, b.ItemID).Scan(&profileID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusConflict, CodeRecordNoProfile, fmt.Sprintf("item %s has no active batch record profile", b.ItemID))
		}
		if err != nil {
			return err
		}
		content, err := compileContent(ctx, tx, b)
		if err != nil {
			return err
		}
		p := principal(ctx)
		var recordID, status string
		err = tx.QueryRow(ctx, `select id::text, status from electronic_batch_record where batch_id = $1 for update`, b.ID).Scan(&recordID, &status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			recordID = ids.New()
			if _, err := tx.Exec(ctx, `insert into electronic_batch_record (tenant_id, id, legal_entity_id, batch_order_id, batch_id, profile_id, content, created_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8)`, p.TenantID, recordID, b.LegalEntityID, *b.BatchOrderID, b.ID, profileID, content, p.UserID); err != nil {
				return err
			}
		case err != nil:
			return err
		case status != RecordInReview:
			cur, err := loadRecord(ctx, tx, recordID, "")
			if err != nil {
				return err
			}
			return recordNotInReview(cur)
		default:
			if _, err := tx.Exec(ctx, `update electronic_batch_record set content = $2, compiled_at = now(), version = version + 1, updated_at = now() where id = $1`,
				recordID, content); err != nil {
				return err
			}
		}
		// One exception per failing value, once: the history row is the source.
		rows, err := tx.Query(ctx, `select h.seq::text, h.name, h.detail from batch_attribute_history h
			where h.batch_id = $1 and not h.pass and not exists (
			  select 1 from ebr_exception e where e.record_id = $2 and e.source_type = 'attribute' and e.source_id = h.seq::text)
			order by h.seq`, b.ID, recordID)
		if err != nil {
			return err
		}
		type failing struct{ seq, name, detail string }
		var fails []failing
		for rows.Next() {
			var f failing
			if err := rows.Scan(&f.seq, &f.name, &f.detail); err != nil {
				rows.Close()
				return err
			}
			fails = append(fails, f)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, f := range fails {
			ex := ids.New()
			if _, err := tx.Exec(ctx, `insert into ebr_exception (tenant_id, id, record_id, legal_entity_id, source_type, source_id, description, severity, created_by)
				values ($1, $2, $3, $4, 'attribute', $5, $6, 'major', $7)`,
				p.TenantID, ex, recordID, b.LegalEntityID, f.seq, domain.ExceptionDescription(f.name, f.detail), p.UserID); err != nil {
				return err
			}
			if err := enqueue(ctx, tx, "batchrecord", recordID, pmfevents.BatchRecordExceptionRaised, pmfevents.BatchRecordExceptionData{
				RecordID: recordID, ExceptionID: ex, LegalEntityID: b.LegalEntityID, BatchID: b.ID, SourceType: "attribute", Severity: "major"}); err != nil {
				return err
			}
		}
		if out, err = loadRecord(ctx, tx, recordID, ""); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "batchrecord", recordID, pmfevents.BatchRecordCompiled, recordEvent(out)); err != nil {
			return err
		}
		changed = true
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, changed, err
}

// RaiseException adds an exception a reviewer found by hand (BR-24.15) to a
// record in review, under the record version the caller read.
func (s *Postgres) RaiseException(ctx context.Context, recordID string, in NewException, version int, idem Idem) (Record, bool, error) {
	var out Record
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := lockRecord(ctx, tx, recordID)
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if r.Version != version {
			return staleRecord(r, version)
		}
		if r.Status != RecordInReview {
			return recordNotInReview(r)
		}
		p := principal(ctx)
		ex := ids.New()
		if _, err := tx.Exec(ctx, `insert into ebr_exception (tenant_id, id, record_id, legal_entity_id, source_type, source_id, description, severity, created_by)
			values ($1, $2, $3, $4, 'manual', $8, $5, $6, $7)`, p.TenantID, ex, recordID, r.LegalEntityID, in.Description, in.Severity, p.UserID, ex); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update electronic_batch_record set version = version + 1, updated_at = now() where id = $1`, recordID); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "batchrecord", recordID, pmfevents.BatchRecordExceptionRaised, pmfevents.BatchRecordExceptionData{
			RecordID: recordID, ExceptionID: ex, LegalEntityID: r.LegalEntityID, BatchID: r.BatchID, SourceType: "manual", Severity: in.Severity}); err != nil {
			return err
		}
		if out, err = loadRecord(ctx, tx, recordID, ""); err != nil {
			return err
		}
		changed = true
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, changed, err
}

// DispositionException records the decision on one open exception of a record
// in review with the signature behind it (BR-24.15). A decision is final
// (PMF-0125). The signature id is stored as given and is not verified: the
// signature service (FR-24.22) is not built.
func (s *Postgres) DispositionException(ctx context.Context, recordID, exceptionID string, in ExceptionDisposition, version int, idem Idem) (Record, bool, error) {
	var out Record
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := lockRecord(ctx, tx, recordID)
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if r.Version != version {
			return staleRecord(r, version)
		}
		if r.Status != RecordInReview {
			return recordNotInReview(r)
		}
		var decided bool
		found := false
		for _, e := range r.Exceptions {
			if e.ID == exceptionID {
				found, decided = true, e.Disposition != ""
			}
		}
		if !found {
			return notFound(CodeExceptionNotFound, "exception", exceptionID)
		}
		if decided {
			// A signed decision is part of the audit trail; it is never overwritten.
			return httpx.NewProblem(http.StatusConflict, CodeExceptionDecided,
				fmt.Sprintf("exception %s is already decided; a decision is final (raise another exception to record a correction)", exceptionID))
		}
		p := principal(ctx)
		if _, err := tx.Exec(ctx, `update ebr_exception set disposition = $3, disposition_comment = nullif($4, ''), signature_id = $5, dispositioned_by = $6, dispositioned_at = now()
			where record_id = $1 and id = $2`, recordID, exceptionID, in.Disposition, in.Comment, in.SignatureID, p.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update electronic_batch_record set version = version + 1, updated_at = now() where id = $1`, recordID); err != nil {
			return err
		}
		var src, sev string
		if err := tx.QueryRow(ctx, `select source_type, severity from ebr_exception where id = $1`, exceptionID).Scan(&src, &sev); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "batchrecord", recordID, pmfevents.BatchRecordExceptionDisposed, pmfevents.BatchRecordExceptionData{
			RecordID: recordID, ExceptionID: exceptionID, LegalEntityID: r.LegalEntityID, BatchID: r.BatchID, SourceType: src, Severity: sev,
			Disposition: in.Disposition, SignatureID: in.SignatureID}); err != nil {
			return err
		}
		if out, err = loadRecord(ctx, tx, recordID, ""); err != nil {
			return err
		}
		changed = true
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, changed, err
}

// ApproveRecord approves a record in review (BR-24.15): every exception needs a
// disposition and a signature (PMF-0014), and every recorded value outside its
// limits must have its exception, so a value recorded after the last compile is
// refused (PMF-0121) until the record is compiled again. The approver must not
// have raised or decided an exception of the record nor created its batch order
// (PMF-0124, PRD 4.24.17).
func (s *Postgres) ApproveRecord(ctx context.Context, id string, version int, idem Idem) (Record, bool, error) {
	var out Record
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := lockRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if r.Version != version {
			return staleRecord(r, version)
		}
		if r.Status != RecordInReview {
			return recordNotInReview(r)
		}
		var missing int
		if err := tx.QueryRow(ctx, `select count(*) from batch_attribute_history h where h.batch_id = $1 and not h.pass and not exists (
			select 1 from ebr_exception e where e.record_id = $2 and e.source_type = 'attribute' and e.source_id = h.seq::text)`,
			r.BatchID, id).Scan(&missing); err != nil {
			return err
		}
		if missing > 0 {
			return httpx.NewProblem(http.StatusConflict, CodeRecordStale,
				fmt.Sprintf("%d values outside limits were recorded after the record was compiled; compile it again", missing))
		}
		ex := make([]domain.Exception, len(r.Exceptions))
		for i, e := range r.Exceptions {
			ex[i] = domain.Exception{ID: e.ID, Disposition: e.Disposition}
			if e.SignatureID != nil {
				ex[i].SignatureID = *e.SignatureID
			}
		}
		if err := domain.CheckApprove(ex); err != nil {
			return err
		}
		var reviewers []string
		for _, e := range r.Exceptions {
			if e.SourceType == "manual" {
				reviewers = append(reviewers, e.CreatedBy)
			}
			if e.DispositionedBy != nil {
				reviewers = append(reviewers, *e.DispositionedBy)
			}
		}
		var orderCreator string
		if err := tx.QueryRow(ctx, `select created_by from batch_order where id = $1`, r.BatchOrderID).Scan(&orderCreator); err != nil {
			return err
		}
		approver := principal(ctx).UserID
		if err := domain.CheckApproverIndependent(approver, reviewers, orderCreator); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update electronic_batch_record set status = 'approved', approved_by = $2, approved_at = now(), version = version + 1, updated_at = now() where id = $1`,
			id, approver); err != nil {
			return err
		}
		if out, err = loadRecord(ctx, tx, id, ""); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "batchrecord", id, pmfevents.BatchRecordApproved, recordEvent(out)); err != nil {
			return err
		}
		changed = true
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, changed, err
}

// sealedDocument is what is hashed: the record, the content that was approved
// and its exceptions with their decisions. Times are UTC. SignaturesVerified
// says whether the signature ids were checked against the signature service;
// until that service exists (FR-24.22) it is false, so the sealed record never
// claims more than was checked.
type sealedDocument struct {
	RecordID           string            `json:"recordId"`
	LegalEntityID      string            `json:"legalEntityId"`
	BatchOrderID       string            `json:"batchOrderId"`
	BatchID            string            `json:"batchId"`
	BatchNumber        string            `json:"batchNumber"`
	ItemID             string            `json:"itemId"`
	ProfileID          string            `json:"profileId"`
	CompiledAt         time.Time         `json:"compiledAt"`
	ApprovedBy         string            `json:"approvedBy"`
	ApprovedAt         time.Time         `json:"approvedAt"`
	SealedBy           string            `json:"sealedBy"`
	SealedAt           time.Time         `json:"sealedAt"`
	SignaturesVerified bool              `json:"signaturesVerified"`
	Content            json.RawMessage   `json:"content"`
	Exceptions         []RecordException `json:"exceptions"`
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// SealRecord seals an approved record (FR-24.21): the canonical JSON document of
// the record, the content as it was approved and the exceptions is stored with
// its SHA-256, and the record never changes again (the database refuses it).
// The content is not compiled again: what is sealed is what the approver
// approved. The hash can be checked by hashing sealedDocument.
func (s *Postgres) SealRecord(ctx context.Context, id string, version int, idem Idem) (Record, bool, error) {
	var out Record
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := lockRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if r.Version != version {
			return staleRecord(r, version)
		}
		if r.Status != RecordApproved {
			return httpx.NewProblem(http.StatusConflict, CodeRecordState,
				fmt.Sprintf("batch record of batch %s is %s; only an approved record is sealed", r.BatchNumber, r.Status))
		}
		p := principal(ctx)
		sealedAt := time.Now().UTC().Truncate(time.Microsecond)
		exceptions := make([]RecordException, len(r.Exceptions))
		for i, e := range r.Exceptions {
			e.CreatedAt = e.CreatedAt.UTC()
			e.DispositionedAt = utcPtr(e.DispositionedAt)
			exceptions[i] = e
		}
		doc := sealedDocument{RecordID: r.ID, LegalEntityID: r.LegalEntityID, BatchOrderID: r.BatchOrderID, BatchID: r.BatchID,
			BatchNumber: r.BatchNumber, ItemID: r.ItemID, ProfileID: r.ProfileID, CompiledAt: r.CompiledAt.UTC(),
			ApprovedBy: *r.ApprovedBy, ApprovedAt: r.ApprovedAt.UTC(), SealedBy: p.UserID, SealedAt: sealedAt,
			SignaturesVerified: false, Content: r.Content, Exceptions: exceptions}
		canon, err := domain.CanonicalJSON(doc)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(canon)
		if _, err := tx.Exec(ctx, `update electronic_batch_record set status = 'sealed', sealed_document = $2, content_hash = $3,
			sealed_by = $4, sealed_at = $5, version = version + 1, updated_at = now() where id = $1`,
			id, string(canon), sum[:], p.UserID, sealedAt); err != nil {
			return err
		}
		if out, err = loadRecord(ctx, tx, id, ""); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "batchrecord", id, pmfevents.BatchRecordSealed, recordEvent(out)); err != nil {
			return err
		}
		changed = true
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, changed, err
}

// HashHex is the hex SHA-256 of a sealed document, for checking a record's
// contentHash.
func HashHex(document string) string {
	sum := sha256.Sum256([]byte(document))
	return hex.EncodeToString(sum[:])
}
