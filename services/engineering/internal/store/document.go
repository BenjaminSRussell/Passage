package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	engevents "github.com/BenjaminSRussell/passage/services/engineering/internal/events"
)

const aggregateDocument = "document"

func (s *Postgres) CreateDocumentType(ctx context.Context, t domain.DocumentType, idem Idem) (domain.DocumentType, error) {
	if t.ID == "" {
		t.ID = ids.New()
	}
	if t.SignatureMeaning == "" {
		t.SignatureMeaning = "Approved for release"
	}
	return createOnce(s, ctx, idem, t.ID, t, func(ctx context.Context, tx pgx.Tx, tenantID string, v domain.DocumentType) error {
		_, err := tx.Exec(ctx, `insert into document_type (tenant_id, id, code, name, change_controlled, signature_on_release, independent_review, signature_meaning)
			values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			tenantID, v.ID, v.Code, v.Name, v.ChangeControlled, v.SignatureOnRelease, v.IndependentReview, v.SignatureMeaning)
		return err
	}, func(ctx context.Context, id string) (domain.DocumentType, error) {
		return s.getDocumentTypeByID(ctx, id)
	})
}

func (s *Postgres) getDocumentTypeByID(ctx context.Context, id string) (domain.DocumentType, error) {
	var t domain.DocumentType
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		err := tx.QueryRow(ctx, `select id::text, code, name, change_controlled, signature_on_release, independent_review, signature_meaning from document_type where tenant_id = $1 and id = $2::uuid`, tenantID, id).
			Scan(&t.ID, &t.Code, &t.Name, &t.ChangeControlled, &t.SignatureOnRelease, &t.IndependentReview, &t.SignatureMeaning)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, "ENG-DOC-TYPE-NOTFOUND", "document type not found")
		}
		return err
	})
	t.Network = domain.DefaultStatusNetwork()
	return t, err
}

func (s *Postgres) GetDocumentType(ctx context.Context, code string) (domain.DocumentType, error) {
	var t domain.DocumentType
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		err := tx.QueryRow(ctx, `select id::text, code, name, change_controlled, signature_on_release, independent_review, signature_meaning from document_type where tenant_id = $1 and code = $2`, tenantID, code).
			Scan(&t.ID, &t.Code, &t.Name, &t.ChangeControlled, &t.SignatureOnRelease, &t.IndependentReview, &t.SignatureMeaning)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, "ENG-DOC-TYPE-NOTFOUND", "document type not found")
		}
		return err
	})
	t.Network = domain.DefaultStatusNetwork()
	return t, err
}

func (s *Postgres) ListDocumentTypes(ctx context.Context, afterID string, limit int) (Page[domain.DocumentType], error) {
	return listSimple(s, ctx, afterID, limit, `select id::text, code, name, change_controlled, signature_on_release, independent_review, signature_meaning from document_type where tenant_id = $1 and id > $2::uuid order by id limit $3`,
		func(row pgx.Row) (domain.DocumentType, error) {
			var t domain.DocumentType
			err := row.Scan(&t.ID, &t.Code, &t.Name, &t.ChangeControlled, &t.SignatureOnRelease, &t.IndependentReview, &t.SignatureMeaning)
			t.Network = domain.DefaultStatusNetwork()
			return t, err
		})
}

func (s *Postgres) CreateDocument(ctx context.Context, d domain.Document, idem Idem) (domain.Document, error) {
	if d.ID == "" {
		d.ID = ids.New()
	}
	if d.Status == "" {
		d.Status = domain.DocInWork
	}
	if d.Part == "" {
		d.Part = "000"
	}
	if d.Version == "" {
		d.Version = "01"
	}
	return createOnce(s, ctx, idem, d.ID, d, func(ctx context.Context, tx pgx.Tx, tenantID string, v domain.Document) error {
		if _, err := tx.Exec(ctx, `insert into document (tenant_id, id, type_code, number, part, version, description, status, last_editor, eco_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, nullif($10, '')::uuid)`,
			tenantID, v.ID, v.TypeCode, v.Number, v.Part, v.Version, v.Description, v.Status, v.LastEditor, v.EcoID); err != nil {
			return err
		}
		for _, o := range v.Originals {
			if _, err := tx.Exec(ctx, `insert into document_original (tenant_id, document_id, storage_key, sha256, size_bytes) values ($1, $2, $3, $4, $5)`,
				tenantID, v.ID, o.StorageKey, o.SHA256, o.SizeBytes); err != nil {
				return err
			}
		}
		for _, l := range v.Links {
			if _, err := tx.Exec(ctx, `insert into document_link (tenant_id, document_id, target_type, target_id) values ($1, $2, $3, $4)`,
				tenantID, v.ID, l.TargetType, l.TargetID); err != nil {
				return err
			}
		}
		return nil
	}, s.GetDocument)
}

func (s *Postgres) GetDocument(ctx context.Context, id string) (domain.Document, error) {
	var d domain.Document
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) (err error) {
		d, err = getDocument(ctx, tx, tenantID, id)
		return err
	})
	return d, err
}

func getDocument(ctx context.Context, tx pgx.Tx, tenantID, id string) (domain.Document, error) {
	var d domain.Document
	var signed *time.Time
	err := tx.QueryRow(ctx, `select id::text, type_code, number, part, version, description, status, last_editor, coalesce(eco_id::text, ''), signed_at
		from document where tenant_id = $1 and id = $2::uuid`, tenantID, id).
		Scan(&d.ID, &d.TypeCode, &d.Number, &d.Part, &d.Version, &d.Description, &d.Status, &d.LastEditor, &d.EcoID, &signed)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, httpx.NewProblem(http.StatusNotFound, "ENG-DOC-NOTFOUND", "document not found")
	}
	if err != nil {
		return d, err
	}
	d.SignedAt = valueOrZero(signed)
	origs, err := tx.Query(ctx, `select storage_key, sha256, size_bytes from document_original where tenant_id = $1 and document_id = $2::uuid order by storage_key`, tenantID, id)
	if err != nil {
		return d, err
	}
	defer origs.Close()
	for origs.Next() {
		var o domain.DocumentOriginal
		if err := origs.Scan(&o.StorageKey, &o.SHA256, &o.SizeBytes); err != nil {
			return d, err
		}
		d.Originals = append(d.Originals, o)
	}
	if err := origs.Err(); err != nil {
		return d, err
	}
	origs.Close()
	links, err := tx.Query(ctx, `select target_type, target_id from document_link where tenant_id = $1 and document_id = $2::uuid order by target_type, target_id`, tenantID, id)
	if err != nil {
		return d, err
	}
	defer links.Close()
	for links.Next() {
		var l domain.DocumentLink
		if err := links.Scan(&l.TargetType, &l.TargetID); err != nil {
			return d, err
		}
		d.Links = append(d.Links, l)
	}
	if err := links.Err(); err != nil {
		return d, err
	}
	links.Close()
	children, err := tx.Query(ctx, `select child_id::text from document_structure where tenant_id = $1 and parent_id = $2::uuid order by position, child_id`, tenantID, id)
	if err != nil {
		return d, err
	}
	defer children.Close()
	for children.Next() {
		var child string
		if err := children.Scan(&child); err != nil {
			return d, err
		}
		d.Children = append(d.Children, child)
	}
	return d, children.Err()
}

func (s *Postgres) ListDocuments(ctx context.Context, number, afterID string, limit int) (Page[domain.Document], error) {
	p, _ := tenant.FromContext(ctx)
	if afterID == "" {
		afterID = ids.Nil
	}
	var items []domain.Document
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, type_code, number, part, version, description, status, last_editor, coalesce(eco_id::text, ''), signed_at
			from document where tenant_id = $1 and id > $2::uuid and ($3 = '' or number = $3) order by id limit $4`, p.TenantID, afterID, number, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d domain.Document
			var signed *time.Time
			if err := rows.Scan(&d.ID, &d.TypeCode, &d.Number, &d.Part, &d.Version, &d.Description, &d.Status, &d.LastEditor, &d.EcoID, &signed); err != nil {
				return err
			}
			d.SignedAt = valueOrZero(signed)
			items = append(items, d)
		}
		return rows.Err()
	})
	return trimPage(items, limit, func(d domain.Document) string { return d.ID }), err
}

