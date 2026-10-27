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
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Catalog codes for payroll export (see README).
const (
	CodeNothingToExport = "SF-0111"
	CodeExportNotFound  = "SF-0112"
)

// PayExport is one payroll export batch. CSV is filled when the batch is
// created or read one at a time, and left empty in lists.
type PayExport struct {
	BatchID   string    `json:"batchId"`
	FromDate  string    `json:"fromDate"`
	ToDate    string    `json:"toDate"`
	LineCount int       `json:"lineCount"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	CSV       string    `json:"csv,omitempty"`
}

type exportLine struct {
	id  string
	row domain.ExportRow
}

const exportLineColumns = `pl.id::text, pl.worker_id::text, pl.work_date::text, pl.pay_type,
	pl.hours::text, pl.rate_multiplier::text, pl.amount::text, pl.currency`

func scanExportLines(rows pgx.Rows) ([]exportLine, error) {
	defer rows.Close()
	var out []exportLine
	for rows.Next() {
		var l exportLine
		r := &l.row
		if err := rows.Scan(&l.id, &r.WorkerID, &r.WorkDate, &r.PayType, &r.Hours, &r.Multiplier, &r.Amount, &r.Currency); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func exportRows(lines []exportLine) []domain.ExportRow {
	out := make([]domain.ExportRow, len(lines))
	for i, l := range lines {
		out[i] = l.row
	}
	return out
}

// ExportPay exports every approved pay line in [fromDate, toDate] that is not
// already in an export batch, emits dev.passage.shopfloor.payday.exported.v1
// with the lines in the same transaction and stores the outcome under the
// Idempotency-Key. Each line joins at most one batch (primary key on
// pay_export_line), so a second export only carries lines approved since.
// With nothing new it fails with SF-0111.
func (s *Postgres) ExportPay(ctx context.Context, fromDate, toDate, createdBy string, idem Idem) (PayExport, error) {
	p, _ := tenant.FromContext(ctx)
	var out PayExport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Exports of one tenant serialize, so two concurrent exports never pick
		// the same line (the second sees the first's pay_export_line rows).
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('shopfloor.pay-export:' || $1, 0))`, p.TenantID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+exportLineColumns+`
			from pay_line pl
			join worker_day wd on wd.tenant_id = pl.tenant_id and wd.worker_id = pl.worker_id and wd.work_date = pl.work_date
			where wd.status = 'approved' and pl.work_date between $1::date and $2::date
				and not exists (select 1 from pay_export_line x where x.tenant_id = pl.tenant_id and x.pay_line_id = pl.id)
			order by pl.work_date, pl.worker_id, pl.pay_type, pl.id`, fromDate, toDate)
		if err != nil {
			return err
		}
		lines, err := scanExportLines(rows)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeNothingToExport,
				"no approved pay lines left to export in the range")
		}
		batchID := ids.New()
		if err := tx.QueryRow(ctx, `insert into pay_export_batch (tenant_id, id, from_date, to_date, line_count, created_by)
			values ($1, $2, $3::date, $4::date, $5, $6) returning created_at`,
			p.TenantID, batchID, fromDate, toDate, len(lines), createdBy).Scan(&out.CreatedAt); err != nil {
			return err
		}
		lineIDs := make([]string, len(lines))
		data := sfevents.PaydayExportedData{BatchID: batchID, FromDate: fromDate, ToDate: toDate, LineCount: len(lines),
			CreatedBy: createdBy, Lines: make([]sfevents.ExportedLineData, 0, len(lines))}
		for i, l := range lines {
			lineIDs[i] = l.id
			d, err := exportedLine(l.row)
			if err != nil {
				return err
			}
			data.Lines = append(data.Lines, d)
		}
		if _, err := tx.Exec(ctx, `insert into pay_export_line (tenant_id, pay_line_id, batch_id)
			select $1, unnest($2::uuid[]), $3`, p.TenantID, lineIDs, batchID); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "pay-export", batchID, sfevents.PaydayExported, p.TenantID, data); err != nil {
			return err
		}
		csv, err := domain.ExportCSV(batchID, exportRows(lines))
		if err != nil {
			return err
		}
		out = PayExport{BatchID: batchID, FromDate: fromDate, ToDate: toDate, LineCount: len(lines), CreatedBy: createdBy,
			CreatedAt: out.CreatedAt, CSV: csv}
		if idem.Key == "" {
			return nil
		}
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	if err != nil {
		return PayExport{}, err
	}
	return out, nil
}

func exportedLine(r domain.ExportRow) (sfevents.ExportedLineData, error) {
	d := sfevents.ExportedLineData{WorkerID: r.WorkerID, WorkDate: r.WorkDate, Type: r.PayType, Currency: r.Currency}
	var err error
	if d.Hours, err = decimal.NewFromString(r.Hours); err != nil {
		return d, err
	}
	if d.Multiplier, err = decimal.NewFromString(r.Multiplier); err != nil {
		return d, err
	}
	d.Amount, err = decimal.NewFromString(r.Amount)
	return d, err
}

const exportColumns = `id::text, from_date::text, to_date::text, line_count, created_by, created_at`

func scanExport(row pgx.Row) (PayExport, error) {
	var e PayExport
	err := row.Scan(&e.BatchID, &e.FromDate, &e.ToDate, &e.LineCount, &e.CreatedBy, &e.CreatedAt)
	return e, err
}

// ListPayExports returns export batches, newest first, at most limit rows.
func (s *Postgres) ListPayExports(ctx context.Context, limit int) ([]PayExport, error) {
	out := []PayExport{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+exportColumns+` from pay_export_batch order by created_at desc, id limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanExport(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// GetPayExport returns one batch with its CSV rebuilt from the lines it holds
// (approved lines are immutable, so the file is the one first produced). An
// unknown id is SF-0112.
func (s *Postgres) GetPayExport(ctx context.Context, id string) (PayExport, error) {
	var out PayExport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanExport(tx.QueryRow(ctx, `select `+exportColumns+` from pay_export_batch where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeExportNotFound, "export batch not found")
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+exportLineColumns+`
			from pay_export_line x join pay_line pl on pl.tenant_id = x.tenant_id and pl.id = x.pay_line_id
			where x.batch_id = $1
			order by pl.work_date, pl.worker_id, pl.pay_type, pl.id`, id)
		if err != nil {
			return err
		}
		lines, err := scanExportLines(rows)
		if err != nil {
			return err
		}
		out.CSV, err = domain.ExportCSV(out.BatchID, exportRows(lines))
		return err
	})
	return out, err
}
