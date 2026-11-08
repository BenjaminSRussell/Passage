-- Pricing context, wave 26 slice 26: the free_goods_calc_rule parameter (PRD 5.26.13, BR-26.13)
-- and free goods rule maintenance (FR-26.22). The calculation rule a new free goods rule takes
-- when its create request names none is a column on pricing_parameter, per legal entity (the
-- table, its row level security and its grant to passage_app come from 0012). A rule revision
-- updates free_goods_rule, which 0009 already grants SELECT, INSERT and UPDATE; no new table
-- here.
ALTER TABLE pricing_parameter
  ADD COLUMN IF NOT EXISTS free_goods_calc_rule text NOT NULL DEFAULT 'WHOLE_UNITS'
    CHECK (free_goods_calc_rule IN ('PROPORTIONAL', 'UNIT_REFERENCE', 'WHOLE_UNITS'));
