-- Wave 27 slice 31: intake_error.request_hash of a one-time customer request is
-- no longer a plain SHA-256. A one-time request carries the walk-in buyer's name
-- and tax id, so a guessed buyer could be confirmed against the plain digest
-- (sales_order has used a keyed hash for the same reason since slice 15). New
-- rows store "hmac:<key id>:<hex>", an HMAC under the field encryption key (or
-- "unkeyed" when the service has no keys); the store finds an existing row
-- recorded under an older key, so a retry still raises attempts instead of
-- opening a second row.
--
-- SQL cannot compute the HMAC (the keys are not in the database), so rows that
-- may hold a plain digest of a one-time request lose it: a row whose body is
-- sealed is certainly one-time, and a row with no kept body (logged before
-- slice 30, or resolved since) cannot be told apart. Their hash becomes
-- 'cleared:<id>', unique per row and never matched by a new record. A row
-- whose clear body has no oneTime member is an ordinary request and keeps its
-- plain hash. The cost: a retry of a cleared row's payload opens a new row
-- instead of raising attempts on the old one; the old row can be resolved.
-- No new table: intake_error keeps its grants (SELECT, INSERT, UPDATE).
--
-- intake_error has FORCE ROW LEVEL SECURITY and a migration sets no tenant, so
-- under the deployed migration role (the database owner, NOSUPERUSER
-- NOBYPASSRLS) the UPDATE would match no row at all and leave every plain
-- digest in place. The policy is lifted for the owner for the one statement and
-- forced again in the same transaction (dbx.Migrate runs each file in one), as
-- fulfillment 0018 and purchasing 0023 do.

SET LOCAL lock_timeout = '5s';
ALTER TABLE intake_error NO FORCE ROW LEVEL SECURITY;
UPDATE intake_error
SET request_hash = 'cleared:' || id::text
WHERE request_hash NOT LIKE 'hmac:%'
  AND request_hash NOT LIKE 'cleared:%'
  AND request_hash <> 'unkeyed'
  AND (request_payload_sealed IS NOT NULL
       OR request_payload IS NULL
       OR request_payload ? 'oneTime');
ALTER TABLE intake_error FORCE ROW LEVEL SECURITY;
