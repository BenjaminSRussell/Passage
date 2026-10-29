package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// AuditPage is one page of the audit trail, oldest first. Next is nil on the
// last page.
type AuditPage struct {
	Items []domain.AuditEntry
	Next  *domain.AuditCursor
}

// AuditExport is a rendered audit trail export: the CSV file, its SHA-256 and
// the id under which the export itself was recorded in the trail.
type AuditExport struct {
	ID      string
	Body    []byte
	SHA256  string
	Entries int
}

// recordAuditTx appends an audit entry about a reporting change inside the
// caller's transaction, so the entry commits or rolls back with the change.
// The actor defaults to the calling user and the trace to the request id.
func recordAuditTx(ctx context.Context, tx pgx.Tx, e domain.AuditEntry) error {
	if e.ActorID == "" {
		p, _ := tenant.FromContext(ctx)
		e.ActorID = p.UserID
	}
	if e.ActorID == "" {
		e.ActorID = "system"
	}
	if e.TraceID == "" {
		// The request id is "<host>/<random>-<n>"; keep it within the
		// trace id limit whatever the host name.
		if e.TraceID = middleware.GetReqID(ctx); len(e.TraceID) > 128 {
			e.TraceID = e.TraceID[len(e.TraceID)-128:]
		}
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now()
	}
	_, err := insertAuditRowTx(ctx, tx, e)
	return err
}

// insertAuditRowTx validates and inserts one entry. Times are stored at the
// database's microsecond precision. An entry whose source event is already in
// the trail is skipped; the result reports whether a row was written.
func insertAuditRowTx(ctx context.Context, tx pgx.Tx, e domain.AuditEntry) (bool, error) {
	if err := e.Validate(); err != nil {
		return false, err
	}
	details := []byte("{}")
	if len(e.Details) > 0 {
		var err error
		if details, err = json.Marshal(e.Details); err != nil {
			return false, err
		}
	}
	if e.ID == "" {
		e.ID = ids.New()
	}
	entity := ""
	if e.LedgerID != "" {
		var err error
		if entity, err = httpx.OwnerLegalEntity(ctx, e.LegalEntityID); err != nil {
			return false, err
		}
	}
	tag, err := tx.Exec(ctx, `insert into audit_trail_entry(tenant_id, id, occurred_at, ledger_id, period, kind, actor_id, object_ref,
			details, trace_id, source_event_id, legal_entity_id)
		values($1,$2,$3,nullif($4,'')::uuid,nullif($5,''),$6,$7,$8,$9,$10,nullif($11,''),nullif($12,'')::uuid)
		on conflict (tenant_id, source_event_id) where source_event_id is not null do nothing`,
		tenantID(ctx), e.ID, e.OccurredAt.UTC().Truncate(time.Microsecond), e.LedgerID, e.Period, e.Kind, e.ActorID, e.ObjectRef,
		details, e.TraceID, e.SourceEventID, entity)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// PutAuditEntries writes audit entries built from other services' events
// (journal posted and reversed, batch approvals, period changes and reopen
// approvals, rate overrides, tax fallbacks, allocation, year-end and rebuild
// runs) in one transaction. Only the ledger kinds are accepted: the reporting
// kinds are written by this service with the change they record. An entry
// whose SourceEventID is already in the trail is a redelivery and is skipped;
// the number of entries written is returned. The consumers that will call this
// are a later slice (NOT-IMPLEMENTED.md); until then tests load entries
// through it.
func (s *Postgres) PutAuditEntries(ctx context.Context, entries []domain.AuditEntry) (int, error) {
	for i, e := range entries {
		if err := e.Validate(); err != nil {
			return 0, err
		}
		if !isLedgerKind(e.Kind) {
			return 0, &domain.Error{Code: domain.CodeAuditInvalid,
				Detail: fmt.Sprintf("entry %d: kind %s is written by the reporting service itself, not ingested", i, e.Kind)}
		}
		if e.SourceEventID == "" {
			return 0, &domain.Error{Code: domain.CodeAuditInvalid, Detail: fmt.Sprintf("entry %d: source event id is required", i)}
		}
	}
	written := 0
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		written = 0
		for _, e := range entries {
			ok, err := insertAuditRowTx(ctx, tx, e)
			if err != nil {
				return err
			}
			if ok {
				written++
			}
		}
		return nil
	})
	return written, err
}

func isLedgerKind(k string) bool {
	for _, known := range domain.AuditLedgerKinds {
		if k == known {
			return true
		}
	}
	return false
}

// auditWhere filters audit_trail_entry by an AuditFilter ($1..$6).
const auditWhere = `where ($1 = '' or ledger_id = nullif($1,'')::uuid)
	and ($2 = '' or period = $2)
	and ($3 = '' or kind = $3)
	and ($4 = '' or actor_id = $4)
	and ($5::timestamptz is null or occurred_at >= $5::timestamptz)
	and ($6::timestamptz is null or occurred_at < $6::timestamptz)`

