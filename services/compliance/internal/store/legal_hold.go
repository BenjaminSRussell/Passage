package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	cevents "github.com/BenjaminSRussell/passage/services/compliance/internal/events"
)

const (
	CodeHoldNotFound = "PRV-1221"
	CodeHoldVersion  = "PRV-1223"

	holdAggregate    = "legal_hold"
	erasureAggregate = "erasure_command"
)

type Hold struct {
	HoldID        string     `json:"holdId"`
	LegalEntityID string     `json:"legalEntityId"`
	SubjectRef    string     `json:"subjectRef,omitempty"`
	DataClass     string     `json:"dataClass,omitempty"`
	RecordRef     string     `json:"recordRef,omitempty"`
	Reason        string     `json:"reason"`
	State         string     `json:"state"`
	PlacedBy      string     `json:"placedBy"`
	PlacedAt      time.Time  `json:"placedAt"`
	ReleasedBy    string     `json:"releasedBy,omitempty"`
	ReleasedAt    *time.Time `json:"releasedAt,omitempty"`
	ReleaseReason string     `json:"releaseReason,omitempty"`
	Version       int        `json:"version"`
}

type HoldInput struct {
	domain.HoldScope
	Reason string `json:"reason"`
}

type HoldFilter struct {
	State         string
	LegalEntityID string
	SubjectRef    string
	DataClass     string
	Limit         int
	Cursor        string
}

