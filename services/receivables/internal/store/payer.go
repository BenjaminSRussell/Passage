package store

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

// valueOrEmpty dereferences an optional text column; nil reads as "".
func valueOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// learnPayer binds the payer's account hash and normalized name to the customer
// the payment was applied to (BR-30.1 R4, R5). Re-learning is a no-op; a payment
// with no customer (nothing applied yet) teaches nothing.
func learnPayer(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, customerID, accountHash, payerName *string) error {
	customer := valueOrEmpty(customerID)
	if customer == "" {
		return nil
	}
	if h := valueOrEmpty(accountHash); h != "" {
		if _, err := tx.Exec(ctx, `insert into payer_account_binding (tenant_id, legal_entity_id, payer_account_hash, customer_id)
			values ($1, $2, $3, $4) on conflict do nothing`, tenantID, legalEntityID, h, customer); err != nil {
			return err
		}
	}
	if name := domain.LearnableName(valueOrEmpty(payerName)); name != "" {
		if _, err := tx.Exec(ctx, `insert into payer_name_binding (tenant_id, legal_entity_id, customer_id, payer_name)
			values ($1, $2, $3, $4) on conflict do nothing`, tenantID, legalEntityID, customer, name); err != nil {
			return err
		}
	}
	return nil
}

// payerCustomers returns the customers already bound to the payer account hash (R4).
func payerCustomers(ctx context.Context, tx pgx.Tx, legalEntityID string, accountHash *string) (map[string]bool, error) {
	out := map[string]bool{}
	h := valueOrEmpty(accountHash)
	if h == "" {
		return out, nil
	}
	rows, err := tx.Query(ctx, `select customer_id::text from payer_account_binding
		where legal_entity_id = $1 and payer_account_hash = $2`, legalEntityID, h)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out[c] = true
	}
	return out, rows.Err()
}

// learnedPayerNames returns the normalized payer names learned for the given
// customers (the customers of the match pool), so R5 never reads the whole table.
func learnedPayerNames(ctx context.Context, tx pgx.Tx, legalEntityID string, customers []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(customers) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `select customer_id::text, payer_name from payer_name_binding
		where legal_entity_id = $1 and customer_id::text = any($2::text[])`, legalEntityID, customers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c, n string
		if err := rows.Scan(&c, &n); err != nil {
			return nil, err
		}
		out[c] = append(out[c], n)
	}
	return out, rows.Err()
}

// poolCustomers lists the distinct customers of the match pool.
func poolCustomers(pool []domain.Item) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, it := range pool {
		if it.CustomerID != "" && !seen[it.CustomerID] {
			seen[it.CustomerID] = true
			out = append(out, it.CustomerID)
		}
	}
	sort.Strings(out)
	return out
}
