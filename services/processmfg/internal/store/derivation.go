package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
)

// Problem codes of the inheritance rules and the derivation.
const (
	CodeRuleNotFound  = "PMF-0126"
	CodeRuleDuplicate = "PMF-0127"
	CodeRuleRetired   = "PMF-0128"
	CodeNoRules       = "PMF-0129"
	CodeDeriveCycle   = "PMF-0130"

	// MaxSenders bounds the sender batches of one derivation.
	MaxSenders = 50
)

// InheritanceRule says how one attribute of a receiver batch of an item is
// derived from the same attribute of its sender batches (FR-24.15). A fixed rule
// carries one of FixedNum, FixedText and FixedDate.
type InheritanceRule struct {
	ID            string           `json:"id"`
	LegalEntityID string           `json:"legalEntityId"`
	ItemID        string           `json:"itemId"`
	Attribute     string           `json:"attribute"`
	Aggregation   string           `json:"aggregation"`
	Decimals      *int             `json:"decimals,omitempty"`
	FixedNum      *decimal.Decimal `json:"fixedNum,omitempty"`
	FixedText     *string          `json:"fixedText,omitempty"`
	FixedDate     *string          `json:"fixedDate,omitempty"`
	RetiredAt     *time.Time       `json:"retiredAt,omitempty"`
	RetiredBy     *string          `json:"retiredBy,omitempty"`
	Version       int              `json:"version"`
	CreatedBy     string           `json:"createdBy"`
	CreatedAt     time.Time        `json:"createdAt"`
	UpdatedAt     time.Time        `json:"updatedAt"`
}

// RuleFilter narrows the rule list. Active keeps the rules not retired.
type RuleFilter struct {
	LegalEntityID string
	ItemID        string
	Active        bool
	Limit         int
}

// DomainRule turns a stored rule into the rule domain.Derive works on.
func (r InheritanceRule) DomainRule() (domain.Rule, error) {
	rule := domain.Rule{Attribute: r.Attribute, Aggregation: domain.Aggregation(r.Aggregation), Decimals: -1}
	if r.Decimals != nil {
		rule.Decimals = int32(*r.Decimals)
	}
	rule.Fixed = domain.Value{Num: r.FixedNum, Text: r.FixedText}
	if r.FixedDate != nil {
		t, err := time.Parse(dateLayout, *r.FixedDate)
		if err != nil {
			return rule, err
		}
		rule.Fixed.Date = &t
	}
	return rule, nil
}

const ruleCols = `id, legal_entity_id, item_id, attribute, aggregation, decimals, fixed_num, fixed_text, to_char(fixed_date, 'YYYY-MM-DD'),
	retired_at, retired_by, version, created_by, created_at, updated_at`

