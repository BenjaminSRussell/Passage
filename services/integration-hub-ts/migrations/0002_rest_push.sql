-- Integration hub: rest-push subscriptions (push events into a customer's existing ERP).
-- No new table: the columns below are on tables 0001 created, which already carry tenant_id,
-- row-level security and the passage_app grants (SELECT, INSERT, UPDATE on hub_delivery; SELECT, INSERT,
-- UPDATE, DELETE on hub_subscription).

ALTER TABLE hub_subscription ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'webhook' CHECK (kind IN ('webhook', 'rest-push'));
-- A rest-push subscription has no signing secret; its credentials are sealed instead.
ALTER TABLE hub_subscription ALTER COLUMN secret_sealed DROP NOT NULL;
-- Non-secret rest-push settings: outboundMappingId, method, responseKeyPath, onClientError, auth (type and public fields).
ALTER TABLE hub_subscription ADD COLUMN IF NOT EXISTS rest_config jsonb;
-- The auth secrets (password, token, api-key value, client secret) as one JSON object, sealed with field
-- encryption (fieldcrypt envelope v1, field hub_subscription.credentials, bound to tenant_id). Write-only.
ALTER TABLE hub_subscription ADD COLUMN IF NOT EXISTS credentials_sealed bytea CHECK (credentials_sealed IS NULL OR substring(credentials_sealed FROM 1 FOR 1) = '\x01'::bytea);
ALTER TABLE hub_subscription ADD CONSTRAINT hub_subscription_kind_shape CHECK (
  (kind = 'webhook' AND secret_sealed IS NOT NULL AND rest_config IS NULL AND credentials_sealed IS NULL)
  OR (kind = 'rest-push' AND secret_sealed IS NULL AND rest_config IS NOT NULL AND credentials_sealed IS NOT NULL));

-- The event's legalentityid extension (the service token for the migration service is scoped to it).
ALTER TABLE hub_delivery ADD COLUMN IF NOT EXISTS event_legal_entity text;
-- The legacy key the receiver answered with, and the receiver's last answer (truncated, credentials removed).
ALTER TABLE hub_delivery ADD COLUMN IF NOT EXISTS legacy_key text;
ALTER TABLE hub_delivery ADD COLUMN IF NOT EXISTS response_excerpt text CHECK (response_excerpt IS NULL OR length(response_excerpt) <= 2100);