func lockDocument(ctx context.Context, tx pgx.Tx, tenantID, id string) error {
	var locked string
	err := tx.QueryRow(ctx, `select id::text from document where tenant_id = $1 and id = $2::uuid for update`, tenantID, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NewProblem(http.StatusNotFound, "ENG-DOC-NOTFOUND", "document not found")
	}
	return err
}

func hasReleaseSignature(ctx context.Context, tx pgx.Tx, tenantID, documentID string) (bool, error) {
	var signed bool
	err := tx.QueryRow(ctx, `select exists(select 1 from document_signature where tenant_id = $1 and document_id = $2::uuid and transition = 'released')`, tenantID, documentID).Scan(&signed)
	return signed, err
}

func (s *Postgres) TransitionDocument(ctx context.Context, id, to, actor string, sig *domain.Signature) (domain.Document, error) {
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockDocument(ctx, tx, p.TenantID, id); err != nil {
			return err
		}
		doc, err := getDocument(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		typ, err := getDocumentTypeByCode(ctx, tx, p.TenantID, doc.TypeCode)
		if err != nil {
			return err
		}
		if err := typ.Network.CanTransition(doc.Status, to, ""); err != nil {
			return err
		}
		if err := doc.CanLeaveRelease(typ, to); err != nil {
			return err
		}
		awaitingEco := false
		if to == domain.DocReleased {
			prev, err := previousReleased(ctx, tx, p.TenantID, doc.Number, doc.ID)
			if err != nil {
				return err
			}
			var eco *domain.Eco
			if doc.EcoID != "" {
				loaded, err := getEco(ctx, tx, p.TenantID, doc.EcoID)
				if err != nil {
					return err
				}
				eco = &loaded
			}
			if err := doc.CanRelease(typ, prev, eco); err != nil {
				return err
			}
			awaitingEco = typ.ChangeControlled && prev.ID != ""
		}
		if typ.Network.RequiresSignature(doc.Status, to) {
			if sig == nil || sig.RecordID != doc.ID || sig.Transition != to {
				return &domain.Error{Code: domain.CodeDocReauth, Detail: "this transition requires a Part 11 signature for this document"}
			}
			if sig.OriginalsHash != domain.HashRecord(doc) || sig.DocumentNumber != doc.Number || sig.DocumentVersion != doc.Version {
				return &domain.Error{Code: domain.CodeDocState, Detail: "the document changed after it was presented for signature; sign it again"}
			}
			if awaitingEco {
				signed, err := hasReleaseSignature(ctx, tx, p.TenantID, doc.ID)
				if err != nil {
					return err
				}
				if signed {
					return &domain.Error{Code: domain.CodeDocState, Detail: "the document is signed and waits for its ECO to release"}
				}
			}
			if sig.ID == "" {
				sig.ID = ids.New()
			}
			if _, err := tx.Exec(ctx, `insert into document_signature (tenant_id, id, document_id, document_number, document_version, transition, signer_id, printed_name, meaning, signed_at, originals_hash, method)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
				p.TenantID, sig.ID, doc.ID, sig.DocumentNumber, sig.DocumentVersion, sig.Transition, sig.SignerID, sig.PrintedName, sig.Meaning, sig.SignedAt, sig.OriginalsHash, sig.Method); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update document set signed_at = $3 where tenant_id = $1 and id = $2::uuid`, p.TenantID, doc.ID, sig.SignedAt); err != nil {
				return err
			}
			if err := emit(ctx, tx, p, doc.ID, engevents.DocumentSigned, aggregateDocument, doc.ID, *sig); err != nil {
				return err
			}
			if err := enqueueAudit(ctx, tx, p, *sig); err != nil {
				return err
			}
			if awaitingEco {
				return nil
			}
		}
		if _, err := tx.Exec(ctx, `update document set status = $3, last_editor = case when $3 = 'in_work' then $4 else last_editor end where tenant_id = $1 and id = $2::uuid`,
			p.TenantID, doc.ID, to, actor); err != nil {
			return err
		}
		doc.Status = to
		if to == domain.DocReleased {
			return emit(ctx, tx, p, doc.ID, engevents.DocumentReleased, aggregateDocument, doc.ID, doc)
		}
		return nil
	})
	if err != nil {
		return domain.Document{}, err
	}
	return s.GetDocument(ctx, id)
}

