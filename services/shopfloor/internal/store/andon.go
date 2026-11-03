package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Catalog codes for the andon conditions (see README).
const (
	// CodeAndonNotFound is SF-0162: no open andon alert has that id.
	CodeAndonNotFound = "SF-0162"
	// CodeAndonNotClearable is SF-0163: only a quality fail alert is cleared by hand; a downtime
	// or scrap rate alert clears itself once its condition has been clear for 5 minutes (BR-25.10).
	CodeAndonNotClearable = "SF-0163"
)

// andonOpen is one alert to open.
type andonOpen struct {
	ResourceID string
	SiteID     *string
	Kind       string
	DowntimeID *string
	JobID      *string
	Detail     string
	StartedAt  time.Time
}

// openAndon opens one alert per (resource, kind) (BR-25.10) and emits
// dev.passage.shopfloor.andon.raised.v1 in the same transaction. While an alert of that kind is
// open for the resource a second open is a no-op and returns 0.
func openAndon(ctx context.Context, tx pgx.Tx, tenantID string, in andonOpen) (int, error) {
	alertID := ids.New()
	in.Detail = clipDetail(in.Detail)
	var got string
	err := tx.QueryRow(ctx, `insert into andon_alert (tenant_id, id, site_id, resource_id, kind, severity, downtime_id, job_id, detail, started_at)
		values ($1, $2, $3, $4, $5, 'warning', $6, $7, nullif($8, ''), $9)
		on conflict (tenant_id, resource_id, kind) where cleared_at is null do nothing
		returning id::text`, tenantID, alertID, in.SiteID, in.ResourceID, in.Kind, in.DowntimeID, in.JobID, in.Detail, in.StartedAt).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err := enqueue(ctx, tx, "andon", alertID, sfevents.AndonRaised, tenantID, sfevents.AndonRaisedData{
		AlertID: alertID, ResourceID: in.ResourceID, Kind: in.Kind, Severity: "warning", JobID: in.JobID,
		StartedAt: in.StartedAt.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return 0, err
	}
	return 1, nil
}

// maxAndonDetail is the longest detail andon_alert keeps (migration 0019).
const maxAndonDetail = 500

// clipDetail cuts a detail line to maxAndonDetail characters, so a job with many failed checks or
// long order references still raises its alert instead of failing the feedback posting.
func clipDetail(detail string) string {
	r := []rune(detail)
	if len(r) <= maxAndonDetail {
		return detail
	}
	return string(r[:maxAndonDetail-1]) + "…"
}

// siteOfResource is the site of the machine registered for a resource, nil without one.
func siteOfResource(ctx context.Context, tx pgx.Tx, tenantID, resourceID string) (*string, error) {
	var site *string
	err := tx.QueryRow(ctx, `select site_id::text from machine where tenant_id = $1 and resource_id = $2 order by created_at, id limit 1`,
		tenantID, resourceID).Scan(&site)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return site, err
}

// qualityFailAndon raises or clears the quality fail alert of a job's resource after a feedback
// posting (FR-25.11). The posting's failed values raise it (one per resource, BR-25.10, naming the
// job of the first failure); a posting after which the alert's own job holds no failed result
// clears it, since that condition is gone. A failure another job on the resource recorded while
// the alert was open was folded into it, so clearing it raises a new alert for that job.
func qualityFailAndon(ctx context.Context, tx pgx.Tx, tenantID string, job Job, postedFailed, jobFailed []string, now time.Time) error {
	if len(postedFailed) > 0 {
		site, err := siteOfResource(ctx, tx, tenantID, job.ResourceID)
		if err != nil {
			return err
		}
		jobID := job.ID
		_, err = openAndon(ctx, tx, tenantID, andonOpen{
			ResourceID: job.ResourceID, SiteID: site, Kind: domain.AndonQualityFail, JobID: &jobID, StartedAt: now,
			Detail: qualityFailDetail(job.OrderRef, job.OperationRef, postedFailed),
		})
		return err
	}
	if len(jobFailed) > 0 {
		return nil
	}
	var started time.Time
	err := tx.QueryRow(ctx, `update andon_alert set cleared_at = greatest($4, started_at)
		where tenant_id = $1 and resource_id = $2 and job_id = $3 and kind = 'quality-fail' and cleared_at is null
		returning started_at`,
		tenantID, job.ResourceID, job.ID, now).Scan(&started)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return carryQualityFail(ctx, tx, tenantID, job.ResourceID, job.ID, started)
}

func qualityFailDetail(orderRef, operationRef string, failed []string) string {
	return fmt.Sprintf("job %s %s: failed %s", orderRef, operationRef, strings.Join(failed, ", "))
}

// carryQualityFail raises a quality fail alert for the latest job on the resource, other than the
// cleared alert's job, that still holds a failed check result recorded since the cleared alert
// started. BR-25.10 keeps one open alert per resource and kind, so that failure was folded into
// the alert just cleared and would otherwise never be shown.
func carryQualityFail(ctx context.Context, tx pgx.Tx, tenantID, resourceID, clearedJobID string, since time.Time) error {
	var jobID, orderRef, operationRef string
	var at time.Time
	var codes []string
	err := tx.QueryRow(ctx, `select j.id::text, j.order_ref, j.operation_ref, max(r.recorded_at), array_agg(r.check_code order by r.check_code)
		from shop_job j join job_check_result r on r.tenant_id = j.tenant_id and r.job_id = j.id
		where j.tenant_id = $1 and j.resource_id = $2 and j.id <> $3 and not r.pass and r.recorded_at >= $4
		group by j.id, j.order_ref, j.operation_ref
		order by max(r.recorded_at) desc, j.id
		limit 1`, tenantID, resourceID, clearedJobID, since).Scan(&jobID, &orderRef, &operationRef, &at, &codes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	site, err := siteOfResource(ctx, tx, tenantID, resourceID)
	if err != nil {
		return err
	}
	_, err = openAndon(ctx, tx, tenantID, andonOpen{
		ResourceID: resourceID, SiteID: site, Kind: domain.AndonQualityFail, JobID: &jobID, StartedAt: at,
		Detail: qualityFailDetail(orderRef, operationRef, codes),
	})
	return err
}

// AndonSweepResult counts what one andon sweep did.
type AndonSweepResult struct {
	DowntimeRaised  int `json:"downtimeRaised"`
	ScrapRateRaised int `json:"scrapRateRaised"`
	Cleared         int `json:"cleared"`
}

// SweepAndon is the scheduled andon job (FR-25.11, BR-25.10). Raising and clearing a downtime
// alert otherwise happen only when the machine sends events, so a machine that went silent
// while DOWN would never raise one. The sweep, in one transaction under the caller's tenant:
//   - raises a downtime alert for every downtime open for more than 10 minutes;
//   - clears the downtime alerts whose downtime ended at least 5 minutes ago;
//   - reads the latest completed stored OEE bucket of each resource and raises a scrap rate
//     alert when more than 5 % of its pieces are scrap, or clears the open one once the latest
//     completed bucket is back at or under 5 % and ended at least 5 minutes ago.
//
// Repeating it changes nothing, so it takes no Idempotency-Key.
func (s *Postgres) SweepAndon(ctx context.Context, now time.Time) (AndonSweepResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out AndonSweepResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The machine row is the lock ingest takes per machine. A machine whose events are being
		// ingested right now is skipped: that ingest raises its alert itself if it is due, and
		// waiting on it could deadlock on the downtime and andon rows both transactions write.
		rows, err := tx.Query(ctx, `select m.id::text, m.resource_id::text, m.site_id::text, d.id::text, d.started_at
			from downtime_record d join machine m on m.tenant_id = d.tenant_id and m.id = d.machine_id
			where d.tenant_id = $1 and d.ended_at is null and not d.andon_raised and d.started_at < $2
			order by d.started_at, d.id
			for update of m skip locked`, p.TenantID, now.Add(-domain.AndonAfter))
		if err != nil {
			return err
		}
		type due struct {
			m machineRow
			d openDown
		}
		var dues []due
		for rows.Next() {
			var x due
			if err := rows.Scan(&x.m.id, &x.m.resourceID, &x.m.siteID, &x.d.id, &x.d.start); err != nil {
				rows.Close()
				return err
			}
			dues = append(dues, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, x := range dues {
			d := x.d
			n, err := raiseAndon(ctx, tx, p.TenantID, x.m, &d)
			if err != nil {
				return err
			}
			out.DowntimeRaised += n
		}
		if out.Cleared, err = clearAndon(ctx, tx, p.TenantID, "", now); err != nil {
			return err
		}
		return sweepScrapRate(ctx, tx, p.TenantID, now, &out)
	})
	return out, err
}

// sweepScrapRate applies the scrap rate rule to the latest completed bucket of every machine's
// resource (the OEE rollup stores buckets only for machines).
func sweepScrapRate(ctx context.Context, tx pgx.Tx, tenantID string, now time.Time, out *AndonSweepResult) error {
	// Per machine resource (one machine per resource) and per bucket width, the newest completed
	// bucket is one backward primary-key probe; of those, the one ending last wins, the narrowest
	// on a tie. This keeps the per-minute sweep off the full bucket history (3-year retention).
	rows, err := tx.Query(ctx, `select m.resource_id::text, b.bucket_start, b.bucket_minutes, b.total_count, b.good_count
		from (select distinct resource_id from machine where tenant_id = $1) m
		cross join lateral (
			select x.bucket_start, x.bucket_minutes, x.total_count, x.good_count
			from (values (5), (15), (60)) w(minutes)
			cross join lateral (
				select o.bucket_start, o.bucket_minutes, o.total_count, o.good_count from oee_bucket o
				where o.tenant_id = $1 and o.resource_id = m.resource_id and o.bucket_minutes = w.minutes
				and o.bucket_start <= $2::timestamptz - make_interval(mins => w.minutes)
				order by o.bucket_start desc limit 1) x
			order by x.bucket_start + make_interval(mins => x.bucket_minutes) desc, x.bucket_minutes
			limit 1) b
		order by m.resource_id`, tenantID, now)
	if err != nil {
		return err
	}
	type latest struct {
		resourceID  string
		start       time.Time
		minutes     int
		total, good int64
	}
	var all []latest
	for rows.Next() {
		var l latest
		if err := rows.Scan(&l.resourceID, &l.start, &l.minutes, &l.total, &l.good); err != nil {
			rows.Close()
			return err
		}
		all = append(all, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range all {
		end := l.start.Add(time.Duration(l.minutes) * time.Minute)
		if domain.ScrapRateExceeded(l.total, l.good) {
			site, err := siteOfResource(ctx, tx, tenantID, l.resourceID)
			if err != nil {
				return err
			}
			n, err := openAndon(ctx, tx, tenantID, andonOpen{
				ResourceID: l.resourceID, SiteID: site, Kind: domain.AndonScrapRate, StartedAt: end,
				Detail: fmt.Sprintf("%d of %d pieces scrapped in the %d-minute bucket from %s", l.total-l.good, l.total, l.minutes, l.start.UTC().Format(time.RFC3339)),
			})
			if err != nil {
				return err
			}
			out.ScrapRateRaised += n
			continue
		}
		if domain.AndonCleared(end, now) {
			tag, err := tx.Exec(ctx, `update andon_alert set cleared_at = greatest($3, started_at)
				where tenant_id = $1 and resource_id = $2 and kind = 'scrap-rate' and cleared_at is null`, tenantID, l.resourceID, now)
			if err != nil {
				return err
			}
			out.Cleared += int(tag.RowsAffected())
		}
	}
	return nil
}

// ClearAndon is a supervisor's clear of a quality fail alert with a reason (FR-25.11). An unknown
// or already cleared alert is SF-0162; a downtime or scrap rate alert is SF-0163, since the
// machine's own data clears those. A failure another job on the resource recorded while the alert
// was open raises a new alert for that job (carryQualityFail). The outcome is stored under the
// Idempotency-Key.
func (s *Postgres) ClearAndon(ctx context.Context, id, reason, clearedBy string, now time.Time, idem Idem) (Andon, error) {
	p, _ := tenant.FromContext(ctx)
	var out Andon
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var kind string
		err := tx.QueryRow(ctx, `select kind from andon_alert where tenant_id = $1 and id = $2 and cleared_at is null for update`,
			p.TenantID, id).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeAndonNotFound, "no open andon alert with that id")
		}
		if err != nil {
			return err
		}
		if kind != domain.AndonQualityFail {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeAndonNotClearable,
				"a "+kind+" alert clears itself once its condition has been clear for 5 minutes; only a quality-fail alert is cleared by hand")
		}
		if err := tx.QueryRow(ctx, `update andon_alert set cleared_at = greatest($3, started_at), cleared_by = $4, clear_reason = $5
			where tenant_id = $1 and id = $2
			returning id::text, resource_id::text, site_id::text, kind, severity, downtime_id::text, job_id::text, detail, started_at`,
			p.TenantID, id, now, clearedBy, reason).
			Scan(&out.ID, &out.ResourceID, &out.SiteID, &out.Kind, &out.Severity, &out.DowntimeID, &out.JobID, &out.Detail, &out.StartedAt); err != nil {
			return err
		}
		if out.JobID != nil {
			if err := carryQualityFail(ctx, tx, p.TenantID, out.ResourceID, *out.JobID, out.StartedAt); err != nil {
				return err
			}
		}
		if idem.Key == "" {
			return nil
		}
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, err
}
