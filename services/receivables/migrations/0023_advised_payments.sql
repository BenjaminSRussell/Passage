-- 30-4: a payment recorded from a remittance advice whose bank credit has not arrived. It is ADVISED: no cash is booked,
-- nothing can be matched or applied to it, and it appears on no statement, until the bank credit (a statement entry, a
-- lockbox check or a manual payment with the advice's reference, the same currency and the same amount) arrives; that payment
-- takes the advice's references and this one becomes CONFIRMED, pointing at it.
ALTER TABLE payment DROP CONSTRAINT IF EXISTS payment_status_check;
ALTER TABLE payment ADD CONSTRAINT payment_status_check
  CHECK (status IN ('RECEIVED', 'SUGGESTED', 'APPLIED', 'ON_ACCOUNT', 'REVERSED', 'ADVISED', 'CONFIRMED'));
ALTER TABLE payment ADD COLUMN IF NOT EXISTS confirmed_by_payment_id uuid;
