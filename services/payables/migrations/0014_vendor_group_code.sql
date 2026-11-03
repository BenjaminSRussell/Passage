-- Short vendor group code of an open item (issue #421): the party vendor group
-- code that receiving posted as the VENDGRP ledger dimension of the invoice's
-- vendor-control credit. NULL for items consumed before the code existed; they
-- compare against the ledger lines that carry no VENDGRP value. ap_open_item is
-- already granted SELECT, INSERT, UPDATE to passage_app (0002).
ALTER TABLE ap_open_item ADD COLUMN IF NOT EXISTS vendor_group_code text CHECK (vendor_group_code IS NULL OR vendor_group_code ~ '^[A-Z0-9_-]{1,10}$');
