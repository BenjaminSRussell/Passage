-- #266: compensation cycle approval with segregation of duties. The cycle
-- records who created it and who approved it, and each worksheet row who
-- submitted it, so the approver can be checked against both before apply.
ALTER TABLE hr_comp_cycle ADD COLUMN IF NOT EXISTS created_by text;
ALTER TABLE hr_comp_cycle ADD COLUMN IF NOT EXISTS approved_by text;
ALTER TABLE hr_comp_cycle ADD COLUMN IF NOT EXISTS approved_at timestamptz;
ALTER TABLE hr_comp_cycle DROP CONSTRAINT IF EXISTS hr_comp_cycle_approval_pair;
ALTER TABLE hr_comp_cycle ADD CONSTRAINT hr_comp_cycle_approval_pair CHECK ((approved_at IS NULL) = (approved_by IS NULL));
ALTER TABLE hr_comp_cycle DROP CONSTRAINT IF EXISTS hr_comp_cycle_applied_approved;
ALTER TABLE hr_comp_cycle ADD CONSTRAINT hr_comp_cycle_applied_approved CHECK (status <> 'APPLIED' OR approved_at IS NOT NULL) NOT VALID;
ALTER TABLE hr_comp_worksheet_row ADD COLUMN IF NOT EXISTS submitted_by text;
