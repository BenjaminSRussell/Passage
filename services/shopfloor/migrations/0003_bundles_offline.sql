-- PSG-25 slice 3: bundle split (BR-25.1) and offline clock correction (BR-25.7).
-- clock_offset_s records the device clock offset (server minus device, seconds)
-- applied to a registration's times when the device was more than 5 minutes off.

ALTER TABLE time_registration ADD COLUMN IF NOT EXISTS clock_offset_s bigint;

-- Reads and the one-batch guard of a bundle look registrations up by bundle.
CREATE INDEX IF NOT EXISTS time_registration_bundle ON time_registration (tenant_id, bundle_id)
  WHERE bundle_id IS NOT NULL;