func scanRule(row pgx.Row) (InheritanceRule, error) {
	var r InheritanceRule
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.ItemID, &r.Attribute, &r.Aggregation, &r.Decimals, &r.FixedNum, &r.FixedText, &r.FixedDate,
		&r.RetiredAt, &r.RetiredBy, &r.Version, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func ruleEvent(r InheritanceRule) pmfevents.InheritanceRuleData {
	d := pmfevents.InheritanceRuleData{RuleID: r.ID, LegalEntityID: r.LegalEntityID, ItemID: r.ItemID, Attribute: r.Attribute,
		Aggregation: r.Aggregation, Decimals: r.Decimals, Version: r.Version}
	switch {
	case r.FixedNum != nil:
		d.Fixed = r.FixedNum.String()
	case r.FixedText != nil:
		d.Fixed = *r.FixedText
	case r.FixedDate != nil:
		d.Fixed = *r.FixedDate
	}
	return d
}

// CreateInheritanceRule stores a rule for an attribute the item defines (FR-24.15)
// after the domain checks, and writes InheritanceRuleDefined in the same
// transaction. An item has one active rule per attribute (PMF-0127).
func (s *Postgres) CreateInheritanceRule(ctx context.Context, in InheritanceRule, idem Idem) (InheritanceRule, bool, error) {
	rule, err := in.DomainRule()
	if err != nil {
		return InheritanceRule{}, false, err
	}
	var out InheritanceRule
	created := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		defs, err := loadDefs(ctx, tx, in.ItemID, MaxList)
		if err != nil {
			return err
		}
		kind := ""
		for _, d := range defs {
			if d.Name == in.Attribute {
				kind = d.Kind
			}
		}
		if kind == "" {
			return httpx.NewProblem(http.StatusBadRequest, domain.CodeInvalid,
				fmt.Sprintf("item %s defines no batch attribute %q; define it first", in.ItemID, in.Attribute))
		}
		if err := domain.ValidateRule(rule, kind); err != nil {
			return err
		}
		p := principal(ctx)
		in.ID = ids.New()
		row, err := scanRule(tx.QueryRow(ctx, `insert into inheritance_rule (tenant_id, id, legal_entity_id, item_id, attribute, aggregation, decimals,
			fixed_num, fixed_text, fixed_date, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::date, $11) returning `+ruleCols,
			p.TenantID, in.ID, in.LegalEntityID, in.ItemID, in.Attribute, in.Aggregation, in.Decimals, in.FixedNum, in.FixedText, in.FixedDate, p.UserID))
		if err != nil {
			return mapDuplicate(err, CodeRuleDuplicate, fmt.Sprintf("item %s already has an active inheritance rule for %q; retire it first", in.ItemID, in.Attribute))
		}
		if err := enqueue(ctx, tx, "inheritancerule", row.ID, pmfevents.InheritanceRuleDefined, ruleEvent(row)); err != nil {
			return err
		}
		out, created = row, true
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, created, err
}

// ListInheritanceRules returns rules ordered by item and attribute, oldest first.
func (s *Postgres) ListInheritanceRules(ctx context.Context, f RuleFilter) ([]InheritanceRule, error) {
	out := []InheritanceRule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+ruleCols+` from inheritance_rule
			where ($1 = '' or item_id::text = $1) and ($2 = '' or legal_entity_id::text = $2) and (not $3 or retired_at is null)
			order by item_id, attribute, created_at, id limit $4`, f.ItemID, f.LegalEntityID, f.Active, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRule(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetInheritanceRule returns one rule.
func (s *Postgres) GetInheritanceRule(ctx context.Context, id string) (InheritanceRule, error) {
	var out InheritanceRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadRule(ctx, tx, id, "")
		return err
	})
	return out, err
}

func loadRule(ctx context.Context, tx pgx.Tx, id, lock string) (InheritanceRule, error) {
	r, err := scanRule(tx.QueryRow(ctx, `select `+ruleCols+` from inheritance_rule where id = $1`+lock, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, notFound(CodeRuleNotFound, "inheritance rule", id)
	}
	return r, err
}

// RetireInheritanceRule ends a rule under the version the caller read; batches
// derived earlier keep their values and their log.
func (s *Postgres) RetireInheritanceRule(ctx context.Context, id string, version int, idem Idem) (InheritanceRule, bool, error) {
	var out InheritanceRule
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := loadRule(ctx, tx, id, " for update")
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if r.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
				fmt.Sprintf("inheritance rule %s is at version %d, not %d; reload and try again", r.ID, r.Version, version))
		}
		if r.RetiredAt != nil {
			return httpx.NewProblem(http.StatusConflict, CodeRuleRetired, fmt.Sprintf("inheritance rule %s is already retired", r.ID))
		}
		if _, err := tx.Exec(ctx, `update inheritance_rule set retired_at = now(), retired_by = $2, version = version + 1, updated_at = now() where id = $1`,
			id, principal(ctx).UserID); err != nil {
			return err
		}
		if out, err = loadRule(ctx, tx, id, ""); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "inheritancerule", id, pmfevents.InheritanceRuleRetired, ruleEvent(out)); err != nil {
			return err
		}
		changed = true
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, changed, err
}

// DeriveSender is one sender batch of a derivation with the quantity consumed
// from it (the weight of a weighted average). The register keeps no quantity.
type DeriveSender struct {
	BatchID string          `json:"batchId"`
	Qty     decimal.Decimal `json:"qty"`
}

