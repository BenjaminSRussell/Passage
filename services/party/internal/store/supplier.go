package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

// Supplier collaboration (#511, items 5 and 6): vendor requests and onboarding, document exchange, and controlled distribution.
// Vendor-side methods take the vendor id the caller was verified for and filter every query by it; a row of another vendor reads
// as not found.

const (
	CodeSupplierNotFound = "PTY-SUP-NOT-FOUND"
	CodeSupplierState    = "PTY-SUP-STATE"
	CodeSupplierInvalid  = "PTY-SUP-INVALID"
	CodeSupplierSoD      = "PTY-SUP-SOD"
	// CodeSupplierPartyWrite: approving a profile change rewrites the tenant-wide party, which needs party.write as well.
	CodeSupplierPartyWrite = "PTY-SUP-PARTY-WRITE"
)

// DutyPartyWrite is the duty that edits a party; approving a vendor's profile change needs it besides the decide duty.
const DutyPartyWrite = "party.write"

type Questionnaire struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Version   int               `json:"version"`
	Questions []domain.Question `json:"questions"`
	CreatedAt time.Time         `json:"createdAt"`
}

type VendorRequest struct {
	ID              string            `json:"id"`
	VendorID        string            `json:"vendorId"`
	Kind            string            `json:"kind"`
	Status          string            `json:"status"`
	Subject         string            `json:"subject"`
	Details         string            `json:"details"`
	QuestionnaireID string            `json:"questionnaireId,omitempty"`
	Questions       []domain.Question `json:"questions,omitempty"`
	Answers         map[string]string `json:"answers"`
	// Changes are the party fields a PROFILE_CHANGE asks for; approving the request applies them to the vendor's party.
	Changes      map[string]string `json:"changes,omitempty"`
	CreatedAt    time.Time         `json:"createdAt"`
	SubmittedAt  *time.Time        `json:"submittedAt,omitempty"`
	DecidedAt    *time.Time        `json:"decidedAt,omitempty"`
	DecisionNote string            `json:"decisionNote,omitempty"`
	// AppliedAt is when an approved PROFILE_CHANGE was written to the party.
	AppliedAt *time.Time `json:"appliedAt,omitempty"`
}

// DocumentMeta describes a stored document without its content.
type DocumentMeta struct {
	ID          string    `json:"id"`
	VendorID    string    `json:"vendorId,omitempty"`
	Direction   string    `json:"direction,omitempty"`
	DocNo       string    `json:"docNo,omitempty"`
	Revision    int       `json:"revision,omitempty"`
	Title       string    `json:"title"`
	FileName    string    `json:"fileName"`
	ContentType string    `json:"contentType"`
	SizeBytes   int       `json:"sizeBytes"`
	SHA256      string    `json:"sha256"`
	RequiresAck bool      `json:"requiresAck,omitempty"`
	At          time.Time `json:"at"`
}

// DocumentContent is a document with its bytes.
type DocumentContent struct {
	DocumentMeta
	Content []byte `json:"-"`
}

type Distribution struct {
	DocumentMeta
	VendorID       string     `json:"vendorId"`
	SentAt         time.Time  `json:"sentAt"`
	AcknowledgedBy string     `json:"acknowledgedBy,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledgedAt,omitempty"`
}

type AuditEntry struct {
	ID        string    `json:"id"`
	VendorID  string    `json:"vendorId"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	SubjectID string    `json:"subjectId,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	At        time.Time `json:"at"`
}

