package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

const (
	CodeSetInvalid          = "LEDGER_SET_INVALID"
	CodeSetNotFound         = "LEDGER_SET_NOT_FOUND"
	CodeSetCodeDuplicate    = "LEDGER_SET_CODE_DUPLICATE"
	CodeMappingDuplicate    = "LEDGER_MAPPING_DUPLICATE"
	CodeSetEntityMismatch   = "LEDGER_SET_LEGAL_ENTITY_MISMATCH"
	CodeSetLeadingConflict  = "LEDGER_SET_LEADING_CONFLICT"
	CodeMappingOnLeading    = "LEDGER_MAPPING_ON_LEADING_LEDGER"
	CodeSetCurrencyMismatch = "LEDGER_SET_CURRENCY_MISMATCH"
	CodeLedgerLinesNotInSet = "LEDGER_LINES_LEDGER_NOT_IN_SET"
)

type LedgerSet struct {
	ID              string    `json:"id"`
	LegalEntityID   string    `json:"legalEntityId"`
	Code            string    `json:"code"`
	Name            string    `json:"name"`
	LeadingLedgerID string    `json:"leadingLedgerId"`
	LedgerIDs       []string  `json:"ledgerIds"`
	CreatedAt       time.Time `json:"createdAt"`
}

type LedgerSetInput struct {
	Code              string
	Name              string
	LeadingLedgerID   string
	ParallelLedgerIDs []string
}

type AccountMapping struct {
	ID            string `json:"id"`
	LedgerID      string `json:"ledgerId"`
	SourceAccount string `json:"sourceAccount"`
	TargetAccount string `json:"targetAccount"`
	EffectiveFrom string `json:"effectiveFrom"`
}

func setViolation(code, field, message string) error {
	return &domain.RejectedError{Violations: []domain.Violation{{Code: code, Field: field, Message: message}}}
}

