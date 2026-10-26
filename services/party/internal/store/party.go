package store

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	partyevents "github.com/BenjaminSRussell/passage/services/party/internal/events"
)

const (
	CodeInvalid          = "PTY-PARTY-INVALID"
	CodeVersionConflict  = "PTY-VERSION-CONFLICT"
	CodeStatusTransition = "PTY-STATUS-TRANSITION"
	CodeIdempotencyReuse = "idempotency_key_reuse"
	maxMergeHops         = 5
	uniqueViolation      = "23505"
	partyColumns         = `id::text, party_number, party_type, name, search_name, status, merged_into_id::text, org_legal_name, org_trade_name, org_registration_number, org_industry_code, person_given_name, person_middle_name, person_family_name, person_prefix, person_suffix, to_char(person_dob, 'YYYY-MM-DD'), person_gender, language_code, time_zone, owner_user_id::text, owner_team_id::text, version, created_at, updated_at`
	insertPartySQL       = `insert into party (tenant_id, id, party_number, party_type, name, search_name, org_legal_name, org_trade_name, org_registration_number, org_industry_code, person_given_name, person_middle_name, person_family_name, person_prefix, person_suffix, person_dob, person_gender, language_code, time_zone, owner_user_id, owner_team_id, idempotency_key)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16::text::date, $17, $18, $19, $20, $21, $22) returning ` + partyColumns
	updatePartySQL = `update party set status = $3, name = $4, search_name = $5, org_legal_name = $6, org_trade_name = $7, org_registration_number = $8, org_industry_code = $9,
		person_given_name = $10, person_middle_name = $11, person_family_name = $12, person_prefix = $13, person_suffix = $14, person_dob = $15::text::date, person_gender = $16,
		language_code = $17, time_zone = $18, owner_user_id = $19, owner_team_id = $20, version = version + 1, updated_at = now()
		where tenant_id = $1 and id = $2 returning ` + partyColumns
)

type NewParty struct {
	PartyType string
	Fields    domain.PartyFields
	Key       string
	Hash      string
}

type PartyPatch struct {
	PartyType *string
	Status    *string
	Fields    domain.PartyFields
}

type Store interface {
	CreateParty(ctx context.Context, in NewParty, next func(context.Context) (string, error)) (domain.Party, bool, error)
	GetParty(ctx context.Context, id string) (domain.Party, bool, error)
	UpdateParty(ctx context.Context, id string, version int, patch PartyPatch) (domain.Party, error)
}