type HoldPage struct {
	Items      []Hold `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type HoldCheck struct {
	Held    bool     `json:"held"`
	HoldIDs []string `json:"holdIds"`
}

type ErasureInput struct {
	LegalEntityID string   `json:"legalEntityId"`
	SubjectRef    string   `json:"subjectRef"`
	DataClasses   []string `json:"dataClasses,omitempty"`
	RequestID     string   `json:"requestId,omitempty"`
}

type ErasureCommand struct {
	ErasureID     string    `json:"erasureId"`
	RequestID     string    `json:"requestId,omitempty"`
	LegalEntityID string    `json:"legalEntityId"`
	SubjectRef    string    `json:"subjectRef"`
	DataClasses   []string  `json:"dataClasses"`
	RequestedBy   string    `json:"requestedBy"`
	RequestedAt   time.Time `json:"requestedAt"`
}

const holdColumns = `hold_id, legal_entity_id, subject_ref, data_class, record_ref, reason, state, placed_by, placed_at,
  released_by, released_at, release_reason, version`

func scanHold(row pgx.Row) (Hold, error) {
	var h Hold
	var subject, class, record, releasedBy, releaseReason *string
	if err := row.Scan(&h.HoldID, &h.LegalEntityID, &subject, &class, &record, &h.Reason, &h.State, &h.PlacedBy, &h.PlacedAt,
		&releasedBy, &h.ReleasedAt, &releaseReason, &h.Version); err != nil {
		return Hold{}, err
	}
	h.SubjectRef, h.DataClass, h.RecordRef = deref(subject), deref(class), deref(record)
	h.ReleasedBy, h.ReleaseReason = deref(releasedBy), deref(releaseReason)
	return h, nil
}

func holdByID(ctx context.Context, tx pgx.Tx, id string, lock bool) (Hold, error) {
	q := `select ` + holdColumns + ` from legal_hold where hold_id = $1`
	if lock {
		q += ` for update`
	}
	h, err := scanHold(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Hold{}, problem(http.StatusNotFound, CodeHoldNotFound, "no legal hold with that id")
	}
	return h, err
}

func nullable(s string) *string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	return &s
}

func lockLegalEntity(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, tenantID+":"+legalEntityID)
	return err
}

func coveringHolds(ctx context.Context, tx pgx.Tx, legalEntityID, subject string, classes []string, record string) ([]string, error) {
	rows, err := tx.Query(ctx, `select hold_id::text from legal_hold
		where state = 'ACTIVE' and legal_entity_id = $1
		  and (subject_ref is null or subject_ref = $2)
		  and (data_class is null or cardinality($3::text[]) = 0 or data_class = any($3))
		  and (record_ref is null or record_ref = $4)
		order by hold_id`, legalEntityID, subject, classes, record)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Postgres) PlaceHold(ctx context.Context, in HoldInput, key string, now time.Time) (Hold, error) {
	p, err := principal(ctx)
	if err != nil {
		return Hold{}, err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	in.SubjectRef, in.RecordRef = strings.TrimSpace(in.SubjectRef), strings.TrimSpace(in.RecordRef)
	if err := in.ValidHold(); err != nil {
		return Hold{}, err
	}
	if err := domain.ValidHoldReason(in.Reason); err != nil {
		return Hold{}, err
	}
	var out Hold
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			Input  HoldInput `json:"input"`
			Placer string    `json:"placer"`
		}{in, p.UserID}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, key, "privacy.hold.place", request, func() (Hold, error) {
			if err := lockLegalEntity(ctx, tx, p.TenantID, in.LegalEntityID); err != nil {
				return Hold{}, err
			}
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into legal_hold (tenant_id, hold_id, legal_entity_id, subject_ref, data_class, record_ref, reason, placed_by, placed_at)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				p.TenantID, id, in.LegalEntityID, nullable(in.SubjectRef), nullable(in.DataClass), nullable(in.RecordRef), in.Reason, p.UserID, now); err != nil {
				return Hold{}, err
			}
			if err := enqueueAggregate(ctx, tx, p, cevents.HoldPlaced, holdAggregate, id, cevents.HoldPlacedData{
				HoldID: id, LegalEntityID: in.LegalEntityID, SubjectRef: in.SubjectRef, DataClass: in.DataClass, RecordRef: in.RecordRef,
				PlacedBy: p.UserID, PlacedAt: now}); err != nil {
				return Hold{}, err
			}
			return holdByID(ctx, tx, id, false)
		})
		return err
	})
	if err == nil {
		slog.InfoContext(ctx, "legal hold placed", "holdId", out.HoldID, "legalEntityId", out.LegalEntityID, "tenantId", p.TenantID, "actor", p.UserID)
	}
	return out, err
}

func (s *Postgres) ReleaseHold(ctx context.Context, id string, expected int, reason string, now time.Time) (Hold, error) {
	p, err := principal(ctx)
	if err != nil {
		return Hold{}, err
	}
	reason = strings.TrimSpace(reason)
	if err := domain.ValidHoldReason(reason); err != nil {
		return Hold{}, err
	}
	var out Hold
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := holdByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return problem(http.StatusPreconditionFailed, CodeHoldVersion, "the hold was changed since you loaded it; reload and try again")
		}
		if cur.State != "ACTIVE" {
			return &domain.Error{Code: domain.CodeHoldState, Detail: "the hold is already released"}
		}
		if _, err := tx.Exec(ctx, `update legal_hold set state = 'RELEASED', released_by = $2, released_at = $3, release_reason = $4, version = version + 1
			where hold_id = $1`, id, p.UserID, now, reason); err != nil {
			return err
		}
		if err := enqueueAggregate(ctx, tx, p, cevents.HoldReleased, holdAggregate, id, cevents.HoldReleasedData{
			HoldID: id, LegalEntityID: cur.LegalEntityID, ReleasedBy: p.UserID, ReleasedAt: now}); err != nil {
			return err
		}
		out, err = holdByID(ctx, tx, id, false)
		return err
	})
	if err == nil {
		slog.InfoContext(ctx, "legal hold released", "holdId", id, "tenantId", p.TenantID, "actor", p.UserID)
	}
	return out, err
}

func (s *Postgres) GetHold(ctx context.Context, id string) (Hold, error) {
	var out Hold
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = holdByID(ctx, tx, id, false)
		return err
	})
	return out, err
}

func (s *Postgres) ListHolds(ctx context.Context, f HoldFilter) (HoldPage, error) {
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
	if f.LegalEntityID != "" {
		add("legal_entity_id = $%d", f.LegalEntityID)
	}
	if f.SubjectRef != "" {
		add("subject_ref = $%d", f.SubjectRef)
	}
	if f.DataClass != "" {
		add("data_class = $%d", f.DataClass)
	}
	if f.Cursor != "" {
		at, id, err := DecodeCursor(f.Cursor)
		if err != nil {
			return HoldPage{}, problem(http.StatusBadRequest, "COM-0002", "cursor is malformed")
		}
		args = append(args, at, id)
		where = append(where, fmt.Sprintf("(placed_at, hold_id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	q := `select ` + holdColumns + ` from legal_hold where ` + strings.Join(where, " and ") +
		fmt.Sprintf(` order by placed_at desc, hold_id desc limit $%d`, len(args))
	page := HoldPage{Items: []Hold{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			h, err := scanHold(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, h)
		}
		return rows.Err()
	})
	if err != nil {
		return HoldPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.NextCursor = cursorOf(last.PlacedAt, last.HoldID)
	}
	return page, nil
}

func (s *Postgres) CheckHolds(ctx context.Context, target domain.HoldScope) (HoldCheck, error) {
	var found []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		found, err = coveringHolds(ctx, tx, target.LegalEntityID, target.SubjectRef, classList(target.DataClass), target.RecordRef)
		return err
	})
	return HoldCheck{Held: len(found) > 0, HoldIDs: found}, err
}

func classList(class string) []string {
	if class == "" {
		return []string{}
	}
	return []string{class}
}

func (s *Postgres) RequestErasure(ctx context.Context, in ErasureInput, key string, now time.Time) (ErasureCommand, error) {
	p, err := principal(ctx)
	if err != nil {
		return ErasureCommand{}, err
	}
	in.SubjectRef = strings.TrimSpace(in.SubjectRef)
	if in.DataClasses == nil {
		in.DataClasses = []string{}
	}
	var out ErasureCommand
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			Input     ErasureInput `json:"input"`
			Requester string       `json:"requester"`
		}{in, p.UserID}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, key, "privacy.erasure.request", request, func() (ErasureCommand, error) {
			if err := lockLegalEntity(ctx, tx, p.TenantID, in.LegalEntityID); err != nil {
				return ErasureCommand{}, err
			}
			if in.RequestID != "" {
				if err := checkErasureRequest(ctx, tx, in); err != nil {
					return ErasureCommand{}, err
				}
			}
			held, err := coveringHolds(ctx, tx, in.LegalEntityID, in.SubjectRef, in.DataClasses, "")
			if err != nil {
				return ErasureCommand{}, err
			}
			if len(held) > 0 {
				return ErasureCommand{}, domain.ErasureBlocked(held)
			}
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into erasure_command (tenant_id, erasure_id, legal_entity_id, subject_ref, data_classes, request_id, requested_by, requested_at)
				values ($1, $2, $3, $4, $5, $6, $7, $8)`,
				p.TenantID, id, in.LegalEntityID, in.SubjectRef, in.DataClasses, nullable(in.RequestID), p.UserID, now); err != nil {
				return ErasureCommand{}, err
			}
			if err := enqueueAggregate(ctx, tx, p, cevents.ErasureRequested, erasureAggregate, id, cevents.ErasureRequestedData{
				ErasureID: id, RequestID: in.RequestID, LegalEntityID: in.LegalEntityID, SubjectRef: in.SubjectRef, DataClasses: in.DataClasses, RequestedAt: now}); err != nil {
				return ErasureCommand{}, err
			}
			return ErasureCommand{ErasureID: id, RequestID: in.RequestID, LegalEntityID: in.LegalEntityID, SubjectRef: in.SubjectRef,
				DataClasses: in.DataClasses, RequestedBy: p.UserID, RequestedAt: now}, nil
		})
		return err
	})
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) && de.Code == domain.CodePrivacyLegalHold {
			slog.WarnContext(ctx, "erasure refused by legal hold", "legalEntityId", in.LegalEntityID, "tenantId", p.TenantID, "actor", p.UserID)
		}
		return ErasureCommand{}, err
	}
	slog.InfoContext(ctx, "erasure command emitted", "erasureId", out.ErasureID, "legalEntityId", out.LegalEntityID, "tenantId", p.TenantID, "actor", p.UserID)
	return out, nil
}

func checkErasureRequest(ctx context.Context, tx pgx.Tx, in ErasureInput) error {
	req, err := requestByID(ctx, tx, in.RequestID, false)
	if err != nil {
		return err
	}
	if req.Type != domain.RequestErasure || req.SubjectRef != in.SubjectRef ||
		(req.State != domain.PrivacyApproved && req.State != domain.PrivacyDelivered) {
		return &domain.Error{Code: domain.CodePrivacyTransition, Detail: "the request must be an approved ERASURE of the same subject"}
	}
	return nil
}