// SupplierStore is the persistence of the supplier collaboration.
type SupplierStore interface {
	CreateQuestionnaire(ctx context.Context, name string, qs []domain.Question) (Questionnaire, error)
	ListQuestionnaires(ctx context.Context) ([]Questionnaire, error)
	InviteOnboarding(ctx context.Context, vendorID, questionnaireID string) (VendorRequest, error)
	ListRequests(ctx context.Context, vendorID, status string, limit int) ([]VendorRequest, error)
	GetRequest(ctx context.Context, vendorID, id string) (VendorRequest, error)
	CreateRequest(ctx context.Context, vendorID, kind, subject, details string, changes map[string]string) (VendorRequest, error)
	SubmitRequest(ctx context.Context, vendorID, id string, answers map[string]string) (VendorRequest, error)
	WithdrawRequest(ctx context.Context, vendorID, id string) (VendorRequest, error)
	DecideRequest(ctx context.Context, id string, approve bool, note string) (VendorRequest, error)

	AddDocument(ctx context.Context, vendorID, direction string, d domain.Document) (DocumentMeta, error)
	ListDocuments(ctx context.Context, vendorID string, limit int) ([]DocumentMeta, error)
	GetDocument(ctx context.Context, vendorID, id string) (DocumentContent, error)

	PublishControlled(ctx context.Context, docNo string, requiresAck bool, d domain.Document) (DocumentMeta, error)
	ListControlled(ctx context.Context, limit int) ([]DocumentMeta, error)
	Distribute(ctx context.Context, documentID string, vendorIDs []string) ([]Distribution, error)
	ListDistributions(ctx context.Context, documentID, vendorID string) ([]Distribution, error)
	GetDistributedContent(ctx context.Context, vendorID, documentID string) (DocumentContent, error)
	Acknowledge(ctx context.Context, vendorID, documentID string) (Distribution, error)

	ListAudit(ctx context.Context, vendorID string, limit int) ([]AuditEntry, error)
}

func supplierNotFound() error { return notFound(CodeSupplierNotFound, "not found") }

func supplierState(detail string) error {
	return httpx.NewProblem(http.StatusConflict, CodeSupplierState, detail)
}

func actorOf(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.UserID
}

// singleEntity is the one legal entity a creating caller works in.
func singleEntity(ctx context.Context) (string, error) {
	caller, _ := tenant.FromContext(ctx)
	scopes := tenant.LegalEntityScopeList(caller)
	if len(scopes) != 1 {
		return "", httpx.NewProblem(http.StatusUnprocessableEntity, CodeSupplierInvalid, "the caller must carry exactly one legal entity")
	}
	return scopes[0], nil
}

func tenantOf(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.TenantID
}

// vendorEntity returns the legal entity of a vendor role the caller can see.
func vendorEntity(ctx context.Context, tx pgx.Tx, vendorID string) (string, error) {
	var le string
	err := tx.QueryRow(ctx, `select legal_entity_id::text from vendor_role where tenant_id = $1 and id = $2`, tenantOf(ctx), vendorID).Scan(&le)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", supplierNotFound()
	}
	return le, err
}

func writeAudit(ctx context.Context, tx pgx.Tx, le, vendorID, action, subjectID, detail string) error {
	var subject *string
	if subjectID != "" {
		subject = &subjectID
	}
	_, err := tx.Exec(ctx, `insert into vendor_portal_audit (tenant_id, id, legal_entity_id, vendor_id, actor, action, subject_id, detail) values ($1, $2, $3, $4, $5, $6, $7, $8)`,
		tenantOf(ctx), ids.New(), le, vendorID, actorOf(ctx), action, subject, detail)
	return err
}

func supplierID(raw string) (string, error) {
	id, err := canonicalID(raw)
	if err != nil {
		return "", supplierNotFound()
	}
	return id, nil
}

func run(ctx context.Context, s *Postgres, fn func(tx pgx.Tx) error) error {
	err := dbx.WithTenantTx(ctx, s.pool, fn)
	if errors.Is(err, pgx.ErrNoRows) {
		return supplierNotFound()
	}
	return err
}

func (s *Postgres) CreateQuestionnaire(ctx context.Context, name string, qs []domain.Question) (Questionnaire, error) {
	le, err := singleEntity(ctx)
	if err != nil {
		return Questionnaire{}, err
	}
	raw, _ := json.Marshal(qs)
	q := Questionnaire{Name: name, Questions: qs}
	err = run(ctx, s, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `insert into vendor_questionnaire (tenant_id, id, legal_entity_id, name, version, questions, created_by)
			values ($1, $2, $3, $4, coalesce((select max(version) from vendor_questionnaire where tenant_id = $1 and legal_entity_id = $3 and name = $4), 0) + 1, $5, $6)
			returning id::text, version, created_at`, tenantOf(ctx), ids.New(), le, name, raw, actorOf(ctx)).Scan(&q.ID, &q.Version, &q.CreatedAt)
	})
	return q, err
}

