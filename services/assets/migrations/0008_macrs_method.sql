-- Wave 37 proof: the MACRS method on asset books and group assets (IRS Pub. 946
-- percentage tables A-1 to A-5, NOT-IMPLEMENTED.md D1c). Expand-only:
-- existing rows keep their meaning.

ALTER TABLE asset_book DROP CONSTRAINT IF EXISTS asset_book_method_check;
ALTER TABLE asset_book ADD CONSTRAINT asset_book_method_check
  CHECK (method IN ('StraightLine', 'DecliningBalance', 'MACRS'));

ALTER TABLE group_asset DROP CONSTRAINT IF EXISTS group_asset_method_check;
ALTER TABLE group_asset ADD CONSTRAINT group_asset_method_check
  CHECK (method IN ('StraightLine', 'DecliningBalance', 'MACRS'));
