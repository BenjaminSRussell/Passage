package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// SettlementExportInput names the period of invoice dates and the destination of one export.
type SettlementExportInput struct {
	LegalEntityID string
	Target        string
	From          time.Time
	To            time.Time
}

// SettlementExportFilter narrows the export list. Empty fields do not filter.
type SettlementExportFilter struct {
	LegalEntityID string
	Target        string
	Status        string
	Limit         int
}

// Dates and the timestamp are formatted explicitly, not with ::text, so the frozen file (whose
// from and to columns come from period_from and period_to) and createdAt do not depend on the
// session's DateStyle or TimeZone.
const settlementExportColumns = `id::text, legal_entity_id::text, target, to_char(period_from, 'YYYY-MM-DD'),
	to_char(period_to, 'YYYY-MM-DD'), status, lines, csv_sha256, exported_by, void_reason, voided_by, version,
	to_char(created_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`

// storedLine is the jsonb form of one export row. Amounts are decimal strings.
type storedLine struct {
	RepID        string `json:"repId"`
	Currency     string `json:"currency"`
	Amount       string `json:"amount"`
	Transactions int    `json:"transactions"`
}

func scanSettlementExport(row pgx.Row) (domain.SettlementExport, error) {
	var e domain.SettlementExport
	var raw []byte
	if err := row.Scan(&e.ID, &e.LegalEntityID, &e.Target, &e.From, &e.To, &e.Status, &raw, &e.CSVSHA256,
		&e.ExportedBy, &e.VoidReason, &e.VoidedBy, &e.Version, &e.CreatedAt); err != nil {
		return domain.SettlementExport{}, err
	}
	var stored []storedLine
	if err := json.Unmarshal(raw, &stored); err != nil {
		return domain.SettlementExport{}, err
	}
	e.Lines = make([]domain.SettlementRow, len(stored))
	for i, l := range stored {
		amount, err := decimal.NewFromString(l.Amount)
		if err != nil {
			return domain.SettlementExport{}, err
		}
		e.Lines[i] = domain.SettlementRow{RepID: l.RepID, Currency: l.Currency, Amount: amount, Transactions: l.Transactions}
	}
	return e, nil
}

func exportNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeExportNotFound, "no settlement export "+id)
}

func exportEventData(e domain.SettlementExport, userID string) pevents.CommissionExportData {
	totals := domain.ExportTotals(e.Lines)
	out := pevents.CommissionExportData{ExportID: e.ID, LegalEntityID: e.LegalEntityID, Target: e.Target, PeriodFrom: e.From,
		PeriodTo: e.To, LineCount: len(e.Lines), Totals: make([]pevents.CommissionExportTotal, len(totals)),
		CSVSHA256: e.CSVSHA256, Version: e.Version, UserID: userID, VoidReason: e.VoidReason}
	for i, t := range totals {
		out.Totals[i] = pevents.CommissionExportTotal{Currency: t.Currency, Amount: t.Amount.StringFixed(money.MinorUnits(t.Currency))}
	}
	return out
}

// exportLockKey names the advisory lock that serializes the exports of one legal entity and
// target, so two callers cannot both pass the overlap check for the same period. The ids are
// canonicalized: uuid.Parse accepts upper case, braces and urn forms, and two spellings of the
// same legal entity must take the same lock.
func exportLockKey(tenantID, legalEntityID, target string) string {
	if c, ok := ids.Canonical(tenantID); ok {
		tenantID = c
	}
	if c, ok := ids.Canonical(legalEntityID); ok {
		legalEntityID = c
	}
	return "commission-export:" + tenantID + ":" + legalEntityID + ":" + target
}

