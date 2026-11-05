-- #483: asynchronous MDNs we owe partners are durable, and deliveries waiting for an asynchronous MDN time out.
--
-- transport_mdn holds an MDN a partner asked us to post to its Receipt-Delivery-Option URL. The MDN that answers a
-- recorded message is inserted in the transaction that records it (with transport_inbound DONE), so the receipt and
-- the MDN commit or roll back together; an MDN for a refused or duplicate message is inserted on its own. The
-- dispatcher leases due rows like transport_delivery (skip locked, attempts, back-off, FAILED after the last attempt)
-- and posts each only to the scheme and host of the partner's configured AS2 URL, without following redirects. The
-- row holds the signed MDN as sent (headers and body): an MDN carries the Message-ID it answers, the MIC and the
-- disposition, never key material.

CREATE TABLE IF NOT EXISTS transport_mdn (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  -- The AS2 Message-ID (without angle brackets) of the partner message this MDN answers, and the MDN's own.
  original_message_id text NOT NULL CHECK (char_length(original_message_id) BETWEEN 1 AND 255),
  mdn_message_id text NOT NULL CHECK (char_length(mdn_message_id) BETWEEN 1 AND 255),
  target_url text NOT NULL CHECK (char_length(target_url) BETWEEN 1 AND 2048),
  headers jsonb NOT NULL CHECK (jsonb_typeof(headers) = 'object'),
  body bytea NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 1048576),
  -- The inbound interchange the receipt recorded; null for an MDN that refuses the message or reports a duplicate.
  interchange_id uuid,
  status text NOT NULL CHECK (status IN ('PENDING', 'DELIVERED', 'FAILED')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error text CHECK (char_length(last_error) <= 1000),
  delivered_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, mdn_message_id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id),
  FOREIGN KEY (tenant_id, interchange_id) REFERENCES interchange (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS transport_mdn_due ON transport_mdn (tenant_id, next_attempt_at) WHERE status = 'PENDING';
CREATE INDEX IF NOT EXISTS transport_mdn_original ON transport_mdn (tenant_id, partner_id, original_message_id);
ALTER TABLE transport_mdn ENABLE ROW LEVEL SECURITY;
ALTER TABLE transport_mdn FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS transport_mdn_tenant_access ON transport_mdn;
CREATE POLICY transport_mdn_tenant_access ON transport_mdn AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS transport_mdn_legal_entity_scope ON transport_mdn;
CREATE POLICY transport_mdn_legal_entity_scope ON transport_mdn AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- When an AWAITING_MDN delivery stops waiting (EDI_AS2_MDN_TIMEOUT_MINUTES after it was sent, set by the dispatcher)
-- and how many times it timed out. Only AWAITING_MDN rows have a due time. Rows that were AWAITING_MDN before this
-- migration (0006 is part of the same unreleased change) get none and wait for their MDN or a resend as before.
ALTER TABLE transport_delivery ADD COLUMN IF NOT EXISTS mdn_due_at timestamptz;
ALTER TABLE transport_delivery ADD COLUMN IF NOT EXISTS mdn_timeouts integer NOT NULL DEFAULT 0;
ALTER TABLE transport_delivery DROP CONSTRAINT IF EXISTS transport_delivery_mdn_due_check;
ALTER TABLE transport_delivery ADD CONSTRAINT transport_delivery_mdn_due_check CHECK (mdn_due_at IS NULL OR status = 'AWAITING_MDN');
ALTER TABLE transport_delivery DROP CONSTRAINT IF EXISTS transport_delivery_mdn_timeouts_check;
ALTER TABLE transport_delivery ADD CONSTRAINT transport_delivery_mdn_timeouts_check CHECK (mdn_timeouts >= 0);
CREATE INDEX IF NOT EXISTS transport_delivery_mdn_due ON transport_delivery (tenant_id, mdn_due_at) WHERE status = 'AWAITING_MDN';

-- The cross-tenant work index gains MDN (owed MDNs due) and MDN_TIMEOUT (deliveries whose MDN wait ends).
ALTER TABLE transport_work DROP CONSTRAINT IF EXISTS transport_work_kind_check;
ALTER TABLE transport_work ADD CONSTRAINT transport_work_kind_check
  CHECK (kind IN ('AS2_ENDPOINT', 'SFTP_POLL', 'DELIVERY', 'MDN', 'MDN_TIMEOUT'));

DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION edi_transport_delivery_track() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    BEGIN
      IF NEW.status = 'PENDING' THEN
        INSERT INTO transport_work (kind, ref, tenant_id, partner_id, due_at) VALUES ('DELIVERY', NEW.id, NEW.tenant_id, NEW.partner_id, NEW.next_attempt_at)
        ON CONFLICT (kind, ref) DO UPDATE SET due_at = EXCLUDED.due_at;
      ELSE
        DELETE FROM transport_work WHERE kind = 'DELIVERY' AND ref = NEW.id;
      END IF;
      IF NEW.status = 'AWAITING_MDN' AND NEW.mdn_due_at IS NOT NULL THEN
        INSERT INTO transport_work (kind, ref, tenant_id, partner_id, due_at) VALUES ('MDN_TIMEOUT', NEW.id, NEW.tenant_id, NEW.partner_id, NEW.mdn_due_at)
        ON CONFLICT (kind, ref) DO UPDATE SET due_at = EXCLUDED.due_at;
      ELSE
        DELETE FROM transport_work WHERE kind = 'MDN_TIMEOUT' AND ref = NEW.id;
      END IF;
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION edi_transport_mdn_track() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    BEGIN
      IF NEW.status = 'PENDING' THEN
        INSERT INTO transport_work (kind, ref, tenant_id, partner_id, due_at) VALUES ('MDN', NEW.id, NEW.tenant_id, NEW.partner_id, NEW.next_attempt_at)
        ON CONFLICT (kind, ref) DO UPDATE SET due_at = EXCLUDED.due_at;
      ELSE
        DELETE FROM transport_work WHERE kind = 'MDN' AND ref = NEW.id;
      END IF;
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION edi_transport_due_tenants(work text) RETURNS SETOF uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT DISTINCT w.tenant_id FROM transport_work w
        WHERE w.kind = work AND work IN ('SFTP_POLL', 'DELIVERY', 'MDN', 'MDN_TIMEOUT') AND w.due_at <= pg_catalog.now() $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION edi_transport_delivery_track() FROM PUBLIC;
REVOKE ALL ON FUNCTION edi_transport_mdn_track() FROM PUBLIC;
REVOKE ALL ON FUNCTION edi_transport_due_tenants(text) FROM PUBLIC;
DROP TRIGGER IF EXISTS transport_delivery_track ON transport_delivery;
CREATE TRIGGER transport_delivery_track AFTER INSERT OR UPDATE OF status, next_attempt_at, mdn_due_at ON transport_delivery
  FOR EACH ROW EXECUTE FUNCTION edi_transport_delivery_track();
DROP TRIGGER IF EXISTS transport_mdn_track ON transport_mdn;
CREATE TRIGGER transport_mdn_track AFTER INSERT OR UPDATE OF status, next_attempt_at ON transport_mdn
  FOR EACH ROW EXECUTE FUNCTION edi_transport_mdn_track();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON transport_mdn TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION edi_transport_due_tenants(text) TO passage_app';
  END IF;
END
$$;