func getDocumentTypeByCode(ctx context.Context, tx pgx.Tx, tenantID, code string) (domain.DocumentType, error) {
	var t domain.DocumentType
	err := tx.QueryRow(ctx, `select id::text, code, name, change_controlled, signature_on_release, independent_review, signature_meaning from document_type where tenant_id = $1 and code = $2`, tenantID, code).
		Scan(&t.ID, &t.Code, &t.Name, &t.ChangeControlled, &t.SignatureOnRelease, &t.IndependentReview, &t.SignatureMeaning)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, httpx.NewProblem(http.StatusNotFound, "ENG-DOC-TYPE-NOTFOUND", "document type not found")
	}
	t.Network = domain.DefaultStatusNetwork()
	return t, err
}

func previousReleased(ctx context.Context, tx pgx.Tx, tenantID, number, exceptID string) (domain.Document, error) {
	var d domain.Document
	err := tx.QueryRow(ctx, `select id::text, type_code, number, part, version, description, status, last_editor, coalesce(eco_id::text, '')
		from document where tenant_id = $1 and number = $2 and status = 'released' and id <> $3::uuid order by version desc, id limit 1`, tenantID, number, exceptID).
		Scan(&d.ID, &d.TypeCode, &d.Number, &d.Part, &d.Version, &d.Description, &d.Status, &d.LastEditor, &d.EcoID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Document{}, nil
	}
	return d, err
}