func (s *Postgres) CreateLedgerSet(ctx context.Context, in LedgerSetInput) (LedgerSet, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return LedgerSet{}, err
	}
	var out LedgerSet
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		if in.LeadingLedgerID, ok = ids.Canonical(in.LeadingLedgerID); !ok {
			return setViolation(CodeSetInvalid, "leadingLedgerId", "must be a UUID")
		}
		parallel := make([]string, len(in.ParallelLedgerIDs))
		for i, raw := range in.ParallelLedgerIDs {
			if parallel[i], ok = ids.Canonical(raw); !ok {
				return setViolation(CodeSetInvalid, "parallelLedgerIds", "each entry must be a UUID")
			}
		}
		in.ParallelLedgerIDs = parallel
		members := append([]string{in.LeadingLedgerID}, in.ParallelLedgerIDs...)
		if len(in.ParallelLedgerIDs) == 0 || slices.Contains(in.ParallelLedgerIDs, in.LeadingLedgerID) {
			return setViolation(CodeSetInvalid, "parallelLedgerIds", "a set needs at least one parallel ledger besides the leading ledger")
		}
		sorted := slices.Clone(members)
		slices.Sort(sorted)
		if len(slices.Compact(sorted)) != len(members) {
			return setViolation(CodeSetInvalid, "parallelLedgerIds", "a ledger can appear in a set only once")
		}
		loaded := make([]Ledger, 0, len(sorted))
		for _, id := range sorted {
			l, err := loadLedger(ctx, tx, p.TenantID, id, false)
			if err != nil {
				return err
			}
			loaded = append(loaded, l)
		}
		for _, l := range loaded {
			if l.LegalEntityID != loaded[0].LegalEntityID {
				return setViolation(CodeSetEntityMismatch, "parallelLedgerIds", "every ledger of a set belongs to one legal entity")
			}
			if l.Currency != loaded[0].Currency {
				return setViolation(CodeSetCurrencyMismatch, "parallelLedgerIds", "every ledger of a set has the same accounting currency")
			}
		}
		if err := lockLegalEntity(ctx, tx, p.TenantID, loaded[0].LegalEntityID); err != nil {
			return err
		}
		for _, id := range sorted {
			if _, err := loadLedger(ctx, tx, p.TenantID, id, true); err != nil {
				return err
			}
		}
		var hasMappings bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from ledger_account_mapping where tenant_id = $1 and ledger_id = $2)`,
			p.TenantID, in.LeadingLedgerID).Scan(&hasMappings); err != nil {
			return err
		}
		if hasMappings {
			return setViolation(CodeMappingOnLeading, "leadingLedgerId", "the leading ledger has account mappings, which are never applied to a leading ledger")
		}
		var conflicting int
		if err := tx.QueryRow(ctx, `select count(*) from ledger_set where tenant_id = $1 and legal_entity_id = $2 and leading_ledger_id <> $3`,
			p.TenantID, loaded[0].LegalEntityID, in.LeadingLedgerID).Scan(&conflicting); err != nil {
			return err
		}
		if conflicting > 0 {
			return setViolation(CodeSetLeadingConflict, "leadingLedgerId", "a legal entity has exactly one leading ledger; its sets must all use it")
		}
		out = LedgerSet{ID: ids.New(), LegalEntityID: loaded[0].LegalEntityID, Code: in.Code, Name: in.Name,
			LeadingLedgerID: in.LeadingLedgerID, LedgerIDs: sorted}
		if err := tx.QueryRow(ctx, `insert into ledger_set (tenant_id, id, legal_entity_id, code, name, leading_ledger_id)
			values ($1, $2, $3, $4, $5, $6) returning created_at`,
			p.TenantID, out.ID, out.LegalEntityID, out.Code, out.Name, out.LeadingLedgerID).Scan(&out.CreatedAt); err != nil {
			return err
		}
		for _, id := range sorted {
			if _, err := tx.Exec(ctx, `insert into ledger_set_member (tenant_id, ledger_set_id, ledger_id, legal_entity_id) values ($1, $2, $3, $4)`,
				p.TenantID, out.ID, id, out.LegalEntityID); err != nil {
				return err
			}
		}
		return nil
	})
	if isUnique(err) {
		return LedgerSet{}, httpx.NewProblem(http.StatusConflict, CodeSetCodeDuplicate, "a ledger set with this code already exists")
	}
	return out, err
}

func lockLegalEntity(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, tenantID+"/"+legalEntityID)
	return err
}

func (s *Postgres) GetLedgerSet(ctx context.Context, id string) (LedgerSet, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return LedgerSet{}, err
	}
	var out LedgerSet
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = loadLedgerSet(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

func loadLedgerSet(ctx context.Context, tx pgx.Tx, tenantID, id string) (LedgerSet, error) {
	var set LedgerSet
	err := tx.QueryRow(ctx, `select s.id::text, s.legal_entity_id::text, s.code, s.name, s.leading_ledger_id::text, s.created_at,
			array(select m.ledger_id::text from ledger_set_member m where m.tenant_id = s.tenant_id and m.ledger_set_id = s.id order by m.ledger_id)
		from ledger_set s where s.tenant_id = $1 and s.id = $2`, tenantID, id).
		Scan(&set.ID, &set.LegalEntityID, &set.Code, &set.Name, &set.LeadingLedgerID, &set.CreatedAt, &set.LedgerIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return set, httpx.NewProblem(http.StatusNotFound, CodeSetNotFound, "ledger set not found")
	}
	return set, err
}

const setListPageSize = 100

func (s *Postgres) ListLedgerSets(ctx context.Context, after string) ([]LedgerSet, string, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, "", err
	}
	out := []LedgerSet{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select s.id::text, s.legal_entity_id::text, s.code, s.name, s.leading_ledger_id::text, s.created_at,
				array(select m.ledger_id::text from ledger_set_member m where m.tenant_id = s.tenant_id and m.ledger_set_id = s.id order by m.ledger_id)
			from ledger_set s where s.tenant_id = $1
				and ($2 = '' or (s.legal_entity_id, s.code) > (select a.legal_entity_id, a.code from ledger_set a where a.tenant_id = $1 and a.id = $2::uuid))
			order by s.legal_entity_id, s.code limit $3`, p.TenantID, after, setListPageSize+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var set LedgerSet
			if err := rows.Scan(&set.ID, &set.LegalEntityID, &set.Code, &set.Name, &set.LeadingLedgerID, &set.CreatedAt, &set.LedgerIDs); err != nil {
				return err
			}
			out = append(out, set)
		}
		return rows.Err()
	})
	next := ""
	if len(out) > setListPageSize {
		out = out[:setListPageSize]
		next = out[setListPageSize-1].ID
	}
	return out, next, err
}

