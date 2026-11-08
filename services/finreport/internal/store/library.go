package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// CodeLibraryNotFound is raised when a report library entry does not exist (or
// is outside the caller's legal-entity scope).
const CodeLibraryNotFound = "REPORT-0043"

// Library page sizes (FR-15.8: browse by definition, period and entity).
const (
	LibraryDefaultPage = 50
	LibraryMaxPage     = 200
)

// LibraryEntry is the metadata of one stored output file. The file itself is
// served by LibraryContent.
type LibraryEntry struct {
	ID                string    `json:"id"`
	RunID             string    `json:"runId"`
	ScheduleCode      string    `json:"scheduleCode"`
	DefinitionCode    string    `json:"definitionCode"`
	DefinitionVersion int       `json:"definitionVersion"`
	LedgerID          string    `json:"ledgerId"`
	LegalEntityID     string    `json:"legalEntityId,omitempty"`
	PeriodYear        int       `json:"periodYear"`
	PeriodNo          int       `json:"periodNo"`
	CurrencyType      string    `json:"currencyType"`
	Format            string    `json:"format"`
	FileName          string    `json:"fileName"`
	ContentType       string    `json:"contentType"`
	SizeBytes         int64     `json:"sizeBytes"`
	SHA256            string    `json:"sha256"`
	RetainUntil       time.Time `json:"retainUntil"`
	CreatedAt         time.Time `json:"createdAt"`
	// OnHold is true while the entry's legal entity is under legal hold, so the
	// retention sweep keeps the file past its retention date (FR-15.8).
	OnHold bool `json:"onHold"`
}

// LibraryQuery narrows the library list. Empty and zero fields do not filter.
type LibraryQuery struct {
	Definition   string
	LedgerID     string
	ScheduleCode string
	Format       string
	PeriodYear   int
	PeriodNo     int // only with PeriodYear
}

// LibraryPage is one keyset page of the library, newest first.
type LibraryPage struct {
	Items []LibraryEntry
	Next  *domain.AuditCursor // created-at and id of the last item, when more follow
}

const libraryColumns = `id::text, run_id::text, schedule_code, definition_code, definition_version, ledger_id::text,
	coalesce(legal_entity_id::text, ''), period_year, period_no, currency_type, format, file_name, content_type, size_bytes, sha256,
	retain_until, created_at,
	exists(select 1 from report_legal_hold h where h.legal_entity_id = report_library_entry.legal_entity_id and h.released_at is null)`

func scanLibrary(row pgx.Row) (LibraryEntry, error) {
	var e LibraryEntry
	err := row.Scan(&e.ID, &e.RunID, &e.ScheduleCode, &e.DefinitionCode, &e.DefinitionVersion, &e.LedgerID, &e.LegalEntityID,
		&e.PeriodYear, &e.PeriodNo, &e.CurrencyType, &e.Format, &e.FileName, &e.ContentType, &e.SizeBytes, &e.SHA256,
		&e.RetainUntil, &e.CreatedAt, &e.OnHold)
	return e, err
}

// ListLibrary returns the report library, newest first, filtered and paged by
// keyset. The files are not read.
func (s *Postgres) ListLibrary(ctx context.Context, q LibraryQuery, after *domain.AuditCursor, limit int) (LibraryPage, error) {
	if limit < 1 || limit > LibraryMaxPage {
		limit = LibraryDefaultPage
	}
	page := LibraryPage{Items: []LibraryEntry{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var cursorAt *time.Time
		cursorID := ""
		if after != nil {
			t := after.OccurredAt
			cursorAt, cursorID = &t, after.ID
		}
		rows, err := tx.Query(ctx, `select `+libraryColumns+` from report_library_entry
			where ($1 = '' or definition_code = $1)
			  and ($2 = '' or ledger_id = $2::uuid)
			  and ($3 = '' or schedule_code = $3)
			  and ($4 = '' or format = $4)
			  and ($5 = 0 or (period_year = $5 and period_no = $6))
			  and ($7::timestamptz is null or (created_at, id) < ($7, nullif($8, '')::uuid))
			order by created_at desc, id desc limit $9`,
			q.Definition, q.LedgerID, q.ScheduleCode, q.Format, q.PeriodYear, q.PeriodNo, cursorAt, cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanLibrary(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, e)
		}
		return rows.Err()
	})
	if err != nil {
		return LibraryPage{Items: []LibraryEntry{}}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.Next = &domain.AuditCursor{OccurredAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

// GetLibraryEntry returns one entry's metadata.
func (s *Postgres) GetLibraryEntry(ctx context.Context, id string) (LibraryEntry, error) {
	e, _, err := s.libraryEntry(ctx, id, false)
	return e, err
}

// LibraryContent returns an entry and its stored file.
func (s *Postgres) LibraryContent(ctx context.Context, id string) (LibraryEntry, []byte, error) {
	return s.libraryEntry(ctx, id, true)
}

func (s *Postgres) libraryEntry(ctx context.Context, id string, withContent bool) (LibraryEntry, []byte, error) {
	var e LibraryEntry
	var content []byte
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		e, content, err = libraryEntryTx(ctx, tx, id, withContent)
		return err
	})
	return e, content, err
}

// libraryEntryTx reads one entry, and its file when withContent, inside tx.
func libraryEntryTx(ctx context.Context, tx pgx.Tx, id string, withContent bool) (LibraryEntry, []byte, error) {
	var e LibraryEntry
	var content []byte
	var err error
	if withContent {
		row := tx.QueryRow(ctx, `select `+libraryColumns+`, content from report_library_entry where id=$1::uuid`, id)
		err = row.Scan(&e.ID, &e.RunID, &e.ScheduleCode, &e.DefinitionCode, &e.DefinitionVersion, &e.LedgerID, &e.LegalEntityID,
			&e.PeriodYear, &e.PeriodNo, &e.CurrencyType, &e.Format, &e.FileName, &e.ContentType, &e.SizeBytes, &e.SHA256,
			&e.RetainUntil, &e.CreatedAt, &e.OnHold, &content)
	} else {
		e, err = scanLibrary(tx.QueryRow(ctx, `select `+libraryColumns+` from report_library_entry where id=$1::uuid`, id))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return LibraryEntry{}, nil, problem(404, CodeLibraryNotFound, "report library entry "+id+" does not exist")
	}
	return e, content, err
}
