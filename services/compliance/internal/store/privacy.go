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
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	cevents "github.com/BenjaminSRussell/passage/services/compliance/internal/events"
)

// Subject request error codes (listed in the service README).
const (
	CodeRequestNotFound = "PRV-1209"
	CodeRequestVersion  = "PRV-1210"

	requestAggregate = "subject_request"
)

// SubjectRequest is a stored GDPR or CCPA subject request (9.48.4 subject_request).
type SubjectRequest struct {
	RequestID        string                    `json:"requestId"`
	Type             domain.PrivacyRequestType `json:"type"`
	Regulation       domain.Regulation         `json:"regulation"`
	SubjectRef       string                    `json:"subjectRef"`
	ReceivedAt       time.Time                 `json:"receivedAt"`
	DeadlineAt       time.Time                 `json:"deadlineAt"`
	State            domain.PrivacyState       `json:"state"`
	HoldIDs          []string                  `json:"holdIds"`
	VerifiedAt       *time.Time                `json:"verifiedAt,omitempty"`
	PrivacyOfficerID string                    `json:"privacyOfficerId,omitempty"`
	ApprovedAt       *time.Time                `json:"approvedAt,omitempty"`
	DeliveredAt      *time.Time                `json:"deliveredAt,omitempty"`
	ClosedAt         *time.Time                `json:"closedAt,omitempty"`
	Late             bool                      `json:"late"`
	Justification    string                    `json:"justification,omitempty"`
	CreatedBy        string                    `json:"createdBy"`
	CreatedAt        time.Time                 `json:"createdAt"`
	UpdatedAt        time.Time                 `json:"updatedAt"`
	Version          int                       `json:"version"`
}

// IntakeInput is a new subject request. ReceivedAt defaults to now; a
// DeadlineDays override may only shorten the regulation's window.
type IntakeInput struct {
	Type         domain.PrivacyRequestType `json:"type"`
	Regulation   domain.Regulation         `json:"regulation"`
	SubjectRef   string                    `json:"subjectRef"`
	ReceivedAt   time.Time                 `json:"receivedAt"`
	DeadlineDays int                       `json:"deadlineDays,omitempty"`
}

// RequestFilter narrows a subject request listing. Empty fields do not filter.
type RequestFilter struct {
	State  string
	Type   string
	Limit  int
	Cursor string
}

// RequestPage is one page of subject requests, nearest deadline first.
type RequestPage struct {
	Items      []SubjectRequest `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// RequestActionInput is one action on a stored request. HoldIDs are the legal
// holds found at DISCOVER, or the holds still standing at RELEASE_HOLD.
type RequestActionInput struct {
	Action            domain.PrivacyAction `json:"action"`
	HoldIDs           []string             `json:"holdIds,omitempty"`
	MatchedAttributes []string             `json:"matchedAttributes,omitempty"`
	EmailLinkVerified bool                 `json:"emailLinkVerified"`
	Justification     string               `json:"justification,omitempty"`
}

const requestColumns = `request_id, type, regulation, subject_ref, received_at, deadline_at, state, hold_ids, verified_at,
  privacy_officer_id, approved_at, delivered_at, closed_at, late, justification, created_by, created_at, updated_at, version`

func scanRequest(row pgx.Row) (SubjectRequest, error) {
	var r SubjectRequest
	var typ, regulation, state string
	var officer, justification *string
	if err := row.Scan(&r.RequestID, &typ, &regulation, &r.SubjectRef, &r.ReceivedAt, &r.DeadlineAt, &state, &r.HoldIDs, &r.VerifiedAt,
		&officer, &r.ApprovedAt, &r.DeliveredAt, &r.ClosedAt, &r.Late, &justification, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt, &r.Version); err != nil {
		return SubjectRequest{}, err
	}
	r.Type, r.Regulation, r.State = domain.PrivacyRequestType(typ), domain.Regulation(regulation), domain.PrivacyState(state)
	r.PrivacyOfficerID, r.Justification = deref(officer), deref(justification)
	r.HoldIDs = nonNil(r.HoldIDs)
	return r, nil
}

// Intake records a subject request in RECEIVED with its deadline (FR-48.10:
// GDPR 30 days, CCPA 45 days). A repeated Idempotency-Key replays the first
// answer; the same key with a different body is refused.
func (s *Postgres) Intake(ctx context.Context, in IntakeInput, key string, now time.Time) (SubjectRequest, error) {
	p, err := principal(ctx)
	if err != nil {
		return SubjectRequest{}, err
	}
	received := in.ReceivedAt
	if received.IsZero() {
		received = now
	}
	plan, err := domain.PlanPrivacyRequest(domain.PrivacyPlanInput{Type: in.Type, Regulation: in.Regulation, ReceivedAt: received, DeadlineDays: in.DeadlineDays, Now: now})
	if err != nil {
		return SubjectRequest{}, err
	}
	var out SubjectRequest
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			Input   IntakeInput `json:"input"`
			Creator string      `json:"creator"`
		}{in, p.UserID}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, key, "privacy.request.intake", request, func() (SubjectRequest, error) {
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into subject_request (tenant_id, request_id, type, regulation, subject_ref, received_at, deadline_at,
					state, created_by, created_at, updated_at)
				values ($1, $2, $3, $4, $5, $6, $7, 'RECEIVED', $8, $9, $9)`,
				p.TenantID, id, string(in.Type), string(in.Regulation), strings.TrimSpace(in.SubjectRef), received.UTC(), plan.DeadlineAt, p.UserID, now); err != nil {
				return SubjectRequest{}, err
			}
			return requestByID(ctx, tx, id, false)
		})
		return err
	})
	return out, err
}