// CreateSettlementExport sums the commission of the period, records it as ISSUED with the
// rows frozen, and announces it in the same transaction. A period that overlaps an ISSUED
// export of the same legal entity and target is PRC-0065 (void that export first); a period
// without commission is PRC-0066. A repeated Idempotency-Key returns the first export.
func (s *Postgres) CreateSettlementExport(ctx context.Context, in SettlementExportInput, idempotencyKey string) (domain.SettlementExport, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.SettlementExport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, exportLockKey(p.TenantID, in.LegalEntityID, in.Target)); err != nil {
			return err
		}
		hash := commandHash("settlement-export", in.LegalEntityID, in.Target, dayStr(in.From), dayStr(in.To))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		var clash string
		err := tx.QueryRow(ctx, `select id::text from commission_settlement_export
			where legal_entity_id = $1::uuid and target = $2 and status = 'ISSUED'
			  and period_from <= $4::date and period_to >= $3::date order by period_from, id limit 1`,
			in.LegalEntityID, in.Target, dayStr(in.From), dayStr(in.To)).Scan(&clash)
		if err == nil {
			return httpx.NewProblem(http.StatusConflict, domain.CodeExportOverlap,
				"settlement export "+clash+" already covers part of this period for "+in.Target+"; void it before exporting again")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		rows, err := settlementTotals(ctx, tx, in.LegalEntityID, in.From, in.To)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeExportEmpty, "no commission was posted for invoices in this period")
		}
		raw, err := domain.SettlementCSV(in.Target, dayStr(in.From), dayStr(in.To), rows)
		if err != nil {
			return err
		}
		stored := make([]storedLine, len(rows))
		for i, r := range rows {
			stored[i] = storedLine{RepID: r.RepID, Currency: r.Currency, Amount: r.Amount.String(), Transactions: r.Transactions}
		}
		lines, err := json.Marshal(stored)
		if err != nil {
			return err
		}
		var key *string
		if idempotencyKey != "" {
			key = &idempotencyKey
		}
		out, err = scanSettlementExport(tx.QueryRow(ctx, `insert into commission_settlement_export
			(tenant_id, id, legal_entity_id, target, period_from, period_to, lines, line_count, csv_sha256, exported_by, idempotency_key)
			values ($1, $2, $3, $4, $5::date, $6::date, $7::jsonb, $8, $9, $10, $11)
			returning `+settlementExportColumns,
			p.TenantID, ids.New(), in.LegalEntityID, in.Target, dayStr(in.From), dayStr(in.To), lines, len(rows),
			domain.CSVDigest(raw), p.UserID, key))
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "commissionexport", out.ID, pevents.CommissionExportIssued, exportEventData(out, p.UserID)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// GetSettlementExport reads one recorded export with its frozen rows.
func (s *Postgres) GetSettlementExport(ctx context.Context, id string) (domain.SettlementExport, error) {
	var e domain.SettlementExport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		e, err = scanSettlementExport(tx.QueryRow(ctx, `select `+settlementExportColumns+` from commission_settlement_export where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return exportNotFound(id)
		}
		return err
	})
	return e, err
}

// ListSettlementExports returns exports newest first (by period end, then id).
func (s *Postgres) ListSettlementExports(ctx context.Context, f SettlementExportFilter) ([]domain.SettlementExport, error) {
	limit := f.Limit
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	out := []domain.SettlementExport{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+settlementExportColumns+` from commission_settlement_export
			where ($1::text = '' or legal_entity_id::text = $1) and ($2::text = '' or target = $2) and ($3::text = '' or status = $3)
			order by period_to desc, id desc limit $4`, f.LegalEntityID, f.Target, f.Status, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanSettlementExport(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// VoidSettlementExport marks an ISSUED export VOIDED at the expected version (412
// conflict_version otherwise), so its period can be exported again. A voided export is
// PRC-0031. The reason is required and is part of the event.
func (s *Postgres) VoidSettlementExport(ctx context.Context, id string, version int, reason, idempotencyKey string) (domain.SettlementExport, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.SettlementExport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		e, err := scanSettlementExport(tx.QueryRow(ctx, `select `+settlementExportColumns+` from commission_settlement_export where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return exportNotFound(id)
		}
		if err != nil {
			return err
		}
		hash := commandHash("settlement-export-void", id, strconv.Itoa(version), reason)
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if e.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "settlement export version does not match")
		}
		if e.Status != domain.ExportIssued {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "settlement export is already voided")
		}
		out, err = scanSettlementExport(tx.QueryRow(ctx, `update commission_settlement_export
			set status = 'VOIDED', void_reason = $2, voided_by = $3, voided_at = now(), version = version + 1
			where id = $1 returning `+settlementExportColumns, id, reason, p.UserID))
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "commissionexport", out.ID, pevents.CommissionExportVoided, exportEventData(out, p.UserID)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}