const auditColumns = `select id::text, occurred_at, coalesce(ledger_id::text,''), coalesce(period,''), kind, actor_id, object_ref,
	details::text, trace_id, coalesce(source_event_id,'') from audit_trail_entry `

func auditArgs(f domain.AuditFilter) []any {
	return []any{f.LedgerID, f.Period, f.Kind, f.ActorID, timeOrNil(f.From), timeOrNil(f.To)}
}

// AuditTrail returns one page of the entries matching f, in (occurred at, id)
// order after the cursor. The filter is validated first, so every read is
// bounded by a period or a date window.
func (s *Postgres) AuditTrail(ctx context.Context, f domain.AuditFilter, after *domain.AuditCursor, limit int) (AuditPage, error) {
	if err := f.Validate(); err != nil {
		return AuditPage{}, err
	}
	if limit < 1 || limit > domain.AuditMaxPage {
		return AuditPage{}, fmt.Errorf("audit page size %d outside 1..%d", limit, domain.AuditMaxPage)
	}
	page := AuditPage{Items: []domain.AuditEntry{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var at, id any
		if after != nil {
			at, id = after.OccurredAt, after.ID
		}
		items, err := queryAuditTx(ctx, tx, auditColumns+auditWhere+`
			and ($7::timestamptz is null or (occurred_at, id) > ($7::timestamptz, $8::uuid))
			order by occurred_at, id limit $9`, append(auditArgs(f), at, id, limit+1)...)
		if err != nil {
			return err
		}
		if len(items) > limit {
			items = items[:limit]
			last := items[limit-1]
			page.Next = &domain.AuditCursor{OccurredAt: last.OccurredAt, ID: last.ID}
		}
		page.Items = items
		return nil
	})
	return page, err
}

// ExportAuditTrail renders every entry matching f as CSV with its SHA-256
// (FR-15.10) and records the export in the trail (kind audit_exported, with
// the filter, the entry count and the digest) in the same transaction, so
// each export file can be traced back to who took it and verified later. A
// filter matching more than AuditMaxExport entries is refused with
// REPORT-0030 rather than cut short.
func (s *Postgres) ExportAuditTrail(ctx context.Context, f domain.AuditFilter) (AuditExport, error) {
	if err := f.Validate(); err != nil {
		return AuditExport{}, err
	}
	var out AuditExport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		items, err := queryAuditTx(ctx, tx, auditColumns+auditWhere+` order by occurred_at, id limit $7`,
			append(auditArgs(f), domain.AuditMaxExport+1)...)
		if err != nil {
			return err
		}
		if len(items) > domain.AuditMaxExport {
			return problem(422, domain.CodeAuditExportTooLarge,
				fmt.Sprintf("The filter matches more than %d entries; narrow the period, ledger, kind or dates.", domain.AuditMaxExport))
		}
		body, err := domain.EncodeAuditCSV(items)
		if err != nil {
			return err
		}
		out = AuditExport{ID: ids.New(), Body: body, SHA256: domain.AuditDigest(body), Entries: len(items)}
		filter := map[string]any{}
		for k, v := range map[string]string{"ledger": f.LedgerID, "period": f.Period, "kind": f.Kind, "actor": f.ActorID} {
			if v != "" {
				filter[k] = v
			}
		}
		if !f.From.IsZero() {
			filter["from"] = f.From.UTC().Format(time.RFC3339Nano)
		}
		if !f.To.IsZero() {
			filter["to"] = f.To.UTC().Format(time.RFC3339Nano)
		}
		return recordAuditTx(ctx, tx, domain.AuditEntry{Kind: domain.AuditExported, LedgerID: f.LedgerID, Period: f.Period,
			ObjectRef: "audit-export:" + out.ID, Details: map[string]any{
				"filter": filter, "entries": out.Entries, "sha256": out.SHA256, "format": "csv",
			}})
	})
	return out, err
}

func queryAuditTx(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]domain.AuditEntry, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AuditEntry{}
	for rows.Next() {
		var e domain.AuditEntry
		var details string
		if err := rows.Scan(&e.ID, &e.OccurredAt, &e.LedgerID, &e.Period, &e.Kind, &e.ActorID, &e.ObjectRef,
			&details, &e.TraceID, &e.SourceEventID); err != nil {
			return nil, err
		}
		e.OccurredAt = e.OccurredAt.UTC()
		// UseNumber keeps amounts and counts exactly as stored: details are
		// never read through float64.
		dec := json.NewDecoder(bytes.NewReader([]byte(details)))
		dec.UseNumber()
		if err := dec.Decode(&e.Details); err != nil {
			return nil, fmt.Errorf("audit entry %s details: %w", e.ID, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// timeOrNil maps the zero time to SQL NULL so an unset bound does not filter.
func timeOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// auditDefinition is the object reference of a definition version.
func auditDefinition(code string, version int) string {
	return fmt.Sprintf("definition:%s@%d", code, version)
}