func scanParty(row pgx.Row) (domain.Party, error) {
	var p domain.Party
	err := row.Scan(&p.ID, &p.PartyNumber, &p.PartyType, &p.Name, &p.SearchName, &p.Status, &p.MergedIntoID, &p.LegalName, &p.TradeName, &p.RegistrationNumber, &p.IndustryCode,
		&p.GivenName, &p.MiddleName, &p.FamilyName, &p.Prefix, &p.Suffix, &p.DOB, &p.Gender, &p.LanguageCode, &p.TimeZone, &p.OwnerUserID, &p.OwnerTeamID, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func attributes(p domain.Party) []any {
	return []any{p.Name, p.SearchName, p.LegalName, p.TradeName, p.RegistrationNumber, p.IndustryCode, p.GivenName, p.MiddleName, p.FamilyName, p.Prefix, p.Suffix, p.DOB, p.Gender, p.LanguageCode, p.TimeZone, p.OwnerUserID, p.OwnerTeamID}
}

func enqueue(ctx context.Context, tx pgx.Tx, tenantID, partyID, eventType string, data any) error {
	evt, err := events.New(eventType, domain.EventSource, partyID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, domain.AggregateParty, partyID, evt)
}

func invalid(errs map[string]string) error {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeInvalid, "party failed validation")
	p.Errors = errs
	return p
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

func (s *Postgres) CreateParty(ctx context.Context, in NewParty, next func(context.Context) (string, error)) (domain.Party, bool, error) {
	for attempt := 0; ; attempt++ {
		p, created, err := s.createOnce(ctx, in, next)
		if err != nil && in.Key != "" && attempt == 0 && isUniqueViolation(err) {
			continue
		}
		return p, created, err
	}
}

func (s *Postgres) createOnce(ctx context.Context, in NewParty, next func(context.Context) (string, error)) (domain.Party, bool, error) {
	caller, _ := tenant.FromContext(ctx)
	var party domain.Party
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var key *string
		if in.Key != "" {
			key = &in.Key
			var storedHash string
			err := tx.QueryRow(ctx, `select request_hash from idempotency_keys where tenant_id = $1 and key = $2`, caller.TenantID, in.Key).Scan(&storedHash)
			if err == nil {
				if storedHash != in.Hash {
					return httpx.NewProblem(http.StatusConflict, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
				}
				party, err = scanParty(tx.QueryRow(ctx, `select `+partyColumns+` from party where tenant_id = $1 and idempotency_key = $2`, caller.TenantID, in.Key))
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		number, err := next(ctx)
		if err != nil {
			return err
		}
		draft := domain.Party{Name: domain.DisplayName(in.PartyType, in.Fields), PartyFields: in.Fields}
		draft.SearchName = domain.Normalize(draft.Name)
		args := append([]any{caller.TenantID, ids.New(), number, in.PartyType}, attributes(draft)...)
		party, err = scanParty(tx.QueryRow(ctx, insertPartySQL, append(args, key)...))
		if err != nil {
			return err
		}
		if key != nil {
			if _, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 201)`, caller.TenantID, in.Key, in.Hash); err != nil {
				return err
			}
		}
		if err := enqueue(ctx, tx, caller.TenantID, party.ID, partyevents.PartyCreated, partyevents.PartyCreatedData{
			PartyID: party.ID, PartyNumber: party.PartyNumber, PartyType: party.PartyType, Name: party.Name,
		}); err != nil {
			return err
		}
		created = true
		if s.beforeCommit != nil {
			return s.beforeCommit()
		}
		return nil
	})
	return party, created && err == nil, err
}

func (s *Postgres) GetParty(ctx context.Context, requestedID string) (domain.Party, bool, error) {
	caller, _ := tenant.FromContext(ctx)
	id, err := canonicalID(requestedID)
	if err != nil {
		return domain.Party{}, false, err
	}
	var party domain.Party
	redirected := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for hop := 0; hop <= maxMergeHops; hop++ {
			var err error
			party, err = scanParty(tx.QueryRow(ctx, `select `+partyColumns+` from party where tenant_id = $1 and id = $2`, caller.TenantID, id))
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if party.Status != domain.StatusMerged || party.MergedIntoID == nil {
				return nil
			}
			id, redirected = *party.MergedIntoID, true
		}
		return ErrNotFound
	})
	return party, redirected, err
}

func (s *Postgres) UpdateParty(ctx context.Context, requestedID string, version int, patch PartyPatch) (domain.Party, error) {
	caller, _ := tenant.FromContext(ctx)
	id, err := canonicalID(requestedID)
	if err != nil {
		return domain.Party{}, err
	}
	var party domain.Party
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanParty(tx.QueryRow(ctx, `select `+partyColumns+` from party where tenant_id = $1 and id = $2 for update`, caller.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		party = cur
		if cur.Status == domain.StatusMerged {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeStatusTransition, "a merged party cannot be changed")
		}
		if patch.PartyType != nil && *patch.PartyType != cur.PartyType {
			return invalid(map[string]string{"partyType": "cannot be changed"})
		}
		if patch.Status != nil && *patch.Status != cur.Status && !domain.CheckTransition(cur.Status, *patch.Status) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeStatusTransition, "status can only move between active and inactive")
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, "party was changed by another request")
		}
		if errs := domain.ValidateParty(cur.PartyType, patch.Fields, false); len(errs) > 0 {
			return invalid(errs)
		}
		changed := domain.ApplyPatch(&cur, patch.Fields)
		if errs := domain.ValidateParty(cur.PartyType, cur.PartyFields, true); len(errs) > 0 {
			return invalid(errs)
		}
		if patch.Status != nil && *patch.Status != cur.Status {
			cur.Status = *patch.Status
			changed = append(changed, "status")
		}
		if len(changed) == 0 {
			return nil
		}
		cur.Name = domain.DisplayName(cur.PartyType, cur.PartyFields)
		cur.SearchName = domain.Normalize(cur.Name)
		args := append([]any{caller.TenantID, id, cur.Status}, attributes(cur)...)
		if party, err = scanParty(tx.QueryRow(ctx, updatePartySQL, args...)); err != nil {
			return err
		}
		sort.Strings(changed)
		if err := enqueue(ctx, tx, caller.TenantID, party.ID, partyevents.PartyUpdated, partyevents.PartyUpdatedData{
			PartyID: party.ID, ChangedFields: domain.PublicFields(changed), Version: party.Version,
		}); err != nil {
			return err
		}
		if s.beforeCommit != nil {
			return s.beforeCommit()
		}
		return nil
	})
	return party, err
}
