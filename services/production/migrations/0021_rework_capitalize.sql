-- Wave 21 slice 23: rework.capitalize (PRD 4.21.13, FR-21.20) per site.
-- Additive (expand): one defaulted column on production_parameters, off as in the PRD, so
-- every site keeps settling rework orders as rework variances. No new table, so no new
-- grant: the table keeps the SELECT, INSERT and UPDATE grant of 0013.
ALTER TABLE production_parameters ADD COLUMN IF NOT EXISTS rework_capitalize boolean NOT NULL DEFAULT false;
