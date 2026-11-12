package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const (
	CodeOutboundNotFound    = "MIG-OUTBOUND-NOT-FOUND"
	CodeOutboundVersionGone = "MIG-OUTBOUND-VERSION-NOT-FOUND"
	CodeOutboundNameExists  = "MIG-OUTBOUND-NAME-EXISTS"
	CodeOutboundArchived    = "MIG-OUTBOUND-ARCHIVED"
	CodeXrefConflict        = "MIG-XREF-CONFLICT"
	CodeXrefEventConflict   = "MIG-XREF-EVENT-CONFLICT"
	CodeXrefRefMismatch     = "MIG-XREF-REF-MISMATCH"
	// HubService is the issuer of the only service token the outbound store serves (services/integration-hub-ts).
	HubService = "integration-hub"
	// The route duties the outbound store admits (the API enforces them per route; the store checks them again before
	// it opens a transaction with the all-entities scope).
	DutyOutboundRender       = "migration.outbound.render"
	DutyXrefRecord           = "migration.xref.record"
	// DutyXrefRead lets a loading service (the migration factory) resolve legacy keys to the Passage ids this service loaded
	// (#49, H-S8-6); it is service-only and reads no other route.
	DutyXrefRead = "migration.xref.read"
	DutyRun                  = "migration.run"
	DutyProjectWrite         = "migration.project.write"
	OutboundActive           = "active"
	OutboundArchivedStatus   = "archived"
	maxLegacyKeyLength       = 512
	maxRefsPerRecord         = 4100 // the header and every element of the most a definition can iterate (4 collections of 1000)
	outboundScopeRefused     = "AUTH-0003"
	outboundNameUniqueIndex  = "outbound_mapping_tenant_id_name_key"
	zeroUUID                 = "00000000-0000-0000-0000-000000000000"
	outboundMappingColumns   = `m.id, m.name, m.source_system, m.event_type, m.status, m.version, m.created_by, m.created_at, m.updated_at`
	outboundMappingFromWhere = ` from migration.outbound_mapping m `
)

// OutboundMapping is an outbound mapping as the API shows it: the header, and the definition of one version (the
// current one unless a version was asked for; lists leave it out).
type OutboundMapping struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	SourceSystem string          `json:"sourceSystem"`
	EventType    string          `json:"eventType"`
	Status       string          `json:"status"`
	Version      int             `json:"version"`
	Checksum     string          `json:"checksum,omitempty"`
	Definition   json.RawMessage `json:"definition,omitempty"`
	CreatedBy    string          `json:"createdBy"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

// OutboundVersion is one row of a mapping's history.
type OutboundVersion struct {
	Version   int       `json:"version"`
	Checksum  string    `json:"checksum"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

type OutboundFilter struct {
	Status string
	After  *ProjectCursor // (name, id) keyset
	Limit  int
}

// OutboundXrefRecord is what the hub reports after the legacy ERP accepted a push.
type OutboundXrefRecord struct {
	MappingID string
	EventID   string
	LegacyKey string
	// Refs are the records the push created. A ref carries the LegacyKey the legacy ERP assigned it; at most one ref may
	// omit it and then takes LegacyKey (the single-record form: one ref, or none for a mapping without a subject).
	Refs  []domain.OutboundRef
	Actor string
}

// OutboundStore is the storage of integration track S5. Mappings and the cross-reference are tenant data (no legal
// entity column; their restrictive policy admits only the all-entities scope), so every method runs with that scope,
// and outboundTx grants it to two callers only: a tenant administrator without a legal-entity claim holding the
// method's human duty, and the integration hub's service token (issuer integration-hub, one legal entity, the
// method's service duty). Anyone else is refused before the transaction opens.
type OutboundStore interface {
	SaveOutboundMapping(ctx context.Context, in OutboundMapping, idempotencyKey string) (saved OutboundMapping, created bool, err error)
	// GetOutboundMapping returns the header with the definition of version (0 is the current one).
	GetOutboundMapping(ctx context.Context, id string, version int) (OutboundMapping, error)
	ListOutboundMappings(ctx context.Context, f OutboundFilter) ([]OutboundMapping, error)
	ListOutboundVersions(ctx context.Context, id string) ([]OutboundVersion, error)
	// UpdateOutboundMapping applies edit under the mapping's version and writes version+1 (409 MIG-VERSION-CONFLICT otherwise).
	UpdateOutboundMapping(ctx context.Context, id string, version int, edit func(*OutboundMapping) error) (OutboundMapping, error)
	// RecordOutboundXref stores the legacy keys of the push of one event, all or none (one transaction): the header's
	// and one per line the push created. replayed is true when the same call was made before. The same event with
	// other keys or records, or a key that belongs to another record, is a 409.
	RecordOutboundXref(ctx context.Context, in OutboundXrefRecord) (replayed bool, err error)
	domain.LegacyKeyResolver
}

func outboundNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeOutboundNotFound, "outbound mapping not found")
}

func outboundVersionGone(v int) error {
	return httpx.NewProblem(http.StatusNotFound, CodeOutboundVersionGone, "outbound mapping has no version "+strconv.Itoa(v))
}

func outboundNameUsed() error {
	return httpx.NewProblem(http.StatusConflict, CodeOutboundNameExists, "an outbound mapping with this name already exists")
}

func xrefConflict(key string) error {
	return httpx.NewProblem(http.StatusConflict, CodeXrefConflict, "legacy key "+key+" is already recorded for another Passage record")
}

func xrefEventConflict(eventID string) error {
	return httpx.NewProblem(http.StatusConflict, CodeXrefEventConflict, "event "+eventID+" already recorded a different legacy key or record for this mapping")
}

// OutboundChecksum is the SHA-256 of a definition's canonical JSON (keys sorted, no insignificant space), so the
// text Postgres hands back for a jsonb column hashes like the text that was stored.
func OutboundChecksum(definition []byte) string {
	canonical := definition
	dec := json.NewDecoder(bytes.NewReader(definition))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) == nil {
		if out, err := json.Marshal(v); err == nil {
			canonical = out
		}
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func sameRefs(a, b []domain.OutboundRef) bool {
	return slices.EqualFunc(a, b, func(x, y domain.OutboundRef) bool {
		return x.Entity == y.Entity && strings.EqualFold(x.ID, y.ID) && x.LegacyKey == y.LegacyKey
	})
}

// ValidateOutboundRecord checks the shape of an xref:record call; the API and the stores both rely on it.
func ValidateOutboundRecord(in OutboundXrefRecord) map[string]string {
	bad := map[string]string{}
	if in.EventID == "" || len(in.EventID) > 256 {
		bad["eventId"] = "is required, at most 256 characters"
	}
	if len(in.Refs) > maxRefsPerRecord {
		bad["refs"] = "at most " + strconv.Itoa(maxRefsPerRecord) + " refs: the header and its lines, or none for a mapping without a subject"
	}
	keyless := 0
	for _, r := range in.Refs {
		switch {
		case r.Entity == "" || len(r.Entity) > 64 || !ids.Valid(r.ID):
			bad["refs"] = "each reference needs an entity and a Passage id (UUID)"
		case r.LegacyKey == "":
			keyless++
		case strings.TrimSpace(r.LegacyKey) == "" || len(r.LegacyKey) > maxLegacyKeyLength:
			bad["refs"] = "a reference's legacyKey is at most 512 characters and not blank"
		}
	}
	top := strings.TrimSpace(in.LegacyKey) != ""
	switch {
	case len(in.LegacyKey) > maxLegacyKeyLength:
		bad["legacyKey"] = "is required, at most 512 characters"
	case keyless > 1:
		bad["refs"] = "at most one ref may omit its legacyKey (it takes the top-level legacyKey); give every other ref its own"
	case keyless == 1 && !top, len(in.Refs) == 0 && !top:
		bad["legacyKey"] = "is required unless every ref carries its own legacyKey, at most 512 characters"
	case keyless == 0 && top && len(in.Refs) > 0 && in.LegacyKey != in.Refs[0].LegacyKey:
		bad["legacyKey"] = "applies to a ref without its own legacyKey and every ref has one"
	}
	return bad
}

// outboundAccess names who a store method serves: the human duty a tenant administrator needs, and the service duty
// the integration hub's token needs ("" when the method is not the hub's).
type outboundAccess struct{ human, hub string }

var (
	obRead   = outboundAccess{human: DutyRun}
	obWrite  = outboundAccess{human: DutyProjectWrite}
	obRender = outboundAccess{human: DutyRun, hub: DutyOutboundRender}
	obRecord = outboundAccess{hub: DutyXrefRecord}
)

// hubHolds checks the hub's service duty with literal constants, so the identity contract test can see which duties
// the store enforces.
func hubHolds(p tenant.Principal, duty string) bool {
	switch duty {
	case DutyOutboundRender:
		return p.HasDuty(DutyOutboundRender)
	case DutyXrefRecord:
		return p.HasDuty(DutyXrefRecord)
	}
	return false
}

// outboundTx opens the transaction with the all-entities scope for the callers access admits, and refuses everyone
// else with 403 without opening it. A tenant administrator passes dbx.TenantWide (no legal-entity claim); the hub's
// token always names one legal entity (the verifier refuses one without) and is admitted on its issuer, which only a
// verified service token sets, and its duty. The API has checked the same before; this keeps the widening in one place.
func (s *Postgres) outboundTx(ctx context.Context, access outboundAccess, fn func(tx pgx.Tx) error) error {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("store: no principal in context")
	}
	hub := access.hub != "" && p.Service == HubService && p.LegalEntityID != "" && hubHolds(p, access.hub)
	human := access.human != "" && p.Service == "" && dbx.TenantWide(p, access.human)
	if !hub && !human {
		return httpx.NewProblem(http.StatusForbidden, outboundScopeRefused, "outbound mappings are tenant data: a tenant administrator without a legal-entity scope, or the integration hub, may use them")
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, p.TenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func scanOutbound(row pgx.Row) (OutboundMapping, error) {
	var m OutboundMapping
	err := row.Scan(&m.ID, &m.Name, &m.SourceSystem, &m.EventType, &m.Status, &m.Version, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

func (s *Postgres) SaveOutboundMapping(ctx context.Context, in OutboundMapping, idempotencyKey string) (OutboundMapping, bool, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	in.Checksum = OutboundChecksum(in.Definition)
	var saved OutboundMapping
	created := false
	err := s.outboundTx(ctx, obWrite, func(tx pgx.Tx) error {
		byKey := func() (OutboundMapping, error) {
			prior, err := scanOutbound(tx.QueryRow(ctx, `select `+outboundMappingColumns+outboundMappingFromWhere+`where m.tenant_id = $1 and m.idempotency_key = $2`, p.TenantID, idempotencyKey))
			if err != nil {
				return prior, err
			}
			var checksum string
			if err := tx.QueryRow(ctx, `select checksum from migration.outbound_mapping_version where tenant_id = $1 and mapping_id = $2 and version = 1`, p.TenantID, prior.ID).Scan(&checksum); err != nil {
				return prior, err
			}
			if prior.Name != in.Name || prior.SourceSystem != in.SourceSystem || checksum != in.Checksum {
				return prior, keyReuse()
			}
			return prior, nil
		}
		if idempotencyKey != "" {
			prior, err := byKey()
			if err == nil {
				saved = prior
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `insert into migration.outbound_mapping (tenant_id, id, name, source_system, event_type, created_by, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7) on conflict (tenant_id, idempotency_key) do nothing`,
			p.TenantID, in.ID, in.Name, in.SourceSystem, in.EventType, p.UserID, nullIfEmpty(idempotencyKey))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 { // a concurrent call with the same key won
			prior, err := byKey()
			saved = prior
			return err
		}
		if _, err := tx.Exec(ctx, `insert into migration.outbound_mapping_version (tenant_id, mapping_id, version, definition, checksum, created_by)
			values ($1, $2, 1, $3, $4, $5)`, p.TenantID, in.ID, in.Definition, in.Checksum, p.UserID); err != nil {
			return err
		}
		created = true
		saved, err = scanOutbound(tx.QueryRow(ctx, `select `+outboundMappingColumns+outboundMappingFromWhere+`where m.tenant_id = $1 and m.id = $2`, p.TenantID, in.ID))
		return err
	})
	if isUniqueViolation(err, outboundNameUniqueIndex) {
		return OutboundMapping{}, false, outboundNameUsed()
	}
	if err != nil {
		return OutboundMapping{}, false, err
	}
	if created {
		saved.Definition, saved.Checksum = in.Definition, in.Checksum
	}
	return saved, created, nil
}

func (s *Postgres) GetOutboundMapping(ctx context.Context, id string, version int) (OutboundMapping, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return OutboundMapping{}, outboundNotFound()
	}
	var out OutboundMapping
	err := s.outboundTx(ctx, obRender, func(tx pgx.Tx) error {
		var err error
		out, err = scanOutbound(tx.QueryRow(ctx, `select `+outboundMappingColumns+outboundMappingFromWhere+`where m.tenant_id = $1 and m.id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return outboundNotFound()
		}
		if err != nil {
			return err
		}
		want := version
		if want == 0 {
			want = out.Version
		}
		err = tx.QueryRow(ctx, `select definition, checksum from migration.outbound_mapping_version where tenant_id = $1 and mapping_id = $2 and version = $3`,
			p.TenantID, id, want).Scan(&out.Definition, &out.Checksum)
		if errors.Is(err, pgx.ErrNoRows) {
			return outboundVersionGone(want)
		}
		if err != nil {
			return err
		}
		out.Version = want
		return nil
	})
	if err != nil {
		return OutboundMapping{}, err
	}
	return out, nil
}

func (s *Postgres) ListOutboundMappings(ctx context.Context, f OutboundFilter) ([]OutboundMapping, error) {
	p, _ := tenant.FromContext(ctx)
	afterName, afterID := "", zeroUUID
	if f.After != nil {
		afterName, afterID = f.After.Name, f.After.ID
	}
	var out []OutboundMapping
	err := s.outboundTx(ctx, obRead, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+outboundMappingColumns+outboundMappingFromWhere+`
			where m.tenant_id = $1 and ($2 = '' or m.status = $2) and ($3::boolean is false or (m.name, m.id) > ($4, $5::uuid))
			order by m.name, m.id limit $6`, p.TenantID, f.Status, f.After != nil, afterName, afterID, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanOutbound(rows)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) ListOutboundVersions(ctx context.Context, id string) ([]OutboundVersion, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return nil, outboundNotFound()
	}
	out := []OutboundVersion{}
	err := s.outboundTx(ctx, obRead, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `select 1 from migration.outbound_mapping where tenant_id = $1 and id = $2`, p.TenantID, id).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
			return outboundNotFound()
		} else if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select version, checksum, created_by, created_at from migration.outbound_mapping_version where tenant_id = $1 and mapping_id = $2 order by version desc`, p.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v OutboundVersion
			if err := rows.Scan(&v.Version, &v.Checksum, &v.CreatedBy, &v.CreatedAt); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) UpdateOutboundMapping(ctx context.Context, id string, version int, edit func(*OutboundMapping) error) (OutboundMapping, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return OutboundMapping{}, outboundNotFound()
	}
	var out OutboundMapping
	err := s.outboundTx(ctx, obWrite, func(tx pgx.Tx) error {
		cur, err := scanOutbound(tx.QueryRow(ctx, `select `+outboundMappingColumns+outboundMappingFromWhere+`where m.tenant_id = $1 and m.id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return outboundNotFound()
		}
		if err != nil {
			return err
		}
		if cur.Version != version {
			return versionConflict(cur.Version)
		}
		if err := tx.QueryRow(ctx, `select definition from migration.outbound_mapping_version where tenant_id = $1 and mapping_id = $2 and version = $3`, p.TenantID, id, cur.Version).Scan(&cur.Definition); err != nil {
			return err
		}
		if err := edit(&cur); err != nil {
			return err
		}
		next := cur.Version + 1
		checksum := OutboundChecksum(cur.Definition)
		if _, err := tx.Exec(ctx, `insert into migration.outbound_mapping_version (tenant_id, mapping_id, version, definition, checksum, created_by) values ($1, $2, $3, $4, $5, $6)`,
			p.TenantID, id, next, cur.Definition, checksum, p.UserID); err != nil {
			return err
		}
		out, err = scanOutbound(tx.QueryRow(ctx, `update migration.outbound_mapping m set name = $3, event_type = $4, status = $5, version = $6, updated_at = now()
			where m.tenant_id = $1 and m.id = $2 returning `+outboundMappingColumns, p.TenantID, id, cur.Name, cur.EventType, cur.Status, next))
		out.Definition, out.Checksum = cur.Definition, checksum
		return err
	})
	if isUniqueViolation(err, outboundNameUniqueIndex) {
		return OutboundMapping{}, outboundNameUsed()
	}
	return out, err
}

func (s *Postgres) LegacyKeys(ctx context.Context, sourceSystem, entity string, passageIDs []string) (map[string]string, error) {
	p, _ := tenant.FromContext(ctx)
	canon := map[string][]string{}
	var uuids []string
	for _, id := range passageIDs {
		if c, ok := ids.Canonical(id); ok {
			if _, seen := canon[c]; !seen {
				uuids = append(uuids, c)
			}
			canon[c] = append(canon[c], id)
		}
	}
	out := map[string]string{}
	if len(uuids) == 0 {
		return out, nil
	}
	err := s.outboundTx(ctx, obRender, func(tx pgx.Tx) error {
		// A record merged into a survivor answers to the survivor's id. The two predicates use the target_id and
		// merged_into_target_id indexes of 0005; the lowest legacy key wins when a record has several.
		//
		// The transaction has the all-entities scope (see outboundTx), so the legal entity is filtered here: a token
		// that names a legal entity (the hub's always does) sees the rows of that legal entity and the tenant-wide ones
		// (legal_entity_id is null), never another legal entity's. A tenant administrator (no legal-entity claim) sees all.
		le, _ := ids.Canonical(p.LegalEntityID)
		rows, err := tx.Query(ctx, `select distinct on (pid) pid, source_key from (
				select coalesce(merged_into_target_id, target_id)::text as pid, source_key from migration.xref
				where tenant_id = $1 and source_system = $2 and source_entity = $3
				  and ((merged_into_target_id is null and target_id = any($4::uuid[])) or merged_into_target_id = any($4::uuid[]))
				  and (not $5::boolean or legal_entity_id is null or legal_entity_id::text = $6)
			) k order by pid, source_key`, p.TenantID, sourceSystem, entity, uuids, p.LegalEntityID != "", le)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pid, key string
			if err := rows.Scan(&pid, &key); err != nil {
				return err
			}
			for _, orig := range canon[pid] {
				out[orig] = key
			}
		}
		return rows.Err()
	})
	return out, err
}

func refMismatch(i int, entity string) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeXrefRefMismatch, "refs["+strconv.Itoa(i)+"].entity "+entity+" is not the subject entity of the header or of a collection of any version of this outbound mapping")
}

// effectiveRefs is what a record call stores: canonical ids, each ref with the key it records (the top-level legacyKey
// for the one that has none), a ref repeated with the same key once (a set, so 4100 refs stay linear).
func effectiveRefs(in OutboundXrefRecord) []domain.OutboundRef {
	out := make([]domain.OutboundRef, 0, len(in.Refs))
	seen := make(map[domain.OutboundRef]bool, len(in.Refs))
	for _, r := range in.Refs {
		id, _ := ids.Canonical(r.ID)
		key := r.LegacyKey
		if key == "" {
			key = in.LegacyKey
		}
		n := domain.OutboundRef{Entity: r.Entity, ID: id, LegacyKey: key}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// eventKey is the event's legacy_key: the header's key, which is the top-level legacyKey when the call gives one (the
// ref without its own key, wherever it stands in refs), else the first ref's.
func eventKey(in OutboundXrefRecord, eff []domain.OutboundRef) string {
	if strings.TrimSpace(in.LegacyKey) != "" || len(eff) == 0 {
		return in.LegacyKey
	}
	return eff[0].LegacyKey
}

// recordedEntities are the entities a mapping's versions create records of: the header subject and the subject of each
// collection. The hub's grant reaches only the cross-reference rows its own mappings produce.
func recordedEntities(definition []byte) []string {
	var d struct {
		Subject     *domain.OutboundSubject `json:"subject"`
		Collections []struct {
			Subject *domain.OutboundSubject `json:"subject"`
		} `json:"collections"`
	}
	if json.Unmarshal(definition, &d) != nil {
		return nil
	}
	var out []string
	if d.Subject != nil {
		out = append(out, d.Subject.Entity)
	}
	for _, c := range d.Collections {
		if c.Subject != nil {
			out = append(out, c.Subject.Entity)
		}
	}
	return out
}

func xrefLess(a, b domain.OutboundRef) int {
	if c := strings.Compare(a.Entity, b.Entity); c != 0 {
		return c
	}
	return strings.Compare(a.LegacyKey, b.LegacyKey)
}

// RecordOutboundXref writes (mapping's source system, ref.entity, legacyKey) -> ref.id. The ref must name the subject
// entity of some version of the mapping, so the hub's grant reaches only the cross-reference rows its own mappings
// produce and never an inbound entity such as customer. An existing row is never repointed: one that already
// resolves to the record (directly or as a record merged into it) is accepted, one that resolves elsewhere is 409.
// Concurrent calls serialise on the primary keys of outbound_xref_event and xref: the second waits for the first and
// then sees its row.
func (s *Postgres) RecordOutboundXref(ctx context.Context, in OutboundXrefRecord) (bool, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(in.MappingID) {
		return false, outboundNotFound()
	}
	if bad := ValidateOutboundRecord(in); len(bad) > 0 {
		return false, errors.New("store: invalid xref record")
	}
	canonical := effectiveRefs(in)
	refsJSON, err := json.Marshal(canonical)
	if err != nil {
		return false, err
	}
	key := eventKey(in, canonical)
	replayed := false
	err = s.outboundTx(ctx, obRecord, func(tx pgx.Tx) error {
		var system string
		if err := tx.QueryRow(ctx, `select source_system from migration.outbound_mapping where tenant_id = $1 and id = $2`, p.TenantID, in.MappingID).Scan(&system); errors.Is(err, pgx.ErrNoRows) {
			return outboundNotFound()
		} else if err != nil {
			return err
		}
		if len(canonical) > 0 {
			rows, err := tx.Query(ctx, `select definition::text from migration.outbound_mapping_version where tenant_id = $1 and mapping_id = $2`, p.TenantID, in.MappingID)
			if err != nil {
				return err
			}
			allowed := map[string]bool{}
			for rows.Next() {
				var def string
				if err := rows.Scan(&def); err != nil {
					rows.Close()
					return err
				}
				for _, e := range recordedEntities([]byte(def)) {
					allowed[e] = true
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for i, r := range canonical {
				if !allowed[r.Entity] {
					return refMismatch(i, r.Entity)
				}
			}
		}
		tag, err := tx.Exec(ctx, `insert into migration.outbound_xref_event (tenant_id, mapping_id, event_id, legacy_key, refs, actor)
			values ($1, $2, $3, $4, $5, $6) on conflict (tenant_id, mapping_id, event_id) do nothing`,
			p.TenantID, in.MappingID, in.EventID, key, refsJSON, in.Actor)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var storedKey string
			var stored []byte
			if err := tx.QueryRow(ctx, `select legacy_key, refs from migration.outbound_xref_event where tenant_id = $1 and mapping_id = $2 and event_id = $3`,
				p.TenantID, in.MappingID, in.EventID).Scan(&storedKey, &stored); err != nil {
				return err
			}
			var was []domain.OutboundRef
			if err := json.Unmarshal(stored, &was); err != nil || storedKey != key {
				return xrefEventConflict(in.EventID)
			}
			for i := range was {
				if was[i].LegacyKey == "" {
					was[i].LegacyKey = storedKey // recorded before a ref carried its own key
				}
			}
			if !sameRefs(was, canonical) {
				return xrefEventConflict(in.EventID)
			}
			replayed = true
			return nil
		}
		if len(canonical) == 0 {
			return nil // a mapping without a subject: the push is recorded, there is no record to tie the key to
		}
		// A row this call creates for a subject the owning service scopes to a legal entity is tagged with the token's
		// (the event's) legal entity; a tenant-wide one stays untagged. An existing row is never changed, its legal
		// entity included: the token cannot prove that an untagged row (loaded before 0013 and not backfilled) belongs
		// to its legal entity, so tagging it would let one legal entity's token take a record away from another. Such a
		// row is accepted as before and stays tenant-wide; one tagged with another legal entity is 409. The no-op update
		// only locks an existing row so RETURNING reports whom it resolves to and its legal entity. Every ref is written
		// in the same transaction, in key order so that two pushes that share keys lock them in the same order; one
		// conflict undoes the whole call.
		writes := slices.Clone(canonical)
		slices.SortFunc(writes, xrefLess)
		leOf := func(entity string) string {
			if domain.XrefLegalEntityScoped(entity) {
				le, _ := ids.Canonical(p.LegalEntityID)
				return le
			}
			return ""
		}
		batch := &pgx.Batch{}
		for _, ref := range writes {
			batch.Queue(`insert into migration.xref (tenant_id, source_system, source_entity, source_key, target_id, legal_entity_id)
				values ($1, $2, $3, $4, $5, nullif($6, '')::uuid)
				on conflict (tenant_id, source_system, source_entity, source_key) do update set updated_at = migration.xref.updated_at
				returning coalesce(merged_into_target_id, target_id)::text, coalesce(legal_entity_id::text, '')`,
				p.TenantID, system, ref.Entity, ref.LegacyKey, ref.ID, leOf(ref.Entity))
		}
		results := tx.SendBatch(ctx, batch)
		defer results.Close()
		for _, ref := range writes {
			var current, tagged string
			if err := results.QueryRow().Scan(&current, &tagged); err != nil {
				return err
			}
			if le := leOf(ref.Entity); current != ref.ID || (le != "" && tagged != "" && tagged != le) {
				return xrefConflict(ref.LegacyKey)
			}
		}
		return results.Close()
	})
	return replayed, err
}

// Mem is the in-memory store of the API tests.

type memOutbound struct {
	mu       sync.Mutex
	items    []OutboundMapping
	versions map[string][]memOutboundVersion
	byKey    map[string]string
	events   map[[2]string]OutboundXrefRecord
}

type memOutboundVersion struct {
	OutboundVersion
	Definition json.RawMessage
}

func (m *Mem) ob() *memOutbound {
	m.obOnce.Do(func() {
		m.obState = &memOutbound{versions: map[string][]memOutboundVersion{}, byKey: map[string]string{}, events: map[[2]string]OutboundXrefRecord{}}
	})
	return m.obState
}

func (st *memOutbound) find(id string) (int, bool) {
	return slices.IndexFunc(st.items, func(x OutboundMapping) bool { return x.ID == id }), slices.ContainsFunc(st.items, func(x OutboundMapping) bool { return x.ID == id })
}

func (m *Mem) SaveOutboundMapping(_ context.Context, in OutboundMapping, idempotencyKey string) (OutboundMapping, bool, error) {
	if m.SaveErr != nil {
		return OutboundMapping{}, false, m.SaveErr
	}
	st := m.ob()
	st.mu.Lock()
	defer st.mu.Unlock()
	in.Checksum = OutboundChecksum(in.Definition)
	if id, ok := st.byKey[idempotencyKey]; ok && idempotencyKey != "" {
		i, _ := st.find(id)
		prior := st.items[i]
		if prior.Name != in.Name || prior.SourceSystem != in.SourceSystem || st.versions[id][0].Checksum != in.Checksum {
			return OutboundMapping{}, false, keyReuse()
		}
		return prior, false, nil
	}
	for _, x := range st.items {
		if x.Name == in.Name {
			return OutboundMapping{}, false, outboundNameUsed()
		}
	}
	now := time.Now().UTC()
	in.ID, in.Version, in.Status, in.CreatedAt, in.UpdatedAt = ids.New(), 1, OutboundActive, now, now
	st.items = append(st.items, in)
	st.versions[in.ID] = []memOutboundVersion{{OutboundVersion{1, in.Checksum, in.CreatedBy, now}, in.Definition}}
	if idempotencyKey != "" {
		st.byKey[idempotencyKey] = in.ID
	}
	return in, true, nil
}

func (m *Mem) GetOutboundMapping(_ context.Context, id string, version int) (OutboundMapping, error) {
	st := m.ob()
	st.mu.Lock()
	defer st.mu.Unlock()
	i, ok := st.find(id)
	if !ok {
		return OutboundMapping{}, outboundNotFound()
	}
	out := st.items[i]
	if version == 0 {
		version = out.Version
	}
	if version < 1 || version > len(st.versions[id]) {
		return OutboundMapping{}, outboundVersionGone(version)
	}
	v := st.versions[id][version-1]
	out.Version, out.Definition, out.Checksum = version, slices.Clone(v.Definition), v.Checksum
	return out, nil
}

func (m *Mem) ListOutboundMappings(_ context.Context, f OutboundFilter) ([]OutboundMapping, error) {
	st := m.ob()
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []OutboundMapping
	for _, x := range st.items {
		if f.Status != "" && x.Status != f.Status {
			continue
		}
		if f.After != nil && (x.Name < f.After.Name || (x.Name == f.After.Name && x.ID <= f.After.ID)) {
			continue
		}
		x.Definition, x.Checksum = nil, ""
		out = append(out, x)
	}
	slices.SortFunc(out, func(a, b OutboundMapping) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out[:min(len(out), clampLimit(f.Limit))], nil
}

func (m *Mem) ListOutboundVersions(_ context.Context, id string) ([]OutboundVersion, error) {
	st := m.ob()
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.find(id); !ok {
		return nil, outboundNotFound()
	}
	out := []OutboundVersion{}
	for _, v := range slices.Backward(st.versions[id]) {
		out = append(out, v.OutboundVersion)
	}
	return out, nil
}

func (m *Mem) UpdateOutboundMapping(_ context.Context, id string, version int, edit func(*OutboundMapping) error) (OutboundMapping, error) {
	st := m.ob()
	st.mu.Lock()
	defer st.mu.Unlock()
	i, ok := st.find(id)
	if !ok {
		return OutboundMapping{}, outboundNotFound()
	}
	cur := st.items[i]
	if cur.Version != version {
		return OutboundMapping{}, versionConflict(cur.Version)
	}
	cur.Definition = slices.Clone(st.versions[id][cur.Version-1].Definition)
	if err := edit(&cur); err != nil {
		return OutboundMapping{}, err
	}
	for _, x := range st.items {
		if x.ID != id && x.Name == cur.Name {
			return OutboundMapping{}, outboundNameUsed()
		}
	}
	now := time.Now().UTC()
	cur.Version++
	cur.Checksum, cur.UpdatedAt = OutboundChecksum(cur.Definition), now
	st.versions[id] = append(st.versions[id], memOutboundVersion{OutboundVersion{cur.Version, cur.Checksum, cur.CreatedBy, now}, cur.Definition})
	header := cur
	header.Definition, header.Checksum = nil, ""
	st.items[i] = header
	return cur, nil
}

func (m *Mem) LegacyKeys(ctx context.Context, sourceSystem, entity string, passageIDs []string) (map[string]string, error) {
	p, _ := tenant.FromContext(ctx)
	le, _ := ids.Canonical(p.LegalEntityID)
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	best := map[string]string{}
	for k, e := range m.xref.Entries {
		if k.SourceSystem != sourceSystem || k.Entity != entity {
			continue
		}
		if p.LegalEntityID != "" && e.LegalEntityID != "" && e.LegalEntityID != le {
			continue
		}
		if cur, ok := best[e.TargetID]; !ok || k.SourceKey < cur {
			best[e.TargetID] = k.SourceKey
		}
	}
	for _, id := range passageIDs {
		if c, ok := ids.Canonical(id); ok {
			if key, found := best[c]; found {
				out[id] = key
			}
		}
	}
	return out, nil
}

func (m *Mem) RecordOutboundXref(ctx context.Context, in OutboundXrefRecord) (bool, error) {
	p, _ := tenant.FromContext(ctx)
	st := m.ob()
	st.mu.Lock()
	defer st.mu.Unlock()
	i, ok := st.find(in.MappingID)
	if !ok {
		return false, outboundNotFound()
	}
	eff := effectiveRefs(in)
	allowed := map[string]bool{}
	for _, v := range st.versions[in.MappingID] {
		for _, e := range recordedEntities(v.Definition) {
			allowed[e] = true
		}
	}
	for j, r := range eff {
		if !allowed[r.Entity] {
			return false, refMismatch(j, r.Entity)
		}
	}
	in.Refs = eff
	k := [2]string{in.MappingID, in.EventID}
	if was, seen := st.events[k]; seen {
		if was.LegacyKey != eventKey(in, eff) || !sameRefs(was.Refs, eff) {
			return false, xrefEventConflict(in.EventID)
		}
		return true, nil
	}
	if len(eff) > 0 {
		m.mu.Lock()
		defer m.mu.Unlock()
		writes := slices.Clone(eff)
		slices.SortFunc(writes, xrefLess)
		type put struct {
			key   domain.XrefKey
			entry domain.XrefEntry
		}
		var puts []put // applied only when no ref conflicts, so a refused call leaves nothing behind
		pending := map[domain.XrefKey]int{}
		for _, ref := range writes {
			key := domain.XrefKey{SourceSystem: st.items[i].SourceSystem, Entity: ref.Entity, SourceKey: ref.LegacyKey}
			le := ""
			if domain.XrefLegalEntityScoped(ref.Entity) {
				le, _ = ids.Canonical(p.LegalEntityID)
			}
			if cur, exists := m.xref.Entries[key]; exists {
				if cur.TargetID != ref.ID || (le != "" && cur.LegalEntityID != "" && cur.LegalEntityID != le) {
					return false, xrefConflict(ref.LegacyKey)
				}
			} else if j, dup := pending[key]; dup {
				if puts[j].entry.TargetID != ref.ID {
					return false, xrefConflict(ref.LegacyKey)
				}
			} else {
				pending[key] = len(puts)
				puts = append(puts, put{key, domain.XrefEntry{TargetID: ref.ID, LegalEntityID: le}})
			}
		}
		for _, x := range puts {
			m.xref.Entries[x.key] = x.entry
		}
	}
	in.LegacyKey = eventKey(in, eff)
	st.events[k] = in
	return false, nil
}
