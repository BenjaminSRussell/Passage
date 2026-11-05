-- #483: partner transports (AS2, SFTP), outbound deliveries and inbound receipt from the transports.
--
-- A partner's transport hangs off its trade profile and is scoped to the profile's legal entity, like the profile
-- itself: only a caller scoped to that entity may read or change it. No secret is stored: the AS2 signing key and
-- the SFTP credential are names of files the service reads at use time from its mounted secret directory
-- (EDI_SECRETS_DIR, populated by an ExternalSecret); the checks below refuse anything that is not such a name.

ALTER TABLE partner_trade_profile DROP CONSTRAINT IF EXISTS partner_trade_profile_entity_key;
ALTER TABLE partner_trade_profile ADD CONSTRAINT partner_trade_profile_entity_key UNIQUE (tenant_id, partner_id, legal_entity_id);

CREATE TABLE IF NOT EXISTS partner_transport (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('API', 'AS2', 'SFTP')),
  -- Opaque id in the AS2 inbound URL (/as2/v1/{endpoint_id}); it identifies the profile, not the caller: the caller
  -- is authenticated by its signature against as2_partner_cert.
  endpoint_id uuid NOT NULL UNIQUE,
  as2_partner_id text CHECK (as2_partner_id ~ '^[A-Za-z0-9._@:-]{1,128}$'),
  as2_our_id text CHECK (as2_our_id ~ '^[A-Za-z0-9._@:-]{1,128}$'),
  as2_url text CHECK (char_length(as2_url) <= 2048),
  as2_partner_cert text CHECK (char_length(as2_partner_cert) <= 16384 AND as2_partner_cert NOT LIKE '%PRIVATE KEY%'),
  as2_signing_key_ref text CHECK (as2_signing_key_ref ~ '^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$' AND as2_signing_key_ref NOT LIKE '%..%'),
  as2_signing_alg text CHECK (as2_signing_alg IN ('sha-256', 'sha-384', 'sha-512')),
  as2_encryption_alg text CHECK (as2_encryption_alg IN ('none', 'aes128-cbc', 'aes192-cbc', 'aes256-cbc')),
  as2_mdn_mode text CHECK (as2_mdn_mode IN ('sync', 'async')),
  sftp_host text CHECK (sftp_host ~ '^[A-Za-z0-9.:-]{1,253}$'),
  sftp_port integer CHECK (sftp_port BETWEEN 1 AND 65535),
  sftp_user text CHECK (sftp_user ~ '^[A-Za-z0-9._-]{1,64}$'),
  sftp_host_key_fingerprint text CHECK (sftp_host_key_fingerprint ~ '^SHA256:[A-Za-z0-9+/]{43}$'),
  sftp_auth_method text CHECK (sftp_auth_method IN ('key', 'password')),
  sftp_credential_ref text CHECK (sftp_credential_ref ~ '^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$' AND sftp_credential_ref NOT LIKE '%..%'),
  sftp_outbound_dir text CHECK (char_length(sftp_outbound_dir) BETWEEN 1 AND 255),
  sftp_inbound_dir text CHECK (char_length(sftp_inbound_dir) BETWEEN 1 AND 255),
  sftp_archive_dir text CHECK (sftp_archive_dir ~ '^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$'),
  sftp_failed_dir text CHECK (sftp_failed_dir ~ '^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$'),
  sftp_poll_seconds integer CHECK (sftp_poll_seconds BETWEEN 60 AND 86400),
  sftp_next_poll_at timestamptz,
  sftp_poll_failures integer NOT NULL DEFAULT 0,
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, partner_id),
  FOREIGN KEY (tenant_id, partner_id, legal_entity_id) REFERENCES partner_trade_profile (tenant_id, partner_id, legal_entity_id) ON UPDATE CASCADE,
  CHECK (kind <> 'AS2' OR (as2_partner_id IS NOT NULL AND as2_our_id IS NOT NULL AND as2_url IS NOT NULL AND as2_partner_cert IS NOT NULL
    AND as2_signing_key_ref IS NOT NULL AND as2_signing_alg IS NOT NULL AND as2_encryption_alg IS NOT NULL AND as2_mdn_mode IS NOT NULL)),
  CHECK (kind <> 'SFTP' OR (sftp_host IS NOT NULL AND sftp_port IS NOT NULL AND sftp_user IS NOT NULL AND sftp_host_key_fingerprint IS NOT NULL
    AND sftp_auth_method IS NOT NULL AND sftp_credential_ref IS NOT NULL AND sftp_outbound_dir IS NOT NULL
    AND sftp_archive_dir IS NOT NULL AND sftp_failed_dir IS NOT NULL AND sftp_poll_seconds IS NOT NULL)),
  CHECK (kind = 'AS2' OR (as2_partner_id IS NULL AND as2_url IS NULL AND as2_partner_cert IS NULL AND as2_signing_key_ref IS NULL)),
  CHECK (kind = 'SFTP' OR (sftp_host IS NULL AND sftp_credential_ref IS NULL AND sftp_next_poll_at IS NULL)),
  CHECK (sftp_next_poll_at IS NULL OR sftp_inbound_dir IS NOT NULL)
);
ALTER TABLE partner_transport ENABLE ROW LEVEL SECURITY;
ALTER TABLE partner_transport FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS partner_transport_tenant_access ON partner_transport;
CREATE POLICY partner_transport_tenant_access ON partner_transport AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS partner_transport_legal_entity_scope ON partner_transport;
CREATE POLICY partner_transport_legal_entity_scope ON partner_transport AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
CREATE INDEX IF NOT EXISTS partner_transport_sftp_due ON partner_transport (tenant_id, sftp_next_poll_at) WHERE sftp_next_poll_at IS NOT NULL;

