package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	cevents "github.com/BenjaminSRussell/passage/services/compliance/internal/events"
)

// Screening error codes (listed in the service README).
const (
	CodeHitNotFound = "TRD-1008"
	CodeHitVersion  = "TRD-1009"

	hitAggregate = "screening_hit"
)

// ScreeningHit is a stored screening hit (9.48.4 screening_hit).
type ScreeningHit struct {
	HitID            string          `json:"hitId"`
	SubjectType      string          `json:"subjectType"`
	SubjectID        string          `json:"subjectId"`
	SubjectName      string          `json:"subjectName"`
	ListCode         string          `json:"listCode"`
	ListEntryID      string          `json:"listEntryId"`
	ListEntryVersion int             `json:"listEntryVersion"`
	ListEntryName    string          `json:"listEntryName"`
	Score            float64         `json:"score"`
	MatchedFields    []string        `json:"matchedFields"`
	State            domain.HitState `json:"state"`
	DecidedBy        string          `json:"decidedBy,omitempty"`
	Reason           string          `json:"reason,omitempty"`
	DecidedAt        *time.Time      `json:"decidedAt,omitempty"`
	DetectedAt       time.Time       `json:"detectedAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
	Version          int             `json:"version"`
}

// ScreeningResult is the outcome of screening one subject: the subject's
// blocking hits (OPEN and CONFIRMED) after this run, highest score first.
type ScreeningResult struct {
	Blocked bool           `json:"blocked"`
	Hits    []ScreeningHit `json:"hits"`
}

// HitFilter narrows a hit listing. Empty fields do not filter.
type HitFilter struct {
	State       string
	ListCode    string
	SubjectType string
	SubjectID   string
	Limit       int
	Cursor      string
}

// HitPage is one page of hits, newest detection first.
type HitPage struct {
	Items      []ScreeningHit `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// DecisionInput is an officer's decision on an OPEN hit.
type DecisionInput struct {
	Decision domain.Decision `json:"decision"`
	Reason   string          `json:"reason"`
}

const hitColumns = `hit_id, subject_type, subject_id, subject_name, list_code, list_entry_id, list_entry_version, list_entry_name,
  score::float8, matched_fields, state, decided_by, reason, decided_at, detected_at, updated_at, version`

func scanHit(row pgx.Row) (ScreeningHit, error) {
	var h ScreeningHit
	var decidedBy, reason *string
	var state string
	if err := row.Scan(&h.HitID, &h.SubjectType, &h.SubjectID, &h.SubjectName, &h.ListCode, &h.ListEntryID, &h.ListEntryVersion,
		&h.ListEntryName, &h.Score, &h.MatchedFields, &state, &decidedBy, &reason, &h.DecidedAt, &h.DetectedAt, &h.UpdatedAt, &h.Version); err != nil {
		return ScreeningHit{}, err
	}
	h.State, h.DecidedBy, h.Reason = domain.HitState(state), deref(decidedBy), deref(reason)
	h.MatchedFields = nonNil(h.MatchedFields)
	return h, nil
}

func scoreText(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// Screen screens one subject against the given list entries at the threshold
// (FR-48.19). False-positive decisions recorded for the subject act as the
// BR-48.15 whitelist: an entry cleared for the same entry version and the same
// subject name, address and country is not flagged again. Every new hit is
// stored OPEN and emits trade.screening.hit in the same transaction; a hit
// already OPEN for the subject and entry is refreshed, not duplicated, so
// screening again is harmless. CONFIRMED hits keep the subject blocked.
func (s *Postgres) Screen(ctx context.Context, subject domain.Subject, entries []domain.ListEntry, threshold float64, now time.Time) (ScreeningResult, error) {
	p, err := principal(ctx)
	if err != nil {
		return ScreeningResult{}, err
	}
	var out ScreeningResult
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		clearances, err := subjectClearances(ctx, tx, subject)
		if err != nil {
			return err
		}
		hits, err := domain.Screen(domain.ScreenInput{Subject: subject, Entries: entries, Threshold: threshold, Clearances: clearances})
		if err != nil {
			return err
		}
		names := map[string]string{}
		for _, e := range entries {
			names[e.ListCode+"\x00"+e.EntryID] = e.Name
		}
		fingerprint := domain.Fingerprint(subject)
		for _, h := range hits {
			id := ids.New()
			var hitID string
			var inserted bool
			err := tx.QueryRow(ctx, `insert into screening_hit (tenant_id, hit_id, subject_type, subject_id, subject_name, subject_fingerprint,
					list_code, list_entry_id, list_entry_version, list_entry_name, score, matched_fields, state, detected_at, updated_at)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'OPEN', $13, $13)
				on conflict (tenant_id, subject_type, subject_id, list_code, list_entry_id) where state <> 'FALSE_POSITIVE'
				do update set subject_name = excluded.subject_name, subject_fingerprint = excluded.subject_fingerprint,
					list_entry_version = excluded.list_entry_version, list_entry_name = excluded.list_entry_name,
					score = excluded.score, matched_fields = excluded.matched_fields, updated_at = excluded.updated_at,
					version = screening_hit.version + 1
				where screening_hit.state = 'OPEN'
					and (screening_hit.subject_fingerprint, screening_hit.list_entry_version, screening_hit.score, screening_hit.matched_fields)
						is distinct from (excluded.subject_fingerprint, excluded.list_entry_version, excluded.score, excluded.matched_fields)
				returning hit_id, (xmax = 0)`,
				p.TenantID, id, subject.Type, subject.ID, strings.TrimSpace(subject.Name), fingerprint, h.ListCode, h.EntryID, h.EntryVersion,
				names[h.ListCode+"\x00"+h.EntryID], scoreText(h.Score), h.MatchedFields, now).Scan(&hitID, &inserted)
			if errors.Is(err, pgx.ErrNoRows) {
				continue // already OPEN and unchanged, or CONFIRMED: kept as it is
			}
			if err != nil {
				return err
			}
			if !inserted {
				continue
			}
			if err := enqueueAggregate(ctx, tx, p, cevents.ScreeningHit, hitAggregate, hitID, cevents.ScreeningHitData{
				HitID: hitID, SubjectType: subject.Type, SubjectID: subject.ID, ListCode: h.ListCode, ListEntryID: h.EntryID,
				Score: scoreText(h.Score), MatchedFields: h.MatchedFields}); err != nil {
				return err
			}
		}
		out.Hits, err = blockingHits(ctx, tx, subject.Type, subject.ID)
		out.Blocked = len(out.Hits) > 0
		return err
	})
	return out, err
}

