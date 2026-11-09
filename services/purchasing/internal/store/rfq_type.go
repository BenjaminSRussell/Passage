package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// Problem codes raised by the RFQ type store (FR-17.19).
const (
	CodeRFQTypeNotFound = "PRC-RFQ-TYPE-NOT-FOUND"
	// CodeRFQTypeExists: the legal entity already has an RFQ type with this code.
	CodeRFQTypeExists = "PRC-RFQ-TYPE-002"
	// CodeRFQTypeStale: If-Match is not the RFQ type's version.
	CodeRFQTypeStale = "PRC-RFQ-TYPE-003"
)

// RFQTypeFilter narrows ListRFQTypes; empty fields do not filter.
type RFQTypeFilter struct {
	LegalEntityID string
	Status        string
}

const rfqTypeColumns = `id, legal_entity_id, code, name, sealed, amendments_allowed, ranking_rule, status, row_version,
	coalesce(created_by::text, ''), created_at, coalesce(updated_by::text, ''), updated_at`

func scanRFQType(row pgx.Row, t *domain.RFQType) error {
	return row.Scan(&t.ID, &t.LegalEntityID, &t.Code, &t.Name, &t.Sealed, &t.AmendmentsAllowed, &t.RankingRule, &t.Status,
		&t.Version, &t.CreatedBy, &t.CreatedAt, &t.UpdatedBy, &t.UpdatedAt)
}

func loadRFQTypeCriteria(ctx context.Context, tx pgx.Tx, id string) ([]domain.ScoringCriterion, error) {
	p, _ := tenant.FromContext(ctx)
	rows, err := tx.Query(ctx, `select seq, name, weight from rfq_type_criterion where tenant_id = $1 and rfq_type_id = $2 order by seq`,
		p.TenantID, id)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.ScoringCriterion, error) {
		var c domain.ScoringCriterion
		err := row.Scan(&c.Seq, &c.Name, &c.Weight)
		return c, err
	})
	if out == nil {
		out = []domain.ScoringCriterion{}
	}
	return out, err
}

func loadRFQType(ctx context.Context, tx pgx.Tx, id string) (domain.RFQType, error) {
	p, _ := tenant.FromContext(ctx)
	var t domain.RFQType
	err := scanRFQType(tx.QueryRow(ctx, `select `+rfqTypeColumns+` from rfq_type where tenant_id = $1 and id = $2`, p.TenantID, id), &t)
	if err != nil {
		return t, notFound(err, CodeRFQTypeNotFound, "RFQ type not found")
	}
	t.Criteria, err = loadRFQTypeCriteria(ctx, tx, id)
	return t, err
}

func insertRFQTypeCriteria(ctx context.Context, tx pgx.Tx, id string, criteria []domain.ScoringCriterion) error {
	p, _ := tenant.FromContext(ctx)
	for i, c := range criteria {
		if _, err := tx.Exec(ctx, `insert into rfq_type_criterion (tenant_id, rfq_type_id, seq, name, weight) values ($1, $2, $3, $4, $5)`,
			p.TenantID, id, i+1, c.Name, c.Weight); err != nil {
			return err
		}
	}
	return nil
}

func rfqTypeData(t domain.RFQType, by string) purchasingevents.RFQTypeData {
	return purchasingevents.RFQTypeData{RFQTypeID: t.ID, LegalEntityID: t.LegalEntityID, Code: t.Code, Name: t.Name, Sealed: t.Sealed,
		AmendmentsAllowed: t.AmendmentsAllowed, RankingRule: t.RankingRule, CriteriaCount: len(t.Criteria), Status: t.Status,
		Version: t.Version, ChangedBy: by}
}

