-- PSG-25 slice 12: shift premium windows on the pay agreement (FR-25.7, pay type
-- shift-premium). Each window is {name, from, to, premium} with HH:MM local times; a
-- window whose end is not after its start crosses midnight. The column is on the existing
-- pay_agreement table, so its row-level security and grants (0010) already apply.

ALTER TABLE pay_agreement ADD COLUMN IF NOT EXISTS shift_premiums jsonb NOT NULL DEFAULT '[]'
  CHECK (jsonb_typeof(shift_premiums) = 'array' AND jsonb_array_length(shift_premiums) <= 6);