func (s *Postgres) ListSignatures(ctx context.Context, documentID string) ([]domain.Signature, error) {
	var out []domain.Signature
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select id::text, document_id::text, document_number, document_version, transition, signer_id, printed_name, meaning, signed_at, originals_hash, method
			from document_signature where tenant_id = $1 and document_id = $2::uuid order by signed_at, id`, tenantID, documentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sig domain.Signature
			if err := rows.Scan(&sig.ID, &sig.RecordID, &sig.DocumentNumber, &sig.DocumentVersion, &sig.Transition, &sig.SignerID, &sig.PrintedName, &sig.Meaning, &sig.SignedAt, &sig.OriginalsHash, &sig.Method); err != nil {
				return err
			}
			out = append(out, sig)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) DocumentWhereUsed(ctx context.Context, id string) ([]domain.DocumentWhereUsed, error) {
	var out []domain.DocumentWhereUsed
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select 'document', parent_id::text, coalesce((select number from document d where d.tenant_id = s.tenant_id and d.id = s.parent_id), '')
			from document_structure s where s.tenant_id = $1 and s.child_id = $2::uuid
			union all
			select 'bom', l.bom_version_id::text, v.product_id
			from bom_line l
			join bom_version v on v.tenant_id = l.tenant_id and v.id = l.bom_version_id
			join document d on d.tenant_id = l.tenant_id and d.id = $2::uuid and l.component_product_id in (d.number, d.id::text)
			where l.tenant_id = $1
			order by 1, 2`, tenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w domain.DocumentWhereUsed
			if err := rows.Scan(&w.Kind, &w.ParentID, &w.Number); err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) SetDocumentStructure(ctx context.Context, parentID string, children []string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockDocument(ctx, tx, p.TenantID, parentID); err != nil {
			return err
		}
		parent, err := getDocument(ctx, tx, p.TenantID, parentID)
		if err != nil {
			return err
		}
		if err := parent.AssertUnlocked(); err != nil {
			return err
		}
		var known int
		if err := tx.QueryRow(ctx, `select count(*) from document where tenant_id = $1 and id = any($2::uuid[])`, p.TenantID, children).Scan(&known); err != nil {
			return err
		}
		if known != len(children) {
			return httpx.NewProblem(http.StatusNotFound, "ENG-DOC-NOTFOUND", "a child document does not exist")
		}
		existing := map[string][]string{}
		rows, err := tx.Query(ctx, `select parent_id::text, child_id::text from document_structure where tenant_id = $1`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var parent, child string
			if err := rows.Scan(&parent, &child); err != nil {
				return err
			}
			existing[parent] = append(existing[parent], child)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if err := domain.CheckDocumentCycle(parentID, children, existing); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from document_structure where tenant_id = $1 and parent_id = $2::uuid`, p.TenantID, parentID); err != nil {
			return err
		}
		for i, child := range children {
			if _, err := tx.Exec(ctx, `insert into document_structure (tenant_id, parent_id, child_id, position) values ($1, $2, $3, $4)`, p.TenantID, parentID, child, i); err != nil {
				return err
			}
		}
		return nil
	})
}

func releaseEcoDocuments(ctx context.Context, tx pgx.Tx, p tenant.Principal, e domain.Eco) error {
	for _, r := range e.Documents {
		for _, id := range []string{r.OldDocumentID, r.NewDocumentID} {
			if err := lockDocument(ctx, tx, p.TenantID, id); err != nil {
				return err
			}
		}
		prev, err := getDocument(ctx, tx, p.TenantID, r.OldDocumentID)
		if err != nil {
			return err
		}
		cur, err := getDocument(ctx, tx, p.TenantID, r.NewDocumentID)
		if err != nil {
			return err
		}
		signed, err := hasReleaseSignature(ctx, tx, p.TenantID, cur.ID)
		if err != nil {
			return err
		}
		if err := domain.ValidateReplacement(prev, cur, signed); err != nil {
			return err
		}
		pair := domain.ReleasePair(prev, cur)
		if _, err := tx.Exec(ctx, `update document set status = $3 where tenant_id = $1 and id = $2::uuid`, p.TenantID, pair.Previous.ID, pair.Previous.Status); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update document set status = $3, eco_id = $4 where tenant_id = $1 and id = $2::uuid`, p.TenantID, pair.Current.ID, pair.Current.Status, e.ID); err != nil {
			return err
		}
		if err := emit(ctx, tx, p, pair.Current.ID, engevents.DocumentReleased, aggregateDocument, pair.Current.ID, pair.Current); err != nil {
			return err
		}
	}
	return nil
}