// CreateRFQType stores an RFQ type (version 1) with its default criteria and writes rfqtype.created.v1 in the
// same transaction. A replayed Idempotency-Key returns the type the first request created; a second type
// with the same code in the legal entity is 409 PRC-RFQ-TYPE-002.
func (s *Postgres) CreateRFQType(ctx context.Context, in domain.RFQType, idem Idem) (domain.RFQType, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	var out domain.RFQType
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, in.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadRFQType(ctx, tx, existing)
			return err
		}
		if _, err := tx.Exec(ctx, `insert into rfq_type (tenant_id, id, legal_entity_id, code, name, sealed, amendments_allowed, ranking_rule,
			status, created_by, updated_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
			p.TenantID, in.ID, in.LegalEntityID, in.Code, in.Name, in.Sealed, in.AmendmentsAllowed, in.RankingRule, in.Status,
			userOrNil(p.UserID)); err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "rfq_type_unique" {
				return httpx.NewProblem(http.StatusConflict, CodeRFQTypeExists,
					"the legal entity already has an RFQ type "+in.Code+"; change that one instead")
			}
			return err
		}
		if err := insertRFQTypeCriteria(ctx, tx, in.ID, in.Criteria); err != nil {
			return err
		}
		if out, err = loadRFQType(ctx, tx, in.ID); err != nil {
			return err
		}
		return enqueue(ctx, tx, "rfqtype", out.ID, purchasingevents.RFQTypeCreated, rfqTypeData(out, p.UserID))
	})
	return out, err
}

// GetRFQType returns one RFQ type with its default criteria.
func (s *Postgres) GetRFQType(ctx context.Context, id string) (domain.RFQType, error) {
	var out domain.RFQType
	err := s.withTx(ctx, func(tx pgx.Tx) (err error) {
		out, err = loadRFQType(ctx, tx, id)
		return err
	})
	return out, err
}

// ListRFQTypes returns RFQ types newest first, after the cursor id.
func (s *Postgres) ListRFQTypes(ctx context.Context, f RFQTypeFilter, after string, limit int) (Page[domain.RFQType], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	items := []domain.RFQType{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+rfqTypeColumns+` from rfq_type
			where tenant_id = $1 and ($2::uuid is null or legal_entity_id = $2::uuid) and ($3 = '' or status = $3)
			and ($4::uuid is null or id < $4::uuid) order by id desc limit $5`, p.TenantID, nullable(f.LegalEntityID), f.Status, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.RFQType, error) {
			var t domain.RFQType
			return t, scanRFQType(row, &t)
		})
		if err != nil {
			return err
		}
		for i := range items {
			if items[i].Criteria, err = loadRFQTypeCriteria(ctx, tx, items[i].ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Page[domain.RFQType]{}, err
	}
	if items == nil {
		items = []domain.RFQType{}
	}
	return pageOf(items, limit, func(t domain.RFQType) string { return t.ID }), nil
}

// UpdateRFQType replaces the name, flags, ranking rule, status and default criteria of an RFQ type under
// If-Match and writes rfqtype.changed.v1. The legal entity and the code do not change. A stale version is
// 412 PRC-RFQ-TYPE-003. RFQs already created keep the flags they copied.
func (s *Postgres) UpdateRFQType(ctx context.Context, id string, ifMatch int, in domain.RFQType) (domain.RFQType, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RFQType
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadRFQType(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != ifMatch {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeRFQTypeStale,
				fmt.Sprintf("RFQ type is at version %d, not %d; reload and retry", cur.Version, ifMatch))
		}
		tag, err := tx.Exec(ctx, `update rfq_type set name = $3, sealed = $4, amendments_allowed = $5, ranking_rule = $6, status = $7,
			updated_by = $8, updated_at = now(), row_version = row_version + 1
			where tenant_id = $1 and id = $2 and row_version = $9`,
			p.TenantID, id, in.Name, in.Sealed, in.AmendmentsAllowed, in.RankingRule, in.Status, userOrNil(p.UserID), ifMatch)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeRFQTypeStale,
				fmt.Sprintf("RFQ type changed after version %d; reload and retry", ifMatch))
		}
		if _, err := tx.Exec(ctx, `delete from rfq_type_criterion where tenant_id = $1 and rfq_type_id = $2`, p.TenantID, id); err != nil {
			return err
		}
		if err := insertRFQTypeCriteria(ctx, tx, id, in.Criteria); err != nil {
			return err
		}
		if out, err = loadRFQType(ctx, tx, id); err != nil {
			return err
		}
		return enqueue(ctx, tx, "rfqtype", id, purchasingevents.RFQTypeChanged, rfqTypeData(out, p.UserID))
	})
	return out, err
}

func insertRFQCriteria(ctx context.Context, tx pgx.Tx, rfqID string, criteria []domain.ScoringCriterion) error {
	p, _ := tenant.FromContext(ctx)
	for i, c := range criteria {
		if _, err := tx.Exec(ctx, `insert into purchase_rfq_criterion (tenant_id, rfq_id, id, seq, name, weight)
			values ($1, $2, $3, $4, $5, $6)`, p.TenantID, rfqID, ids.New(), i+1, c.Name, c.Weight); err != nil {
			return err
		}
	}
	return nil
}

// EditRFQ changes the reply deadline, ranking rule, sealed flag and scoring group of a DRAFT RFQ under
// If-Match (FR-17.19: both were fixed at create before). The RFQ version is bumped; a sent RFQ changes only
// by amendment. domain.CheckEdit has the rules.
func (s *Postgres) EditRFQ(ctx context.Context, id string, ifMatch int, in domain.RFQEdit, now time.Time) (domain.RFQ, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RFQ
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, _, err := lockForAction(ctx, tx, id, ifMatch, Idem{})
		if err != nil {
			return err
		}
		if err := domain.CheckEdit(cur, in, now); err != nil {
			return stateConflict(err)
		}
		tag, err := tx.Exec(ctx, `update purchase_rfq set reply_deadline = $3, ranking_rule = $4, sealed = $5, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and version = $6`, p.TenantID, id, in.ReplyDeadline, in.RankingRule, in.Sealed, cur.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, "RFQ changed since it was read; reload and retry")
		}
		if _, err := tx.Exec(ctx, `delete from purchase_rfq_criterion where tenant_id = $1 and rfq_id = $2`, p.TenantID, id); err != nil {
			return err
		}
		if err := insertRFQCriteria(ctx, tx, id, in.Criteria); err != nil {
			return err
		}
		out, err = loadRFQ(ctx, tx, id)
		return err
	})
	return out, err
}

func loadAmendments(ctx context.Context, tx pgx.Tx, rfqID string) ([]domain.RFQAmendment, error) {
	p, _ := tenant.FromContext(ctx)
	rows, err := tx.Query(ctx, `select amendment_no, summary, previous_deadline, new_deadline, criteria_changed,
		array(select v::text from unnest(late_cleared_vendor_ids) as v order by 1), coalesce(issued_by::text, ''), issued_at
		from purchase_rfq_amendment where tenant_id = $1 and rfq_id = $2 order by amendment_no`,
		p.TenantID, rfqID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.RFQAmendment, error) {
		var a domain.RFQAmendment
		err := row.Scan(&a.AmendmentNo, &a.Summary, &a.PreviousDeadline, &a.NewDeadline, &a.CriteriaChanged, &a.LateClearedVendorIDs,
			&a.IssuedBy, &a.IssuedAt)
		if a.LateClearedVendorIDs == nil {
			a.LateClearedVendorIDs = []string{}
		}
		return a, err
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []domain.RFQAmendment{}
	}
	acks, err := tx.Query(ctx, `select amendment_no, vendor_id, acknowledged_at from purchase_rfq_amendment_ack
		where tenant_id = $1 and rfq_id = $2 order by amendment_no, vendor_id`, p.TenantID, rfqID)
	if err != nil {
		return nil, err
	}
	defer acks.Close()
	byNo := make(map[int]*domain.RFQAmendment, len(out))
	for i := range out {
		out[i].Acknowledgements = []domain.AmendmentAckInfo{}
		byNo[out[i].AmendmentNo] = &out[i]
	}
	for acks.Next() {
		var no int
		var ack domain.AmendmentAckInfo
		if err := acks.Scan(&no, &ack.VendorID, &ack.AcknowledgedAt); err != nil {
			return nil, err
		}
		if a := byNo[no]; a != nil {
			a.Acknowledgements = append(a.Acknowledgements, ack)
		}
	}
	return out, acks.Err()
}

// AmendRFQ issues an addendum to a SENT or REPLIES_OPEN RFQ (FR-17.19) under If-Match: it numbers the
// amendment, records its summary, moves the reply deadline and replaces the scoring criteria when it says
// so, asks every invited vendor to acknowledge it, and writes rfq.amended.v1, all in one transaction. A
// reply stored late only because the deadline has now moved is no longer late (an extension applies to every
// invited vendor alike); the vendors it clears are recorded on the amendment row and in the event, so the
// change of a reply's standing is always audited. domain.CheckAmendment has the rules; a replay with the
// same key returns the RFQ as it is. The RFQ row is locked, so amendment numbers have no gaps or duplicates.
func (s *Postgres) AmendRFQ(ctx context.Context, id string, ifMatch int, in domain.AmendmentInput, now time.Time, idem Idem) (domain.RFQ, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RFQ
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, replayed, err := lockForAction(ctx, tx, id, ifMatch, idem)
		if err != nil || replayed {
			out = cur
			return err
		}
		if err := domain.CheckAmendment(cur, in, now); err != nil {
			return stateConflict(err)
		}
		no := cur.AmendmentNo + 1
		deadline := cur.ReplyDeadline
		var prev, next *time.Time
		if in.ReplyDeadline != nil {
			d := in.ReplyDeadline.UTC()
			prev, next, deadline = &cur.ReplyDeadline, &d, d
		}
		cleared := []string{}
		if next != nil {
			rows, err := tx.Query(ctx, `update purchase_rfq_reply set late = false
				where tenant_id = $1 and rfq_id = $2 and late and received_at <= $3 returning vendor_id::text`, p.TenantID, id, deadline)
			if err != nil {
				return err
			}
			vendors, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, v := range vendors {
				if !seen[v] {
					seen[v] = true
					cleared = append(cleared, v)
				}
			}
			sort.Strings(cleared)
		}
		if _, err := tx.Exec(ctx, `insert into purchase_rfq_amendment (tenant_id, rfq_id, amendment_no, summary, previous_deadline, new_deadline,
			criteria_changed, late_cleared_vendor_ids, issued_by, issued_at) values ($1, $2, $3, $4, $5, $6, $7, $8::uuid[], $9, $10)`,
			p.TenantID, id, no, in.Summary, prev, next, in.ReplaceCriteria, cleared, userOrNil(p.UserID), now); err != nil {
			return err
		}
		vendorIDs := make([]string, 0, len(cur.Invitations))
		for _, inv := range cur.Invitations {
			vendorIDs = append(vendorIDs, inv.VendorID)
			if _, err := tx.Exec(ctx, `insert into purchase_rfq_amendment_ack (tenant_id, rfq_id, amendment_no, vendor_id) values ($1, $2, $3, $4)`,
				p.TenantID, id, no, inv.VendorID); err != nil {
				return err
			}
		}
		if in.ReplaceCriteria {
			if _, err := tx.Exec(ctx, `delete from purchase_rfq_criterion where tenant_id = $1 and rfq_id = $2`, p.TenantID, id); err != nil {
				return err
			}
			if err := insertRFQCriteria(ctx, tx, id, in.Criteria); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `update purchase_rfq set reply_deadline = $3, amendment_no = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and version = $5`, p.TenantID, id, deadline, no, cur.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, "RFQ changed since it was read; reload and retry")
		}
		if out, err = loadRFQ(ctx, tx, id); err != nil {
			return err
		}
		data := purchasingevents.RFQAmendedData{RFQID: id, Number: cur.Number, AmendmentNo: no, LegalEntityID: cur.LegalEntityID,
			Summary: in.Summary, VendorIDs: vendorIDs, ReplyDeadline: deadline.UTC().Format(time.RFC3339), CriteriaChanged: in.ReplaceCriteria,
			LateClearedVendorIDs: cleared, IssuedBy: p.UserID}
		if prev != nil {
			data.PreviousDeadline = prev.UTC().Format(time.RFC3339)
		}
		return enqueue(ctx, tx, "rfq", id, purchasingevents.RFQAmended, data)
	})
	return out, err
}

// AcknowledgeAmendment records that an invited vendor acknowledged an amendment (FR-17.19). It is
// idempotent: a second call keeps the first time. An amendment or vendor the RFQ does not have is 404.
func (s *Postgres) AcknowledgeAmendment(ctx context.Context, id string, amendmentNo int, vendorID string, now time.Time) (domain.RFQ, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RFQ
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockRFQ(ctx, tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update purchase_rfq_amendment_ack set acknowledged_at = coalesce(acknowledged_at, $5),
			acknowledged_by = coalesce(acknowledged_by, $6)
			where tenant_id = $1 and rfq_id = $2 and amendment_no = $3 and vendor_id = $4`,
			p.TenantID, id, amendmentNo, vendorID, now, userOrNil(p.UserID))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusNotFound, CodeAmendmentNotFound,
				fmt.Sprintf("amendment %d of this RFQ has no acknowledgement due from vendor %s", amendmentNo, vendorID))
		}
		out, err = loadRFQ(ctx, tx, id)
		return err
	})
	return out, err
}

// CodeAmendmentNotFound is the 404 code for an unknown amendment or a vendor that owes no acknowledgement.
const CodeAmendmentNotFound = "PRC-RFQ-AMENDMENT-NOT-FOUND"