func (s *Postgres) ListQuestionnaires(ctx context.Context) ([]Questionnaire, error) {
	out := []Questionnaire{}
	err := run(ctx, s, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, name, version, questions, created_at from vendor_questionnaire where tenant_id = $1 order by created_at desc limit 200`, tenantOf(ctx))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var q Questionnaire
			var raw []byte
			if err := rows.Scan(&q.ID, &q.Name, &q.Version, &raw, &q.CreatedAt); err != nil {
				return err
			}
			_ = json.Unmarshal(raw, &q.Questions)
			out = append(out, q)
		}
		return rows.Err()
	})
	return out, err
}

const requestColumns = `r.id::text, r.vendor_id::text, r.kind, r.status, r.subject, r.details, coalesce(r.questionnaire_id::text, ''), coalesce(q.questions, '[]'::jsonb), r.answers, r.created_at, r.submitted_at, r.decided_at, r.decision_note, r.changes, r.applied_at`
const requestFrom = ` from vendor_request r left join vendor_questionnaire q on q.tenant_id = r.tenant_id and q.id = r.questionnaire_id`

func scanRequest(row pgx.Row) (VendorRequest, error) {
	var r VendorRequest
	var qs, answers, changes []byte
	if err := row.Scan(&r.ID, &r.VendorID, &r.Kind, &r.Status, &r.Subject, &r.Details, &r.QuestionnaireID, &qs, &answers, &r.CreatedAt, &r.SubmittedAt, &r.DecidedAt, &r.DecisionNote, &changes, &r.AppliedAt); err != nil {
		return r, err
	}
	if len(changes) > 0 && string(changes) != "{}" {
		_ = json.Unmarshal(changes, &r.Changes)
	}
	_ = json.Unmarshal(qs, &r.Questions)
	r.Answers = map[string]string{}
	_ = json.Unmarshal(answers, &r.Answers)
	return r, nil
}

func loadRequest(ctx context.Context, tx pgx.Tx, vendorID, id string, lock bool) (VendorRequest, error) {
	suffix := ""
	if lock {
		suffix = " for update of r"
	}
	return scanRequest(tx.QueryRow(ctx, `select `+requestColumns+requestFrom+` where r.tenant_id = $1 and r.id = $2 and ($3 = '' or r.vendor_id::text = $3)`+suffix, tenantOf(ctx), id, vendorID))
}

// InviteOnboarding opens the onboarding of a prospective vendor: the vendor role is blocked until its request is approved.
func (s *Postgres) InviteOnboarding(ctx context.Context, vendorID, questionnaireID string) (VendorRequest, error) {
	vid, err := supplierID(vendorID)
	if err != nil {
		return VendorRequest{}, err
	}
	qid, err := supplierID(questionnaireID)
	if err != nil {
		return VendorRequest{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeSupplierInvalid, "questionnaireId is not a questionnaire")
	}
	var out VendorRequest
	err = run(ctx, s, func(tx pgx.Tx) error {
		le, err := vendorEntity(ctx, tx, vid)
		if err != nil {
			return err
		}
		var one int
		if err := tx.QueryRow(ctx, `select 1 from vendor_questionnaire where tenant_id = $1 and id = $2 and legal_entity_id = $3`, tenantOf(ctx), qid, le).Scan(&one); err != nil {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeSupplierInvalid, "questionnaireId is not a questionnaire of the vendor's legal entity")
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into vendor_request (tenant_id, id, legal_entity_id, vendor_id, kind, status, questionnaire_id, created_by) values ($1, $2, $3, $4, 'ONBOARDING', 'INVITED', $5, $6)`,
			tenantOf(ctx), id, le, vid, qid, actorOf(ctx)); err != nil {
			if isUniqueViolation(err) {
				return supplierState("the vendor already has an onboarding in progress")
			}
			return err
		}
		if _, err := tx.Exec(ctx, `update vendor_role set blocked = 'all', version = version + 1 where tenant_id = $1 and id = $2`, tenantOf(ctx), vid); err != nil {
			return err
		}
		if err := writeAudit(ctx, tx, le, vid, "onboarding.invited", id, ""); err != nil {
			return err
		}
		out, err = loadRequest(ctx, tx, "", id, false)
		return err
	})
	return out, err
}

