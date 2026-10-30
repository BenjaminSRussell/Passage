package store

import (
	"context"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// RevisedDay is one worker-day of a revision. Changed is false for a day whose pay
// did not change. ReversedLines carry negative hours and amounts.
type RevisedDay struct {
	WorkDate      string    `json:"workDate"`
	Status        string    `json:"status"`
	Revision      int       `json:"revision"`
	Changed       bool      `json:"changed"`
	ReversedLines []PayLine `json:"reversedLines"`
	AddedLines    []PayLine `json:"addedLines"`
}

// RevisionResult is the outcome of RevisePay, stored for Idempotency-Key replays.
type RevisionResult struct {
	WorkerID string       `json:"workerId"`
	From     string       `json:"from"`
	To       string       `json:"to"`
	Reason   string       `json:"reason"`
	Days     []RevisedDay `json:"days"`
}

// RevisePay applies a recalculation to a range that may hold approved days (BR-25.8,
// AC-25.8). Days that are not approved are replaced like pay:calculate does. An
// approved day keeps its lines: lines that differ from the recalculation are
// cancelled by reversal lines (negative hours and amount, reversal_of set) and the
// new lines are added, all stamped with the day's next revision, and
// dev.passage.shopfloor.payday.revised.v1 is published in the same transaction. A
// revision with no difference in any day is SF-0129; a recalculated approved day
// with open flags is SF-0006; the worker's own user cannot revise (SF-0106).
func (s *Postgres) RevisePay(ctx context.Context, calc PayCalculation, reason, revisedBy string, idem Idem) (RevisionResult, error) {
	p, _ := tenant.FromContext(ctx)
	out := RevisionResult{WorkerID: calc.WorkerID, From: calc.FromDate, To: calc.ToDate, Reason: reason, Days: []RevisedDay{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		userID, err := lockWorker(ctx, tx, p.TenantID, calc.WorkerID)
		if err != nil {
			return err
		}
		if userID != "" && userID == revisedBy {
			return httpx.NewProblem(http.StatusForbidden, CodeOwnApproval, "you cannot revise your own pay")
		}
		var currency string
		if err := tx.QueryRow(ctx, `select currency from worker where tenant_id = $1 and id = $2`, p.TenantID, calc.WorkerID).Scan(&currency); err != nil {
			return err
		}
		type existing struct {
			status   string
			revision int
		}
		have := map[string]existing{}
		rows, err := tx.Query(ctx, `select work_date::text, status, revision from worker_day
			where tenant_id = $1 and worker_id = $2 and work_date between $3::date and $4::date for update`, p.TenantID, calc.WorkerID, calc.FromDate, calc.ToDate)
		if err != nil {
			return err
		}
		for rows.Next() {
			var date string
			var e existing
			if err := rows.Scan(&date, &e.status, &e.revision); err != nil {
				rows.Close()
				return err
			}
			have[date] = e
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		fresh := map[string]domain.DayPay{}
		dates := map[string]bool{}
		for _, d := range calc.Days {
			key := d.Date.Format(dateLayout)
			fresh[key], dates[key] = d, true
		}
		for k := range have {
			dates[k] = true
		}
		ordered := make([]string, 0, len(dates))
		for k := range dates {
			ordered = append(ordered, k)
		}
		sort.Strings(ordered)
		changed := 0
		for _, date := range ordered {
			nd, hasNew := fresh[date]
			ex, hasEx := have[date]
			day := RevisedDay{WorkDate: date, Status: "calculated", ReversedLines: []PayLine{}, AddedLines: []PayLine{}}
			switch {
			case !hasEx || ex.status == "calculated":
				if hasEx {
					if _, err := tx.Exec(ctx, `delete from pay_line where tenant_id = $1 and worker_id = $2 and work_date = $3::date`, p.TenantID, calc.WorkerID, date); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `delete from worker_day where tenant_id = $1 and worker_id = $2 and work_date = $3::date`, p.TenantID, calc.WorkerID, date); err != nil {
						return err
					}
				}
				if hasNew {
					if err := insertDay(ctx, tx, p.TenantID, calc.WorkerID, currency, calc.TimeZone, nd); err != nil {
						return err
					}
					day.Changed = true
					for _, l := range nd.Lines {
						day.AddedLines = append(day.AddedLines, PayLine{Type: l.Type, Hours: l.Hours, Multiplier: l.Multiplier, Amount: l.Amount, Currency: currency})
					}
				} else {
					day.Changed = hasEx
				}
			default:
				day.Status, day.Revision = "approved", ex.revision
				if hasNew && len(nd.Flags) > 0 {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeDayFlagged, "Day cannot be revised: "+date+" has open flags.")
				}
				if err := reviseApproved(ctx, tx, p.TenantID, calc.WorkerID, currency, date, ex.revision, nd, hasNew, reason, revisedBy, &day); err != nil {
					return err
				}
			}
			if day.Changed {
				changed++
			}
			out.Days = append(out.Days, day)
		}
		if changed == 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeRevisionNothingChanged, "the recalculation matches the stored pay; nothing to revise")
		}
		if idem.Key == "" {
			return nil
		}
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	if err != nil {
		return RevisionResult{}, err
	}
	return out, nil
}

