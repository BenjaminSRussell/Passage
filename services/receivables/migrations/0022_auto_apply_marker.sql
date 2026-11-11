-- 30-4: the scheduled auto-apply job takes unapplied payments oldest-attempt first, so a payment the matcher only suggests
-- does not use up every run's batch. auto_apply_at is when the job last considered the payment.
ALTER TABLE payment ADD COLUMN IF NOT EXISTS auto_apply_at timestamptz;
CREATE INDEX IF NOT EXISTS payment_auto_apply_due ON payment (tenant_id, legal_entity_id, auto_apply_at NULLS FIRST, received_date)
  WHERE status IN ('RECEIVED', 'SUGGESTED');