func (s *Postgres) ListRequests(ctx context.Context, vendorID, status string, limit int) ([]VendorRequest, error) {
	out := []VendorRequest{}
	err := run(ctx, s, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+requestColumns+requestFrom+` where r.tenant_id = $1 and ($2 = '' or r.vendor_id::text = $2) and ($3 = '' or r.status = $3) order by r.created_at desc, r.id limit $4`,
			tenantOf(ctx), vendorID, status, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRequest(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) GetRequest(ctx context.Context, vendorID, id string) (VendorRequest, error) {
	rid, err := supplierID(id)
	if err != nil {
		return VendorRequest{}, err
	}
	var out VendorRequest
	err = run(ctx, s, func(tx pgx.Tx) error {
		out, err = loadRequest(ctx, tx, vendorID, rid, false)
		return err
	})
	return out, err
}

func (s *Postgres) CreateRequest(ctx context.Context, vendorID, kind, subject, details string, changes map[string]string) (VendorRequest, error) {
	var out VendorRequest
	if changes == nil {
		changes = map[string]string{}
	}
	rawChanges, _ := json.Marshal(changes)
	err := run(ctx, s, func(tx pgx.Tx) error {
		le, err := vendorEntity(ctx, tx, vendorID)
		if err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into vendor_request (tenant_id, id, legal_entity_id, vendor_id, kind, status, subject, details, created_by, submitted_by, submitted_at, changes) values ($1, $2, $3, $4, $5, 'SUBMITTED', $6, $7, $8, $8, now(), $9)`,
			tenantOf(ctx), id, le, vendorID, kind, subject, details, actorOf(ctx), rawChanges); err != nil {
			return err
		}
		if err := writeAudit(ctx, tx, le, vendorID, "request.submitted", id, kind); err != nil {
			return err
		}
		out, err = loadRequest(ctx, tx, vendorID, id, false)
		return err
	})
	return out, err
}

func (s *Postgres) SubmitRequest(ctx context.Context, vendorID, id string, answers map[string]string) (VendorRequest, error) {
	rid, err := supplierID(id)
	if err != nil {
		return VendorRequest{}, err
	}
	var out VendorRequest
	err = run(ctx, s, func(tx pgx.Tx) error {
		r, err := loadRequest(ctx, tx, vendorID, rid, true)
		if err != nil {
			return err
		}
		if !domain.CanSubmit(r.Kind, r.Status) {
			return supplierState("the request is " + r.Status + " and cannot be submitted")
		}
		if errs := domain.ValidateAnswers(r.Questions, answers); len(errs) > 0 {
			p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeSupplierInvalid, "answers failed validation")
			p.Errors = errs
			return p
		}
		raw, _ := json.Marshal(answers)
		if _, err := tx.Exec(ctx, `update vendor_request set status = 'SUBMITTED', answers = $3, submitted_by = $4, submitted_at = now() where tenant_id = $1 and id = $2`, tenantOf(ctx), rid, raw, actorOf(ctx)); err != nil {
			return err
		}
		le, err := vendorEntity(ctx, tx, vendorID)
		if err != nil {
			return err
		}
		if err := writeAudit(ctx, tx, le, vendorID, "onboarding.submitted", rid, ""); err != nil {
			return err
		}
		out, err = loadRequest(ctx, tx, vendorID, rid, false)
		return err
	})
	return out, err
}

func (s *Postgres) WithdrawRequest(ctx context.Context, vendorID, id string) (VendorRequest, error) {
	rid, err := supplierID(id)
	if err != nil {
		return VendorRequest{}, err
	}
	var out VendorRequest
	err = run(ctx, s, func(tx pgx.Tx) error {
		r, err := loadRequest(ctx, tx, vendorID, rid, true)
		if err != nil {
			return err
		}
		if !domain.CanWithdraw(r.Status) {
			return supplierState("the request is " + r.Status + " and cannot be withdrawn")
		}
		if _, err := tx.Exec(ctx, `update vendor_request set status = 'WITHDRAWN' where tenant_id = $1 and id = $2`, tenantOf(ctx), rid); err != nil {
			return err
		}
		le, err := vendorEntity(ctx, tx, vendorID)
		if err != nil {
			return err
		}
		if err := writeAudit(ctx, tx, le, vendorID, "request.withdrawn", rid, ""); err != nil {
			return err
		}
		out, err = loadRequest(ctx, tx, vendorID, rid, false)
		return err
	})
	return out, err
}

// DecideRequest approves or rejects a submitted request. Approving an onboarding unblocks the vendor role: that is where the
// approval lands in party. The deciding user must not be the one who opened the onboarding.
func (s *Postgres) DecideRequest(ctx context.Context, id string, approve bool, note string) (VendorRequest, error) {
	rid, err := supplierID(id)
	if err != nil {
		return VendorRequest{}, err
	}
	var out VendorRequest
	err = run(ctx, s, func(tx pgx.Tx) error {
		r, err := loadRequest(ctx, tx, "", rid, true)
		if err != nil {
			return err
		}
		if !domain.CanDecide(r.Status) {
			return supplierState("the request is " + r.Status + " and cannot be decided")
		}
		// The party a profile change rewrites is tenant-wide: the same party can be a customer in another legal entity. The
		// decide duty alone, which can be scoped to one legal entity, does not reach it; the approver needs party.write too.
		if approve && r.Kind == domain.RequestProfileChange && len(r.Changes) > 0 {
			if caller, _ := tenant.FromContext(ctx); !caller.HasDuty(DutyPartyWrite) {
				return httpx.NewProblem(http.StatusForbidden, CodeSupplierPartyWrite, "approving a profile change updates the vendor's party, which needs party.write as well")
			}
		}
		var createdBy string
		if err := tx.QueryRow(ctx, `select created_by from vendor_request where tenant_id = $1 and id = $2`, tenantOf(ctx), rid).Scan(&createdBy); err != nil {
			return err
		}
		if r.Kind == domain.RequestOnboarding && createdBy == actorOf(ctx) {
			return httpx.NewProblem(http.StatusForbidden, CodeSupplierSoD, "the user who invited the vendor cannot approve its onboarding")
		}
		status := domain.RequestRejected
		if approve {
			status = domain.RequestApproved
		}
		if _, err := tx.Exec(ctx, `update vendor_request set status = $3, decided_by = $4, decided_at = now(), decision_note = $5 where tenant_id = $1 and id = $2`, tenantOf(ctx), rid, status, actorOf(ctx), note); err != nil {
			return err
		}
		if approve && r.Kind == domain.RequestOnboarding {
			if _, err := tx.Exec(ctx, `update vendor_role set blocked = 'none', version = version + 1 where tenant_id = $1 and id = $2`, tenantOf(ctx), r.VendorID); err != nil {
				return err
			}
		}
		le, err := vendorEntity(ctx, tx, r.VendorID)
		if err != nil {
			return err
		}
		if err := writeAudit(ctx, tx, le, r.VendorID, "request."+status, rid, ""); err != nil {
			return err
		}
		if approve && r.Kind == domain.RequestProfileChange && len(r.Changes) > 0 {
			if err := applyProfileChange(ctx, tx, le, r); err != nil {
				return err
			}
		}
		out, err = loadRequest(ctx, tx, "", rid, false)
		return err
	})
	return out, err
}

const docMetaColumns = `id::text, title, file_name, content_type, size_bytes, sha256`

func (s *Postgres) AddDocument(ctx context.Context, vendorID, direction string, d domain.Document) (DocumentMeta, error) {
	var m DocumentMeta
	vendorID, err := supplierID(vendorID)
	if err != nil {
		return m, err
	}
	err = run(ctx, s, func(tx pgx.Tx) error {
		le, err := vendorEntity(ctx, tx, vendorID)
		if err != nil {
			return err
		}
		id := ids.New()
		m = DocumentMeta{ID: id, VendorID: vendorID, Direction: direction, Title: d.Title, FileName: d.FileName, ContentType: d.ContentType, SizeBytes: len(d.Content), SHA256: d.SHA256}
		if err := tx.QueryRow(ctx, `insert into vendor_document (tenant_id, id, legal_entity_id, vendor_id, direction, title, file_name, content_type, size_bytes, sha256, content, uploaded_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) returning uploaded_at`,
			tenantOf(ctx), id, le, vendorID, direction, d.Title, d.FileName, d.ContentType, len(d.Content), d.SHA256, d.Content, actorOf(ctx)).Scan(&m.At); err != nil {
			return err
		}
		return writeAudit(ctx, tx, le, vendorID, "document."+direction, id, d.SHA256)
	})
	return m, err
}

func (s *Postgres) ListDocuments(ctx context.Context, vendorID string, limit int) ([]DocumentMeta, error) {
	out := []DocumentMeta{}
	err := run(ctx, s, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+docMetaColumns+`, vendor_id::text, direction, uploaded_at from vendor_document where tenant_id = $1 and ($2 = '' or vendor_id::text = $2) order by uploaded_at desc, id limit $3`, tenantOf(ctx), vendorID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m DocumentMeta
			if err := rows.Scan(&m.ID, &m.Title, &m.FileName, &m.ContentType, &m.SizeBytes, &m.SHA256, &m.VendorID, &m.Direction, &m.At); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// GetDocument reads a document with its content and records the download. A vendor id of "" is a buyer's read.
func (s *Postgres) GetDocument(ctx context.Context, vendorID, id string) (DocumentContent, error) {
	did, err := supplierID(id)
	if err != nil {
		return DocumentContent{}, err
	}
	var d DocumentContent
	err = run(ctx, s, func(tx pgx.Tx) error {
		var le string
		if err := tx.QueryRow(ctx, `select `+docMetaColumns+`, vendor_id::text, direction, uploaded_at, content, legal_entity_id::text from vendor_document where tenant_id = $1 and id = $2 and ($3 = '' or vendor_id::text = $3)`,
			tenantOf(ctx), did, vendorID).Scan(&d.ID, &d.Title, &d.FileName, &d.ContentType, &d.SizeBytes, &d.SHA256, &d.VendorID, &d.Direction, &d.At, &d.Content, &le); err != nil {
			return err
		}
		return writeAudit(ctx, tx, le, d.VendorID, "document.downloaded", did, "")
	})
	return d, err
}

func (s *Postgres) PublishControlled(ctx context.Context, docNo string, requiresAck bool, d domain.Document) (DocumentMeta, error) {
	le, err := singleEntity(ctx)
	if err != nil {
		return DocumentMeta{}, err
	}
	m := DocumentMeta{ID: ids.New(), DocNo: docNo, Title: d.Title, FileName: d.FileName, ContentType: d.ContentType, SizeBytes: len(d.Content), SHA256: d.SHA256, RequiresAck: requiresAck}
	err = run(ctx, s, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `insert into controlled_document (tenant_id, id, legal_entity_id, doc_no, revision, title, file_name, content_type, size_bytes, sha256, content, requires_ack, published_by)
			values ($1, $2, $3, $4, coalesce((select max(revision) from controlled_document where tenant_id = $1 and legal_entity_id = $3 and doc_no = $4), 0) + 1, $5, $6, $7, $8, $9, $10, $11, $12)
			returning revision, published_at`, tenantOf(ctx), m.ID, le, docNo, d.Title, d.FileName, d.ContentType, len(d.Content), d.SHA256, d.Content, requiresAck, actorOf(ctx)).Scan(&m.Revision, &m.At)
	})
	return m, err
}

func (s *Postgres) ListControlled(ctx context.Context, limit int) ([]DocumentMeta, error) {
	out := []DocumentMeta{}
	err := run(ctx, s, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+docMetaColumns+`, doc_no, revision, requires_ack, published_at from controlled_document where tenant_id = $1 order by published_at desc, id limit $2`, tenantOf(ctx), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m DocumentMeta
			if err := rows.Scan(&m.ID, &m.Title, &m.FileName, &m.ContentType, &m.SizeBytes, &m.SHA256, &m.DocNo, &m.Revision, &m.RequiresAck, &m.At); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

const distColumns = `d.document_id::text, d.vendor_id::text, d.sent_at, coalesce(d.acknowledged_by, ''), d.acknowledged_at, c.title, c.file_name, c.content_type, c.size_bytes, c.sha256, c.doc_no, c.revision, c.requires_ack`
const distFrom = ` from controlled_distribution d join controlled_document c on c.tenant_id = d.tenant_id and c.id = d.document_id`

func scanDistribution(row pgx.Row) (Distribution, error) {
	var d Distribution
	err := row.Scan(&d.ID, &d.VendorID, &d.SentAt, &d.AcknowledgedBy, &d.AcknowledgedAt, &d.Title, &d.FileName, &d.ContentType, &d.SizeBytes, &d.SHA256, &d.DocNo, &d.Revision, &d.RequiresAck)
	d.DocumentMeta.VendorID = ""
	d.At = d.SentAt
	return d, err
}

// Distribute sends one revision of a controlled document to the named vendors of the caller's legal entity. A vendor that
// already holds the revision keeps its original record.
func (s *Postgres) Distribute(ctx context.Context, documentID string, vendorIDs []string) ([]Distribution, error) {
	did, err := supplierID(documentID)
	if err != nil {
		return nil, err
	}
	out := []Distribution{}
	err = run(ctx, s, func(tx pgx.Tx) error {
		var docLE string
		if err := tx.QueryRow(ctx, `select legal_entity_id::text from controlled_document where tenant_id = $1 and id = $2`, tenantOf(ctx), did).Scan(&docLE); err != nil {
			return err
		}
		for _, raw := range vendorIDs {
			vid, err := supplierID(raw)
			if err != nil {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeSupplierInvalid, "vendorIds must be vendors of the document's legal entity")
			}
			le, err := vendorEntity(ctx, tx, vid)
			if err != nil || le != docLE {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeSupplierInvalid, "vendorIds must be vendors of the document's legal entity")
			}
			tag, err := tx.Exec(ctx, `insert into controlled_distribution (tenant_id, document_id, vendor_id, legal_entity_id, sent_by) values ($1, $2, $3, $4, $5) on conflict do nothing`, tenantOf(ctx), did, vid, le, actorOf(ctx))
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				if err := writeAudit(ctx, tx, le, vid, "distribution.sent", did, ""); err != nil {
					return err
				}
			}
		}
		rows, err := tx.Query(ctx, `select `+distColumns+distFrom+` where d.tenant_id = $1 and d.document_id = $2 and d.vendor_id::text = any($3) order by d.vendor_id`, tenantOf(ctx), did, vendorIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDistribution(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// ListDistributions lists distributions of one document (buyer) or of one vendor (vendor side); either filter may be empty.
func (s *Postgres) ListDistributions(ctx context.Context, documentID, vendorID string) ([]Distribution, error) {
	out := []Distribution{}
	err := run(ctx, s, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+distColumns+distFrom+` where d.tenant_id = $1 and ($2 = '' or d.document_id::text = $2) and ($3 = '' or d.vendor_id::text = $3) order by d.sent_at desc, d.vendor_id limit 200`, tenantOf(ctx), documentID, vendorID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDistribution(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// GetDistributedContent returns a controlled document to a vendor it was distributed to, and records the download.
func (s *Postgres) GetDistributedContent(ctx context.Context, vendorID, documentID string) (DocumentContent, error) {
	did, err := supplierID(documentID)
	if err != nil {
		return DocumentContent{}, err
	}
	var d DocumentContent
	err = run(ctx, s, func(tx pgx.Tx) error {
		var le string
		if err := tx.QueryRow(ctx, `select `+docMetaColumns+`, doc_no, revision, content, legal_entity_id::text from controlled_document c
			where tenant_id = $1 and id = $2 and exists (select 1 from controlled_distribution d where d.tenant_id = c.tenant_id and d.document_id = c.id and d.vendor_id::text = $3)`,
			tenantOf(ctx), did, vendorID).Scan(&d.ID, &d.Title, &d.FileName, &d.ContentType, &d.SizeBytes, &d.SHA256, &d.DocNo, &d.Revision, &d.Content, &le); err != nil {
			return err
		}
		return writeAudit(ctx, tx, le, vendorID, "distribution.downloaded", did, "")
	})
	return d, err
}

// Acknowledge records the vendor's acknowledgement of a distributed revision. A second acknowledgement keeps the first.
func (s *Postgres) Acknowledge(ctx context.Context, vendorID, documentID string) (Distribution, error) {
	did, err := supplierID(documentID)
	if err != nil {
		return Distribution{}, err
	}
	var out Distribution
	err = run(ctx, s, func(tx pgx.Tx) error {
		var le string
		if err := tx.QueryRow(ctx, `select legal_entity_id::text from controlled_distribution where tenant_id = $1 and document_id = $2 and vendor_id::text = $3 for update`, tenantOf(ctx), did, vendorID).Scan(&le); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update controlled_distribution set acknowledged_by = $4, acknowledged_at = now() where tenant_id = $1 and document_id = $2 and vendor_id::text = $3 and acknowledged_at is null`, tenantOf(ctx), did, vendorID, actorOf(ctx))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			if err := writeAudit(ctx, tx, le, vendorID, "distribution.acknowledged", did, ""); err != nil {
				return err
			}
		}
		out, err = scanDistribution(tx.QueryRow(ctx, `select `+distColumns+distFrom+` where d.tenant_id = $1 and d.document_id = $2 and d.vendor_id::text = $3`, tenantOf(ctx), did, vendorID))
		return err
	})
	return out, err
}