// reviseApproved reverses and adds the lines of one approved day.
func reviseApproved(ctx context.Context, tx pgx.Tx, tenantID, workerID, currency, date string, revision int, nd domain.DayPay, hasNew bool, reason, by string, day *RevisedDay) error {
	rows, err := tx.Query(ctx, `select l.id::text, l.pay_type, l.hours::text, l.rate_multiplier::text, l.amount::text, l.source_registration_ids::text[]
		from pay_line l
		where l.tenant_id = $1 and l.worker_id = $2 and l.work_date = $3::date and l.reversal_of is null
		  and not exists (select 1 from pay_line r where r.tenant_id = l.tenant_id and r.reversal_of = l.id)
		order by l.revision, l.pay_type, l.id`, tenantID, workerID, date)
	if err != nil {
		return err
	}
	var effective []domain.EffectiveLine
	sources := map[string][]string{}
	for rows.Next() {
		var e domain.EffectiveLine
		var hours, mult, amount string
		var src []string
		if err := rows.Scan(&e.ID, &e.Type, &hours, &mult, &amount, &src); err != nil {
			rows.Close()
			return err
		}
		if e.Hours, err = decimal.NewFromString(hours); err != nil {
			rows.Close()
			return err
		}
		if e.Multiplier, err = decimal.NewFromString(mult); err != nil {
			rows.Close()
			return err
		}
		if e.Amount, err = decimal.NewFromString(amount); err != nil {
			rows.Close()
			return err
		}
		sources[e.ID] = src
		effective = append(effective, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var fresh []domain.PayLine
	if hasNew {
		fresh = nd.Lines
	}
	plan := domain.PlanRevision(effective, fresh)
	if plan.Empty() {
		return nil
	}
	next := revision + 1
	data := sfevents.PaydayRevisedData{WorkerID: workerID, WorkDate: date, Revision: next, Reason: reason,
		ReversedLines: []sfevents.PayLineData{}, AddedLines: []sfevents.PayLineData{}, RevisedBy: by}
	for _, e := range plan.Reverse {
		neg := PayLine{Type: e.Type, Hours: e.Hours.Neg(), Multiplier: e.Multiplier, Amount: e.Amount.Neg(), Currency: currency, Revision: next, Reversal: true}
		if _, err := tx.Exec(ctx, `insert into pay_line (tenant_id, id, worker_id, work_date, pay_type, hours, rate_multiplier, amount, currency, source_registration_ids, revision, reversal_of)
			values ($1, $2, $3, $4::date, $5, $6, $7, $8, $9, $10::text[]::uuid[], $11, $12)`,
			tenantID, ids.New(), workerID, date, e.Type, neg.Hours.String(), e.Multiplier.String(), neg.Amount.String(), currency, nonNil(sources[e.ID]), next, e.ID); err != nil {
			return err
		}
		day.ReversedLines = append(day.ReversedLines, neg)
		data.ReversedLines = append(data.ReversedLines, sfevents.PayLineData{Type: neg.Type, Hours: neg.Hours, Multiplier: neg.Multiplier, Amount: neg.Amount, Currency: currency})
	}
	for _, l := range plan.Add {
		if _, err := tx.Exec(ctx, `insert into pay_line (tenant_id, id, worker_id, work_date, pay_type, hours, rate_multiplier, amount, currency, source_registration_ids, revision)
			values ($1, $2, $3, $4::date, $5, $6, $7, $8, $9, $10::text[]::uuid[], $11)`,
			tenantID, ids.New(), workerID, date, l.Type, l.Hours.String(), l.Multiplier.String(), l.Amount.String(), currency, nonNil(l.Sources), next); err != nil {
			return err
		}
		added := PayLine{Type: l.Type, Hours: l.Hours, Multiplier: l.Multiplier, Amount: l.Amount, Currency: currency, Revision: next}
		day.AddedLines = append(day.AddedLines, added)
		data.AddedLines = append(data.AddedLines, sfevents.PayLineData{Type: l.Type, Hours: l.Hours, Multiplier: l.Multiplier, Amount: l.Amount, Currency: currency})
	}
	hours := decimal.Zero
	if hasNew {
		hours = nd.Hours
	}
	if _, err := tx.Exec(ctx, `update worker_day set hours = $4, revision = $5, revised_by = $6, revised_at = now(), revision_reason = $7
		where tenant_id = $1 and worker_id = $2 and work_date = $3::date`, tenantID, workerID, date, hours.String(), next, by, reason); err != nil {
		return err
	}
	day.Revision, day.Changed = next, true
	return enqueue(ctx, tx, workerSource, workerID, sfevents.PaydayRevised, tenantID, data)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
