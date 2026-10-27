-- Media sizes per device and PDF document jobs (FR-32.13). Expand-only: existing
-- devices take their device type's default media, existing jobs are ZPL.

ALTER TABLE print_device ADD COLUMN IF NOT EXISTS media_size text;
UPDATE print_device
   SET media_size = CASE WHEN device_type = 'ZPL_II' THEN '4X6_IN' ELSE 'A4' END
 WHERE media_size IS NULL;
ALTER TABLE print_device ALTER COLUMN media_size SET NOT NULL;
-- Label media (inches) only on ZPL II devices; sheet media only on PDF_IPP and PCL_6.
ALTER TABLE print_device ADD CONSTRAINT print_device_media_size_check CHECK (
  (device_type = 'ZPL_II' AND media_size IN ('2X1_IN', '3X1_IN', '4X2_IN', '4X6_IN'))
  OR (device_type IN ('PDF_IPP', 'PCL_6') AND media_size IN ('A4', 'LETTER'))
);

-- A PDF job keeps its document bytes in `document` (PDFs are binary, which a text
-- column cannot hold) and an empty `zpl`; a ZPL job keeps no document.
ALTER TABLE print_job ADD COLUMN IF NOT EXISTS payload_format text NOT NULL DEFAULT 'ZPL';
ALTER TABLE print_job ADD COLUMN IF NOT EXISTS document bytea;
ALTER TABLE print_job ADD CONSTRAINT print_job_payload_check CHECK (
  (payload_format = 'ZPL' AND document IS NULL)
  OR (payload_format = 'PDF' AND document IS NOT NULL AND zpl = '')
);
