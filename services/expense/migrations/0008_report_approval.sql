-- PSG-43 AC3: approval of a submitted report (SUBMITTED to APPROVED or REJECTED) and its
-- reimbursement through AP. submitted_by feeds the own-items block (EXP-SOD-001);
-- ap_open_item_id and reimbursed_at record the AP open item that paid the report.
alter table expense_report
  add column submitted_by text not null default '',
  add column decided_by text,
  add column decided_at timestamptz,
  add column ap_open_item_id uuid,
  add column reimbursed_at timestamptz;
