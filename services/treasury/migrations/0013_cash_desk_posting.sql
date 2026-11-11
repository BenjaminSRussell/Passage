-- FR-36.16 remainders: cash desk entries post to the general ledger, a receipt or payment can be a transfer to or from a
-- bank account, a day is counted by denomination, a desk can require a second signature on close.

alter table cash_desk add column dual_control boolean not null default false;

-- posting_status: not_posted (no ledger configured when it was booked), pending (booked, ledger not yet answered), posted.
alter table cash_desk_entry
  add column counter_account text check (counter_account is null or counter_account ~ '^[A-Za-z0-9._-]{1,20}$'),
  add column bank_account_id uuid,
  add column posting_status text not null default 'not_posted' check (posting_status in ('not_posted', 'pending', 'posted')),
  add column ledger_entry_id text,
  add column posted_at timestamptz;

alter table cash_desk_day
  add column denominations jsonb,
  add column approved_by text,
  add column approved_at timestamptz,
  add column difference_posting_status text not null default 'not_posted' check (difference_posting_status in ('not_posted', 'pending', 'posted')),
  add column difference_ledger_entry_id text;

-- A day counted by one person waits in 'closing' for the second signature; it still blocks the next day and new entries.
alter table cash_desk_day drop constraint cash_desk_day_status_check;
alter table cash_desk_day drop constraint cash_desk_day_check;
alter table cash_desk_day add constraint cash_desk_day_status_check check (status in ('open', 'closing', 'closed'));
alter table cash_desk_day add constraint cash_desk_day_closed_at_check check ((status in ('open', 'closing')) = (closed_at is null));
drop index ux_cash_desk_day_open;
create unique index ux_cash_desk_day_open on cash_desk_day (tenant_id, desk_id) where status in ('open', 'closing');

-- The journal stays append-only; only the posting outcome of a line may be written after the insert.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (posting_status, ledger_entry_id, posted_at) ON cash_desk_entry TO passage_app';
  END IF;
END
$$;
