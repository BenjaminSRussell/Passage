-- PSG-35 slice 3: message monitor and partner SLA dashboard (FR-35.10, §6.35.14).

-- When the partner's 997 was matched to an outbound interchange. The SLA
-- dashboard measures ack latency as acked_at - received_at (the interchange is
-- recorded when it is generated; transmission is not built yet).
ALTER TABLE interchange ADD COLUMN IF NOT EXISTS acked_at timestamptz;

-- Message monitor list: newest first with a (created_at, id) keyset cursor.
CREATE INDEX IF NOT EXISTS message_created ON message (tenant_id, created_at DESC, id DESC);

-- SLA dashboard: outbound interchanges per partner in a window.
CREATE INDEX IF NOT EXISTS interchange_partner_received ON interchange (tenant_id, partner_id, direction, received_at DESC);