func (s *Postgres) ListAudit(ctx context.Context, vendorID string, limit int) ([]AuditEntry, error) {
	out := []AuditEntry{}
	err := run(ctx, s, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, vendor_id::text, actor, action, coalesce(subject_id::text, ''), detail, at from vendor_portal_audit where tenant_id = $1 and ($2 = '' or vendor_id::text = $2) order by at desc, id limit $3`, tenantOf(ctx), vendorID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e AuditEntry
			if err := rows.Scan(&e.ID, &e.VendorID, &e.Actor, &e.Action, &e.SubjectID, &e.Detail, &e.At); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// applyProfileChange writes an approved PROFILE_CHANGE to the vendor's party in the approval's own transaction: the party row,
// its party.updated.v1 event and the audit entry commit together with the decision, or none of them does. A change the party
// cannot take (it was merged, or the values no longer validate) fails the approval with the party's problem and the request stays
// SUBMITTED for the buyer to reject with a note.
func applyProfileChange(ctx context.Context, tx pgx.Tx, le string, r VendorRequest) error {
	fields, errs := domain.ProfileChange(r.Changes)
	if len(errs) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeSupplierInvalid, "the requested profile change is not valid")
		p.Errors = errs
		return p
	}
	var partyID string
	if err := tx.QueryRow(ctx, `select party_id::text from vendor_role where tenant_id = $1 and id = $2`, tenantOf(ctx), r.VendorID).Scan(&partyID); err != nil {
		return err
	}
	_, changed, err := updatePartyTx(ctx, tx, partyID, -1, PartyPatch{Fields: fields})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update vendor_request set applied_at = now() where tenant_id = $1 and id = $2`, tenantOf(ctx), r.ID); err != nil {
		return err
	}
	return writeAudit(ctx, tx, le, r.VendorID, "profile.applied", r.ID, strings.Join(changed, ","))
}
