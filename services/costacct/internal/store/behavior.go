package store

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
)

// CodeBehaviorRuleExists is a second rule for the same element, object kind and date.
const CodeBehaviorRuleExists = "CST-0020"

// oneBehaviorRule is the unique index behind CST-0020.
const oneBehaviorRule = "cost_behavior_rule_once"

// BehaviorRule is an effective-dated fixed / variable classification of one
// cost element, optionally narrowed to one cost object kind (FR-38.16).
type BehaviorRule struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	ElementID     string `json:"costElementId"`
	ObjectKind    string `json:"objectKind,omitempty"`
	Behavior      string `json:"behavior"`
	EffectiveFrom string `json:"effectiveFrom"`
	CreatedAt     string `json:"createdAt,omitempty"`
}

// CreateBehaviorRule stores a rule for a cost element of the rule's legal
// entity. Rules classify journal lines mirrored after they are stored; entries
// already in the ledger keep their class.
func (s *Postgres) CreateBehaviorRule(ctx context.Context, key, hash string, in BehaviorRule) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		var known bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from cost_element where tenant_id = $1 and legal_entity_id = $2 and id = $3)`,
			p.TenantID, in.LegalEntityID, in.ElementID).Scan(&known); err != nil {
			return 0, nil, err
		}
		if !known {
			return 0, nil, problem(http.StatusUnprocessableEntity, CodeSecondaryNotFound, "cost element not found in this legal entity")
		}
		in.ID = ids.New()
		var created time.Time
		if err := tx.QueryRow(ctx, `insert into cost_behavior_rule (tenant_id, id, legal_entity_id, cost_element_id, object_kind, behavior, effective_from)
			values ($1, $2, $3, $4, $5, $6, $7::date) returning created_at`,
			p.TenantID, in.ID, in.LegalEntityID, in.ElementID, nullIfEmpty(in.ObjectKind), in.Behavior, in.EffectiveFrom).Scan(&created); err != nil {
			return 0, nil, err
		}
		in.CreatedAt = stamp(created)
		return http.StatusCreated, in, nil
	})
}

// ListBehaviorRules lists rules by element and date; legalEntityID and
// elementID narrow the list when set.
func (s *Postgres) ListBehaviorRules(ctx context.Context, legalEntityID, elementID string) ([]BehaviorRule, error) {
	out := []BehaviorRule{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select id::text, legal_entity_id::text, cost_element_id::text, coalesce(object_kind, ''), behavior,
				to_char(effective_from, 'YYYY-MM-DD'), created_at
			from cost_behavior_rule where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or cost_element_id::text = $3)
			order by cost_element_id, effective_from desc, object_kind nulls last limit $4`, tenantID, legalEntityID, elementID, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r BehaviorRule
			var created time.Time
			if err := rows.Scan(&r.ID, &r.LegalEntityID, &r.ElementID, &r.ObjectKind, &r.Behavior, &r.EffectiveFrom, &created); err != nil {
				return err
			}
			r.CreatedAt = stamp(created)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// behaviorClassifier loads the legal entity's rules, the element defaults and
// the kinds of the given cost objects once, and returns the FR-38.16 class of
// one entry.
func behaviorClassifier(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, objectIDs []string) (func(elementID, objectID, period string) string, error) {
	var rules []domain.BehaviorRule
	rows, err := tx.Query(ctx, `select cost_element_id::text, coalesce(object_kind, ''), behavior, to_char(effective_from, 'YYYY-MM-DD')
		from cost_behavior_rule where tenant_id = $1 and legal_entity_id = $2`, tenantID, legalEntityID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r domain.BehaviorRule
		if err := rows.Scan(&r.ElementID, &r.ObjectKind, &r.Behavior, &r.EffectiveFrom); err != nil {
			rows.Close()
			return nil, err
		}
		rules = append(rules, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	defaults, err := stringMap(ctx, tx, `select id::text, default_behavior from cost_element
		where tenant_id = $1 and legal_entity_id = $2 and default_behavior is not null`, tenantID, legalEntityID)
	if err != nil {
		return nil, err
	}
	kinds, err := stringMap(ctx, tx, `select id::text, kind from cost_object where tenant_id = $1 and legal_entity_id = $2 and id = any($3::uuid[])`,
		tenantID, legalEntityID, objectIDs)
	if err != nil {
		return nil, err
	}
	return func(elementID, objectID, period string) string {
		return domain.ClassifyBehavior(rules, elementID, defaults[elementID], kinds[objectID], period)
	}, nil
}

// stringMap reads a two-column (key, value) query into a map.
func stringMap(ctx context.Context, tx pgx.Tx, sql string, args ...any) (map[string]string, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
