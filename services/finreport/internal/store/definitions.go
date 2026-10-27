package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

// Version selectors for GetDefinition.
const (
	Draft  = 0  // the single editable draft
	Latest = -1 // the newest published version (BR-15.8)
)

// DefinitionSummary is one definition code in the designer list: its newest
// name and kind, the newest published version (0 when never published),
// whether a draft is open, and the code's status: published when a version can
// run, retired when every version was retired (BR-15.8), draft otherwise.
type DefinitionSummary struct {
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	LatestVersion int       `json:"latestVersion"`
	HasDraft      bool      `json:"hasDraft"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ListDefinitions returns one summary per definition code, ordered by code.
func (s *Postgres) ListDefinitions(ctx context.Context) ([]DefinitionSummary, error) {
	out := []DefinitionSummary{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select code,
				(array_agg(name order by (status = 'draft') desc, version desc))[1],
				(array_agg(kind order by (status = 'draft') desc, version desc))[1],
				coalesce(max(version) filter (where status = 'published'), 0),
				bool_or(status = 'draft'),
				case when bool_or(status = 'published') then 'published'
					when bool_or(status = 'retired') then 'retired' else 'draft' end,
				max(updated_at)
			from report_definition group by code order by code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d DefinitionSummary
			if err := rows.Scan(&d.Code, &d.Name, &d.Kind, &d.LatestVersion, &d.HasDraft, &d.Status, &d.UpdatedAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// SaveDraft creates or replaces the draft of def.Code. When a draft exists the
// caller must send the revision it read (ifMatch); otherwise the write is
// refused with 428 (missing) or 412 conflict_version (stale). The returned
// revision is the new ETag.
func (s *Postgres) SaveDraft(ctx context.Context, def domain.Definition, ifMatch *int) (domain.Definition, int, error) {
	var revision int
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		def, revision, err = saveDraftTx(ctx, tx, def, ifMatch, false)
		return err
	})
	return def, revision, err
}

// saveDraftTx writes the draft inside tx. replace skips the If-Match check
// (an import with overwrite=true replaces whatever draft is open).
func saveDraftTx(ctx context.Context, tx pgx.Tx, def domain.Definition, ifMatch *int, replace bool) (domain.Definition, int, error) {
	def.Version = 0
	def.Status = "draft"
	def.SchemaVersion = domain.SchemaVersion
	body, err := json.Marshal(def)
	if err != nil {
		return def, 0, err
	}
	var current int
	err = tx.QueryRow(ctx, `select revision from report_definition where code=$1 and status='draft' for update`, def.Code).Scan(&current)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_, err = tx.Exec(ctx, `insert into report_definition(tenant_id, id, code, version, status, revision, name, kind, body, updated_at)
			values($1,$2,$3,0,'draft',1,$4,$5,$6,now())`, tenantID(ctx), ids.New(), def.Code, def.Name, string(def.Kind), body)
		if isUniqueViolation(err) {
			return def, 0, problem(409, CodeRevisionConflict, "a draft for "+def.Code+" was created concurrently")
		}
		return def, 1, err
	case err != nil:
		return def, 0, err
	}
	if !replace {
		if ifMatch == nil {
			return def, 0, problem(428, "precondition_required", "If-Match is required to replace the draft of "+def.Code)
		}
		if *ifMatch != current {
			return def, 0, problem(412, CodeRevisionConflict, fmt.Sprintf("draft revision is %d, not %d", current, *ifMatch))
		}
	}
	revision := current + 1
	_, err = tx.Exec(ctx, `update report_definition set name=$2, kind=$3, body=$4, revision=$5, updated_at=now()
		where code=$1 and status='draft'`, def.Code, def.Name, string(def.Kind), body, revision)
	return def, revision, err
}

// importResponse is what an import stores against its Idempotency-Key.
type importResponse struct {
	Definition domain.Definition `json:"definition"`
	Revision   int               `json:"revision"`
}

// ImportDefinition stores an exported definition as the draft of its code
// (FR-15.11) in one transaction. A code that already has a draft or a published
// version is refused with REPORT-0011 unless overwrite is set. With a key, a
// repeated request returns the stored result (replayed=true) and a different
// request under the same key is refused (idempotency_key_reuse).
func (s *Postgres) ImportDefinition(ctx context.Context, def domain.Definition, overwrite bool, key, requestHash string) (saved domain.Definition, revision int, replayed bool, err error) {
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			var hash string
			var body []byte
			err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where key=$1`, key).Scan(&hash, &body)
			if err == nil {
				if hash != requestHash {
					return problem(422, CodeIdempotencyReuse, "Idempotency-Key was used for a different request")
				}
				var stored importResponse
				if err := json.Unmarshal(body, &stored); err != nil {
					return err
				}
				saved, revision, replayed = stored.Definition, stored.Revision, true
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var existing int
		if err := tx.QueryRow(ctx, `select count(*) from report_definition where code=$1`, def.Code).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 && !overwrite {
			return problem(409, CodeImportDuplicate, fmt.Sprintf("Definition code %s exists; use overwrite to replace.", def.Code))
		}
		var err error
		if saved, revision, err = saveDraftTx(ctx, tx, def, nil, true); err != nil {
			return err
		}
		if key == "" {
			return nil
		}
		body, err := json.Marshal(importResponse{Definition: saved, Revision: revision})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into idempotency_keys(tenant_id, key, request_hash, response_status, response_body) values($1,$2,$3,201,$4)`,
			tenantID(ctx), key, requestHash, body)
		if isUniqueViolation(err) {
			return problem(409, CodeIdempotencyInFlight, "a request with this Idempotency-Key is already being processed; retry")
		}
		return err
	})
	return saved, revision, replayed, err
}

// GetDefinition returns version (Draft, Latest or an exact version) of a
// definition and the draft revision (0 for other versions). Latest is the
// newest non-draft version. Retired versions stay readable, so the runs that
// used them still render (BR-15.8); their Status says retired.
func (s *Postgres) GetDefinition(ctx context.Context, code string, version int) (domain.Definition, int, error) {
	var def domain.Definition
	var body []byte
	var revision int
	var status string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		switch version {
		case Draft:
			return tx.QueryRow(ctx, `select body, revision, status from report_definition where code=$1 and status='draft'`, code).Scan(&body, &revision, &status)
		case Latest:
			return tx.QueryRow(ctx, `select body, 0, status from report_definition where code=$1 and status<>'draft' order by version desc limit 1`, code).Scan(&body, &revision, &status)
		default:
			return tx.QueryRow(ctx, `select body, 0, status from report_definition where code=$1 and status<>'draft' and version=$2`, code, version).Scan(&body, &revision, &status)
		}
	})
	if errors.Is(err, pgx.ErrNoRows) {
		which := "latest published"
		switch {
		case version == Draft:
			which = "draft"
		case version > 0:
			which = fmt.Sprintf("version %d", version)
		}
		return def, 0, problem(404, CodeDefinitionNotFound, fmt.Sprintf("definition %s has no %s", code, which))
	}
	if err != nil {
		return def, 0, err
	}
	if err := json.Unmarshal(body, &def); err != nil {
		return def, 0, err
	}
	def.Status = status
	return def, revision, nil
}

// Publish turns the draft of code into the next immutable published version
// (BR-15.8). Invalid drafts are refused with the first validation finding.
func (s *Postgres) Publish(ctx context.Context, code, actor string) (domain.Definition, error) {
	var out domain.Definition
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = publishTx(ctx, tx, code, actor)
		return err
	})
	return out, err
}

// publishTx publishes the draft of code inside tx. Version numbers count
// retired versions too, so a code published again after retiring gets a new
// number instead of colliding with its history.
func publishTx(ctx context.Context, tx pgx.Tx, code, actor string) (domain.Definition, error) {
	var out domain.Definition
	err := func() error {
		var draftID string
		var body []byte
		err := tx.QueryRow(ctx, `select id::text, body from report_definition where code=$1 and status='draft' for update`, code).Scan(&draftID, &body)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(404, CodeDefinitionNotFound, "no draft of "+code+" to publish")
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return err
		}
		if issues := out.Validate(); len(issues) > 0 {
			return problem(422, issues[0].Code, issues[0].Field+": "+issues[0].Detail)
		}
		var next int
		if err := tx.QueryRow(ctx, `select coalesce(max(version),0)+1 from report_definition where code=$1 and status<>'draft'`, code).Scan(&next); err != nil {
			return err
		}
		out.Version = next
		out.Status = "published"
		out.SchemaVersion = domain.SchemaVersion
		published, err := json.Marshal(out)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update report_definition set version=$2, status='published', body=$3, published_at=now(), updated_at=now()
			where id=$1::uuid`, draftID, next, published); err != nil {
			return err
		}
		return enqueue(ctx, tx, "definition", code, events.DefinitionPublished, tenantID(ctx), events.DefinitionPublishedData{
			Code: code, Version: next, Actor: actor, DiffSummary: fmt.Sprintf("%d rows, %d columns", len(out.Rows), len(out.Columns)),
		})
	}()
	return out, err
}