-- One delivery attempt chain of an outbound interchange (DOCUMENT: its raw payload) or of the 997 answering an
-- inbound one (ACK: its ack payload) over the partner's transport. message_id is the AS2 Message-ID (and the SFTP
-- file stem); a lease on next_attempt_at keeps two dispatchers from sending the same row at once, and a resend after
-- a crash reuses the Message-ID so the partner can drop the duplicate.
CREATE TABLE IF NOT EXISTS transport_delivery (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  interchange_id uuid NOT NULL,
  payload_kind text NOT NULL CHECK (payload_kind IN ('DOCUMENT', 'ACK')),
  transport text NOT NULL CHECK (transport IN ('AS2', 'SFTP')),
  message_id text NOT NULL CHECK (message_id ~ '^[A-Za-z0-9][A-Za-z0-9._@-]{0,200}$'),
  filename text NOT NULL CHECK (filename ~ '^[A-Za-z0-9][A-Za-z0-9._@-]{0,200}$'),
  status text NOT NULL CHECK (status IN ('PENDING', 'AWAITING_MDN', 'DELIVERED', 'REJECTED', 'FAILED')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  sent_mic text CHECK (char_length(sent_mic) <= 200),
  mdn_mic text CHECK (char_length(mdn_mic) <= 200),
  mdn_disposition text CHECK (char_length(mdn_disposition) <= 500),
  mdn_signed boolean,
  reference text CHECK (char_length(reference) <= 1024),
  last_error text CHECK (char_length(last_error) <= 1000),
  sent_at timestamptz,
  delivered_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, message_id),
  FOREIGN KEY (tenant_id, interchange_id) REFERENCES interchange (tenant_id, id),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS transport_delivery_due ON transport_delivery (tenant_id, next_attempt_at) WHERE status = 'PENDING';
CREATE INDEX IF NOT EXISTS transport_delivery_interchange ON transport_delivery (tenant_id, interchange_id, created_at);
ALTER TABLE transport_delivery ENABLE ROW LEVEL SECURITY;
ALTER TABLE transport_delivery FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS transport_delivery_tenant_access ON transport_delivery;
CREATE POLICY transport_delivery_tenant_access ON transport_delivery AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS transport_delivery_legal_entity_scope ON transport_delivery;
CREATE POLICY transport_delivery_legal_entity_scope ON transport_delivery AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- What arrived over a transport: an AS2 message (ref = Message-ID, content_sha256 = hash of the payload) or an SFTP
-- file (ref = file name). A row is DONE once its interchange is recorded (same transaction), so a retransmission of
-- the same Message-ID or the same file name and content is recognised; FAILED rows back off, PARKED rows are moved
-- to the partner's failed directory and left for an operator.
CREATE TABLE IF NOT EXISTS transport_inbound (
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  transport text NOT NULL CHECK (transport IN ('AS2', 'SFTP')),
  ref text NOT NULL CHECK (char_length(ref) BETWEEN 1 AND 255),
  content_sha256 char(64) NOT NULL CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
  status text NOT NULL CHECK (status IN ('DONE', 'FAILED', 'PARKED')),
  interchange_id uuid,
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at timestamptz,
  last_error text CHECK (char_length(last_error) <= 1000),
  received_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, partner_id, transport, ref, content_sha256),
  FOREIGN KEY (tenant_id, partner_id) REFERENCES trading_partner (tenant_id, id),
  FOREIGN KEY (tenant_id, interchange_id) REFERENCES interchange (tenant_id, id)
);
ALTER TABLE transport_inbound ENABLE ROW LEVEL SECURITY;
ALTER TABLE transport_inbound FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS transport_inbound_tenant_access ON transport_inbound;
CREATE POLICY transport_inbound_tenant_access ON transport_inbound AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS transport_inbound_legal_entity_scope ON transport_inbound;
CREATE POLICY transport_inbound_legal_entity_scope ON transport_inbound AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Cross-tenant work index for the background jobs and the AS2 endpoint lookup, like close.accrual_due: no
-- passage_app grant, maintained by SECURITY DEFINER triggers and read only by the SECURITY DEFINER functions below,
-- which return tenant and partner ids and nothing else.
CREATE TABLE IF NOT EXISTS transport_work (
  kind text NOT NULL CHECK (kind IN ('AS2_ENDPOINT', 'SFTP_POLL', 'DELIVERY')),
  ref uuid NOT NULL,
  tenant_id uuid NOT NULL,
  partner_id uuid NOT NULL,
  due_at timestamptz,
  PRIMARY KEY (kind, ref)
);
CREATE INDEX IF NOT EXISTS transport_work_due ON transport_work (kind, due_at) WHERE due_at IS NOT NULL;

DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION edi_partner_transport_track() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    BEGIN
      IF TG_OP IN ('UPDATE', 'DELETE') THEN
        DELETE FROM transport_work WHERE kind IN ('AS2_ENDPOINT', 'SFTP_POLL') AND ref = OLD.endpoint_id;
      END IF;
      IF TG_OP = 'DELETE' THEN
        RETURN OLD;
      END IF;
      IF NEW.kind = 'AS2' THEN
        INSERT INTO transport_work (kind, ref, tenant_id, partner_id) VALUES ('AS2_ENDPOINT', NEW.endpoint_id, NEW.tenant_id, NEW.partner_id);
      ELSIF NEW.kind = 'SFTP' AND NEW.sftp_next_poll_at IS NOT NULL THEN
        INSERT INTO transport_work (kind, ref, tenant_id, partner_id, due_at) VALUES ('SFTP_POLL', NEW.endpoint_id, NEW.tenant_id, NEW.partner_id, NEW.sftp_next_poll_at);
      END IF;
      RETURN NEW;
    END
    $body$;
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
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION edi_as2_endpoint(endpoint uuid) RETURNS TABLE (tenant_id uuid, partner_id uuid)
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT w.tenant_id, w.partner_id FROM transport_work w WHERE w.kind = 'AS2_ENDPOINT' AND w.ref = endpoint $body$;
    CREATE OR REPLACE FUNCTION edi_transport_due_tenants(work text) RETURNS SETOF uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT DISTINCT w.tenant_id FROM transport_work w
        WHERE w.kind = work AND work IN ('SFTP_POLL', 'DELIVERY') AND w.due_at <= pg_catalog.now() $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION edi_partner_transport_track() FROM PUBLIC;
REVOKE ALL ON FUNCTION edi_transport_delivery_track() FROM PUBLIC;
REVOKE ALL ON FUNCTION edi_as2_endpoint(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION edi_transport_due_tenants(text) FROM PUBLIC;
DROP TRIGGER IF EXISTS partner_transport_track ON partner_transport;
CREATE TRIGGER partner_transport_track AFTER INSERT OR UPDATE OR DELETE ON partner_transport
  FOR EACH ROW EXECUTE FUNCTION edi_partner_transport_track();
DROP TRIGGER IF EXISTS transport_delivery_track ON transport_delivery;
CREATE TRIGGER transport_delivery_track AFTER INSERT OR UPDATE OF status, next_attempt_at ON transport_delivery
  FOR EACH ROW EXECUTE FUNCTION edi_transport_delivery_track();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON partner_transport, transport_delivery, transport_inbound TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION edi_as2_endpoint(uuid), edi_transport_due_tenants(text) TO passage_app';
  END IF;
END
$$;