func (s *Postgres) ListAccountMappings(ctx context.Context, ledgerID, after string) ([]AccountMapping, string, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, "", err
	}
	out := []AccountMapping{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, ledger_id::text, source_account, target_account, effective_from::text
			from ledger_account_mapping where tenant_id = $1 and ledger_id = $2
				and ($3 = '' or (source_account, effective_from) > (select a.source_account, a.effective_from from ledger_account_mapping a where a.tenant_id = $1 and a.ledger_id = $2 and a.id = $3::uuid))
			order by source_account, effective_from limit $4`, p.TenantID, ledgerID, after, setListPageSize+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m AccountMapping
			if err := rows.Scan(&m.ID, &m.LedgerID, &m.SourceAccount, &m.TargetAccount, &m.EffectiveFrom); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	next := ""
	if len(out) > setListPageSize {
		out = out[:setListPageSize]
		next = out[setListPageSize-1].ID
	}
	return out, next, err
}

func (s *Postgres) CreateAccountMapping(ctx context.Context, in AccountMapping) (AccountMapping, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return AccountMapping{}, err
	}
	in.ID = ids.New()
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, in.LedgerID, false)
		if err != nil {
			return err
		}
		if err := lockLegalEntity(ctx, tx, p.TenantID, ledger.LegalEntityID); err != nil {
			return err
		}
		if ledger, err = loadLedger(ctx, tx, p.TenantID, in.LedgerID, true); err != nil {
			return err
		}
		var isLeading bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from ledger_set where tenant_id = $1 and leading_ledger_id = $2)`, p.TenantID, in.LedgerID).Scan(&isLeading); err != nil {
			return err
		}
		if isLeading {
			return setViolation(CodeMappingOnLeading, "ledgerId", "account mappings are not applied to a set's leading ledger")
		}
		targets, err := loadAccounts(ctx, tx, p.TenantID, ledger.ChartCode, []string{in.TargetAccount})
		if err != nil {
			return err
		}
		target, found := targets[in.TargetAccount]
		if !found {
			return setViolation("LEDGER_ACCOUNT_NOT_FOUND", "targetAccount", "account "+in.TargetAccount+" does not exist in chart "+ledger.ChartCode)
		}
		if !target.Postable {
			return setViolation("LEDGER_ACCOUNT_NOT_POSTABLE", "targetAccount", "account "+in.TargetAccount+" is not postable")
		}
		_, err = tx.Exec(ctx, `insert into ledger_account_mapping (tenant_id, id, legal_entity_id, ledger_id, source_account, target_account, effective_from)
			values ($1, $2, $3, $4, $5, $6, $7::text::date)`,
			p.TenantID, in.ID, ledger.LegalEntityID, in.LedgerID, in.SourceAccount, in.TargetAccount, in.EffectiveFrom)
		return err
	})
	if isUnique(err) {
		return AccountMapping{}, httpx.NewProblem(http.StatusConflict, CodeMappingDuplicate, "this account already has a mapping on this ledger from that date")
	}
	return in, err
}

func loadMappings(ctx context.Context, tx pgx.Tx, tenantID, ledgerID string, postingDate time.Time) ([]domain.AccountMapping, error) {
	rows, err := tx.Query(ctx, `select source_account, target_account, effective_from from ledger_account_mapping
		where tenant_id = $1 and ledger_id = $2 and effective_from <= $3::text::date`, tenantID, ledgerID, postingDate.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AccountMapping
	for rows.Next() {
		var m domain.AccountMapping
		if err := rows.Scan(&m.Source, &m.Target, &m.EffectiveFrom); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Postgres) PostSetEntry(ctx context.Context, setID string, in PostInput) (EntryResult, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return EntryResult{}, err
	}
	var out EntryResult
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = postSetTx(ctx, tx, p.TenantID, setID, in)
		return err
	})
	return out, idempotencyRace(err)
}