func subjectClearances(ctx context.Context, tx pgx.Tx, subject domain.Subject) ([]domain.Clearance, error) {
	rows, err := tx.Query(ctx, `select list_code, list_entry_id, list_entry_version, subject_fingerprint, reason from screening_hit
		where subject_type = $1 and subject_id = $2 and state = 'FALSE_POSITIVE'`, subject.Type, subject.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Clearance{}
	for rows.Next() {
		c := domain.Clearance{SubjectID: subject.ID}
		var reason *string
		if err := rows.Scan(&c.ListCode, &c.EntryID, &c.EntryVersion, &c.SubjectFingerprint, &reason); err != nil {
			return nil, err
		}
		c.Reason = deref(reason)
		out = append(out, c)
	}
	return out, rows.Err()
}

func blockingHits(ctx context.Context, tx pgx.Tx, subjectType, subjectID string) ([]ScreeningHit, error) {
	rows, err := tx.Query(ctx, `select `+hitColumns+` from screening_hit
		where subject_type = $1 and subject_id = $2 and state in ('OPEN', 'CONFIRMED')
		order by score desc, list_code, list_entry_id`, subjectType, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScreeningHit{}
	for rows.Next() {
		h, err := scanHit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ListHits returns hits newest detection first, keyset-paginated.
func (s *Postgres) ListHits(ctx context.Context, f HitFilter) (HitPage, error) {
	limit := f.Limit
	if limit <= 0 || limit > MaxPageSize {
		limit = MaxPageSize
	}
	where := []string{"true"}
	args := []any{}
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.State != "" {
		add("state = $%d", f.State)
	}
	if f.ListCode != "" {
		add("list_code = $%d", f.ListCode)
	}
	if f.SubjectType != "" {
		add("subject_type = $%d", f.SubjectType)
	}
	if f.SubjectID != "" {
		add("subject_id = $%d", f.SubjectID)
	}
	if f.Cursor != "" {
		at, id, err := DecodeCursor(f.Cursor)
		if err != nil {
			return HitPage{}, problem(http.StatusBadRequest, "COM-0002", "cursor is malformed")
		}
		args = append(args, at, id)
		where = append(where, fmt.Sprintf("(detected_at, hit_id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	q := `select ` + hitColumns + ` from screening_hit where ` + strings.Join(where, " and ") +
		fmt.Sprintf(` order by detected_at desc, hit_id desc limit $%d`, len(args))
	page := HitPage{Items: []ScreeningHit{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			h, err := scanHit(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, h)
		}
		return rows.Err()
	})
	if err != nil {
		return HitPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.NextCursor = cursorOf(last.DetectedAt, last.HitID)
	}
	return page, nil
}

// GetHit returns one hit.
func (s *Postgres) GetHit(ctx context.Context, id string) (ScreeningHit, error) {
	var out ScreeningHit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = hitByID(ctx, tx, id, false)
		return err
	})
	return out, err
}

func hitByID(ctx context.Context, tx pgx.Tx, id string, lock bool) (ScreeningHit, error) {
	q := `select ` + hitColumns + ` from screening_hit where hit_id = $1`
	if lock {
		q += ` for update`
	}
	h, err := scanHit(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ScreeningHit{}, problem(http.StatusNotFound, CodeHitNotFound, "no screening hit with that id")
	}
	return h, err
}

// DecideHit clears an OPEN hit as a false positive or confirms it (FR-48.19).
// The decider is the calling user, who holds compliance.trade-screening.decide (enforced
// by the API); the reason is required. A repeated Idempotency-Key replays the
// first answer; a stale expected version is 412.
func (s *Postgres) DecideHit(ctx context.Context, id string, expected int, in DecisionInput, key string, now time.Time) (ScreeningHit, error) {
	p, err := principal(ctx)
	if err != nil {
		return ScreeningHit{}, err
	}
	var out ScreeningHit
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			ID      string        `json:"id"`
			Version int           `json:"version"`
			Input   DecisionInput `json:"input"`
			Decider string        `json:"decider"`
		}{id, expected, in, p.UserID}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, key, "trade.screening.decide", request, func() (ScreeningHit, error) {
			cur, err := hitByID(ctx, tx, id, true)
			if err != nil {
				return ScreeningHit{}, err
			}
			if cur.Version != expected {
				return ScreeningHit{}, problem(http.StatusPreconditionFailed, CodeHitVersion, "the hit was changed since you loaded it; reload and decide again")
			}
			reason := strings.TrimSpace(in.Reason)
			next, err := domain.DecideHit(cur.State, in.Decision, reason, p.UserID)
			if err != nil {
				return ScreeningHit{}, err
			}
			if _, err := tx.Exec(ctx, `update screening_hit set state = $2, decided_by = $3, reason = $4, decided_at = $5, updated_at = $5,
				version = version + 1 where hit_id = $1`, id, string(next), p.UserID, reason, now); err != nil {
				return ScreeningHit{}, err
			}
			return hitByID(ctx, tx, id, false)
		})
		return err
	})
	return out, err
}
