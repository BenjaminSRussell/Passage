-- #49 (H-S10-1): the first bank reconciliation of an account can start from the opening book balance the migration factory
-- loaded (bank_opening_balance), instead of a balance the user states. The reconciliation records which one it holds; a
-- later restatement of the balances makes it stated again.
alter table bank_reconciliation add column if not exists book_balance_source text not null default 'stated'
  check (book_balance_source in ('stated', 'opening'));