// RetireDefinition retires every published version of code (BR-15.8): runs
// can no longer use them, while the versions and the runs that used them stay
// readable. An open draft is kept; publishing it later makes the code runnable
// again under the next version number. Retiring a code that is already retired
// succeeds again (the action is safe to repeat); a code that was never
// published is REPORT-0020.
func (s *Postgres) RetireDefinition(ctx context.Context, code string) (retired int, err error) {
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update report_definition set status='retired', updated_at=now() where code=$1 and status='published'`, code)
		if err != nil {
			return err
		}
		if retired = int(tag.RowsAffected()); retired > 0 {
			return nil
		}
		var already bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from report_definition where code=$1 and status='retired')`, code).Scan(&already); err != nil {
			return err
		}
		if !already {
			return problem(404, CodeDefinitionNotFound, "definition "+code+" has no published version to retire")
		}
		return nil
	})
	return retired, err
}

// InstallResult reports what InstallStandard did with one standard definition.
type InstallResult struct {
	Code    string `json:"code"`
	Action  string `json:"action"` // installed or skipped
	Version int    `json:"version,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// InstallStandard publishes each definition as version 1 of its code in one
// transaction, so either every missing definition is installed or none is.
// A code that already has any version (draft, published or retired) is
// skipped and left untouched: install never overwrites a tenant's own edits
// or brings back a retired statement. Repeating the call is therefore safe.
func (s *Postgres) InstallStandard(ctx context.Context, defs []domain.Definition, actor string) ([]InstallResult, error) {
	out := make([]InstallResult, 0, len(defs))
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out = out[:0]
		for _, def := range defs {
			var status string
			err := tx.QueryRow(ctx, `select status from report_definition where code=$1
				order by (status = 'published') desc, (status = 'draft') desc limit 1`, def.Code).Scan(&status)
			switch {
			case err == nil:
				out = append(out, InstallResult{Code: def.Code, Action: "skipped", Reason: "the code already has a " + status + " version"})
				continue
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
			if _, _, err := saveDraftTx(ctx, tx, def, nil, false); err != nil {
				return err
			}
			pub, err := publishTx(ctx, tx, def.Code, actor)
			if err != nil {
				return err
			}
			out = append(out, InstallResult{Code: def.Code, Action: "installed", Version: pub.Version})
		}
		return nil
	})
	return out, err
}
