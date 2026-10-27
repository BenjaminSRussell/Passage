-- Wave 36 (PSG-36) slice 4: BAI2 and MT940 statements are stored beside camt.053.
-- Expand-only: the format check widens and bank_account gains an optional
-- statement account number; existing rows stay valid.

-- The slice-1 column check carries the default name bank_statement_format_check.
alter table bank_statement drop constraint bank_statement_format_check;
alter table bank_statement add constraint bank_statement_format_check
  check (format like 'camt.053.%' or format in ('BAI2', 'MT940'));

-- BAI2 03 records, MT940 :25: and camt.053 Acct/Id/Othr identify the account by
-- the bank's own number instead of the IBAN. Like the IBAN, only a SHA-256 of the
-- normalized value and its last four characters are kept.
alter table bank_account add column statement_account_hash bytea;
alter table bank_account add column statement_account_last4 text
  check (statement_account_last4 is null or char_length(statement_account_last4) between 1 and 4);
alter table bank_account add constraint bank_account_statement_account_pair
  check ((statement_account_hash is null) = (statement_account_last4 is null));