// ListRequests returns subject requests nearest deadline first, keyset-paginated.
func (s *Postgres) ListRequests(ctx context.Context, f RequestFilter) (RequestPage, error) {
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
	if f.Type != "" {
		add("type = $%d", f.Type)
	}
	if f.Cursor != "" {
		at, id, err := DecodeCursor(f.Cursor)
		if err != nil {
			return RequestPage{}, problem(http.StatusBadRequest, "COM-0002", "cursor is malformed")
		}
		args = append(args, at, id)
		where = append(where, fmt.Sprintf("(deadline_at, request_id) > ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit+1)
	q := `select ` + requestColumns + ` from subject_request where ` + strings.Join(where, " and ") +
		fmt.Sprintf(` order by deadline_at, request_id limit $%d`, len(args))
	page := RequestPage{Items: []SubjectRequest{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRequest(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, r)
		}
		return rows.Err()
	})
	if err != nil {
		return RequestPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.NextCursor = cursorOf(last.DeadlineAt, last.RequestID)
	}
	return page, nil
}

// GetRequest returns one subject request.
func (s *Postgres) GetRequest(ctx context.Context, id string) (SubjectRequest, error) {
	var out SubjectRequest
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = requestByID(ctx, tx, id, false)
		return err
	})
	return out, err
}

func requestByID(ctx context.Context, tx pgx.Tx, id string, lock bool) (SubjectRequest, error) {
	q := `select ` + requestColumns + ` from subject_request where request_id = $1`
	if lock {
		q += ` for update`
	}
	r, err := scanRequest(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SubjectRequest{}, problem(http.StatusNotFound, CodeRequestNotFound, "no subject request with that id")
	}
	return r, err
}

// ActOnRequest applies one action to a stored request under its row lock
// (FR-48.10, FR-48.11). The type, state and deadline are the stored ones, never
// the caller's. The holds found at DISCOVER are stored on the request: an
// ERASURE under any of them goes to HELD and changes nothing until every hold is
// released (AC-48.7). APPROVE records the calling privacy officer; CLOSE records
// whether the deadline was missed and emits privacy.request.completed in the
// same transaction. A stale expected version is 412.
func (s *Postgres) ActOnRequest(ctx context.Context, id string, expected int, in RequestActionInput, now time.Time) (SubjectRequest, error) {
	p, err := principal(ctx)
	if err != nil {
		return SubjectRequest{}, err
	}
	var out SubjectRequest
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := requestByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return problem(http.StatusPreconditionFailed, CodeRequestVersion, "the request was changed since you loaded it; reload and try again")
		}
		holds := cur.HoldIDs
		if in.Action == domain.ActionDiscover || in.Action == domain.ActionReleaseHold {
			holds = domain.PrivacyHolds(in.HoldIDs)
		}
		justification := strings.TrimSpace(in.Justification)
		next, err := domain.ApplyPrivacyTransition(domain.PrivacyTransitionInput{
			Type: cur.Type, From: cur.State, Action: in.Action, HoldIDs: holds, MatchedAttributes: in.MatchedAttributes,
			EmailLinkVerified: in.EmailLinkVerified, Justification: justification, DeadlineAt: cur.DeadlineAt, Now: now,
		})
		if err != nil {
			return err
		}
		set := []string{"state = $2", "hold_ids = $3", "updated_at = $4", "version = version + 1"}
		args := []any{id, string(next.State), holds, now}
		col := func(name string, v any) {
			args = append(args, v)
			set = append(set, fmt.Sprintf("%s = $%d", name, len(args)))
		}
		switch in.Action {
		case domain.ActionVerify:
			col("verified_at", now)
		case domain.ActionApprove:
			col("privacy_officer_id", p.UserID)
			col("approved_at", now)
		case domain.ActionDeliver:
			col("delivered_at", now)
		case domain.PrivacyActionClose, domain.ActionReject:
			col("closed_at", now)
			col("late", next.Late)
			if justification != "" {
				col("justification", justification)
			}
		}
		if _, err := tx.Exec(ctx, `update subject_request set `+strings.Join(set, ", ")+` where request_id = $1`, args...); err != nil {
			return err
		}
		out, err = requestByID(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if in.Action != domain.PrivacyActionClose {
			return nil
		}
		delivered := now
		if out.DeliveredAt != nil {
			delivered = *out.DeliveredAt
		}
		return enqueueAggregate(ctx, tx, p, cevents.RequestCompleted, requestAggregate, id, cevents.RequestCompletedData{
			RequestID: id, Type: string(out.Type), Regulation: string(out.Regulation), AffectedDomains: []string{},
			DeliveredAt: delivered, ClosedAt: now, Late: out.Late,
		})
	})
	return out, err
}