// DeriveInput is the derive command: the receiver batch at the version the
// caller read and its senders.
type DeriveInput struct {
	BatchID string
	Version int
	Senders []DeriveSender
}

// DerivedValue is the outcome of one rule. Value is the derived value as text
// (a date as yyyy-mm-dd); Recorded is false when no sender had a value, so the
// receiver attribute was left as it was. Missing lists the senders without a
// value (PMF-0007).
type DerivedValue struct {
	RuleID      string   `json:"ruleId"`
	Attribute   string   `json:"attribute"`
	Aggregation string   `json:"aggregation"`
	Recorded    bool     `json:"recorded"`
	Value       string   `json:"value,omitempty"`
	Missing     []string `json:"missing"`
}

// DeriveResult is the answer of the derive command. Warnings carry one PMF-0007
// line per attribute with senders that had no value.
type DeriveResult struct {
	Batch    Batch          `json:"batch"`
	Derived  []DerivedValue `json:"derived"`
	Warnings []string       `json:"warnings"`
}

// DeriveBatch fills the attributes of a receiver batch from its sender batches
// with the item's active inheritance rules (FR-24.15, BR-24.11): per rule it
// reads the senders' current values, runs domain.Derive and records the result
// as the receiver's attribute values (checked against its definitions, in the
// attribute history), logs each derivation with the senders used and the ones
// without a value, and writes BatchDerived. Running it again re-derives from
// the senders given and moves the version. A sender must be another batch of
// the receiver's legal entity, and none may descend from the receiver through
// the latest derivation of each batch (PMF-0130): material flows one way, and a
// cycle would never end the forward propagation of FR-24.15.
func (s *Postgres) DeriveBatch(ctx context.Context, in DeriveInput, idem Idem) (DeriveResult, bool, error) {
	var out DeriveResult
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		b, err := loadBatch(ctx, tx, in.BatchID, " for update")
		if err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); found || err != nil {
			return err
		}
		if b.Version != in.Version {
			return staleBatch(b, in.Version)
		}
		senderVals := make([]map[string]domain.Value, len(in.Senders))
		numbers := make([]string, len(in.Senders))
		for i, snd := range in.Senders {
			if snd.BatchID == b.ID {
				return httpx.NewProblem(http.StatusBadRequest, domain.CodeInvalid, "a batch cannot be its own sender")
			}
			sb, err := loadBatch(ctx, tx, snd.BatchID, "")
			if err != nil {
				return err
			}
			if sb.LegalEntityID != b.LegalEntityID {
				return httpx.NewProblem(http.StatusBadRequest, domain.CodeInvalid,
					fmt.Sprintf("sender %s belongs to another legal entity than %s", sb.BatchNumber, b.BatchNumber))
			}
			if senderVals[i], err = domainValues(sb.Attributes); err != nil {
				return err
			}
			numbers[i] = sb.BatchNumber
		}
		if err := refuseCycle(ctx, tx, b, in.Senders); err != nil {
			return err
		}
		before, err := domainValues(b.Attributes)
		if err != nil {
			return err
		}
		rules, err := activeRules(ctx, tx, b.LegalEntityID, b.ItemID)
		if err != nil {
			return err
		}
		if len(rules) == 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeNoRules,
				fmt.Sprintf("item %s has no active inheritance rule; define one first", b.ItemID))
		}
		values := map[string]domain.Value{}
		res := DeriveResult{Derived: []DerivedValue{}, Warnings: []string{}}
		type logRow struct {
			rule   InheritanceRule
			dv     DerivedValue
			val    domain.Value
			before domain.Value
		}
		var logs []logRow
		evt := pmfevents.BatchDerivedData{BatchID: b.ID, LegalEntityID: b.LegalEntityID, ItemID: b.ItemID, BatchNumber: b.BatchNumber,
			Senders: []string{}, Derived: []pmfevents.DerivedAttribute{}}
		for _, snd := range in.Senders {
			evt.Senders = append(evt.Senders, snd.BatchID)
		}
		for _, r := range rules {
			dr, err := r.DomainRule()
			if err != nil {
				return err
			}
			ds := make([]domain.Sender, len(in.Senders))
			for i, snd := range in.Senders {
				ds[i] = domain.Sender{BatchID: snd.BatchID, Value: senderVals[i][r.Attribute], Qty: snd.Qty}
			}
			d, err := domain.Derive(dr, ds)
			if err != nil {
				return err
			}
			dv := DerivedValue{RuleID: r.ID, Attribute: r.Attribute, Aggregation: r.Aggregation, Recorded: !d.Value.Empty(), Missing: append([]string{}, d.Missing...)}
			if dv.Recorded {
				values[r.Attribute] = d.Value
				dv.Value = valueText(d.Value)
			}
			if len(dv.Missing) > 0 {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %d sender batch(es) have no value for %s", domain.CodeMissingSender, len(dv.Missing), r.Attribute))
			}
			res.Derived = append(res.Derived, dv)
			logs = append(logs, logRow{r, dv, d.Value, before[r.Attribute]})
			evt.Derived = append(evt.Derived, pmfevents.DerivedAttribute{Attribute: dv.Attribute, Aggregation: dv.Aggregation, Recorded: dv.Recorded, Value: dv.Value, Missing: dv.Missing})
		}
		version := b.Version
		if len(values) > 0 {
			rec, err := recordValues(ctx, tx, b, values)
			if err != nil {
				return err
			}
			res.Batch = rec.Batch
			version = rec.Batch.Version
		} else if res.Batch, err = loadBatch(ctx, tx, b.ID, ""); err != nil {
			return err
		}
		evt.Version = version
		type senderLog struct {
			BatchID     string `json:"batchId"`
			BatchNumber string `json:"batchNumber"`
			Qty         string `json:"qty"`
		}
		sl := make([]senderLog, len(in.Senders))
		for i, snd := range in.Senders {
			sl[i] = senderLog{snd.BatchID, numbers[i], snd.Qty.String()}
		}
		sj, _ := json.Marshal(sl)
		p := principal(ctx)
		for _, l := range logs {
			mj, _ := json.Marshal(l.dv.Missing)
			if _, err := tx.Exec(ctx, `insert into batch_derivation (tenant_id, id, legal_entity_id, receiver_batch_id, receiver_version, rule_id, attribute, aggregation,
				recorded, num_value, text_value, date_value, before_num, before_text, before_date, senders, missing, run_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
				p.TenantID, ids.New(), b.LegalEntityID, b.ID, version, l.rule.ID, l.rule.Attribute, l.rule.Aggregation,
				l.dv.Recorded, l.val.Num, l.val.Text, l.val.Date, l.before.Num, l.before.Text, l.before.Date, sj, mj, p.UserID); err != nil {
				return err
			}
		}
		if err := enqueue(ctx, tx, "batch", b.ID, pmfevents.BatchDerived, evt); err != nil {
			return err
		}
		out, changed = res, true
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, changed, err
}

// refuseCycle refuses a derivation when the receiver is among the ancestors of a
// sender: the senders of each batch's latest derivation are its sender links,
// followed up from the senders given (PMF-0130).
func refuseCycle(ctx context.Context, tx pgx.Tx, receiver Batch, senders []DeriveSender) error {
	start := make([]string, len(senders))
	for i, snd := range senders {
		start[i] = snd.BatchID
	}
	var via string
	err := tx.QueryRow(ctx, `with recursive up (id, child) as (
			select s, s from unnest($1::uuid[]) s
		  union
			select (e->>'batchId')::uuid, up.child from up
			cross join lateral (select d.senders from batch_derivation d where d.receiver_batch_id = up.id
				order by d.run_at desc, d.id limit 1) last
			cross join lateral jsonb_array_elements(last.senders) e
		)
		select b.batch_number from up join batch_record b on b.id = up.child where up.id = $2 limit 1`, start, receiver.ID).Scan(&via)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeDeriveCycle,
		fmt.Sprintf("sender %s was derived from %s, directly or through other batches; a batch cannot be derived from its own descendants", via, receiver.BatchNumber))
}

func valueText(v domain.Value) string {
	switch {
	case v.Num != nil:
		return v.Num.String()
	case v.Text != nil:
		return *v.Text
	case v.Date != nil:
		return v.Date.Format(dateLayout)
	}
	return ""
}

// storedText is a stored value as text: a number, a text or a yyyy-mm-dd date.
func storedText(num *decimal.Decimal, text, date *string) string {
	switch {
	case num != nil:
		return num.String()
	case text != nil:
		return *text
	case date != nil:
		return *date
	}
	return ""
}

func activeRules(ctx context.Context, tx pgx.Tx, legalEntityID, itemID string) ([]InheritanceRule, error) {
	rows, err := tx.Query(ctx, `select `+ruleCols+` from inheritance_rule
		where legal_entity_id = $1 and item_id = $2 and retired_at is null order by attribute limit $3`, legalEntityID, itemID, MaxList)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InheritanceRule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Derivation is one row of the derivation log (the derivation monitor of
// FR-24.15): the rule applied to a receiver batch, the receiver value before and
// the value derived, the senders used and the senders without a value.
type Derivation struct {
	ID              string          `json:"id"`
	LegalEntityID   string          `json:"legalEntityId"`
	ReceiverBatchID string          `json:"receiverBatchId"`
	BatchNumber     string          `json:"batchNumber"`
	ReceiverVersion int             `json:"receiverVersion"`
	RuleID          string          `json:"ruleId"`
	Attribute       string          `json:"attribute"`
	Aggregation     string          `json:"aggregation"`
	Recorded        bool            `json:"recorded"`
	Value           string          `json:"value,omitempty"`
	ValueBefore     string          `json:"valueBefore,omitempty"`
	Senders         json.RawMessage `json:"senders"`
	Missing         []string        `json:"missing"`
	RunBy           string          `json:"runBy"`
	RunAt           time.Time       `json:"runAt"`
}

// DerivationFilter narrows the log. WithMissing keeps the derivations that had
// senders without a value.
type DerivationFilter struct {
	BatchID     string
	ItemID      string
	WithMissing bool
	Limit       int
}

// ListDerivations returns the derivation log, newest first.
func (s *Postgres) ListDerivations(ctx context.Context, f DerivationFilter) ([]Derivation, error) {
	out := []Derivation{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select d.id, d.legal_entity_id, d.receiver_batch_id, b.batch_number, d.receiver_version, d.rule_id, d.attribute, d.aggregation,
			d.recorded, d.num_value, d.text_value, to_char(d.date_value, 'YYYY-MM-DD'),
			d.before_num, d.before_text, to_char(d.before_date, 'YYYY-MM-DD'), d.senders, d.missing, d.run_by, d.run_at
			from batch_derivation d join batch_record b on b.tenant_id = d.tenant_id and b.id = d.receiver_batch_id
			where ($1 = '' or d.receiver_batch_id::text = $1) and ($2 = '' or b.item_id::text = $2) and (not $3 or jsonb_array_length(d.missing) > 0)
			order by d.run_at desc, d.id limit $4`, f.BatchID, f.ItemID, f.WithMissing, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Derivation
			var num, bnum *decimal.Decimal
			var text, date, btext, bdate *string
			var missing []byte
			if err := rows.Scan(&d.ID, &d.LegalEntityID, &d.ReceiverBatchID, &d.BatchNumber, &d.ReceiverVersion, &d.RuleID, &d.Attribute, &d.Aggregation,
				&d.Recorded, &num, &text, &date, &bnum, &btext, &bdate, &d.Senders, &missing, &d.RunBy, &d.RunAt); err != nil {
				return err
			}
			d.Value = storedText(num, text, date)
			d.ValueBefore = storedText(bnum, btext, bdate)
			d.Missing = []string{}
			if err := json.Unmarshal(missing, &d.Missing); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}
