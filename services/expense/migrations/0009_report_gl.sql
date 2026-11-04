-- #472: the reimbursement liability of an approved report (Dr expense per category, Cr AP
-- control) is posted to the general ledger before payables is told about the report.
-- approved_on is the approval date the journal and the report.approved event carry;
-- gl_entry_id and gl_posted_at record the journal entry. Reports approved before this
-- migration take their decision date.
alter table expense_report
  add column approved_on date,
  add column gl_entry_id text check (gl_entry_id is null or gl_entry_id <> ''),
  add column gl_posted_at timestamptz,
  add constraint expense_report_gl_posted_together check ((gl_entry_id is null) = (gl_posted_at is null));
update expense_report set approved_on = (decided_at at time zone 'UTC')::date
  where status in ('APPROVED', 'PAID') and decided_at is not null;
create index expense_report_gl_due on expense_report (tenant_id, legal_entity_id, approved_on, id)
  where gl_entry_id is null and reimbursable_amount > 0 and status in ('APPROVED', 'PAID');