func postSetTx(ctx context.Context, tx pgx.Tx, tenantID, setID string, in PostInput) (EntryResult, error) {
	var out EntryResult
	set, err := loadLedgerSet(ctx, tx, tenantID, setID)
	if err != nil {
		return out, err
	}
	ledgers := make([]Ledger, 0, len(set.LedgerIDs))
	for _, id := range set.LedgerIDs {
		l, err := loadLedger(ctx, tx, tenantID, id, true)
		if err != nil {
			return out, err
		}
		ledgers = append(ledgers, l)
	}
	ledgerLines, err := checkLedgerLines(set, in)
	if err != nil {
		return out, err
	}
	replayed, err := replayIdempotent(ctx, tx, tenantID, set.LegalEntityID, in.Key, in.Hash, &out)
	if err != nil || replayed {
		return out, err
	}
	out = EntryResult{Idempotency: Idempotency{Key: in.Key}, TraceID: in.TraceID}
	for _, ledger := range ledgers {
		li := in
		li.LedgerID = ledger.ID
		if ledger.ID != set.LeadingLedgerID {
			mappings, err := loadMappings(ctx, tx, tenantID, ledger.ID, in.PostingDate)
			if err != nil {
				return EntryResult{}, err
			}
			li.Lines = domain.ApplyMappings(in.Lines, mappings, in.PostingDate)
		}
		li.Lines = append(slices.Clone(li.Lines), ledgerLines[ledger.ID]...)
		li.Key = setEntryKey(in.Key, ledger.ID)
		entry, notices, err := postToLedger(ctx, tx, tenantID, ledger, li)
		var rej *domain.RejectedError
		if errors.As(err, &rej) {
			for i := range rej.Violations {
				rej.Violations[i].Message = "ledger " + ledger.Code + ": " + rej.Violations[i].Message
			}
		}
		if err != nil {
			return EntryResult{}, err
		}
		out.Entries = append(out.Entries, entry)
		for _, n := range notices {
			n.Message = "ledger " + ledger.Code + ": " + n.Message
			out.Notices = append(out.Notices, n)
		}
	}
	return out, storeIdempotent(ctx, tx, tenantID, set.LegalEntityID, in.Key, in.Hash, out)
}

// checkLedgerLines canonicalises the per-ledger line groups, refuses a ledger outside the set
// (422) and a non-leading ledger without the ledger-specific duty (403, FB50L).
func checkLedgerLines(set LedgerSet, in PostInput) (map[string][]domain.LineInput, error) {
	out := make(map[string][]domain.LineInput, len(in.LedgerLines))
	needsDuty := false
	for _, raw := range slices.Sorted(maps.Keys(in.LedgerLines)) {
		id, ok := ids.Canonical(raw)
		if !ok || !slices.Contains(set.LedgerIDs, id) {
			return nil, setViolation(CodeLedgerLinesNotInSet, "ledgerLines", "ledger "+raw+" is not a member of ledger set "+set.Code)
		}
		needsDuty = needsDuty || id != set.LeadingLedgerID
		out[id] = in.LedgerLines[raw]
	}
	if needsDuty && !in.CanPostLedgerSpecific {
		return nil, httpx.NewProblem(http.StatusForbidden, "AUTH-0003",
			"ledger-specific lines for a parallel ledger need duty "+DutyPostLedgerSpecific)
	}
	return out, nil
}

// Each set entry needs its own journal_entry.idempotency_key (unique per legal entity), so the
// request key is hashed with the ledger id; the request key itself is stored once in idempotency_keys.
func setEntryKey(key, ledgerID string) string {
	sum := sha256.Sum256([]byte(key + "|" + ledgerID))
	return hex.EncodeToString(sum[:])
}
